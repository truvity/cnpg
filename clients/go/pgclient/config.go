package pgclient

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// VerifyFull is the only TLS mode the adapter speaks.
const VerifyFull = "verify-full"

// Config is everything a connection needs. Start from [DefaultConfig] and
// override; a zero Config is invalid (no host, no CA).
//
// There is deliberately no free-form DSN: a parameter handed through a string
// can be dropped on the way to the driver, and a dropped sslrootcert turns
// verify-full into something that does not verify.
type Config struct {
	Host     string
	Port     uint16
	Database string
	User     string

	// Password is for a scram role. PasswordFile, when set, wins and is read
	// again for every new connection, so a rotated Secret is picked up.
	Password     string
	PasswordFile string

	// SSLRootCert is the server CA file. Required: the adapter trusts this
	// file and nothing else (not the system store).
	SSLRootCert string
	// SSLCert and SSLKey are the client certificate and key of a certificate
	// role, both or neither. Read again for every new connection.
	SSLCert string
	SSLKey  string
	// SSLMode exists to be refused: empty or "verify-full" only.
	SSLMode string

	ApplicationName string

	ConnectTimeout   time.Duration
	StatementTimeout time.Duration // 0 disables
	IdleInTxTimeout  time.Duration // 0 disables

	MaxConns          int32
	MinConns          int32
	MaxConnLifetime   time.Duration // keep below the client certificate's life
	MaxConnIdleTime   time.Duration
	HealthCheckPeriod time.Duration

	Retry RetryPolicy

	// Tracer receives query events; see the otelpg package.
	Tracer pgx.QueryTracer
}

// DefaultConfig returns the contract's defaults.
func DefaultConfig() Config {
	return Config{
		Port:              5432,
		ConnectTimeout:    5 * time.Second,
		StatementTimeout:  30 * time.Second,
		IdleInTxTimeout:   60 * time.Second,
		MaxConns:          10,
		MaxConnLifetime:   30 * time.Minute,
		MaxConnIdleTime:   5 * time.Minute,
		HealthCheckPeriod: 30 * time.Second,
		Retry:             DefaultRetryPolicy(),
	}
}

// Validate returns every problem it finds, not just the first.
func (c Config) Validate() error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf("pgclient: "+format, a...)) }

	if c.SSLMode != "" && c.SSLMode != VerifyFull {
		add("sslmode %q is refused; only %s is accepted", c.SSLMode, VerifyFull)
	}
	if c.Host == "" {
		add("host is required")
	}
	if strings.ContainsAny(c.Host, ", ") {
		add("host %q: exactly one host name is accepted (the cluster's rw Service)", c.Host)
	}
	if c.Port == 0 {
		add("port is required")
	}
	if c.Database == "" {
		add("database is required")
	}
	if c.User == "" {
		add("user is required")
	}
	if c.SSLRootCert == "" {
		add("the server CA file is required (PGSSLROOTCERT): verify-full has nothing to verify against without it")
	}
	if (c.SSLCert == "") != (c.SSLKey == "") {
		add("client certificate and key go together")
	}
	if c.MaxConns < 1 {
		add("MaxConns must be at least 1")
	}
	if c.MinConns < 0 || c.MinConns > c.MaxConns {
		add("MinConns must be between 0 and MaxConns")
	}
	if c.ConnectTimeout <= 0 {
		add("ConnectTimeout must be positive")
	}
	if c.StatementTimeout < 0 || c.IdleInTxTimeout < 0 {
		add("timeouts must not be negative")
	}
	if c.MaxConnLifetime <= 0 || c.MaxConnIdleTime <= 0 || c.HealthCheckPeriod <= 0 {
		add("MaxConnLifetime, MaxConnIdleTime and HealthCheckPeriod must be positive")
	}
	if err := c.Retry.validate(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// String redacts the password.
func (c Config) String() string {
	return fmt.Sprintf("pgclient.Config{host=%s port=%d database=%s user=%s password=%s sslmode=%s sslrootcert=%s sslcert=%s sslkey=%s application_name=%s}",
		c.Host, c.Port, c.Database, c.User, c.redactedPassword(), VerifyFull, c.SSLRootCert, c.SSLCert, c.SSLKey, c.ApplicationName)
}

// GoString redacts the password for %#v.
func (c Config) GoString() string { return c.String() }

// LogValue redacts the password for slog.
func (c Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("host", c.Host),
		slog.Int("port", int(c.Port)),
		slog.String("database", c.Database),
		slog.String("user", c.User),
		slog.String("password", c.redactedPassword()),
		slog.String("sslmode", VerifyFull),
		slog.String("application_name", c.ApplicationName),
	)
}

