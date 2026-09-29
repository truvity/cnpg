# cnpg-cluster

Helm charts for running PostgreSQL on [CloudNativePG](https://cloudnative-pg.io/):

- **charts/cnpg-cluster** — a CNPG `Cluster` with an opinionated two-posture
  profile model, plus roles, declared databases, backup `ObjectStore`s and
  `ScheduledBackup`s.
- **charts/cnpg-database** — a logical `Database` inside an existing cluster
  (CNPG 1.28+ declarative database management), for consumers that own their
  database but not the cluster.

Published as OCI charts:

```
oci://ghcr.io/truvity/charts/cnpg-cluster
oci://ghcr.io/truvity/charts/cnpg-database
```

## The profile contract

`profile` is **required** and must be `devel` or `prod` — a missing value fails
the render rather than silently choosing a posture:

- `devel` — 1 instance, `enablePDB: false`. A single-instance cluster has
  nowhere to fail over; a PDB would only stall node maintenance.
- `prod` — 3 instances, `enablePDB: true`. Replicas make eviction survivable
  via switchover; the PDB protects the primary while Karpenter (or any drainer)
  moves replicas freely.

`enablePDB` is always emitted explicitly (CNPG's server-side default is `true`,
so omission on devel would resurrect the drain-blocking primary PDB). The chart
tests in `charts/cnpg-cluster/chart_test.go` pin this.

## Server certificate

By default the operator mints a self-signed CA per cluster (`<cluster>-ca`) and
signs the server certificate from it. Set `serverTLS.issuerRef.name` to take the
**server** certificate from a cert-manager issuer instead:

```yaml
serverTLS:
  issuerRef: {name: my-issuer, kind: ClusterIssuer, group: cert-manager.io}
  privateKey: {algorithm: ECDSA, size: 384}   # whatever the issuer signs
  caCertificates: |                           # required: the issuer's roots
    -----BEGIN CERTIFICATE-----
    ...
```

The chart renders a `Certificate` for `<cluster>-{rw,ro,r}.<namespace>.svc.cluster.local`
and a `<cluster>-server-ca` Secret holding `caCertificates`, and points
`spec.certificates.serverTLSSecret` / `serverCASecret` at them.

Both carry `cnpg.io/reload: "true"` (the `Certificate` through
`secretTemplate`): CloudNativePG reloads a user-provided server Secret only
with it, so a cert-manager renewal is served without an instance restart.

Server side only: the client CA stays `<cluster>-ca`, so replication, role client
certificates and `pg_hba` do not change. Clients that do not verify the server
(`sslmode=require`) notice nothing. Clients that do (`verify-ca`, `verify-full`)
must trust the issuer's roots first, and under `verify-full` dial one of the
fully-qualified names above. Unset the value to go back to the operator's CA.

## Backups on an S3-compatible store

`backup` (and `bootstrap.recovery.source`, for a recovery from an archive on a
different store) is AWS-shaped by default: the `ObjectStore` inherits the pod's
own identity and asks for `AES256` server-side encryption on every upload.
Three values, all inert when empty, point it at any S3-compatible store
instead -- Cloudflare R2, MinIO, Ceph RGW or an AWS bucket reached with static
keys:

```yaml
backup:
  enabled: true
  bucketName: my-backups
  endpoint: https://<account>.r2.cloudflarestorage.com   # R2; MinIO/Ceph: your gateway URL
  existingSecret: pg-backup-s3   # AWS_ACCESS_KEY_ID + AWS_SECRET_ACCESS_KEY
  encryption: ""                 # R2 rejects x-amz-server-side-encryption
```

- `endpoint` becomes the `ObjectStore`'s `endpointURL`. A custom endpoint is
  addressed path-style (`endpoint/bucket/key`), the S3 SDK's own default once
  an endpoint is set; every store above accepts it, so there is no addressing
  switch. No region is needed: the SDK signs with `us-east-1` when none is
  configured, which R2 aliases to its single `auto` region and MinIO ignores.
- `endpointCA: {name, key}` names a Secret holding the PEM bundle that verifies
  a private store's certificate.
- `existingSecret` switches `s3Credentials` from `inheritFromIAMRole` to the
  named Secret. `existingSecretKeys` renames the keys; `sessionToken` and
  `region` are read only when named, since the plugin refuses a missing key
  rather than skipping it.
- `encryption: ""` drops the `encryption` field, leaving encryption to the
  bucket's policy. The default stays `AES256`.

With every value at its default the rendered chart is byte-identical to the
previous release; the tests in `charts/cnpg-cluster/objectstore_test.go` pin
both shapes.

## Scheduling

`scheduling.databasePool` (default `database`) selects the Karpenter node pool
CNPG instances land on and tolerates a taint of the same name. Set it to your
pool name (e.g. `durable` for pools named by interruption contract), or to the
empty string to opt out entirely on clusters without a dedicated database pool.

## Development

```sh
devbox shell   # or rely on direnv
just check     # test + lint + chart-lint + vuln
```

Chart versions are placeholders (`0.0.0`); the release workflow injects the
real version from the git tag via `ocictl helmctl` when packaging.

## Provenance

Extracted from its maintainers' internal estate so the cluster and database
shapes can be consumed by any estate, MIT-licensed. Used in production by its
maintainers.

## License

MIT — see [LICENSE](LICENSE).
