# Moving a cluster to a new backup bucket

A running cluster that archived to a shared or legacy bucket moves onto a
per-project bucket (`pkg/aws/backupstore`, access by `cnpg-backup-access`) with
a new `serverName` and a new `ObjectStore`. The charts have four options for
this; all are off by default (the adoption value too) and change nothing for a render that does not set
them.

| Need | Value |
|---|---|
| The plugin must not prune (the new roles cannot delete) | `backup.retentionDays: 0` renders no `retentionPolicy` |
| The bucket lifecycle must find the objects | `backup.tags` and `backup.historyTags` |
| A base backup in the new location at once | `backup.onDemand.name: <id>` |
| The archive role exists before the ServiceAccount is rebound | `cnpg-backup-access`: `clusters[].archive.association: false` |
| An association is adopted instead of created (rarely wanted) | `cnpg-backup-access`: `global.adoptAssociations: true` (default `false`) |

`cnpg-cluster` and `cnpg-projects` render the same `ObjectStore` and `Backup`
for the same inputs: `cnpg-projects` carries the `cnpg-cluster` templates
(`hack/sync-cnpg-projects.sh`), and its test renders every
`tests/cases/cnpg-cluster/` case through both charts and compares them.

## Why no `retentionPolicy`

With `retentionPolicy`, the barman-cloud plugin deletes backups and WAL older
than the window. The tenant roles of a per-project bucket have no
`s3:DeleteObject` by design (docs/backupstore.md, "Append-only"), so that
pruning would get 403 and mark backups failed. Expiry is the bucket
lifecycle's job: one age for everything tagged `cnpg-backup-retention:
managed`. `backup.retentionDays: 0` omits the field.

## Tags: the lifecycle contract

```yaml
backup:
  tags:
    cnpg-backup-retention: managed   # base backups and WAL: expire at the bucket's age
  historyTags:
    cnpg-backup-retention: keep      # timeline history files: never expire
```

They render as `ObjectStore` `spec.configuration.tags` and `.historyTags`
(barman `--tags`, `--history-tags`). The role's `s3:PutObjectTagging` is in the
archive policy. Keep the schedule away from midnight UTC (backupstore.md,
"Lifecycle").

## The on-demand backup

The `ScheduledBackup`'s `immediate: true` is read only on a `ScheduledBackup`
that has never run, so it does not fire when `serverName` changes: the new
directory then holds WAL and no base backup. `backup.onDemand.name: g2`
renders

```yaml
apiVersion: postgresql.cnpg.io/v1
kind: Backup
metadata:
  name: <clusterName>-g2
  annotations:
    argocd.argoproj.io/sync-options: Prune=false
spec:
  cluster: {name: <clusterName>}
  method: plugin
  pluginConfiguration:
    name: barman-cloud.cloudnative-pg.io
    parameters: {barmanObjectName: <clusterName>-objectstore}
