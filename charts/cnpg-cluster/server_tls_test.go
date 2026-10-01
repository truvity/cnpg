// Copyright 2026 Truvity B.V.. All rights reserved.
// SPDX-License-Identifier: MIT

package chart_test

import (
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

const testRoots = "-----BEGIN CERTIFICATE-----\nroot-one\n-----END CERTIFICATE-----\n-----BEGIN CERTIFICATE-----\nroot-two\n-----END CERTIFICATE-----\n"

// renderDocs renders the chart with one values file and returns every
// object by kind.
func renderDocs(t *testing.T, values map[string]any) (map[string][]map[string]any, error) {
	t.Helper()

	raw, err := yaml.Marshal(values)
	require.NoError(t, err)

	file := filepath.Join(t.TempDir(), "values.yaml")
	require.NoError(t, os.WriteFile(file, raw, 0o600))

	out, err := exec.CommandContext(t.Context(), "helm", "template", "pg", ".", "-f", file).CombinedOutput()
	if err != nil {
		return nil, &renderError{out: string(out)}
	}

	byKind := map[string][]map[string]any{}

	for _, doc := range strings.Split(string(out), "\n---") {
		var obj map[string]any
		if err := yaml.Unmarshal([]byte(doc), &obj); err != nil || obj == nil {
			continue
		}

		kind, _ := obj["kind"].(string)
		byKind[kind] = append(byKind[kind], obj)
	}

	return byKind, nil
}

type renderError struct{ out string }

func (e *renderError) Error() string { return e.out }

func tlsValues(serverTLS map[string]any) map[string]any {
	return map[string]any{
		"clusterName": "app-pg",
		"namespace":   "app",
		"profile":     "devel",
		"serverTLS":   serverTLS,
	}
}

func field(obj map[string]any, path ...string) any {
	var cur any = obj
	for _, key := range path {
		m, _ := cur.(map[string]any)
		cur = m[key]
	}

	return cur
}

// TestServerTLS_DefaultIsTheOperatorsCA: with no issuer named, the chart
// renders exactly what it always did -- no spec.certificates, no
// Certificate, no CA Secret -- so the operator keeps minting its own.
func TestServerTLS_DefaultIsTheOperatorsCA(t *testing.T) {
	docs, err := renderDocs(t, tlsValues(map[string]any{}))
	require.NoError(t, err)

	require.Len(t, docs["Cluster"], 1)
	assert.Nil(t, field(docs["Cluster"][0], "spec", "certificates"))
	assert.Empty(t, docs["Certificate"])
	assert.Empty(t, docs["Secret"])
}

// TestServerTLS_FromAnIssuer: naming an issuer moves the SERVER side and
// nothing else. The Certificate names every Service fully qualified, the
// CA Secret carries the roots verbatim, the Cluster points at both, and
// the client side -- client CA, replication certificate, pg_hba -- is
// untouched.
func TestServerTLS_FromAnIssuer(t *testing.T) {
	issuer := map[string]any{"name": "example-issuer", "kind": "ClusterIssuer", "group": "cert-manager.io"}
	key := map[string]any{"algorithm": "ECDSA", "size": 384}

	docs, err := renderDocs(t, tlsValues(map[string]any{
		"issuerRef":      issuer,
		"duration":       "720h",
		"renewBefore":    "240h",
		"privateKey":     key,
		"caCertificates": testRoots,
	}))
	require.NoError(t, err)

	require.Len(t, docs["Cluster"], 1)
	assert.Equal(t, map[string]any{
		"serverTLSSecret": "app-pg-server-tls",
		"serverCASecret":  "app-pg-server-ca",
	}, field(docs["Cluster"][0], "spec", "certificates"), "server side only: no clientCASecret, no replicationTLSSecret")
	assert.Equal(t, []any{"hostssl all all all cert"}, field(docs["Cluster"][0], "spec", "postgresql", "pg_hba"))

	require.Len(t, docs["Certificate"], 1)
	cert := docs["Certificate"][0]
	assert.Equal(t, "app-pg-server-tls", field(cert, "spec", "secretName"))
	assert.Equal(t, []any{
		"app-pg-rw.app.svc.cluster.local",
		"app-pg-ro.app.svc.cluster.local",
		"app-pg-r.app.svc.cluster.local",
	}, field(cert, "spec", "dnsNames"))
	assert.Equal(t, "720h", field(cert, "spec", "duration"))
	assert.Equal(t, "240h", field(cert, "spec", "renewBefore"))
	assert.Equal(t, map[string]any{"algorithm": "ECDSA", "size": float64(384)}, field(cert, "spec", "privateKey"))
	assert.Equal(t, issuer, field(cert, "spec", "issuerRef"))
	assert.Equal(t, map[string]any{"cnpg.io/reload": "true"}, field(cert, "spec", "secretTemplate", "labels"),
		"without the label CloudNativePG does not serve a renewed certificate until the instance restarts")

	require.Len(t, docs["Secret"], 1)
	secret := docs["Secret"][0]
	assert.Equal(t, "app-pg-server-ca", field(secret, "metadata", "name"))
	assert.Equal(t, map[string]any{"cnpg.io/reload": "true"}, field(secret, "metadata", "labels"))

	encoded, _ := field(secret, "data", "ca.crt").(string)
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	require.NoError(t, err)
	assert.Equal(t, testRoots, string(decoded))
}

// TestServerTLS_RequiresTheRoots: an issuer without the roots that verify
// it would leave the operator, replicas and poolers unable to verify the
// server, so the render refuses it.
func TestServerTLS_RequiresTheRoots(t *testing.T) {
	_, err := renderDocs(t, tlsValues(map[string]any{
		"issuerRef": map[string]any{"name": "example-issuer", "kind": "ClusterIssuer"},
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "serverTLS.caCertificates is required")
}

// TestServerTLS_ReloadLabelMergesWithChartLabels: the chart-wide labels
// and the reload label coexist on the CA Secret and the Certificate keeps
// its own labels.
func TestServerTLS_ReloadLabelMergesWithChartLabels(t *testing.T) {
	v := tlsValues(map[string]any{
		"issuerRef":      map[string]any{"name": "example-issuer", "kind": "ClusterIssuer"},
		"caCertificates": testRoots,
	})
	v["labels"] = map[string]any{"app.kubernetes.io/part-of": "app"}

	docs, err := renderDocs(t, v)
	require.NoError(t, err)

	require.Len(t, docs["Secret"], 1)
	assert.Equal(t, map[string]any{
		"app.kubernetes.io/part-of": "app",
		"cnpg.io/reload":            "true",
	}, field(docs["Secret"][0], "metadata", "labels"))

	require.Len(t, docs["Certificate"], 1)
	assert.Equal(t, map[string]any{"app.kubernetes.io/part-of": "app"}, field(docs["Certificate"][0], "metadata", "labels"))
	assert.Equal(t, map[string]any{"cnpg.io/reload": "true"}, field(docs["Certificate"][0], "spec", "secretTemplate", "labels"))
}

// TestServerTLS_ExistingSecretsByName: naming both Secrets points the
// Cluster at them and renders nothing for the server side.
func TestServerTLS_ExistingSecretsByName(t *testing.T) {
	docs, err := renderDocs(t, tlsValues(map[string]any{
		"existingSecret":   "app-db-server-tls",
		"existingCASecret": "app-db-server-ca",
	}))
	require.NoError(t, err)

	require.Len(t, docs["Cluster"], 1)
	assert.Equal(t, map[string]any{
		"serverTLSSecret": "app-db-server-tls",
		"serverCASecret":  "app-db-server-ca",
	}, field(docs["Cluster"][0], "spec", "certificates"))
	assert.Empty(t, docs["Certificate"])
	assert.Empty(t, docs["Issuer"])
	assert.Empty(t, docs["Secret"])
}

// TestServerTLS_ExistingSecretsNeedBoth: one name alone is refused.
func TestServerTLS_ExistingSecretsNeedBoth(t *testing.T) {
	for _, one := range []map[string]any{
		{"existingSecret": "app-db-server-tls"},
		{"existingCASecret": "app-db-server-ca"},
	} {
		_, err := renderDocs(t, tlsValues(one))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "go together")
	}
}

// TestServerTLS_ExistingSecretsExcludeIssuer: the chart's own Certificate
// and names-only mode are mutually exclusive.
func TestServerTLS_ExistingSecretsExcludeIssuer(t *testing.T) {
	_, err := renderDocs(t, tlsValues(map[string]any{
		"issuerRef":        map[string]any{"name": "example-issuer", "kind": "ClusterIssuer"},
		"caCertificates":   testRoots,
		"existingSecret":   "app-db-server-tls",
		"existingCASecret": "app-db-server-ca",
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be combined with serverTLS.issuerRef")
}
