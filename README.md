# cnpg-cluster

Helm charts for running PostgreSQL on [CloudNativePG](https://cloudnative-pg.io/):

- **charts/cnpg-cluster** — a CNPG `Cluster` with an opinionated two-posture
  profile model, plus roles, declared databases, backup `ObjectStore`s and
  `ScheduledBackup`s.
- **charts/cnpg-database** — a logical `Database` inside an existing cluster
  (CNPG 1.28+ declarative database management), for a consumer that owns its
  database but not the cluster.

Published as OCI charts:

```
oci://ghcr.io/truvity/charts/cnpg-cluster
oci://ghcr.io/truvity/charts/cnpg-database
```

## Who it is for

A platform that runs the [CloudNativePG operator](https://cloudnative-pg.io/)
(1.30+ — `cnpg-cluster` sets the quorum-failover annotation unconditionally on
its `prod` profile) and wants every PostgreSQL cluster it hosts to come from
one of two hardened postures instead of a bespoke `Cluster` manifest per
project. It assumes nothing about node scheduling, backup storage or server
TLS beyond the operator itself — `scheduling`, `backup` and `serverTLS` are
all plain inputs with neutral (empty) defaults; see "Install and a worked
example" below for how two different estates fill them in.

It does not install the CNPG operator, a storage class, or cert-manager —
those are the platform's own.

## The model

`profile` (`devel` | `prod`, required) is the only posture knob a project
never gets to set itself: it decides instance count, storage size, the WAL
volume, resource defaults and pod anti-affinity. A **platform** injects
`profile` and the `backup` coordinates (the bucket and the identity allowed
to write to it); a **project** declares `databases`, `roles` and `customers`
inside the cluster the platform gave it. `charts/cnpg-database` absorbed into
`cnpg-cluster` at v2 (a project can declare its database inline via
`databases[]`) but remains standalone for a consumer that owns a database
inside a cluster it does not own.

## Install and a worked example

```sh
helm install pg oci://ghcr.io/truvity/charts/cnpg-cluster --version 2.0.0 -f values.yaml
```

A minimal `values.yaml` that renders as written:

```yaml
clusterName: pg
namespace: default
profile: devel
```

### Scheduling: estate-shaped examples

`scheduling` and `backup.encryption`/`backup.endpoint` are estate facts —
a node pool name, an arch taint, whether a bucket needs an encryption
header — and none of them defaults to any particular estate's shape.
Two examples, both proven to render in `charts/cnpg-cluster/examples_test.go`:

A platform with a tainted Karpenter `database` node pool, an arch taint on
every node, and buckets that require `AES256`:

```yaml
clusterName: pg
namespace: default
profile: prod
scheduling:
  databasePool: database
  tolerations:
    - key: arch
      operator: Exists
backup:
  enabled: true
  bucketName: example-backups
  # encryption is left unset — AES256 is still the chart's default
```

A platform backed by an R2 bucket instead — no node pool is implied by a
backup target, so `scheduling` is left out entirely, and R2 rejects the
`AES256` header this chart asks for by default:

```yaml
clusterName: pg
namespace: default
profile: prod
backup:
  enabled: true
  bucketName: example-backups
  endpoint: https://example-account.r2.cloudflarestorage.com
  existingSecret: pg-backup-s3   # AWS_ACCESS_KEY_ID + AWS_SECRET_ACCESS_KEY
  encryption: ""                 # R2 rejects x-amz-server-side-encryption
```

Full value-by-value reference: [`docs/reference.md`](docs/reference.md).

## Consumers

| repo | consumers (surface) |
|---|---|
| cnpg-cluster | truvity/gitops (chart `cnpg-cluster`) |

## Neighbours

- **openbao ↔ cnpg-cluster**: openbao is the credential source behind
  `backup.existingSecret` (or the pod identity behind `inheritFromIAMRole`)
  — this chart takes a Secret name or an inherited role, never a credential
  of its own.
- **audit ↔ cnpg-cluster**: in a consuming estate, `audit`'s own database
  runs on a cluster this chart renders. The dependency is one-directional —
  `cnpg-cluster` carries no knowledge of what runs on it.

## Documentation

- [`docs/adoption.md`](docs/adoption.md) — how a platform takes this chart
  into use, including the breaking migrations between the `scheduling`
  shape older releases assumed and the plain-input shape at HEAD.
- [`docs/reference.md`](docs/reference.md) — every value, generated from
  `values.yaml`'s own comments.

## The rule that makes this repository public

Mechanism only: nothing in this repository names an account, a cluster, a
bucket, a hostname or a secret path — every such thing is an input with a
neutral default, and the consuming estate supplies it from its own private
repository. `hack/leak-canary.sh` enforces this mechanically and runs in CI
as its own job. The full contract this repository is held to lives at
[truvity/policy `docs/contracts/component.md`](https://github.com/truvity/policy/blob/master/docs/contracts/component.md).

## Status

Used in production by its maintainers.

## Development

```sh
devbox shell   # or rely on direnv
just check     # test + lint + charts + leak-canary
```

Chart versions are placeholders (`0.0.0`); the release workflow injects the
real version from the git tag when packaging. `just check` intentionally
excludes `vuln` (`.github/workflows/security.yaml` runs it separately, daily
and un-required) — a standard-library advisory with no released fix must not
turn every pull request red.

## Releasing

Tag-triggered (`.github/workflows/release.yaml`): pushing `vX.Y.Z` packages
and publishes both charts at that version. `.github/workflows/auto-release.yaml`
can cut patch tags on its own — same-day for a push whose merged PR carries
the `security` label, weekly otherwise — but only once `vars.AUTO_RELEASE` is
set for this repository; see that workflow for today's setting. Minors and
majors are always cut by hand, after the `CHANGELOG.md` heading for that
version has merged.

## Provenance

Extracted from its maintainers' internal estate so the cluster and database
shapes can be consumed by any estate, MIT-licensed.

## Licence

MIT — see [LICENSE](LICENSE).
