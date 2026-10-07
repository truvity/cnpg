// Copyright 2026 Truvity B.V.. All rights reserved.
// SPDX-License-Identifier: MIT

package chart_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestScheduledBackup_Immediate: by default the ScheduledBackup takes its
// first base backup on creation. Without it a new cluster archives WAL
// with no base backup to replay it onto, so nothing is restorable until
// the first cron tick, up to a day later. backup.immediate=false leaves
// the field out (the operator's default, false).
func TestScheduledBackup_Immediate(t *testing.T) {
	for name, tc := range map[string]struct {
		backup map[string]any
		want   any
	}{
		"default":  {backup: map[string]any{}, want: true},
		"disabled": {backup: map[string]any{"immediate": false}, want: nil},
	} {
		t.Run(name, func(t *testing.T) {
			docs, err := renderDocs(t, storeValues(tc.backup, nil))
			require.NoError(t, err)
			require.Len(t, docs["ScheduledBackup"], 1)

			spec, _ := docs["ScheduledBackup"][0]["spec"].(map[string]any)
			assert.Equal(t, tc.want, spec["immediate"])
		})
	}
}
