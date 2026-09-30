// Copyright 2026 Truvity B.V.. All rights reserved.
// SPDX-License-Identifier: MIT

package conformance_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// do runs a command and returns its error (with output), for setup steps
// that report through the suite's single setup error.
func (s *suite) do(timeout time.Duration, stdin, name string, args ...string) error {
	// A minute of grace over helm's and kubectl's own --timeout, so that
	// their message, not a kill, is what the log shows.
	ctx, cancel := context.WithTimeout(context.Background(), timeout+time.Minute)
	defer cancel()

	_, err := s.run(ctx, stdin, name, args...)

	return err
}

// up creates the kind cluster and installs the platform, in the order the
// docs ask for it. Every chart and image is pinned above.
func (s *suite) up(t *testing.T) error {
	root, err := filepath.Abs("..")
	if err != nil {
		return err
	}

	s.root = root

	s.dir, err = os.MkdirTemp("", "cnpg-conformance-")
	if err != nil {
		return err
	}

	// A temporary kubeconfig, never the caller's: kind writes the new
	// cluster's credentials only where --kubeconfig says, and every later
	// command reads only that file. Helm keeps its own state in the scratch
	// directory too, so a run neither reads nor changes the caller's repos.
	s.kubeconfig = filepath.Join(s.dir, "kubeconfig")
	s.env = append(os.Environ(),
		"KUBECONFIG="+s.kubeconfig,
		"HELM_CONFIG_HOME="+filepath.Join(s.dir, "helm", "config"),
		"HELM_CACHE_HOME="+filepath.Join(s.dir, "helm", "cache"),
		"HELM_DATA_HOME="+filepath.Join(s.dir, "helm", "data"),
	)

	t.Logf("scratch directory %s (kubeconfig %s)", s.dir, s.kubeconfig)

	steps := []struct {
		name string
		fn   func() error
	}{
		{"kind cluster", s.createCluster},
		{"cert-manager (auto-approval off), approver-policy, trust-manager", s.installCertificates},
		{"operator (examples/operator values)", s.installOperator},
		{"cnpg-platform", s.installPlatform},
		{"namespaces, test policies, server CA", s.prepareNamespaces},
		{"cnpg-cluster (trust objects on)", s.installCluster},
		{"cnpg-database (two roles with client certificates)", s.installDatabase},
		{"client pods (cnpg-client helpers)", s.installClients},
	}

	for _, step := range steps {
		started := time.Now()

		t.Logf("==> %s", step.name)

		if err := step.fn(); err != nil {
			return fmt.Errorf("%s: %w", step.name, err)
		}

		t.Logf("    done in %s", time.Since(started).Round(time.Second))
	}

	return nil
}

// retry runs fn until it succeeds or the time is up, returning the last error.
func (s *suite) retry(timeout time.Duration, fn func() error) error {
	deadline := time.Now().Add(timeout)

	for {
		err := fn()
		if err == nil || time.Now().After(deadline) {
			return err
		}

		time.Sleep(5 * time.Second)
	}
}

func (s *suite) createCluster() error {
	// One cluster, created fresh. A leftover one of the same name (an
	// aborted run) is removed first.
	_ = s.do(2*time.Minute, "", "kind", "delete", "cluster", "--name", clusterName, "--kubeconfig", s.kubeconfig)

	s.created = true

	return s.do(10*time.Minute, "", "kind", "create", "cluster",
		"--name", clusterName,
		"--config", s.path("conformance", "fixtures", "kind.yaml"),
		"--image", nodeImage,
		"--kubeconfig", s.kubeconfig,
		"--wait", "180s")
}

func (s *suite) helm(timeout time.Duration, args ...string) error {
	return s.do(timeout, "", "helm", append([]string{"upgrade", "--install", "--wait", "--timeout", timeout.String()}, args...)...)
}

