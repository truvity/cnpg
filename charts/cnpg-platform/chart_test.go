// Copyright 2026 Truvity B.V.. All rights reserved.
// SPDX-License-Identifier: MIT

// Package chart_test asserts what the goldens cannot say in words: each
// alert names the metric it is about and carries the threshold it was given,
// the admission guard refuses exactly the attributes asked, and the
// metrics policy selects database instances only. promtool is not in the
// dev shell, so expressions are checked structurally (a metric name, a
// balanced brace count), not parsed.
package chart_test

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

func render(t *testing.T, sets ...string) []map[string]any {
	t.Helper()

	args := []string{"template", "x", "."}
	for _, s := range sets {
		args = append(args, "--set", s)
	}

	out, err := exec.CommandContext(t.Context(), "helm", args...).CombinedOutput()
	require.NoErrorf(t, err, "helm template failed:\n%s", out)

	var docs []map[string]any

	for _, d := range strings.Split(string(out), "\n---") {
		var m map[string]any
		if yaml.Unmarshal([]byte(d), &m) == nil && len(m) > 0 {
			docs = append(docs, m)
		}
	}

	return docs
}

func TestAlertRulesNameTheirMetricAndThreshold(t *testing.T) {
	docs := render(t, "alerts.enabled=true", "alerts.certExpiry.enabled=true",
		"alerts.certExpiry.nameRegex=pg-.*", "alerts.backupAge.maxAgeSeconds=1234")
	require.Len(t, docs, 1)

	spec := docs[0]["spec"].(map[string]any)
	rules := spec["groups"].([]any)[0].(map[string]any)["rules"].([]any)

	want := map[string]string{
		"CnpgWalArchivingFailing":       "cnpg_pg_stat_archiver_last_failed_time",
		"CnpgBackupTooOld":              "barman_cloud_cloudnative_pg_io_last_available_backup_timestamp",
		"CnpgReplicationLagHigh":        "cnpg_pg_replication_lag",
		"CnpgCertificateExpiring":       "certmanager_certificate_expiration_timestamp_seconds",
		"CnpgCertificateExpiryImminent": "certmanager_certificate_expiration_timestamp_seconds",
		"CnpgWalVolumeFillingUp":        "kubelet_volume_stats_available_bytes",
		"CnpgInstanceExporterDown":      "cnpg_collector_up",
		"CnpgInstanceScrapeDown":        "up{",
		"CnpgBackupNotConfigured":       "unless on (namespace, pod) barman_cloud_cloudnative_pg_io_last_available_backup_timestamp",
	}

	got := map[string]bool{}

	for _, r := range rules {
		rule := r.(map[string]any)
		name := rule["alert"].(string)
		expr := rule["expr"].(string)
		got[name] = true

		assert.Contains(t, expr, want[name], name)
		assert.Equal(t, strings.Count(expr, "{"), strings.Count(expr, "}"), name)
		assert.NotEmpty(t, rule["for"], name)
	}

	assert.Len(t, got, len(want))
	assert.Contains(t, rules[1].(map[string]any)["expr"], "> 1234")
}

func TestGuardForbidsOnlyWhatWasAsked(t *testing.T) {
	docs := render(t, "admission.databaseRoleGuard.enabled=true",
		"admission.databaseRoleGuard.namespaceSelector.matchLabels.a=b",
		"admission.databaseRoleGuard.forbid={superuser,bypassrls}")
	require.Len(t, docs, 2)

	policy := docs[0]["spec"].(map[string]any)
	vals := policy["validations"].([]any)
	require.Len(t, vals, 2)
	assert.Contains(t, vals[0].(map[string]any)["expression"], "object.spec.superuser")
	assert.Contains(t, vals[1].(map[string]any)["expression"], "object.spec.bypassrls")
}

func TestMetricsPolicySelectsInstancesOnly(t *testing.T) {
	docs := render(t, "metricsNetworkPolicy.enabled=true",
		"metricsNetworkPolicy.namespaces={a,b}",
		"metricsNetworkPolicy.from[0].podSelector.matchLabels.app=s")
	require.Len(t, docs, 2)

	for _, d := range docs {
		sel := d["spec"].(map[string]any)["podSelector"].(map[string]any)["matchExpressions"].([]any)[0].(map[string]any)
		assert.Equal(t, "cnpg.io/cluster", sel["key"])
		assert.Equal(t, "Exists", sel["operator"])
	}
}

