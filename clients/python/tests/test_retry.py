import psycopg
import psycopg.errors as pe
import psycopg_pool
import pytest

from truvity_cnpg import CnpgError, RetryPolicy, backoff, is_retryable, retry

FAST = RetryPolicy(attempts=4, initial_delay=0.001, max_delay=0.005, budget=5.0)


def with_state(cls: type[psycopg.Error], state: str) -> psycopg.Error:
    """An error that carries a SQLSTATE the way a server error does."""
    e = cls("x")
    e.__dict__["sqlstate"] = state
    return e


def test_backoff_is_jittered_under_a_doubling_ceiling() -> None:
    p = RetryPolicy(attempts=9, initial_delay=0.2, max_delay=5.0, budget=60)
    for _ in range(200):
        assert 0 <= backoff(p, 1) <= 0.2
        assert 0 <= backoff(p, 3) <= 0.8
        assert 0 <= backoff(p, 8) <= 5.0
        assert 0 <= backoff(p, 500) <= 5.0  # no overflow
    assert len({backoff(p, 5) for _ in range(200)}) > 1


def test_a_connection_error_is_retried_until_it_passes() -> None:
    n = 0

    def fn() -> str:
        nonlocal n
        n += 1
        if n < 3:
            raise psycopg.OperationalError("server closed the connection unexpectedly")
        return "ok"

    assert retry(FAST, fn, sleep=lambda _: None) == "ok"
    assert n == 3


def test_it_stops_at_the_attempt_count_and_reraises_the_last_error() -> None:
    n = 0

    def fn() -> None:
        nonlocal n
        n += 1
        raise psycopg.OperationalError(f"gone {n}")

    with pytest.raises(psycopg.OperationalError, match="gone 4"):
        retry(FAST, fn, sleep=lambda _: None)
    assert n == 4


def test_a_permanent_error_is_not_retried() -> None:
    n = 0

    def fn() -> None:
        nonlocal n
        n += 1
        raise with_state(pe.UniqueViolation, "23505")

    with pytest.raises(pe.UniqueViolation):
        retry(FAST, fn, sleep=lambda _: None)
    assert n == 1


def test_the_waiting_budget_bounds_the_retry() -> None:
    now = 0.0
    n = 0

    def sleep(d: float) -> None:
        nonlocal now
        now += d

    def fn() -> None:
        nonlocal n
        n += 1
        raise psycopg.OperationalError("gone")

    tight = RetryPolicy(attempts=1000, initial_delay=0.05, max_delay=0.05, budget=0.12)
    with pytest.raises(psycopg.OperationalError):
        retry(tight, fn, sleep=sleep, clock=lambda: now)
    assert 2 <= n <= 20


@pytest.mark.parametrize(
    "state", ["57P01", "57P02", "57P03", "25006", "53300", "08000", "08001", "08006", "08P01"]
)
def test_connection_class_sqlstates_are_retryable(state: str) -> None:
    assert is_retryable(with_state(pe.OperationalError, state))


@pytest.mark.parametrize(
    "state", ["28P01", "28000", "42501", "42601", "23505", "23503", "57014", "40001", "40P01", "22P02"]
)
def test_other_sqlstates_are_not(state: str) -> None:
    assert not is_retryable(with_state(pe.Error, state))


def test_a_connection_failure_without_a_sqlstate_is_retryable() -> None:
    for m in (
        "connection failed: connection to server at 127.0.0.1, port 5432 failed: Connection refused",
        "consuming input failed: server closed the connection unexpectedly",
        "consuming input failed: SSL SYSCALL error: EOF detected",
        "consuming input failed: SSL error: unexpected eof while reading",
        "the connection is closed",
    ):
        assert is_retryable(psycopg.OperationalError(m)), m
    assert is_retryable(psycopg_pool.PoolTimeout("couldn't get a connection after 5.00 sec"))
    assert is_retryable(ConnectionResetError())
    assert is_retryable(psycopg.InterfaceError("the connection is closed"))


def test_certificate_host_and_login_failures_are_not() -> None:
    for m in (
        'connection failed: connection to server at "127.0.0.1", port 5432 failed: SSL error: certificate verify failed',
        'connection failed: server certificate for "localhost" (and 0 other names) does not match host name "127.0.0.1"',
        "connection failed: SSL error: tlsv1 alert unknown ca",
        "connection failed: SSL error: tlsv13 alert certificate required",
        "connection failed: SSL error: sslv3 alert bad certificate",
        'connection failed: FATAL:  password authentication failed for user "app_pw"',
        'connection failed: could not read root certificate file "/x": No such file or directory',
        'connection failed: private key file "/k" has group or world access',
    ):
        assert not is_retryable(psycopg.OperationalError(m)), m
    assert not is_retryable(ValueError("bug"))


def test_the_cause_chain_is_read() -> None:
    try:
        try:
            raise psycopg.OperationalError("connection failed: SSL error: certificate verify failed")
        except psycopg.OperationalError as inner:
            raise CnpgError("first connection") from inner
    except CnpgError as outer:
        assert not is_retryable(outer)
    try:
        try:
            raise with_state(pe.OperationalError, "57P03")
        except psycopg.OperationalError as inner:
            raise CnpgError("first connection") from inner
    except CnpgError as outer:
        assert is_retryable(outer)