```

Use a new id at each bump (`g3` at the next one): a new id is a new object and
the old `Backup` is not touched. `Prune=false` stops Argo CD deleting the old
record when the id leaves Git.

Does deleting a `Backup` delete backup data? A `Backup` object is the
Kubernetes record of a base backup. Data in S3 is removed only by the plugin's
retention sweep (`retentionPolicy`, which these roles cannot do) and by the
bucket lifecycle; with the no-delete roles of a per-project bucket a
client-side delete is refused by IAM in any case. That is the design, not
something this chart tested against a live plugin; the `Prune=false` annotation
keeps the records for the audit trail, not for the data's safety.

## The cutover order

Before: the database's ServiceAccount is bound by a legacy Pod Identity
association object to a legacy role, and the `ObjectStore` points at the legacy
bucket.

0. Take a base backup into the OLD location first: a plain one-off `Backup`
   on the old `ObjectStore`, named for the cutover. After the switch the old
   location receives no more WAL, so this is the last recent base backup there,
   and the starting point for restoring customer data from before the move.
   Then prepare the destination. Deploy the `backupstore` bucket (see
   `backupstore.NewProjectBuckets` for a bucket per project) and
   `cnpg-backup-access` with `clusters[].archive.association: false`: the
   archive and restore roles exist, and no second association is made on the
   ServiceAccount.
1. **First sync: the legacy association leaves.** Remove the legacy
   association object of the database's ServiceAccount from Git. The legacy
   role keeps any non-database ServiceAccount it serves (a rehearsal
   ServiceAccount, say). Then verify in AWS that the association is gone:

   ```
   aws eks list-pod-identity-associations --cluster-name <eks-cluster> \
     --namespace <namespace> --service-account <cluster>
   ```

   The list must be empty. Do not go on while it is not: EKS allows one
   association per (EKS cluster, namespace, ServiceAccount), so the new
   association cannot be made while the old one exists. Do not "retain and
   adopt" the legacy association either (`deletion-policy: retain` on the
   legacy object, and adoption on the new one): two objects would then own one
   association, and when the legacy object is pruned its controller deletes
   the association in AWS while the new object still reports Synced, leaving
   the database with none. The chart does not adopt associations by default
   for this reason (`global.adoptAssociations`, docs/backup-access.md).
2. **Second sync: the new association and the cluster values.** Set
   `archive.association: true` (the new association takes the ServiceAccount)
   and change the cluster values: `backup.bucketName`, a new
   `backup.serverName` (the next generation, e.g. `<project>-pg-g2`; barman
   refuses a non-empty directory for a new history), `backup.retentionDays: 0`,
   `backup.tags` and `backup.historyTags`, and a new `backup.onDemand.name`.
   Then verify in AWS that the association exists (the `aws eks
   list-pod-identity-associations` call above, now listing one). The ACK
   controller can report the `PodIdentityAssociation` object Synced while AWS
   has none; delete the object and let the GitOps tool recreate it (an
   annotation nudge does not).

   Build this change from the default branch AFTER the detach change has
   merged. A branch stacked on the unmerged detach conflicts once the detach is
   squash-merged.
3. **Recreate EVERY instance, replicas first, then the primary by
   switchover.** Pod Identity credentials are injected when a pod is created.
   A pod created while the ServiceAccount had no association gets no AWS
   environment at all, and a pod that was running before the switch keeps the
   old role's cached credentials. Either way each instance must be recreated
   after the new association exists: delete the replicas one at a time, then
   switch over to a recreated replica and delete the old primary's pod. Do not
   rely on the `ObjectStore` change to roll them. A slow image pull on a fresh
   node is not a failure: a recreated instance can sit in `ContainerCreating`
   for minutes. Give it a bounded wait before suspecting the cutover.

   The old primary needs one more step. It keeps the cached credentials of the
   old role, so as a replica it cannot read WAL from the new location and never
   rejoins. As soon as it has become a replica after the switchover, delete its
   pod once more; the volume stays, and the new pod starts with the new
   association's credentials.

   If the switchover leaves the phase stuck ("Switchover in progress", then
   "Waiting for the instances to become active") while every instance is ready,
   that is upstream CloudNativePG issue 11627, a lost configuration reload. Run
   `SELECT pg_reload_conf();` on each instance (psql in the pod, as the owner).
   No restart is needed; the Cluster turns healthy within seconds. Until it
   does, a GitOps sync of the Cluster stays blocked.
4. Mind the primary's shutdown. A primary that cannot archive WAL (it has no
   working credentials for the new location) blocks its own shutdown until
   `stopDelay` runs out, because the instance manager waits for the archive to
   drain. If the first sync left the primary without credentials, expect the
   delete to take up to `stopDelay`.
5. **Take the base backup after the restarts, and prove where it landed.** The
   chart's `onDemand` `Backup` is created at sync, before the instances are
   restarted. It can run before the `ObjectStore` change reaches the plugin and
   write into the OLD location while reporting `completed`, or run with the old
   credentials and fail. Either way a `completed` status does not show that a
   base backup exists in the new location. Once every instance is back, delete
   that `Backup` record (the chart recreates it from the values) or set a new
   `backup.onDemand.name`, so that a fresh one runs, and then check in the
   bucket: a base backup under `<serverName>/base/` in the NEW bucket, WAL
   under `<serverName>/wal/`, the timeline history file archived, and the same
   objects in the replica bucket if there is one.
6. Expect the plugin to delete Kubernetes `Backup` records that are not in the
   new location's catalog: the records of the old location's base backups
   disappear from the cluster. This is bookkeeping only; nothing in S3 is
   touched, and the old location stays restorable from its bucket and
   `serverName`.

## Verifying the result

`cnpgctl verify` (docs/cnpgctl.md) covers health, archiving, the recoverability
window and the last backup. Two opt-in flags cover what a cutover leaves
behind: `--pool-label key=value` fails if an instance runs on a node without
the label (a recreated pod can land elsewhere), and `--check-applied` fails if
a `Database` or `DatabaseRole` of the cluster does not report applied. It does
not read the bucket: step 5's check of the new location stays a manual one.

## The PITR gap

Until the first base backup in the new location completes, the new
`serverName` holds WAL with nothing to replay it onto: point-in-time recovery
into that window is not possible from the new location. Between the switch and
that completion the recoverable history is the old location's, up to the last
WAL it received. To keep the gap short, take the new-named `Backup` as soon as every instance
is recreated (steps 3 to 5), and do not bump `serverName` again until it has
completed.

## Restoring older points from the old serverName

The old directory is not touched and stays readable. To recover a point before
the move, bootstrap a recovery cluster from the OLD bucket and `serverName`
with `bootstrap.mode: recovery` (`bootstrap.recovery.source`: the old
`bucketName`, `s3Prefix` and `serverName`, with credentials that can still read
the legacy bucket) and a `recoveryTarget`, as for any recovery drill.
After the move, restore points since the first new base backup from the new
`serverName`. Keep the legacy bucket's read access until its oldest needed
point has aged out.

## Planning the phases in Go: `pkg/backupplan`

The cutover order above is a state per database, and the library names it so
that a caller's configuration does not have to. `backupplan.Phase` has four
values, each one merge of the caller's configuration:

| Phase      | `OnNewPath` | `ArchiveAssociated` | `LegacyBindsDatabase` | What changes |
|------------|-------------|---------------------|-----------------------|--------------|
| `Legacy`   | no          | no                  | yes                   | nothing; the default |
| `Prepare`  | yes         | no                  | yes                   | step 0: roles exist, no archive association |
| `Detach`   | yes         | no                  | no                    | step 1: the legacy association leaves |
| `Switched` | yes         | yes                 | no                    | step 2: the archive association and the cluster values |

`ArchiveAssociated` is false before `Switched` because EKS allows one
association per (cluster, namespace, ServiceAccount): the legacy one holds the
ServiceAccount until `Detach` removes it. `ParsePhase` validates a string from
a configuration file.

`backupplan.BuildAccess` turns the caller's facts (bucket prefix, environment,
region, and each project's clusters and phase) into the rows of the
`cnpg-backup-access` releases: one per project that is `OnNewPath`, with the
bucket named by `backupnaming.ProjectBucketName`, the role prefix by
`backupnaming.RolePrefix` and `association` set from the phase. The package
has no cloud SDK, so a configuration generator can import it.
