# Reference

Every value, generated from `values.yaml`'s own comments in each chart.
`values.schema.json` (in the same directory as each chart) is the strict,
machine-checked form of this table — an unknown top-level key, or a value of
the wrong type, fails the render rather than being silently ignored.

## charts/cnpg-platform

Everything is off until enabled, and there are no estate defaults. The
chart renders nothing with its default values.

| Key | Type | Default | What it does |
|---|---|---|---|
| `commonLabels` / `commonAnnotations` | map | `{}` | Stamped on every object the chart renders. |
| `storageClasses.enabled` | bool | `false` | Render `storageClasses.classes`. Enabled with no classes fails the render. |
| `storageClasses.classes[].name` | string | required | Class name. Duplicates fail the render. |
| `storageClasses.classes[].provisioner` | string | required | CSI provisioner. |
| `storageClasses.classes[].parameters` | map[string]string | `{}` | Provisioner parameters. |
| `storageClasses.classes[].reclaimPolicy` | `Retain` \| `Delete` | `Retain` | What happens to the volume when its claim is deleted. |
| `storageClasses.classes[].volumeBindingMode` | `WaitForFirstConsumer` \| `Immediate` | `WaitForFirstConsumer` | Bind where the pod is scheduled. |
| `storageClasses.classes[].allowVolumeExpansion` | bool | `true` | Online growth. |
| `storageClasses.classes[].isDefault` | bool | `false` | Sets the default-class annotation. At most one class. |
| `storageClasses.classes[].mountOptions` / `labels` / `annotations` | list / map / map | empty | Passthrough. |
| `metricsNetworkPolicy.enabled` | bool | `false` | One `NetworkPolicy` per listed namespace opening the metrics port on database instance pods. |
| `metricsNetworkPolicy.name` | string | `cnpg-instance-metrics` | Policy name. |
| `metricsNetworkPolicy.port` | int | `9187` | The instance exporter's port. |
| `metricsNetworkPolicy.namespaces` | []string \| []object | `[]` | Required when enabled. Each entry is a namespace name, or `{name, labels, annotations}` when that namespace's policy needs metadata the others do not carry (merged over `metricsNetworkPolicy.labels` / `.annotations`, the entry winning). A namespace listed twice fails the render. |
| `metricsNetworkPolicy.from` | []NetworkPolicyPeer | `[]` | Required when enabled; no default scraper. Each peer needs a selector. |
| `podMonitor.enabled` | bool | `false` | Render ONE `PodMonitor` that scrapes every CloudNativePG instance pod the scraper can see: pods labelled `cnpg.io/podRole=instance` (the operator sets it on instance pods and only there), the port named `metrics`, path `/metrics`. The scrape job is `<namespace>/<name>`. Needs the `monitoring.coreos.com/v1` `PodMonitor` API (prometheus-operator, or the VictoriaMetrics operator's converter); the chart does not probe for it. A database that renders its own `PodMonitor` (`cnpg-cluster` does, by default) would be scraped twice: turn that one off (`monitoring.podMonitor.enabled=false`) or leave this off. |
| `podMonitor.name` / `.namespace` | string | `cnpg-instances` / release namespace | The object's name and namespace. |
| `podMonitor.namespaces` | []string | `[]` | Namespaces whose instance pods are scraped. Empty means every namespace (`namespaceSelector.any`). |
| `podMonitor.interval` | duration | `""` | Scrape interval; empty is the scraper's own default. |
| `podMonitor.podTargetLabels` | []string | `[]` | Pod labels copied onto every scraped series, e.g. `cnpg-cluster/backup-expected` (becomes `cnpg_cluster_backup_expected`), which `alerts.backupNotConfigured` reads. |
| `podMonitor.dropPgSettings` / `.keepPgSettings` | bool / []string | `true` / the eight the upstream dashboard reads | Drops the `cnpg_pg_settings_*` family (roughly 500-700 series per instance) before storage, sparing the listed settings. The same relabeling `cnpg-cluster`'s own `PodMonitor` renders. An empty `keepPgSettings` drops the whole family. |
| `podMonitor.metricRelabelings` | []object | `[]` | Appended after the drop. Write `action` explicitly on every rule. |
| `podMonitor.labels` / `.annotations` | map | `{}` | On the object. |
| `alerts.enabled` | bool | `false` | Render the rule object. Enabled with every rule off fails the render. |
| `alerts.kind` | `PrometheusRule` \| `VMRule` | `PrometheusRule` | Which CRD. |
| `alerts.name` | string | `cnpg-baseline` | Object and group name. |
| `alerts.labels` / `annotations` / `ruleLabels` | map | `{}` | On the object / on the object / on every rule. |
| `alerts.selector` | string | `""` | Extra label matchers, added to every expression, e.g. `namespace=~"pg-.*"`. |
| `alerts.severity.warning` / `.critical` | string | `warning` / `critical` | The `severity` label values. |
| `alerts.archiving.enabled` / `.for` | bool / duration | `true` / `15m` | `CnpgWalArchivingFailing`: the last failed archive is newer than the last success. |
| `alerts.backupAge.enabled` / `.maxAgeSeconds` / `.for` | bool / int / duration | `true` / `172800` / `30m` | `CnpgBackupTooOld`: `time() - barman_cloud_cloudnative_pg_io_last_available_backup_timestamp`. The metric is the barman-cloud plugin sidecar's (plugin-barman-cloud v0.13.0, the release of chart 0.7.0: `internal/cnpgi/instance/metrics.go` lines 37-52 name it, 123 and 150 fill it from the ObjectStore `status.serverRecoveryWindow[server].lastSuccessfulBackupTime`; the operator relays plugin metrics under their own names). It is 0 when no backup is recorded, so a never-backed-up cluster fires too. The in-core `cnpg_collector_last_available_backup_timestamp` is deprecated since CloudNativePG 1.26 and stays 0 under the plugin, so it is not used. Set `enabled: false` if backups are taken another way. |
| `alerts.replicationLag.enabled` / `.maxLagSeconds` / `.for` | bool / int / duration | `true` / `300` / `10m` | `CnpgReplicationLagHigh`. |
| `alerts.exporterDown.enabled` / `.for` | bool / duration | `true` / `5m` | `CnpgInstanceExporterDown`: `cnpg_collector_up == 0`, the exporter is up but cannot reach PostgreSQL. |
| `alerts.scrapeDown.enabled` / `.jobRegex` / `.for` | bool / string / duration | `true` / `.+/.+` / `10m` | `CnpgInstanceScrapeDown`: `up{container="postgres",job=~"<jobRegex>"} == 0`, the instance's scrape target is down. The `cnpg-cluster` PodMonitor's job is `<namespace>/<clusterName>` and its target is the `postgres` container. A target that is absent (no PodMonitor, scrape dropped) cannot fire an `up == 0` rule. |
| `alerts.backupNotConfigured.enabled` / `.for` | bool / duration | `true` / `1h` | `CnpgBackupNotConfigured` (warning): `cnpg_collector_up{cnpg_cluster_backup_expected!="false"} unless on (namespace, pod) barman_cloud_cloudnative_pg_io_last_available_backup_timestamp`. An instance that exports no barman-cloud backup series has no backups configured, which `CnpgBackupTooOld` cannot see (no series, no alert). Both series come from the same scrape target, so they join on `(namespace, pod)`. A cluster that is deliberately not backed up opts out in the `cnpg-cluster` chart with `backup.expected: false` and a `backup.notExpectedReason`; the opt-out reaches the rule through the `cnpg_cluster_backup_expected` series label, which the `cnpg-cluster` PodMonitor adds, so it needs that PodMonitor (or a scrape that adds the same label). A cluster that backs up another way, with no barman-cloud metric, should also set `alerts.backupNotConfigured.enabled=false` or opt out. |
| `alerts.certExpiry.enabled` | bool | `false` | `CnpgCertificateExpiring` and `CnpgCertificateExpiryImminent`, from cert-manager's metric. |
| `alerts.certExpiry.nameRegex` | string | `""` | Required when enabled: RE2 over the Certificate `name` label. |
| `alerts.certExpiry.warnDays` / `.criticalDays` / `.for` | int / int / duration | `14` / `3` / `10m` | `warnDays` must exceed `criticalDays`. |
| `alerts.walDisk.enabled` / `.pvcRegex` / `.minFreeFraction` / `.for` | bool / string / number / duration | `true` / `.*-wal` / `0.15` / `10m` | `CnpgWalVolumeFillingUp`, from kubelet volume stats; needs the cluster to have a WAL volume (`prod`). |
| `admission.databaseRoleGuard.enabled` | bool | `false` | Render the `ValidatingAdmissionPolicy` and its binding. |
| `admission.databaseRoleGuard.namespaceSelector` | LabelSelector | `{}` | Required when enabled, with `matchLabels` or `matchExpressions`. |
| `admission.databaseRoleGuard.forbid` | []string | all four | Subset of `superuser`, `replication`, `bypassrls`, `createrole`. |
| `admission.databaseRoleGuard.validationActions` | []string | `[Deny]` | `Deny`, `Warn`, `Audit`. |
| `admission.databaseRoleGuard.failurePolicy` | string | `Fail` | `Fail` or `Ignore`. |
| `admission.databaseRoleGuard.name` | string | `cnpg-database-role-guard` | Policy and binding name. |

The guard assumes the `DatabaseRole` spec fields `superuser`, `replication`,
`bypassrls` and `createrole`; a spec that omits one is accepted.

## charts/cnpg-project-platform

The objects a platform owns for a project's CloudNativePG `Cluster` when the
project's own chart does not render them: the barman-cloud `ObjectStore`, the
`ScheduledBackup` that takes base backups into it, and the server
certificate with its CA Secret. Every list is empty until asked for, so the
chart renders nothing with its default values, and nothing in it names an
estate: every name, namespace and address is an entry's input.

The chart decides NOTHING about which project gets which object, or whether a
project has moved to this arrangement yet. The caller computes that and
passes the result as entries, and as `enabled` on an entry it wants to keep
in its input but not render. It adds no `app.kubernetes.io` labels: an object
that already exists under another renderer keeps its metadata exactly, and
only `commonLabels`, `commonAnnotations` and the entry's own are rendered.
Two entries that would render the same object (same kind, namespace and name)
fail the render, whichever list they are in.

| Key | Type | Default | What it does |
|---|---|---|---|
| `commonLabels` / `commonAnnotations` | map | `{}` | On every object. An entry's own keys win. |
| `objectStores[].name` / `.namespace` | string | required | The `ObjectStore`'s name and namespace. |
| `objectStores[].bucketName` / `.prefix` | string | required | The destination, `s3://<bucketName>/<prefix>`. The prefix has no leading or trailing slash. Credentials are the pod's ambient identity (`inheritFromIAMRole`); a store that needs a Secret is `cnpg-cluster`'s. |
| `objectStores[].retentionDays` | int | `30` | The functional point-in-time window, rendered as `retentionPolicy: <n>d`. A bucket lifecycle rule is only a backstop and must outlive it. |
| `objectStores[].dataCompression` | `bzip2` \| `gzip` \| `lz4` \| `snappy` \| `""` | `snappy` | Base-backup compression; empty sends none. |
| `objectStores[].walCompression` | as above plus `xz`, `zstd` | `zstd` | WAL compression. |
| `objectStores[].encryption` | `AES256` \| `aws:kms` \| `""` | `AES256` | Server-side encryption asked for on every upload, on data and WAL. Empty sends no header, for stores that reject it. |
| `objectStores[].dataJobs` / `.walMaxParallel` | int | `2` / `4` | Parallel uploads. |
| `scheduledBackups[].name` / `.namespace` / `.clusterName` / `.objectStoreName` | string | required | The `ScheduledBackup`, the CNPG `Cluster` it backs up and the `ObjectStore` it writes to. Method `plugin`, `backupOwnerReference: self`. |
| `scheduledBackups[].schedule` | string | `0 0 2 * * *` | Six-field cron, seconds first. |
| `serverTLS[].clusterName` / `.namespace` | string | required | The `Cluster`. The `Certificate` covers `<clusterName>-{rw,ro,r}.<namespace>.svc.cluster.local` and nothing shorter (a private CA constrained to `cluster.local` cannot sign the short forms). |
| `serverTLS[].certificateName` / `.secretName` / `.caSecretName` | string | `<clusterName>-server-tls` / `<clusterName>-server-tls` / `<clusterName>-server-ca` | The names, so an object another renderer made can be taken over under its own name. The Certificate and its Secret may share a name; two Secrets or two Certificates may not. |
| `serverTLS[].issuerRef` | `{name, kind, group}` | required | The cert-manager issuer. |
| `serverTLS[].caCertificates` | []string (PEM) | required | The roots the issued chain verifies against, in order, each trimmed and joined on a new line into the CA Secret's `ca.crt` (what the operator, replicas and poolers verify the server with). The issued Secret's own `ca.crt` is not enough: an issuer below an intermediate puts the intermediate there, and libpq will not accept a trust anchor that is not self-signed. The Secret carries `ca.crt` as `stringData`. |
| `serverTLS[].duration` / `.renewBefore` / `.privateKey` | duration / duration / object | unset | Passed to the `Certificate` when set; cert-manager's defaults otherwise. |
| `*[].labels` / `.annotations` | map | `{}` | On the entry's object(s); the CA Secret also carries `cnpg.io/reload: "true"`, the `Certificate` sets it on its Secret through `secretTemplate`, so a renewal is served without a restart. |
| `serverTLS[].caSecretAnnotations` | map | `{}` | Annotations on the CA Secret only, merged over the entry's `annotations` (this one wins). Lets the Certificate and the CA Secret differ, e.g. an Argo CD `sync-options` guard on the Secret alone. |
| `*[].enabled` | bool | `true` | `false` renders nothing for the entry and exempts it from the duplicate check. |

A namespace named `on`, `off`, `y` or `n` is read by YAML as a boolean and
refused by the schema; quote it.

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
| `backup.expected` / `backup.notExpectedReason` | bool / string | `true` / `""` | Whether the cluster is meant to be backed up. `false` is the declared opt-out from `cnpg-platform`'s `CnpgBackupNotConfigured` and REQUIRES a non-empty reason (the render fails otherwise). It adds the pod label `cnpg-cluster/backup-expected: "false"` through `spec.inheritedMetadata.labels` (unlike `labels`, not gated by the operator's `INHERITED_LABELS`; the operator patches it onto running pods, no restart), and the `PodMonitor` gets `podTargetLabels: [cnpg-cluster/backup-expected]`, so every scraped series of the cluster carries the label `cnpg_cluster_backup_expected="false"` (the operator turns each non-alphanumeric character of the pod-label key into `_`). Default render: neither is emitted. |
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
| `postgresql.extra_pg_hba` | []string | `[]` | Extra `pg_hba.conf` lines, appended **after** the catch-all `hostssl all all all cert`. hba is first-match, so these reach nothing today; kept for compatibility. Use `postgresql.pgHba.beforeCatchAll`. |
| `postgresql.pgHba.beforeCatchAll` | []string | `[]` | Extra `pg_hba.conf` lines placed after the scram and people lines and before the catch-all, where they can match. |
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

