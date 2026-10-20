# cnpgctl

`cnpgctl` operates on CloudNativePG clusters built from these charts. It has
three commands:

| command | writes? | what |
|---|---|---|
| `verify` | never: every call it makes to the Kubernetes API is a GET | read-only assertions against a live cluster |
| `drill` | **yes**: it creates and deletes one `Cluster` and one `ObjectStore`, and writes one `ConfigMap`, in the drill namespace | a restore drill of a cluster's archive |
| `drill-metrics` | never | serves the recorded drill results as metrics |

Install: the `cnpgctl_<version>_<os>_<arch>.tar.gz` archive of a release
(linux and darwin, amd64 and arm64; `checksums.txt` alongside), the image
`ghcr.io/truvity/cnpg/cnpgctl:<version>` (what the `cnpg-restore-drill` chart
runs), or

```sh
go run github.com/truvity/cnpg/v2/cmd/cnpgctl@<tag> verify --help
```

## verify

```sh
cnpgctl verify --namespace db --cluster pg [--context my-context] [--output json]
```

It reads the `Cluster`, and the Secrets the `Cluster` names, and prints one
line per assertion:

```
== cnpgctl verify: db/pg
  PASS  cluster phase healthy: phase "Cluster in healthy state"
  FAIL  last backup recent: last successful backup 41h0m0s ago, max 26h0m0s
== 14 passed, 1 failed, 2 skipped
```

The exit status is 1 if any assertion FAILs, 2 for a usage or connection
error, 0 otherwise. `SKIP` is an assertion that does not apply (an optional
Secret the cluster does not use) and never fails the run.

| flag | default | meaning |
|---|---|---|
| `--namespace` | required | namespace of the `Cluster` |
| `--cluster` | required | name of the `Cluster` |
| `--kubeconfig` | `$KUBECONFIG`, then `~/.kube/config` | kubeconfig file |
| `--context` | the current context | kubeconfig context |
| `--max-backup-age` | `26h` | how old the last successful backup may be |
| `--min-cert-validity` | `336h` (14 days) | how long each certificate must stay valid |
| `--pool-label` | off | `key=value`: assert every instance Pod runs on a node carrying this label (see below) |
| `--check-applied` | off | assert every `Database` and `DatabaseRole` of the cluster reports `status.applied: true` (see below) |
| `--check-identity` | off | assert the backup sidecar of every instance Pod has Pod Identity credentials (see below) |
| `--output` | `text` | `text` or `json` |

`--output json` prints `{namespace, cluster, results: [{name, status, message}]}`.

### Assertions

