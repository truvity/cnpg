# Connecting an application

`charts/cnpg-client` mounts the role's client certificate and the server CA
and sets the libpq environment for `sslmode=verify-full`. The files are
Secret projections: cert-manager renews the Secrets and the kubelet updates
the files in place, **without restarting the pod**. What matters per driver is
whether it re-reads them.

Rules that hold for every driver: connect to the `PGHOST` the chart sets (the
server certificate names the Services, `verify-full` checks the host name),
cache no connector or pool that outlives the certificate, and treat the
certificate's lifetime as the longest a connection may live.

- **libpq and everything built on it** (psql, psycopg, psycopg2, asyncpg's
  libpq-less cousin excluded): reads the files on every new connection, so a
  renewed certificate is picked up by the next one. Keep pools from holding a
  connection past the certificate's life (`max_lifetime`), and have psycopg
  reconnect on failure rather than reuse a connection object.
- **pgx (Go):** parse the config once, but load the certificate in
  `BeforeConnect`, so each new connection reads the current files; a
  `tls.Config` built once at start holds the first certificate forever. Set
  `MaxConnLifetime` below the certificate's life.
- **node-postgres:** pass `ssl` with `ca`, `cert` and `key` read from the
  files when a connection is made (a function that returns the options, not a
  value computed at import); `pg.Pool` with `maxLifetimeSeconds` recycles the
  old ones.
- **pgjdbc:** reads the key as DER PKCS#8, not PEM; set `keyDer: true` so
  `key.der` is projected, and use `jdbcEnv` (`sslkey` points at it). The
  driver reads the files when a connection opens, so the pool must recycle
  connections (HikariCP `maxLifetime`) before the certificate expires. Do not
  substitute a cached `SSLContext` or a connector that loads the key once.

No driver is asked to disable verification: the chart refuses any `sslmode`
but `verify-full`.

The file modes default to `0444` (certificate) and `0440` (key); set the
pod's `securityContext.fsGroup` so a non-root process can read the key.
