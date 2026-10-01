// Copyright 2026 Truvity B.V.. All rights reserved.
// SPDX-License-Identifier: MIT

package chart_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

func baseValues() map[string]any {
	return map[string]any{"clusterName": "app-pg", "namespace": "app", "profile": "devel"}
}

const podMonitorAPI = "monitoring.coreos.com/v1/PodMonitor"

func endpoint(t *testing.T, pm map[string]any) map[string]any {
	t.Helper()

	eps := pm["spec"].(map[string]any)["podMetricsEndpoints"].([]any)
	require.Len(t, eps, 1)

	return eps[0].(map[string]any)
}

// TestPodMonitor_NeedsTheAPI: the PodMonitor is on by default but only
// rendered where the cluster serves the PodMonitor API, so an install
// without the CRDs is unchanged and cannot fail.
func TestPodMonitor_NeedsTheAPI(t *testing.T) {
	docs, err := renderDocs(t, baseValues())
	require.NoError(t, err)
	assert.Empty(t, docs["PodMonitor"], "no PodMonitor API, no PodMonitor")

	v := baseValues()
	v["monitoring"] = map[string]any{"podMonitor": map[string]any{"enabled": true}}
	explicit, err := renderDocs(t, v)
	require.NoError(t, err)
	assert.Equal(t, docs, explicit, "explicit enabled=true without the API must equal the default render")

	withAPI, err := renderDocsAPI(t, baseValues(), podMonitorAPI)
	require.NoError(t, err)
	require.Len(t, withAPI["PodMonitor"], 1)

	delete(withAPI, "PodMonitor")
	assert.Equal(t, docs, withAPI, "the PodMonitor is the only difference")
}

// TestPodMonitor_Disabled: enabled=false renders nothing even with the API.
func TestPodMonitor_Disabled(t *testing.T) {
	v := baseValues()
	v["monitoring"] = map[string]any{"podMonitor": map[string]any{"enabled": false}}
	docs, err := renderDocsAPI(t, v, podMonitorAPI)
	require.NoError(t, err)
	assert.Empty(t, docs["PodMonitor"])
}

// TestPodMonitor_DefaultDropsPgSettings: the default render selects this
// cluster's instance pods, scrapes the exporter port, drops the
// cnpg_pg_settings_* family except the settings the upstream dashboard
// reads, and adds no `cluster` relabeling (the exporter emits that label).
func TestPodMonitor_DefaultDropsPgSettings(t *testing.T) {
	docs, err := renderDocsAPI(t, baseValues(), podMonitorAPI)
	require.NoError(t, err)
	require.Len(t, docs["PodMonitor"], 1)

	pm := docs["PodMonitor"][0]
	assert.Equal(t, "monitoring.coreos.com/v1", pm["apiVersion"])
	meta := pm["metadata"].(map[string]any)
	assert.Equal(t, "app-pg", meta["name"])
	assert.Equal(t, "app", meta["namespace"])

	assert.Equal(t, map[string]any{"matchLabels": map[string]any{
		"cnpg.io/cluster": "app-pg", "cnpg.io/podRole": "instance",
	}}, pm["spec"].(map[string]any)["selector"])

	ep := endpoint(t, pm)
	assert.Equal(t, "metrics", ep["port"])
	assert.Equal(t, "/metrics", ep["path"])
	assert.NotContains(t, ep, "interval")

	rel := ep["metricRelabelings"].([]any)
	require.Len(t, rel, 3)

	keep := rel[0].(map[string]any)
	assert.Equal(t, "replace", keep["action"])
	assert.Contains(t, keep["regex"], "cnpg_pg_settings_setting;(")
	assert.Contains(t, keep["regex"], "max_connections")

	drop := rel[1].(map[string]any)
	assert.Equal(t, "drop", drop["action"])
	assert.Equal(t, "cnpg_pg_settings_.*;", drop["regex"])

	// The marker label is removed explicitly: Prometheus does not reliably
	// strip __-prefixed labels after metric relabeling.
	assert.Equal(t, map[string]any{"action": "labeldrop", "regex": "__tmp_keep_pg_setting"}, rel[2])

	raw, err := yaml.Marshal(rel)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "targetLabel: cluster")
}

// TestPodMonitor_DropEverything: with no kept settings the drop is the
// single, plain rule on the family.
func TestPodMonitor_DropEverything(t *testing.T) {
	v := baseValues()
	v["monitoring"] = map[string]any{"podMonitor": map[string]any{"keepPgSettings": []any{}}}
	docs, err := renderDocsAPI(t, v, podMonitorAPI)
	require.NoError(t, err)

	assert.Equal(t, []any{
		map[string]any{"action": "drop", "sourceLabels": []any{"__name__"}, "regex": "cnpg_pg_settings_.*"},
	}, endpoint(t, docs["PodMonitor"][0])["metricRelabelings"])
}

