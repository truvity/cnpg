// Copyright 2026 Truvity B.V.. All rights reserved.
// SPDX-License-Identifier: MIT

package drill

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// connInfo reads the drill Cluster's superuser and CA Secrets, which the
// operator creates for it (enableSuperuserAccess is on for the drill only).
func (d *driller) connInfo(ctx context.Context) (ConnInfo, error) {
	o := d.plan.Options
	su, err := d.k.GetSecret(ctx, o.DrillNamespace, o.DrillName+"-superuser")
	if err != nil {
		return ConnInfo{}, fmt.Errorf("read the drill's superuser Secret: %w", err)
	}
	ca, err := d.k.GetSecret(ctx, o.DrillNamespace, o.DrillName+"-ca")
	if err != nil {
		return ConnInfo{}, fmt.Errorf("read the drill's CA Secret: %w", err)
	}
	server := o.DrillName + "-rw." + o.DrillNamespace + ".svc"
	host := o.PGHost
	if host == "" {
		host = server
	}
	user := string(su.Data["username"])
	if user == "" {
		user = "postgres"
	}
	return ConnInfo{
		Host: host, Port: o.PGPort, ServerName: server,
		User: user, Password: string(su.Data["password"]),
		CA: ca.Data["ca.crt"], StatementTimeout: o.StatementTimeout,
	}, nil
}

// connect retries for a while: the read-write Service can lag the Cluster's
// healthy status by a few seconds.
func (d *driller) connect(ctx context.Context, p Prober, db string) (Session, error) {
	deadline := time.Now().Add(2 * time.Minute)
	for {
		s, err := p.Connect(ctx, db)
		if err == nil || time.Now().After(deadline) || ctx.Err() != nil {
			return s, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(d.plan.Options.PollInterval / 2):
		}
	}
}

// check runs every assertion against the promoted drill instance.
func (d *driller) check(ctx context.Context, rep *Report) {
	o := d.plan.Options
	info, err := d.connInfo(ctx)
	if err != nil {
		rep.fail("connect", "%v", err)
		return
	}
	prober, err := d.newProber(info)
	if err != nil {
		rep.fail("connect", "%v", err)
		return
	}
	admin, err := d.connect(ctx, prober, "postgres")
	if err != nil {
		rep.fail("connect", "%v", err)
		return
	}
	defer func() { _ = admin.Close(context.WithoutCancel(ctx)) }()
	rep.pass("connect", "TLS (verify-full) to %s:%d as %s", info.Host, info.Port, info.User)

	if v, err := admin.Version(ctx); err == nil {
		rep.ServerVersion = v
	}

	inRecovery, err := admin.InRecovery(ctx)
	switch {
	case err != nil:
		rep.fail("promoted", "pg_is_in_recovery(): %v", err)
	case inRecovery:
		rep.fail("promoted", "pg_is_in_recovery() is true: the instance never left recovery")
	default:
		rep.pass("promoted", "pg_is_in_recovery() is false")
	}

	d.recoveryPointCheck(ctx, admin, rep)

	present, err := admin.Databases(ctx)
	if err != nil {
		rep.fail("databases", "list databases: %v", err)
		return
	}
	expected := d.plan.Databases
	if len(expected) == 0 {
		for _, db := range present {
			if db != "postgres" {
				expected = append(expected, db)
			}
		}
		if len(expected) == 0 {
			rep.fail("databases", "the restored instance holds no database but postgres")
			return
		}
	}
	var missing []string
	for _, db := range expected {
		if !slices.Contains(present, db) {
			missing = append(missing, db)
		}
	}
	if len(missing) > 0 {
		rep.fail("databases", "missing %s (expected %s, from the %s; present %s)",
			strings.Join(missing, ", "), strings.Join(expected, ", "), d.plan.DatabasesSource, strings.Join(present, ", "))
	} else {
		rep.pass("databases", "%s present (expected from the %s)", strings.Join(expected, ", "), d.plan.DatabasesSource)
	}
	for _, db := range expected {
		if slices.Contains(missing, db) {
			continue
		}
		d.checkDatabase(ctx, prober, db, rep)
	}
	// A table minimum for a database that was not checked is a typo in the
	// configuration, not a pass.
	for _, key := range sortedKeys(o.MinRows) {
		if db, _, _ := strings.Cut(key, "/"); !slices.Contains(expected, db) {
			rep.fail("min rows "+key, "database %s is not among the expected databases", db)
		}
	}
}

