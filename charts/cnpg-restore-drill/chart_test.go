// Copyright 2026 Truvity B.V.. All rights reserved.
// SPDX-License-Identifier: MIT

// Package chart_test holds cnpg-restore-drill to what the goldens cannot show
// at a glance: the default schedules keep out of the base backup window, the
// Job outlives the drill's timeout, every argument a CronJob passes is one
// cnpgctl drill accepts, no two workloads share a ServiceAccount, and no rule
// lets a runner write to anything but its own drill.
package chart_test

import (
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	"github.com/truvity/cnpg/v2/pkg/drill"
)

func render(t *testing.T, values string, extra ...string) []map[string]any {
	t.Helper()

	args := append([]string{"template", "x", ".", "--namespace", "drills", "-f", "-"}, extra...)
	cmd := exec.CommandContext(t.Context(), "helm", args...)
	cmd.Stdin = strings.NewReader(values)

	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "helm template failed:\n%s", out)

	var objs []map[string]any

	for _, d := range strings.Split(string(out), "\n---") {
		var m map[string]any
		if yaml.Unmarshal([]byte(d), &m) == nil && len(m) > 0 {
			objs = append(objs, m)
		}
	}

	return objs
}

func kind(objs []map[string]any, k string) []map[string]any {
	var out []map[string]any

	for _, o := range objs {
		if o["kind"] == k {
			out = append(out, o)
		}
	}

	return out
}

func get(o map[string]any, path ...string) any {
	var cur any = o
	for _, p := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}

		cur = m[p]
	}

	return cur
}

func podSpec(o map[string]any) map[string]any {
	if o["kind"] == "CronJob" {
		return get(o, "spec", "jobTemplate", "spec", "template", "spec").(map[string]any)
	}

	return get(o, "spec", "template", "spec").(map[string]any)
}

func args(o map[string]any) []string {
	var out []string
	for _, a := range podSpec(o)["containers"].([]any)[0].(map[string]any)["args"].([]any) {
		out = append(out, a.(string))
	}

	return out
}

func manyDrills(n int) string {
	var b strings.Builder
	b.WriteString("defaults: {storageClass: example-delete}\ndrills:\n")

	for i := range n {
		fmt.Fprintf(&b, "  - {namespace: project-%d, cluster: app-%d-pg}\n", i, i)
	}

	return b.String()
}

// TestDefaultSchedulesAvoidTheBackupWindow: monthly, a day 1-28 (every month
// has it), an hour 04-23 UTC, spread out, and the same on every render.
func TestDefaultSchedulesAvoidTheBackupWindow(t *testing.T) {
	values := manyDrills(80)
	first := kind(render(t, values), "CronJob")
	again := kind(render(t, values), "CronJob")
	require.Len(t, first, 80)

	days, hours := map[int]bool{}, map[int]bool{}

	for i, cj := range first {
		schedule := get(cj, "spec", "schedule").(string)
		assert.Equal(t, schedule, get(again[i], "spec", "schedule"), "a render is stable")
		assert.Equal(t, "Etc/UTC", get(cj, "spec", "timeZone"))

		f := strings.Fields(schedule)
		require.Len(t, f, 5, schedule)
		minute, _ := strconv.Atoi(f[0])
		hour, _ := strconv.Atoi(f[1])
		day, _ := strconv.Atoi(f[2])
		assert.Equal(t, []string{"*", "*"}, f[3:], schedule)
		assert.True(t, minute >= 0 && minute < 60, schedule)
		assert.True(t, hour >= 4 && hour <= 23, "%s: never inside 01:00-04:00 UTC", schedule)
		assert.True(t, day >= 1 && day <= 28, schedule)
		days[day], hours[hour] = true, true
	}

	assert.Greater(t, len(days), 10, "the drills are spread over the month")
	assert.Greater(t, len(hours), 8, "and over the day")
}

