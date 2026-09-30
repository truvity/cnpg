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
