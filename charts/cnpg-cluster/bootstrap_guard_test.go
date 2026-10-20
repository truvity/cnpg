// Copyright 2026 Truvity B.V.. All rights reserved.
// SPDX-License-Identifier: MIT

package chart_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func guardValues(backup map[string]any, guards map[string]any) map[string]any {
	v := map[string]any{
		"clusterName": "app-pg",
		"namespace":   "app",
		"profile":     "prod",
		"bootstrap":   map[string]any{"initdb": map[string]any{"database": "app", "owner": "app_owner"}},
		"backup":      backup,
	}
	if guards != nil {
		v["guards"] = map[string]any{"emptyBootstrap": guards}
	}

	return v
}

func perProjectBackup() map[string]any {
	return map[string]any{
		"enabled":    true,
		"mode":       "perProjectBucket",
		"bucket":     map[string]any{"prefix": "example-pg", "project": "app", "env": "staging", "region": "eu-west-1"},
		"generation": "g4",
	}
}

func hooks(docs map[string][]map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}

	for _, kind := range []string{"Role", "RoleBinding", "Job"} {
		for _, d := range docs[kind] {
			meta, _ := d["metadata"].(map[string]any)
			if strings.HasSuffix(meta["name"].(string), "-bootstrap-guard") {
				out[kind] = d
			}
		}
	}

	return out
}

