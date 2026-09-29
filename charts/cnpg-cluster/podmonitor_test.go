// Copyright 2026 Truvity B.V.. All rights reserved.
// SPDX-License-Identifier: MIT

package chart_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func baseValues() map[string]any {
	return map[string]any{"clusterName": "app-pg", "namespace": "app", "profile": "devel"}
}

// TestPodMonitor_OffByDefault: a default render carries no PodMonitor,
// so an install without the CRD keeps working and existing renders do
// not change.
func TestPodMonitor_OffByDefault(t *testing.T) {
	docs, err := renderDocs(t, baseValues())
	require.NoError(t, err)
	assert.Empty(t, docs["PodMonitor"])

	v := baseValues()
	v["monitoring"] = map[string]any{"podMonitor": map[string]any{"enabled": false}}
	withOff, err := renderDocs(t, v)
	require.NoError(t, err)
	assert.Equal(t, docs, withOff, "explicit enabled=false must equal the default render")
}

// TestPodMonitor_Enabled: the selector targets this cluster's instance
// pods, the endpoint is the exporter's port, and the enabled render
// differs from the default one by the PodMonitor alone.
func TestPodMonitor_Enabled(t *testing.T) {
	labels := map[string]any{"example.com/tier": "db"}

	off := baseValues()
	off["labels"] = labels
	base, err := renderDocs(t, off)
	require.NoError(t, err)

	v := baseValues()
	v["labels"] = labels
	v["monitoring"] = map[string]any{"podMonitor": map[string]any{
		"enabled":  true,
		"interval": "30s",
		"labels":   map[string]any{"release": "scraper"},
		"metricRelabelings": []any{
			map[string]any{"action": "drop", "sourceLabels": []any{"__name__"}, "regex": "cnpg_pg_settings_setting"},
		},
	}}
	docs, err := renderDocs(t, v)
	require.NoError(t, err)
	require.Len(t, docs["PodMonitor"], 1)

	pm := docs["PodMonitor"][0]
	assert.Equal(t, "monitoring.coreos.com/v1", pm["apiVersion"])

	meta := pm["metadata"].(map[string]any)
	assert.Equal(t, "app-pg", meta["name"])
	assert.Equal(t, "app", meta["namespace"])
	assert.Equal(t, map[string]any{"release": "scraper", "example.com/tier": "db"}, meta["labels"])

	spec := pm["spec"].(map[string]any)
	assert.Equal(t, map[string]any{"matchLabels": map[string]any{
		"cnpg.io/cluster": "app-pg", "cnpg.io/podRole": "instance",
	}}, spec["selector"])

	eps := spec["podMetricsEndpoints"].([]any)
	require.Len(t, eps, 1)
	ep := eps[0].(map[string]any)
	assert.Equal(t, "metrics", ep["port"])
	assert.Equal(t, "/metrics", ep["path"])
	assert.Equal(t, "30s", ep["interval"])
	assert.Equal(t, []any{
		map[string]any{"action": "drop", "sourceLabels": []any{"__name__"}, "regex": "cnpg_pg_settings_setting"},
	}, ep["metricRelabelings"])

	delete(docs, "PodMonitor")
	assert.Equal(t, base["Cluster"], docs["Cluster"], "the Cluster must not change")
}

// TestPodMonitor_MinimalOmitsOptionals: no interval and no relabelings
// means neither key is emitted, so the scraper's defaults apply.
func TestPodMonitor_MinimalOmitsOptionals(t *testing.T) {
	v := baseValues()
	v["monitoring"] = map[string]any{"podMonitor": map[string]any{"enabled": true}}
	docs, err := renderDocs(t, v)
	require.NoError(t, err)
	require.Len(t, docs["PodMonitor"], 1)

	ep := docs["PodMonitor"][0]["spec"].(map[string]any)["podMetricsEndpoints"].([]any)[0].(map[string]any)
	assert.NotContains(t, ep, "interval")
	assert.NotContains(t, ep, "metricRelabelings")
}

// TestPodMonitor_SchemaRejects: unknown keys and wrong types under
// monitoring fail the render.
func TestPodMonitor_SchemaRejects(t *testing.T) {
	for name, pmv := range map[string]any{
		"unknown key": map[string]any{"enabled": true, "bogus": 1},
		"wrong type":  map[string]any{"enabled": "yes"},
	} {
		v := baseValues()
		v["monitoring"] = map[string]any{"podMonitor": pmv}
		_, err := renderDocs(t, v)
		require.Error(t, err, name)
	}

	v := baseValues()
	v["monitoring"] = map[string]any{"bogus": true}
	_, err := renderDocs(t, v)
	require.Error(t, err)
}
