"""
The conformance suite: every case in clients/conformance/cases.txt, against a real
PostgreSQL that serves TLS (clients/conformance/pg-tls.sh). A test is named
``test_`` plus the case name with ``-`` as ``_``; the CI guard fails unless each
one ran and passed. Without CNPG_CLIENTS_PG_HOST the suite is skipped, or, with
CNPG_CLIENTS_PG=required, fails.
"""

from __future__ import annotations

import contextlib
import os
import shutil
import socket
import tempfile
import threading
import time
from collections.abc import Iterator
from pathlib import Path
from typing import Any

import psycopg
import pytest

from truvity_cnpg import (
    HEALTH_TIMEOUT,
    CnpgConfig,
    CnpgError,
    CnpgPool,
    ConfigError,
    RetryPolicy,
    is_retryable,
)

E = os.environ
HOST = E.get("CNPG_CLIENTS_PG_HOST")
if not HOST:
    if E.get("CNPG_CLIENTS_PG") == "required":
        raise RuntimeError(
            "CNPG_CLIENTS_PG=required but no TLS PostgreSQL is configured (clients/conformance/pg-tls.sh up)"
        )
    pytest.skip("no TLS PostgreSQL configured", allow_module_level=True)

DIR = Path(E["CNPG_CLIENTS_PG_DIR"])
ADDR = E["CNPG_CLIENTS_PG_ADDR"]
PORT = int(E["CNPG_CLIENTS_PG_PORT"])
PW_ROLE = E["CNPG_CLIENTS_PG_PW_ROLE"]
PW = E["CNPG_CLIENTS_PG_PW_PASSWORD"]
DATABASE = E["CNPG_CLIENTS_PG_DATABASE"]

FAST_RETRY = RetryPolicy(attempts=3, initial_delay=0.02, max_delay=0.1, budget=5.0)


def copy(src: str, dst: Path, mode: int = 0o644) -> None:
    """Atomic, as a Secret volume update is."""
    tmp = dst.with_name(f".swap-{dst.name}")
    shutil.copyfile(DIR / src, tmp)
    tmp.chmod(mode)
    os.replace(tmp, dst)


def mount(cert: str) -> Path:
    """Lays the files out as the cnpg-client chart does: ca.crt, tls.crt, tls.key (libpq wants the key 0600)."""
    m = Path(tempfile.mkdtemp(prefix="cnpg-py-"))
    copy("ca.crt", m / "ca.crt")
    set_cert(m, cert)
    return m


def set_cert(m: Path, cert: str) -> None:
    copy(f"{cert}.crt", m / "tls.crt")
    copy(f"{cert}.key", m / "tls.key", 0o600)


def cert_config(m: Path, **over: Any) -> CnpgConfig:
    return CnpgConfig(
        host=HOST or "",
        port=PORT,
        database=DATABASE,
        user="app_cert",
        ssl_root_cert=str(m / "ca.crt"),
        ssl_cert=str(m / "tls.crt"),
        ssl_key=str(m / "tls.key"),
        application_name="conformance",
        retry=FAST_RETRY,
    ).with_(**over)


def password_config(m: Path, **over: Any) -> CnpgConfig:
    return cert_config(m, **{"user": PW_ROLE, "password": PW, "ssl_cert": None, "ssl_key": None, **over})


@pytest.fixture
def opened() -> Iterator[list[CnpgPool]]:
    pools: list[CnpgPool] = []
    yield pools
    for p in pools:
        with contextlib.suppress(Exception):
            p.close()


def open_pool(opened: list[CnpgPool], c: CnpgConfig, tracer: Any = None) -> CnpgPool:
    p = CnpgPool.create(c, tracer)
    opened.append(p)
    return p


def scalar(p: CnpgPool, sql: str, params: Any = None) -> Any:
    return p.query(sql, params)[0][0]


def chain(e: BaseException) -> list[BaseException]:
    out: list[BaseException] = []
    cur: BaseException | None = e
    while cur is not None and cur not in out:
        out.append(cur)
        cur = cur.__cause__
    return out


def sqlstate(e: BaseException) -> str | None:
    return next((s for t in chain(e) if (s := getattr(t, "sqlstate", None))), None)


