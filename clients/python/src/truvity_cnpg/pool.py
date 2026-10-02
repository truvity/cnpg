"""The pool: psycopg 3 + psycopg_pool behind the contract."""

from __future__ import annotations

import math
import re
import threading
from collections.abc import Callable, Iterator, Sequence
from contextlib import AbstractContextManager, contextmanager
from dataclasses import dataclass
from pathlib import Path
from typing import TYPE_CHECKING, Any, TypeVar

import psycopg
from psycopg_pool import ConnectionPool

from .config import CnpgConfig
from .retry import retry

if TYPE_CHECKING:
    from opentelemetry.trace import Tracer

T = TypeVar("T")

HEALTH_TIMEOUT = 2.0
"""Bounds :meth:`CnpgPool.health`, seconds."""


class CnpgError(Exception):
    """An error from this library itself (the driver's own errors are chained as ``__cause__``)."""


@dataclass(frozen=True)
class PoolStats:
    """Open, idle and waiting connections, for a metrics exporter."""

    total: int
    idle: int
    waiting: int


def read_password(c: CnpgConfig) -> str | None:
    """The password as of NOW: the file (re-read every time) wins over the value."""
    if c.password_file:
        try:
            return Path(c.password_file).read_text(encoding="utf-8").rstrip("\r\n")
        except OSError as e:
            raise CnpgError(f"cnpg-client: reading the password file: {e}") from e
    return c.password


def connection_params(c: CnpgConfig) -> dict[str, Any]:
    """
    libpq parameters for ONE new connection. libpq reads ``sslrootcert``,
    ``sslcert`` and ``sslkey`` when it connects and caches nothing, so passing the
    paths is enough for TLS; the password file is read here, now.
    """
    p: dict[str, Any] = {
        "host": c.host,
        "port": c.port,
        "dbname": c.database,
        "user": c.user,
        # verify-full against the given CA file only (not the system store), TLS 1.2 or later.
        "sslmode": "verify-full",
        "sslrootcert": c.ssl_root_cert,
        "ssl_min_protocol_version": "TLSv1.2",
        "application_name": c.application_name,
        "connect_timeout": max(2, math.ceil(c.connect_timeout)),  # libpq's own floor is 2
    }
    if c.ssl_cert and c.ssl_key:
        p["sslcert"] = c.ssl_cert
        p["sslkey"] = c.ssl_key
    pw = read_password(c)
    if pw is not None:
        p["password"] = pw
    options = []
    if c.statement_timeout > 0:
        options.append(f"-c statement_timeout={round(c.statement_timeout * 1000)}")
    if c.idle_in_tx_timeout > 0:
        options.append(f"-c idle_in_transaction_session_timeout={round(c.idle_in_tx_timeout * 1000)}")
    if options:
        p["options"] = " ".join(options)
    return p


class _ReloadingConnection(psycopg.Connection[Any]):
    """``connect`` builds the parameters anew, so a renewed file is used by the next connection."""

    @classmethod
    def connect(cls, conninfo: str = "", **kwargs: Any) -> Any:
        config: CnpgConfig = kwargs.pop("cnpg_config")
        return super().connect("", **connection_params(config), **kwargs)


_OPERATION = re.compile(r"^[\s(]*([A-Za-z]{1,16})\b")


def operation(sql: str) -> str:
    m = _OPERATION.match(sql)
    return m.group(1).upper() if m else "QUERY"