#### Names-only mode: Secrets that already exist

When something else already mints the server certificate Secret and the
server CA Secret (for example `app-db-server-tls` and `app-db-server-ca`) and
clients verify against that CA, name them and the chart renders nothing for
the server side:

| Key | Type | Default | What it does |
|---|---|---|---|
| `serverTLS.existingSecret` | string | `""` | Name of an existing `kubernetes.io/tls` Secret. Becomes `spec.certificates.serverTLSSecret`. |
| `serverTLS.existingCASecret` | string | `""` | Name of an existing Secret holding the CA in `ca.crt`. Becomes `spec.certificates.serverCASecret`. |

No `Certificate`, `Issuer` or CA `Secret` is rendered for the server side;
the client CA, replication and trust objects are unaffected. The render
fails when only one of the two names is set, and when they are combined with
`serverTLS.issuerRef` (the chart cannot both request the certificate and use
one that exists).

The chart does not own these Secrets, so their producer must label them
`cnpg.io/reload` (any value, for example `""`): CloudNativePG reloads a
referenced Secret only with that label, otherwise a renewed certificate is not
served until the instance restarts.

### `trust`, `replication`, `people` (optional, platform-installed)

Per-database client trust; the platform installs the chart once per cluster. Everything is off by default and a default render is unchanged. Model and ordering: [`authentication.md`](authentication.md).

