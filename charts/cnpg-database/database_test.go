// Copyright 2026 Truvity B.V.. All rights reserved.
// SPDX-License-Identifier: MIT

// Package chart_test renders the cnpg-database chart. This chart has no
// posture logic of its own (chart_test.go's territory in cnpg-cluster);
// these tests only prove the minimum-values render and that
// values.schema.json (component contract C2) rejects a bad values file
// (C3's negative-fixture requirement for a repo using Go tests instead
// of golden renders).
package chart_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

func renderDatabase(t *testing.T, values map[string]any) (map[string]any, error) {
	t.Helper()

	raw, err := yaml.Marshal(values)
	require.NoError(t, err)

	file := filepath.Join(t.TempDir(), "values.yaml")
	require.NoError(t, os.WriteFile(file, raw, 0o600))

	out, err := exec.CommandContext(t.Context(), "helm", "template", "db", ".", "-f", file).CombinedOutput()
	if err != nil {
		return nil, &renderError{out: string(out)}
	}

	for _, doc := range strings.Split(string(out), "\n---") {
		var obj map[string]any
		if err := yaml.Unmarshal([]byte(doc), &obj); err != nil || obj == nil {
			continue
		}
		if obj["kind"] == "Database" {
			return obj, nil
		}
	}

	require.FailNow(t, "render produced no Database object:\n"+string(out))
	return nil, nil
}

type renderError struct{ out string }

func (e *renderError) Error() string { return e.out }

func field(obj map[string]any, path ...string) any {
	var cur any = obj
	for _, key := range path {
		m, _ := cur.(map[string]any)
		cur = m[key]
	}
	return cur
}

// TestDatabase_MinimumValues: the minimum values (clusterName, databaseName) render a Database CR owned by the default
// owner "app".
func TestDatabase_MinimumValues(t *testing.T) {
	obj, err := renderDatabase(t, map[string]any{
		"databaseName": "app",
		"clusterName":  "pg",
		"namespace":    "default",
	})
	require.NoError(t, err)

	assert.Equal(t, "pg-app", field(obj, "metadata", "name"))
	assert.Equal(t, "app", field(obj, "spec", "name"))
	assert.Equal(t, "app", field(obj, "spec", "owner"), "owner defaults to app")
	assert.Equal(t, "pg", field(obj, "spec", "cluster", "name"))
}

// TestDatabase_OwnerOverride: an explicit owner replaces the default.
func TestDatabase_OwnerOverride(t *testing.T) {
	obj, err := renderDatabase(t, map[string]any{
		"databaseName": "app",
		"clusterName":  "pg",
		"namespace":    "default",
		"owner":        "us-owner",
	})
	require.NoError(t, err)

	assert.Equal(t, "us-owner", field(obj, "spec", "owner"))
}

// TestSchema_RejectsWrongType: owner is a string in the schema; a
// number would otherwise render an owner role name Postgres rejects at
// apply time rather than at render time.
func TestSchema_RejectsWrongType(t *testing.T) {
	_, err := renderDatabase(t, map[string]any{
		"databaseName": "app",
		"clusterName":  "pg",
		"namespace":    "default",
		"owner":        123,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "owner")
}

// TestSchema_RejectsMissingRequired: clusterName is required by both the
// schema and the guard; the schema catches it first.
func TestSchema_RejectsMissingRequired(t *testing.T) {
	_, err := renderDatabase(t, map[string]any{
		"databaseName": "app",
		"namespace":    "default",
	})
	require.Error(t, err)
}

// renderAll renders the chart and returns every object, keyed "Kind/name".
func renderAll(t *testing.T, values map[string]any) map[string]map[string]any {
	t.Helper()

	raw, err := yaml.Marshal(values)
	require.NoError(t, err)

	file := filepath.Join(t.TempDir(), "values.yaml")
	require.NoError(t, os.WriteFile(file, raw, 0o600))

	out, err := exec.CommandContext(t.Context(), "helm", "template", "db", ".", "-f", file).CombinedOutput()
	require.NoError(t, err, string(out))

	objs := map[string]map[string]any{}
	for _, doc := range strings.Split(string(out), "\n---") {
		var obj map[string]any
		if err := yaml.Unmarshal([]byte(doc), &obj); err != nil || obj == nil {
			continue
		}
		objs[obj["kind"].(string)+"/"+field(obj, "metadata", "name").(string)] = obj
	}
	return objs
}

// TestRole_GeneratedPassword: the opt-in renders the generator, a once-only
// ExternalSecret and the DatabaseRole that reads the Secret, under exactly
// the names and labels the caller passes.
func TestRole_GeneratedPassword(t *testing.T) {
	objs := renderAll(t, map[string]any{
		"clusterName": "pg",
		"roles": []any{map[string]any{
			"name":                      "shop_app",
			"objectName":                "shop-role-shop-app",
			"labels":                    map[string]any{"app.kubernetes.io/name": "shop-infra"},
			"inherit":                   true,
			"connectionLimit":           -1,
			"databaseRoleReclaimPolicy": "retain",
			"generatedPassword":         map[string]any{"secretName": "shop-app-credentials"},
		}},
	})
	require.Len(t, objs, 3)

	role := objs["DatabaseRole/shop-role-shop-app"]
	require.NotNil(t, role)
	assert.Equal(t, "shop-app-credentials", field(role, "spec", "passwordSecret", "name"))
	assert.Equal(t, "shop-infra", field(role, "metadata", "labels", "app.kubernetes.io/name"))
	assert.EqualValues(t, -1, field(role, "spec", "connectionLimit"))

	gen := objs["Password/shop-app-credentials"]
	require.NotNil(t, gen)
	assert.Equal(t, "generators.external-secrets.io/v1alpha1", gen["apiVersion"])
	assert.EqualValues(t, 32, field(gen, "spec", "length"))
	assert.EqualValues(t, 5, field(gen, "spec", "digits"))
	assert.EqualValues(t, 0, field(gen, "spec", "symbols"), "no symbols: the password goes into a connection string")
	assert.Equal(t, "shop-infra", field(gen, "metadata", "labels", "app.kubernetes.io/name"))

	es := objs["ExternalSecret/shop-app-credentials"]
	require.NotNil(t, es)
	assert.Equal(t, "0", field(es, "spec", "refreshInterval"), "generated once: a refresh would lock out running pods")
	assert.Equal(t, "kubernetes.io/basic-auth", field(es, "spec", "target", "template", "type"))
	assert.Equal(t, "shop_app", field(es, "spec", "target", "template", "data", "username"))
	assert.Equal(t, "{{ .password }}", field(es, "spec", "target", "template", "data", "password"))
	assert.Equal(t, "shop-app-credentials", field(es, "spec", "target", "name"))
}

// TestRole_NoGeneratedPassword: without the opt-in nothing of External
// Secrets is rendered, so a cluster without its CRDs installs as before.
func TestRole_NoGeneratedPassword(t *testing.T) {
	objs := renderAll(t, map[string]any{
		"clusterName": "pg",
		"roles":       []any{map[string]any{"name": "svc_a", "passwordSecret": "svc-a-password"}},
	})
	require.Len(t, objs, 1)
	assert.NotNil(t, objs["DatabaseRole/pg-role-svc-a"])
}
