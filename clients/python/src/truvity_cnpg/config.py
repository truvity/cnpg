"""Configuration: the contract's inputs, validation and the environment reader."""

from __future__ import annotations

import math
import os
import re
import sys
from collections.abc import Callable, Mapping
from dataclasses import dataclass, field, replace
from typing import TypeVar

T = TypeVar("T")


@dataclass(frozen=True)
class RetryPolicy:
    """Bounds the retry of connection-class failures."""

    attempts: int = 5
    """Total tries, including the first."""
    initial_delay: float = 0.2
    """Ceiling of the first sleep, seconds."""
    max_delay: float = 5.0
    """Ceiling of any sleep, seconds."""
    budget: float = 30.0
    """Total time that may be spent waiting, seconds."""


class ConfigError(ValueError):
    """Every problem with a configuration, at once."""

    def __init__(self, problems: list[str]) -> None:
        super().__init__("cnpg-client: invalid configuration:\n  - " + "\n  - ".join(problems))
        self.problems = problems


def _program_name() -> str:
    argv0 = sys.argv[0] if sys.argv and sys.argv[0] else "python"
    name = os.path.basename(argv0) or "python"
    return name[:63]  # NAMEDATALEN-1


@dataclass(frozen=True)
class CnpgConfig:
    """
    What a connection needs. There is no free-form DSN: a parameter passed
    through a string can be dropped on the way to the driver, and a dropped
    ``sslrootcert`` turns verify-full into a connection that does not verify.
    Durations are seconds.
    """

    host: str
    database: str
    user: str
    ssl_root_cert: str
    """Server CA file. Required: this file is the only trust root."""
    port: int = 5432
    password: str | None = field(default=None, repr=False)
    """For a scram role. ``password_file`` wins and is read again for every new connection."""
    password_file: str | None = None
    ssl_cert: str | None = None
    ssl_key: str | None = None
    """Client certificate and key of a certificate role; both or neither."""
    ssl_mode: str | None = None
    """Exists to be refused: None or ``verify-full``."""
    application_name: str = field(default_factory=_program_name)
    connect_timeout: float = 5.0
    statement_timeout: float = 30.0
    """0 disables."""
    idle_in_tx_timeout: float = 60.0
    """0 disables."""
    pool_max: int = 10
    pool_min: int = 0
    max_conn_lifetime: float = 30 * 60.0
    """Keep below the client certificate's life."""
    max_conn_idle: float = 5 * 60.0
    health_period: float = 30.0
    """Background check of idle pooled connections; 0 disables."""
    retry: RetryPolicy = field(default_factory=RetryPolicy)

    def __repr__(self) -> str:
        has_pw = self.password is not None or self.password_file is not None
        return (
            f"CnpgConfig(host={self.host!r}, port={self.port}, database={self.database!r}, "
            f"user={self.user!r}, password={'<redacted>' if has_pw else '<none>'}, "
            "sslmode='verify-full', "
            f"ssl_root_cert={self.ssl_root_cert!r}, ssl_cert={self.ssl_cert!r}, ssl_key={self.ssl_key!r}, "
            f"application_name={self.application_name!r})"
        )

    __str__ = __repr__

    def with_(self, **changes: object) -> CnpgConfig:
        """A copy with some fields replaced."""
        return replace(self, **changes)  # type: ignore[arg-type]

    def validate(self) -> None:
        """Raises :class:`ConfigError` listing every problem."""
        p: list[str] = []
        if self.ssl_mode not in (None, "", "verify-full"):
            p.append(f'sslmode "{self.ssl_mode}" is refused; only verify-full is accepted')
        if not self.host:
            p.append("host is required")
        elif not _HOST.fullmatch(self.host):
            p.append(f'host "{self.host}": exactly one host name is accepted (the cluster\'s rw Service)')
        if not 1 <= self.port <= 65535:
            p.append("port must be 1-65535")
        if not self.database:
            p.append("database is required")
        if not self.user:
            p.append("user is required")
        if not self.ssl_root_cert:
            p.append(
                "the server CA file is required (PGSSLROOTCERT): "
                "verify-full has nothing to verify against without it"
            )
        if bool(self.ssl_cert) != bool(self.ssl_key):
            p.append("client certificate and key go together")
        if self.pool_max < 1:
            p.append("pool_max must be at least 1")
        if not 0 <= self.pool_min <= self.pool_max:
            p.append("pool_min must be between 0 and pool_max")
        if not self.connect_timeout > 0:
            p.append("connect_timeout must be positive")
        if self.statement_timeout < 0 or self.idle_in_tx_timeout < 0:
            p.append("timeouts must not be negative")
        if not (self.max_conn_lifetime > 0 and self.max_conn_idle > 0):
            p.append("max_conn_lifetime and max_conn_idle must be positive")
        if self.health_period < 0:
            p.append("health_period must not be negative")
        r = self.retry
        if not (r.attempts >= 1 and 0 < r.initial_delay <= r.max_delay and r.budget > 0):
            p.append("retry: attempts>=1, 0<initial_delay<=max_delay, budget>0")
        if p:
            raise ConfigError(p)

    @classmethod
    def from_env(cls, env: Mapping[str, str] | None = None) -> CnpgConfig:
        """
        Builds a configuration from the contract's environment: the libpq names
        the ``cnpg-client`` Helm library chart exports, plus the ``CNPG_CLIENT_*``
        tuning variables. The result is validated.
        """
        e = os.environ if env is None else env
        problems: list[str] = []

        def get(k: str) -> str | None:
            v = e.get(k)
            return v if v else None

        def parse(k: str, fallback: T, f: Callable[[str], T]) -> T:
            v = get(k)
            if v is None:
                return fallback
            try:
                return f(v)
            except ValueError as err:
                problems.append(f'{k}="{v}": {err}')
                return fallback

        d = cls(host="", database="", user="", ssl_root_cert="")
        c = cls(
            host=get("PGHOST") or "",
            database=get("PGDATABASE") or "",
            user=get("PGUSER") or "",
            ssl_root_cert=get("PGSSLROOTCERT") or "",
            ssl_cert=get("PGSSLCERT"),
            ssl_key=get("PGSSLKEY"),
            ssl_mode=get("PGSSLMODE"),
            password=get("PGPASSWORD"),
            password_file=get("CNPG_CLIENT_PASSWORD_FILE"),
            application_name=get("PGAPPNAME") or d.application_name,
            port=parse("PGPORT", d.port, _int),
            connect_timeout=parse("PGCONNECT_TIMEOUT", d.connect_timeout, _positive_seconds),
            statement_timeout=parse("CNPG_CLIENT_STATEMENT_TIMEOUT", d.statement_timeout, parse_duration),
            idle_in_tx_timeout=parse("CNPG_CLIENT_IDLE_TX_TIMEOUT", d.idle_in_tx_timeout, parse_duration),
            pool_max=parse("CNPG_CLIENT_POOL_MAX", d.pool_max, _int),
            pool_min=parse("CNPG_CLIENT_POOL_MIN", d.pool_min, _int),
            max_conn_lifetime=parse("CNPG_CLIENT_CONN_MAX_LIFETIME", d.max_conn_lifetime, parse_duration),
            max_conn_idle=parse("CNPG_CLIENT_CONN_MAX_IDLE", d.max_conn_idle, parse_duration),
            health_period=parse("CNPG_CLIENT_HEALTH_PERIOD", d.health_period, parse_duration),
            retry=RetryPolicy(
                attempts=parse("CNPG_CLIENT_RETRY_ATTEMPTS", d.retry.attempts, _int),
                max_delay=parse("CNPG_CLIENT_RETRY_MAX_DELAY", d.retry.max_delay, parse_duration),
                budget=parse("CNPG_CLIENT_RETRY_BUDGET", d.retry.budget, parse_duration),
            ),
        )
        if problems:
            raise ConfigError(problems)
        c.validate()
        return c


