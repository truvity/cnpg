// Copyright 2026 Truvity B.V.. All rights reserved.
// SPDX-License-Identifier: MIT

package chart_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This repository has no golden renders / tests/invalid fixtures
// (component contract C3); its Go chart tests carry the negative case
// instead. values.schema.json (C2) is what actually rejects these —
// without it a bad type or an unknown key would render a CRD-invalid
// object that only fails later, at apply time.

// TestSchema_RejectsWrongType: retentionDays is an integer in the
// schema; nothing in the templates checks its type, so a string would
// otherwise render `retentionPolicy: thirtyd` — a syntactically valid
// but meaningless value the operator would reject at apply time, far
// from where the mistake was made.
func TestSchema_RejectsWrongType(t *testing.T) {
	_, err := renderDocs(t, map[string]any{
		"clusterName": "app-pg",
		"namespace":   "app",
		"profile":     "prod",
		"backup": map[string]any{
			"enabled":       true,
			"bucketName":    "example-backups",
			"retentionDays": "thirty",
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "retentionDays")
}

// TestSchema_RejectsUnknownKey: an unknown top-level key must fail the
// render, not be silently ignored (policy's docs/contracts/component.md,
// C2: the schema is strict, so a typo fails at render instead of
// installing with a default nobody chose).
func TestSchema_RejectsUnknownKey(t *testing.T) {
	_, err := renderDocs(t, map[string]any{
		"clusterName": "app-pg",
		"namespace":   "app",
		"profile":     "devel",
		"bogusKey":    "typo of some kind",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "additional properties")
}

// TestSchema_RejectsBadProfileEnum: profile is constrained to
// devel|prod at the schema level too, ahead of the template's own
// `fail` — belt and suspenders, same message class either way.
func TestSchema_RejectsBadProfileEnum(t *testing.T) {
	_, err := renderDocs(t, map[string]any{
		"clusterName": "app-pg",
		"namespace":   "app",
		"profile":     "staging",
	})
	require.Error(t, err)
}

// TestSchema_AcceptsGlobal: Helm always passes `global` down to a
// subchart, and a strict schema that refuses it makes the chart
// unusable as a dependency ("additional properties 'global' not
// allowed") -- which is how every consumer embeds it.
func TestSchema_AcceptsGlobal(t *testing.T) {
	_, err := renderDocs(t, map[string]any{
		"clusterName": "app-pg",
		"namespace":   "app",
		"profile":     "devel",
		"global":      map[string]any{},
	})
	require.NoError(t, err)
}
