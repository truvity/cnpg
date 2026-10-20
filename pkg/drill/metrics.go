// Copyright 2026 Truvity B.V.. All rights reserved.
// SPDX-License-Identifier: MIT

package drill

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
)

// The metric names. Every series carries namespace and cluster: the SOURCE
// Cluster's namespace and name.
const (
	MetricConfigured       = "cnpg_restore_drill_configured"
	MetricLastRun          = "cnpg_restore_drill_last_run_timestamp_seconds"
	MetricLastSuccess      = "cnpg_restore_drill_last_success_timestamp_seconds"
	MetricLastResult       = "cnpg_restore_drill_last_result"
	MetricLastDuration     = "cnpg_restore_drill_last_duration_seconds"
	MetricRecoveryPointAge = "cnpg_restore_drill_recovery_point_age_seconds"
)

// Source is one drilled cluster as the exporter knows it.
type Source struct {
	Namespace string // the source Cluster's namespace
	Cluster   string // the source Cluster's name
	DrillName string // default <Cluster>-restore
}

// ParseSource parses "<namespace>/<cluster>[=<drill name>]".
func ParseSource(s string) (Source, error) {
	spec, drillName, _ := strings.Cut(s, "=")
	ns, cluster, ok := strings.Cut(spec, "/")
	if !ok || ns == "" || cluster == "" || strings.Contains(cluster, "/") {
		return Source{}, fmt.Errorf("%q: want <namespace>/<cluster>[=<drill name>]", s)
	}
	if drillName == "" {
		drillName = cluster + DrillSuffix
	}
	return Source{Namespace: ns, Cluster: cluster, DrillName: drillName}, nil
}

var families = []struct{ name, help string }{
	{MetricConfigured, "1 for every source cluster the exporter is configured with, whether or not a drill has run yet."},
	{MetricLastRun, "Unix time the last restore drill of the source cluster finished."},
	{MetricLastSuccess, "Unix time the last successful restore drill of the source cluster finished; 0 when none has succeeded."},
	{MetricLastResult, "1 when the last restore drill of the source cluster succeeded, 0 when it failed."},
	{MetricLastDuration, "Duration of the last restore drill of the source cluster, teardown included."},
	{MetricRecoveryPointAge, "Age of the recovered data in the last restore drill, when it was checked: the time of the check minus the later of the last replayed commit and the base backup start. Absent when it could not be measured."},
}

// WriteMetrics writes the Prometheus text exposition of the results. results
// maps a drill name to its result ConfigMap (absent when no drill ran yet).
func WriteMetrics(w io.Writer, sources []Source, results map[string]*corev1.ConfigMap) error {
	values := map[string][]string{}
	for _, s := range sources {
		labels := fmt.Sprintf(`namespace="%s",cluster="%s"`, escapeLabel(s.Namespace), escapeLabel(s.Cluster))
		values[MetricConfigured] = append(values[MetricConfigured], fmt.Sprintf("%s{%s} 1", MetricConfigured, labels))
		cm := results[s.DrillName]
		if cm == nil {
			continue
		}
		for _, m := range []struct{ metric, key string }{
			{MetricLastRun, KeyLastRun},
			{MetricLastSuccess, KeyLastSuccess},
			{MetricLastResult, KeyLastResult},
			{MetricLastDuration, KeyLastDuration},
			{MetricRecoveryPointAge, KeyRecoveryPointAge},
		} {
			raw, ok := cm.Data[m.key]
			if !ok || raw == "" {
				continue
			}
			v, err := strconv.ParseFloat(raw, 64)
			if err != nil {
				return fmt.Errorf("ConfigMap %s/%s: %s=%q is not a number", cm.Namespace, cm.Name, m.key, raw)
			}
			values[m.metric] = append(values[m.metric], fmt.Sprintf("%s{%s} %s", m.metric, labels, strconv.FormatFloat(v, 'f', -1, 64)))
		}
	}
	for _, f := range families {
		if len(values[f.name]) == 0 {
			continue
		}
		if _, err := fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s gauge\n%s\n", f.name, f.help, f.name, strings.Join(values[f.name], "\n")); err != nil {
			return err
		}
	}
	return nil
}

func escapeLabel(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s)
}

// ReadResults gets the result ConfigMap of every source from the drill
// namespace. A missing one is no error: that drill has not run yet.
func ReadResults(ctx context.Context, k Kube, namespace string, sources []Source) (map[string]*corev1.ConfigMap, error) {
	out := map[string]*corev1.ConfigMap{}
	for _, s := range sources {
		cm, err := k.GetConfigMap(ctx, namespace, ResultName(s.DrillName))
		switch {
		case errors.Is(err, ErrNotFound):
		case err != nil:
			return nil, err
		default:
			out[s.DrillName] = cm
		}
	}
	return out, nil
}

// MetricsHandler serves the results as Prometheus text at every scrape,
// reading the ConfigMaps afresh (a handful of GETs by name). A read error is
// a 500, so the scrape fails visibly rather than reporting stale numbers.
func MetricsHandler(k Kube, namespace string, sources []Source) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		results, err := ReadResults(ctx, k, namespace, sources)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		var buf bytes.Buffer
		if err := WriteMetrics(&buf, sources, results); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = w.Write(buf.Bytes())
	})
}