class Forwarder:
    """A TCP forwarder that can be dropped and restored: the rw Service while the primary behind it switches."""

    def __init__(self) -> None:
        self.server: socket.socket | None = None
        self.sockets: set[socket.socket] = set()
        self.lock = threading.Lock()
        self.port = 0

    def up(self, listen_port: int = 0) -> None:
        s = socket.socket()
        s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        s.bind(("127.0.0.1", listen_port))
        s.listen(50)
        self.server = s
        self.port = s.getsockname()[1]
        threading.Thread(target=self._accept, args=(s,), daemon=True).start()

    def _accept(self, s: socket.socket) -> None:
        while True:
            try:
                c, _ = s.accept()
                u = socket.create_connection((ADDR, PORT))
            except OSError:
                return
            with self.lock:
                self.sockets.update((c, u))
            threading.Thread(target=self._pipe, args=(c, u), daemon=True).start()
            threading.Thread(target=self._pipe, args=(u, c), daemon=True).start()

    @staticmethod
    def _pipe(a: socket.socket, b: socket.socket) -> None:
        try:
            while data := a.recv(65536):
                b.sendall(data)
        except OSError:
            pass
        finally:
            for x in (a, b):
                with contextlib.suppress(OSError):
                    x.shutdown(socket.SHUT_RDWR)
                x.close()

    def down(self) -> None:
        if self.server:
            # close() alone does not wake a thread blocked in accept(): the listener would stay up.
            with contextlib.suppress(OSError):
                self.server.shutdown(socket.SHUT_RDWR)
            with contextlib.suppress(OSError):
                self.server.close()
        with self.lock:
            socks, self.sockets = list(self.sockets), set()
        for s in socks:
            with contextlib.suppress(OSError):
                s.shutdown(socket.SHUT_RDWR)
            s.close()


@pytest.fixture
def forwarder() -> Iterator[Forwarder]:
    f = Forwarder()
    f.up()
    yield f
    f.down()


def test_verify_full_connects(opened: list[CnpgPool]) -> None:
    p = open_pool(opened, cert_config(mount("app_cert.1")))
    assert scalar(p, "select ssl from pg_stat_ssl where pid = pg_backend_pid()") is True
    assert scalar(p, "select current_setting('application_name')") == "conformance"
    assert scalar(p, "select current_setting('statement_timeout')") == "30s"
    assert scalar(p, "select current_setting('idle_in_transaction_session_timeout')") == "1min"
    p.health()


def test_rejects_unknown_ca(opened: list[CnpgPool]) -> None:
    m = mount("app_cert.1")
    copy("other-ca.crt", m / "ca.crt")
    with pytest.raises(CnpgError) as e:
        CnpgPool.create(cert_config(m))
    assert not is_retryable(e.value), str(e.value)


def test_rejects_hostname_mismatch(opened: list[CnpgPool]) -> None:
    with pytest.raises(CnpgError) as e:
        CnpgPool.create(cert_config(mount("app_cert.1"), host=ADDR))
    assert not is_retryable(e.value), str(e.value)
    assert "does not match host name" in str(e.value)


def test_refuses_weaker_sslmode(opened: list[CnpgPool]) -> None:
    m = mount("app_cert.1")
    for mode in ("disable", "allow", "prefer", "require", "verify-ca"):
        with pytest.raises(ConfigError, match="only verify-full is accepted"):
            CnpgPool.create(cert_config(m, ssl_mode=mode))
        with pytest.raises(ConfigError, match="only verify-full is accepted"):
            CnpgConfig.from_env(
                {
                    "PGHOST": "h",
                    "PGDATABASE": "d",
                    "PGUSER": "u",
                    "PGSSLROOTCERT": "/ca",
                    "PGSSLMODE": mode,
                }
            )
    with pytest.raises(ConfigError, match="server CA file is required"):
        CnpgPool.create(cert_config(m, ssl_root_cert=""))


def test_password_role(opened: list[CnpgPool]) -> None:
    m = mount("app_cert.1")
    p = open_pool(opened, password_config(m))
    assert scalar(p, "select current_user") == PW_ROLE

    with pytest.raises(CnpgError) as e:
        CnpgPool.create(password_config(m, password="wrong"))
    assert "password authentication failed" in str(e.value)
    assert not is_retryable(e.value)


def test_client_cert_role(opened: list[CnpgPool]) -> None:
    p = open_pool(opened, cert_config(mount("app_cert.1")))
    assert scalar(p, "select current_user") == "app_cert"

    # The right name from the wrong authority is refused, and not retried.
    m = mount("app_cert.1")
    copy("foreign.crt", m / "tls.crt")
    copy("foreign.key", m / "tls.key", 0o600)
    start = time.monotonic()
    with pytest.raises(CnpgError) as e:
        CnpgPool.create(cert_config(m))
    assert not is_retryable(e.value), str(e.value)
    assert time.monotonic() - start < 2


def test_client_cert_rotation(opened: list[CnpgPool]) -> None:
    m = mount("app_cert.1")
    p = open_pool(opened, cert_config(m, max_conn_lifetime=0.4))

    def serial() -> str:
        return str(scalar(p, "select client_serial::text from pg_stat_ssl where pid = pg_backend_pid()"))

    assert serial() == str(0x101)
    set_cert(m, "app_cert.2")  # what cert-manager's renewal does to the mounted files
    deadline = time.monotonic() + 10
    while True:
        try:
            if serial() == str(0x202):
                break
        except psycopg.Error:
            pass  # the two files are replaced one after the other: that window is not the bug
        assert time.monotonic() < deadline, "the renewed certificate was never picked up"
        time.sleep(0.15)