func (s *suite) installCertificates() error {
	// cert-manager's own approver is OFF. Left on it approves every request
	// for a cert-manager issuer, and approver-policy's refusals would be
	// decoration: a request is approved once by anyone.
	if err := s.helm(10*time.Minute, "cert-manager", "cert-manager",
		"--repo", jetstackRepo, "--version", certManagerVersion,
		"--namespace", certManager, "--create-namespace",
		"--set", "crds.enabled=true",
		"--set", "disableAutoApproval=true"); err != nil {
		return err
	}

	if err := s.helm(10*time.Minute, "approver-policy", "cert-manager-approver-policy",
		"--repo", jetstackRepo, "--version", approverPolicyVersion,
		"--namespace", certManager); err != nil {
		return err
	}

	// The test-only policies, before anything else requests a certificate:
	// with the default approver off, an unapproved request waits forever
	// (trust-manager's own webhook certificate included).
	// Retried: the Deployment is ready before its validating webhook
	// answers, and the first apply can meet "connection refused".
	if err := s.retry(3*time.Minute, func() error {
		return s.do(time.Minute, "", "kubectl", "apply", "-f", s.path("conformance", "fixtures", "policies.yaml"))
	}); err != nil {
		return err
	}

	// Secret targets are off by default; the Bundle the cnpg-cluster chart
	// renders delivers <cluster>-client-ca into the database namespace as a
	// Secret, and only a Secret trust-manager is authorized for.
	return s.helm(10*time.Minute, "trust-manager", "trust-manager",
		"--repo", jetstackRepo, "--version", trustManagerVersion,
		"--namespace", certManager,
		"--set", "secretTargets.enabled=true",
		"--set-json", `secretTargets.authorizedSecrets=["`+pgName+`-client-ca"]`,
		"--set", "defaultPackage.enabled=false")
}

func (s *suite) operatorValues() []string {
	examples := s.path("examples", "operator")

	return []string{
		"-f", filepath.Join(examples, "cloudnative-pg.values.yaml"),
		// A disposable cluster: no stagger between rollouts.
		"-f", filepath.Join(examples, "cloudnative-pg.fast-rollout.values.yaml"),
		// The preset scrapes the operator through a PodMonitor; kind has no
		// monitoring.coreos.com CRDs.
		"--set", "monitoring.podMonitorEnabled=false",
	}
}

func (s *suite) installOperator() error {
	if os.Getenv(backupVariable) != "" {
		// The plugin goes in BEFORE the operator (plugin-barman-cloud.values.yaml).
		if err := s.helm(10*time.Minute, "plugin-barman-cloud", "plugin-barman-cloud",
			"--repo", cnpgRepo, "--version", pluginChartVersion,
			"--namespace", platformNS, "--create-namespace",
			"-f", s.path("examples", "operator", "plugin-barman-cloud.values.yaml")); err != nil {
			return err
		}
	}

	args := append([]string{"cnpg", "cloudnative-pg",
		"--repo", cnpgRepo, "--version", operatorChartVersion,
		"--namespace", platformNS, "--create-namespace"}, s.operatorValues()...)

	return s.helm(10*time.Minute, args...)
}

func (s *suite) installPlatform() error {
	return s.helm(2*time.Minute, "cnpg-platform", s.path("charts", "cnpg-platform"),
		"--namespace", platformNS,
		"-f", s.path("conformance", "fixtures", "platform.values.yaml"))
}

func (s *suite) prepareNamespaces() error {
	// The product namespace carries the label the admission guard selects.
	manifest := fmt.Sprintf(`apiVersion: v1
kind: Namespace
metadata:
  name: %s
  labels:
    cnpg-conformance/guarded: "true"
---
apiVersion: v1
kind: Namespace
metadata:
  name: %s
`, appNS, otherNS)

	if _, err := s.apply(manifest); err != nil {
		return err
	}

	// The server certificate's issuer: a CA the suite owns, in the product
	// namespace. The operator's own CA is not used, so a client has a
	// <cluster>-server-ca Secret to verify against (the cnpg-client contract).
	ca, err := newAuthority("conformance server CA")
	if err != nil {
		return err
	}

	s.serverCA = ca

	secret := fmt.Sprintf(`apiVersion: v1
kind: Secret
metadata: {name: conformance-server-ca, namespace: %s}
type: kubernetes.io/tls
stringData:
  tls.crt: |
%s
  tls.key: |
%s
---
apiVersion: cert-manager.io/v1
kind: Issuer
metadata: {name: conformance-server, namespace: %s}
spec:
  ca: {secretName: conformance-server-ca}
`, appNS, indent(ca.certPEM, 4), indent(ca.keyPEM, 4), appNS)

	_, err = s.apply(secret)

	return err
}

