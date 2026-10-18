// Copyright 2026 Truvity B.V.. All rights reserved.
// SPDX-License-Identifier: MIT

// Package chart_test holds cnpg-projects to its two promises: a cluster
// entry renders exactly the objects the cnpg-cluster chart renders for the
// same values (the templates are carried over by hack/sync-cnpg-projects.sh,
// so this is the check that the carry is faithful), and the archive roles
// have the trust and the permissions the platform has always minted.
package chart_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

func objects(t *testing.T, chart string, values string) map[string]map[string]any {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), "helm", "template", "x", chart, "--namespace", "app",
		"--api-versions", "monitoring.coreos.com/v1/PodMonitor", "-f", "-")
	cmd.Stdin = strings.NewReader(values)

	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "helm template %s failed:\n%s", chart, out)

	objs := map[string]map[string]any{}

	for _, d := range strings.Split(string(out), "\n---") {
		var m map[string]any
		if yaml.Unmarshal([]byte(d), &m) != nil || len(m) == 0 {
			continue
		}

		objs[m["kind"].(string)+"/"+m["metadata"].(map[string]any)["name"].(string)] = m
	}

	return objs
}

func TestClusterEntryIsWhatCnpgClusterRenders(t *testing.T) {
	cases, err := filepath.Glob("../../tests/cases/cnpg-cluster/*/values.yaml")
	require.NoError(t, err)
	require.NotEmpty(t, cases)

	for _, path := range cases {
		t.Run(filepath.Base(filepath.Dir(path)), func(t *testing.T) {
			raw, err := os.ReadFile(path)
			require.NoError(t, err)

			var values map[string]any
			require.NoError(t, yaml.Unmarshal(raw, &values))

			wrapped, err := yaml.Marshal(map[string]any{"clusters": []any{map[string]any{"values": values}}})
			require.NoError(t, err)

			assert.Equal(t, objects(t, "../cnpg-cluster", string(raw)), objects(t, ".", string(wrapped)))
		})
	}
}

func TestArchiveRolesAreInTheControllersNamespace(t *testing.T) {
	raw, err := os.ReadFile("../../tests/cases/cnpg-projects/full/values.yaml")
	require.NoError(t, err)

	objs := objects(t, ".", string(raw))

	role := objs["Role/example-pg-backup-alpha"]
	require.NotNil(t, role)
	assert.Equal(t, "platform-system", role["metadata"].(map[string]any)["namespace"])
	assert.Equal(t, "adopt-or-create", role["metadata"].(map[string]any)["annotations"].(map[string]any)["services.k8s.aws/adoption-policy"])

	spec := role["spec"].(map[string]any)
	assert.Contains(t, spec["assumeRolePolicyDocument"], "pods.eks.amazonaws.com")
	assert.Contains(t, spec["assumeRolePolicyDocument"], `["alpha-pg","alpha-rehearsal-pg"]`)
	assert.Contains(t, spec["inlinePolicies"].(map[string]any)["cnpg-backup"], "arn:aws:s3:::example-backups/alpha/alpha-pg/*")

	for _, name := range []string{"alpha-pg", "alpha-rehearsal-pg"} {
		pia := objs["PodIdentityAssociation/example-pg-backup-alpha-"+name]
		require.NotNil(t, pia)
		assert.Equal(t, "alpha", pia["spec"].(map[string]any)["namespace"], "the ServiceAccount is the project's")
		assert.Equal(t, "platform-system", pia["metadata"].(map[string]any)["namespace"])
		assert.Equal(t, "adopt-or-create", pia["metadata"].(map[string]any)["annotations"].(map[string]any)["services.k8s.aws/adoption-policy"])
	}
}

// TestScheduledBackupImmediate: the first base backup is taken on creation
// by default; the operator ignores the field once the ScheduledBackup has
// run. `immediate: false` on an entry leaves it out.
func TestScheduledBackupImmediate(t *testing.T) {
	objs := objects(t, ".", `
scheduledBackups:
  - {name: a, namespace: app, clusterName: app-pg, objectStoreName: o}
  - {name: b, namespace: app, clusterName: app-pg, objectStoreName: o, immediate: false}
`)
	spec := func(n string) map[string]any { return objs["ScheduledBackup/"+n]["spec"].(map[string]any) }
	assert.Equal(t, true, spec("a")["immediate"])
	assert.NotContains(t, spec("b"), "immediate")
}

const rootPEM = "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----"