def test_password_file_rotation(opened: list[CnpgPool]) -> None:
    m = mount("app_cert.1")
    pwfile = Path(tempfile.mkdtemp(prefix="cnpg-py-pw-")) / "password"

    def write(v: str) -> None:
        pwfile.write_text(v + "\n")
        pwfile.chmod(0o600)

    write(PW)
    p = open_pool(opened, password_config(m, password=None, password_file=str(pwfile), max_conn_lifetime=0.4))
    assert scalar(p, "select current_user") == PW_ROLE
    first_pid = scalar(p, "select pg_backend_pid()")

    nxt = "rotated-password"
    p.execute(f"alter role {PW_ROLE} password '{nxt}'")
    try:
        write(nxt)
        # The lifetime retires the pooled connection; the next one must read the new file,
        # or authentication fails (the old password is gone).
        deadline = time.monotonic() + 10
        while scalar(p, "select pg_backend_pid()") == first_pid:
            assert time.monotonic() < deadline, "no new connection was made"
            time.sleep(0.15)
        assert scalar(p, "select current_user") == PW_ROLE
    finally:
        write(nxt)
        q = open_pool(opened, password_config(m, password=nxt))
        q.execute(f"alter role {PW_ROLE} password '{PW}'")


def test_reconnects_after_backend_termination(opened: list[CnpgPool]) -> None:
    p = open_pool(opened, cert_config(mount("app_cert.1"), pool_max=2))
    victim = p.pool.getconn()
    pid = victim.execute("select pg_backend_pid()").fetchone()[0]
    assert scalar(p, "select pg_terminate_backend(%s)", (pid,)) is True
    p.pool.putconn(victim)  # its rollback fails: the pool drops the dead connection
    # (a pool that kept it would hand it out and fail the next call; the retry covers that too)

    attempts = 0

    def work() -> None:
        nonlocal attempts
        attempts += 1
        p.query("select 1")

    p.do(work)
    assert attempts >= 1
    p.health()


def test_survives_primary_switch(opened: list[CnpgPool], forwarder: Forwarder) -> None:
    policy = RetryPolicy(attempts=20, initial_delay=0.05, max_delay=0.5, budget=15.0)
    p = open_pool(opened, cert_config(mount("app_cert.1"), port=forwarder.port, retry=policy))
    assert scalar(p, "select current_user") == "app_cert"

    listen_port = forwarder.port
    forwarder.down()  # the old primary is gone and nothing answers yet
    down_at = time.monotonic()
    threading.Timer(1.5, lambda: forwarder.up(listen_port)).start()

    p.do(lambda: p.query("select current_user"))
    assert time.monotonic() - down_at >= 1.0, "the call finished before the Service came back"


def test_statement_timeout_enforced(opened: list[CnpgPool]) -> None:
    p = open_pool(opened, cert_config(mount("app_cert.1"), statement_timeout=0.3))
    start = time.monotonic()
    attempts = 0

    def work() -> None:
        nonlocal attempts
        attempts += 1
        p.query("select pg_sleep(5)")

    with pytest.raises(psycopg.Error) as e:
        p.do(work)
    assert sqlstate(e.value) == "57014"
    assert attempts == 1, "a timeout is not a connection failure"
    assert time.monotonic() - start < 3


def test_permanent_error_not_retried(opened: list[CnpgPool]) -> None:
    p = open_pool(opened, cert_config(mount("app_cert.1")))
    p.execute("drop table if exists cnpg_clients_conf_py")
    p.execute("create table cnpg_clients_conf_py (id int primary key)")
    p.execute("insert into cnpg_clients_conf_py values (1)")
    try:
        attempts = 0

        def work() -> None:
            nonlocal attempts
            attempts += 1
            p.execute("insert into cnpg_clients_conf_py values (1)")

        with pytest.raises(psycopg.Error) as e:
            p.do(work)
        assert sqlstate(e.value) == "23505"
        assert attempts == 1
    finally:
        p.execute("drop table if exists cnpg_clients_conf_py")


def test_health_check(opened: list[CnpgPool], forwarder: Forwarder) -> None:
    p = open_pool(opened, cert_config(mount("app_cert.1"), port=forwarder.port))
    p.health()
    forwarder.down()
    start = time.monotonic()
    with pytest.raises(CnpgError):
        p.health()
    assert time.monotonic() - start < HEALTH_TIMEOUT + 1


def test_traces_statements_without_arguments(opened: list[CnpgPool]) -> None:
    from opentelemetry.sdk.trace import TracerProvider
    from opentelemetry.sdk.trace.export import SimpleSpanProcessor
    from opentelemetry.sdk.trace.export.in_memory_span_exporter import InMemorySpanExporter

    exporter = InMemorySpanExporter()
    provider = TracerProvider()
    provider.add_span_processor(SimpleSpanProcessor(exporter))
    p = open_pool(opened, cert_config(mount("app_cert.1")), provider.get_tracer("test"))
    secret = "user-data-that-must-not-be-traced"
    assert scalar(p, "select %s::text", (secret,)) == secret

    spans = exporter.get_finished_spans()
    assert any((s.attributes or {}).get("db.query.text") == "select %s::text" for s in spans)
    for s in spans:
        assert (s.attributes or {}).get("db.system.name") == "postgresql"
        assert secret not in str(dict(s.attributes or {}))
        assert secret not in s.to_json()
