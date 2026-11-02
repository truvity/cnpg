// Copyright 2026 Truvity B.V.. All rights reserved.
// SPDX-License-Identifier: MIT

package chart_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func stuckReloadValues(g map[string]any) map[string]any {
	v := map[string]any{
		"clusterName": "app-pg",
		"namespace":   "app",
		"profile":     "prod",
		"bootstrap":   map[string]any{"initdb": map[string]any{"database": "app", "owner": "app_owner"}},
	}
	if g != nil {
		v["guards"] = map[string]any{"stuckReload": g}
	}

	return v
}

func stuckReloadObjects(docs map[string][]map[string]any) int {
	n := 0

	for _, kind := range []string{"ServiceAccount", "Role", "RoleBinding", "CronJob"} {
		for _, d := range docs[kind] {
			if d["metadata"].(map[string]any)["name"] == "app-pg-stuck-reload" {
				n++
			}
		}
	}

	return n
}

func find(docs map[string][]map[string]any, kind, name string) map[string]any {
	for _, d := range docs[kind] {
		if d["metadata"].(map[string]any)["name"] == name {
			return d
		}
	}

	return nil
}

// TestStuckReload_OffByDefault: nothing is rendered unless enabled.
func TestStuckReload_OffByDefault(t *testing.T) {
	for name, g := range map[string]map[string]any{"unset": nil, "disabled": {"enabled": false}} {
		t.Run(name, func(t *testing.T) {
			docs, err := renderDocs(t, stuckReloadValues(g))
			require.NoError(t, err)
			assert.Zero(t, stuckReloadObjects(docs))
			assert.Empty(t, docs["CronJob"])
		})
	}
}

// TestStuckReload_Objects: its own ServiceAccount, a Role with exactly the
// verbs the command needs, a RoleBinding to that ServiceAccount, and a
// CronJob that is not a hook.
func TestStuckReload_Objects(t *testing.T) {
	docs, err := renderDocs(t, stuckReloadValues(map[string]any{"enabled": true}))
	require.NoError(t, err)
	assert.Equal(t, 4, stuckReloadObjects(docs))

	for _, kind := range []string{"ServiceAccount", "Role", "RoleBinding", "CronJob"} {
		d := find(docs, kind, "app-pg-stuck-reload")
		require.NotNil(t, d, kind)

		meta := d["metadata"].(map[string]any)
		assert.Equal(t, "app", meta["namespace"], kind)
		assert.Nil(t, meta["annotations"], kind+": not a hook")

		labels := meta["labels"].(map[string]any)
		assert.Equal(t, "stuck-reload", labels["app.kubernetes.io/component"], kind)
		assert.Equal(t, "app-pg", labels["app.kubernetes.io/instance"], kind)
	}

	role := find(docs, "Role", "app-pg-stuck-reload")
	assert.Equal(t, []any{
		map[string]any{"apiGroups": []any{"postgresql.cnpg.io"}, "resources": []any{"clusters"}, "resourceNames": []any{"app-pg"}, "verbs": []any{"get"}},
		map[string]any{"apiGroups": []any{""}, "resources": []any{"pods"}, "verbs": []any{"list"}},
		map[string]any{"apiGroups": []any{""}, "resources": []any{"pods/exec"}, "verbs": []any{"create"}},
		map[string]any{"apiGroups": []any{""}, "resources": []any{"events"}, "verbs": []any{"create"}},
	}, role["rules"])

	rb := find(docs, "RoleBinding", "app-pg-stuck-reload")
	assert.Equal(t, map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "Role", "name": "app-pg-stuck-reload"}, rb["roleRef"])
	assert.Equal(t, []any{map[string]any{"kind": "ServiceAccount", "name": "app-pg-stuck-reload", "namespace": "app"}}, rb["subjects"])

	// No egress policy: the exec goes through the API server.
	assert.Empty(t, docs["NetworkPolicy"])
}

