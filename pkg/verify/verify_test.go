package verify

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

var now = time.Date(2030, 1, 10, 12, 0, 0, 0, time.UTC)

type fake struct {
	cluster *unstructured.Unstructured
	secrets map[string]*corev1.Secret
}

func (f fake) Cluster(context.Context, string, string) (*unstructured.Unstructured, error) {
	return f.cluster, nil
}

func (f fake) Secret(_ context.Context, _, name string) (*corev1.Secret, error) {
	if s, ok := f.secrets[name]; ok {
		return s, nil
	}
	return nil, fmt.Errorf("%w: %s", ErrNotFound, name)
}

func certPEM(t *testing.T, notAfter time.Time) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "example"},
		NotBefore: notAfter.Add(-time.Hour * 24 * 90), NotAfter: notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func secret(name string, reload bool, data map[string][]byte) *corev1.Secret {
	s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name}, Data: data}
	if reload {
		s.Labels = map[string]string{ReloadLabel: "true"}
	}
	return s
}

func healthy() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{
			"instances": int64(3),
			"certificates": map[string]any{
				"serverTLSSecret": "pg-server-tls", "serverCASecret": "pg-server-ca",
				"clientCASecret": "pg-client-ca", "replicationTLSSecret": "pg-replication",
			},
			"postgresql": map[string]any{
				"pg_hba":   []any{"hostssl all people all cert map=people", "hostssl all all all cert"},
				"pg_ident": []any{"people /^(.*)@example\\.com$ \\1"},
			},
		},
		"status": map[string]any{
			"phase": healthyPhase, "readyInstances": int64(3), "currentPrimary": "pg-1",
			"lastSuccessfulBackup":     now.Add(-2 * time.Hour).Format(time.RFC3339),
			"firstRecoverabilityPoint": now.Add(-48 * time.Hour).Format(time.RFC3339),
			"conditions":               []any{map[string]any{"type": "ContinuousArchiving", "status": "True"}},
		},
	}}
}

func goodSecrets(t *testing.T) map[string]*corev1.Secret {
	ok := certPEM(t, now.Add(90*24*time.Hour))
	return map[string]*corev1.Secret{
		"pg-server-tls":  secret("pg-server-tls", true, map[string][]byte{"tls.crt": ok}),
		"pg-server-ca":   secret("pg-server-ca", true, map[string][]byte{"ca.crt": ok}),
		"pg-client-ca":   secret("pg-client-ca", true, map[string][]byte{"ca.crt": ok}),
		"pg-replication": secret("pg-replication", true, map[string][]byte{"tls.crt": ok}),
	}
}

func run(t *testing.T, f fake) Report {
	t.Helper()
	rep, err := Run(context.Background(), f, Options{Namespace: "db", Cluster: "pg", Now: now})
	require.NoError(t, err)
	return rep
}

func failures(r Report) []string {
	var out []string
	for _, x := range r.Results {
		if x.Status == Fail {
			out = append(out, x.Name)
		}
	}
	return out
}

func TestHealthyClusterPasses(t *testing.T) {
	rep := run(t, fake{healthy(), goodSecrets(t)})
	assert.Empty(t, failures(rep), "%+v", rep.Results)
	assert.False(t, rep.Failed())
}