// TestAbsenceGuardsFollowTheSelectorAndSwitches: the exporter-down and
// scrape-down rules carry alerts.selector, scrape-down is scoped to a
// PodMonitor job's postgres container, and each has its own switch.
func TestAbsenceGuardsFollowTheSelectorAndSwitches(t *testing.T) {
	exprs := func(sets ...string) map[string]string {
		docs := render(t, append([]string{"alerts.enabled=true"}, sets...)...)
		rules := docs[0]["spec"].(map[string]any)["groups"].([]any)[0].(map[string]any)["rules"].([]any)
		out := map[string]string{}

		for _, r := range rules {
			rule := r.(map[string]any)
			out[rule["alert"].(string)] = rule["expr"].(string)
		}

		return out
	}

	got := exprs("alerts.selector=namespace=~\"pg-.*\"")
	assert.Equal(t, `cnpg_collector_up{namespace=~"pg-.*"} == 0`, got["CnpgInstanceExporterDown"])
	assert.Equal(t, `up{container="postgres",job=~".+/.+", namespace=~"pg-.*"} == 0`, got["CnpgInstanceScrapeDown"])

	got = exprs()
	assert.Equal(t, `cnpg_collector_up{} == 0`, got["CnpgInstanceExporterDown"])
	assert.Equal(t, `up{container="postgres",job=~".+/.+"} == 0`, got["CnpgInstanceScrapeDown"])

	got = exprs("alerts.exporterDown.enabled=false", "alerts.scrapeDown.jobRegex=pg/.+")
	assert.NotContains(t, got, "CnpgInstanceExporterDown")
	assert.Contains(t, got["CnpgInstanceScrapeDown"], `job=~"pg/.+"`)

	got = exprs("alerts.scrapeDown.enabled=false")
	assert.NotContains(t, got, "CnpgInstanceScrapeDown")
	assert.Contains(t, got, "CnpgInstanceExporterDown")
}

// TestBackupNotConfigured: the rule is cnpg_collector_up unless the barman
// series exists for the same (namespace, pod), skips clusters that opted out
// with cnpg_cluster_backup_expected="false", follows alerts.selector, is a
// warning, and has its own switch and `for`.
func TestBackupNotConfigured(t *testing.T) {
	rule := func(sets ...string) map[string]any {
		docs := render(t, append([]string{"alerts.enabled=true"}, sets...)...)
		rules := docs[0]["spec"].(map[string]any)["groups"].([]any)[0].(map[string]any)["rules"].([]any)

		for _, r := range rules {
			if m := r.(map[string]any); m["alert"] == "CnpgBackupNotConfigured" {
				return m
			}
		}

		return nil
	}

	const tail = ` unless on (namespace, pod) barman_cloud_cloudnative_pg_io_last_available_backup_timestamp`

	r := rule()
	require.NotNil(t, r)
	assert.Equal(t, `cnpg_collector_up{cnpg_cluster_backup_expected!="false"}`+tail, r["expr"])
	assert.Equal(t, "warning", r["labels"].(map[string]any)["severity"])
	assert.Equal(t, "1h", r["for"])
	assert.Contains(t, r["annotations"].(map[string]any)["description"], "backup.expected=false")

	r = rule("alerts.selector=namespace=~\"pg-.*\"", "alerts.backupNotConfigured.for=3h")
	assert.Equal(t, `cnpg_collector_up{namespace=~"pg-.*", cnpg_cluster_backup_expected!="false"}`+tail, r["expr"])
	assert.Equal(t, "3h", r["for"])

	assert.Nil(t, rule("alerts.backupNotConfigured.enabled=false"))
}

