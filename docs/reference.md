# Reference

Every value, generated from `values.yaml`'s own comments in each chart.
`values.schema.json` (in the same directory as each chart) is the strict,
machine-checked form of this table — an unknown top-level key, or a value of
the wrong type, fails the render rather than being silently ignored.

## charts/cnpg-cluster

### Identity

| Key | Type | Default | What it does |
|---|---|---|---|
| `clusterName` | string | `""` (required) | Name of the CNPG `Cluster` and the prefix for every resource this chart renders. |
| `namespace` | string | `""` (required) | Namespace the `Cluster` and its companion resources are rendered into. |
| `profile` | string | `""` (required) | `devel` (1 instance, no PDB) or `prod` (3 instances, PDB, quorum failover annotation, WAL volume, pod anti-affinity). The render fails when unset or invalid — a misspelled profile must never silently degrade `prod` to `devel`. Platform-injected; a project does not set it. |
| `labels` | map[string]string | `{}` | Stamped on the `Cluster` CR and, via `inheritedMetadata`, fanned out to Pods/PVCs/Services by the operator's own `INHERITED_LABELS` setting — list a key there too or it goes nowhere. |

### `backup` (platform-injected)

The bucket and the identity that may write to it are the platform's to mint
and hand in here.

| Key | Type | Default | What it does |
|---|---|---|---|
| `backup.enabled` | bool | `false` | Turns on continuous WAL archiving and renders the backup `ObjectStore`/`ScheduledBackup`. An empty `bucketName` keeps archiving off even when `true`. |
| `backup.bucketName` | string | `""` | S3 (or S3-compatible) bucket backups are written to. |
| `backup.s3Prefix` | string | `""` | Prefix under the bucket. Defaults to `{namespace}/{clusterName}/` — the permission-model write scope — when empty. Override only for recovery drills. |
| `backup.serverName` | string | `""` | Barman server directory under the prefix; defaults to `clusterName`. A recreated (fresh `initdb`) cluster must not reuse its predecessor's archive dir — barman's `check-wal-archive` refuses a non-empty archive for a new timeline history — so bump a generation suffix (`{clusterName}-g2`, `-g3`, ...) on recreate. A recovery-bootstrapped cluster continues the old dir instead. |
| `backup.schedule` | string (cron) | `"0 0 2 * * *"` | Schedule for the `ScheduledBackup`. |
| `backup.retentionDays` | integer | `30` | The functional PITR window, enforced by the barman-cloud plugin's own pruning. A bucket lifecycle rule, if the platform sets one, is only a backstop for what the plugin fails to prune and must outlive this window. |
| `backup.walCompression` | string | `zstd` | WAL segment compression. The plugin's WAL enum is wider than the data enum below (adds xz/zstd). |
| `backup.dataCompression` | string | `snappy` | Base backup compression. Narrower enum than WAL's (bzip2/gzip/lz4/snappy). |
| `backup.encryption` | string | `AES256` | Server-side encryption header requested on every upload. Empty sends no `x-amz-server-side-encryption` header and leaves encryption to the bucket's own policy — required by stores that reject the header (Cloudflare R2 among them). |
| `backup.endpoint` | string | `""` | S3-compatible endpoint URL (Cloudflare R2, MinIO, Ceph RGW, ...). Empty keeps the AWS endpoint, so nothing changes for an AWS caller. Addressed path-style, the S3 SDK's own default once an endpoint is set; no region is needed either (signs with `us-east-1`, which R2 aliases to its single `auto` region and MinIO ignores). |
| `backup.endpointCA.name` / `.key` | string | unset | Secret (and key inside it) holding the PEM bundle that verifies a private endpoint's certificate. Both are required together; empty trusts the system roots. |
| `backup.existingSecret` | string | `""` | Existing Secret with static `AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY`. Empty keeps the pod's ambient identity (`inheritFromIAMRole`) — the right default on AWS, since asking for keys there would ask for the thing pod identity exists to remove. |
| `backup.existingSecretKeys.accessKeyId` / `.secretAccessKey` | string | `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` | Key names read from `existingSecret`. |
| `backup.existingSecretKeys.sessionToken` / `.region` | string | `""` | Read only when named — the plugin refuses a missing key rather than skipping it, so leave these empty for the usual static key pair. |

