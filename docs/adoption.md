# Adoption

How a platform takes `cnpg-cluster`/`cnpg-database` into use, and every
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
- `cert-manager`, only if `serverTLS.issuerRef.name` is set.
- Nothing else. `scheduling`, `backup.encryption`, `backup.endpoint` and
  every identity-shaped value are plain inputs with empty defaults — see
  README "Install and a worked example" for two different estates' shapes.

## Install order

1. Install the CNPG operator and the barman-cloud plugin (if backups are
   used) first; this chart renders CRs those controllers own.
2. Render `charts/cnpg-cluster` with `profile` and, if the estate archives
   backups, `backup.bucketName` and the identity that may write to it.
3. A project that owns a database but not the cluster installs
   `charts/cnpg-database` against an existing `clusterName`/`namespace`
   instead of declaring its database inline under the cluster's own
   `databases[]`.

## Adopting an existing hand-written `Cluster`

Render this chart with values that reproduce the existing object's spec
field for field — `profile`, `instances`, `storage`, `resources`,
`scheduling.databasePool`/`tolerations`, and any `postgresql.parameters` —
and diff the render against `kubectl get cluster <name> -o yaml` before
applying. A diff at this step is a value to add, not a migration to plan.

## Breaking changes

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