func annotations(o map[string]any) map[string]any {
	a, _ := o["metadata"].(map[string]any)["annotations"].(map[string]any)

	return a
}

// TestPlatformDefaults: an entry that names only its Cluster gets the
// ScheduledBackup and ObjectStore names of the cnpg-cluster convention, the
// shared TLS settings, the sync waves and the CA Secret guard; the entry's
// own values win, and an empty wave or guardCASecret false render none.
func TestPlatformDefaults(t *testing.T) {
	values := `
serverTLSDefaults:
  issuerRef: {name: issuer, kind: ClusterIssuer}
  caCertificates: ["` + strings.ReplaceAll(rootPEM, "\n", `\n`) + `"]
  duration: 720h
  labels: {team: data}
scheduledBackups:
  - {namespace: app, clusterName: app-pg}
  - {name: custom, namespace: app, clusterName: other-pg, objectStoreName: o, annotations: {argocd.argoproj.io/sync-wave: "3"}}
serverTLS:
  - {clusterName: app-pg, namespace: app}
  - {clusterName: other-pg, namespace: app, certificateName: other-pg-server, duration: 1h}
`
	objs := objects(t, ".", values)

	sb := objs["ScheduledBackup/app-pg-scheduled-backup"]
	require.NotNil(t, sb)
	assert.Equal(t, "app-pg-objectstore", sb["spec"].(map[string]any)["pluginConfiguration"].(map[string]any)["parameters"].(map[string]any)["barmanObjectName"])
	assert.Equal(t, "25", annotations(sb)["argocd.argoproj.io/sync-wave"])
	assert.Equal(t, "3", annotations(objs["ScheduledBackup/custom"])["argocd.argoproj.io/sync-wave"], "the entry's annotation wins")

	cert := objs["Certificate/app-pg-server-tls"]
	require.NotNil(t, cert)
	assert.Equal(t, "-1", annotations(cert)["argocd.argoproj.io/sync-wave"])
	assert.Equal(t, "720h", cert["spec"].(map[string]any)["duration"])
	assert.Equal(t, "data", cert["metadata"].(map[string]any)["labels"].(map[string]any)["team"])

	ca := objs["Secret/app-pg-server-ca"]
	require.NotNil(t, ca)
	assert.Equal(t, "Prune=false,Delete=false", annotations(ca)["argocd.argoproj.io/sync-options"])
	assert.NotContains(t, annotations(cert), "argocd.argoproj.io/sync-options", "the guard is on the CA Secret alone")

	assert.Equal(t, "1h", objs["Certificate/other-pg-server"]["spec"].(map[string]any)["duration"], "the entry wins over the defaults")

	off := objects(t, ".", values+`
argocd: {scheduledBackupSyncWave: "", serverTLSSyncWave: "", guardCASecret: false}
`)
	assert.Nil(t, annotations(off["ScheduledBackup/app-pg-scheduled-backup"]))
	assert.Nil(t, annotations(off["Certificate/app-pg-server-tls"]))
	assert.Nil(t, annotations(off["Secret/app-pg-server-ca"]))
}

// TestBackupCutoverOptions: the options a move to a new backup bucket needs
// render in a cluster entry as in cnpg-cluster (the glob test above compares
// the whole render for tests/cases/cnpg-cluster/backup-cutover; this states
// what that render must contain).
func TestBackupCutoverOptions(t *testing.T) {
	raw, err := os.ReadFile("../../tests/cases/cnpg-cluster/backup-cutover/values.yaml")
	require.NoError(t, err)

	var values map[string]any
	require.NoError(t, yaml.Unmarshal(raw, &values))

	wrapped, err := yaml.Marshal(map[string]any{"clusters": []any{map[string]any{"values": values}}})
	require.NoError(t, err)

	objs := objects(t, ".", string(wrapped))

	store := objs["ObjectStore/pg-objectstore"]
	require.NotNil(t, store)

	spec := store["spec"].(map[string]any)
	assert.NotContains(t, spec, "retentionPolicy")

	conf := spec["configuration"].(map[string]any)
	assert.Equal(t, map[string]any{"cnpg-backup-retention": "managed"}, conf["tags"])
	assert.Equal(t, map[string]any{"cnpg-backup-retention": "keep"}, conf["historyTags"])
	assert.NotNil(t, objs["Backup/pg-g2"])
}

