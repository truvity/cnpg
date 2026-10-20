# Restore drill

A backup nobody has restored is a hope. A restore drill proves, on a schedule,
that each cluster's archive (barman-cloud plugin `ObjectStore` on S3: base
backups and WAL) actually turns back into the database it came from.

Two pieces ship for it:

- **`cnpgctl drill`** ([cnpgctl.md](cnpgctl.md#drill)): one drill, from a
  terminal or a Job. It is the only command in this repository that writes to
  a cluster on purpose.
- **`charts/cnpg-restore-drill`**: one CronJob per source cluster that runs it
  monthly, the RBAC it needs, an exporter that turns the recorded results
  into metrics, and optional NetworkPolicies.

## How a drill runs

1. **Plan (read-only).** Read the source `Cluster`: its barman-cloud plugin
   entry (the `ObjectStore` it archives to and the server name, generation
   suffix included), that `ObjectStore`'s `destinationPath` and endpoint, its
   image (`status.image`), the size of its volumes (the larger of the spec and
   the volume claims' actual capacity), its node selector and tolerations, and
   the databases it should hold (its `Database` objects and its bootstrap
   database). Every one of these can be given explicitly instead.
2. **One drill at a time.** If a drill `Cluster` of that name exists and the
   drill created it (label `app.kubernetes.io/managed-by: cnpgctl-drill`), it
   is a drill in progress while it is younger than the timeout (the run stops,
   exit 3, nothing recorded) and stale after that (it is deleted first). An
   object of that name the drill did not create is never touched: the run
   fails.
3. **Build and check.** Build the drill `ObjectStore` (`<drill>-source`) and
   `Cluster` (`<drill>`, by default `<cluster>-restore`), then refuse to go on
   unless they cannot archive (see [Never writing to the archive](#never-writing-to-the-archive)).
4. **Create and wait.** Create both, and poll the `Cluster` until it is
   healthy with a ready, known primary. The recovery Job (`<drill>-1-full-recovery`)
   restores the latest base backup and replays every archived WAL segment
   (no recovery target: the end of the archive), then PostgreSQL promotes.
   While it runs, its PostgreSQL log is followed for the commit time of the
   last replayed transaction. Two failed recovery pods, or the operator
   declaring the cluster unrecoverable, end the wait early; so does the
   timeout. A failure prints the `Cluster`'s phase and conditions, every drill
   pod's containers, the drill's recent events and the last log lines.
5. **Check** (see [What is checked](#what-is-checked)).
6. **Tear down, always.** Delete the `Cluster` and the `ObjectStore` and wait
   until the `Cluster`, its pods and its volume claims are gone; the operator's
   children (Services, Secrets, the ServiceAccount it created, the plugin's
   Role) go with the `Cluster`. The teardown runs on a context of its own, after
   success, failure, timeout or a SIGTERM (the chart gives the pod 11 minutes
   of grace for it).
7. **Record** the result in the `ConfigMap` `<drill>-result` in the drill
   namespace (see [Metrics](#metrics)).

## Identity

**The drill reads the archive only with the read-only restore identity, by
naming the drill `Cluster` after the restore ServiceAccount.**
`cnpg-backup-access` binds the role `<rolePrefix>-<cluster>-restore` (only
`s3:GetObject` under the cluster's prefix and `s3:ListBucket`; no put, no
delete) to the ServiceAccount `<cluster>-restore` in `restore.namespace`
through an EKS Pod Identity association. The drill `Cluster` is called
`<cluster>-restore` and created in that namespace, so every pod that touches
the archive runs as that ServiceAccount.

Verified against the versions this repository pins and tests with (operator
chart 0.29.0, CloudNativePG 1.30.0, also read in 1.30.1; plugin chart 0.7.0,
barman-cloud plugin v0.13.0, also read in v0.14.0 and v0.15.1), in their source:

- **The operator runs a Cluster's pods as `cluster.GetServiceAccountName()`**,
  which is `spec.serviceAccountName` when set and the Cluster's name otherwise.
  That holds for the instance pods and for the bootstrap Jobs, the
  `full-recovery` Job among them (`pkg/specs/jobs.go`, `pkg/specs/pods.go`).
  With `spec.serviceAccountName` unset, the operator creates the ServiceAccount
  named like the Cluster, owned by it and deleted with it, or, when one of that
  name already exists (`cnpg-backup-access` with
  `restore.createServiceAccount: true`), patches it and leaves it in place
  (`internal/controller/cluster_create.go`).
- **The plugin reads the archive from a sidecar in the recovery Job's own
  pod.** Its lifecycle hook injects the `plugin-barman-cloud` container into
  the `full-recovery` (or `snapshot-recovery`) Job of a Cluster whose recovery
  source is the plugin, and into instance pods only when the Cluster archives
  or is a replica of an archive (`internal/cnpgi/operator/lifecycle.go`). A
  sidecar shares its pod's ServiceAccount, so the base backup and every WAL
  segment are read with the Pod Identity of `<cluster>-restore`. The drill's
  promoted instance has no sidecar at all.
- **The plugin's RBAC names the Cluster, not `spec.serviceAccountName`.** It
  binds a Role that may read the referenced `ObjectStore`s to the
  ServiceAccount named exactly like the Cluster
  (`internal/cnpgi/operator/specs/role.go`, `BuildRoleBinding`: subject
  `cluster.Name`, in all three versions). So `spec.serviceAccountName` cannot
  point a differently named drill at the restore ServiceAccount: the sidecar
  would run as it but could not read its `ObjectStore`. Naming the Cluster
  after the ServiceAccount is the one shape where both agree, which is why the
  drill sets no `serviceAccountName`.

Consequences:

- **One restore identity per cluster, two users of it.** EKS allows one Pod
  Identity association per (cluster, namespace, ServiceAccount), and
  `cnpg-backup-access` makes exactly one restore association, in
  `restore.namespace`. Two things run as `<cluster>-restore` there: this
  drill (its `Cluster` and recovery Job) and `cnpg-cluster`'s empty-bootstrap
  guard (`guards.emptyBootstrap`, a pre-sync hook `Job` that lists the archive
  dir; [cnpgctl.md](cnpgctl.md#guard-empty-bootstrap)). Both must therefore
  run in that one namespace: install `cnpg-restore-drill` in
  `restore.namespace`, and when that is not the cluster's own namespace set
  `guards.emptyBootstrap.namespace` to it as well. `restore.serviceAccount`
  is the drill name (default `<cluster>-restore` on both sides).
- **The ServiceAccount may already exist, and usually does.** The guard needs
  it before every sync, so `restore.createServiceAccount: true` renders it.
  The operator then uses that ServiceAccount for the drill `Cluster` instead of
  creating one (it may add its own pull secret and the Cluster's inherited
  metadata to it) and leaves it in place when the drill is deleted. Without
  it, the operator creates `<cluster>-restore` for the drill and deletes it
  with the drill.
- The drill name is at most 50 characters (CloudNativePG's limit on a Cluster
  name), so a source cluster name is at most 42 with the default suffix.
- The runner (the CronJob's pod) never holds the restore identity: it has a
  ServiceAccount of its own, `<drill>-runner`, and only talks to the
  Kubernetes API and, for the checks, to the drill instance. The chart refuses
  a drill name equal to any runner's or the exporter's ServiceAccount.
- EKS injects Pod Identity credentials when a pod is admitted. The restore
  association exists long before the drill's pods (it is made with the
  archive's), so the drill does not race it.

## Never writing to the archive

Three layers, the first two the drill's own:

1. **The hard check** (`drill.AssertNoArchive`), run on the objects exactly as
   they are about to be created, before anything is: the `Cluster` has no
   `backup` section, no `plugins` entry (an entry is how the barman-cloud
   plugin's WAL archiving is turned on), is not a replica cluster, bootstraps
   by recovery and nothing else, and has exactly one external cluster, the
   drill's own `ObjectStore` through the plugin; the `ObjectStore` has no
   `retentionPolicy` (the plugin applies retention by deleting backups) and
   reads with the pod's ambient identity (`s3Credentials: {inheritFromIAMRole:
   true}`, nothing else). A test makes each of those changes and proves the
   check refuses it and nothing is created.
2. **What is built.** The drill `ObjectStore` carries the archive's location
   only: no upload settings, tags, compression or encryption, no credentials.
   Static credentials in the sidecar's environment are refused
   (`--allow-static-credentials` exists for S3-compatible test endpoints, and
   the chart cannot set it).
3. **The identity.** The restore role cannot put, tag or delete, and the
   bucket policy admits it to read only (see
   [backupstore.md](backupstore.md)). The drill does not rely on this layer.

Nor does it write to the source `Cluster`: in the source namespace the runner
may only `get` the source `Cluster` (by name), `get` `ObjectStore`s and `list`
`Database` objects and volume claims. In the drill namespace it can create
`Cluster`s, `ObjectStore`s and `ConfigMap`s (Kubernetes cannot narrow `create`
by name) but read, delete or update only its own drill's names, so even a drill
run in the source's own namespace cannot delete the source.

## What is checked

After the drill `Cluster` is healthy, over TLS verified against the drill's
own CA (`<drill>-ca`) as its superuser (`<drill>-superuser`; superuser access
is enabled for the drill only), with every session read-only:

| check | passes when |
|---|---|
| promoted | `pg_is_in_recovery()` is false |
| databases | every expected database exists. Expected: `--database`, else the source's `Database` objects (not `ensure: absent`) and its bootstrap database, else every database but `postgres` (at least one) |
| database `<db>` | at least one user table (plain tables and partitions outside the system schemas, not owned by an extension) and at least `--min-rows <db>=<n>` rows over them (default 1) |
| min rows `<db>/<schema>.<table>` | that table exists and holds at least `n` rows |
| recovery point | its age is at most `--max-recovery-point-age` (default 24h) |

`--count exact` (the default) counts every row of every user table: a full
read of the data, so every page is proven readable (and checksummed, where the
source has data checksums). `--count estimate` sums `pg_class.reltuples`
instead, for a database too large to read monthly; the estimate is the
source's own statistics, so it proves the catalog came back, not the rows.

### The recovery point and its age

The recovery point is the later of:

- the commit time of the last transaction the recovery replayed, from the
  line PostgreSQL logs when recovery ends (`last completed transaction was at
  log time …`), read from the recovery Job's log while it runs; and
- the start of the base backup that was restored (`START TIME` in
  `backup_label.old`, which PostgreSQL leaves in the data directory).

Its age is the time of the check minus that point, reported as
`cnpg_restore_drill_recovery_point_age_seconds`.

**The trade-off.** A drill cannot see the source's last commit, only the last
one that reached the archive. For a busy database those are close:
CloudNativePG sets `archive_timeout` to 5 minutes, so a segment with any WAL in
it is switched and archived at least that often, and the last replayed commit
is minutes old. A quiet database may have committed nothing for days: then no
transaction is replayed after the base backup, the log line is absent, and the
base backup's start is the recovery point. With daily base backups at 02:00
UTC and the drill's default schedule between 04:00 and 23:59 UTC, that is 2 to
22 hours, inside the default 24 hours. An age above it therefore means the
archive stopped (no WAL and no base backup for a day), not a quiet database.
Raise `maxRecoveryPointAgeHours` for a cluster whose base backups are less
frequent than daily.

## Metrics

**The choice: the result is recorded on a Kubernetes object, a ConfigMap per
drill, and a small exporter in the chart serves it.** The options were:

- **kube-state-metrics custom-resource-state on a ConfigMap or an
  annotation.** Not possible for a ConfigMap: custom-resource-state resolves
  only kinds served by a CustomResourceDefinition and refuses a group-less
  (core) kind ("group is required"), as of kube-state-metrics v2.20.0
  (`internal/discovery/discovery.go`). Annotating a custom resource the
  estate already exports, such as the source `Cluster`, would give the runner
  `patch` on production `Cluster`s, which RBAC cannot narrow to annotations.
- **Pushing to a vmagent import endpoint.** Each run would be one sample at
  one instant: "no success in 35 days" becomes a 35-day range query, a failed
  run must find the previous success time somewhere to push it again, a push
  lost to an unreachable endpoint is a month without data, and the drill
  namespace needs egress to the metrics pipeline.
- **A ConfigMap plus the chart's exporter** (chosen): the state survives
  between monthly runs in the cluster, is readable with `kubectl`, keeps the
  last success across failures, and is exported continuously as ordinary
  gauges, so the alerts are instant queries. The exporter is
  `cnpgctl drill-metrics`, from the same image: one small pod that reads the
  ConfigMaps by name at each scrape (RBAC `get` on those names only) and holds
  no state. The chart renders a `PodMonitor` for it with `honorLabels: true`.

The `ConfigMap` `<drill>-result` (labels `cnpg-restore-drill/result: "true"`,
`cnpg-restore-drill/source-namespace`, `cnpg-restore-drill/source-cluster`)
holds `last_run_timestamp_seconds`, `last_result`,
`last_success_timestamp_seconds` (kept from an earlier run when this one
failed; `0` when none succeeded), `last_duration_seconds`,
`recovery_point_age_seconds` (absent when not measured), `last_failure` and the
full JSON report.

Every series carries `namespace` and `cluster`, the **source** cluster's (the
`PodMonitor`'s `honorLabels: true` keeps them over the exporter pod's own
`namespace`):

| metric | meaning |
|---|---|
| `cnpg_restore_drill_configured` | `1` for every source the exporter is configured with, drilled yet or not |
| `cnpg_restore_drill_last_run_timestamp_seconds` | Unix time the last drill finished |
| `cnpg_restore_drill_last_success_timestamp_seconds` | Unix time the last successful drill finished; `0` when none succeeded |
| `cnpg_restore_drill_last_result` | `1` when the last drill succeeded, `0` when it failed |
| `cnpg_restore_drill_last_duration_seconds` | duration of the last drill, teardown included |
| `cnpg_restore_drill_recovery_point_age_seconds` | age of the recovered data at the check (above); absent when it could not be measured |

A source with no drill yet has only `cnpg_restore_drill_configured`.

### Alert expressions

The rules belong in the platform's alert chart; these are the expressions, and
[runbooks.md](runbooks.md#cnpgrestoredrillfailed) has what to do when each
fires (`CnpgRestoreDrillFailed`, `CnpgRestoreDrillOverdue`; link them as the
rules' `runbook_url`):

```promql
# The last restore drill failed.
cnpg_restore_drill_last_result == 0

# No successful restore drill in 35 days (also fires when one ran and none
# ever succeeded: the success time is then 0).
time() - cnpg_restore_drill_last_success_timestamp_seconds > 35 * 86400
```

A cluster listed but never drilled has no success series, so the second
expression cannot see it. Run its first drill by hand when you list it
(`kubectl -n <drill namespace> create job --from=cronjob/<drill> <drill>-first`),
or add, with a `for` of a day or two:

```promql
cnpg_restore_drill_configured unless on (namespace, cluster) cnpg_restore_drill_last_run_timestamp_seconds
```

A broken exporter shows as a failed scrape (its `/metrics` answers 500 when it
cannot read the ConfigMaps), which the platform's target-down alerting covers.

## The chart

`oci://ghcr.io/truvity/charts/cnpg-restore-drill`, installed once per drill
namespace (the release namespace). Values reference:
[reference.md](reference.md#chartscnpg-restore-drill).

```yaml
defaults:
  storageClass: example-delete      # required: a class that DELETES its volumes
  instance:
    podAnnotations:
      example.com/do-not-disrupt: "true"
drills:
  - namespace: shop                 # the source Cluster
    cluster: shop-pg
  - namespace: ledger
    cluster: ledger-pg
    archive:                        # explicit; otherwise read from the source
      bucket: example-ledger-backups
      project: ledger
      generation: g2                # serverName ledger-pg-g2
    checks:
      minRows:
        ledger: 1000
        ledger/public.entries: 100
      maxRecoveryPointAgeHours: 48
```

Per drill it renders a CronJob named like the drill `Cluster`, a ServiceAccount
`<drill>-runner`, a Role and RoleBinding in the drill namespace and a read-only
Role and RoleBinding (`<drill>-runner-source`) in the source namespace; once,
the exporter Deployment with its ServiceAccount, Role and RoleBinding, and a
`PodMonitor` where the API is served.

- **Schedule.** Monthly. Unless `schedule` is set, the day (1 to 28, which
  every month has), hour (04 to 23 UTC) and minute come from a checksum of
  `<namespace>/<cluster>`, so drills spread out, a render is stable, and none
  runs inside 01:00 to 04:00 UTC, around the 02:00 base backups. An explicit
  schedule whose hour is 1, 2 or 3 is refused. The CronJob has `timeZone:
  Etc/UTC`, `concurrencyPolicy: Forbid`, a `startingDeadlineSeconds` of an
  hour, history limits 1 and 3, and its Job `backoffLimit: 0` and
  `activeDeadlineSeconds` of the drill's timeout plus 30 minutes.
- **Sizing.** The drill gets one instance with 250m CPU and 512Mi memory
  requested, 1Gi memory limit, and volumes as large as the source's (the WAL
  volume too, when the source has one); `instance.*`, `storageSize` and
  `walStorageSize` override them. Plan on one source-sized volume for an hour
  or two per drill, and on the recovery reading the whole latest base backup
  and every WAL segment since.
- **Scheduling.** The drill's pods get the source's node selector and
  tolerations, so they land where the source's instances can; set
  `instance.nodeSelector` or `instance.tolerations` to place them elsewhere.
  Give them the annotation your node autoscaler honours as "do not disrupt"
  (`instance.podAnnotations`), or a consolidation can end a drill.
- **Refusals** (`templates/_validate.tpl`, `values.schema.json`; one fixture
  each under `tests/invalid/cnpg-restore-drill/`): no storage class; a drill
  name over 50 characters, used twice, equal to the source, or equal to a
  runner's or the exporter's ServiceAccount; a source listed twice; a schedule
  without five fields or inside the backup window; `archive.prefix` without
  `archive.bucket` or not shaped `<namespace>/<cluster>/`; `serverName` with
  `generation`, or `generation` and `project` apart; NetworkPolicies on
  without the operator's peers; an unknown key.

### Network

`networkPolicy.enabled: true` renders, for a namespace that denies ingress by
default: ingress to the drill instances (`cnpg.io/podRole: instance` of the
drill `Cluster`s) on 5432 from the runners and on 8000 from
`networkPolicy.operator` (the CloudNativePG operator reaches each instance's
status endpoint there; selecting the instances for ingress closes it
otherwise, so the peers are required), and, when `networkPolicy.metrics` is
set, ingress to the exporter on 8080 from the scraper. Egress is not rendered.
Where egress is denied by default, allow:

| from | to |
|---|---|
| the drill's recovery Job pod (`cnpg.io/jobRole: full-recovery`) | S3 (443) and the Pod Identity agent (EKS: link-local 169.254.170.23, port 80) |
| the drill's pods | DNS; the Kubernetes API (the instance manager) |
| the runners | DNS; the Kubernetes API; the drill instances on 5432 |
| the exporter | DNS; the Kubernetes API |

## Wiring it into a platform

1. **Namespace.** Pick the drill namespace (one per Kubernetes cluster is
   enough) and install the chart there, with `defaults.storageClass` set to a
   class whose reclaim policy is `Delete`.
2. **Restore identity.** In each cluster's `cnpg-backup-access` entry set
   `restore.namespace` to the drill namespace (`restore.serviceAccount` stays
   `<cluster>-restore` unless the drill entry sets `drillName`), keep
   `restore.createServiceAccount: true` where the empty-bootstrap guard is on,
   and set that cluster's `guards.emptyBootstrap.namespace` to the drill
   namespace too: the guard and the drill share the one restore identity
   ([Identity](#identity)).
3. **Image.** The chart runs `ghcr.io/truvity/cnpg/cnpgctl` at its own
   `appVersion`, the image of the same release (the one the guard runs); pin
   the chart version and the image comes with it. `image.digest` pins it
   further.
4. **Network.** Turn `networkPolicy` on with the operator's peers if the
   namespace denies ingress, and allow the egress above if it denies egress.
5. **Monitoring.** With the `PodMonitor` API served, the exporter is scraped
   with `honorLabels`. Otherwise scrape port `metrics` of the pods labelled
   `app.kubernetes.io/name: cnpg-restore-drill, app.kubernetes.io/component:
   exporter` and keep their `namespace` label. Add the two alert expressions
   above to the platform's alert rules. No kube-state-metrics configuration is
   needed.
6. **First run.** Run each new drill once by hand (`kubectl create job
   --from=cronjob/<drill>`) and read its log: it says what it planned, what it
   found, and, on failure, why.

## What a drill proves, and what it does not

It proves that, at the time of the drill:

- the archive's latest base backup and every WAL segment after it can be read
  with the restore identity and restored by the PostgreSQL image the source
  runs;
- the result is a consistent, promoted database that holds the expected
  databases with tables and rows, every row of which was read (exact mode);
- the archive reaches within the measured age of the drill's start.

It does not prove:

- **older restore points.** Only the latest base backup and the WAL after it
  are used; an older base backup or a point-in-time target is not exercised
  (`cnpg-cluster`'s `bootstrap.recovery.target` is the tool for that).
- **that the data is right.** Row minimums catch an empty or truncated
  restore, not a wrong one; compare application-level invariants yourself if
  you need that.
- **the source's last commits.** Whatever had not reached the archive when
  the drill started is not in it; the recovery point age measures that gap
  only up to the trade-off above.
- **the time to recover a production-sized instance.** The drill runs one
  small instance; its duration is a lower bound, not a recovery time
  objective.
- **a restore in another account or region.** The drill reads the primary
  bucket from the cluster's own account; a cross-region replica bucket is not
  read.
- **the application.** Nothing connects to the drill but the checks.

## Troubleshooting

- **Exit 3, "already running".** A drill `Cluster` of that name exists and is
  younger than the timeout. Wait, or delete it if you know it is dead
  (`kubectl -n <drill namespace> delete cluster.postgresql.cnpg.io/<drill>
  objectstore.barmancloud.cnpg.io/<drill>-source`); the next run deletes it
  anyway once it is older than the timeout.
- **"refusing to touch it".** Something the drill did not create holds the
  drill's name. Rename the drill (`drillName`, and `restore.serviceAccount` to
  match) or move the other object.
- **Recovery fails at once.** Read the `plugin-barman-cloud` lines in the
  diagnostics: an access error means the association or the role
  (`restore.namespace` must be the drill namespace); "no backup found" means
  the destination path or the server name (a generation suffix) is wrong.
- **Volumes left behind.** The storage class retains them; delete the
  released volumes and use a class that deletes them.