// TestPodMonitor_DropOff: dropPgSettings=false leaves only the user's rules.
func TestPodMonitor_DropOff(t *testing.T) {
	v := baseValues()
	v["monitoring"] = map[string]any{"podMonitor": map[string]any{"dropPgSettings": false}}
	docs, err := renderDocsAPI(t, v, podMonitorAPI)
	require.NoError(t, err)
	assert.NotContains(t, endpoint(t, docs["PodMonitor"][0]), "metricRelabelings")
}

// TestPodMonitor_UserRelabelingsAppend: the user's rules come after the
// default drop, and interval and labels pass through.
func TestPodMonitor_UserRelabelingsAppend(t *testing.T) {
	user := map[string]any{"action": "drop", "sourceLabels": []any{"__name__"}, "regex": "cnpg_pg_stat_statements_.*"}

	v := baseValues()
	v["labels"] = map[string]any{"example.com/tier": "db"}
	v["monitoring"] = map[string]any{"podMonitor": map[string]any{
		"interval":          "30s",
		"labels":            map[string]any{"release": "scraper"},
		"metricRelabelings": []any{user},
	}}
	docs, err := renderDocsAPI(t, v, podMonitorAPI)
	require.NoError(t, err)

	pm := docs["PodMonitor"][0]
	assert.Equal(t, map[string]any{"release": "scraper", "example.com/tier": "db"}, pm["metadata"].(map[string]any)["labels"])

	ep := endpoint(t, pm)
	assert.Equal(t, "30s", ep["interval"])

	rel := ep["metricRelabelings"].([]any)
	require.Len(t, rel, 4)
	assert.Equal(t, "labeldrop", rel[2].(map[string]any)["action"])
	assert.Equal(t, "replace", rel[0].(map[string]any)["action"])
	assert.Equal(t, "cnpg_pg_settings_.*;", rel[1].(map[string]any)["regex"])
	assert.Equal(t, user, rel[3])
}

func peers() []any {
	return []any{map[string]any{
		"namespaceSelector": map[string]any{"matchLabels": map[string]any{"kubernetes.io/metadata.name": "monitoring"}},
		"podSelector":       map[string]any{"matchLabels": map[string]any{"app.kubernetes.io/name": "vmagent"}},
	}}
}

// TestMetricsNetworkPolicy_OnlyWithPeers: no peers, no policy; peers
// render a policy that admits them to 9187 on this cluster's instances.
func TestMetricsNetworkPolicy_OnlyWithPeers(t *testing.T) {
	docs, err := renderDocs(t, baseValues())
	require.NoError(t, err)
	assert.Empty(t, docs["NetworkPolicy"])

	v := baseValues()
	v["monitoring"] = map[string]any{"networkPolicy": map[string]any{"from": peers()}}
	docs, err = renderDocs(t, v)
	require.NoError(t, err)
	require.Len(t, docs["NetworkPolicy"], 1)

	np := docs["NetworkPolicy"][0]
	assert.Equal(t, "app-pg-metrics", np["metadata"].(map[string]any)["name"])

	spec := np["spec"].(map[string]any)
	assert.Equal(t, map[string]any{"matchLabels": map[string]any{
		"cnpg.io/cluster": "app-pg", "cnpg.io/podRole": "instance",
	}}, spec["podSelector"])
	assert.Equal(t, []any{"Ingress"}, spec["policyTypes"])

	rule := spec["ingress"].([]any)[0].(map[string]any)
	assert.Equal(t, peers(), rule["from"])
	assert.Equal(t, []any{map[string]any{"port": float64(9187), "protocol": "TCP"}}, rule["ports"])

	// An explicit enabled=true with peers renders the same; false suppresses.
	v["monitoring"] = map[string]any{"networkPolicy": map[string]any{"enabled": true, "from": peers()}}
	again, err := renderDocs(t, v)
	require.NoError(t, err)
	assert.Equal(t, docs, again)

	v["monitoring"] = map[string]any{"networkPolicy": map[string]any{"enabled": false, "from": peers()}}
	off, err := renderDocs(t, v)
	require.NoError(t, err)
	assert.Empty(t, off["NetworkPolicy"])
}

// TestMetricsNetworkPolicy_RefusesEmptyPeers: an empty peer list means
// allow-all, so an explicit enabled=true without peers fails the render.
func TestMetricsNetworkPolicy_RefusesEmptyPeers(t *testing.T) {
	for name, from := range map[string]any{"absent": nil, "empty": []any{}} {
		np := map[string]any{"enabled": true}
		if from != nil {
			np["from"] = from
		}

		v := baseValues()
		v["monitoring"] = map[string]any{"networkPolicy": np}
		_, err := renderDocs(t, v)
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), "monitoring.networkPolicy.from is empty", name)
	}
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
	v["monitoring"] = map[string]any{"networkPolicy": map[string]any{"bogus": true}}
	_, err := renderDocs(t, v)
	require.Error(t, err)

	v = baseValues()
	v["monitoring"] = map[string]any{"bogus": true}
	_, err = renderDocs(t, v)
	require.Error(t, err)
}