| assertion | passes when |
|---|---|
| cluster phase healthy | `status.phase` is `Cluster in healthy state` |
| instances ready | `status.readyInstances` equals `spec.instances` |
| primary known | `status.currentPrimary` is set |
| continuous archiving | the `ContinuousArchiving` condition is `True` |
| recoverability window | `status.firstRecoverabilityPoint` is set |
| last backup recent | `status.lastSuccessfulBackup` is within `--max-backup-age` |
| TLS server certificate | the Secret named by `spec.certificates.serverTLSSecret` is labelled `cnpg.io/reload: "true"` and its certificate is valid for `--min-cert-validity` |
| TLS server CA, client CA, replication certificate | same, for `spec.certificates.serverCASecret` / `clientCASecret` / `replicationTLSSecret`; when the spec does not name one, the contract name `<cluster>-server-ca`, `<cluster>-client-ca`, `<cluster>-replication` is checked (the chart names its replication Secret in the spec, so this fallback applies only to hand-written Clusters) if the Secret exists, and skipped if not |
| Secret `<name>` reload label | every user-provided Secret the `Cluster` references (the certificate Secrets above, `superuserSecret`, `bootstrap.initdb.secret`, managed roles' `passwordSecret`, `externalClusters` credentials) carries `cnpg.io/reload: "true"` |
| pg_hba has no trust | no `spec.postgresql.pg_hba` line uses `trust` |
| pg_hba maps have pg_ident rows | every `map=NAME` has at least one `spec.postgresql.pg_ident` row for `NAME` |
| pg_hba catch-all is last | no line follows a catch-all (`all all all`), where it could never match |

Three assertions are opt-in, because they read more than the `Cluster` and its
Secrets (a `get` of nodes, a `list` of Pods and of `databases` and
`databaseroles` in the namespace; still read-only):

| assertion | flag | passes when |
|---|---|---|
| instances on pool | `--pool-label key=value` | at least one Pod labelled `cnpg.io/cluster=<cluster>` exists, and every one is scheduled on a node whose label `key` equals `value`. The failure names each Pod and the node it is on. Use it where a database pool is tainted and labelled, to catch an instance that landed elsewhere |
| Database/DatabaseRole applied | `--check-applied` | every `Database` and `DatabaseRole` with `spec.cluster.name` equal to the cluster has `status.applied: true`. A missing `status.applied` fails ("not reported yet"); an `applied: false` fails with the object's `status.message`. SKIP when the cluster has none, or the CRDs are not installed |
| backup sidecar has Pod Identity | `--check-identity` | every instance Pod (labelled `cnpg.io/cluster=<cluster>`) has, on its `plugin-barman-cloud` container (the sidecar the barman-cloud plugin injects), both `AWS_CONTAINER_CREDENTIALS_FULL_URI` and `AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE` set. The failure names each Pod and the missing variable (names only; a value is never printed). FAIL when there are no instance Pods or when a Pod has no sidecar while another has one; SKIP when no Pod has the sidecar (the cluster does not archive through the plugin) |

`--check-identity` is the detective half of the Pod Identity check: EKS injects
the credentials when a pod is admitted, and a pod admitted before the
association was visible to the webhook has none for life (restarting the
container does not admit it again). The preventive half is the
`cnpg-projects` `identityCheck` hook ([backup-access.md](backup-access.md)).
It proves the environment is present, not which role it is bound to. After a
failure, delete the pod (replicas first, the primary by switchover) so it is
recreated and admitted afresh.

In Go, set `verify.Options.PoolLabel` (`"key=value"`, see `verify.ParsePoolLabel`)
`verify.Options.CheckApplied` and `verify.Options.CheckIdentity`. The data comes
from three optional interfaces, `verify.PlacementSource` (`InstanceNodes`,
`NodeLabels`), `verify.DeclarativeSource` (`DeclarativeObjects`) and
`verify.IdentitySource` (`InstancePods`), which `KubeSource`
implements; `verify.Source` is unchanged, so an existing implementation keeps
compiling, and with an option set a `Source` that lacks the interface gets a
FAIL result rather than a panic.

For a CA Secret holding a bundle, the validity check uses the longest-lived
certificate in it, so a retired root beside the live one does not fail it.

### What it does not do

- **Archive age.** The `Cluster` status carries the `ContinuousArchiving`
  condition but not the time of the last archived WAL segment (that is
  `pg_stat_archiver` inside the instance), and `verify` does not exec into
  pods. A stalled archive therefore shows as the condition turning `False`
  and, in time, as a stale `lastSuccessfulBackup`.
- **Reading object storage.** It does not list the bucket.

## drill

```sh
cnpgctl drill --namespace shop --cluster shop-pg --drill-namespace cnpg-drills [--output json]
```

The explicitly mutating command: a restore drill of the source cluster's
archive. It creates a one-instance `Cluster` named `<cluster>-restore` (the
read-only restore ServiceAccount's name) that recovers the whole archive, base
backup and every WAL segment, with no recovery target; waits until it is
promoted and healthy; checks it; deletes it with its `ObjectStore`, pods and
volume claims; and writes the result to the `ConfigMap`
`<cluster>-restore-result`. The source `Cluster` is only read. How it works,
why it can only read the archive, and what it proves are in
[restore-drill.md](restore-drill.md); the `cnpg-restore-drill` chart runs it
monthly.

Progress and, after a failure, diagnostics (the Cluster's phase and
conditions, every pod's containers, recent events and log lines) go to stderr;
the report goes to stdout, one line per step:

```
== cnpgctl drill: shop/shop-pg -> cnpg-drills/shop-pg-restore
  PASS  plan: archive s3://example-backups/shop/shop-pg server shop-pg; image ghcr.io/cloudnative-pg/postgresql:17.6; storage 20Gi (class "example-delete")
  PASS  no archiving: the drill Cluster has no backup section and no plugin entry; ...
  PASS  create: ObjectStore shop-pg-restore-source and Cluster shop-pg-restore in cnpg-drills
  PASS  recovery: the drill Cluster is healthy with a promoted primary after 6m12s
  PASS  connect: TLS (verify-full) to shop-pg-restore-rw.cnpg-drills.svc:5432 as postgres
  PASS  promoted: pg_is_in_recovery() is false
  PASS  recovery point: 2026-10-20T11:55:00Z (last replayed transaction), 9m30s old, maximum 24h0m0s
  PASS  databases: shop present (expected from the source)
  PASS  database shop: 12 user tables, 48211 rows (minimum 1)
  PASS  teardown: Cluster shop-pg-restore and ObjectStore shop-pg-restore-source deleted, with their pods and volume claims, in 41s
== drill SUCCEEDED in 7m40s: 10 passed, 0 failed, 0 skipped
```

The exit status is 0 when the drill succeeded, 1 when it failed (and the
failure is recorded), 2 for a usage or connection error (nothing created), and
3 when a drill of the same source is already running (nothing created,
deleted or recorded).

| flag | default | meaning |
|---|---|---|
| `--namespace`, `--cluster` | required | the SOURCE `Cluster` |
| `--drill-namespace` | `--namespace` | where the drill `Cluster`, its `ObjectStore` and the result `ConfigMap` are created |
| `--drill-name` | `<cluster>-restore` | the drill `Cluster`'s name: must be the restore ServiceAccount's (see [restore-drill.md](restore-drill.md#identity)); at most 50 characters |
| `--destination-path`, `--server-name`, `--endpoint-url` | the source's | the archive: `s3://<bucket>/<prefix>`, the barman server name (generation suffix included), the S3 endpoint. Each one left empty is read from the source's barman-cloud plugin entry and its `ObjectStore` |
| `--image` | the source's `status.image` | PostgreSQL image (same major as the source) |
| `--storage-class` | the source's | must delete its volumes: a `Retain` class is refused when readable |
| `--allow-retained-volumes` | off | accept a `Retain` class (each drill then leaves its volumes behind) |
| `--storage-size`, `--wal-storage-size` | the source's | the larger of the source's spec and its volume claims' capacity |
| `--cpu-request`, `--memory-request`, `--memory-limit` | `250m`, `512Mi`, `1Gi` | the drill instance |
| `--node-selector key=value`, `--toleration key[=value]:Effect` | the source's | repeatable; either one replaces the copy of the source's affinity |
| `--pod-annotation key=value` | none | repeatable; on the drill's pods and volumes |
| `--sidecar-env NAME=value` | none | repeatable; environment of the barman-cloud sidecar (e.g. `AWS_REGION`). AWS credential variables are refused |
| `--allow-static-credentials` | off | test endpoints only: accept credential variables in `--sidecar-env` (the drill then does not read with the restore identity) |
| `--database` | derived | repeatable; the databases expected. Default: the source's `Database` objects and bootstrap database, else every database but `postgres` |
| `--min-rows <db>=<n>`, `--min-rows <db>/<schema>.<table>=<n>` | `<db>=1` | repeatable; minimum rows over a database's user tables, or in one table |
| `--count` | `exact` | `exact` counts every row of every user table (reads all the data); `estimate` sums `reltuples` |
| `--max-recovery-point-age` | `24h` | how old the recovered data may be |
| `--statement-timeout` | `15m` | bound on each SQL statement |
| `--timeout` | `2h` | bound on the drill up to the end of its checks |
| `--teardown-timeout` | `10m` | bound on the teardown, which runs on its own context after success, failure, timeout or SIGTERM |
| `--max-recovery-attempts` | `2` | failed recovery Job pods that end the drill early |
| `--poll-interval` | `10s` | how often to look at the drill `Cluster` |
| `--pg-host`, `--pg-port` | `<drill>-rw.<drill-namespace>.svc`, `5432` | where the checks connect; the certificate is verified for the Service name either way. From outside the cluster, point them at a `kubectl port-forward svc/<drill>-rw` started once the drill `Cluster` exists |
| `--no-record` | off | do not write the result `ConfigMap` |
| `--kubeconfig`, `--context` | in a pod its ServiceAccount, else as `verify` | |
| `--output` | `text` | `text` or `json` (the full report: steps, databases, recovery point) |

In Go: `drill.Run(ctx, drill.Deps{Kube: …}, drill.Options{…})`, with
`drill.KubeClient` and `drill.PGProber` as the real Kubernetes and SQL sides.

## drill-metrics

```sh
cnpgctl drill-metrics --namespace cnpg-drills --source shop/shop-pg --source ledger/ledger-pg=ledger-drill
```

Serves `/metrics` (and `/healthz`) on `--listen` (`:8080`): the result
`ConfigMap` of every `--source <namespace>/<cluster>[=<drill name>]`, read by
name from `--namespace` at each scrape, as the `cnpg_restore_drill_*` gauges
listed in [restore-drill.md](restore-drill.md#metrics). It holds no state; a
read error is a 500, so a broken exporter shows as a failed scrape rather than
as stale numbers.