| Key | Type | Default | Meaning |
|---|---|---|---|
| `trust.namespace` | string | `""` | cert-manager's cluster resource namespace, also trust-manager's trust namespace. Required with `ca.enabled` or `bundle.enabled`. |
| `trust.ca.enabled` | bool | `false` | Renders a self-signed `Issuer`, a CA `Certificate` (ECDSA P-384) in `trust.namespace`, and `ClusterIssuer` `cnpg-<namespace>-<clusterName>` using that CA. |
| `trust.ca.duration` / `.renewBefore` | string | `87600h` / unset | CA lifetime. |
| `trust.policy.enabled` | bool | `false` | Renders an approver-policy `CertificateRequestPolicy` (only the database namespace, `client auth` only, CN in `roles` plus `streaming_replica`, ECDSA only) and the `ClusterRole` + `RoleBinding` that let the requester `use` it. |
| `trust.policy.roles` | []string | `[]` | Common names admitted. Names starting `pg_` are refused. |
| `trust.policy.requester.name` / `.namespace` | string | `cert-manager` / `cert-manager` | The ServiceAccount that requests on behalf of a `Certificate`; bound in the database namespace only. |
| `trust.bundle.enabled` | bool | `false` | Renders a trust-manager `Bundle` delivering `<clusterName>-client-ca` (key `ca.crt`, label `cnpg.io/reload`) into the database namespace. A `Bundle` is cluster-scoped and its Secret shares its name, so the cluster name must be unique across namespaces. |
| `trust.bundle.extraCertificates` | []string (PEM) | `[]` | Extra roots in the bundle, e.g. a people root. |
| `replication.enabled` | bool | `false` | Renders a `Certificate` (CN `streaming_replica`, `client auth`) from the ClusterIssuer and sets `certificates.clientCASecret: <clusterName>-client-ca` and `replicationTLSSecret` (the Secret name below) on the Cluster. |
| `replication.secretName` | string | `""` | Secret the replication `Certificate` writes. Empty = `<clusterName>-replication-tls`, which never collides with the operator's own `<clusterName>-replication`, `-ca` or `-server`. See `docs/adoption.md`. |
| `people[].email` / `.role` | string | `[]` | A row of the `people` pg_ident map and the database role the person may connect as. Roles starting `pg_` are refused. Non-empty `people` adds `hostssl all <roles> all cert map=people` before the catch-all. |

