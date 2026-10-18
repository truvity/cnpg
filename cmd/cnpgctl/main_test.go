package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/truvity/cnpg/v2/pkg/verify"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type empty struct{}

func (empty) Cluster(context.Context, string, string) (*unstructured.Unstructured, error) {
	return &unstructured.Unstructured{Object: map[string]any{}}, nil
}

func (empty) Secret(context.Context, string, string) (*corev1.Secret, error) {
	return nil, verify.ErrNotFound
}

func TestExitAndOutput(t *testing.T) {
	var out, errb bytes.Buffer
	err := run(context.Background(), []string{"verify", "--namespace", "db", "--cluster", "pg", "--output", "json"}, &out, &errb, empty{})
	assert.ErrorIs(t, err, errFailed)
	assert.Contains(t, out.String(), `"status": "FAIL"`)
}

func TestFlagErrors(t *testing.T) {
	var out, errb bytes.Buffer
	assert.Error(t, run(context.Background(), []string{"verify"}, &out, &errb, empty{}))
	assert.Error(t, run(context.Background(), []string{"verify", "--namespace", "a", "--cluster", "b", "--output", "yaml"}, &out, &errb, empty{}))
	assert.Error(t, run(context.Background(), []string{"nope"}, &out, &errb, empty{}))
	assert.NoError(t, run(context.Background(), []string{"version"}, &out, &errb, empty{}))
}

func TestPoolLabelFlag(t *testing.T) {
	var out, errb bytes.Buffer
	err := run(context.Background(), []string{"verify", "--namespace", "a", "--cluster", "b", "--pool-label", "nokey"}, &out, &errb, empty{})
	assert.ErrorContains(t, err, "--pool-label")
	// empty{} has no placement support: the assertion is reported as a FAIL.
	out.Reset()
	err = run(context.Background(), []string{"verify", "--namespace", "a", "--cluster", "b", "--pool-label", "pool=database", "--check-applied"}, &out, &errb, empty{})
	assert.ErrorIs(t, err, errFailed)
	assert.Contains(t, out.String(), "instances on pool")
	assert.Contains(t, out.String(), "Database/DatabaseRole applied")
}

func TestCheckIdentityFlag(t *testing.T) {
	var out, errb bytes.Buffer
	// empty{} cannot read Pods: the assertion is reported as a FAIL, not skipped.
	err := run(context.Background(), []string{"verify", "--namespace", "a", "--cluster", "b", "--check-identity"}, &out, &errb, empty{})
	assert.ErrorIs(t, err, errFailed)
	assert.Contains(t, out.String(), "backup sidecar has Pod Identity")

	out.Reset()
	_ = run(context.Background(), []string{"verify", "--namespace", "a", "--cluster", "b"}, &out, &errb, empty{})
	assert.NotContains(t, out.String(), "backup sidecar has Pod Identity")
}
