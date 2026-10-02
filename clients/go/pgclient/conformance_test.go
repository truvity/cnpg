package pgclient_test

// The conformance suite: every case in clients/conformance/cases.txt, against a
// real PostgreSQL that serves TLS (clients/conformance/pg-tls.sh). The case
// names are the subtest names; the CI guard fails unless each one ran and
// passed. Without CNPG_CLIENTS_PG_HOST the suite skips, or, with
// CNPG_CLIENTS_PG=required, fails.

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/truvity/cnpg/v2/clients/go/pgclient"
	"github.com/truvity/cnpg/v2/clients/go/pgclient/otelpg"
)

type server struct {
	host, addr, database, dir, pwRole, pw string
	port                                  uint16
}

func harness(t *testing.T) server {
	t.Helper()
	host := os.Getenv("CNPG_CLIENTS_PG_HOST")
	if host == "" {
		if os.Getenv("CNPG_CLIENTS_PG") == "required" {
			t.Fatal("CNPG_CLIENTS_PG=required but no TLS PostgreSQL is configured (clients/conformance/pg-tls.sh up)")
		}
		t.Skip("no TLS PostgreSQL (clients/conformance/pg-tls.sh up)")
	}
	port, err := strconv.ParseUint(os.Getenv("CNPG_CLIENTS_PG_PORT"), 10, 16)
	require.NoError(t, err)
	return server{
		host: host, addr: os.Getenv("CNPG_CLIENTS_PG_ADDR"), port: uint16(port),
		database: os.Getenv("CNPG_CLIENTS_PG_DATABASE"), dir: os.Getenv("CNPG_CLIENTS_PG_DIR"),
		pwRole: os.Getenv("CNPG_CLIENTS_PG_PW_ROLE"), pw: os.Getenv("CNPG_CLIENTS_PG_PW_PASSWORD"),
	}
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	b, err := os.ReadFile(from)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(to, b, 0o600))
}

// mount lays the files out as the cnpg-client chart does: ca.crt, tls.crt, tls.key.
func (s server) mount(t *testing.T, cert string) string {
	t.Helper()
	m := t.TempDir()
	copyFile(t, filepath.Join(s.dir, "ca.crt"), filepath.Join(m, "ca.crt"))
	s.setCert(t, m, cert)
	return m
}

func (s server) setCert(t *testing.T, mount, cert string) {
	t.Helper()
	copyFile(t, filepath.Join(s.dir, cert+".crt"), filepath.Join(mount, "tls.crt"))
	copyFile(t, filepath.Join(s.dir, cert+".key"), filepath.Join(mount, "tls.key"))
}

func (s server) certConfig(mount string) pgclient.Config {
	c := pgclient.DefaultConfig()
	c.Host, c.Port, c.Database, c.User = s.host, s.port, s.database, "app_cert"
	c.SSLRootCert = filepath.Join(mount, "ca.crt")
	c.SSLCert = filepath.Join(mount, "tls.crt")
	c.SSLKey = filepath.Join(mount, "tls.key")
	c.ApplicationName = "conformance"
	c.Retry = pgclient.RetryPolicy{Attempts: 3, InitialDelay: 20 * time.Millisecond, MaxDelay: 100 * time.Millisecond, Budget: 5 * time.Second}
	return c
}

func (s server) passwordConfig(mount string) pgclient.Config {
	c := s.certConfig(mount)
	c.User, c.Password = s.pwRole, s.pw
	c.SSLCert, c.SSLKey = "", ""
	return c
}

func open(t *testing.T, c pgclient.Config) *pgclient.Pool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	p, err := pgclient.New(ctx, c)
	require.NoError(t, err)
	t.Cleanup(p.Close)
	return p
}

func scalar[T any](t *testing.T, p *pgclient.Pool, sql string, args ...any) T {
	t.Helper()
	var v T
	require.NoError(t, p.QueryRow(context.Background(), sql, args...).Scan(&v))
	return v
}

