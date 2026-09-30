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
		"CnpgBackupTooOld":              "cnpg_collector_last_available_backup_timestamp",
		"CnpgReplicationLagHigh":        "cnpg_pg_replication_lag",
		"CnpgCertificateExpiring":       "certmanager_certificate_expiration_timestamp_seconds",
		"CnpgCertificateExpiryImminent": "certmanager_certificate_expiration_timestamp_seconds",
		"CnpgWalVolumeFillingUp":        "kubelet_volume_stats_available_bytes",
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