func TestEachFailureIsNamed(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(c *unstructured.Unstructured, s map[string]*corev1.Secret)
		want   string
	}{
		{"degraded", func(c *unstructured.Unstructured, _ map[string]*corev1.Secret) {
			require.NoError(t, unstructured.SetNestedField(c.Object, "Setting up primary", "status", "phase"))
		}, "cluster phase healthy"},
		{"replica missing", func(c *unstructured.Unstructured, _ map[string]*corev1.Secret) {
			require.NoError(t, unstructured.SetNestedField(c.Object, int64(2), "status", "readyInstances"))
		}, "instances ready"},
		{"no primary", func(c *unstructured.Unstructured, _ map[string]*corev1.Secret) {
			require.NoError(t, unstructured.SetNestedField(c.Object, "", "status", "currentPrimary"))
		}, "primary known"},
		{"archiving false", func(c *unstructured.Unstructured, _ map[string]*corev1.Secret) {
			require.NoError(t, unstructured.SetNestedSlice(c.Object,
				[]any{map[string]any{"type": "ContinuousArchiving", "status": "False", "message": "boom"}}, "status", "conditions"))
		}, "continuous archiving"},
		{"stale backup", func(c *unstructured.Unstructured, _ map[string]*corev1.Secret) {
			require.NoError(t, unstructured.SetNestedField(c.Object, now.Add(-72*time.Hour).Format(time.RFC3339), "status", "lastSuccessfulBackup"))
		}, "last backup recent"},
		{"no backup", func(c *unstructured.Unstructured, _ map[string]*corev1.Secret) {
			unstructured.RemoveNestedField(c.Object, "status", "lastSuccessfulBackup")
		}, "last backup recent"},
		{"server cert unlabelled", func(_ *unstructured.Unstructured, s map[string]*corev1.Secret) {
			s["pg-server-tls"].Labels = nil
		}, "TLS server certificate reload label"},
		{"server cert expiring", func(t2 *unstructured.Unstructured, s map[string]*corev1.Secret) {
			s["pg-server-tls"].Data["tls.crt"] = certPEM(t, now.Add(24*time.Hour))
		}, "TLS server certificate validity"},
		{"client ca unlabelled", func(_ *unstructured.Unstructured, s map[string]*corev1.Secret) {
			s["pg-client-ca"].Labels = nil
		}, "TLS client CA reload label"},
		{"trust", func(c *unstructured.Unstructured, _ map[string]*corev1.Secret) {
			require.NoError(t, unstructured.SetNestedStringSlice(c.Object,
				[]string{"host all all 10.0.0.0/8 trust", "hostssl all all all cert"}, "spec", "postgresql", "pg_hba"))
		}, "pg_hba has no trust"},
		{"map without rows", func(c *unstructured.Unstructured, _ map[string]*corev1.Secret) {
			unstructured.RemoveNestedField(c.Object, "spec", "postgresql", "pg_ident")
		}, "pg_hba maps have pg_ident rows"},
		{"catch-all first", func(c *unstructured.Unstructured, _ map[string]*corev1.Secret) {
			require.NoError(t, unstructured.SetNestedStringSlice(c.Object,
				[]string{"hostssl all all all cert", "hostssl all people all cert map=people"}, "spec", "postgresql", "pg_hba"))
		}, "pg_hba catch-all is last"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, s := healthy(), goodSecrets(t)
			tc.mutate(c, s)
			rep := run(t, fake{c, s})
			assert.Contains(t, failures(rep), tc.want)
			assert.True(t, rep.Failed())
		})
	}
}

func TestUserSecretsNeedReloadLabel(t *testing.T) {
	c, s := healthy(), goodSecrets(t)
	require.NoError(t, unstructured.SetNestedField(c.Object, "pg-super", "spec", "superuserSecret", "name"))
	require.NoError(t, unstructured.SetNestedSlice(c.Object, []any{
		map[string]any{"name": "app", "passwordSecret": map[string]any{"name": "pg-app-pw"}},
	}, "spec", "managed", "roles"))
	s["pg-super"] = secret("pg-super", false, nil)
	s["pg-app-pw"] = secret("pg-app-pw", true, nil)
	rep := run(t, fake{c, s})
	assert.Equal(t, []string{"Secret pg-super reload label"}, failures(rep))

	delete(s, "pg-app-pw")
	assert.Contains(t, failures(run(t, fake{c, s})), "Secret pg-app-pw reload label")
}

func TestOptionalSecretsAreSkippedWhenAbsent(t *testing.T) {
	c := healthy()
	unstructured.RemoveNestedField(c.Object, "spec", "certificates")
	rep := run(t, fake{c, map[string]*corev1.Secret{}})
	assert.Empty(t, failures(rep), "%+v", rep.Results)
	var skipped int
	for _, x := range rep.Results {
		if x.Status == Skip {
			skipped++
		}
	}
	assert.GreaterOrEqual(t, skipped, 4)
}

func TestContractNamesAreCheckedWhenPresent(t *testing.T) {
	c := healthy()
	unstructured.RemoveNestedField(c.Object, "spec", "certificates")
	s := map[string]*corev1.Secret{"pg-client-ca": secret("pg-client-ca", false, map[string][]byte{"ca.crt": certPEM(t, now.Add(90*24*time.Hour))})}
	assert.Contains(t, failures(run(t, fake{c, s})), "TLS client CA reload label")
}

func TestCABundleUsesLongestLived(t *testing.T) {
	c, s := healthy(), goodSecrets(t)
	bundle := append(certPEM(t, now.Add(24*time.Hour)), certPEM(t, now.Add(400*24*time.Hour))...)
	s["pg-client-ca"].Data["ca.crt"] = bundle
	assert.Empty(t, failures(run(t, fake{c, s})))
}

func TestTextAndJSON(t *testing.T) {
	rep := run(t, fake{healthy(), goodSecrets(t)})
	var b strings.Builder
	WriteText(&b, rep)
	assert.Contains(t, b.String(), "PASS  cluster phase healthy")
	assert.Contains(t, b.String(), "0 failed")
	b.Reset()
	require.NoError(t, WriteJSON(&b, rep))
	assert.Contains(t, b.String(), `"status": "PASS"`)
}

func TestRequiresNames(t *testing.T) {
	_, err := Run(context.Background(), fake{}, Options{})
	assert.Error(t, err)
}
