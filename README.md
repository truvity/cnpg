# cnpg

Helm charts for running PostgreSQL on [CloudNativePG](https://cloudnative-pg.io/),
split by who installs them. (This repository was `truvity/cnpg-cluster`; GitHub
redirects the old name, and the Go module is `github.com/truvity/cnpg/v2`.)

| chart | installed by | how often |
|---|---|---|
| `cnpg-platform` | the platform | once per Kubernetes cluster |
| `cnpg-cluster` | the platform | once per PostgreSQL cluster |
| `cnpg-database` | a product | once per product (its databases, roles, client certificates) |
| `cnpg-client` | a product (library) | included by each application chart that connects |

- **charts/cnpg-platform** — what a platform adds beside the upstream operator
  charts: storage classes, a metrics network policy, baseline alert rules and
  an admission guard against privileged database roles. The operator and
  barman-cloud plugin presets ship as values under
  [`examples/operator/`](examples/operator/).

- **charts/cnpg-cluster** — a CNPG `Cluster` with an opinionated two-posture
  profile model, plus roles, declared databases, backup `ObjectStore`s and
  `ScheduledBackup`s.
- **charts/cnpg-database** — what a product installs in its own namespace
  against the platform's cluster: `Database` objects (CNPG 1.28+ declarative
  database management), unprivileged `DatabaseRole` objects and per-role client
  certificates, behind one render-time guard.

- **charts/cnpg-client** — a library chart an application chart includes to
  connect with a client certificate and `sslmode=verify-full`: volumes, mounts,
  libpq/JDBC environment and an egress policy. See
  [docs/connecting.md](docs/connecting.md).

- **cmd/cnpgctl** — a command-line tool. `cnpgctl verify` runs read-only
  assertions against a live cluster: health, archiving, backups, TLS,
  reload labels and pg_hba. Prebuilt for linux and darwin (amd64, arm64) on
  each release; see [docs/cnpgctl.md](docs/cnpgctl.md).
- **clients/** — PostgreSQL client adapters that connect an application the
  way these charts expect: `verify-full` only, certificates re-read per
  connection, switchover-aware retry. Go (`pgx`) and TypeScript (`pg`) today,
  Python and Kotlin to follow; one contract and one conformance suite, see
  [clients/README.md](clients/README.md).

Published as OCI charts:

```
oci://ghcr.io/truvity/charts/cnpg-platform
oci://ghcr.io/truvity/charts/cnpg-cluster
oci://ghcr.io/truvity/charts/cnpg-database
oci://ghcr.io/truvity/charts/cnpg-client
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

`cnpg-cluster` does not install the CNPG operator, a storage class, or
cert-manager. The operator is the platform's (see "The operator" below);
cert-manager is the platform's own.

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

### The operator

The upstream `cloudnative-pg` and `plugin-barman-cloud` charts install the
operator. This repository contributes the preset they are installed with
(`examples/operator/cloudnative-pg.values.yaml`: in-place instance-manager
updates, inherited labels and annotations, staggered rollouts) and a chart,
`cnpg-platform`, for the objects around it:

```sh
helm install cnpg-platform oci://ghcr.io/truvity/charts/cnpg-platform --version 2.2.0 -f values.yaml
```

Everything in `cnpg-platform` is off until asked for, and nothing in it is
an estate name: storage class names and provisioner, the namespaces that
host PostgreSQL, the scraper's selector and the tenant label selector are
all inputs. `examples/operator/cnpg-platform.values.yaml` fills each with a
placeholder. It ships no subcharts, because the release workflow cannot
package a chart's dependencies today; see
[`docs/decisions/0002-operator-chart.md`](docs/decisions/0002-operator-chart.md).

## Consumers

| repo | consumers (surface) |
|---|---|
| cnpg-cluster | truvity/gitops (chart `cnpg-cluster`; `cnpgctl` as a verification step) |
| cnpg-platform | not yet adopted |

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
- [`docs/authentication.md`](docs/authentication.md) — the per-database client trust model, hba ordering and the people mapping.
- [`docs/cnpgctl.md`](docs/cnpgctl.md) — the `cnpgctl verify` assertions and flags.
- [`docs/conformance.md`](docs/conformance.md) — the kind conformance suite:
  what the charts do with cert-manager, approver-policy, trust-manager and
  the CloudNativePG operator, asserted on a disposable cluster
  (`just conformance`, separate from `just check`).
- [`docs/reference.md`](docs/reference.md) — every value, generated from
  `values.yaml`'s own comments.
- [`docs/decisions/`](docs/decisions/) — why the repository is shaped as it is:
  `0001` the rename and the charts-by-owner split, `0002` the operator chart.

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
just conformance   # kind cluster + the real components; needs docker (docs/conformance.md)
just clients-go-conformance clients-ts-conformance   # client adapters against a TLS PostgreSQL; needs docker (clients/README.md)
```

Chart versions are placeholders (`0.0.0`); the release workflow injects the
real version from the git tag when packaging. `just check` intentionally
excludes `vuln` (`.github/workflows/security.yaml` runs it separately, daily
and un-required) — a standard-library advisory with no released fix must not
turn every pull request red.

## Releasing

Tag-triggered (`.github/workflows/release.yaml`): pushing `vX.Y.Z` packages
publishes all four charts at that version and attaches the `cnpgctl` archives to the GitHub release. `.github/workflows/auto-release.yaml`
can cut patch tags on its own — same-day for a push whose merged PR carries
the `security` label, weekly otherwise — but only once `vars.AUTO_RELEASE` is
set for this repository; see that workflow for today's setting. Minors and
majors are always cut by hand, after the `CHANGELOG.md` heading for that
version has merged.

## Provenance

Extracted from its maintainers' internal estate so the cluster and database
shapes can be consumed by any estate, MIT-licensed. Renamed from
`cnpg-cluster` when the operator chart arrived; see
[`docs/decisions/0001-absorb-and-rename.md`](docs/decisions/0001-absorb-and-rename.md).

## Licence

MIT — see [LICENSE](LICENSE).