### `bootstrap` (project-declared, static)

| Key | Type | Default | What it does |
|---|---|---|---|
| `bootstrap.mode` | string | `initdb` | `initdb` creates a fresh database; `recovery` bootstraps from `bootstrap.recovery.source`. |
| `bootstrap.initdb.database` | string | `""` | Bootstrap database name. Empty renders no `initdb` bootstrap block. |
| `bootstrap.initdb.owner` | string | `""` | Technical owner role for the bootstrap database — used only by the migration job (DDL), never interactively. CNPG's own `initdb` also mints the `{clusterName}-app` basic-auth Secret for it. Also the implicit owner for `databases[]`/`customers[]` entries that don't set their own. |
| `bootstrap.initdb.schemas` | []string | `[]` | Declarative schemas on the bootstrap database. |
| `bootstrap.recovery.source.*` | — | all empty | Bucket coordinates for the archive being recovered from, same shape and meaning as `backup.*` (it may be a different store than the one this cluster archives to going forward — a drill from R2 into a cluster that archives to AWS, say). Has no `encryption` key: the archive was already written with whatever encryption it was written with. |
| `bootstrap.recovery.target.targetTime` | string | `""` | PITR target timestamp. Empty recovers to the latest available point. |
| `bootstrap.recovery.target.exclusive` | bool | `false` | Whether the recovery target is exclusive. |

### `databases`, `customers`, `roles` (project-declared)

| Key | Type | Default | What it does |
|---|---|---|---|
| `databases[].name` | string | required | PostgreSQL database name (absorbs what the retired standalone use of `cnpg-database` did inline). |
| `databases[].owner` | string | `bootstrap.initdb.owner` | Owner role. |
| `databases[].schemas` | []string | `[]` | Declarative schemas on this database. |
| `databases[].ensure` | string | `present` | `present` or `absent`. |
| `customers[].name` | string | required | Renders a `Database` (`customer-{name}`, owned by `bootstrap.initdb.owner`) plus a `{name}-access` NOLOGIN `DatabaseRole` — the shared-cluster, database-per-customer shape. One daemon certificate joins many customer databases via `inRoles`; one shared PITR timeline. |
| `customers[].ensure` | string | `present` | `present` or `absent`. |
| `roles[].name` | string | required | Rendered as a `DatabaseRole` CR, unless it equals `bootstrap.initdb.owner` (which needs no entry — `initdb` already minted its Secret). |
| `roles[].auth` | string | `cert` | `cert` — operator-issued mTLS client certificate in Secret `{name}-client-cert` (the terminal state for applications). `password` — the compatibility escape hatch; any non-owner password role must bring its own `passwordSecret` (a `kubernetes.io/basic-auth` Secret) since the chart never generates passwords: template-time randomness would rotate the password under a running application on every GitOps sync. |
| `roles[].inRoles` | []string | `[]` | Roles this role is a member of. |
| `roles[].passwordSecret` | string | unset | Required when `auth: password` on a non-owner role. |

### Overridable knobs (the profile provides defaults)

