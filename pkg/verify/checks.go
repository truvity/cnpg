package verify

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// healthyPhase is the phase text CloudNativePG reports for a healthy cluster.
const healthyPhase = "Cluster in healthy state"

// ReloadLabel makes CloudNativePG reload a user-provided Secret when it
// changes; without it a rotation is not served until a restart.
const ReloadLabel = "cnpg.io/reload"

func checkHealth(c *unstructured.Unstructured) []Result {
	phase, _, _ := unstructured.NestedString(c.Object, "status", "phase")
	want, _, _ := unstructured.NestedInt64(c.Object, "spec", "instances")
	ready, _, _ := unstructured.NestedInt64(c.Object, "status", "readyInstances")
	primary, _, _ := unstructured.NestedString(c.Object, "status", "currentPrimary")
	return []Result{
		res("cluster phase healthy", phase == healthyPhase,
			fmt.Sprintf("phase %q", phase),
			fmt.Sprintf("phase is %q, want %q", phase, healthyPhase)),
		res("instances ready", want > 0 && ready == want,
			fmt.Sprintf("%d/%d ready", ready, want),
			fmt.Sprintf("%d of %d instances ready", ready, want)),
		res("primary known", primary != "",
			"primary is "+primary,
			"status.currentPrimary is empty"),
	}
}

func condition(c *unstructured.Unstructured, typ string) (status, message string, found bool) {
	conds, _, _ := unstructured.NestedSlice(c.Object, "status", "conditions")
	for _, raw := range conds {
		m, ok := raw.(map[string]any)
		if !ok || m["type"] != typ {
			continue
		}
		s, _ := m["status"].(string)
		msg, _ := m["message"].(string)
		return s, msg, true
	}
	return "", "", false
}

// The Cluster status carries the ContinuousArchiving condition but no
// timestamp of the last archived WAL segment (that lives in
// pg_stat_archiver on the instance), so archiving is asserted by the
// condition and the recoverability window, not by segment age.
func checkArchiving(c *unstructured.Unstructured) []Result {
	st, msg, found := condition(c, "ContinuousArchiving")
	var r Result
	switch {
	case !found:
		r = Result{Name: "continuous archiving", Status: Fail, Message: "no ContinuousArchiving condition on the Cluster"}
	case st != "True":
		r = Result{Name: "continuous archiving", Status: Fail, Message: fmt.Sprintf("ContinuousArchiving is %s: %s", st, msg)}
	default:
		r = Result{Name: "continuous archiving", Status: Pass, Message: "ContinuousArchiving is True"}
	}
	frp, _, _ := unstructured.NestedString(c.Object, "status", "firstRecoverabilityPoint")
	return []Result{r, res("recoverability window", frp != "",
		"first recoverability point "+frp,
		"status.firstRecoverabilityPoint is empty: no base backup is usable for recovery")}
}

func checkBackup(c *unstructured.Unstructured, opt Options) []Result {
	const name = "last backup recent"
	s, _, _ := unstructured.NestedString(c.Object, "status", "lastSuccessfulBackup")
	if s == "" {
		return []Result{{Name: name, Status: Fail, Message: "status.lastSuccessfulBackup is empty: no backup has succeeded"}}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return []Result{{Name: name, Status: Fail, Message: fmt.Sprintf("status.lastSuccessfulBackup %q is not RFC 3339", s)}}
	}
	age := opt.now().Sub(t).Round(time.Second)
	return []Result{res(name, age <= opt.MaxBackupAge,
		fmt.Sprintf("last successful backup %s ago (max %s)", age, opt.MaxBackupAge),
		fmt.Sprintf("last successful backup %s ago, max %s", age, opt.MaxBackupAge))}
}

type secretRef struct {
	label    string // what the Secret is for
	name     string
	optional bool // absent is a SKIP, not a FAIL
	cert     bool // parse and check expiry
	keys     []string
}

func getString(c *unstructured.Unstructured, path ...string) string {
	s, _, _ := unstructured.NestedString(c.Object, path...)
	return s
}

func checkTLS(ctx context.Context, src Source, c *unstructured.Unstructured, opt Options) []Result {
	ns, cl := opt.Namespace, opt.Cluster
	pick := func(set, contract string) (string, bool) {
		if set != "" {
			return set, false
		}
		return contract, true
	}
	serverTLS := getString(c, "spec", "certificates", "serverTLSSecret")
	serverCA, serverCAOpt := pick(getString(c, "spec", "certificates", "serverCASecret"), cl+"-server-ca")
	clientCA, clientCAOpt := pick(getString(c, "spec", "certificates", "clientCASecret"), cl+"-client-ca")
	repl, replOpt := pick(getString(c, "spec", "certificates", "replicationTLSSecret"), cl+"-replication")

	var refs []secretRef
	if serverTLS != "" {
		refs = append(refs, secretRef{label: "server certificate", name: serverTLS, cert: true, keys: []string{"tls.crt"}})
	} else {
		refs = append(refs, secretRef{label: "server certificate", name: "", optional: true})
	}
	refs = append(refs,
		secretRef{label: "server CA", name: serverCA, optional: serverCAOpt, cert: true, keys: []string{"ca.crt"}},
		secretRef{label: "client CA", name: clientCA, optional: clientCAOpt, cert: true, keys: []string{"ca.crt", "tls.crt"}},
		secretRef{label: "replication certificate", name: repl, optional: replOpt, cert: true, keys: []string{"tls.crt"}},
	)
	var out []Result
	for _, ref := range refs {
		out = append(out, checkTLSSecret(ctx, src, ns, ref, opt)...)
	}
	return out
}