// TestTheJobOutlivesTheDrill: activeDeadlineSeconds leaves the teardown 30
// minutes after cnpgctl's own timeout; the Job is never retried and never
// overlaps itself.
func TestTheJobOutlivesTheDrill(t *testing.T) {
	objs := render(t, `
defaults: {storageClass: example-delete, timeoutMinutes: 90}
drills:
  - {namespace: shop, cluster: shop-pg}
  - {namespace: ledger, cluster: ledger-pg, timeoutMinutes: 240}
`)
	want := map[string]int{"shop-pg-restore": 90, "ledger-pg-restore": 240}

	for _, cj := range kind(objs, "CronJob") {
		name := get(cj, "metadata", "name").(string)
		timeout := want[name]
		assert.Contains(t, args(cj), fmt.Sprintf("--timeout=%dm", timeout), name)
		assert.EqualValues(t, (timeout+30)*60, get(cj, "spec", "jobTemplate", "spec", "activeDeadlineSeconds"), name)
		assert.EqualValues(t, 0, get(cj, "spec", "jobTemplate", "spec", "backoffLimit"), name)
		assert.Equal(t, "Forbid", get(cj, "spec", "concurrencyPolicy"), name)
		assert.EqualValues(t, 660, podSpec(cj)["terminationGracePeriodSeconds"], "the teardown after a SIGTERM")
	}
}

// TestTheArgumentsAreOnesCnpgctlAccepts parses every CronJob's and the
// exporter's arguments with cnpgctl's own flag parser, on the fully set case.
func TestTheArgumentsAreOnesCnpgctlAccepts(t *testing.T) {
	values, err := readCase("full")
	require.NoError(t, err)

	objs := render(t, values)
	cronJobs := kind(objs, "CronJob")
	require.Len(t, cronJobs, 2)

	for _, cj := range cronJobs {
		a := args(cj)
		require.Equal(t, "drill", a[0])
		cli, err := drill.ParseArgs(a[1:], io.Discard)
		require.NoError(t, err, "%v", a)
		assert.Equal(t, "drills", cli.Options.DrillNamespace)
		assert.Equal(t, get(cj, "metadata", "name"), cli.Options.DrillName)
	}

	ledger, err := drill.ParseArgs(args(cronJobs[0])[1:], io.Discard)
	require.NoError(t, err)

	if ledger.Options.Cluster != "ledger-pg" {
		ledger, err = drill.ParseArgs(args(cronJobs[1])[1:], io.Discard)
		require.NoError(t, err)
	}

	o := ledger.Options
	assert.Equal(t, "s3://example-backups/ledger/ledger-pg", o.DestinationPath)
	assert.Equal(t, "ledger-pg-g2", o.ServerName, "<project>-pg-<generation>")
	assert.Equal(t, "ledger-drill", o.DrillName)
	assert.Equal(t, map[string]int64{"ledger": 1000, "shop": 100, "shop/public.orders": 10}, o.MinRows, "maps merge under the entry")
	assert.Equal(t, []string{"ledger"}, o.Databases, "a list on the entry replaces the default")
	assert.Len(t, o.Tolerations, 2)
	assert.Equal(t, "exact", o.CountMode)

	exporters := kind(objs, "Deployment")
	require.Len(t, exporters, 1)

	ea := args(exporters[0])
	require.Equal(t, "drill-metrics", ea[0])
	m, err := drill.ParseMetricsArgs(ea[1:], io.Discard)
	require.NoError(t, err)
	assert.Equal(t, "drills", m.Namespace)
	assert.Equal(t, []drill.Source{
		{Namespace: "shop", Cluster: "shop-pg", DrillName: "shop-pg-restore"},
		{Namespace: "ledger", Cluster: "ledger-pg", DrillName: "ledger-drill"},
	}, m.Sources)
}

