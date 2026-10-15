// Copyright 2026 Truvity B.V.. All rights reserved.
// SPDX-License-Identifier: MIT

package chart_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/truvity/cnpg/v2/pkg/backupnaming"
)

func presetValues(backup map[string]any) map[string]any {
	backup["enabled"] = true

	return map[string]any{
		"clusterName": "app-pg",
		"namespace":   "app",
		"profile":     "prod",
		"backup":      backup,
	}
}

// TestBackupPreset_BucketMatchesBackupnaming: the chart builds the bucket
// name by the rule pkg/backupnaming.ProjectBucketName states, and refuses
// the names it refuses.
func TestBackupPreset_BucketMatchesBackupnaming(t *testing.T) {
	for _, tc := range []struct{ prefix, project, env, region string }{
		{"acme-pg", "billing", "staging", "eu-west-1"},
		{"acme-pg", "ledger", "live", "us-east-2"},
		{"x", "a", "b", "c"},
		{"", "billing", "staging", "eu-west-1"},
		{"acme-pg", "", "staging", "eu-west-1"},
		{"acme-pg", "billing", "", "eu-west-1"},
		{"acme-pg", "billing", "staging", ""},
		{"acme-pg", "Billing", "staging", "eu-west-1"},
		{"acme-pg", "bill_ing", "staging", "eu-west-1"},
		{"acme-pg", strings.Repeat("p", 40), "staging", "eu-west-1"},
		{"-acme", "billing", "staging", "eu-west-1-"},
	} {
		t.Run(fmt.Sprintf("%+v", tc), func(t *testing.T) {
			want, wantErr := backupnaming.ProjectBucketName(tc.prefix, tc.project, tc.env, tc.region)

			docs, err := renderDocs(t, presetValues(map[string]any{
				"bucket": map[string]any{"prefix": tc.prefix, "project": tc.project, "env": tc.env, "region": tc.region},
			}))

			if wantErr != nil {
				require.Error(t, err, "backupnaming refuses it: %v", wantErr)

				return
			}

			require.NoError(t, err)
			require.Len(t, docs["ObjectStore"], 1)

			spec, _ := docs["ObjectStore"][0]["spec"].(map[string]any)
			conf, _ := spec["configuration"].(map[string]any)
			assert.Equal(t, "s3://"+want+"/app/app-pg", conf["destinationPath"])
		})
	}
}

// TestBackupPreset_ServerNameMatchesBackupnaming: generation gives
// backupnaming.ServerName, from backup.project or backup.bucket.project.
func TestBackupPreset_ServerNameMatchesBackupnaming(t *testing.T) {
	for name, backup := range map[string]map[string]any{
		"project":        {"project": "billing", "bucketName": "b", "generation": "g4"},
		"bucket project": {"bucket": map[string]any{"prefix": "p", "project": "billing", "env": "e", "region": "r"}, "generation": "g4"},
	} {
		t.Run(name, func(t *testing.T) {
			docs, err := renderDocs(t, presetValues(backup))
			require.NoError(t, err)

			cluster := docs["Cluster"][0]["spec"].(map[string]any)
			plugin := cluster["plugins"].([]any)[0].(map[string]any)
			params := plugin["parameters"].(map[string]any)
			assert.Equal(t, backupnaming.ServerName("billing", "g4"), params["serverName"])
		})
	}
}

// TestBackupPreset_ExplicitWins: an explicit bucketName, serverName,
// retentionDays, tags and onDemand.name are never replaced by the preset.
func TestBackupPreset_ExplicitWins(t *testing.T) {
	docs, err := renderDocs(t, presetValues(map[string]any{
		"mode":          "perProjectBucket",
		"bucket":        map[string]any{"prefix": "p", "project": "billing", "env": "e", "region": "r"},
		"generation":    "g4",
		"bucketName":    "explicit-bucket",
		"serverName":    "explicit-server",
		"retentionDays": 7,
		"tags":          map[string]any{"k": "v"},
		"historyTags":   map[string]any{"k": "h"},
		"onDemand":      map[string]any{"name": "mine"},
	}))
	require.NoError(t, err)

	spec := docs["ObjectStore"][0]["spec"].(map[string]any)
	conf := spec["configuration"].(map[string]any)
	assert.Equal(t, "s3://explicit-bucket/app/app-pg", conf["destinationPath"])
	assert.Equal(t, "7d", spec["retentionPolicy"])
	assert.Equal(t, map[string]any{"k": "v"}, conf["tags"])
	assert.Equal(t, map[string]any{"k": "h"}, conf["historyTags"])
	require.Len(t, docs["Backup"], 1)
	assert.Equal(t, "app-pg-mine", docs["Backup"][0]["metadata"].(map[string]any)["name"])

	cluster := docs["Cluster"][0]["spec"].(map[string]any)
	params := cluster["plugins"].([]any)[0].(map[string]any)["parameters"].(map[string]any)
	assert.Equal(t, "explicit-server", params["serverName"])
}

// TestBackupPreset_Refusals: the preset fails the render on a half-set input.
func TestBackupPreset_Refusals(t *testing.T) {
	for name, tc := range map[string]struct {
		backup map[string]any
		want   string
	}{
		"mode without generation": {map[string]any{"mode": "perProjectBucket", "bucketName": "b"}, "needs backup.generation"},
		"unknown mode":            {map[string]any{"mode": "other", "bucketName": "b"}, "mode"},
		"generation no project":   {map[string]any{"generation": "g1", "bucketName": "b"}, "backup.project"},
		"projects differ": {map[string]any{
			"project": "a", "generation": "g1",
			"bucket": map[string]any{"prefix": "p", "project": "b", "env": "e", "region": "r"},
		}, "differ"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := renderDocs(t, presetValues(tc.backup))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}