// TestStuckReload_CronJob: the defaults and the pod shape.
func TestStuckReload_CronJob(t *testing.T) {
	docs, err := renderDocs(t, stuckReloadValues(map[string]any{"enabled": true}))
	require.NoError(t, err)

	cj := find(docs, "CronJob", "app-pg-stuck-reload")
	spec := cj["spec"].(map[string]any)
	assert.Equal(t, "*/3 * * * *", spec["schedule"])
	assert.Equal(t, "Forbid", spec["concurrencyPolicy"])
	assert.EqualValues(t, 120, spec["startingDeadlineSeconds"])
	assert.EqualValues(t, 1, spec["successfulJobsHistoryLimit"])
	assert.EqualValues(t, 3, spec["failedJobsHistoryLimit"])

	job := spec["jobTemplate"].(map[string]any)["spec"].(map[string]any)
	assert.EqualValues(t, 0, job["backoffLimit"])
	assert.EqualValues(t, 120, job["activeDeadlineSeconds"])

	pod := job["template"].(map[string]any)["spec"].(map[string]any)
	assert.Equal(t, "app-pg-stuck-reload", pod["serviceAccountName"])
	assert.Equal(t, "Never", pod["restartPolicy"])
	assert.Equal(t, true, pod["automountServiceAccountToken"])
	assert.Equal(t, map[string]any{"runAsNonRoot": true, "runAsUser": float64(65532), "runAsGroup": float64(65532), "seccompProfile": map[string]any{"type": "RuntimeDefault"}}, pod["securityContext"])

	c := pod["containers"].([]any)[0].(map[string]any)
	assert.Equal(t, "ghcr.io/truvity/cnpg/cnpgctl:0.0.0", c["image"])
	assert.Equal(t, []any{
		"guard", "stuck-reload", "--namespace=app", "--cluster=app-pg", "--stuck-for=5m",
		"--phases=Waiting for the instances to become active",
		"--phases=Switchover in progress",
		"--phases=Cluster cannot proceed to reconciliation due to an error while interacting with plugins",
	}, c["args"])
	assert.Equal(t, map[string]any{
		"allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true,
		"capabilities": map[string]any{"drop": []any{"ALL"}},
	}, c["securityContext"])
}

// TestStuckReload_Overrides: schedule, window, phases, image and deadline.
func TestStuckReload_Overrides(t *testing.T) {
	digest := "sha256:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	docs, err := renderDocs(t, stuckReloadValues(map[string]any{
		"enabled": true, "schedule": "*/10 * * * *", "stuckFor": "15m", "phases": []any{"Switchover in progress"},
		"image":                 map[string]any{"tag": "2.39.0", "digest": digest},
		"activeDeadlineSeconds": 60,
	}))
	require.NoError(t, err)

	spec := find(docs, "CronJob", "app-pg-stuck-reload")["spec"].(map[string]any)
	assert.Equal(t, "*/10 * * * *", spec["schedule"])

	job := spec["jobTemplate"].(map[string]any)["spec"].(map[string]any)
	assert.EqualValues(t, 60, job["activeDeadlineSeconds"])

	c := job["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)
	assert.Equal(t, "ghcr.io/truvity/cnpg/cnpgctl:2.39.0@"+digest, c["image"])
	assert.Equal(t, []any{"guard", "stuck-reload", "--namespace=app", "--cluster=app-pg", "--stuck-for=15m", "--phases=Switchover in progress"}, c["args"])
}

// TestStuckReload_Schema: bad values are refused at render time.
func TestStuckReload_Schema(t *testing.T) {
	for name, g := range map[string]map[string]any{
		"unknown key":  {"enabled": true, "bogus": 1},
		"bad duration": {"enabled": true, "stuckFor": "soon"},
		"empty phases": {"enabled": true, "phases": []any{}},
		"comma phase":  {"enabled": true, "phases": []any{"a,b"}},
		"short":        {"enabled": true, "activeDeadlineSeconds": 5},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := renderDocs(t, stuckReloadValues(g))
			require.Error(t, err)
		})
	}
}
