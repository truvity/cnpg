# truvity-cnpg-client (Python)

The Python adapter of the [client contract](../README.md), on psycopg 3 and
`psycopg_pool`. Python 3.10 or later. Import name: `truvity_cnpg`.

```python
from truvity_cnpg import CnpgConfig, CnpgPool

pool = CnpgPool.create(CnpgConfig.from_env())  # PGHOST, PGSSLROOTCERT, ... ; retries the first connection
rows = pool.query("select %s::int as n", (1,))  # [(1,)]


def work() -> None:  # idempotent unit of work, retried on connection-class errors
    pool.execute("update ...")


pool.do(work)

# readiness probe
try:
    pool.health()
except Exception:
    ...  # not ready
```

`pool.pool` is the underlying `psycopg_pool.ConnectionPool` and
`pool.connection()` a pooled connection for transactions; statements on it are
not traced and not retried, so wrap the unit of work in `pool.do(...)`. The
API is synchronous; an `AsyncConnectionPool` flavour is not provided yet.

## Install

Released as a **wheel and an sdist attached to the GitHub release** of the
tag, not on a package index: GitHub Packages has no Python registry, and
release assets of a public repository need no token. Public PyPI is a possible
later move.

```
pip install "truvity-cnpg-client[binary] @ https://github.com/truvity/cnpg/releases/download/v2.7.0/truvity_cnpg_client-2.7.0-py3-none-any.whl"
# uv:
uv add "truvity-cnpg-client[binary] @ https://github.com/truvity/cnpg/releases/download/v2.7.0/truvity_cnpg_client-2.7.0-py3-none-any.whl"
```

The version is the repository's `v*` tag without the `v`, shared with the
charts, the Go module and the other clients. Pin the URL (a requirement is a
URL, so a lock file records it, and `uv lock` / `pip-compile` can hash it).

Extras: `binary` pulls `psycopg[binary]` (libpq with OpenSSL bundled); without
it psycopg needs a system libpq 13 or later. `otel` pulls `opentelemetry-api`.

## What it does

- Always `verify-full`: `sslmode=verify-full`, `sslrootcert=<the CA file>` (a
  file, never `system`), `ssl_min_protocol_version=TLSv1.2`. A missing CA, a
  `ssl_mode` other than `verify-full`, or half a client certificate is a
  `ConfigError` listing every problem.
- No free-form DSN: libpq parameters are built from the parts, and the host is
  restricted to one name or address.
- libpq reads `sslrootcert`, `sslcert` and `sslkey` for every new connection;
  the password file is read in `_ReloadingConnection.connect`, the connection
  class the pool uses, for every new connection too. `max_conn_lifetime` (30m)
  recycles the connections, so a renewed Secret reaches the process without a
  restart. `check=ConnectionPool.check_connection` drops a dead connection
  when the pool hands it out.
- `pool.health()` (`select 1`, bounded to 2s), `pool.do(fn)`,
  `retry(policy, fn)`, `is_retryable(exc)` (reads the cause chain; libpq
  reports a refused certificate, a host name mismatch and a failed login as a
  plain `OperationalError`, so those are recognised by message and never
  retried).
- `repr()` of the configuration and the pool carries no password or key
  material.

## Differences from the contract's letter

- **Key file mode.** libpq refuses a private key readable by group or others
  unless it is owned by root and at most `0640`. The chart's default `0440`
  works with `securityContext.fsGroup` (the mounted file is root-owned); for a
  key owned by the process's own user use `0600`.
- **Units.** Durations in `CnpgConfig` are seconds (floats); the environment
  variables use `500ms`, `30s`, `5m`, `1h` as in the contract.
- **`CNPG_CLIENT_HEALTH_PERIOD`** is read and validated but psycopg_pool has no
  background keepalive of idle connections; its `check` runs when a connection
  is handed out.
- **Blocking and threads.** `retry` sleeps the calling thread.

## Tracing

```python
from opentelemetry import trace

pool = CnpgPool.create(config, tracer=trace.get_tracer("my-service"))
```

Only `opentelemetry-api` is needed (`pip install ...[otel]`). `pool.query` and
`pool.execute` are traced: a client span `SELECT app` with `db.system.name`,
`db.namespace`, `db.operation.name`, `db.query.text` (the statement with its
`%s` placeholders, never the arguments), `server.address` and `server.port`.

## Develop

```
cd clients/python
uv sync && uv run ruff check . && uv run mypy src tests && uv run pytest
just clients-python               # from the repository root: the same, plus a build
just clients-python-conformance   # needs docker
```

Why uv, with the hatchling build backend: that is what the estate's other Python
package (`truvity/policy`) does, uv is pinned in devbox, and one tool locks
(`uv.lock`), runs and builds (`uv build`). Hatch itself (the project manager) would
add a second environment tool for nothing.
