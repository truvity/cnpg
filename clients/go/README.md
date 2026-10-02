# pgclient (Go)

`github.com/truvity/cnpg/v2/clients/go/pgclient`: the Go adapter of the
[client contract](../README.md), on `pgx/v5`.

```go
cfg, err := pgclient.FromEnv(os.Getenv) // PGHOST, PGSSLROOTCERT, ... (see the contract)
if err != nil { /* every problem is listed */ }
pool, err := pgclient.New(ctx, cfg)     // verifies the first connection, retrying
if err != nil { /* ... */ }
defer pool.Close()

err = pool.Do(ctx, func(ctx context.Context) error { // idempotent unit of work
    _, err := pool.Exec(ctx, "update ...")
    return err
})
```

`*pgclient.Pool` embeds `*pgxpool.Pool`, so the whole pgx API is there, and
`stdlib.OpenDBFromPool(pool.Pool)` hands the same pool to `database/sql` or
gorm.

## What it does

- Builds the connection from parts (no DSN): `sslmode=verify-full`, the CA
  file as the only trust root, `application_name`, `statement_timeout` and
  `idle_in_transaction_session_timeout` as session parameters. After parsing
  it checks that pgx really produced a verifying configuration and refuses to
  connect otherwise.
- `BeforeConnect` re-reads the CA, certificate, key and `PasswordFile` for
  every new connection. `MaxConnLifetime` (30m, jittered 10%) recycles
  connections, so a renewed certificate is used without a restart.
- `Pool.Health(ctx)`, `Pool.Do(ctx, fn)`, `pgclient.Retry`, `pgclient.IsRetryable`.
- `Config` redacts its password in `%v`, `%+v`, `%#v` and `slog`.

## Tracing

```go
import "github.com/truvity/cnpg/v2/clients/go/pgclient/otelpg"

cfg.Tracer = otelpg.New(nil) // global tracer provider
```

`otelpg` depends on the OpenTelemetry API only. Statements are recorded with
their placeholders; arguments are never recorded.

## Tests

`go test ./clients/...` runs the unit tests. The conformance cases need a TLS
PostgreSQL: `just clients-go-conformance`.
