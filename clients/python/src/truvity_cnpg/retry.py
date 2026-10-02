"""Retry of connection-class failures, with exponential backoff and full jitter."""

from __future__ import annotations

import random
import re
import time
from collections.abc import Callable
from typing import TypeVar

import psycopg
import psycopg_pool

from .config import RetryPolicy

T = TypeVar("T")


def backoff(policy: RetryPolicy, attempt: int) -> float:
    """Full jitter under a doubling ceiling. ``attempt`` is 1 for the first retry."""
    ceil = min(policy.max_delay, policy.initial_delay * 2 ** min(attempt - 1, 30))
    return random.uniform(0.0, ceil)


def retry(
    policy: RetryPolicy,
    fn: Callable[[], T],
    *,
    sleep: Callable[[float], object] = time.sleep,
    clock: Callable[[], float] = time.monotonic,
) -> T:
    """
    Runs ``fn``, repeating it while it fails with a retryable error (see
    :func:`is_retryable`). ``fn`` must be safe to run again: a statement that may
    have committed before the connection broke is the caller's to reason about,
    which is why the unit of retry is the caller's whole function.
    """
    start = clock()
    attempt = 1
    while True:
        try:
            return fn()
        except Exception as err:
            if not is_retryable(err) or attempt >= policy.attempts:
                raise
            delay = backoff(policy, attempt)
            if clock() - start + delay > policy.budget:
                raise
            sleep(delay)
            attempt += 1


_RETRYABLE_STATES = frozenset(
    {
        "57P01",  # admin_shutdown
        "57P02",  # crash_shutdown
        "57P03",  # cannot_connect_now
        "25006",  # read_only_sql_transaction: a demoted primary still answering
        "53300",  # too_many_connections: a new primary filling
    }
)

# libpq reports a refused certificate, a host name the certificate does not carry and a
# failed login as a plain OperationalError. A second try cannot fix any of them.
_NEVER = re.compile(
    r"certificate verify failed"
    r"|does not match host name"
    r"|\b(?:tlsv1\d*|sslv3) alert\b"
    r"|authentication failed"
    r"|no pg_hba\.conf entry"
    r"|server does not support SSL"
    r"|root certificate file"
    r"|private key file"
    r"|could not load (?:client|private)"
)


def _chain(err: BaseException) -> list[BaseException]:
    out: list[BaseException] = []
    cur: BaseException | None = err
    while cur is not None and len(out) < 16 and cur not in out:
        out.append(cur)
        cur = cur.__cause__ or cur.__context__
    return out


def is_retryable(err: BaseException) -> bool:
    """
    Reports whether ``err`` means the connection (not the statement) failed: the
    server went away, was demoted, or is not accepting yet. Those are what a
    CloudNativePG switchover or crash looks like to a client.

    Never retryable: authentication and permission errors, constraint and syntax
    errors, a statement timeout (57014), a serialization failure (40001, 40P01:
    the caller retries its transaction), and certificate or host name failures.
    """
    chain = _chain(err)
    for t in chain:
        if _NEVER.search(str(t)):
            return False
    for t in chain:
        state = getattr(t, "sqlstate", None)
        if isinstance(state, str) and len(state) == 5:
            return state.startswith("08") or state in _RETRYABLE_STATES
    for t in chain:
        if isinstance(t, psycopg_pool.PoolTimeout | ConnectionError | EOFError | TimeoutError):
            return True
        # No SQLSTATE: the failure came from the connection itself (libpq), not from a statement.
        if isinstance(t, psycopg.OperationalError):
            return True
        if isinstance(t, psycopg.InterfaceError) and "closed" in str(t):
            return True
    return False
