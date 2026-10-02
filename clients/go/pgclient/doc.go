// Package pgclient is the Go adapter of the cnpg PostgreSQL client contract
// (clients/README.md): a pgx/v5 pool that always verifies the server
// (verify-full, against the given CA file only), reloads its certificate,
// key and password files for every new connection, and ships the failover
// behaviour a CloudNativePG `-rw` Service needs.
//
// It is configured with the same environment the cnpg-client Helm library
// chart exports (PGHOST, PGPORT, PGDATABASE, PGUSER, PGSSLROOTCERT,
// PGSSLCERT, PGSSLKEY), see [FromEnv].
package pgclient
