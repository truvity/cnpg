// Copyright 2026 Truvity B.V.. All rights reserved.
// SPDX-License-Identifier: MIT

package chart_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These two values blocks are the ones documented in README.md's
// "Scheduling: estate-shaped examples" section, kept here so a change
// to the chart that breaks either example fails a test instead of
// leaving stale prose in the README (rule: every doc claim must be
// true of the repo at HEAD).

// TestDocumentedExample_TruvityShapedEstate: a platform with a tainted
// Karpenter "database" node pool, an arch taint on every node, and
// AES256-requiring buckets — scheduling.databasePool and
// scheduling.tolerations pin the placement; backup.encryption keeps its
// AES256 default so nothing needs to be said about it here.
func TestDocumentedExample_TruvityShapedEstate(t *testing.T) {
	docs, err := renderDocs(t, map[string]any{
		"clusterName": "app-pg",
		"namespace":   "app",
		"profile":     "prod",
		"scheduling": map[string]any{
			"databasePool": "database",
			"tolerations": []map[string]any{
				{"key": "arch", "operator": "Exists"},
			},
		},
		"backup": map[string]any{
			"enabled":    true,
			"bucketName": "example-backups",
		},
	})
	require.NoError(t, err)
	require.Len(t, docs["Cluster"], 1)

	cluster := docs["Cluster"][0]
	assert.Equal(t, "database", field(cluster, "spec", "affinity", "nodeSelector", "karpenter.sh/nodepool"))
	tolerations, _ := field(cluster, "spec", "affinity", "tolerations").([]any)
	require.Len(t, tolerations, 2, "the arch toleration plus the pool's own")

	backup, _ := objectStores(t, docs)
	assert.Equal(t, "AES256", field(backup, "spec", "configuration", "data", "encryption"), "AES256 stays the default — this estate's buckets require it")
}

// TestDocumentedExample_R2BackedEstate: no node pool (R2 has nothing to
// do with node scheduling), an R2 endpoint, static keys and no
// encryption header — R2 rejects x-amz-server-side-encryption.
func TestDocumentedExample_R2BackedEstate(t *testing.T) {
	docs, err := renderDocs(t, map[string]any{
		"clusterName": "app-pg",
		"namespace":   "app",
		"profile":     "prod",
		"backup": map[string]any{
			"enabled":        true,
			"bucketName":     "example-backups",
			"endpoint":       "https://example-account.r2.cloudflarestorage.com",
			"existingSecret": "pg-backup-s3",
			"encryption":     "",
		},
	})
	require.NoError(t, err)
	require.Len(t, docs["Cluster"], 1)

	cluster := docs["Cluster"][0]
	assert.Nil(t, field(cluster, "spec", "affinity", "nodeSelector"), "no pool — R2 is a backup concern, not a scheduling one")

	backup, _ := objectStores(t, docs)
	conf, _ := field(backup, "spec", "configuration").(map[string]any)
	assert.Equal(t, "https://example-account.r2.cloudflarestorage.com", conf["endpointURL"])
	data, _ := conf["data"].(map[string]any)
	assert.NotContains(t, data, "encryption", "R2 rejects the header; empty omits the field")
}
