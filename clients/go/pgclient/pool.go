package pgclient

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// HealthTimeout bounds [Pool.Health].
const HealthTimeout = 2 * time.Second

// Pool is a pgx pool with the contract's behaviour. The embedded
// *pgxpool.Pool is the full pgx API (Query, Exec, Begin, Stat, Close, and
// stdlib.OpenDBFromPool for gorm and database/sql).
type Pool struct {
	*pgxpool.Pool
	cfg Config
}

// New validates cfg, builds the pool and proves the first connection, retrying
// it under cfg.Retry (a startup race with the Service is normal).
func New(ctx context.Context, cfg Config) (*Pool, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	pc, err := buildPoolConfig(cfg)
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return nil, fmt.Errorf("pgclient: create pool: %w", err)
	}
	p := &Pool{Pool: pool, cfg: cfg}
	if err := Retry(ctx, cfg.Retry, p.Health); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pgclient: first connection to %s:%d: %w", cfg.Host, cfg.Port, err)
	}
	return p, nil
}

// Health runs SELECT 1 on a pooled connection, bounded by [HealthTimeout].
// It exercises the real path (Service, TLS, authentication), so it suits a
// readiness probe.
func (p *Pool) Health(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, HealthTimeout)
	defer cancel()
	var one int
	if err := p.QueryRow(ctx, "select 1").Scan(&one); err != nil {
		return fmt.Errorf("pgclient: health: %w", err)
	}
	return nil
}

// Do runs fn under the pool's retry policy; see [Retry].
func (p *Pool) Do(ctx context.Context, fn func(context.Context) error) error {
	return Retry(ctx, p.cfg.Retry, fn)
}

// Config returns the configuration the pool was built from.
func (p *Pool) Config() Config { return p.cfg }

// connConfig reads the files NOW and returns a pgx config for one connection.
// Called at start and again for every new connection.
func connConfig(c Config) (*pgx.ConnConfig, error) {
	cc, err := pgx.ParseConfig(connURL(c))
	if err != nil {
		return nil, redact(c, fmt.Errorf("pgclient: %w", err))
	}
	return finishConnConfig(c, cc)
}

// connURL carries only what the contract names; the password never goes in it.
func connURL(c Config) string {
	u := url.URL{
		Scheme: "postgres",
		Host:   net.JoinHostPort(c.Host, strconv.Itoa(int(c.Port))),
		Path:   "/" + c.Database,
		User:   url.User(c.User),
	}
	q := url.Values{}
	q.Set("sslmode", VerifyFull)
	q.Set("sslrootcert", c.SSLRootCert)
	if c.SSLCert != "" {
		q.Set("sslcert", c.SSLCert)
		q.Set("sslkey", c.SSLKey)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func finishConnConfig(c Config, cc *pgx.ConnConfig) (*pgx.ConnConfig, error) {
	// pgx falls back to PG* environment and ~/.pgpass for anything unset; what
	// the caller configured is what is used.
	if len(cc.Fallbacks) != 0 || cc.TLSConfig == nil || cc.TLSConfig.InsecureSkipVerify || cc.TLSConfig.ServerName == "" || cc.TLSConfig.RootCAs == nil {
		return nil, errors.New("pgclient: the driver did not produce a verify-full configuration; refusing to connect")
	}
	cc.ConnectTimeout = c.ConnectTimeout
	if c.ApplicationName != "" {
		cc.RuntimeParams["application_name"] = c.ApplicationName
	}
	cc.RuntimeParams["statement_timeout"] = strconv.FormatInt(c.StatementTimeout.Milliseconds(), 10)
	cc.RuntimeParams["idle_in_transaction_session_timeout"] = strconv.FormatInt(c.IdleInTxTimeout.Milliseconds(), 10)

	cc.Password = c.Password
	if c.PasswordFile != "" {
		b, err := os.ReadFile(c.PasswordFile)
		if err != nil {
			return nil, fmt.Errorf("pgclient: read password file: %w", err)
		}
		cc.Password = strings.TrimRight(string(b), "\r\n")
	}
	cc.Tracer = c.Tracer
	return cc, nil
}

// redact keeps a password out of an error that quotes the URL.
func redact(c Config, err error) error {
	msg := err.Error()
	if c.Password != "" {
		msg = strings.ReplaceAll(msg, c.Password, "<redacted>")
	}
	return errors.New(msg)
}

func buildPoolConfig(c Config) (*pgxpool.Config, error) {
	pc, err := pgxpool.ParseConfig(connURL(c))
	if err != nil {
		return nil, redact(c, fmt.Errorf("pgclient: %w", err))
	}
	cc, err := finishConnConfig(c, pc.ConnConfig)
	if err != nil {
		return nil, err
	}
	pc.ConnConfig = cc
	pc.MaxConns = c.MaxConns
	pc.MinConns = c.MinConns
	pc.MaxConnLifetime = c.MaxConnLifetime
	pc.MaxConnLifetimeJitter = c.MaxConnLifetime / 10
	pc.MaxConnIdleTime = c.MaxConnIdleTime
	pc.HealthCheckPeriod = c.HealthCheckPeriod
	// Every new physical connection re-reads the certificate, key, CA and
	// password files, so a renewed Secret is used by the next connection and
	// no tls.Config built once at start holds the first certificate forever.
	pc.BeforeConnect = func(_ context.Context, conn *pgx.ConnConfig) error {
		fresh, err := connConfig(c)
		if err != nil {
			return err
		}
		conn.TLSConfig = fresh.TLSConfig
		conn.Password = fresh.Password
		return nil
	}
	return pc, nil
}