func jobArgs(t *testing.T, job map[string]any) map[string]string {
	t.Helper()

	spec := job["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
	c := spec["containers"].([]any)[0].(map[string]any)
	out := map[string]string{"serviceAccountName": spec["serviceAccountName"].(string), "image": c["image"].(string)}

	for _, a := range c["args"].([]any) {
		k, v, _ := strings.Cut(a.(string), "=")
		out[k] = v
	}

	return out
}

// TestEmptyBootstrapGuard_Default: on by default only for perProjectBucket
// with archiving on; nothing is rendered otherwise.
func TestEmptyBootstrapGuard_Default(t *testing.T) {
	for name, tc := range map[string]struct {
		backup map[string]any
		guards map[string]any
		want   bool
	}{
		"perProjectBucket":            {backup: perProjectBackup(), want: true},
		"perProjectBucket turned off": {backup: perProjectBackup(), guards: map[string]any{"enabled": false}},
		"archiving off":               {backup: map[string]any{"enabled": false}},
		"plain bucket":                {backup: map[string]any{"enabled": true, "bucketName": "b"}},
		"plain bucket turned on":      {backup: map[string]any{"enabled": true, "bucketName": "b"}, guards: map[string]any{"enabled": true}, want: true},
		"perProjectBucket with static credentials": {backup: func() map[string]any {
			b := perProjectBackup()
			b["existingSecret"] = "keys"

			return b
		}()},
	} {
		t.Run(name, func(t *testing.T) {
			docs, err := renderDocs(t, guardValues(tc.backup, tc.guards))
			require.NoError(t, err)

			h := hooks(docs)
			if !tc.want {
				assert.Empty(t, h)

				return
			}

			require.Len(t, h, 3)

			for kind, d := range h {
				ann := d["metadata"].(map[string]any)["annotations"].(map[string]any)
				assert.Equal(t, "pre-install,pre-upgrade", ann["helm.sh/hook"], kind)
				assert.Equal(t, "before-hook-creation", ann["helm.sh/hook-delete-policy"], kind)
			}
			// RBAC is in place before the Job runs.
			assert.Equal(t, "-11", h["Role"]["metadata"].(map[string]any)["annotations"].(map[string]any)["helm.sh/hook-weight"])
			assert.Equal(t, "-10", h["Job"]["metadata"].(map[string]any)["annotations"].(map[string]any)["helm.sh/hook-weight"])
		})
	}
}

// TestEmptyBootstrapGuard_ChecksWhatTheClusterWrites: the Job's destination
// is the ObjectStore's destinationPath and its server name the Cluster
// plugin's serverName, so the guard looks exactly where the cluster archives.
func TestEmptyBootstrapGuard_ChecksWhatTheClusterWrites(t *testing.T) {
	for name, backup := range map[string]map[string]any{
		"preset":                 perProjectBackup(),
		"explicit serverName":    {"enabled": true, "bucketName": "b", "serverName": "app-pg-g7"},
		"default serverName":     {"enabled": true, "bucketName": "b"},
		"custom prefix":          {"enabled": true, "bucketName": "b", "s3Prefix": "drills/app-pg"},
		"preset with serverName": func() map[string]any { b := perProjectBackup(); b["serverName"] = "app-pg-g9"; return b }(),
	} {
		t.Run(name, func(t *testing.T) {
			docs, err := renderDocs(t, guardValues(backup, map[string]any{"enabled": true}))
			require.NoError(t, err)

			job := hooks(docs)["Job"]
			require.NotNil(t, job)
			args := jobArgs(t, job)

			conf := docs["ObjectStore"][0]["spec"].(map[string]any)["configuration"].(map[string]any)
			assert.Equal(t, conf["destinationPath"], args["--destination"])

			plugin := docs["Cluster"][0]["spec"].(map[string]any)["plugins"].([]any)[0].(map[string]any)
			assert.Equal(t, plugin["parameters"].(map[string]any)["serverName"], args["--server-name"])

			assert.Equal(t, "initdb", args["--bootstrap"])
			assert.Equal(t, "app-pg-restore", args["serviceAccountName"])
			assert.Equal(t, "ghcr.io/truvity/cnpg/cnpgctl:0.0.0", args["image"])
		})
	}
}

// TestEmptyBootstrapGuard_RegionFromPreset: the preset's bucket region is
// passed; an explicit region wins.
func TestEmptyBootstrapGuard_RegionFromPreset(t *testing.T) {
	docs, err := renderDocs(t, guardValues(perProjectBackup(), nil))
	require.NoError(t, err)
	assert.Equal(t, "eu-west-1", jobArgs(t, hooks(docs)["Job"])["--region"])

	docs, err = renderDocs(t, guardValues(perProjectBackup(), map[string]any{"region": "us-east-2"}))
	require.NoError(t, err)
	assert.Equal(t, "us-east-2", jobArgs(t, hooks(docs)["Job"])["--region"])
}

// TestEmptyBootstrapGuard_Recovery: a recovery bootstrap continues an
// archive, so nothing is rendered.
func TestEmptyBootstrapGuard_Recovery(t *testing.T) {
	v := guardValues(perProjectBackup(), nil)
	v["bootstrap"] = map[string]any{
		"mode":     "recovery",
		"recovery": map[string]any{"source": map[string]any{"bucketName": "b", "s3Prefix": "app/app-pg", "serverName": "app-pg-pg-g3"}},
	}

	docs, err := renderDocs(t, v)
	require.NoError(t, err)
	assert.Empty(t, hooks(docs))
}

// TestEmptyBootstrapGuard_Override: the escape hatch renders no hook and puts
// the reason on the Cluster; it is refused without one.
func TestEmptyBootstrapGuard_Override(t *testing.T) {
	docs, err := renderDocs(t, guardValues(perProjectBackup(), map[string]any{"allowNonEmptyArchive": true, "reason": "restored by hand"}))
	require.NoError(t, err)
	assert.Empty(t, hooks(docs))

	ann := docs["Cluster"][0]["metadata"].(map[string]any)["annotations"].(map[string]any)
	assert.Equal(t, "restored by hand", ann["cnpg-cluster/empty-bootstrap-override"])

	_, err = renderDocs(t, guardValues(perProjectBackup(), map[string]any{"allowNonEmptyArchive": true}))
	require.ErrorContains(t, err, "needs guards.emptyBootstrap.reason")
}

// TestEmptyBootstrapGuard_Refusals: forcing it on where it cannot work.
func TestEmptyBootstrapGuard_Refusals(t *testing.T) {
	_, err := renderDocs(t, guardValues(map[string]any{"enabled": false}, map[string]any{"enabled": true}))
	require.ErrorContains(t, err, "archiving is off")

	_, err = renderDocs(t, guardValues(map[string]any{"enabled": true, "bucketName": "b", "endpointCA": map[string]any{"name": "ca", "key": "ca.crt"}}, map[string]any{"enabled": true}))
	require.ErrorContains(t, err, "ambient identity")
}