// TestPodMonitorSelectsInstancePodsInEveryNamespace: one PodMonitor, in the
// release namespace unless told otherwise, selecting instance pods by the
// label the operator sets, on the port named metrics, with the pg_settings
// drop first and the caller's rules after it.
func TestPodMonitorSelectsInstancePodsInEveryNamespace(t *testing.T) {
	assert.Empty(t, render(t), "off by default")

	docs := render(t, "podMonitor.enabled=true")
	require.Len(t, docs, 1)

	pm := docs[0]
	assert.Equal(t, "PodMonitor", pm["kind"])

	meta := pm["metadata"].(map[string]any)
	assert.Equal(t, "cnpg-instances", meta["name"])
	assert.Equal(t, "default", meta["namespace"])

	spec := pm["spec"].(map[string]any)
	assert.Equal(t, map[string]any{"any": true}, spec["namespaceSelector"])
	assert.Equal(t, map[string]any{"matchLabels": map[string]any{"cnpg.io/podRole": "instance"}}, spec["selector"])
	assert.NotContains(t, spec, "podTargetLabels")

	ep := spec["podMetricsEndpoints"].([]any)[0].(map[string]any)
	assert.Equal(t, "metrics", ep["port"])
	assert.Equal(t, "/metrics", ep["path"])

	rel := ep["metricRelabelings"].([]any)
	require.Len(t, rel, 3)
	assert.Equal(t, "replace", rel[0].(map[string]any)["action"])
	assert.Equal(t, "cnpg_pg_settings_setting;(block_size|effective_cache_size|maintenance_work_mem|max_connections|random_page_cost|seq_page_cost|shared_buffers|work_mem)", rel[0].(map[string]any)["regex"])
	assert.Equal(t, "drop", rel[1].(map[string]any)["action"])
	assert.Equal(t, "labeldrop", rel[2].(map[string]any)["action"])
}

func TestPodMonitorKnobs(t *testing.T) {
	docs := render(t, "podMonitor.enabled=true", "podMonitor.namespaces={pg-one,pg-two}",
		"podMonitor.interval=30s", "podMonitor.namespace=monitoring",
		"podMonitor.podTargetLabels={cnpg-cluster/backup-expected}",
		"podMonitor.keepPgSettings=null")
	require.Len(t, docs, 1)

	assert.Equal(t, "monitoring", docs[0]["metadata"].(map[string]any)["namespace"])

	spec := docs[0]["spec"].(map[string]any)
	assert.Equal(t, map[string]any{"matchNames": []any{"pg-one", "pg-two"}}, spec["namespaceSelector"])
	assert.Equal(t, []any{"cnpg-cluster/backup-expected"}, spec["podTargetLabels"])

	ep := spec["podMetricsEndpoints"].([]any)[0].(map[string]any)
	assert.Equal(t, "30s", ep["interval"])

	// No settings spared: the whole family is dropped by one rule.
	rel := ep["metricRelabelings"].([]any)
	require.Len(t, rel, 1)
	assert.Equal(t, "cnpg_pg_settings_.*", rel[0].(map[string]any)["regex"])

	// dropPgSettings=false leaves only the caller's rules, in order.
	docs = render(t, "podMonitor.enabled=true", "podMonitor.dropPgSettings=false")
	ep = docs[0]["spec"].(map[string]any)["podMetricsEndpoints"].([]any)[0].(map[string]any)
	assert.NotContains(t, ep, "metricRelabelings")
}

// TestMetricsPolicyNamespaceMetadata: an entry that is an object carries its
// own labels and annotations over the policy-wide ones; a plain name
// inherits only the policy-wide ones.
func TestMetricsPolicyNamespaceMetadata(t *testing.T) {
	docs := render(t, "metricsNetworkPolicy.enabled=true",
		"metricsNetworkPolicy.labels.shared=yes",
		"metricsNetworkPolicy.annotations.shared=yes",
		"metricsNetworkPolicy.namespaces[0]=plain",
		"metricsNetworkPolicy.namespaces[1].name=special",
		"metricsNetworkPolicy.namespaces[1].labels.tier=primary",
		"metricsNetworkPolicy.namespaces[1].annotations.shared=overridden",
		"metricsNetworkPolicy.from[0].podSelector.matchLabels.app=s")
	require.Len(t, docs, 2)

	byNS := map[string]map[string]any{}
	for _, d := range docs {
		byNS[d["metadata"].(map[string]any)["namespace"].(string)] = d["metadata"].(map[string]any)
	}

	plain := byNS["plain"]
	assert.Equal(t, "yes", plain["labels"].(map[string]any)["shared"])
	assert.NotContains(t, plain["labels"], "tier")
	assert.Equal(t, map[string]any{"shared": "yes"}, plain["annotations"])

	special := byNS["special"]
	assert.Equal(t, "primary", special["labels"].(map[string]any)["tier"])
	assert.Equal(t, "yes", special["labels"].(map[string]any)["shared"])
	assert.Equal(t, map[string]any{"shared": "overridden"}, special["annotations"])
}
