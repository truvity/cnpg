// Copyright 2026 Truvity B.V.. All rights reserved.
// SPDX-License-Identifier: MIT

// Package chart_test holds cnpg-projects to its two promises: a cluster
// entry renders exactly the objects the cnpg-cluster chart renders for the
// same values (the templates are carried over by hack/sync-cnpg-projects.sh,
// so this is the check that the carry is faithful), and the archive roles
// have the trust and the permissions the platform has always minted.
package chart_test

import (
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
