// Copyright 2026 Truvity B.V.. All rights reserved.
// SPDX-License-Identifier: MIT

package chart_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestObjectStore_RetentionPolicy: the default still renders 30d; 0 omits the
// field, because a no-delete writer role would get 403 on the plugin's
// pruning and mark backups failed (expiry is the bucket lifecycle's job).
func TestObjectStore_RetentionPolicy(t *testing.T) {
	for name, tc := range map[string]struct {
		backup map[string]any
		want   any
	}{
		"default": {backup: map[string]any{}, want: "30d"},
		"set":     {backup: map[string]any{"retentionDays": 7}, want: "7d"},
		"zero":    {backup: map[string]any{"retentionDays": 0}, want: nil},
	} {
		t.Run(name, func(t *testing.T) {
			docs, err := renderDocs(t, storeValues(tc.backup, nil))
			require.NoError(t, err)

			backup, _ := objectStores(t, docs)
			assert.Equal(t, tc.want, field(backup, "spec", "retentionPolicy"))
		})
	}
}

// TestObjectStore_Tags: tags and historyTags reach spec.configuration, and
// are absent by default.
func TestObjectStore_Tags(t *testing.T) {
	docs, err := renderDocs(t, storeValues(map[string]any{}, nil))
	require.NoError(t, err)

	backup, _ := objectStores(t, docs)
	assert.NotContains(t, field(backup, "spec", "configuration"), "tags")
	assert.NotContains(t, field(backup, "spec", "configuration"), "historyTags")

	docs, err = renderDocs(t, storeValues(map[string]any{
		"tags":        map[string]any{"cnpg-backup-retention": "managed"},
		"historyTags": map[string]any{"cnpg-backup-retention": "keep"},
	}, nil))
	require.NoError(t, err)

	backup, _ = objectStores(t, docs)
	assert.Equal(t, map[string]any{"cnpg-backup-retention": "managed"}, field(backup, "spec", "configuration", "tags"))
	assert.Equal(t, map[string]any{"cnpg-backup-retention": "keep"}, field(backup, "spec", "configuration", "historyTags"))
}

// TestOnDemandBackup: off by default; an id renders one plugin Backup named
// from it, and a different id renders a different name.
func TestOnDemandBackup(t *testing.T) {
	docs, err := renderDocs(t, storeValues(map[string]any{}, nil))
	require.NoError(t, err)
	assert.Empty(t, docs["Backup"])

	var names []any

	for _, id := range []string{"g2", "g3"} {
		docs, err = renderDocs(t, storeValues(map[string]any{"onDemand": map[string]any{"name": id}}, nil))
		require.NoError(t, err)
		require.Len(t, docs["Backup"], 1)

		b := docs["Backup"][0]
		names = append(names, field(b, "metadata", "name"))
		assert.Equal(t, "plugin", field(b, "spec", "method"))
		assert.Equal(t, "app-pg", field(b, "spec", "cluster", "name"))
		assert.Equal(t, "app-pg-objectstore", field(b, "spec", "pluginConfiguration", "parameters", "barmanObjectName"))
		assert.Equal(t, "Prune=false", field(b, "metadata", "annotations", "argocd.argoproj.io/sync-options"))
	}

	assert.Equal(t, []any{"app-pg-g2", "app-pg-g3"}, names)
}
