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

	spec := role["spec"].(map[string]any)
	assert.Contains(t, spec["assumeRolePolicyDocument"], "pods.eks.amazonaws.com")
	assert.Contains(t, spec["assumeRolePolicyDocument"], `["alpha-pg","alpha-rehearsal-pg"]`)
	assert.Contains(t, spec["inlinePolicies"].(map[string]any)["cnpg-backup"], "arn:aws:s3:::example-backups/alpha/alpha-pg/*")

	for _, name := range []string{"alpha-pg", "alpha-rehearsal-pg"} {
		pia := objs["PodIdentityAssociation/example-pg-backup-alpha-"+name]
		require.NotNil(t, pia)
		assert.Equal(t, "alpha", pia["spec"].(map[string]any)["namespace"], "the ServiceAccount is the project's")
		assert.Equal(t, "platform-system", pia["metadata"].(map[string]any)["namespace"])
	}
}