class CnpgPool:
    """
    A psycopg_pool connection pool with the contract's behaviour: verify-full
    against the given CA, certificate/key/password reload per connection, bounded
    connection lifetime, failover-aware :meth:`do` and a :meth:`health` check.

    ``pool`` is the underlying ``psycopg_pool.ConnectionPool``; statements run on
    a connection taken from it are not traced and not retried.
    """

    def __init__(self, config: CnpgConfig, tracer: Tracer | None = None) -> None:
        config.validate()
        self.config = config
        self._tracer = tracer
        self.pool: ConnectionPool[Any] = ConnectionPool(
            "",
            kwargs={"cnpg_config": config},
            connection_class=_ReloadingConnection,
            min_size=config.pool_min,
            max_size=config.pool_max,
            max_lifetime=config.max_conn_lifetime,
            max_idle=config.max_conn_idle,
            timeout=max(config.connect_timeout, 0.25),
            check=ConnectionPool.check_connection,
            name=f"cnpg-{config.application_name}",
            open=False,
        )

    @classmethod
    def create(cls, config: CnpgConfig, tracer: Tracer | None = None) -> CnpgPool:
        """
        Validates, proves the first connection under the retry policy (a start-up
        race with the Service is retried like any other) and opens the pool.
        """
        config.validate()

        def first() -> None:
            conn = _ReloadingConnection.connect(cnpg_config=config)
            try:
                conn.execute("select 1")
            finally:
                conn.close()

        try:
            retry(config.retry, first)
        except Exception as e:
            raise CnpgError(f"cnpg-client: first connection to {config.host}:{config.port}: {e}") from e
        p = cls(config, tracer)
        p.pool.open()
        return p

    def connection(self) -> AbstractContextManager[psycopg.Connection[Any]]:
        """A pooled connection; the transaction commits on a clean exit."""
        return self.pool.connection()

    def query(self, sql: str, params: Sequence[Any] | None = None) -> list[tuple[Any, ...]]:
        """Runs one statement and returns its rows (none for a statement that returns none)."""
        with self._traced(sql), self.pool.connection() as conn:
            cur = conn.execute(sql, params)
            return cur.fetchall() if cur.description else []

    def execute(self, sql: str, params: Sequence[Any] | None = None) -> int:
        """Runs one statement and returns the affected row count."""
        with self._traced(sql), self.pool.connection() as conn:
            return int(conn.execute(sql, params).rowcount)

    @contextmanager
    def _traced(self, sql: str) -> Iterator[None]:
        if self._tracer is None:
            yield
            return
        from opentelemetry.trace import SpanKind

        op = operation(sql)
        with self._tracer.start_as_current_span(
            f"{op} {self.config.database}",
            kind=SpanKind.CLIENT,
            attributes={
                "db.system.name": "postgresql",
                "db.namespace": self.config.database,
                "db.operation.name": op,
                "db.query.text": sql,  # placeholders only; the arguments are user data
                "server.address": self.config.host,
                "server.port": self.config.port,
            },
        ):
            yield

    def do(self, fn: Callable[[], T]) -> T:
        """Runs ``fn`` under the retry policy; see :func:`truvity_cnpg.retry`."""
        return retry(self.config.retry, fn)

    def health(self) -> None:
        """
        ``SELECT 1`` on a pooled connection, bounded by :data:`HEALTH_TIMEOUT`. It
        exercises the real path (Service, TLS, authentication), so it suits a
        readiness probe. Raises :class:`CnpgError` when unhealthy.
        """
        try:
            with self.pool.connection(timeout=HEALTH_TIMEOUT) as conn:
                timer = threading.Timer(HEALTH_TIMEOUT, conn.cancel)
                timer.daemon = True
                timer.start()
                try:
                    conn.execute("select 1")
                finally:
                    timer.cancel()
        except Exception as e:
            raise CnpgError(f"cnpg-client: health: {e}") from e

    def stats(self) -> PoolStats:
        s = self.pool.get_stats()
        return PoolStats(
            total=s.get("pool_size", 0),
            idle=s.get("pool_available", 0),
            waiting=s.get("requests_waiting", 0),
        )

    def close(self) -> None:
        """Closes every connection."""
        self.pool.close()

    def __enter__(self) -> CnpgPool:
        return self

    def __exit__(self, *exc: object) -> None:
        self.close()

    def __repr__(self) -> str:
        return f"CnpgPool({self.config!r})"