func (c Config) redactedPassword() string {
	if c.Password == "" && c.PasswordFile == "" {
		return "<none>"
	}
	return "<redacted>"
}

// FromEnv builds a Config from the contract's environment, starting from
// [DefaultConfig]. Pass os.Getenv, or a map lookup in tests. The result is
// validated.
func FromEnv(getenv func(string) string) (Config, error) {
	c := DefaultConfig()
	var errs []error
	str := func(name string, dst *string) {
		if v := getenv(name); v != "" {
			*dst = v
		}
	}
	dur := func(name string, dst *time.Duration) {
		v := getenv(name)
		if v == "" {
			return
		}
		d, err := time.ParseDuration(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("pgclient: %s=%q: %w", name, v, err))
			return
		}
		*dst = d
	}
	i32 := func(name string, dst *int32) {
		v := getenv(name)
		if v == "" {
			return
		}
		n, err := strconv.ParseInt(v, 10, 32)
		if err != nil {
			errs = append(errs, fmt.Errorf("pgclient: %s=%q: %w", name, v, err))
			return
		}
		*dst = int32(n)
	}

	str("PGHOST", &c.Host)
	if v := getenv("PGPORT"); v != "" {
		n, err := strconv.ParseUint(v, 10, 16)
		if err != nil {
			errs = append(errs, fmt.Errorf("pgclient: PGPORT=%q: %w", v, err))
		} else {
			c.Port = uint16(n)
		}
	}
	str("PGDATABASE", &c.Database)
	str("PGUSER", &c.User)
	str("PGSSLMODE", &c.SSLMode)
	str("PGSSLROOTCERT", &c.SSLRootCert)
	str("PGSSLCERT", &c.SSLCert)
	str("PGSSLKEY", &c.SSLKey)
	str("PGPASSWORD", &c.Password)
	str("CNPG_CLIENT_PASSWORD_FILE", &c.PasswordFile)
	str("PGAPPNAME", &c.ApplicationName)
	if v := getenv("PGCONNECT_TIMEOUT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			errs = append(errs, fmt.Errorf("pgclient: PGCONNECT_TIMEOUT=%q is not a positive number of seconds", v))
		} else {
			c.ConnectTimeout = time.Duration(n) * time.Second
		}
	}
	dur("CNPG_CLIENT_STATEMENT_TIMEOUT", &c.StatementTimeout)
	dur("CNPG_CLIENT_IDLE_TX_TIMEOUT", &c.IdleInTxTimeout)
	i32("CNPG_CLIENT_POOL_MAX", &c.MaxConns)
	i32("CNPG_CLIENT_POOL_MIN", &c.MinConns)
	dur("CNPG_CLIENT_CONN_MAX_LIFETIME", &c.MaxConnLifetime)
	dur("CNPG_CLIENT_CONN_MAX_IDLE", &c.MaxConnIdleTime)
	dur("CNPG_CLIENT_HEALTH_PERIOD", &c.HealthCheckPeriod)
	if v := getenv("CNPG_CLIENT_RETRY_ATTEMPTS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("pgclient: CNPG_CLIENT_RETRY_ATTEMPTS=%q: %w", v, err))
		} else {
			c.Retry.Attempts = n
		}
	}
	dur("CNPG_CLIENT_RETRY_MAX_DELAY", &c.Retry.MaxDelay)
	dur("CNPG_CLIENT_RETRY_BUDGET", &c.Retry.Budget)

	if c.ApplicationName == "" {
		c.ApplicationName = programName()
	}
	if len(errs) > 0 {
		return c, errors.Join(errs...)
	}
	return c, c.Validate()
}

func programName() string {
	if len(os.Args) == 0 {
		return "go"
	}
	name := os.Args[0]
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}
	if len(name) > 63 { // NAMEDATALEN-1
		name = name[:63]
	}
	return name
}