| Key | Type | Default | What it does |
|---|---|---|---|
| `instances` | integer or null | `null` | Instance count. Null = profile default (devel 1, prod 3). `prod` requires >= 3 — quorum failover is meaningless below it. |
| `storage.size` | string | `""` | Empty = profile default (devel `2Gi`, prod `10Gi`). |
| `storage.storageClass` | string | `""` | Empty = profile default (devel `cnpg-gp3-delete`, prod `cnpg-gp3`). |
| `walStorage.size` | string | `"5Gi"` | WAL volume size. `prod` only; `devel` skips the WAL volume entirely. |
| `resources` | object | `{}` | Raw Kubernetes `ResourceRequirements` (passthrough). Empty = profile default (devel `100m`/`256Mi` request + `512Mi` limit with `shared_buffers: 64MB`; prod `250m`/`512Mi` request + `1Gi` limit). A set value replaces the default wholesale, not merges with it. |
| `postgresql.parameters` | map | `{}` | Extra `postgresql.conf` parameters, merged over the profile's own (`synchronous_commit`, `shared_buffers`, `max_slot_wal_keep_size`). |
| `postgresql.extra_pg_hba` | []string | `[]` | Extra `pg_hba.conf` lines, appended **after** the profile's own posture lines (hba is first-match; a leading blanket rule would shadow the password lines this chart emits per password role). |
| `enableSuperuserAccess` | bool | `false` | `spec.enableSuperuserAccess`. |

### `serverTLS` (optional)

By default the operator mints a self-signed CA per cluster and signs the
server certificate from it. Naming an issuer moves the **server** side only
— client CA, replication certificates and `pg_hba` are unchanged.

| Key | Type | Default | What it does |
|---|---|---|---|
| `serverTLS.issuerRef.name` / `.kind` / `.group` | string | `""` | cert-manager issuer. Empty `name` = the operator's own CA (default). |
| `serverTLS.duration` / `.renewBefore` | string | `""` | Passed to the `Certificate` when set; cert-manager's own defaults otherwise. |
| `serverTLS.privateKey` | object | `{}` | Passthrough to the `Certificate`'s `spec.privateKey`, e.g. `{algorithm: ECDSA, size: 384}` — an issuer that signs one key type only (most Vault/OpenBAO roles) refuses any other. |
| `serverTLS.caCertificates` | string (PEM) | `""` | Required with `issuerRef.name`: the roots that verify the issued chain. Becomes the server CA Secret's `ca.crt`. The issued Secret's own `ca.crt` is not enough when the issuer sits below an intermediate — libpq will not accept a trust anchor that is not self-signed. |

The rendered `Certificate` covers every Service of the cluster fully
qualified: `{clusterName}-{rw,ro,r}.{namespace}.svc.cluster.local`. A client
under `sslmode=verify-full` must dial one of those names.

The `Certificate` sets `secretTemplate.labels: {cnpg.io/reload: "true"}` and
the `{clusterName}-server-ca` Secret carries the same label. CloudNativePG
reloads a user-provided server Secret only when it has that label; without
it a renewed certificate is not served until the instance restarts.

### `scheduling` (estate facts — no default)

| Key | Type | Default | What it does |
|---|---|---|---|
| `scheduling.databasePool` | string | `""` | Node pool name. Selects `nodeSelector: {karpenter.sh/nodepool: <value>}` and adds a matching `Equal`/`NoSchedule` toleration for it automatically. Pins both profiles equally — there is no profile-aware default (unlike releases before the current one; see `docs/adoption.md`). |
| `scheduling.tolerations` | []object | `[]` | Extra raw Kubernetes tolerations (passthrough), appended after the pool's own (if `databasePool` is set). |

## charts/cnpg-database

| Key | Type | Default | What it does |
|---|---|---|---|
| `databaseName` | string | required | PostgreSQL database name (`spec.name` on the rendered `Database`). |
| `clusterName` | string | required | Name of the existing CNPG `Cluster` this database belongs to. |
| `namespace` | string | required | Namespace the `Database` CR is rendered into. |
| `owner` | string | `app` | PostgreSQL role that owns this database. |
| `allowedConsumers` | []string | `[]` | Retired (ADR-026): previously generated a `CiliumNetworkPolicy` ingress rule per entry. Kept as a no-op for values compatibility; per-install `NetworkPolicy` objects live in the consuming chart now. |