func (d *driller) checkDatabase(ctx context.Context, prober Prober, db string, rep *Report) {
	o := d.plan.Options
	name := "database " + db
	s, err := d.connect(ctx, prober, db)
	if err != nil {
		rep.fail(name, "%v", err)
		return
	}
	defer func() { _ = s.Close(context.WithoutCancel(ctx)) }()
	tables, err := s.Tables(ctx)
	if err != nil {
		rep.fail(name, "list tables: %v", err)
		return
	}
	if len(tables) == 0 {
		rep.fail(name, "no user table")
		rep.Databases = append(rep.Databases, DatabaseResult{Name: db})
		return
	}
	var total int64
	counts := map[string]int64{}
	for _, t := range tables {
		var n int64
		if o.CountMode == CountEstimate {
			n = max(t.Estimate, 0)
		} else {
			n, err = s.CountRows(ctx, t)
			if err != nil {
				rep.fail(name, "count rows of %s: %v", t, err)
				return
			}
		}
		counts[t.String()] = n
		total += n
	}
	rep.Databases = append(rep.Databases, DatabaseResult{Name: db, Tables: len(tables), Rows: total})
	minTotal, ok := o.MinRows[db]
	if !ok {
		minTotal = 1
	}
	what := "rows"
	if o.CountMode == CountEstimate {
		what = "rows (estimated)"
	}
	if total < minTotal {
		rep.fail(name, "%d user tables, %d %s, fewer than the minimum %d", len(tables), total, what, minTotal)
	} else {
		rep.pass(name, "%d user tables, %d %s (minimum %d)", len(tables), total, what, minTotal)
	}
	for _, key := range sortedKeys(o.MinRows) {
		kdb, table, isTable := strings.Cut(key, "/")
		if !isTable || kdb != db {
			continue
		}
		n, found := counts[table]
		switch {
		case !found:
			rep.fail("min rows "+key, "table %s not found in %s", table, db)
		case n < o.MinRows[key]:
			rep.fail("min rows "+key, "%d %s, fewer than the minimum %d", n, what, o.MinRows[key])
		default:
			rep.pass("min rows "+key, "%d %s (minimum %d)", n, what, o.MinRows[key])
		}
	}
}

// recoveryPointCheck measures how old the recovered data is: now minus the
// later of the last transaction replayed (from the recovery log) and the start
// of the base backup restored (backup_label.old).
func (d *driller) recoveryPointCheck(ctx context.Context, admin Session, rep *Report) {
	o := d.plan.Options
	var backupStart time.Time
	label, err := admin.BackupLabel(ctx)
	switch {
	case err == nil:
		if t, ok := ParseBackupStart(label); ok {
			backupStart = t
		} else {
			d.logf("backup_label.old has no readable START TIME")
		}
	case errors.Is(err, ErrNotFound):
		d.logf("no backup_label.old in the restored data directory")
	default:
		d.logf("reading backup_label.old: %v", err)
	}
	point, source := recoveryPoint(d.log.lastTransaction(), backupStart)
	if point.IsZero() {
		rep.fail("recovery point", "unknown: no transaction time in the recovery log and no base backup label")
		return
	}
	age := o.Now().Sub(point)
	ageSeconds := age.Seconds()
	rep.RecoveryPoint, rep.RecoveryPointSource, rep.RecoveryPointAgeSeconds = &point, source, &ageSeconds
	msg := fmt.Sprintf("%s (%s), %s old, maximum %s", point.UTC().Format(time.RFC3339), source, age.Round(time.Second), o.MaxRecoveryPointAge)
	if age > o.MaxRecoveryPointAge {
		rep.fail("recovery point", "%s", msg)
	} else {
		rep.pass("recovery point", "%s", msg)
	}
}
