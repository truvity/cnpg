# Moving a cluster to a new backup bucket

A running cluster that archived to a shared or legacy bucket moves onto a
per-project bucket (`pkg/aws/backupstore`, access by `cnpg-backup-access`) with
a new `serverName` and a new `ObjectStore`. The charts have four options for
this; all are off by default and change nothing for a render that does not set
them.

| Need | Value |
|---|---|
| The plugin must not prune (the new roles cannot delete) | `backup.retentionDays: 0` renders no `retentionPolicy` |
| The bucket lifecycle must find the objects | `backup.tags` and `backup.historyTags` |
| A base backup in the new location at once | `backup.onDemand.name: <id>` |
| The archive role exists before the ServiceAccount is rebound | `cnpg-backup-access`: `clusters[].archive.association: false` |

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

1. Prepare the destination. Deploy the `backupstore` bucket and
   `cnpg-backup-access` with `clusters[].archive.association: false`: the
   archive and restore roles exist, and no second association is made on the
   ServiceAccount.
2. Mark the legacy association objects `services.k8s.aws/deletion-policy:
   retain`, and let that sync first. Removing the objects later then leaves
   the AWS association in place until the new one replaces it, instead of
   deleting it.
3. In one sync: remove the legacy association objects from Git, set
   `archive.association: true` (the new association takes the
   ServiceAccount), and change the cluster values: `backup.bucketName`, a new
   `backup.serverName` (the next generation, e.g. `<cluster>-g2`; barman
   refuses a non-empty directory for a new history), `backup.retentionDays: 0`,
   `backup.tags` and `backup.historyTags`, and a new `backup.onDemand.name`.
4. The primary must restart: Pod Identity credentials are injected when a pod
   is created, so a running pod keeps the old role's environment. The
   `ObjectStore` change makes the operator roll the primary; if it does not,
   delete the pod (switchover first on a replicated cluster).
5. The `Backup` renders with the sync and runs once the primary is healthy.
   Check it reaches `completed`, and that WAL appears under the new
   `serverName`.

Do not run the legacy association and the new one on one ServiceAccount: EKS
allows one association per (EKS cluster, namespace, ServiceAccount) and refuses
the second.

## The PITR gap

Until the first base backup in the new location completes, the new
`serverName` holds WAL with nothing to replay it onto: point-in-time recovery
into that window is not possible from the new location. Between the switch and
that completion the recoverable history is the old location's, up to the last
WAL it received. To keep the gap short, take the on-demand backup in the same
sync as the move (step 3), and do not bump `serverName` again until it has
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