// forwarder is a TCP forwarder that can be dropped and restored, standing in
// for the rw Service while the primary behind it switches.
type forwarder struct {
	target string
	addr   string
	mu     sync.Mutex
	ln     net.Listener
	conns  []net.Conn
}

func newForwarder(t *testing.T, target string) *forwarder {
	t.Helper()
	f := &forwarder{target: target}
	f.up(t, "127.0.0.1:0")
	t.Cleanup(f.down)
	return f
}

func (f *forwarder) up(t *testing.T, addr string) {
	t.Helper()
	ln, err := net.Listen("tcp", addr)
	require.NoError(t, err)
	f.mu.Lock()
	f.ln, f.addr = ln, ln.Addr().String()
	f.mu.Unlock()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			up, err := net.Dial("tcp", f.target)
			if err != nil {
				_ = c.Close()
				continue
			}
			f.mu.Lock()
			f.conns = append(f.conns, c, up)
			f.mu.Unlock()
			go func() { _, _ = io.Copy(up, c); _ = up.Close() }()
			go func() { _, _ = io.Copy(c, up); _ = c.Close() }()
		}
	}()
}

func (f *forwarder) down() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ln != nil {
		_ = f.ln.Close()
	}
	for _, c := range f.conns {
		_ = c.Close()
	}
	f.conns = nil
}

func (f *forwarder) port() uint16 {
	_, p, _ := net.SplitHostPort(f.addr)
	n, _ := strconv.ParseUint(p, 10, 16)
	return uint16(n)
}

