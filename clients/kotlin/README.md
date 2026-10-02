# com.truvity.cnpg:cnpg-client (Kotlin / JVM)

The Kotlin adapter of the [client contract](../README.md), on pgjdbc and
HikariCP. Java 21 or later; usable from Java as well as Kotlin.

```kotlin
import com.truvity.cnpg.CnpgConfig
import com.truvity.cnpg.CnpgPool

val pool = CnpgPool.create(CnpgConfig.fromEnv())   // PGHOST, PGSSLROOTCERT, ... ; retries the first connection
val n = pool.query("select ?::int as n", listOf(1)) { it.getInt("n") }.first()

pool.withRetry {                                    // idempotent unit of work, retried on connection-class errors
    pool.update("update ...")
}

// readiness probe
fun ready(): Boolean = runCatching { pool.health() }.isSuccess
```

`pool.dataSource` is the pooled `javax.sql.DataSource` (HikariCP) for an ORM or
a query library. Statements that go through it are not traced and not retried;
wrap the unit of work in `pool.withRetry { }`.

## Install

The package is on **GitHub Packages**. GitHub Packages asks for a token even
when the repository is public, so a consumer needs a token in
`~/.m2/settings.xml` (a classic personal access token with `read:packages`;
in GitHub Actions, `GITHUB_TOKEN` with `packages: read`):

```xml
<settings>
  <servers>
    <server>
      <id>github-truvity-cnpg</id>
      <username>YOUR_GITHUB_LOGIN</username>
      <password>${env.GITHUB_TOKEN}</password>
    </server>
  </servers>
</settings>
```

and the repository in the `pom.xml` (the `id` must match the server id):

```xml
<repositories>
  <repository>
    <id>github-truvity-cnpg</id>
    <url>https://maven.pkg.github.com/truvity/cnpg</url>
  </repository>
</repositories>

<dependency>
  <groupId>com.truvity.cnpg</groupId>
  <artifactId>cnpg-client</artifactId>
  <version>2.7.0</version> <!-- the repository tag, without the v -->
</dependency>
```

The version is the repository's `v*` tag, shared with the charts, the Go
module and the other clients, so a chart-only release publishes an unchanged
client under a new number. Maven Central is a possible later move; nothing in
the API depends on the registry.

## What it does

- Always `verify-full`: `sslmode=verify-full`, pgjdbc's `LibPQFactory` (it
  builds its trust store from the `sslrootcert` file alone, never the JVM's),
  the host name checked against the certificate. A missing CA, a `sslMode`
  other than `verify-full`, or half a client certificate is a `ConfigError`
  listing every problem. TLS 1.2 is the floor on any supported JDK (21
  disables the older protocols by default).
- No free-form URL. The JDBC URL is built from the parts and the host is
  restricted to one name or address.
- `ReloadingDataSource` builds the connection properties anew for every
  physical connection. pgjdbc reads `sslrootcert`, `sslcert` and `sslkey` when
  it opens a connection; the password file is read in the data source. So a
  renewed Secret reaches the next connection without a restart, within
  `maxConnLifetimeMs` (30m) for the connections already open.
- `pool.health()` (`select 1`, bounded to 2s), `pool.withRetry { }`,
  `retry(policy) { }`, `isRetryable(throwable)` (reads the whole cause chain:
  pgjdbc reports a refused certificate as SQLSTATE `08001` with the TLS error
  as the cause, and HikariCP wraps the last failure in a timeout).
- `toString()` of the configuration and the pool carries no password or key
  material.

## Differences from the contract's letter

- **Key format.** pgjdbc reads the client key as DER PKCS#8 (or an
  unencrypted PEM PKCS#8 key), not as the `tls.key` cert-manager writes by
  default. Point `PGSSLKEY` at `key.der`: in the `cnpg-client` chart set
  `keyDer: true` (and `additionalOutputFormats: [DER]` on the role). See
  [`docs/connecting.md`](../../docs/connecting.md).
- **Lifetime floors.** HikariCP does not retire a connection sooner than 30
  seconds, an idle one sooner than 10 seconds, nor run its keepalive more
  often than every 10 seconds. A configuration below that is a `ConfigError`
  rather than silently becoming 30 minutes, as HikariCP would do.
- **`CNPG_CLIENT_HEALTH_PERIOD`** drives HikariCP's keepalive of idle
  connections (`0` turns it off).
- **Names.** `pool.withRetry { }` is the contract's `pool.Do` / `pool.do`
  (`do` is a Kotlin keyword).
- **Blocking.** The API is blocking JDBC. Retrying sleeps the calling thread;
  interrupting the thread stops it.

## Tracing

```kotlin
val pool = CnpgPool.create(config, tracer = openTelemetry.getTracer("my-service"))
```

Only `opentelemetry-api` is a dependency. `pool.query` and `pool.update` are
traced: a client span `SELECT app` with `db.system.name`, `db.namespace`,
`db.operation.name`, `db.query.text` (the statement with its `?`
placeholders, never the arguments), `server.address` and `server.port`.

## Develop

```
cd clients/kotlin
mvn verify                    # compile and unit tests (JDK 21, Maven 3.9: devbox has both)
just clients-kotlin           # the same, plus the dry run of the release artifacts
just clients-kotlin-conformance   # from the repository root; needs docker
```

Why Maven: the estate's JVM builds are Maven (the Keycloak providers), devbox
already pins its JDK and Maven, and a library this small gains nothing from a
second build tool. Coordinates and the release job are in
[`.github/workflows/release.yaml`](../../.github/workflows/release.yaml).
