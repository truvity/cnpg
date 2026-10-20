// Copyright 2026 Truvity B.V.. All rights reserved.
// SPDX-License-Identifier: MIT

package drill

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The keys of a result ConfigMap. Numbers are decimal strings; a key the run
// could not measure is absent.
const (
	KeySourceNamespace     = "source_namespace"
	KeySourceCluster       = "source_cluster"
	KeyLastRun             = "last_run_timestamp_seconds"
	KeyLastResult          = "last_result"
	KeyLastSuccess         = "last_success_timestamp_seconds"
	KeyLastDuration        = "last_duration_seconds"
	KeyRecoveryPointAge    = "recovery_point_age_seconds"
	KeyLastFailure         = "last_failure"
	KeyReport              = "report.json"
	maxConfigMapReportSize = 512 * 1024
)

// Record writes the outcome of a drill to its result ConfigMap in the drill
// namespace, keeping the last success time of earlier runs when this one
// failed. The ConfigMap is the durable state between runs; cnpgctl
// drill-metrics exports it.
func Record(ctx context.Context, k Kube, rep *Report) error {
	name := ResultName(rep.DrillName)
	ok := !rep.Failed()
	data := map[string]string{
		KeySourceNamespace: rep.SourceNamespace,
		KeySourceCluster:   rep.SourceCluster,
		KeyLastRun:         strconv.FormatInt(rep.FinishedAt.Unix(), 10),
		KeyLastDuration:    strconv.FormatFloat(rep.DurationSeconds, 'f', 0, 64),
		KeyLastResult:      "0",
		KeyLastSuccess:     "0",
	}
	if ok {
		data[KeyLastResult] = "1"
		data[KeyLastSuccess] = data[KeyLastRun]
	} else {
		data[KeyLastFailure] = rep.FirstFailure()
	}
	if rep.RecoveryPointAgeSeconds != nil {
		data[KeyRecoveryPointAge] = strconv.FormatFloat(*rep.RecoveryPointAgeSeconds, 'f', 0, 64)
	}
	if b, err := json.Marshal(rep); err == nil && len(b) < maxConfigMapReportSize {
		data[KeyReport] = string(b)
	}

	existing, err := k.GetConfigMap(ctx, rep.DrillNamespace, name)
	switch {
	case errors.Is(err, ErrNotFound):
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: rep.DrillNamespace,
				Labels: map[string]string{
					LabelManagedBy:       ManagedBy,
					LabelResult:          "true",
					LabelSourceNamespace: rep.SourceNamespace,
					LabelSourceCluster:   rep.SourceCluster,
				},
			},
			Data: data,
		}
		return k.CreateConfigMap(ctx, cm)
	case err != nil:
		return fmt.Errorf("read the result ConfigMap: %w", err)
	}
	if !ok {
		if prev := existing.Data[KeyLastSuccess]; prev != "" {
			data[KeyLastSuccess] = prev
		}
	}
	cm := existing.DeepCopy()
	cm.Data = data
	if cm.Labels == nil {
		cm.Labels = map[string]string{}
	}
	cm.Labels[LabelManagedBy] = ManagedBy
	cm.Labels[LabelResult] = "true"
	cm.Labels[LabelSourceNamespace] = rep.SourceNamespace
	cm.Labels[LabelSourceCluster] = rep.SourceCluster
	return k.UpdateConfigMap(ctx, cm)
}
