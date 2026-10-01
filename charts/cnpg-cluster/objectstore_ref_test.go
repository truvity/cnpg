// Copyright 2026 Truvity B.V.. All rights reserved.
// SPDX-License-Identifier: MIT

package chart_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func refValues(backup map[string]any) map[string]any {
	backup["enabled"] = true

	return map[string]any{
		"clusterName": "app-pg",
		"namespace":   "app",
		"profile":     "prod",
		"backup":      backup,
	}
}

// TestObjectStoreRef_ReferencesWithoutRendering: naming an existing
// ObjectStore renders none, and points the Cluster's plugin and the
// ScheduledBackup at it, with this cluster's own serverName.
func TestObjectStoreRef_ReferencesWithoutRendering(t *testing.T) {
	docs, err := renderDocs(t, refValues(map[string]any{
		"objectStoreName": "app-archive",
		"serverName":      "app-pg-g2",
	}))
	require.NoError(t, err)

	assert.Empty(t, docs["ObjectStore"], "a referenced store is somebody else's to render")

	cluster := docs["Cluster"][0]
	assert.Equal(t, []any{map[string]any{
		"name":          "barman-cloud.cloudnative-pg.io",
		"enabled":       true,
		"isWALArchiver": true,
		"parameters": map[string]any{
			"barmanObjectName": "app-archive",
			"serverName":       "app-pg-g2",
		},
	}}, field(cluster, "spec", "plugins"))

	require.Len(t, docs["ScheduledBackup"], 1)
	sb := docs["ScheduledBackup"][0]
	assert.Equal(t, "app-pg-scheduled-backup", field(sb, "metadata", "name"))
	assert.Equal(t, "0 0 2 * * *", field(sb, "spec", "schedule"))
	assert.Equal(t, map[string]any{
		"name":       "barman-cloud.cloudnative-pg.io",
		"parameters": map[string]any{"barmanObjectName": "app-archive"},
	}, field(sb, "spec", "pluginConfiguration"))
}

// TestObjectStoreRef_OwnStoreUnchanged: with objectStoreName empty the
// chart keeps rendering "{clusterName}-objectstore" and refers to it.
func TestObjectStoreRef_OwnStoreUnchanged(t *testing.T) {
	docs, err := renderDocs(t, refValues(map[string]any{"bucketName": "example-backups"}))
	require.NoError(t, err)

	require.Len(t, docs["ObjectStore"], 1)
	assert.Equal(t, "app-pg-objectstore", field(docs["ObjectStore"][0], "metadata", "name"))
	assert.Equal(t, "app-pg-objectstore",
		field(docs["ScheduledBackup"][0], "spec", "pluginConfiguration", "parameters", "barmanObjectName"))
	assert.Equal(t, "app-pg",
		field(docs["Cluster"][0], "spec", "plugins").([]any)[0].(map[string]any)["parameters"].(map[string]any)["serverName"])
}

// TestObjectStoreRef_NoScheduleNoScheduledBackup: the schedule stays
// optional with a referenced store.
func TestObjectStoreRef_NoScheduleNoScheduledBackup(t *testing.T) {
	docs, err := renderDocs(t, refValues(map[string]any{
		"objectStoreName": "app-archive",
		"serverName":      "app-pg-g2",
		"schedule":        "",
	}))
	require.NoError(t, err)
	assert.Empty(t, docs["ScheduledBackup"])
}

// TestObjectStoreRef_DisabledRendersNothing: enabled=false wins.
func TestObjectStoreRef_DisabledRendersNothing(t *testing.T) {
	values := refValues(map[string]any{"objectStoreName": "app-archive", "serverName": "g2"})
	values["backup"].(map[string]any)["enabled"] = false

	docs, err := renderDocs(t, values)
	require.NoError(t, err)
	assert.Empty(t, docs["ScheduledBackup"])
	assert.Nil(t, field(docs["Cluster"][0], "spec", "plugins"))
}

// TestObjectStoreRef_Refusals: each combination that would be silently
// ignored, or that starts a second timeline in somebody else's archive,
// fails the render.
func TestObjectStoreRef_Refusals(t *testing.T) {
	cases := map[string]struct {
		backup map[string]any
		want   string
	}{
		"no serverName": {
			map[string]any{"objectStoreName": "app-archive"},
			"backup.objectStoreName needs backup.serverName",
		},
		"bucket too": {
			map[string]any{"objectStoreName": "a", "serverName": "g2", "bucketName": "b"},
			"mutually exclusive",
		},
		"prefix": {
			map[string]any{"objectStoreName": "a", "serverName": "g2", "s3Prefix": "x/y"},
			"unset backup.s3Prefix",
		},
		"endpoint": {
			map[string]any{"objectStoreName": "a", "serverName": "g2", "endpoint": "https://s3.example.test"},
			"unset backup.endpoint",
		},
		"secret": {
			map[string]any{"objectStoreName": "a", "serverName": "g2", "existingSecret": "keys"},
			"unset backup.existingSecret",
		},
		"endpoint CA": {
			map[string]any{"objectStoreName": "a", "serverName": "g2", "endpointCA": map[string]any{"name": "ca", "key": "ca.crt"}},
			"unset backup.endpointCA",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := renderDocs(t, refValues(tc.backup))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}
