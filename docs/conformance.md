# Conformance

The render tests prove what the charts emit. They cannot prove what the real
components do with it: whether cert-manager signs, whether approver-policy
refuses, whether trust-manager delivers a Secret the operator accepts, whether
CloudNativePG serves a renewed certificate. `conformance/` does, on a
disposable kind cluster. It is the permanent form of the spikes that decided
the per-database CA design ([decision 0004](decisions/0004-per-database-ca.md)).

```sh
just conformance                 # one kind cluster, created and deleted by the run
CNPG_CONFORMANCE_BACKUP=1 just conformance   # plus backup and point-in-time restore
CNPG_CONFORMANCE_KEEP=1 just conformance     # leave the cluster for debugging
```

It needs `docker`, `kind`, `kubectl` and `helm` on `PATH` (the dev shell pins
them) and the network. It is **not** part of `just check`, which stays free of
a cluster and of credentials: without `CNPG_CONFORMANCE` every test skips, so
`go test ./...` is unchanged. `just conformance` sets
`CNPG_CONFORMANCE=required`, under which a missing tool is a failure, not a
skip: a contract test that quietly skipped would prove nothing. Any other
non-empty value runs the suite and skips for a missing tool.

## What it installs

One single-node kind cluster (`conformance/fixtures/kind.yaml`; a second kind
cluster on one machine can exhaust inotify instances). Its kubeconfig is a
temporary file: the run never reads or writes the caller's, and helm state is
kept in the same scratch directory. Everything is pinned in
`conformance/harness_test.go`.

1. cert-manager, with `disableAutoApproval=true`. Left on, its built-in
   approver approves every request first and approver-policy's refusals would
   be decoration.
2. approver-policy and trust-manager (secret targets on, authorized for
   `pg-client-ca` only), then `conformance/fixtures/policies.yaml`: four
   narrow test-only policies for the requests the charts do not cover (trust-manager's webhook, the
   chart's self-signed root, the server certificate, the barman-cloud
   plugin). They are scoped by issuer and namespace so they cannot admit what
   assertion 3 expects to be refused.
3. The upstream `cloudnative-pg` chart with `examples/operator/` values (the
   preset, plus its fast-rollout overlay and the PodMonitor off, since kind has
   no such CRD).
4. `charts/cnpg-platform` with the admission guard on.
5. `charts/cnpg-cluster`: two instances, `serverTLS` from a cert-manager
   `Issuer` the suite owns, and `trust.ca`, `trust.policy`, `trust.bundle`,
   `replication` all on.
6. `charts/cnpg-database`: a database and two roles, each with a client
   certificate from the per-database ClusterIssuer.
7. Two pods from a test-only chart (`conformance/fixtures/client`) that use
   the `cnpg-client` helpers, one per role, running as a non-root user.

## Assertions

| # | Test | What it proves |
|---|------|----------------|
| 1 | `TestClientAuthentication/1…` | A role certificate from the per-database CA logs in with `sslmode=verify-full`, from a pod configured by the `cnpg-client` helpers; the server sees the certificate's subject. |
| 2 | `TestClientAuthentication/2…` | The same common name from another CA is refused (`unknown ca`). |
| 3 | `TestApproval/3a…`, `3b…` | approver-policy refuses a request for the ClusterIssuer from another namespace, and a common name outside the allow-list; nothing is issued. |
| 4 | `TestApproval/4…` | The admission policy refuses a `DatabaseRole` with `superuser: true` in a product namespace, and admits the same role without it. |
| 5 | `TestReplication/5…` | With `clientCASecret` holding no `ca.key` and our `replicationTLSSecret`, two instances are ready, the replica streams as `streaming_replica`, and data reaches it. |
| 6 | `TestReplication/6…` | After a forced renewal the new server certificate is served and no instance restarted: same pod UIDs, same restart counts, same postmaster start time. This relies on the `cnpg.io/reload` label. |
| 7 | `TestClientAuthentication/7…` | With a password set on a certificate-only role, a password login without a client certificate is refused: there is no scram fallback. |
| 8 | `TestBackupAndRestore` | Opt-in. A barman-cloud base backup and WAL archive to an S3 endpoint in the cluster, and a point-in-time restore of a second cluster that stops before a row written after the target. |

Notes on how they are made:

- Assertion 6 forces the renewal the way `cmctl renew` does, by adding an
  `Issuing` condition to the Certificate's status, and reads the certificate
  a server presents with a TLS handshake through a port-forward. `pg_stat_ssl`
  reports the serial of the *client* certificate, never the server's, so it
  cannot show this.
- Assertion 2 builds its other CA and certificate with the Go standard
  library and installs nothing for it: the point is that only the issuer
  differs.
- Assertion 8 uses LocalStack's community edition, by the digest the family's
  kind box pins, as the S3 endpoint: MinIO's public images are no longer
  published, and the assertion needs an endpoint, not a product.
- On failure the suite prints pods, certificates, requests, events and the
  operator's and approver's logs, so a CI log is enough to start from.

## CI

`.github/workflows/conformance.yaml` calls the same shared `check` workflow as
`ci.yaml`, with the `conformance` recipe. It runs on changes to the charts, the
suite or the operator preset, weekly, on release tags and on demand. It is
not a required check and is not part of `check`'s fan-in. The runner is a
GitHub-hosted one and the cluster is kind: no cloud account, no credential.

## Adding an assertion

Add a subtest to the test file that matches its subject, using the `suite`
helpers (`kubectl`, `psql`, `superuserSQL`, `eventually`). Number it in the
table above. An assertion that needs a new component adds it to `up` in
`setup_test.go` with a pinned version, and a policy to `policies.yaml` if it
requests certificates: with the default approver off, an unapproved request
waits forever.
