# Runbooks

What to do when a database built from these charts loses its cluster, takes in
bad data, or raises one of the alerts that link here. Every command is
generic: `<namespace>`, `<cluster>`, `<bucket>` and the rest are yours to fill
in. `kubectl cnpg` is the CloudNativePG kubectl plugin.

Names used below:

| Name | What | Where to read it |
|---|---|---|
| archive prefix | `<namespace>/<cluster>` by default (`backup.s3Prefix`) | the `ObjectStore` `<cluster>-objectstore`, `spec.configuration.destinationPath` (`s3://<bucket>/<prefix>`) |
| serverName | the barman server directory under the prefix; one per generation (`<project>-pg-<generation>` with the per-project preset, `backup.serverName` otherwise) | the `Cluster`, `spec.plugins[].parameters.serverName` |
| archive dir | `s3://<bucket>/<prefix>/<serverName>/`, holding `base/` (base backups) and `wals/` | |

Two rules hold throughout:

- **One writer per archive dir.** A cluster that starts anew (an `initdb`, a
  recovery, a point-in-time copy) never archives into a dir another cluster
  wrote: it gets a new serverName generation. barman refuses a non-empty dir
  for a new cluster (`check-wal-archive`), and two clusters in one dir corrupt
  each other's timeline history.
- **Never `initdb` onto the old archive dir.** That is an empty database
  serving while the data sits in the archive. The `cnpg-cluster` chart's
  empty-bootstrap guard refuses it (see
  [below](#the-empty-bootstrap-guard-refused-a-sync)).

## Latest-point recovery after losing a cluster

When the `Cluster` object, its PVCs or both are gone (an accidental prune, a
bad deploy, a deleted namespace) and the archive is intact.

1. **Keep GitOps from creating an empty cluster.** With the empty-bootstrap
   guard on (the default with `backup.mode: perProjectBucket`), the sync that
   would re-create the `Cluster` with `initdb` fails at the
   `<cluster>-bootstrap-guard` hook. Without it, pause the application's
   automated sync now, before it re-creates the cluster.

2. **Find the archive dir and what it holds.** The serverName is the one the
   lost cluster archived to (from git: the values of the last good revision;
   the alert or the guard's refusal names it too).

   ```sh
   aws s3 ls s3://<bucket>/<prefix>/<serverName>/base/   # one directory per base backup
   aws s3 ls s3://<bucket>/<prefix>/<serverName>/wals/ --recursive | tail -3
   ```

   The newest WAL segment is how far the recovery can go.

3. **Change the values: recovery from that dir, archiving into a new
   generation.** With the per-project preset:

   ```yaml
   bootstrap:
     mode: recovery
     recovery:
       source:
         bucketName: <bucket>
         s3Prefix: <namespace>/<cluster>       # the lost cluster's prefix
         serverName: <project>-pg-g4            # the dir it archived to
   backup:
     enabled: true
     mode: perProjectBucket
     bucket: {prefix: <prefix>, project: <project>, env: <env>, region: <region>}
     generation: g5                             # NEW: the recovered cluster's own dir
   ```

   Without the preset, set `backup.serverName` to a new `<cluster>-gN`. No
   `recovery.target`: the recovery replays to the end of the archived WAL. Keep
   the cluster's name and namespace when you can: the Services
   (`<cluster>-rw`, `-ro`, `-r`), the client CA and the certificates keep their
   names, so clients need no change, and the cluster's own identity (the
   archive role) can already read its own prefix. The recovery reads the
   source through `<cluster>-source-objectstore` with that same identity.

4. **Sync, and watch it come up.**

   ```sh
   kubectl -n <namespace> get cluster.postgresql.cnpg.io <cluster> -w
   kubectl -n <namespace> get pods -l cnpg.io/cluster=<cluster>
   kubectl -n <namespace> logs -f job/<cluster>-1-full-recovery   # the recovery job
   ```

5. **Verify before anyone writes.**

   ```sh
   cnpgctl verify --namespace <namespace> --cluster <cluster> --check-identity
   kubectl cnpg psql <cluster> -n <namespace> -- -Atc "select now(), pg_is_in_recovery()"
   ```

   Then the application's own check: the newest row of a table that is
   written all the time (`select max(created_at) from <table>`) should be
   close to when the cluster was lost. `ContinuousArchiving` must turn `True`,
   and the new dir needs a base backup: the preset's on-demand `Backup` (named
   for the generation) takes it; otherwise
   `kubectl cnpg backup <cluster> -n <namespace> -m plugin --plugin-name barman-cloud.cloudnative-pg.io`.

6. **Switch clients.** Same name: they reconnect on their own once the
   `-rw` Service has an endpoint again. A new name: point the connection strings at
   `<new-cluster>-rw` and roll the clients.

7. **Afterwards.** `spec.bootstrap` is read only when a cluster is created,
   and the operator accepts a change to it later. Set `bootstrap.mode` back to
   `initdb`, so that a future loss is caught by the guard instead of
   recovering silently from a dir that is no longer the newest.

What NOT to do:

- Do not re-create the cluster with `initdb` and the old serverName, or turn
  the guard off to get past it.
- Do not give the recovered cluster the serverName it recovers from (two
  writers, one dir), and do not run the old and the recovered cluster side by
  side on the same dir.
- Do not delete the old dir: the bucket lifecycle expires it, and until then
  it is the fallback.

## Point-in-time recovery after bad data

When the cluster is healthy but its data is wrong from a known moment on (a
bad migration, a mass delete). Recover a **side cluster** to just before that
moment, compare, then decide. Restoring over the live database is never the
first step and stays a manual decision.

1. **Pick the target time.** The moment just before the bad change, from the
   application's logs or `pg_stat_statements`. With `exclusive: true` the
   recovery stops just before the transactions that committed exactly at that
   time; with `false` (the default) it includes them.

2. **Create the side cluster** with its own name, from the live cluster's
   archive dir:

   ```yaml
   clusterName: <cluster>-pitr
   namespace: <namespace>
   profile: devel                               # one instance is enough to compare
   bootstrap:
     mode: recovery
     recovery:
       source:
         bucketName: <bucket>
         s3Prefix: <namespace>/<cluster>
         serverName: <serverName of the live cluster>
       target:
         targetTime: "2026-01-31T09:59:00Z"     # RFC 3339, or PostgreSQL's timestamp format
         exclusive: true
   backup:
     enabled: false                             # the side cluster archives nowhere
     expected: false
     notExpectedReason: "point-in-time copy for comparison, deleted afterwards"
   ```

   The side cluster's pods run as the ServiceAccount named like it
   (`<cluster>-pitr`), and that identity must be able to read the live
   cluster's prefix (`s3:GetObject` on it and `s3:ListBucket`). With
   `cnpg-backup-access`, the cluster's restore role is the read-only identity
   for exactly that prefix: bind it to the side cluster for the duration
   (`clusters[].restore.serviceAccount: <cluster>-pitr`, and
   `restore.namespace` if the side cluster runs elsewhere). The base backup it
   starts from must be older than the target time.

3. **Compare.** Query the side cluster and the live one for the rows the bad
   change touched:

   ```sh
   kubectl cnpg psql <cluster>-pitr -n <namespace> -- -c "select count(*) from <table> where <predicate>"
   kubectl cnpg psql <cluster> -n <namespace> -- -c "select count(*) from <table> where <predicate>"
   ```

4. **Decide.**
   - **Copy rows back** (the usual case: a few tables, writes kept since):
     dump the affected rows from the side cluster and load them into the live
     one in a transaction, for example
     `pg_dump --data-only --table=<table>` from `<cluster>-pitr` into a
     staging table on the live cluster, then an `INSERT … ON CONFLICT` or
     `UPDATE … FROM` the application's owners agree on.
   - **Switch to the side cluster** (only when everything since the target
     time may be lost): stop the writers, give the side cluster archiving of
     its own (a backup prefix it is admitted to, a new generation), move the
     clients to `<cluster>-pitr-rw`, and retire the old cluster afterwards.
     This is a decision for the data's owners, not an on-call step.

5. **Clean up.** Delete the side cluster's values (its `Cluster` and PVCs go
   with it) and move the restore binding back.