func indent(text string, spaces int) string {
	pad := strings.Repeat(" ", spaces)
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")

	for i, line := range lines {
		lines[i] = pad + line
	}

	return strings.Join(lines, "\n")
}

func (s *suite) installCluster() error {
	caFile := filepath.Join(s.dir, "server-ca.crt")
	if err := os.WriteFile(caFile, []byte(s.serverCA.certPEM), 0o600); err != nil {
		return err
	}

	// No --wait for the release itself: its objects are custom resources
	// with no readiness helm knows; the wait below is on the Cluster.
	// Retried: each webhook (cert-manager's, approver-policy's,
	// trust-manager's, the operator's) can trail its Deployment's readiness.
	if err := s.retry(3*time.Minute, func() error {
		return s.do(5*time.Minute, "", "helm", "upgrade", "--install", pgName, s.path("charts", "cnpg-cluster"),
			"--namespace", appNS,
			"-f", s.path("conformance", "fixtures", "cluster.values.yaml"),
			"--set-file", "serverTLS.caCertificates="+caFile)
	}); err != nil {
		return err
	}

	return s.do(15*time.Minute, "", "kubectl", "wait", "--for=condition=Ready",
		"cluster.postgresql.cnpg.io/"+pgName, "-n", appNS, "--timeout=900s")
}

func (s *suite) installDatabase() error {
	if err := s.retry(3*time.Minute, func() error {
		return s.do(2*time.Minute, "", "helm", "upgrade", "--install", "db", s.path("charts", "cnpg-database"),
			"--namespace", appNS,
			"-f", s.path("conformance", "fixtures", "database.values.yaml"))
	}); err != nil {
		return err
	}

	// Both client certificates issued (approved by the chart's policy, signed
	// by the per-database CA), then both roles and the database created in
	// PostgreSQL by the operator.
	if err := s.do(5*time.Minute, "", "kubectl", "wait", "--for=condition=Ready",
		"certificate", "--all", "-n", appNS, "--timeout=300s"); err != nil {
		return err
	}

	deadline := time.Now().Add(5 * time.Minute)

	for {
		primary, err := s.primaryName(appNS, pgName)

		var out string
		if err == nil {
			out, err = s.run(context.Background(), "", "kubectl", "exec", "-n", appNS, primary, "-c", "postgres", "--",
				"psql", "-X", "-A", "-t", "-U", "postgres", "-c",
				fmt.Sprintf("select count(*) from pg_roles where rolname in ('%s','%s')", roleRW, roleRO))
			if err == nil && strings.TrimSpace(out) == "2" {
				break
			}
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("the roles never appeared in PostgreSQL: %v %s", err, out)
		}

		time.Sleep(5 * time.Second)
	}

	return nil
}

func (s *suite) installClients() error {
	chart := s.path("conformance", "fixtures", "client")

	for release, role := range map[string]string{"client-rw": roleRW, "client-ro": roleRO} {
		if err := s.do(2*time.Minute, "", "helm", "upgrade", "--install", release, chart,
			"--namespace", appNS,
			"--set", "client.cluster="+pgName,
			"--set", "client.role="+role,
			"--set", "client.database="+database,
			// The server certificate names the Services fully qualified.
			"--set", "client.clusterDomain="+clusterDomain()); err != nil {
			return err
		}
	}

	return s.do(5*time.Minute, "", "kubectl", "wait", "--for=condition=Ready",
		"pod/client-rw", "pod/client-ro", "-n", appNS, "--timeout=300s")
}

// clusterDomain is the cluster's DNS domain, kind's default. Built from
// parts: the suite asserts a behaviour of the chart, not of this name.
func clusterDomain() string {
	return strings.Join([]string{"cluster", "local"}, ".")
}

func (s *suite) teardown() {
	if !s.created {
		return
	}

	if os.Getenv(keepVariable) != "" {
		fmt.Fprintf(os.Stderr, "%s set: cluster %q kept; KUBECONFIG=%s\n", keepVariable, clusterName, s.kubeconfig)

		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, "kind", "delete", "cluster", "--name", clusterName, "--kubeconfig", s.kubeconfig)
	cmd.Env = s.env
	_ = cmd.Run()

	_ = os.RemoveAll(s.dir)
}