const identityRows = `
clusters:
  - values: {clusterName: alpha-pg, namespace: alpha, profile: devel}
%s
  - values: {clusterName: beta-pg, namespace: beta, profile: devel}
%s
`

func jobs(objs map[string]map[string]any) []string {
	var names []string

	for k := range objs {
		if strings.HasPrefix(k, "Job/") {
			names = append(names, k)
		}
	}

	return names
}

// TestIdentityCheckIsOffByDefault: a row without identityCheck, or with it
// disabled, renders no Job (and the chart never renders a ServiceAccount: the
// CNPG operator owns the Cluster's).
func TestIdentityCheckIsOffByDefault(t *testing.T) {
	objs := objects(t, ".", fmt.Sprintf(identityRows, "", "    identityCheck: {enabled: false, expectRole: r}"))

	assert.Empty(t, jobs(objs))

	for k := range objs {
		assert.False(t, strings.HasPrefix(k, "ServiceAccount/"), k)
	}
}

// TestIdentityCheckPerRow: each enabled row gets one hook Job in its own
// namespace, as its ServiceAccount (the Cluster's name unless named), with the
// expected role, and a pod no CNPG or application selector can match.
func TestIdentityCheckPerRow(t *testing.T) {
	objs := objects(t, ".", fmt.Sprintf(identityRows,
		"    identityCheck: {enabled: true, expectRole: role-alpha}",
		"    identityCheck: {enabled: true, expectRole: role-beta, serviceAccount: beta-sa}"))

	assert.ElementsMatch(t, []string{"Job/x-alpha-pg-identity-check", "Job/x-beta-pg-identity-check"}, jobs(objs))

	for name, want := range map[string]struct{ ns, sa, role string }{
		"Job/x-alpha-pg-identity-check": {"alpha", "alpha-pg", "role-alpha"},
		"Job/x-beta-pg-identity-check":  {"beta", "beta-sa", "role-beta"},
	} {
		job := objs[name]
		meta := job["metadata"].(map[string]any)
		assert.Equal(t, want.ns, meta["namespace"], name)
		assert.Equal(t, "pre-install,pre-upgrade", meta["annotations"].(map[string]any)["helm.sh/hook"], name)

		pod := job["spec"].(map[string]any)["template"].(map[string]any)
		spec := pod["spec"].(map[string]any)
		assert.Equal(t, "Never", spec["restartPolicy"], name)
		assert.Equal(t, want.sa, spec["serviceAccountName"], name)

		c := spec["containers"].([]any)[0].(map[string]any)
		assert.Equal(t, []any{"-expect-role", want.role}, c["args"], name)
		assert.Contains(t, c["image"], "@sha256:", name)

		labels := pod["metadata"].(map[string]any)["labels"].(map[string]any)
		assert.Equal(t, "cnpg-projects-identity-check", labels["app.kubernetes.io/name"], name)
		assert.NotContains(t, labels, "cnpg.io/cluster", name)
	}
}

// TestIdentityCheckExpectAccount: expectAccount reaches the check as
// "-expect-account <id>" after "-expect-role <role>", and only on the rows that
// set it. The id is built at run time: this repo is public and its leak canary
// rejects a literal 12-digit run.
func TestIdentityCheckExpectAccount(t *testing.T) {
	account := strings.Repeat("7", 12)
	objs := objects(t, ".", fmt.Sprintf(identityRows,
		fmt.Sprintf(`    identityCheck: {enabled: true, expectRole: role-alpha, expectAccount: %q}`, account),
		`    identityCheck: {enabled: true, expectRole: role-beta}`))

	args := func(name string) any {
		spec := objs[name]["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)

		return spec["containers"].([]any)[0].(map[string]any)["args"]
	}

	assert.Equal(t, []any{"-expect-role", "role-alpha", "-expect-account", account}, args("Job/x-alpha-pg-identity-check"))
	assert.Equal(t, []any{"-expect-role", "role-beta"}, args("Job/x-beta-pg-identity-check"), "a row without expectAccount passes no flag")

	// anything but 12 digits is refused
	cmd := exec.CommandContext(t.Context(), "helm", "template", "x", ".", "-f", "-")
	cmd.Stdin = strings.NewReader(fmt.Sprintf(identityRows,
		`    identityCheck: {enabled: true, expectRole: role-alpha, expectAccount: "12345"}`, ""))
	out, err := cmd.CombinedOutput()
	require.Error(t, err)
	assert.Contains(t, string(out), "expectAccount")
}
