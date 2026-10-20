// Copyright 2026 Truvity B.V.. All rights reserved.
// SPDX-License-Identifier: MIT

package drill

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// Table is a user table of a database.
type Table struct {
	Schema string
	Name   string
	// Estimate is the planner's row estimate (pg_class.reltuples; -1 when
	// the table was never analyzed).
	Estimate int64
}

func (t Table) String() string { return t.Schema + "." + t.Name }

// Session is one connection to one database of the drill instance.
type Session interface {
	// InRecovery is pg_is_in_recovery().
	InRecovery(ctx context.Context) (bool, error)
	// Databases lists the databases that accept connections and are not
	// templates.
	Databases(ctx context.Context) ([]string, error)
	// Tables lists the user tables (plain tables and partitions; not
	// partitioned parents, system schemas or tables owned by an extension).
	Tables(ctx context.Context) ([]Table, error)
	// CountRows is SELECT count(*) of one table.
	CountRows(ctx context.Context, t Table) (int64, error)
	// BackupLabel is the content of backup_label.old in the data directory:
	// the label of the base backup the instance was restored from, which
	// PostgreSQL renames when recovery starts. ErrNotFound when absent.
	BackupLabel(ctx context.Context) (string, error)
	// Version is server_version.
	Version(ctx context.Context) (string, error)
	Close(ctx context.Context) error
}

// Prober opens Sessions.
type Prober interface {
	Connect(ctx context.Context, database string) (Session, error)
}

// ConnInfo is how the checks reach the drill instance.
type ConnInfo struct {
	Host       string
	Port       int
	ServerName string // the name the server certificate is verified for
	User       string
	Password   string
	CA         []byte // PEM
	// StatementTimeout bounds each statement.
	StatementTimeout time.Duration
}

// PGProber is the pgx implementation of Prober: TLS with the server
// certificate verified against the drill's own CA (verify-full).
type PGProber struct {
	info ConnInfo
	tls  *tls.Config
}

// NewPGProber builds a PGProber.
func NewPGProber(info ConnInfo) (*PGProber, error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(info.CA) {
		return nil, errors.New("the drill's CA Secret holds no PEM certificate")
	}
	return &PGProber{info: info, tls: &tls.Config{
		RootCAs:    pool,
		ServerName: info.ServerName,
		MinVersion: tls.VersionTLS12,
	}}, nil
}

// Connect implements Prober.
func (p *PGProber) Connect(ctx context.Context, database string) (Session, error) {
	// ParseConfig("") would read PG* variables and ~/.pgpass; every field
	// that matters is set below, and fallbacks are dropped.
	cfg, err := pgx.ParseConfig("")
	if err != nil {
		return nil, err
	}
	cfg.Host = p.info.Host
	cfg.Port = uint16(p.info.Port) //nolint:gosec // a TCP port
	cfg.User = p.info.User
	cfg.Password = p.info.Password
	cfg.Database = database
	cfg.TLSConfig = p.tls.Clone()
	cfg.Fallbacks = nil
	cfg.ConnectTimeout = 15 * time.Second
	cfg.RuntimeParams = map[string]string{
		"application_name":  "cnpgctl-drill",
		"statement_timeout": strconv.FormatInt(p.info.StatementTimeout.Milliseconds(), 10),
		// Never write: the drill only reads the restored data.
		"default_transaction_read_only": "on",
	}
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect to database %s: %w", database, err)
	}
	return &pgSession{conn: conn}, nil
}

type pgSession struct{ conn *pgx.Conn }

func (s *pgSession) InRecovery(ctx context.Context) (bool, error) {
	var v bool
	err := s.conn.QueryRow(ctx, "SELECT pg_catalog.pg_is_in_recovery()").Scan(&v)
	return v, err
}

func (s *pgSession) Databases(ctx context.Context) ([]string, error) {
	rows, err := s.conn.Query(ctx, "SELECT datname FROM pg_catalog.pg_database WHERE datallowconn AND NOT datistemplate ORDER BY 1")
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

const tablesQuery = `SELECT n.nspname, c.relname, c.reltuples::bigint
FROM pg_catalog.pg_class c
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE c.relkind = 'r'
  AND n.nspname NOT IN ('pg_catalog', 'information_schema')
  AND n.nspname NOT LIKE 'pg\_%'
  AND NOT EXISTS (
    SELECT 1 FROM pg_catalog.pg_depend d
    WHERE d.classid = 'pg_catalog.pg_class'::regclass AND d.objid = c.oid AND d.deptype = 'e')
ORDER BY 1, 2`

func (s *pgSession) Tables(ctx context.Context) ([]Table, error) {
	rows, err := s.conn.Query(ctx, tablesQuery)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Table, error) {
		var t Table
		err := r.Scan(&t.Schema, &t.Name, &t.Estimate)
		return t, err
	})
}

func (s *pgSession) CountRows(ctx context.Context, t Table) (int64, error) {
	var n int64
	q := "SELECT count(*) FROM " + pgx.Identifier{t.Schema, t.Name}.Sanitize()
	err := s.conn.QueryRow(ctx, q).Scan(&n)
	return n, err
}

func (s *pgSession) BackupLabel(ctx context.Context) (string, error) {
	var exists bool
	if err := s.conn.QueryRow(ctx, "SELECT (pg_catalog.pg_stat_file('backup_label.old', true)).size IS NOT NULL").Scan(&exists); err != nil {
		return "", err
	}
	if !exists {
		return "", fmt.Errorf("%w: backup_label.old", ErrNotFound)
	}
	var label string
	err := s.conn.QueryRow(ctx, "SELECT pg_catalog.pg_read_file('backup_label.old')").Scan(&label)
	return label, err
}

func (s *pgSession) Version(ctx context.Context) (string, error) {
	var v string
	err := s.conn.QueryRow(ctx, "SHOW server_version").Scan(&v)
	return v, err
}

func (s *pgSession) Close(ctx context.Context) error { return s.conn.Close(ctx) }