func TestConformance(t *testing.T) {
	s := harness(t)

	t.Run("verify-full-connects", func(t *testing.T) {
		p := open(t, s.certConfig(s.mount(t, "app_cert.1")))
		assert.True(t, scalar[bool](t, p, "select ssl from pg_stat_ssl where pid = pg_backend_pid()"))
		assert.Equal(t, "conformance", scalar[string](t, p, "select current_setting('application_name')"))
		assert.Equal(t, "30s", scalar[string](t, p, "select current_setting('statement_timeout')"))
		assert.Equal(t, "1min", scalar[string](t, p, "select current_setting('idle_in_transaction_session_timeout')"))
		assert.NoError(t, p.Health(context.Background()))
	})

	t.Run("rejects-unknown-ca", func(t *testing.T) {
		m := s.mount(t, "app_cert.1")
		copyFile(t, filepath.Join(s.dir, "other-ca.crt"), filepath.Join(m, "ca.crt"))
		_, err := pgclient.New(context.Background(), s.certConfig(m))
		require.Error(t, err)
		t.Logf("unknown CA: %v", err)
		assert.False(t, pgclient.IsRetryable(err), "a second try cannot fix a certificate: %v", err)
	})

	t.Run("rejects-hostname-mismatch", func(t *testing.T) {
		c := s.certConfig(s.mount(t, "app_cert.1"))
		c.Host = s.addr // the certificate carries the name localhost, not this address
		_, err := pgclient.New(context.Background(), c)
		require.Error(t, err)
		assert.False(t, pgclient.IsRetryable(err), "%v", err)
	})

	t.Run("refuses-weaker-sslmode", func(t *testing.T) {
		m := s.mount(t, "app_cert.1")
		for _, mode := range []string{"disable", "allow", "prefer", "require", "verify-ca"} {
			c := s.certConfig(m)
			c.SSLMode = mode
			_, err := pgclient.New(context.Background(), c)
			require.Error(t, err, mode)
			assert.Contains(t, err.Error(), "only verify-full is accepted", mode)
		}
		c := s.certConfig(m)
		c.SSLRootCert = ""
		_, err := pgclient.New(context.Background(), c)
		require.ErrorContains(t, err, "server CA file is required")
	})

	t.Run("password-role", func(t *testing.T) {
		m := s.mount(t, "app_cert.1")
		p := open(t, s.passwordConfig(m))
		assert.Equal(t, s.pwRole, scalar[string](t, p, "select current_user"))

		c := s.passwordConfig(m)
		c.Password = "wrong"
		_, err := pgclient.New(context.Background(), c)
		var pgErr *pgconn.PgError
		require.ErrorAs(t, err, &pgErr)
		assert.Equal(t, "28P01", pgErr.Code)
		assert.False(t, pgclient.IsRetryable(err))
	})

	t.Run("client-cert-role", func(t *testing.T) {
		p := open(t, s.certConfig(s.mount(t, "app_cert.1")))
		assert.Equal(t, "app_cert", scalar[string](t, p, "select current_user"))

		// The right name from the wrong authority is refused, and not retried.
		m := s.mount(t, "app_cert.1")
		copyFile(t, filepath.Join(s.dir, "foreign.crt"), filepath.Join(m, "tls.crt"))
		copyFile(t, filepath.Join(s.dir, "foreign.key"), filepath.Join(m, "tls.key"))
		start := time.Now()
		_, err := pgclient.New(context.Background(), s.certConfig(m))
		require.Error(t, err)
		t.Logf("foreign client certificate: %v", err)
		assert.False(t, pgclient.IsRetryable(err), "%v", err)
		assert.Less(t, time.Since(start), 2*time.Second)
	})

	t.Run("client-cert-rotation", func(t *testing.T) {
		m := s.mount(t, "app_cert.1")
		c := s.certConfig(m)
		c.MaxConnLifetime = 400 * time.Millisecond
		c.HealthCheckPeriod = 100 * time.Millisecond
		p := open(t, c)
		serial := func() int64 {
			return scalar[int64](t, p, "select client_serial from pg_stat_ssl where pid = pg_backend_pid()")
		}
		assert.Equal(t, int64(0x101), serial())
		s.setCert(t, m, "app_cert.2") // what cert-manager's renewal does to the mounted files
		deadline := time.Now().Add(10 * time.Second)
		for serial() != 0x202 {
			require.True(t, time.Now().Before(deadline), "the renewed certificate was never picked up")
			time.Sleep(150 * time.Millisecond)
		}
	})

	t.Run("password-file-rotation", func(t *testing.T) {
		m := s.mount(t, "app_cert.1")
		file := filepath.Join(t.TempDir(), "password")
		write := func(pw string) { require.NoError(t, os.WriteFile(file, []byte(pw+"\n"), 0o600)) }
		write(s.pw)
		c := s.passwordConfig(m)
		c.Password, c.PasswordFile = "", file
		p := open(t, c)
		assert.Equal(t, s.pwRole, scalar[string](t, p, "select current_user"))

		const next = "rotated-password"
		_, err := p.Exec(context.Background(), "alter role "+s.pwRole+" password '"+next+"'")
		require.NoError(t, err)
		t.Cleanup(func() {
			write(next)
			c2 := s.passwordConfig(m)
			c2.Password = next
			q := open(t, c2)
			_, _ = q.Exec(context.Background(), "alter role "+s.pwRole+" password '"+s.pw+"'")
		})
		write(next)
		p.Reset() // drop the pooled connections, as the connection lifetime eventually does
		assert.Equal(t, s.pwRole, scalar[string](t, p, "select current_user"))
	})

	t.Run("reconnects-after-backend-termination", func(t *testing.T) {
		c := s.certConfig(s.mount(t, "app_cert.1"))
		c.MaxConns = 2
		p := open(t, c)
		ctx := context.Background()
		victim, err := p.Acquire(ctx)
		require.NoError(t, err)
		var victimPID int32
		require.NoError(t, victim.QueryRow(ctx, "select pg_backend_pid()").Scan(&victimPID))
		var ok bool
		require.NoError(t, p.QueryRow(ctx, "select pg_terminate_backend($1)", victimPID).Scan(&ok))
		require.True(t, ok)
		victim.Release()

		attempts := 0
		require.NoError(t, p.Do(ctx, func(ctx context.Context) error {
			attempts++
			var one int
			return p.QueryRow(ctx, "select 1").Scan(&one)
		}))
		assert.NoError(t, p.Health(ctx))
	})

	t.Run("survives-primary-switch", func(t *testing.T) {
		f := newForwarder(t, net.JoinHostPort(s.addr, strconv.Itoa(int(s.port))))
		c := s.certConfig(s.mount(t, "app_cert.1"))
		c.Port = f.port() // the "Service": same name, certificate unchanged
		c.Retry = pgclient.RetryPolicy{Attempts: 20, InitialDelay: 50 * time.Millisecond, MaxDelay: 500 * time.Millisecond, Budget: 15 * time.Second}
		p := open(t, c)
		assert.Equal(t, "app_cert", scalar[string](t, p, "select current_user"))

		addr := f.addr
		f.down() // the old primary is gone and nothing answers yet
		go func() { time.Sleep(1500 * time.Millisecond); f.up(t, addr) }()

		attempts := 0
		err := p.Do(context.Background(), func(ctx context.Context) error {
			attempts++
			var u string
			return p.QueryRow(ctx, "select current_user").Scan(&u)
		})
		require.NoError(t, err)
		assert.Greater(t, attempts, 1, "the first try must have met the switch")
	})

	t.Run("statement-timeout-enforced", func(t *testing.T) {
		c := s.certConfig(s.mount(t, "app_cert.1"))
		c.StatementTimeout = 300 * time.Millisecond
		p := open(t, c)
		start := time.Now()
		attempts := 0
		err := p.Do(context.Background(), func(ctx context.Context) error {
			attempts++
			_, err := p.Exec(ctx, "select pg_sleep(5)")
			return err
		})
		var pgErr *pgconn.PgError
		require.ErrorAs(t, err, &pgErr)
		assert.Equal(t, "57014", pgErr.Code)
		assert.Equal(t, 1, attempts, "a timeout is not a connection failure")
		assert.Less(t, time.Since(start), 3*time.Second)
	})

	t.Run("permanent-error-not-retried", func(t *testing.T) {
		p := open(t, s.certConfig(s.mount(t, "app_cert.1")))
		ctx := context.Background()
		_, err := p.Exec(ctx, "drop table if exists cnpg_clients_conf; create table cnpg_clients_conf (id int primary key); insert into cnpg_clients_conf values (1)")
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = p.Exec(context.Background(), "drop table if exists cnpg_clients_conf") })

		attempts := 0
		err = p.Do(ctx, func(ctx context.Context) error {
			attempts++
			_, err := p.Exec(ctx, "insert into cnpg_clients_conf values (1)")
			return err
		})
		var pgErr *pgconn.PgError
		require.ErrorAs(t, err, &pgErr)
		assert.Equal(t, "23505", pgErr.Code)
		assert.Equal(t, 1, attempts)
	})

	t.Run("health-check", func(t *testing.T) {
		f := newForwarder(t, net.JoinHostPort(s.addr, strconv.Itoa(int(s.port))))
		c := s.certConfig(s.mount(t, "app_cert.1"))
		c.Port = f.port()
		p := open(t, c)
		require.NoError(t, p.Health(context.Background()))
		f.down()
		start := time.Now()
		require.Error(t, p.Health(context.Background()))
		assert.Less(t, time.Since(start), pgclient.HealthTimeout+time.Second)
	})

	t.Run("traces-statements-without-arguments", func(t *testing.T) {
		rec := tracetest.NewSpanRecorder()
		tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
		c := s.certConfig(s.mount(t, "app_cert.1"))
		c.Tracer = otelpg.New(tp)
		p := open(t, c)
		const secret = "user-data-that-must-not-be-traced"
		require.Equal(t, secret, scalar[string](t, p, "select $1::text", secret))

		var found bool
		for _, sp := range rec.Ended() {
			for _, kv := range sp.Attributes() {
				assert.NotContains(t, kv.Value.String(), secret, "an argument reached a span attribute")
				if string(kv.Key) == "db.query.text" && kv.Value.String() == "select $1::text" {
					found = true
				}
			}
		}
		assert.True(t, found, "the statement text, with its placeholder, is on a span")
	})
}