A `map=` in any `pg_hba` line that names no rows (`map=people` with empty `people`, or any other map name) fails the render.

### `monitoring`

Every instance pod serves the PostgreSQL exporter on port 9187 (port name
`metrics`). By default the chart renders a `monitoring.coreos.com/v1`
`PodMonitor` for the cluster's instance pods (`cnpg.io/cluster: {clusterName}`,
`cnpg.io/podRole: instance`), named after the cluster, so its scrape job is
`<namespace>/<clusterName>`. It is rendered only when the cluster serves the
`PodMonitor` API (`.Capabilities.APIVersions.Has "monitoring.coreos.com/v1/PodMonitor"`:
prometheus-operator, or the VictoriaMetrics operator's converter). A cluster
without those CRDs installs unchanged and the object is simply absent;
`helm template` cannot ask a cluster, so pass
`--api-versions monitoring.coreos.com/v1/PodMonitor` to see it. It is a chart
value rather than the operator's `Cluster.spec.monitoring.enablePodMonitor`
because that field is deprecated upstream: the CloudNativePG 1.30 monitoring
documentation ("Deprecation of Automatic `PodMonitor` Creation") says it will
be removed and tells users to create the `PodMonitor` manually. The chart never
sets it.

| Key | Type | Default | What it does |
|---|---|---|---|
| `monitoring.podMonitor.enabled` | bool | `true` | Render the `PodMonitor` when the `PodMonitor` API is served. `false` never renders it. |
| `monitoring.podMonitor.interval` | string | `""` | Scrape interval (e.g. `30s`). Empty = the scraper's default. |
| `monitoring.podMonitor.dropPgSettings` | bool | `true` | Drop the `cnpg_pg_settings_*` family (one series per PostgreSQL setting per instance, roughly 500-700) with a `metricRelabelings` rule. |
| `monitoring.podMonitor.keepPgSettings` | []string | the eight settings the upstream dashboard reads | With `dropPgSettings`, the `name` values of `cnpg_pg_settings_setting` kept anyway: `block_size`, `effective_cache_size`, `maintenance_work_mem`, `max_connections`, `random_page_cost`, `seq_page_cost`, `shared_buffers`, `work_mem`. Empty = drop the whole family. |
| `monitoring.podMonitor.metricRelabelings` | []object | `[]` | Passthrough to the endpoint, rendered AFTER the drop, so yours append. Write `action` on every rule: only the prometheus-operator CRD defaults it, and the VictoriaMetrics operator converts these objects. |
| `monitoring.podMonitor.labels` | map | `{}` | Extra labels on the `PodMonitor` object (merged with top-level `labels`). |
| `monitoring.networkPolicy.from` | []NetworkPolicyPeer | `[]` | Who may reach TCP 9187 on this cluster's instance pods. Non-empty renders `NetworkPolicy` `<clusterName>-metrics` (ingress, selector `cnpg.io/cluster: {clusterName}`, `cnpg.io/podRole: instance`); empty renders none. |
| `monitoring.networkPolicy.enabled` | bool | unset | Optional. `true` with an empty `from` fails the render (an empty peer list means allow-all, so there is no default scraper); `false` suppresses the policy. |
| `monitoring.networkPolicy.labels` | map | `{}` | Extra labels on the `NetworkPolicy` (merged with top-level `labels`). |

**Labels.** The exporter already puts `cluster="<clusterName>"` on its own
series (the CloudNativePG monitoring documentation shows
`cnpg_collector_up{cluster="cluster-example"} 1`), and the upstream Grafana
dashboard's `cluster` variable reads exactly that label from
`cnpg_collector_up`; its other variables use `namespace` and `pod`, which the
scrape adds. So the chart adds no `cluster` relabeling: with the scraper's
default `honor_labels: false`, a target label of that name would move the
exporter's value to `exported_cluster` and break the dashboard. The upstream
`cluster` chart's own PodMonitor adds none either.

**The `pg_settings` drop and the dashboard.** The upstream dashboard reads
`cnpg_pg_settings_setting` for the eight settings above, so the default drop
keeps those and removes the rest (three relabel rules: mark the kept names with a
temporary label, drop the family's unmarked series, then `labeldrop` the temporary
label, because Prometheus does not reliably strip `__`-prefixed labels after metric
relabeling).

The exporter is served over plain HTTP; the chart does not enable
`spec.monitoring.tls`. A default-deny ingress policy must admit the scraper to
port 9187 on the instance pods: set `monitoring.networkPolicy.from`.

### `scheduling` (estate facts — no default)

| Key | Type | Default | What it does |
|---|---|---|---|
| `scheduling.databasePool` | string | `""` | Node pool name. Selects `nodeSelector: {karpenter.sh/nodepool: <value>}` and adds a matching `Equal`/`NoSchedule` toleration for it automatically. Pins both profiles equally — there is no profile-aware default (unlike releases before the current one; see `docs/adoption.md`). |
| `scheduling.tolerations` | []object | `[]` | Extra raw Kubernetes tolerations (passthrough), appended after the pool's own (if `databasePool` is set). |

## charts/cnpg-database

The chart a product installs (typically as a dependency of its own `-infra`
chart) against the cluster the platform installed with `cnpg-cluster`. It
renders, in the release namespace only, `Database` objects, unprivileged
`DatabaseRole` objects and a cert-manager `Certificate` per role that asks for
a client certificate. It renders nothing else.

### Identity and guard

One render-time guard (`templates/guard.yaml`) checks these; a mismatch fails
the render instead of leaving a `Database` that never reconciles.

| Key | Type | Default | What it does |
|---|---|---|---|
| `clusterName` | string | required | Name of the platform's CNPG `Cluster`; a DNS label. |
| `namespace` | string | `""` | Optional. When set it must equal the release namespace; the chart renders into the namespace it is installed into. |
| `databaseName` | string | `""` | The primary database (`spec.name`). Empty renders none. Shape `^[a-z][a-z0-9_]{0,62}$`; `postgres`, `template0` and `template1` are refused. |
| `owner` | string | `app` | Owner of the primary database and default owner of `databases[]`. Shape `^[a-z][a-z0-9_-]{0,62}$`; `pg_*` is refused. |
| `expect.clusterName` / `expect.databaseName` / `expect.owner` | string | unset | Exact-match assertions for a product that derives these names from its own install name; a mismatch fails the render. |
| `allowedConsumers` | []string | `[]` | Retired: no-op, kept for values compatibility. |

### `databases[]`

| Key | Type | Default | What it does |
|---|---|---|---|
| `databases[].name` | string | required | Database name, same shape as `databaseName`; names are compared with `_` and `-` equal, duplicates are refused. The object name is `{clusterName}-{name}` with `_` as `-`. |
| `databases[].owner` | string | `owner` | Owner role. |
| `databases[].ensure` | string | `present` | `present` or `absent`. |
| `databases[].databaseReclaimPolicy` | string | unset | `retain` or `delete`. |
| `databases[].schemas` | []string | `[]` | Declarative schemas, owned by `owner`. |

### `roles[]`

`superuser`, `replication`, `bypassrls`, `createrole` and `createdb` are
refused at render time (the platform's admission policy refuses them as well).
A role name starting with `pg_`, and membership in a `pg_*` role through
`inRoles`, are refused.

| Key | Type | Default | What it does |
|---|---|---|---|
| `roles[].name` | string | required | Role name, `^[a-z][a-z0-9_-]{0,62}$`. Also the certificate `commonName`. The object name is `{clusterName}-role-{name}` with `_` as `-`. |
| `roles[].login` / `inherit` / `inRoles` / `connectionLimit` / `passwordSecret` / `disablePassword` / `validUntil` / `comment` / `ensure` / `databaseRoleReclaimPolicy` | | CNPG defaults | Passed to the `DatabaseRole` spec unchanged; `login` defaults to `true`, `ensure` to `present`. |
| `roles[].clientCertificate.enabled` | bool | `false` | Renders a `Certificate` into Secret `{clusterName}-role-{name}` (`_` as `-`), `commonName` the role name, usage `client auth`, `rotationPolicy: Always`, `secretTemplate.labels: {cnpg.io/reload: "true"}`. Needs `login: true` and no `passwordSecret`. |
| `roles[].clientCertificate.algorithm` / `size` / `duration` / `renewBefore` | | from `clientCertificate.*` | Per-role override of the defaults below. `size` defaults to 256 for `ECDSA` and 2048 for `RSA`. |
| `roles[].clientCertificate.issuerRef` | object | from `clientCertificateIssuerRef` | `name`, `kind` (`ClusterIssuer` or `Issuer`), `group`. |
| `roles[].clientCertificate.additionalOutputFormats` | []string | `[]` | `[DER]` adds the key as PKCS#8 DER in the Secret, for Java clients (pgjdbc). |

### Certificate defaults

| Key | Type | Default | What it does |
|---|---|---|---|
| `clientCertificate.algorithm` | string | `ECDSA` | `ECDSA` (P-256) or `RSA`. |
| `clientCertificate.size` / `duration` / `renewBefore` / `additionalOutputFormats` | | unset | Defaults for every role's certificate. |
| `clientCertificateIssuerRef` | object | `{}` | Empty name = the platform's naming contract, `ClusterIssuer` `cnpg-{namespace}-{clusterName}`. |

## charts/cnpg-client

A **library chart** (`type: library`): it renders nothing on its own. An
application chart lists it as a dependency and includes its named templates
to connect to a CNPG cluster with a client certificate and
`sslmode=verify-full`. Every template takes one dict,
`(dict "context" $ "client" .Values.postgres)`; `context` is the calling
chart's root context and `client` holds the inputs below.

```yaml
# Chart.yaml of the application
dependencies:
  - name: cnpg-client
    version: <release>
    repository: oci://ghcr.io/truvity/charts
```

Naming contract, shared with `cnpg-cluster`: the role's client certificate is
the Secret `<cluster>-role-<role>` (`tls.crt`, `tls.key`, optionally
`key.der`); the server CA is the Secret `<cluster>-server-ca` (`ca.crt`).

| Template | Render under | Output |
|---|---|---|
| `cnpg-client.volumes` | pod `volumes:` | one projected volume with both Secrets |
| `cnpg-client.volumeMounts` | container `volumeMounts:` | the volume, read-only |
| `cnpg-client.env` | container `env:` | `PGHOST`, `PGPORT`, `PGDATABASE`, `PGUSER`, `PGSSLMODE=verify-full`, `PGSSLROOTCERT`, `PGSSLCERT`, `PGSSLKEY`, plus the optional URL variables |
| `cnpg-client.networkPolicy` | a manifest of its own | egress from `podSelector` to `cnpg.io/cluster: <cluster>` on the port |

| Input | Type | Default | What it does |
|---|---|---|---|
| `cluster` | string | required | The CNPG `Cluster` name (a DNS label). |
| `role` | string | required | The role the certificate names; becomes `PGUSER`. Lower-case letters, digits, `-` and `_`; never a `pg_` role. `PGUSER` and the certificate name keep the role as written; the Secret name slugs `_` to `-` (`app_rw` reads `<cluster>-role-app-rw`). |
| `database` | string | `""` | `PGDATABASE`; left out when empty. |
| `namespace` | string | release namespace | The cluster's namespace, for the host name. |
| `service` | `rw`\|`ro`\|`r` | `rw` | Which cluster Service to connect to. |
| `clusterDomain` | string | `""` | Empty renders the short `<cluster>-<service>.<namespace>.svc` host; set it to append a cluster domain. |
| `port` | int | `5432` | `PGPORT` and the policy port. |
| `sslmode` | string | `verify-full` | Only `verify-full` is accepted; anything else fails the render. |
| `mountPath` | string | `/var/run/cnpg-client` | Directory of the projected files. |
| `volumeName` | string | `cnpg-client` | Name of the volume. |
| `keyDer` | bool | `false` | Also project `key.der` (DER PKCS#8), which pgjdbc reads. |
| `certMode`, `keyMode` | string | `0444`, `0440` | File modes. libpq accepts a root-owned key up to `0640`; a non-root pod reads `0440` through `securityContext.fsGroup`, which the pod must set. |
| `dsnEnv` | string | `""` | Name of an extra env var carrying a `postgresql://` URL (all TLS parameters included). |
| `jdbcEnv` | string | `""` | Name of an extra env var carrying a `jdbc:postgresql://` URL; needs `keyDer: true`. |
| `sslfactory` | string | `org.postgresql.ssl.jdbc4.LibPQFactory` | The JDBC `sslfactory`. |
| `podSelector` | map | required for the policy | `matchLabels` of the application's pods. |
| `clusterNamespaceSelector` | bool | `false` | Add a namespace selector for `namespace`, for a cluster outside the release's namespace. |
| `name` | string | `<release>-cnpg-<cluster>` | NetworkPolicy name. |

An input that is not listed fails the render (a library template has no
`values.schema.json`). The policy selects the application's pods for Egress,
which denies their other egress, DNS included; NetworkPolicies add up, so the
application's own chart grants what else it needs. See
[connecting.md](connecting.md) for driver notes.