func checkTLSSecret(ctx context.Context, src Source, ns string, ref secretRef, opt Options) []Result {
	prefix := "TLS " + ref.label
	if ref.name == "" {
		return []Result{{Name: prefix, Status: Skip, Message: "not set in spec.certificates: the operator manages it"}}
	}
	sec, err := src.Secret(ctx, ns, ref.name)
	if err != nil {
		if errors.Is(err, ErrNotFound) && ref.optional {
			return []Result{{Name: prefix, Status: Skip, Message: fmt.Sprintf("Secret %s not present", ref.name)}}
		}
		return []Result{{Name: prefix, Status: Fail, Message: fmt.Sprintf("Secret %s: %v", ref.name, err)}}
	}
	lbl := sec.Labels[ReloadLabel] == "true"
	out := []Result{res(prefix+" reload label", lbl,
		fmt.Sprintf("Secret %s carries %s=true", ref.name, ReloadLabel),
		fmt.Sprintf("Secret %s lacks %s=true: a renewal is not served until a restart", ref.name, ReloadLabel))}
	out = append(out, expiry(prefix+" validity", sec, ref, opt))
	return out
}

func expiry(name string, sec *corev1.Secret, ref secretRef, opt Options) Result {
	var data []byte
	var key string
	for _, k := range ref.keys {
		if d := sec.Data[k]; len(d) > 0 {
			data, key = d, k
			break
		}
	}
	if data == nil {
		return Result{Name: name, Status: Fail, Message: fmt.Sprintf("Secret %s has none of %s", sec.Name, strings.Join(ref.keys, ", "))}
	}
	certs, err := parseCerts(data)
	if err != nil {
		return Result{Name: name, Status: Fail, Message: fmt.Sprintf("Secret %s key %s: %v", sec.Name, key, err)}
	}
	// A leaf file leads with the leaf; a CA bundle may carry a retired
	// root beside the live one, so a bundle is as good as its longest-lived
	// member.
	notAfter := certs[0].NotAfter
	if key != "tls.crt" {
		for _, c := range certs {
			if c.NotAfter.After(notAfter) {
				notAfter = c.NotAfter
			}
		}
	}
	left := notAfter.Sub(opt.now()).Round(time.Minute)
	return res(name, left >= opt.MinCertValidity,
		fmt.Sprintf("%s valid for %s more (min %s)", sec.Name, left, opt.MinCertValidity),
		fmt.Sprintf("%s has %s of validity left, min %s (notAfter %s)", sec.Name, left, opt.MinCertValidity, notAfter.UTC().Format(time.RFC3339)))
}

func parseCerts(data []byte) ([]*x509.Certificate, error) {
	var out []*x509.Certificate
	for {
		var b *pem.Block
		b, data = pem.Decode(data)
		if b == nil {
			break
		}
		if b.Type != "CERTIFICATE" {
			continue
		}
		c, err := x509.ParseCertificate(b.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse certificate: %w", err)
		}
		out = append(out, c)
	}
	if len(out) == 0 {
		return nil, errors.New("no PEM certificate")
	}
	return out, nil
}

// userSecrets lists every Secret the Cluster spec names, keyed by name.
// The operator mints the rest (-app, -ca, -server ...) and labels them.
func userSecrets(c *unstructured.Unstructured) map[string]string {
	out := map[string]string{}
	add := func(why string, path ...string) {
		if n := getString(c, path...); n != "" {
			out[n] = why
		}
	}
	add("spec.certificates.serverTLSSecret", "spec", "certificates", "serverTLSSecret")
	add("spec.certificates.serverCASecret", "spec", "certificates", "serverCASecret")
	add("spec.certificates.clientCASecret", "spec", "certificates", "clientCASecret")
	add("spec.certificates.replicationTLSSecret", "spec", "certificates", "replicationTLSSecret")
	add("spec.superuserSecret", "spec", "superuserSecret", "name")
	add("spec.bootstrap.initdb.secret", "spec", "bootstrap", "initdb", "secret", "name")
	roles, _, _ := unstructured.NestedSlice(c.Object, "spec", "managed", "roles")
	for _, r := range roles {
		m, _ := r.(map[string]any)
		ps, _ := m["passwordSecret"].(map[string]any)
		if n, _ := ps["name"].(string); n != "" {
			out[n] = "spec.managed.roles passwordSecret"
		}
	}
	ext, _, _ := unstructured.NestedSlice(c.Object, "spec", "externalClusters")
	for _, e := range ext {
		m, _ := e.(map[string]any)
		for _, k := range []string{"password", "sslKey", "sslCert", "sslRootCert"} {
			s, _ := m[k].(map[string]any)
			if n, _ := s["name"].(string); n != "" {
				out[n] = "spec.externalClusters " + k
			}
		}
	}
	return out
}

func checkReloadLabels(ctx context.Context, src Source, c *unstructured.Unstructured, opt Options) []Result {
	refs := userSecrets(c)
	if len(refs) == 0 {
		return []Result{{Name: "user-provided Secrets labelled for reload", Status: Skip, Message: "the Cluster references no user-provided Secret"}}
	}
	names := make([]string, 0, len(refs))
	for n := range refs {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []Result
	for _, n := range names {
		name := fmt.Sprintf("Secret %s reload label", n)
		sec, err := src.Secret(ctx, opt.Namespace, n)
		if err != nil {
			out = append(out, Result{Name: name, Status: Fail, Message: fmt.Sprintf("referenced by %s: %v", refs[n], err)})
			continue
		}
		out = append(out, res(name, sec.Labels[ReloadLabel] == "true",
			fmt.Sprintf("referenced by %s, carries %s=true", refs[n], ReloadLabel),
			fmt.Sprintf("referenced by %s but lacks %s=true", refs[n], ReloadLabel)))
	}
	return out
}
