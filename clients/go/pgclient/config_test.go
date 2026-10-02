package pgclient

import (
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

// The environment the cnpg-client chart's `env` helper renders.
func chartEnv() map[string]string {
	return map[string]string{
		"PGHOST":        "pg-rw.shop.svc",
		"PGPORT":        "5432",
		"PGDATABASE":    "app",
		"PGUSER":        "app_role",
		"PGSSLMODE":     "verify-full",
		"PGSSLROOTCERT": "/var/run/cnpg-client/ca.crt",
		"PGSSLCERT":     "/var/run/cnpg-client/tls.crt",
		"PGSSLKEY":      "/var/run/cnpg-client/tls.key",
	}
}

func TestFromEnvChartEnvironment(t *testing.T) {
	c, err := FromEnv(env(chartEnv()))
	require.NoError(t, err)
	assert.Equal(t, "pg-rw.shop.svc", c.Host)
	assert.Equal(t, uint16(5432), c.Port)
	assert.Equal(t, "app", c.Database)
	assert.Equal(t, "app_role", c.User)
	assert.Equal(t, "/var/run/cnpg-client/ca.crt", c.SSLRootCert)
	// Defaults of the contract.
	assert.Equal(t, 5*time.Second, c.ConnectTimeout)
	assert.Equal(t, 30*time.Second, c.StatementTimeout)
	assert.Equal(t, 60*time.Second, c.IdleInTxTimeout)
	assert.Equal(t, int32(10), c.MaxConns)
	assert.Equal(t, 30*time.Minute, c.MaxConnLifetime)
	assert.Equal(t, 5*time.Minute, c.MaxConnIdleTime)
	assert.Equal(t, 30*time.Second, c.HealthCheckPeriod)
	assert.Equal(t, DefaultRetryPolicy(), c.Retry)
	assert.NotEmpty(t, c.ApplicationName)
}

func TestFromEnvOverrides(t *testing.T) {
	e := chartEnv()
	e["PGAPPNAME"] = "urls"
	e["PGCONNECT_TIMEOUT"] = "3"
	e["CNPG_CLIENT_STATEMENT_TIMEOUT"] = "500ms"
	e["CNPG_CLIENT_IDLE_TX_TIMEOUT"] = "0s"
	e["CNPG_CLIENT_POOL_MAX"] = "4"
	e["CNPG_CLIENT_POOL_MIN"] = "1"
	e["CNPG_CLIENT_CONN_MAX_LIFETIME"] = "10m"
	e["CNPG_CLIENT_RETRY_ATTEMPTS"] = "7"
	e["CNPG_CLIENT_RETRY_MAX_DELAY"] = "1s"
	e["CNPG_CLIENT_RETRY_BUDGET"] = "9s"
	e["CNPG_CLIENT_PASSWORD_FILE"] = "/secrets/pw"
	c, err := FromEnv(env(e))
	require.NoError(t, err)
	assert.Equal(t, "urls", c.ApplicationName)
	assert.Equal(t, 3*time.Second, c.ConnectTimeout)
	assert.Equal(t, 500*time.Millisecond, c.StatementTimeout)
	assert.Zero(t, c.IdleInTxTimeout)
	assert.Equal(t, int32(4), c.MaxConns)
	assert.Equal(t, int32(1), c.MinConns)
	assert.Equal(t, 10*time.Minute, c.MaxConnLifetime)
	assert.Equal(t, RetryPolicy{Attempts: 7, InitialDelay: 200 * time.Millisecond, MaxDelay: time.Second, Budget: 9 * time.Second}, c.Retry)
	assert.Equal(t, "/secrets/pw", c.PasswordFile)
}

func TestFromEnvRefusesAnythingButVerifyFull(t *testing.T) {
	for _, mode := range []string{"disable", "allow", "prefer", "require", "verify-ca"} {
		e := chartEnv()
		e["PGSSLMODE"] = mode
		_, err := FromEnv(env(e))
		require.Error(t, err, mode)
		assert.Contains(t, err.Error(), "only verify-full is accepted", mode)
	}
}

func TestValidateReportsEveryProblem(t *testing.T) {
	err := Config{}.Validate()
	require.Error(t, err)
	for _, want := range []string{"host is required", "database is required", "user is required", "server CA file is required", "MaxConns"} {
		assert.Contains(t, err.Error(), want)
	}
}

func TestValidateRequiresTheCAAndPairedClientFiles(t *testing.T) {
	c := DefaultConfig()
	c.Host, c.Database, c.User = "h", "d", "u"
	require.ErrorContains(t, c.Validate(), "server CA file is required")
	c.SSLRootCert = "/ca"
	require.NoError(t, c.Validate())
	c.SSLCert = "/crt"
	require.ErrorContains(t, c.Validate(), "go together")
	c.Host = "a,b"
	require.ErrorContains(t, c.Validate(), "exactly one host")
}

func TestFromEnvReportsBadNumbers(t *testing.T) {
	e := chartEnv()
	e["PGPORT"] = "http"
	e["CNPG_CLIENT_POOL_MAX"] = "many"
	e["CNPG_CLIENT_CONN_MAX_LIFETIME"] = "soon"
	_, err := FromEnv(env(e))
	require.Error(t, err)
	for _, name := range []string{"PGPORT", "CNPG_CLIENT_POOL_MAX", "CNPG_CLIENT_CONN_MAX_LIFETIME"} {
		assert.Contains(t, err.Error(), name)
	}
}

func TestPasswordNeverPrinted(t *testing.T) {
	const secret = "s3cr3t-value"
	c := DefaultConfig()
	c.Host, c.Database, c.User, c.SSLRootCert, c.Password = "h", "d", "u", "/ca", secret
	for name, out := range map[string]string{
		"%v":  fmt.Sprintf("%v", c),
		"%+v": fmt.Sprintf("%+v", c),
		"%#v": fmt.Sprintf("%#v", c),
		"%s":  fmt.Sprintf("<%s>", c),
	} {
		assert.NotContains(t, out, secret, name)
	}
	var b strings.Builder
	slog.New(slog.NewTextHandler(&b, nil)).Info("cfg", "config", c)
	assert.NotContains(t, b.String(), secret)
	assert.Contains(t, b.String(), "redacted")
}

func TestConnURLCarriesNoPasswordAndEveryTLSFile(t *testing.T) {
	c := DefaultConfig()
	c.Host, c.Database, c.User, c.Password = "pg-rw.shop.svc", "app", "app role", "p@ss/word"
	c.SSLRootCert, c.SSLCert, c.SSLKey = "/m/ca.crt", "/m/tls.crt", "/m/tls.key"
	u := connURL(c)
	assert.NotContains(t, u, "p@ss")
	for _, want := range []string{"sslmode=verify-full", "sslrootcert=%2Fm%2Fca.crt", "sslcert=%2Fm%2Ftls.crt", "sslkey=%2Fm%2Ftls.key", "app%20role@pg-rw.shop.svc:5432/app"} {
		assert.Contains(t, u, want)
	}
}