// TestNoTwoWorkloadsShareAServiceAccount is component contract C14: every
// runner and the exporter have their own, and none is a drill's (a restore
// ServiceAccount).
func TestNoTwoWorkloadsShareAServiceAccount(t *testing.T) {
	values, err := readCase("full")
	require.NoError(t, err)

	for _, v := range []string{values, manyDrills(5)} {
		objs := render(t, v)
		seen := map[string]string{}
		drills := map[string]bool{}

		for _, cj := range kind(objs, "CronJob") {
			drills[get(cj, "metadata", "name").(string)] = true
		}

		for _, o := range append(kind(objs, "CronJob"), kind(objs, "Deployment")...) {
			sa := podSpec(o)["serviceAccountName"].(string)
			name := get(o, "metadata", "name").(string)
			assert.Empty(t, seen[sa], "%s and %s share ServiceAccount %s", name, seen[sa], sa)
			assert.False(t, drills[sa], "%s runs as a drill's restore ServiceAccount %s", name, sa)
			seen[sa] = name
		}

		assert.Len(t, kind(objs, "ServiceAccount"), len(seen), "one ServiceAccount per workload, all rendered")
	}
}

// TestNoRuleLetsARunnerWriteToTheSource: in a source namespace a runner may
// only get and list; in the drill namespace every verb but create, list and
// reading logs is narrowed to its own drill's names. The same-namespace case is the one that
// matters: the source Cluster sits beside the drill.
func TestNoRuleLetsARunnerWriteToTheSource(t *testing.T) {
	objs := render(t, `
defaults: {storageClass: example-delete}
drills:
  - {namespace: drills, cluster: shop-pg}
  - {namespace: ledger, cluster: ledger-pg}
`)

	for _, role := range kind(objs, "Role") {
		name := get(role, "metadata", "name").(string)
		ns := get(role, "metadata", "namespace").(string)

		for _, raw := range role["rules"].([]any) {
			rule := raw.(map[string]any)
			names, _ := rule["resourceNames"].([]any)

			for _, v := range rule["verbs"].([]any) {
				verb := v.(string)

				if strings.HasSuffix(name, "-runner-source") {
					assert.Contains(t, []string{"get", "list"}, verb, "%s/%s: a source Role only reads", ns, name)
					continue
				}

				// Logs cannot be narrowed: the recovery Job's pod names are
				// generated. It is a read.
				if verb == "create" || verb == "list" || (verb == "get" && fmt.Sprint(rule["resources"]) == "[pods/log]") {
					continue
				}

				require.NotEmpty(t, names, "%s/%s: %s %v is not narrowed by name", ns, name, verb, rule["resources"])

				for _, n := range names {
					assert.NotEqual(t, "shop-pg", n, "%s/%s: the source Cluster is never in a rule that may %s", ns, name, verb)
					assert.NotEqual(t, "ledger-pg", n)
				}
			}
		}
	}
}

func TestThePodMonitorKeepsTheSourceNamespace(t *testing.T) {
	values := "defaults: {storageClass: example-delete}\ndrills: [{namespace: shop, cluster: shop-pg}]\n"
	assert.Empty(t, kind(render(t, values), "PodMonitor"), "not without the API")

	pms := kind(render(t, values, "--api-versions", "monitoring.coreos.com/v1/PodMonitor"), "PodMonitor")
	require.Len(t, pms, 1)
	ep := get(pms[0], "spec", "podMetricsEndpoints").([]any)[0].(map[string]any)
	assert.Equal(t, true, ep["honorLabels"])
	assert.Equal(t, "metrics", ep["port"])

	off := kind(render(t, values+"exporter: {enabled: false}\n", "--api-versions", "monitoring.coreos.com/v1/PodMonitor"), "PodMonitor")
	assert.Empty(t, off)
}

func TestNothingWithoutDrills(t *testing.T) {
	assert.Empty(t, render(t, "{}\n"))
}

func readCase(name string) (string, error) {
	out, err := exec.Command("cat", "../../tests/cases/cnpg-restore-drill/"+name+"/values.yaml").Output() //nolint:gosec // a fixed test path
	return string(out), err
}
