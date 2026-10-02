# @truvity/cnpg-client (TypeScript)

The TypeScript adapter of the [client contract](../README.md), on
`pg` (node-postgres). ESM; on Node 22.12 or later `require()` works too.
Published to GitHub Packages.

## Install

GitHub Packages answers 401 to anonymous installs even for a public package,
so a token with `read:packages` is always needed. In `.npmrc`:

```
@truvity:registry=https://npm.pkg.github.com
//npm.pkg.github.com/:_authToken=${GITHUB_PACKAGES_TOKEN}
```

```
npm install @truvity/cnpg-client
```

Locally the token can be `$(gh auth token)` (after
`gh auth refresh -s read:packages`); in GitHub Actions use
`${{ github.token }}` with `packages: read`. Yarn 4: set
`npmScopes.truvity.npmRegistryServer` to `https://npm.pkg.github.com` and
`npmAuthToken` in `.yarnrc.yml`.

```ts
import { CnpgPool, configFromEnv } from "@truvity/cnpg-client";

const pool = await CnpgPool.create(configFromEnv()); // PGHOST, PGSSLROOTCERT, ... ; retries the first connection
const { rows } = await pool.query("select $1::int as n", [1]);

await pool.do(async () => {           // idempotent unit of work, retried on connection-class errors
  await pool.query("update ...");
});

app.get("/readyz", async (_req, res) => {
  await pool.health().then(() => res.sendStatus(200), () => res.sendStatus(503));
});
```

## What it does

- Always `verify-full`: `ssl` is built with `rejectUnauthorized: true`, the
  given CA as the only trust root and `servername` set to the host. A
  missing CA, a `sslMode` other than `verify-full`, or a half client
  certificate is a `ConfigError` that lists every problem.
- The CA, certificate, key and `passwordFile` are read again for every new
  connection: `pgPoolConfig()` returns a `pg.PoolConfig` whose `Client` class
  loads them in its constructor, which is the one hook node-postgres gives
  for per-connection TLS material. `maxConnLifetimeMs` (30m) recycles
  connections, so a renewed certificate is used without a restart. (No
  per-connection jitter: node-postgres has a single pool-wide lifetime.)
- An error on an idle pooled connection is handled (`onError`), never thrown:
  a `pg.Pool` without an `error` listener takes the process down when the
  primary goes away.
- `pool.health()`, `pool.do(fn)`, `retry(policy, fn)`, `isRetryable(err)`.
- `toJSON()` and `redactConfig()` carry no password or key material.

For TypeORM or another library that owns the pool, pass the options through:
`extra: pgPoolConfig(config)`.

## Tracing

```ts
import { trace } from "@opentelemetry/api";
const pool = await CnpgPool.create(config, { tracer: trace.getTracer("my-service") });
```

`@opentelemetry/api` is an optional peer dependency. `pool.query` is traced
(statement with placeholders, never arguments); statements on a client from
`pool.connect()` are not.

## Develop

```
cd clients/ts
npm ci && npm run lint && npm run typecheck && npm test && npm run build
just clients-ts-conformance   # from the repository root; needs docker
```
