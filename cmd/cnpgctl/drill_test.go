// Copyright 2026 Truvity B.V.. All rights reserved.
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/truvity/cnpg/v2/pkg/drill"
)

// nothing is a cluster with no objects at all; writes succeed.
type nothing struct{ configMaps int }

func nf(what string) error { return fmt.Errorf("%w: %s", drill.ErrNotFound, what) }

func (nothing) Get(_ context.Context, _ schema.GroupVersionResource, _, name string) (*unstructured.Unstructured, error) {
	return nil, nf(name)
}
func (nothing) Create(context.Context, schema.GroupVersionResource, *unstructured.Unstructured) error {
	return nil
}
func (nothing) Delete(context.Context, schema.GroupVersionResource, string, string) error { return nil }
func (nothing) List(context.Context, schema.GroupVersionResource, string) ([]unstructured.Unstructured, error) {
	return nil, nil
}
func (nothing) ListPVCs(context.Context, string, string) ([]corev1.PersistentVolumeClaim, error) {
	return nil, nil
}
func (nothing) ListPods(context.Context, string, string) ([]corev1.Pod, error) { return nil, nil }
func (nothing) ListEvents(context.Context, string) ([]corev1.Event, error)     { return nil, nil }
func (nothing) Logs(context.Context, string, string, string, bool, int64) (io.ReadCloser, error) {
	return nil, nf("log")
}
func (nothing) GetSecret(_ context.Context, _, name string) (*corev1.Secret, error) {
	return nil, nf(name)
}
func (nothing) GetConfigMap(_ context.Context, _, name string) (*corev1.ConfigMap, error) {
	return nil, nf(name)
}
func (n *nothing) CreateConfigMap(context.Context, *corev1.ConfigMap) error {
	n.configMaps++
	return nil
}
func (nothing) UpdateConfigMap(context.Context, *corev1.ConfigMap) error { return nil }
func (nothing) ReclaimPolicy(context.Context, string) (string, error)    { return "", nil }

func TestDrillFlagErrors(t *testing.T) {
	for _, args := range [][]string{
		{"drill"},
		{"drill", "--namespace", "a"},
		{"drill", "--namespace", "a", "--cluster", "b", "--output", "yaml"},
		{"drill", "--namespace", "a", "--cluster", "b", "--min-rows", "a=-1"},
		{"drill", "--namespace", "a", "--cluster", "b", "--toleration", "pool"},
		{"drill", "--namespace", "a", "--cluster", "b", "--node-selector", "pool"},
		{"drill", "--namespace", "a", "--cluster", "b", "--memory-limit", "lots"},
		{"drill", "--namespace", "a", "--cluster", "b", "--sidecar-env", "AWS_ACCESS_KEY_ID=x"},
		{"drill", "--namespace", "a", "--cluster", "b", "extra"},
		{"drill-metrics", "--namespace", "a"},
		{"drill-metrics", "--namespace", "a", "--source", "nope"},
	} {
		var out, errb bytes.Buffer
		err := run(context.Background(), args, &out, &errb, deps{drill: drill.Deps{Kube: &nothing{}}})
		assert.Error(t, err, "%v", args)
		assert.NotErrorIs(t, err, errFailed, "%v", args)
	}
}

func TestDrillOfAMissingClusterFailsAndIsRecorded(t *testing.T) {
	k := &nothing{}
	var out, errb bytes.Buffer
	err := run(context.Background(), []string{"drill", "--namespace", "a", "--cluster", "b", "--output", "json"}, &out, &errb, deps{drill: drill.Deps{Kube: k}})
	assert.ErrorIs(t, err, errFailed)
	assert.Contains(t, out.String(), `"name": "plan"`)
	assert.Equal(t, 1, k.configMaps, "a drill that cannot start is recorded as a failure")

	out.Reset()
	err = run(context.Background(), []string{"drill", "--namespace", "a", "--cluster", "b", "--no-record"}, &out, &errb, deps{drill: drill.Deps{Kube: k}})
	assert.ErrorIs(t, err, errFailed)
	assert.Contains(t, out.String(), "drill FAILED")
	assert.Equal(t, 1, k.configMaps)
}