## A phase stuck after a primary change

Upstream CloudNativePG issue
[#11627](https://github.com/cloudnative-pg/cloudnative-pg/issues/11627): after
a switchover or failover (a node rotation, say) a configuration reload is lost,
and the `Cluster`'s `status.phase` stays at "Switchover in progress" or
"Waiting for the instances to become active" while every instance is ready and
serving. The `Cluster` never reports healthy, which blocks every Argo CD sync of
the application that owns it (the sync waits for the `Cluster` to become
healthy).

1. Confirm the signature: every instance ready, a primary known.

   ```sh
   kubectl -n <namespace> get cluster.postgresql.cnpg.io <cluster> -o jsonpath='{.status.phase}{"\n"}{.status.readyInstances}/{.spec.instances}{"\n"}{.status.currentPrimary}{"\n"}'
   kubectl -n <namespace> get pods -l cnpg.io/cluster=<cluster> -L cnpg.io/instanceRole
   ```

2. Reload the configuration on **each** instance (`<cluster>-1`, `-2`, …):

   ```sh
   kubectl -n <namespace> exec <cluster>-<n> -c postgres -- psql -U postgres -Atc "select pg_reload_conf()"
   ```

   No restart is needed, and restarting the operator or the pods does not
   help. The phase returns to "Cluster in healthy state" within a minute or
   so, and the blocked sync proceeds.

## The empty-bootstrap guard refused a sync

The `<cluster>-bootstrap-guard` hook `Job` failed with `REFUSED: Cluster … does
not exist, its bootstrap is initdb, and its archive … already holds …`
(`kubectl -n <namespace> logs job/<cluster>-bootstrap-guard`). Nothing of the
sync was applied. The `cnpg-cluster` chart's `guards.emptyBootstrap` runs it
before every sync ([reference.md](reference.md#guards-pre-sync-checks),
[cnpgctl.md](cnpgctl.md#guard-empty-bootstrap)).

Why is the `Cluster` missing?

- **It was lost** (deleted, pruned, its namespace removed): recover it,
  [latest-point recovery](#latest-point-recovery-after-losing-a-cluster),
  from the dir the refusal names.
- **A new, empty database is intended** (a deliberate re-create): bump the
  serverName generation (`backup.generation`, or `backup.serverName`). The new
  dir is empty and the guard passes; the old one stays readable until the
  lifecycle expires it.
- **Neither fits** (an archive dir prepared by hand for a test, say): set
  `guards.emptyBootstrap.allowNonEmptyArchive: true` with a `reason`. No guard
  runs and the `Cluster` carries the reason as
  `cnpg-cluster/empty-bootstrap-override`. Remove both once the cluster
  exists.

When the `Job` failed without a verdict (exit 2, or no pod at all):

| Symptom | Cause | Fix |
|---|---|---|
| no pod, `Job` failed with `DeadlineExceeded`, events `serviceaccount "<cluster>-restore" not found` | the restore ServiceAccount does not exist | render it (`cnpg-backup-access` `restore.createServiceAccount: true`), or point `guards.emptyBootstrap.serviceAccount` / `.namespace` at one that exists |
| `read Cluster …: … forbidden` | the hook `Role`/`RoleBinding` is missing or binds another ServiceAccount | the chart renders both as hooks; check `guards.emptyBootstrap.namespace` matches the ServiceAccount's |
| `list s3://…: … AccessDenied`, or no credentials | the ServiceAccount's identity cannot list the bucket (no Pod Identity association yet, or the wrong role) | bind the restore role to it; a new association is seen only by pods created after it, and the next sync makes a new pod |
| `… PermanentRedirect` / wrong region | the bucket is in another region | set `guards.emptyBootstrap.region` |

## Alerts

### CNPGClusterPhaseStuck

The phase is not healthy while every instance is ready: see
[a phase stuck after a primary change](#a-phase-stuck-after-a-primary-change).

### CNPGBackupFailed

**Meaning.** The cluster's newest `Backup` is in phase `failed` (or
`walArchivingFailing`) and none has completed since. The recovery window stops
moving forward at the last good base backup; as long as WAL archiving works,
point-in-time recovery still reaches the present.

**First checks.**

```sh
kubectl -n <namespace> get backups.postgresql.cnpg.io --sort-by=.metadata.creationTimestamp
kubectl -n <namespace> get backups.postgresql.cnpg.io <backup> -o jsonpath='{.status.error}{"\n"}'
kubectl -n <namespace> logs <instance-pod> -c plugin-barman-cloud --since=2h
cnpgctl verify --namespace <namespace> --cluster <cluster> --check-identity
```

**Fix by cause.**

- `walArchivingFailing`: archiving is broken, which comes first (the
  `CnpgWalArchivingFailing` alert, usually credentials or the bucket).
- Access denied on upload: the instance pods lack their Pod Identity
  credentials (`--check-identity` fails) or the role lost the prefix. Recreate
  the pods (replicas first, the primary by switchover).
- Access denied on delete: a `retentionPolicy` against a bucket whose writer
  roles cannot delete; set `backup.retentionDays: 0` (the per-project preset
  does).
- Then take a new backup and watch it complete:
  `kubectl cnpg backup <cluster> -n <namespace> -m plugin --plugin-name barman-cloud.cloudnative-pg.io`.
  The alert clears when a `Backup` newer than the failed one completes.

### CNPGBackupStuck

**Meaning.** The cluster's newest unfinished `Backup` (no phase yet, `pending`,
`started`, `running` or `finalizing`) was created more than two hours ago. A
hung backup holds the ones after it.

**First checks.** The `Backup`'s phase and the `Cluster`'s health; a `Backup`
with no phase was never picked up.

```sh
kubectl -n <namespace> get backups.postgresql.cnpg.io --sort-by=.metadata.creationTimestamp
kubectl -n <namespace> get cluster.postgresql.cnpg.io <cluster>
kubectl -n <namespace> logs <instance-pod> -c plugin-barman-cloud --since=3h
```

**Fix.**

- The `Backup` names a `Cluster` that no longer exists (a leftover on-demand
  `Backup`): delete the `Backup` object.
- The `Cluster` is not healthy: that comes first; the backup runs once it is.
- The sidecar shows no backup process while the `Backup` says `running`:
  delete the `Backup` and take a new one. Otherwise let it finish: a large
  first base backup can take hours, in which case raise
  `groups.cnpg.backupStuckSeconds` for good.

### CNPGInstanceOnDisruptedNode

**Meaning.** A database instance pod runs on a node Karpenter is draining (the
node carries the `karpenter.sh/disrupted` taint). The instance's
PodDisruptionBudget (and, on a prod cluster, `karpenter.sh/do-not-disrupt`)
holds the drain, so the node sits half-drained until the node pool's
termination grace period evicts the pod by force, a primary included.

**First checks.** Which instance it is and what it is:

```sh
kubectl -n <namespace> get pods -l cnpg.io/cluster=<cluster> -L cnpg.io/instanceRole -o wide
kubectl get node <node> -o jsonpath='{.spec.taints}{"\n"}'
kubectl get nodeclaims -o wide | grep <node>
```

**Fix.** Move it on your terms:

- A replica: delete its pod. The operator recreates it on another node of the
  same zone (its volume is zonal), and the drain goes on.
- The primary: switch over to a ready replica first,
  `kubectl cnpg promote <cluster> <replica-instance> -n <namespace>`, then the
  old primary is a replica: delete its pod as above.

### CnpgManualSwitchoverRequired

**Meaning.** The primary needs a restart (an image update, a changed
parameter) and the cluster's `primaryUpdateStrategy` is `supervised`: the
operator restarted the replicas and waits for a person to switch over. The
`cnpg-cluster` chart sets `unsupervised`, so this cluster was changed by hand
or comes from elsewhere.

**Fix.** Switch over to an updated replica:
`kubectl cnpg promote <cluster> <replica-instance> -n <namespace>`. The
operator then updates the former primary. To make it automatic, set
`primaryUpdateStrategy: unsupervised`.

### CnpgInstanceFenced

**Meaning.** An instance is fenced: the `Cluster`'s `cnpg.io/fencedInstances`
annotation names it (or `"*"`, every instance). PostgreSQL is stopped on it;
the pod runs so that its data can be inspected. Fencing is deliberate and
temporary; this alert is the reminder that one is still in place.

**First checks.** Who fenced it and why (the change log, the incident it was
for):

```sh
kubectl -n <namespace> get cluster.postgresql.cnpg.io <cluster> -o jsonpath='{.metadata.annotations.cnpg\.io/fencedInstances}{"\n"}'
```

**Fix.** When the investigation is over, lift it:
`kubectl cnpg fencing off <cluster> <instance> -n <namespace>` (or `"*"`). A
fenced primary means no writes, and the cluster's readiness alerts fire
too.

### CnpgRestoreDrillFailed

`cnpg_restore_drill_last_result == 0`: the last monthly restore drill of the
cluster in the alert's `namespace` and `cluster` labels failed
([restore-drill.md](restore-drill.md)). Nothing is wrong with the running
database because of it, but its archive is not proven restorable until a
drill passes.

**First checks.** What failed, from the result the drill recorded (in the
drill namespace, the `cnpg-restore-drill` release namespace):

```sh
kubectl -n <drill-namespace> get configmap <cluster>-restore-result -o jsonpath='{.data.last_failure}{"\n"}'
kubectl -n <drill-namespace> logs -l cnpg-restore-drill/source-cluster=<cluster> --tail=-1 --prefix
```

(`<cluster>-restore` is the default drill name; a drill entry's `drillName`
replaces it everywhere here.)

The step named there points at the cause:

- **plan**: the source `Cluster`, its `ObjectStore` or the storage class could
  not be read, or a value is wrong (the archive coordinates, a `Retain`
  storage class).
- **one drill at a time**: an object of the drill's name exists that no drill
  created; rename one of them.
- **recovery**: read the diagnostics in the Job's log. An access error from
  `plugin-barman-cloud` means the restore identity: the Pod Identity
  association must be in the drill namespace (`restore.namespace`); "no backup
  found" means the destination path or the server name (a generation suffix);
  a WAL that cannot be fetched means a gap in the archive, which is exactly
  what the drill exists to find: treat it like [CNPGBackupFailed](#cnpgbackupfailed)
  and take a fresh base backup.
- **recovery point**: the archive is older than the maximum: WAL archiving or
  the base backups stopped (check the source's `ContinuousArchiving`
  condition and its last backup, `cnpgctl verify`), or the database is quiet
  and its base backups are less frequent than the maximum allows.
- **databases**, **database `<db>`**, **min rows**: the restore came back
  without the expected data. Compare with the source before anything else.
- **teardown**: the drill's objects are still there; delete them
  (`kubectl -n <drill-namespace> delete cluster.postgresql.cnpg.io/<cluster>-restore objectstore.barmancloud.cnpg.io/<cluster>-restore-source`).

**Fix and confirm.** Fix the cause, then run the drill again rather than wait
a month: `kubectl -n <drill-namespace> create job --from=cronjob/<cluster>-restore <cluster>-restore-rerun`.
The alert resolves when that run records a success.

### CnpgRestoreDrillOverdue

`time() - cnpg_restore_drill_last_success_timestamp_seconds > 35 * 86400`: no
restore drill of the cluster has succeeded for 35 days (or none ever has: the
success time is then `0`).

**First checks.** Whether drills run at all: the CronJob
`<cluster>-restore` in the drill namespace (suspended? `kubectl get cronjob`
shows `SUSPEND` and `LAST SCHEDULE`), its Jobs (an image pull or scheduling
failure leaves no result behind), and the exporter's `last_run` for the
cluster. If drills run and fail, it is [CnpgRestoreDrillFailed](#cnpgrestoredrillfailed)
too; start there.

**Fix.** Make the CronJob run, then run a drill by hand as above.
