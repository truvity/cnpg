// Copyright 2026 Truvity B.V.. All rights reserved.
// SPDX-License-Identifier: MIT

// Package operator_test holds the operator preset files to what the docs
// promise: they parse, they carry the settings the preset exists for, and
// the fast-rollout overlay differs from the default in the rollout delays
// and nothing else. The upstream charts are not rendered here (that needs
// the network); a chart version bump re-runs the preset by hand.
package operator_test

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

func load(t *testing.T, name string) map[string]any {
	t.Helper()

	raw, err := os.ReadFile(name)
	require.NoError(t, err)

	var out map[string]any
	require.NoError(t, yaml.UnmarshalStrict(raw, &out))

	return out
}

func data(t *testing.T, values map[string]any) map[string]any {
	t.Helper()

	cfg, ok := values["config"].(map[string]any)
	require.True(t, ok, "config block")

	d, ok := cfg["data"].(map[string]any)
	require.True(t, ok, "config.data block")

	return d
}

func TestOperatorPreset(t *testing.T) {
	d := data(t, load(t, "cloudnative-pg.values.yaml"))

	assert.Equal(t, "true", d["ENABLE_INSTANCE_MANAGER_INPLACE_UPDATES"])
	assert.Contains(t, d["INHERITED_LABELS"], "app.kubernetes.io/instance")
	assert.NotEmpty(t, d["INHERITED_ANNOTATIONS"])
	assert.Equal(t, "120", d["CLUSTERS_ROLLOUT_DELAY"])
	assert.Equal(t, "30", d["INSTANCES_ROLLOUT_DELAY"])
}

func TestFastRolloutOverlayTouchesOnlyTheDelays(t *testing.T) {
	overlay := load(t, "cloudnative-pg.fast-rollout.values.yaml")
	d := data(t, overlay)

	assert.Len(t, overlay, 1, "the overlay sets config and nothing else")
	assert.Equal(t, map[string]any{
		"CLUSTERS_ROLLOUT_DELAY":  "0",
		"INSTANCES_ROLLOUT_DELAY": "0",
	}, d)
}

func TestNoEstateNamesInPreset(t *testing.T) {
	for _, f := range []string{"cloudnative-pg.values.yaml", "plugin-barman-cloud.values.yaml"} {
		v := load(t, f)
		assert.NotContains(t, v, "nodeSelector", f)
		assert.NotContains(t, v, "tolerations", f)
	}
}

func TestOtherExamplesParse(t *testing.T) {
	assert.NotEmpty(t, load(t, "plugin-barman-cloud.values.yaml"))
	assert.NotEmpty(t, load(t, "cnpg-platform.values.yaml"))
}
