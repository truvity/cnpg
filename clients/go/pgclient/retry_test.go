package pgclient

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsRetryable(t *testing.T) {
	pg := func(code string) error { return &pgconn.PgError{Code: code} }
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"admin shutdown (switchover)", pg("57P01"), true},
		{"crash shutdown", pg("57P02"), true},
		{"cannot connect now", pg("57P03"), true},
		{"connection failure", pg("08006"), true},
		{"demoted primary, read-only", pg("25006"), true},
		{"too many connections", pg("53300"), true},
		{"unique violation", pg("23505"), false},
		{"syntax", pg("42601"), false},
		{"permission", pg("42501"), false},
		{"bad password", pg("28P01"), false},
		{"statement timeout", pg("57014"), false},
		{"serialization failure", pg("40001"), false},
		{"deadlock", pg("40P01"), false},
		{"EOF", io.EOF, true},
		{"unexpected EOF wrapped", fmt.Errorf("failed to receive message: %w", io.ErrUnexpectedEOF), true},
		{"connection reset", fmt.Errorf("write: %w", syscall.ECONNRESET), true},
		{"unknown authority", x509.UnknownAuthorityError{}, false},
		{"hostname mismatch", x509.HostnameError{Host: "x"}, false},
		{"canceled", context.Canceled, false},
		{"caller deadline", context.DeadlineExceeded, false},
		{"plain error", errors.New("boom"), false},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, IsRetryable(c.err), c.name)
	}
}

func fast() RetryPolicy {
	return RetryPolicy{Attempts: 4, InitialDelay: time.Millisecond, MaxDelay: 4 * time.Millisecond, Budget: time.Second}
}

func TestRetryStopsAtAttempts(t *testing.T) {
	n := 0
	err := Retry(context.Background(), fast(), func(context.Context) error { n++; return io.EOF })
	assert.ErrorIs(t, err, io.EOF)
	assert.Equal(t, 4, n)
}

func TestRetrySucceedsLater(t *testing.T) {
	n := 0
	err := Retry(context.Background(), fast(), func(context.Context) error {
		n++
		if n < 3 {
			return &pgconn.PgError{Code: "57P01"}
		}
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, 3, n)
}

func TestRetryNeverRepeatsAPermanentError(t *testing.T) {
	n := 0
	err := Retry(context.Background(), fast(), func(context.Context) error { n++; return &pgconn.PgError{Code: "23505"} })
	require.Error(t, err)
	assert.Equal(t, 1, n)
}

func TestRetryHonoursBudgetAndContext(t *testing.T) {
	p := RetryPolicy{Attempts: 100, InitialDelay: 50 * time.Millisecond, MaxDelay: 50 * time.Millisecond, Budget: 120 * time.Millisecond}
	start := time.Now()
	n := 0
	_ = Retry(context.Background(), p, func(context.Context) error { n++; return io.EOF })
	assert.Less(t, time.Since(start), time.Second)
	assert.Less(t, n, 100)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	n = 0
	_ = Retry(ctx, fast(), func(context.Context) error { n++; return io.EOF })
	assert.Equal(t, 1, n)
}

func TestRetryRejectsAnInvalidPolicy(t *testing.T) {
	require.Error(t, Retry(context.Background(), RetryPolicy{}, func(context.Context) error { return nil }))
}

func TestBackoffIsBounded(t *testing.T) {
	p := DefaultRetryPolicy()
	for attempt := 1; attempt < 20; attempt++ {
		d := backoff(p, attempt)
		assert.GreaterOrEqual(t, d, time.Duration(0))
		assert.LessOrEqual(t, d, p.MaxDelay)
	}
}

// A try that ran out of its own time is retried; the first connection used to
// fail on it because IsRetryable reads a deadline as the caller's.
func TestStartupAttemptTimeoutIsRetried(t *testing.T) {
	slow := fmt.Errorf("pgclient: health: %w", context.DeadlineExceeded)

	// Parent live: the try's own timeout, retryable.
	assert.True(t, IsRetryable(markAttemptTimeout(context.Background(), slow)))
	// Other errors pass through untouched.
	assert.NoError(t, markAttemptTimeout(context.Background(), nil))
	assert.False(t, IsRetryable(markAttemptTimeout(context.Background(), errors.New("boom"))))
	// Parent expired: the caller's own deadline, not retryable.
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	assert.False(t, IsRetryable(markAttemptTimeout(expired, slow)))

	// Succeeds on the 2nd attempt and logs the retry.
	var logged []int
	p := fast()
	p.OnRetry = func(attempt int, _ error, _ time.Duration) { logged = append(logged, attempt) }
	n := 0
	err := Retry(context.Background(), p, func(ctx context.Context) error {
		n++
		if n == 1 {
			return markAttemptTimeout(ctx, slow)
		}
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, 2, n)
	assert.Equal(t, []int{1}, logged)

	// Gives up at the attempt cap.
	n = 0
	err = Retry(context.Background(), fast(), func(ctx context.Context) error { n++; return markAttemptTimeout(ctx, slow) })
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Equal(t, 4, n)
}