# One DNS name, or an IPv4/IPv6 literal. No `/ ? # & , @`, no spaces: nothing that could
# smuggle a second host or a parameter into the connection.
_HOST = re.compile(r"[A-Za-z0-9._:-]+")
_DURATION = re.compile(r"(\d+(?:\.\d+)?)(ms|s|m|h)")
_UNIT = {"ms": 0.001, "s": 1.0, "m": 60.0, "h": 3600.0}


def _int(s: str) -> int:
    if not re.fullmatch(r"\d{1,9}", s):
        raise ValueError("not a whole number")
    return int(s)


def _positive_seconds(s: str) -> float:
    n = _int(s)
    if n <= 0:
        raise ValueError("must be positive seconds")
    return float(n)


def parse_duration(text: str) -> float:
    """``"500ms"``, ``"30s"``, ``"5m"``, ``"1h"`` in seconds; a plain ``"0"`` is accepted too."""
    if text == "0":
        return 0.0
    m = _DURATION.fullmatch(text)
    if not m:
        raise ValueError(f'"{text}" is not a duration (use 500ms, 30s, 5m, 1h)')
    out = float(m.group(1)) * _UNIT[m.group(2)]
    if not math.isfinite(out):
        raise ValueError(f'"{text}" is not a duration')
    return out
