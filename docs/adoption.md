# Adoption

How a platform takes `cnpg-platform`/`cnpg-cluster`/`cnpg-database` into use, and every
breaking upgrade with its steps. The zero-diff gate applies to all of it:
**a consumer adopts a release only when the render it produces is
byte-identical to what runs, or differs exactly by the change the release
announces.**

## Prerequisites

- The [CloudNativePG operator](https://cloudnative-pg.io/), 1.30 or newer —
  `charts/cnpg-cluster`'s `prod` profile sets the quorum-failover annotation
  (`alpha.cnpg.io/failoverQuorum`) unconditionally.
- The `barman-cloud.cloudnative-pg.io` plugin, when `backup.enabled` or a
  `recovery` bootstrap is used.
- `cert-manager`, only if `serverTLS.issuerRef.name` is set (not needed for `serverTLS.existingSecret`/`existingCASecret`, which only name Secrets that already exist).
- Nothing else. `scheduling`, `backup.encryption`, `backup.endpoint` and
  every identity-shaped value are plain inputs with empty defaults — see
  README "Install and a worked example" for two different estates' shapes.

## Installing the operator and the platform add-ons (`cnpg-platform`)

Once per Kubernetes cluster, by the platform. Two upstream charts install the
operator; this repository supplies their values and one chart for the rest.

1. Install the upstream `cloudnative-pg` chart with
   `examples/operator/cloudnative-pg.values.yaml` (and, in an environment
   where databases are disposable,
   `examples/operator/cloudnative-pg.fast-rollout.values.yaml` on top). The
   preset was written against chart 0.29.0.
2. Install the upstream `plugin-barman-cloud` chart with
   `examples/operator/plugin-barman-cloud.values.yaml` (chart 0.7.0), before
   or with the operator, never after. It needs cert-manager. Exactly one
   release owns the `ObjectStore` CRD.
3. Install `cnpg-platform` with only the parts you want. Start from
   `examples/operator/cnpg-platform.values.yaml`; every name in it is a
   placeholder.

What to fill in, by feature:

- **Storage classes.** The class names and the provisioner are yours. The
  default posture is `WaitForFirstConsumer` binding and expansion on; the
  reclaim policy decides whether deleting a claim takes the volume with it,
  so a production class is normally `Retain`.
- **Metrics policy.** Name the namespaces that host PostgreSQL and the
  scraper (`from` takes ordinary NetworkPolicy peers). The policy selects
  pods labelled `cnpg.io/cluster` only.
- **Alert rules.** Choose `PrometheusRule` or `VMRule`, add the labels your
  rule selector needs, and narrow every expression with `alerts.selector`.
  The certificate rule is off until `alerts.certExpiry.nameRegex` says which
  Certificates are the database's. The rules read `cnpg_*` instance metrics
  (`cnpg-cluster` renders a `PodMonitor` for them by default where the PodMonitor API is served; see `monitoring.podMonitor`),
  cert-manager's expiry metric and the kubelet's volume stats.
- **Role guard.** `admission.databaseRoleGuard.namespaceSelector` is required
  and must not be empty: the guard covers exactly the namespaces it selects.
  Try `validationActions: [Warn, Audit]` first; it needs Kubernetes 1.30 or
  newer (ValidatingAdmissionPolicy is GA there).

## Install order

1. Install the CNPG operator and the barman-cloud plugin (if backups are
   used) first (see above); this chart renders CRs those controllers own.
2. Render `charts/cnpg-cluster` with `profile` and, if the estate archives
   backups, `backup.bucketName` and the identity that may write to it.
   A cluster that is deliberately not backed up (for example a rebuildable
   index) sets `backup.expected: false` and a `backup.notExpectedReason`;
   without that, `cnpg-platform`'s `CnpgBackupNotConfigured` warning fires
   after an hour for any instance that exports no backup metric. The opt-out
   travels as a pod label and a `PodMonitor` target label, so it needs the
   chart's `PodMonitor`. Upgrading with `alerts.enabled` therefore adds one
   rule: set `alerts.backupNotConfigured.enabled=false` to keep the old set.
3. A product installs `charts/cnpg-database` (usually as a dependency of its
   own chart) into its own namespace, against the platform's `clusterName`,
   for its databases, roles and per-role client certificates.

## Adopting an existing hand-written `Cluster`

Render this chart with values that reproduce the existing object's spec
field for field — `profile`, `instances`, `storage`, `resources`,
`scheduling.databasePool`/`tolerations`, and any `postgresql.parameters` —
and diff the render against `kubectl get cluster <name> -o yaml` before
applying. A diff at this step is a value to add, not a migration to plan.

### Keeping the archive of the Cluster you adopt

A Cluster that already archives has a barman server directory
(`<bucket>/<prefix>/<serverName>/`) holding its base backups and WAL. Adopt it
without moving that directory: the chart must hand the plugin the same bucket,
prefix and `serverName`, or archiving silently starts a second timeline
elsewhere (or `check-wal-archive` refuses it).

- The chart renders the store itself: set `backup.bucketName`, leave
  `backup.s3Prefix` empty if the old prefix was `{namespace}/{clusterName}`
  (otherwise set it), and set `backup.serverName` to the old directory name.
  The chart's ObjectStore is named `<clusterName>-objectstore`; a differently
  named one that points at the same path can be deleted AFTER the Cluster
  refers to the new one.
- Another owner already renders the ObjectStore: set `backup.objectStoreName`
  to its name and `backup.serverName` to the old directory; the chart renders
  no store and the object keeps its owner, bucket, credentials and retention.

Before the first sync, set `argocd.argoproj.io/sync-options:
Prune=false,Delete=false` on the live ObjectStore: if the previous owner
stops rendering it first, a deleted ObjectStore stalls WAL archiving.
After the sync, `ContinuousArchiving` must be `True` and
`cnpg_pg_stat_archiver_last_archived_time` must advance.

### Operator-generated Secrets are left behind

A Cluster the operator used to manage certificates for owns three Secrets
the operator generated: `<cluster>-replication`, `<cluster>-ca` and
`<cluster>-server` (owned by the Cluster, labelled
`app.kubernetes.io/managed-by: cloudnative-pg`). The chart never uses those
names: the replication Certificate writes `<cluster>-replication-tls`
(`replication.secretName`), the server side uses `<cluster>-server-tls` and
`<cluster>-server-ca`, and the client CA is `<cluster>-client-ca`. A chart
Certificate that wrote an operator-named Secret would be stuck in
`IncorrectIssuer` (cert-manager does not overwrite a Secret it did not
issue) while the Cluster, already pointing at the per-database client CA,
failed with `x509: certificate signed by unknown authority`.

After adoption the three operator Secrets are unused. Wait until the
Cluster is `Ready` on the chart's Secrets
(`kubectl get cluster app-db -o jsonpath='{.spec.certificates}'`), then
delete them:

```sh
kubectl -n app delete secret app-db-replication app-db-ca app-db-server
```

Deleting before the Cluster is Ready on the chart's Secrets makes the
operator regenerate them.

## Breaking changes

### Unreleased — `cnpg-database` becomes the product-installed chart

`databaseName`, `clusterName` and `owner` keep their meaning and the default
render (`clusterName`, `namespace`, `databaseName` only) is byte-identical to
v2.1.1. Three things change, allowed inside v2 until the charts are stable:

1. `namespace` must equal the release namespace (or be left unset); it used
   to be free. Upgrade: install with `--namespace <that namespace>` or drop
   the value.
2. `databaseName` is optional (a product with only roles sets none) and is now
   checked: `^[a-z][a-z0-9_]{0,62}$`, not `postgres`/`template0`/`template1`.
3. `owner` is checked (`^[a-z][a-z0-9_-]{0,62}$`, never `pg_*`).

A product that copied a `pg-guard` template around the cluster's values
replaces it with `expect.clusterName`, `expect.databaseName` and
`expect.owner` on this chart, set to the names derived from its own install
name; the guard's other checks are built in. New optional inputs: `databases`,
`roles`, `clientCertificate`, `clientCertificateIssuerRef`, `expect`.

### v2.0.0 — chart-side IAM removed; `cnpg-database` absorbed

The chart renders no IRSA annotation and no role. With no static keys the
`ObjectStore` inherits the pod's ambient identity, so the platform must
bind whatever identity it uses (EKS Pod Identity, IRSA, or a node role) to
the `{clusterName}` ServiceAccount and grant it the bucket prefix, before
upgrading a cluster with `backup.enabled: true`. `databases[]`/`customers[]`/
`roles[]` moved onto `cnpg-cluster` itself; a project using the standalone
`cnpg-database` chart for a database it also owns the cluster for can fold
it into `databases[]` (optional — the standalone chart is still supported
for a project that owns a database but not the cluster).

### Current HEAD — `scheduling.databasePool` lost its profile-aware default

Every release through v1.2.1 gave `scheduling.databasePool` a profile-aware
default: left unset, the `prod` profile silently pinned `karpenter.sh/nodepool:
database` and added an unconditional `{key: arch, operator: Exists}`
toleration on **every** profile, `devel` included. Both defaults were estate
facts (a pool literally named `database`, an `arch` taint) baked into the
chart rather than supplied by the caller — the kind of thing component
contract rule C13 exists to catch.

At HEAD, `scheduling.databasePool` defaults to empty on both profiles, and
the `arch` toleration is gone entirely; nothing renders a `nodeSelector` or a
`tolerations` list unless the caller sets `scheduling.databasePool` and/or
`scheduling.tolerations`.

**To reproduce the pre-HEAD `prod` render exactly**, set:

```yaml
scheduling:
  databasePool: database
  tolerations:
    - key: arch
      operator: Exists
```

**To reproduce the pre-HEAD `devel` render**, which had the `arch`
toleration but no pool, set:

```yaml
scheduling:
  tolerations:
    - key: arch
      operator: Exists
```

A platform that upgrades without adding either block gets a real change —
its clusters stop being scheduled onto the tainted pool and stop tolerating
the `arch` taint — so this is exactly the case the zero-diff gate exists
for: diff the render before rolling the upgrade out, and add the values
block above in the same change if the platform still wants the old
placement.
