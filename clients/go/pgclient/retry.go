package pgclient

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// RetryPolicy bounds the retry of connection-class failures: exponential
// backoff with full jitter, stopping at Attempts or Budget, whichever is
// first.
type RetryPolicy struct {
	Attempts     int           // total tries, including the first
	InitialDelay time.Duration // ceiling of the first sleep
	MaxDelay     time.Duration // ceiling of any sleep
	Budget       time.Duration // total time that may be spent waiting

	// OnRetry, when set, is called before each sleep with the attempt that
	// just failed (1-based), its error and the delay about to be slept. Use
	// it to log. It is not part of the policy's validity.
	OnRetry func(attempt int, err error, delay time.Duration)
}

// DefaultRetryPolicy is 5 tries, 200ms doubling to 5s, 30s of waiting.
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{Attempts: 5, InitialDelay: 200 * time.Millisecond, MaxDelay: 5 * time.Second, Budget: 30 * time.Second}
}

func (p RetryPolicy) validate() error {
	if p.Attempts < 1 || p.InitialDelay <= 0 || p.MaxDelay < p.InitialDelay || p.Budget <= 0 {
		return fmt.Errorf("pgclient: retry policy %+v: Attempts>=1, 0<InitialDelay<=MaxDelay, Budget>0", p)
	}
	return nil
}

// Retry runs fn, repeating it while it fails with a retryable error (see
// [IsRetryable]). fn must be safe to run again: a statement that may have
// committed before the connection broke is the caller's to reason about,
// which is why the unit of retry is the caller's whole function.
func Retry(ctx context.Context, p RetryPolicy, fn func(context.Context) error) error {
	if err := p.validate(); err != nil {
		return err
	}
	start := time.Now()
	var err error
	for attempt := 1; ; attempt++ {
		if err = fn(ctx); err == nil {
			return nil
		}
		if ctx.Err() != nil || !IsRetryable(err) || attempt >= p.Attempts {
			return err
		}
		delay := backoff(p, attempt)
		if time.Since(start)+delay > p.Budget {
			return err
		}
		if p.OnRetry != nil {
			p.OnRetry(attempt, err, delay)
		}
		t := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			t.Stop()
			return err
		case <-t.C:
		}
	}
}

func backoff(p RetryPolicy, attempt int) time.Duration {
	ceil := p.InitialDelay
	for i := 1; i < attempt && ceil < p.MaxDelay; i++ {
		ceil *= 2
	}
	ceil = min(ceil, p.MaxDelay)
	return time.Duration(rand.Int64N(int64(ceil) + 1))
}

// IsRetryable reports whether err means the connection (not the statement)
// failed: the server went away, was demoted, or is not accepting yet. Those
// are what a CloudNativePG switchover or crash looks like to a client.
//
// Never retryable: authentication and permission errors, constraint and
// syntax errors, a statement timeout (57014), a serialization failure
// (40001, 40P01: the caller retries its transaction) and certificate
// verification failures, which a second try cannot fix.
func IsRetryable(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	var attemptTimeout *attemptTimeoutError
	if errors.As(err, &attemptTimeout) {
		return true
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return retryableCode(pgErr.Code)
	}
	var (
		unknown  x509.UnknownAuthorityError
		hostname x509.HostnameError
		invalid  x509.CertificateInvalidError
	)
	var verify *tls.CertificateVerificationError
	if errors.As(err, &unknown) || errors.As(err, &hostname) || errors.As(err, &invalid) || errors.As(err, &verify) {
		return false
	}
	var connErr *pgconn.ConnectError
	if errors.As(err, &connErr) {
		// Dial failures and connect timeouts. A TLS verification failure was
		// excluded above; what is left is the server not being reachable.
		return true
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return false // the caller's own deadline
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	return pgconn.SafeToRetry(err)
}

func retryableCode(code string) bool {
	if strings.HasPrefix(code, "08") { // connection exception
		return true
	}
	switch code {
	case "57P01", "57P02", "57P03", // admin shutdown, crash shutdown, cannot connect now
		"25006", // read_only_sql_transaction: a demoted primary still answering
		"53300": // too_many_connections: a new primary still filling
		return true
	}
	return false
}
