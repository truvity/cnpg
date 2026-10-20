// Copyright 2026 Truvity B.V.. All rights reserved.
// SPDX-License-Identifier: MIT

package drill

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/truvity/cnpg/v2/pkg/verify"
)

// The step statuses (the same words cnpgctl verify prints).
const (
	Pass = verify.Pass
	Fail = verify.Fail
	Skip = verify.Skip
)

// Step is one assertion or stage of a drill.
type Step struct {
	Name    string        `json:"name"`
	Status  verify.Status `json:"status"`
	Message string        `json:"message"`
}

// DatabaseResult is what the checks found in one restored database.
type DatabaseResult struct {
	Name   string `json:"name"`
	Tables int    `json:"tables"`
	// Rows is the exact count, or the estimate in estimate mode.
	Rows int64 `json:"rows"`
}

// Report is the outcome of one drill.
type Report struct {
	SourceNamespace string `json:"sourceNamespace"`
	SourceCluster   string `json:"sourceCluster"`
	DrillNamespace  string `json:"drillNamespace"`
	DrillName       string `json:"drillName"`
	DestinationPath string `json:"destinationPath,omitempty"`
	ServerName      string `json:"serverName,omitempty"`

	StartedAt       time.Time `json:"startedAt"`
	FinishedAt      time.Time `json:"finishedAt"`
	DurationSeconds float64   `json:"durationSeconds"`

	// RecoveryPoint is the later of the last replayed transaction's commit
	// time and the base backup's start; RecoveryPointSource says which.
	RecoveryPoint           *time.Time `json:"recoveryPoint,omitempty"`
	RecoveryPointSource     string     `json:"recoveryPointSource,omitempty"`
	RecoveryPointAgeSeconds *float64   `json:"recoveryPointAgeSeconds,omitempty"`
	ServerVersion           string     `json:"serverVersion,omitempty"`

	Databases []DatabaseResult `json:"databases,omitempty"`
	Steps     []Step           `json:"steps"`
}

func (r *Report) add(name string, status verify.Status, format string, args ...any) {
	r.Steps = append(r.Steps, Step{Name: name, Status: status, Message: fmt.Sprintf(format, args...)})
}

func (r *Report) pass(name, format string, args ...any) { r.add(name, Pass, format, args...) }
func (r *Report) fail(name, format string, args ...any) { r.add(name, Fail, format, args...) }

// Failed reports whether any step failed.
func (r *Report) Failed() bool {
	for _, s := range r.Steps {
		if s.Status == Fail {
			return true
		}
	}
	return false
}

// FirstFailure is the first failed step, "" when none.
func (r *Report) FirstFailure() string {
	for _, s := range r.Steps {
		if s.Status == Fail {
			return s.Name + ": " + s.Message
		}
	}
	return ""
}

// WriteText prints one line per step and a summary.
func WriteText(w io.Writer, r *Report) {
	_, _ = fmt.Fprintf(w, "== cnpgctl drill: %s/%s -> %s/%s\n", r.SourceNamespace, r.SourceCluster, r.DrillNamespace, r.DrillName)
	var p, f, s int
	for _, x := range r.Steps {
		switch x.Status {
		case Pass:
			p++
		case Fail:
			f++
		case Skip:
			s++
		}
		_, _ = fmt.Fprintf(w, "  %-4s  %s: %s\n", x.Status, x.Name, x.Message)
	}
	result := "SUCCEEDED"
	if r.Failed() {
		result = "FAILED"
	}
	_, _ = fmt.Fprintf(w, "== drill %s in %s: %d passed, %d failed, %d skipped\n",
		result, time.Duration(r.DurationSeconds*float64(time.Second)).Round(time.Second), p, f, s)
}

// WriteJSON prints the Report as indented JSON.
func WriteJSON(w io.Writer, r *Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}
