// Copyright 2026 Truvity B.V.. All rights reserved.
// SPDX-License-Identifier: MIT

package drill

import (
	"bufio"
	"regexp"
	"strings"
	"time"
)

// PostgreSQL logs this line when it ends recovery, naming the commit time of
// the last transaction it replayed (xlogrecovery.c). There is no such line
// when no transaction was replayed after the base backup.
var lastTransactionLine = regexp.MustCompile(`last completed transaction was at log time ([0-9]{4}-[0-9]{2}-[0-9]{2} [0-9]{2}:[0-9]{2}:[0-9]{2}(?:\.[0-9]+)?(?:[+-][0-9]{2}(?::?[0-9]{2})?)?)`)

// ParseLastTransaction returns the commit time named by a recovery log line.
func ParseLastTransaction(line string) (time.Time, bool) {
	m := lastTransactionLine.FindStringSubmatch(line)
	if m == nil {
		return time.Time{}, false
	}
	for _, layout := range []string{
		"2006-01-02 15:04:05.999999999-07",
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999-0700",
		"2006-01-02 15:04:05.999999999",
	} {
		if t, err := time.Parse(layout, m[1]); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// ParseBackupStart returns the START TIME of a backup_label.
func ParseBackupStart(label string) (time.Time, bool) {
	sc := bufio.NewScanner(strings.NewReader(label))
	for sc.Scan() {
		v, ok := strings.CutPrefix(sc.Text(), "START TIME: ")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		for _, layout := range []string{"2006-01-02 15:04:05 MST", "2006-01-02 15:04:05 -0700", "2006-01-02 15:04:05"} {
			if t, err := time.Parse(layout, v); err == nil {
				return t, true
			}
		}
		return time.Time{}, false
	}
	return time.Time{}, false
}

// recoveryPoint is the later of the two, with its source.
func recoveryPoint(lastTx, backupStart time.Time) (time.Time, string) {
	switch {
	case lastTx.IsZero() && backupStart.IsZero():
		return time.Time{}, ""
	case lastTx.After(backupStart):
		return lastTx, "last replayed transaction"
	default:
		return backupStart, "base backup start (no later transaction replayed)"
	}
}
