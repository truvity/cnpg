# PostgreSQL client adapters

Small libraries that connect an application to a CloudNativePG cluster the
way this repository's charts expect, so each product does not rewrite it:
`verify-full` always, certificates and passwords re-read for every new
connection, retry that understands a switchover, a health check, and opt-in
OpenTelemetry. One contract, one conformance suite, one implementation per
language.

| Language | Where | Driver | Status |
|---|---|---|---|
| Go | [`go/`](go/README.md), `github.com/truvity/cnpg/v2/clients/go/pgclient` | pgx/v5 | available |
| TypeScript | [`ts/`](ts/README.md), `@truvity/cnpg-client` | node-postgres (`pg`) | available |
| Python | `python/` | psycopg 3 | planned |
| Kotlin | `kotlin/` | pgjdbc + HikariCP | planned |

## The contract

Every adapter implements this; a conformance suite proves it (see below).

### Inputs

The names are the libpq environment the [`cnpg-client`](../docs/connecting.md)
library chart already exports, so a chart that includes `cnpg-client.env`
needs no extra wiring. Nothing else is read implicitly. Every adapter also
takes the same values programmatically.

| Variable | Meaning | Default |
|---|---|---|
| `PGHOST` | the cluster's `-rw` Service name; the server certificate must carry it | required |
| `PGPORT` | | `5432` |
| `PGDATABASE`, `PGUSER` | | required |
| `PGSSLROOTCERT` | server CA file (`<cluster>-server-ca`, `ca.crt`) | **required, always** |
| `PGSSLCERT`, `PGSSLKEY` | client certificate and key of a certificate role | both or neither |
| `PGSSLMODE` | read only to **refuse** anything but `verify-full` | unset = `verify-full` |
| `PGPASSWORD` | password of a scram role | none |
| `CNPG_CLIENT_PASSWORD_FILE` | file holding the password; wins over `PGPASSWORD`; re-read per connection | none |
| `PGAPPNAME` | `application_name` | the program name |
| `PGCONNECT_TIMEOUT` | seconds | `5` |
| `CNPG_CLIENT_STATEMENT_TIMEOUT` | `30s`, `500ms`; `0` disables | `30s` |
| `CNPG_CLIENT_IDLE_TX_TIMEOUT` | `idle_in_transaction_session_timeout` | `60s` |
| `CNPG_CLIENT_POOL_MAX`, `_POOL_MIN` | | `10`, `0` |
| `CNPG_CLIENT_CONN_MAX_LIFETIME` | keep below the client certificate's life | `30m` |
| `CNPG_CLIENT_CONN_MAX_IDLE` | | `5m` |
| `CNPG_CLIENT_HEALTH_PERIOD` | background pool health check, where the driver has one | `30s` |
| `CNPG_CLIENT_RETRY_ATTEMPTS`, `_RETRY_MAX_DELAY`, `_RETRY_BUDGET` | see below | `5`, `5s`, `30s` |

There is no free-form DSN. A parameter passed through a string can be dropped
on the way to the driver, and a dropped `sslrootcert` turns `verify-full`
into a connection that does not verify. The adapters take parts.

### TLS and credentials

- `verify-full`, TLS 1.2 or later, trust is **only** the given CA file (not
  the system store), the server name is `PGHOST`.
- No code path skips verification, accepts `sslmode=require`, or runs
  without a CA. Configuration that asks for it is an error, listing every
  problem at once.
- The CA, certificate, key and password file are read **for every new
  physical connection**, never cached at start. Pools recycle connections at
  `CONN_MAX_LIFETIME`, so a cert-manager renewal reaches the process within
  one lifetime without a restart (see [`docs/connecting.md`](../docs/connecting.md)).
- The password and key material never appear in a string form, JSON, log
  line, error or span attribute.

### Failover

- Dial the `<cluster>-rw` Service, never an instance. The operator moves the
  Service on a switchover; the adapter does no primary discovery.
- A switchover or crash breaks connections (SQLSTATE `57P01`, `57P02`,
  `08xxx`, EOF, reset) or leaves a demoted primary answering read-only
  (`25006`). The pool drops a broken connection; the next dial follows the
  Service.
- The retry helper (`Retry` / `pool.Do` in Go, `retry` / `pool.do` in
  TypeScript) retries **only** connection-class errors (above, plus `57P03`
  and `53300`), with exponential backoff and full jitter (200ms doubling to
  the max delay), stopping at the attempt count or the waiting budget and
  honouring cancellation. It never retries constraint, syntax, permission,
  authentication or certificate errors, a statement timeout, or a
  serialization failure (`40001` and `40P01` are the caller's transaction
  retry). The unit of retry is the caller's function, which must be safe to
  run again: a statement that may have committed before the connection broke
  is the caller's to reason about.
- The first connection retries the same way (a startup race with the Service).

### Health and observability

- `Health` / `health()`: `SELECT 1` on a pooled connection, bounded to 2s,
  through the real path (Service, TLS, authentication). Suits a readiness
  probe.
- OpenTelemetry is opt-in and uses the API only: client spans named
  `<OPERATION> <database>` with `db.system.name`, `db.namespace`,
  `db.operation.name`, `db.query.text` (the statement with its placeholders,
  **never the arguments**), `server.address`, `server.port`, and error
  status on failure.
- `application_name` is always set, so `pg_stat_activity` and traces agree.

Migrations are out of scope: run them as a separate job with a role that has
DDL rights; only `PGUSER` differs.

## Conformance

[`conformance/cases.txt`](conformance/cases.txt) is the contract's executable
form: one case name per line. Each language's suite names its tests exactly
that and runs them against a real PostgreSQL that serves TLS:

- [`conformance/pg-tls.sh`](conformance/pg-tls.sh) generates, per run, a CA, a
  second unrelated CA, a server certificate that carries only the name
  `localhost`, two client certificates for a certificate role and a password
  role, and starts a PostgreSQL image pinned by digest with `ssl=on` and an
  hba of `hostssl ... cert` / `scram-sha-256`. (A GitHub Actions `services:`
  container cannot be used: it starts before any step can generate the
  certificates.)
- [`conformance/guard.sh`](conformance/guard.sh) reads the runner's machine
  output and fails unless **every** case ran and passed. A suite that skips
  because no Postgres was there exits 0 and reads as green; the guard is what
  makes that a red job.

```
just clients-go-conformance
just clients-ts-conformance
just clients-ts            # lint, types, unit tests, build; no Postgres
```

The conformance tests skip without `CNPG_CLIENTS_PG_HOST` so that
`go test ./...` and `npm test` stay local-friendly; `CNPG_CLIENTS_PG=required`
(set by the recipes) turns a missing Postgres into a failure.

## Releasing

One `v*` tag stamps the charts, the Go module and the binaries. The Go adapter
is part of the `github.com/truvity/cnpg/v2` module, so it needs nothing more.
The npm package is not published yet; see `CHANGELOG.md`.
