# Changelog

One heading per tag, newest first, prose written for a consumer deciding
whether to move. See `docs/adoption.md` for the zero-diff gate every
upgrade is expected to clear.

## Unreleased

- **Fixed (Go, TypeScript, Python and Kotlin clients):** the default retry now rides out a switchover or failover. It used to stop after 5 tries, and with jittered back-off from 200ms those tries were spent in about 1.5 seconds, so the 30 second budget never applied; a CloudNativePG switchover leaves the primary unreachable for 10 to 30 seconds, and every call on the defaults (`Retry`/`Do`, `retry`, `withRetry`, and the first connection in `New`) failed in the middle of it. The default policy now has no attempt cap and is bounded by time: initial delay 200ms, max delay 2s (was 5s), budget 45s (was 30s). `RetryPolicy.Attempts` (`attempts`) of 0 means no cap and is the new default; a value above 0 still caps the tries, and a negative one is refused. `CNPG_CLIENT_RETRY_ATTEMPTS` unset or `0` means no cap, `CNPG_CLIENT_RETRY_BUDGET` and `CNPG_CLIENT_RETRY_MAX_DELAY` set the budget and the sleep ceiling. The caller's context deadline or abort signal still ends the retry sooner. A process that pinned all three values keeps its behaviour; one that relied on the defaults now waits up to 45 seconds before a persistent connection failure is returned (set `CNPG_CLIENT_RETRY_BUDGET` lower to fail faster). `IsRetryable` is unchanged. This changes default behaviour, so it is a minor release (v2.30.0).

## v2.29.0

- **Feature (chart `cnpg-database`):** an opt-in generated password for a login role, so a product stops copying the same three objects. `roles[].generatedPassword: {secretName, length, digits, symbols, noUpper, allowRepeat}` renders an External Secrets `Password` generator and a once-only `ExternalSecret` (`refreshInterval: "0"`, a `kubernetes.io/basic-auth` Secret) named `secretName`, and points the `DatabaseRole`'s `passwordSecret` at it. `roles[].objectName` and `roles[].labels` let a product keep the object names and labels it already renders (a renamed Secret would be regenerated and lock out running pods). A render that sets none of them is byte-identical (the existing goldens are unchanged; the new case is `generated-password`). The External Secrets CRDs are needed only by a role that sets `generatedPassword`; the chart does not probe for them. The render fails when `generatedPassword` is combined with a client certificate, a different `passwordSecret` or `disablePassword: true`, or has no `secretName`.

## v2.28.0

- **Feature (charts `cnpg-cluster`, `cnpg-projects`):** an opt-in posture profile and an external password role, so a platform stops repeating the same values and one hand-written hba line per project. A render that sets neither is byte-identical (the existing goldens are unchanged; the new cases are `platform-posture`, `platform-posture-explicit` and, for `cnpg-projects`, `posture`).
  - `posture: platform` fills, only while a value is unset, `scheduling.databasePool: durable`, `scheduling.tolerations: [{key: arch, operator: Exists}]`, `trust.namespace: cert-manager`, `trust.ca.enabled`, `trust.policy.enabled` and `trust.bundle.enabled` true, `trust.policy.roles` (the `roles[]` that hold a credential, then the bootstrap owner), `replication.enabled: true` and `monitoring.podMonitor.enabled: false`. An explicit value always wins, `false` included: the five booleans now default to `null` (read as before: `false`, and `true` for the PodMonitor), so "unset" can be told from "set to the old default". It does not invent the caller's inputs: the private PKI roots (`trust.bundle.extraCertificates`), `peopleProject`, `profile`, the backup coordinates and `serverTLS`. An unknown posture fails the render.
  - `roles[].external: true` (with `auth: password`): another release declares the `DatabaseRole` and its Secret. The chart renders no `DatabaseRole` and needs no `passwordSecret`, and emits the role's `hostssl all <name> all scram-sha-256` line where every password role has it: after the owner's, before the people lines, the `beforeCatchAll` lines and the catch-all. For a role whose line a consumer wrote in `postgresql.pgHba.beforeCatchAll` the `pg_hba` is byte-identical (a test pins the order: owner, password roles, people, catch-all). The people check still refuses an external role that collides with `{project}_admin|observer|read`.

## v2.27.0

- **Feature (charts `cnpg-cluster`, `cnpg-projects`):** an opt-in backup preset that states the per-project model once, so a consumer passes coordinates instead of repeating the rules. Every value only fills what is left empty (an explicit value wins), and a render that sets none of them is byte-identical (the existing goldens are unchanged; the new case is `backup-preset`).
  - `backup.bucket: {prefix, project, env, region}` computes `bucketName` as `<prefix>-<project>-<env>-<region>`, the rule of `backupnaming.ProjectBucketName` with the same checks (all four required, 3 to 63 characters, `[a-z0-9-]`, no edge hyphen); the render fails on an invalid name. A Go test holds the chart to `ProjectBucketName` over a table of inputs.
  - `backup.generation: <gN>` computes `serverName` as `<project>-pg-<gN>` (`backupnaming.ServerName`); the project is `backup.project`, else `backup.bucket.project`.
  - `backup.mode: perProjectBucket` sets what a bucket with no-delete writer roles needs: `retentionDays` 0 (while it is left at the default 30), `tags {cnpg-backup-retention: managed}`, `historyTags {cnpg-backup-retention: keep}` and `onDemand.name` = the generation (each while empty; `backup.onDemandFromGeneration: false` renders no one-off `Backup`, for a cluster born on its bucket). It requires `backup.generation`. The schedule is not touched.

## v2.26.0

- **Feature (`cnpgctl verify`, package `pkg/verify`):** two opt-in assertions. `--pool-label key=value` (`Options.PoolLabel`) fails unless every Pod of the cluster runs on a node with that label; `--check-applied` (`Options.CheckApplied`) fails unless every `Database` and `DatabaseRole` of the cluster reports `status.applied: true`, naming each object that does not (with its `status.message`). Both are off by default, so a run without them is unchanged. The data comes from the new optional interfaces `PlacementSource` and `DeclarativeSource`, which `KubeSource` implements; `Source` is unchanged, and a custom `Source` without them gets a FAIL result when an option is set. `ParsePoolLabel` splits the flag value. The extra reads need `get` on nodes and `list` on pods, `databases` and `databaseroles`.
- **Docs:** `docs/moving-to-a-new-backup-bucket.md` gains what the first live cutovers taught: take a base backup into the old location before the move; verify the new association in AWS (the controller can report one Synced that does not exist); build the switch change after the detach has merged; the old primary keeps the old role's cached credentials and never rejoins until its pod is deleted once it is a replica; a switchover can stick (upstream issue 11627, fixed by `SELECT pg_reload_conf()` on each instance); a slow image pull is not a failure; the chart's `onDemand` `Backup` can complete into the OLD location, so the base backup is re-taken after the restarts and its presence in the new location (and its replica) is checked. `docs/cnpgctl.md` documents the flags.

## v2.25.0

- **Feature (Go, new package `pkg/backupplan`):** the backup cutover plan, with no cloud SDK. `Phase` (`Legacy`, `Prepare`, `Detach`, `Switched`) with `ParsePhase`, `Valid`, `Phases` and the predicates `OnNewPath`, `ArchiveAssociated` and `LegacyBindsDatabase` (the one-association-per-ServiceAccount order of docs/moving-to-a-new-backup-bucket.md), and `BuildAccess(AccessArgs)`, a pure builder of the `cnpg-backup-access` release rows (`AccessRow`: project, bucket via `backupnaming.ProjectBucketName`, role prefix via `backupnaming.RolePrefix`, phase and clusters with `Association = Phase.ArchiveAssociated()`) for every project that is `OnNewPath`. Opt-in: no existing API changes.

## v2.24.0

- **Feature (Go, `backupstore`):** an opt-in replica for the per-project bucket set. `ProjectBucketsArgs.Replica *ProjectReplica` (`Region`, `Provider`, `ObjectLockGovernance`, `ObjectLockDays`, `RolePermissionsBoundary`, `RoleName`, `KeyDescription`) gives every project's bucket a replica in another region of the same account and maps onto the existing `Args.Replica` per project: replication from the primary, and with `ObjectLockGovernance` the replica is created with Object Lock in GOVERNANCE mode, a default retention of `ObjectLockDays` (default `Lifecycle.RetentionDays`), the delete-protection bucket policy and the lifecycle rules. A caller that leaves `Replica` nil gets byte-identical resources (the `projects/urns.json` golden is unchanged); `projects/urns-replica.json` pins the replica case. Object Lock is creation-time only, and objects that existed before replication need S3 Batch Replication (docs/backupstore.md).
- **Feature (Go, `pkg/backupnaming`):** `ReplicaBucketName(prefix, project, env, region)` (the replica's name; the prefix is still a required input) and `ReplicationRoleName(env, project)` (`<env>-pg-<project>-replication`, the default replication role name).

## v2.23.0

- **Feature (Go, new package `pkg/backupnaming`):** the naming rules of the per-project backup model, with no cloud SDK: `ProjectBucketName(prefix, project, env, region)` (`<prefix>-<project>-<env>-<region>`; the prefix is a required input, the library has no estate stem) with `ValidateBucketName` (3 to 63 characters, `[a-z0-9-]`, no edge hyphen), `ClusterPrefix(namespace, cluster)` (`<namespace>/<cluster>/`), `RolePrefix(env)`, `ArchiveRoleName` and `RestoreRoleName` (`<env>-pg-<cluster>-archive|restore`), `RestoreServiceAccount` and `ServerName(project, generation)` (`<project>-pg-<generation>`).
- **Feature (Go, `backupstore`):** `NewProjectBuckets(ctx, *ProjectBucketsArgs)` creates one `BackupStore` per project of an environment (SSE-S3, `SingleProject`, `DenyDestructive`, one expiry age; an archive tenant with the association's cluster, namespace and ServiceAccount session tags and a restore tenant per cluster), and `BuildProjectBuckets` returns the validated `Args` without registering. The default registration is a component named after the bucket, directly under the stack, with the options you pass: the same Pulumi identity as `New(ctx, bucketName, args, pulumi.Providers(p))` per bucket (a golden and an equality test pin it). `ResourceName`, `ResourceOptions`, `ProjectOptions` (aliases, parents), `LegacyTopLevel` and `Protect` keep a caller's existing URNs. Opt-in: no existing API changes.
- **Behaviour change (chart `cnpg-backup-access`):** the `PodIdentityAssociation` objects no longer carry `services.k8s.aws/adoption-policy: adopt-or-create`. A render of associations loses the annotation (the `Role` objects keep it: role names are unique, so adopting is safe). With adoption, a new association object adopts the existing association of the same (EKS cluster, namespace, ServiceAccount); when the legacy object that also owns it is pruned, its controller deletes the association in AWS and the database is left with none while the new object still reports Synced. `global.adoptAssociations: true` restores the old behaviour. A release that already created the objects with the annotation keeps the annotation in the cluster until the objects are re-applied; it only matters at creation.
- **Docs:** `docs/moving-to-a-new-backup-bucket.md` has a new cutover order (the old "retain and adopt" advice is removed): the legacy association leaves in a first sync and is verified gone in AWS, then `archive.association: true` and the cluster values in a second; every instance is recreated (replicas first, then a switchover); a primary that cannot archive blocks its shutdown up to `stopDelay`; the chart's `onDemand` `Backup` fires at sync, before the restarts, and fails, so a new-named one is taken after them; the plugin deletes Kubernetes `Backup` records that are not in the new location's catalog (S3 is untouched). `docs/backupstore.md` documents the bucket set and URN preservation, `docs/backup-access.md` the adoption value.

## v2.22.0

- **Feature (charts `cnpg-cluster`, `cnpg-projects`):** what a move to a new backup bucket needs, all opt-in; a render that sets none of them is byte-identical (the existing goldens are unchanged).
  - `backup.retentionDays: 0` renders no `retentionPolicy` on the `ObjectStore`. With the no-delete roles of a per-project bucket the plugin's pruning would get 403 and mark backups failed; expiry is the bucket lifecycle's job. The default stays `30`.
  - `backup.tags` and `backup.historyTags` render `spec.configuration.tags` and `.historyTags` (the `cnpg-backup-retention: managed` / `keep` contract of the backupstore lifecycle). Default empty.
  - `backup.onDemand.name: <id>` renders a one-off plugin `Backup` named `<clusterName>-<id>` with `argocd.argoproj.io/sync-options: Prune=false`; a new id renders a new `Backup` and leaves the old one. `ScheduledBackup` `immediate` does not fire on a `serverName` bump; this does. Default off.
  - `cnpg-projects` carries the same templates (`hack/sync-cnpg-projects.sh`); the parity test now covers the new `backup-cutover` case.
- **Feature (chart `cnpg-backup-access`):** `clusters[].archive.association: false` renders the archive `Role` but no `PodIdentityAssociation`, for the cutover period in which the ServiceAccount is still bound to a legacy role (EKS allows one association per cluster, namespace and ServiceAccount). Default `true`.
- **Docs:** new guide docs/moving-to-a-new-backup-bucket.md: the cutover order, the PITR gap until the first base backup in the new location completes, and restoring older points from the old `serverName`.

## v2.21.0

- **Feature (chart `cnpg-backup-access`, new):** the source-account side of a per-project backup bucket as ACK objects. For each cluster entry it renders an `iam.services.k8s.aws` `Role` `<rolePrefix>-<cluster>-archive` and `-restore` (Pod Identity trust pinned to `aws:SourceAccount`, `aws:SourceArn` (the EKS cluster) and the binding's namespace and ServiceAccount session tags, optional permissions boundary, tags, and an inline policy generated from the bucket name, prefix and partition) and the `eks.services.k8s.aws` `PodIdentityAssociation` of each (archive: the cluster's own ServiceAccount; restore: `<cluster>-restore`, optionally in a drill namespace, optionally rendered as a `ServiceAccount`). The inline policies equal `backupstore.TenantRolePolicy` for an SSE-S3 `SingleProject` bucket byte for byte; a Go test renders the chart and compares them. The association's `roleARN` is computed (the `cnpg-projects` `backupAccess` pattern); `global.useRoleRef` writes `roleRef` instead. The render is refused for an empty bucket, account, region, EKS cluster or role prefix, a duplicate cluster or prefix, and a prefix that does not end in `/`. Published as `oci://ghcr.io/truvity/charts/cnpg-backup-access`; see docs/backup-access.md.
- **Feature (Go, `backupstore`):** the per-cluster access model, all opt-in. A caller that sets none of the new fields gets byte-identical resources and policies (a golden test pins a minimal and a full configuration); a note on the one escape change below.
  - `Args.Encryption: EncryptionSSES3` makes the bucket `AES256` by default, creates no KMS key (primary or replica), gives role policies and the replication role no KMS statements, and adds a bucket-policy deny of `s3:PutObject` unless `s3:x-amz-server-side-encryption` is `AES256` (the client must send the header: the plugin's `encryption: AES256`). `KMSKeyARN` is empty in that mode. KMS stays the default.
  - `Args.Tenants []Tenant` (role ARN, prefix `<namespace>/<cluster>/`, purpose `archive`|`restore`, optional Pod Identity session tags `eks-cluster-name`, `kubernetes-namespace`, `kubernetes-service-account`) replace the writer glob: per tenant an allow of exactly its rights on its prefix, a strict `s3:ListBucket` with `s3:prefix` limited to the prefix (no `StringLikeIfExists`), and an explicit deny for the role outside its prefixes; globally, denies of `DeleteObject`, `DeleteObjectVersion`, `BypassGovernanceRetention`, `PutLifecycleConfiguration`, `PutBucketPolicy`, `DeleteBucketPolicy`, `PutBucketVersioning`, `PutReplicationConfiguration` and `PutBucketObjectLockConfiguration` for principals outside the backup account (`Args.DenyDestructive` turns the global denies on without tenants). **Behaviour to know:** barman-cloud calls HeadBucket first, which carries no `s3:prefix`, so a strict prefix list returns 403 to it; `Args.SingleProject` (below) admits the unprefixed list for a bucket that holds one project. Archive also gets `s3:PutObjectTagging` (barman sends `--tags` with the upload).
  - `TenantRolePolicy`, `Args.TenantRolePolicies` and the `BackupStore.TenantRolePolicies` output return the identity policy of each tenant role as JSON for the caller to attach in the source account (archive: put with the `AES256` condition, abort, list parts, get, tagging on the prefix; restore: get on the prefix; list limited to the prefix; never a delete).
  - `Args.Lifecycle` (`RetentionDays`, `NoncurrentDays` and `AbortMultipartDays` default 7, optional `Transitions`) and `LifecycleRules` generate one tag-based expiry rule, `housekeeping` (noncurrent versions, incomplete multipart uploads, expired delete markers) and optional tiering. Objects are selected by `cnpg-backup-retention=managed` (`ObjectStore` `tags`; `historyTags: keep` so history files never expire).
  - `Replica.ObjectLockGovernance` creates the replica with Object Lock in GOVERNANCE mode (retention `Replica.ObjectLockDays`, default N), the delete-protection bucket policy and lifecycle rules. Object Lock is creation-time only: an existing replica is not changed in place (setting it forces a replacement that protection refuses); create a new replica bucket.
- **Feature (Go, `backupstore`):** a bucket per project per environment is the documented standard shape, with one expiry age per bucket. This reworks the unreleased API of the entry above; nothing here was ever tagged, so the changes are not breaking.
  - `Args.SingleProject` (requires `Tenants`): every tenant may `s3:ListBucket` the whole bucket, in the bucket policy and in the tenant role policies (`TenantRolePolicyArgs.SingleProject`), so barman-cloud's HeadBucket works; the list-outside-prefix deny is dropped. Put and get stay on the tenant's own `<namespace>/<cluster>/` prefix and the explicit deny outside it stays, as does the absence of any delete. Replaces `Tenant.AllowHeadBucket`, which is removed.
  - `Lifecycle` has one expiry age: `BaseBackupDays` is renamed `RetentionDays`, and `WALMarginDays`, the per-path `base-backups/...` and `wal/...` rules and `Tenant.ServerName` (only the lifecycle used it) are removed. The single `managed` rule expires everything tagged `cnpg-backup-retention=managed` at `RetentionDays`; `keep`-tagged history files never expire; `housekeeping` and `Transitions` are unchanged; the replica lifecycle and the Object Lock governance default retention use the same days. `LifecycleRules(l, withTiering)` no longer takes the tenants.
  - **Requirement:** a backup's WAL is archived after the backup starts, so with backups scheduled away from midnight UTC (02:00, say) both are created on the same UTC day and expire together. Keep the backup schedule away from midnight UTC (docs/backupstore.md).
- **Note (Go, `backupstore`):** bucket and replica policy documents are now marshalled without HTML escaping. The bytes are unchanged for every valid AWS name or ARN (none holds `<`, `>` or `&`).

## v2.20.0

- **Behaviour change (Go, `backupstore`):** the bucket's default encryption rule now sets `BucketKeyEnabled: true` (S3 Bucket Keys), and states `BlockedEncryptionTypes: [SSE-C]` (what S3 already applies), by default and without an option. A busy backup bucket drops about 99% of its KMS requests (one observed bucket made about 78k `GenerateDataKey` calls an hour). For an existing bucket the next apply is one in-place update of the `-encryption` resource (`bucketKeyEnabled: false => true`); it affects only objects written afterwards, nothing is rewritten, and no resource is created, replaced or deleted. A caller that already enabled bucket keys through a transform sees no change and can drop the transform.

## v2.19.0

- **Feature (Go):** `backupstore.NewRole` and `backupstore.RolePolicy`, the writer's side of the backup store. `NewRole` makes the IAM role the bucket's writer glob admits, with its inline policy and one Pod Identity association per service account, as the `truvity/k8s` Pod Identity component. The policy is write (put, get, delete, multipart) on one prefix, read and list on the project prefix, and the bucket's key; `RolePolicy` renders it alone. Bucket and key ARNs, both prefixes, the names, namespace and service accounts are inputs. Options (`WithChildNames`, `WithLegacyTopLevel`, `WithProtect`, `WithIgnoreAssociationTags`, `WithResourceOptions`) let a caller that already runs such a role keep its logical names and aliases, so its URNs do not move. The policy bytes are pinned by a test. New dependency: `github.com/truvity/k8s` v0.28.0.
- **Feature (charts `cnpg-projects`, `cnpg-project-platform`):** the platform objects take their conventions from the chart. A `scheduledBackups[]` entry without `name` / `objectStoreName` is named `{clusterName}-scheduled-backup` and points at `{clusterName}-objectstore`. `serverTLSDefaults` holds what every Cluster of a platform shares (`issuerRef`, `caCertificates`, `duration`, `renewBefore`, `privateKey`, `labels`) and is merged under each `serverTLS[]` entry, the entry winning. Because of this, `name`, `objectStoreName`, `issuerRef` and `caCertificates` are no longer required by the schema on an entry; the render still fails when the merged entry lacks `issuerRef` or `caCertificates`, or a scheduled backup lacks `clusterName`.
- **Behaviour change (annotations on the platform objects):** new `argocd.scheduledBackupSyncWave` (`"25"`), `argocd.serverTLSSyncWave` (`"-1"`, the server Certificate and CA Secret sync before the Cluster that names them) and `argocd.guardCASecret` (`true`: `argocd.argoproj.io/sync-options: Prune=false,Delete=false` on the CA Secret alone). They are on by default, so a render of a ScheduledBackup, Certificate or CA Secret gains those annotations (an entry's own annotations win). Set the waves to `""` and `guardCASecret` to `false` to render none. Nothing else in the render changes; the cnpg-cluster chart is untouched.

## v2.18.4

- **Fix (TypeScript client):** `CnpgPool.create` no longer fails on a slow or refused first try. The first connection ran `health()`, whose 2s bound ended a cold start (DNS, TLS verify-full, authentication) with `first connection ... health: timed out`, and `isRetryable` read only the top-level error, so that timeout and even `ECONNREFUSED` (wrapped by `health()`) were never retried. Each startup try is now a raw `select 1` with its own 5s bound (`STARTUP_ATTEMPT_TIMEOUT_MS`, error class `AttemptTimeoutError`) under `config.retry`; `isRetryable` walks the `cause` chain; `health()` keeps its 2s bound. New `RetryPolicy.onRetry(attempt, err, delayMs)` hook, called before each sleep, and `CnpgPoolOptions.signal` to abandon the first connection (an abort is never retried). Mirrors the Go client fix of v2.18.2.

## v2.18.3

- Dependency updates.

## v2.18.2

- **Fix (the transitional ddl role is gone):** the `DatabaseRole` that v2.18.1 re-declared for a retained `{project}_ddl` role (`databaseRoleReclaimPolicy: delete`) is no longer rendered. With the object gone from the render, a GitOps controller that prunes deletes it, the operator drops the PostgreSQL role, and the `ddl` level is fully removed. Before moving, check that the role owns nothing and holds no grants (`select count(*) from pg_class c join pg_roles r on r.oid=c.relowner where r.rolname='{project}_ddl'` and the `pg_shdepend` rows for it); a role that owns objects makes the drop fail visibly on the object. If the controller does not prune (`Prune=false`), delete the `{cluster}-{project}-ddl` objects by hand. A render of `peopleProject` now has no `_ddl` object at all.

- **Fix:** the Go client's first connection (`pgclient.New`) no longer fails on a slow first try. Each try was bounded by the 2s health timeout, and `IsRetryable` treats a deadline as the caller's own, so a cold start (DNS, TLS verify-full, authentication) that took over 2s ended `New` with `first connection ... health: context deadline exceeded` on the first try and the configured retry never ran. Each startup try now has its own 5s timeout (`StartupAttemptTimeout`), and a try that times out while the caller's context is live is retried under `cfg.Retry`. `Health` keeps its 2s bound. New `RetryPolicy.OnRetry` hook, called before each sleep, so a service can log every attempt.

## v2.18.1

- **Fix:** the `people` pg_ident rule assumed the DN order of PostgreSQL 17 (`OU=..,CN=..`); PostgreSQL 18 presents `CN=..,OU=..` and no row matched (`no match in usermap "people"`). `peopleProject` now renders two rows, one per order, each with a single capture group, same anti-injection shape (exactly one OU, a CN without an unescaped comma, no other RDN).
- **Fix:** v2.18.0 asked the operator to drop a retained `{project}_ddl` role with a `DatabaseRole` of `ensure: absent`; the operator rejects that (`ensure: absent is not supported for DatabaseRole; delete the resource with databaseRoleReclaimPolicy: delete instead`), so the sync of the existing `*-ddl` objects failed. The chart now re-declares that legacy object with `databaseRoleReclaimPolicy: delete` (everything else as in v2.17.x). Syncing it makes the policy live; the release after it removes the object, which prunes it and drops the role (check first that it owns nothing). Adopt v2.18.1 directly rather than v2.18.0 if the ddl objects exist.

## v2.18.0

- **Feature (minor, a role is removed):** `peopleProject` drops the `ddl` level. It now renders three roles: `{project}_admin` (member of the database owner, never superuser), `{project}_observer` and `{project}_read`. The hba line names those three and the pg_ident rule accepts `OU={project}_(admin|observer|read)`. A certificate with `OU={project}_ddl` no longer maps. For a cluster that rendered `{project}_ddl` under v2.17.x (the PostgreSQL role was kept by `databaseRoleReclaimPolicy: retain`), the chart renders a transitional `DatabaseRole` meant to drop it (it did not work, see v2.18.1). Check first that the role owns nothing (`select count(*) from pg_class c join pg_roles r on r.oid=c.relowner where r.rolname='{project}_ddl'`); a role that owns objects makes the drop fail visibly and nothing is lost. That transitional object goes in a later release. A render without `peopleProject` is byte-identical.

## v2.17.1

- **Fix:** `peopleProject` (v2.17.0) rendered the hba line `hostssl all /^{project}_.*$ all cert clientname=DN map=people` BEFORE the caller's `beforeCatchAll` lines. The pattern matched every role that starts with the project name, such as an application role `{project}_app`, and first match wins, so that role had to present a client certificate and its password login failed. The line now names exactly the four people roles (`{p}_admin,{p}_ddl,{p}_observer,{p}_read`) and sits AFTER the `beforeCatchAll` lines, still before the catch-all. The render also fails when one of the four names is the owner, a `roles[]` entry or a user of a `beforeCatchAll` line. A render without `peopleProject` is byte-identical.

## v2.17.0

- **Feature:** `peopleProject` on `cnpg-cluster` (and `cnpg-projects`): a person's database role comes from the OU of their client certificate, by convention. Set to the project name, the chart renders the four roles `{project}_admin` (member of the database owner, never superuser), `{project}_ddl`, `{project}_observer` and `{project}_read` (no credential of their own), the hba line `hostssl all /^{project}_.*$ all cert clientname=DN map=people` before the catch-all, and one pg_ident rule mapping a certificate with `OU={project}_{level}` to that role. The person connects with `-U {project}_{level}`; a certificate with another project's OU, with no OU, or with a second OU does not map. Off by default: a render without it is byte-identical. The client CA must carry the issuing root of those certificates (`trust.bundle.extraCertificates`).

## v2.16.0

- **Feature:** `roles[]` of `cnpg-cluster` (and `cnpg-projects`) take `auth: none` and `login`. `auth: none` renders a `DatabaseRole` with neither a client certificate nor a password: the shape of the `people_*` roles (`people_cluster_admin`, `people_ddl`, `people_schema_observer`, `people_read_all`), which a person reaches only through the `people` pg_ident map, and which get their rights from `inRoles`. `login: false` renders a pure group role (default `true`, as before). The `DatabaseRole` object name now turns `_` into `-` (a role name with an underscore made an invalid Kubernetes name before; a name without one renders as it did). A render that sets neither field is byte-identical to v2.15.1.

## v2.15.1

- **Fix:** v2.15.0 set `immediate: true` only on the ScheduledBackup that `cnpg-cluster` renders for itself. The `scheduledBackups[]` list of `cnpg-projects` and of `cnpg-project-platform`, which is what the estate renders, did not get it. Both now render `immediate: true` per entry by default; `immediate: false` on an entry leaves it out. The operator acts on the field only for a ScheduledBackup that has never been checked (`status.lastCheckTime` unset), so an existing, running ScheduledBackup takes no extra backup; one that has never run takes its first base backup once. The render diff is that one line on each ScheduledBackup.

## v2.15.0

- **Feature:** `cnpg-cluster` (and `cnpg-projects`, which carries its templates) renders `immediate: true` on the `ScheduledBackup` by default (`backup.immediate`), so a new cluster takes its first base backup when the `ScheduledBackup` is created instead of at the first cron tick. Until then such a cluster archives WAL with no base backup under its server directory, nothing is restorable, and a "no recent backup" alert on the barman-cloud plugin's `last_available_backup_timestamp` fires for up to a day. The render diff is that one line on every `ScheduledBackup`; the operator reads the field only on a `ScheduledBackup` that has never run, so an existing one is unaffected. It does not fire on a `serverName` bump, so the values file and `docs/reference.md` now say to take an on-demand `Backup` (method `plugin`) right after one. `backup.immediate: false` restores the previous render.

## v2.14.0

- **Feature:** `github.com/truvity/cnpg/v2/pkg/servertls`, the check of a catalogue's per-environment list of where a project's CNPG server takes its certificate from a workload issuer: the project has the postgres capability, and every listed environment is known, listed once, and has a primary install. It moves here from a consuming repository's own catalogue code. No chart, `cnpgctl` or client change: every render is byte-identical to v2.13.0.

## v2.13.0

- **Feature:** `cnpg-projects` stamps `services.k8s.aws/adoption-policy: adopt-or-create` on its ACK Roles and PodIdentityAssociations, so an archive role or association that already exists under the same identity (role name; cluster, namespace and ServiceAccount) is adopted instead of failing with "already exists". Needs the ResourceAdoption feature gate on the ACK IAM and EKS controllers. Nothing is deleted or recreated; `backupAccess.annotations` can override the policy.

## v2.12.0

- **Feature:** new chart `cnpg-projects`: every project's database on one Kubernetes cluster from a list of rows, so one Application or release carries them all. A `clusters[]` entry renders exactly what `cnpg-cluster` renders for the same values (the templates are carried over by `hack/sync-cnpg-projects.sh`, which CI checks, and a test compares the objects). It also takes the `cnpg-project-platform` inputs (`scheduledBackups`, `serverTLS`; Rule Z: the cluster chart renders the ObjectStore) and `backupAccess`: per project an IAM role and its Pod Identity associations through ACK (S3 write only on `{project}/{primaryCluster}/*`, read on `{project}/*`, the backup key), created in `backupAccess.objectNamespace` so the controller's own identity mints them and a project's narrower identity cannot. `cnpg-project-platform` stays for installs that use it.
- **Feature:** `cnpg-cluster` takes `annotations`, stamped on the Cluster and on the ObjectStore (for example a deployment tool's never-prune markers on the two objects that hold data). Default empty: a default render is byte-identical.

## v2.11.0

- **Feature:** `github.com/truvity/cnpg/v2/pkg/aws/backupstore`, the cross-account CNPG backup bucket and its KMS key as a Pulumi component (`truvity:cnpg/aws:BackupStore`), moves here from `truvity/k8s` (`pkg/aws/backupbucket`, `truvity:k8s/aws:BackupBucket`). The code and every child's logical name are unchanged; the component aliases itself from the former type, so a stack that deployed `backupbucket` adopts it with no replace and a zero-change preview. It builds the versioned, KMS-encrypted bucket, the bucket and key policies that admit a writer-role glob and deny version deletion outside the backup account, the lifecycle backstop, optional Object Lock and an optional same-account cross-region replica. It builds no IAM role: the writer role and its Pod Identity belong to the cluster's cnpg-projects chart. The module's dependency graph gains the Pulumi SDK and the AWS provider; charts and `cnpgctl` are unchanged. See `docs/backupstore.md`.

## v2.10.1

- Dependency and CI pin updates only (ci-workflows v3.25.0, devbox packages, `uv`, lock-file maintenance). No chart, `cnpgctl` or client change: every render is byte-identical to v2.10.0.

## v2.10.0

- **Feature:** `cnpg-project-platform` `serverTLS[].caSecretAnnotations`: annotations on the CA `Secret` only, merged over the entry's `annotations`, so the `Certificate` and the `Secret` can carry different metadata. Default none; a render without it is byte-identical to v2.9.0.
- No other chart changes: every other render is byte-identical to v2.9.0.

## v2.9.0

- **Feature:** new chart `cnpg-project-platform` (published as `oci://ghcr.io/truvity/charts/cnpg-project-platform`): the per-project objects a platform owns beside a CloudNativePG `Cluster` that another chart renders. Three lists, each empty until asked for: `objectStores` (the barman-cloud `ObjectStore`, destination `s3://<bucketName>/<prefix>`, the pod's ambient identity for credentials), `scheduledBackups` (a `ScheduledBackup`, method `plugin`, naming the `ObjectStore` it writes to) and `serverTLS` (a cert-manager `Certificate` for the `-rw`, `-ro` and `-r` Services and the CA `Secret` carrying `ca.crt`, both marked `cnpg.io/reload`). Every name, namespace and address is an input and the chart decides nothing about which project gets which object; an entry can be switched off with `enabled: false`. The names of the three server-TLS objects can be set, so a platform can take over objects another renderer made under their own names. No `app.kubernetes.io` labels are added: an object keeps exactly the metadata its entry names. Defaults render nothing. Strict schema, goldens and refused fixtures under `tests/`.
- **Feature:** `cnpg-platform` gains `podMonitor`: ONE `PodMonitor` for every CloudNativePG instance pod the scraper can see (selected by `cnpg.io/podRole=instance`, port `metrics`), with the same `cnpg_pg_settings_*` drop `cnpg-cluster`'s own monitor renders. Off by default; a render with the default values is unchanged. Values `podMonitor.enabled`, `.name`, `.namespace`, `.namespaces`, `.interval`, `.podTargetLabels`, `.dropPgSettings`, `.keepPgSettings`, `.metricRelabelings`, `.labels`, `.annotations`. A database that renders its own `PodMonitor` is scraped twice beside it: turn that one off.
- **Feature:** `cnpg-platform`'s `metricsNetworkPolicy.namespaces` entries may be an object `{name, labels, annotations}` as well as a name, for a namespace whose policy needs metadata the others do not carry. A namespace listed twice now fails the render. Plain names render exactly as before.
- No other chart changes: every other render is byte-identical to v2.8.0.

## v2.8.0

- **Feature:** `charts/barman-cloud-crds`, the CustomResourceDefinitions of the barman-cloud plugin, moves here from `truvity/ocictl` and is published from here (`oci://ghcr.io/truvity/charts/barman-cloud-crds`) at the UPSTREAM version it mirrors, as it was from `truvity/ocictl`: `0.13.0` today, already in the registry, so the first release here publishes nothing for it. A release publishes the chart only when its version is not in the registry yet, and never overwrites one. The rendered CRDs are byte-identical to the ocictl chart. The CRDs are generated from `crdctl.yaml` by `just crds` (crdctl from a pinned `truvity/ocictl` release) and committed; `just crds-check` fails when they drift from the pinned upstream.
- No other chart changes.

## v2.7.0

- **Feature:** Kotlin/JVM adapter `clients/kotlin` (`com.truvity.cnpg:cnpg-client`, pgjdbc + HikariCP, Java 21 or later, built with Maven like the Keycloak providers). Same contract and the same 14 conformance cases as the Go and TypeScript adapters: `verify-full` only with the CA file as the sole trust root, no free-form JDBC URL, the CA, client certificate, key and password file read again for every new connection (`ReloadingDataSource`; pgjdbc reads the TLS files itself per connection), retry of connection-class errors only (the whole cause chain is read, because pgjdbc and HikariCP wrap), a health check bounded to 2s, and opt-in OpenTelemetry spans (API only) that carry the statement and never its arguments. Two things differ from the other languages and are documented in `clients/kotlin/README.md`: pgjdbc reads the client key as DER PKCS#8, so `PGSSLKEY` points at the `cnpg-client` chart's `key.der` (`keyDer: true`); and HikariCP will not retire a connection sooner than 30 seconds, so a shorter `CNPG_CLIENT_CONN_MAX_LIFETIME` is a configuration error instead of a silent 30 minutes.
- **Feature:** Python adapter `clients/python` (`truvity-cnpg-client`, import `truvity_cnpg`, psycopg 3 + psycopg_pool, Python 3.10 or later, built and locked with uv). Same contract and the same 14 conformance cases. Distributed as a wheel and an sdist attached to the GitHub release of the tag (GitHub Packages has no Python registry; release assets of a public repository need no token): `pip install "truvity-cnpg-client[binary] @ https://github.com/truvity/cnpg/releases/download/v2.7.0/truvity_cnpg_client-2.7.0-py3-none-any.whl"`. Public PyPI is a later decision. libpq reads the TLS files for every connection; the password file is read by the pool's connection class for every connection. Needs the client key at `0600` when owned by the process's own user (libpq's rule); the chart's `0440` works with `fsGroup`.
- **Feature:** the release workflow publishes the Kotlin artifact to GitHub Packages (Maven, `https://maven.pkg.github.com/truvity/cnpg`) at the tag's version, after the release job and next to the npm job. GitHub Packages needs a token in `settings.xml` even for public packages; see `clients/kotlin/README.md`. CI runs the same build as a dry run.
- **Test:** `clients/conformance/pg-tls.sh` also writes the client keys as DER PKCS#8 (`*.key.der`); new CI recipes `clients-kotlin`, `clients-kotlin-conformance`, `clients-python` and `clients-python-conformance`; `devbox.json` gains `temurin-bin-21`, `maven`, `python@3.14.4` and `uv`. `guard.sh` reads JUnit XML (`guard.sh kotlin|python <report>`).
- No chart changes: every chart renders byte-identical to v2.6.0.

## v2.6.0

- **Feature:** PostgreSQL client adapters under `clients/`: one contract (`clients/README.md`), one conformance case list, one implementation per language. They connect with `verify-full` only (a missing CA, or any weaker `sslmode`, is a configuration error), read the CA, client certificate, key and password file again for every new connection (so a cert-manager renewal needs no restart), retry only connection-class errors (what a CloudNativePG switchover looks like: `57P01`, `57P02`, `57P03`, `08xxx`, `25006`, `53300`, EOF, reset) with jittered backoff, expose a health check, and offer opt-in OpenTelemetry spans that carry the statement with its placeholders and never its arguments. Their inputs are the libpq environment the `cnpg-client` library chart already renders (`PGHOST`, `PGSSLROOTCERT`, `PGSSLCERT`, `PGSSLKEY`, ...) plus `CNPG_CLIENT_*` tuning variables. Python and Kotlin follow.
- **Feature:** Go adapter `github.com/truvity/cnpg/v2/clients/go/pgclient` (pgx/v5) and `.../pgclient/otelpg` (OpenTelemetry API only). It is part of the existing module, so the new minor is all a consumer needs: `go get github.com/truvity/cnpg/v2@v2.6.0`. The module's dependency graph gains pgx and the OpenTelemetry API; the charts and `cnpgctl` are unchanged.
- **Feature:** TypeScript adapter `clients/ts` (`@truvity/cnpg-client`, node-postgres). Published to GitHub Packages (`https://npm.pkg.github.com`) by the release workflow at the tag's version; the package contains only the built output and the README. GitHub Packages answers 401 to anonymous installs even for a public package, so installing needs an `.npmrc` with a token that has `read:packages` (`clients/ts/README.md`). Public registries (npmjs, PyPI, Maven Central) are a later step.
- **Test:** `clients/conformance/` runs each adapter against a real PostgreSQL that serves TLS: certificates generated per run, an image pinned by digest, 14 cases (verify-full, unknown CA, host name mismatch, refused weaker modes, password and certificate roles, certificate and password rotation, backend termination, a primary switch, statement timeout, permanent errors not retried, health, tracing without arguments). A guard fails the CI job unless every case ran and passed, so a skipped suite cannot be green. New CI recipes `clients-ts`, `clients-go-conformance` and `clients-ts-conformance`; `devbox.json` gains `nodejs@22`.
- No chart changes: every chart renders byte-identical to v2.5.0.

## v2.5.0

- **Feature:** `cnpg-platform` baseline alerts gain `CnpgBackupNotConfigured` (warning, `for: 1h`): `cnpg_collector_up{cnpg_cluster_backup_expected!="false"} unless on (namespace, pod) barman_cloud_cloudnative_pg_io_last_available_backup_timestamp`, because a cluster with no backups has no backup series and `CnpgBackupTooOld` read it as healthy. Values `alerts.backupNotConfigured.enabled` (`true`) and `.for` (`1h`), following `alerts.selector`. An install that renders the alerts gains one rule; set `enabled: false` to keep the old set.
- **Feature:** `cnpg-cluster` can declare that a cluster is deliberately not backed up: `backup.expected` (default `true`) and `backup.notExpectedReason`. `expected: false` with an empty reason fails the render. It adds the pod label `cnpg-cluster/backup-expected: "false"` via `spec.inheritedMetadata.labels` (applied to running pods in place, no restart) and `podTargetLabels` on the `PodMonitor`, so the series carry `cnpg_cluster_backup_expected="false"` and the new alert skips the cluster. A default render is unchanged. See `docs/reference.md`.

## v2.4.0

- **Behaviour change:** with `replication.enabled`, the chart-issued replication `Certificate` now writes Secret `<clusterName>-replication-tls` (and is named the same), not `<clusterName>-replication`; `spec.certificates.replicationTLSSecret` follows. `<clusterName>-replication`, `-ca` and `-server` are the names CloudNativePG generates itself when it manages certificates, and cert-manager never overwrites a Secret it did not issue, so adopting a Cluster the operator used to manage left the Certificate stuck in `IncorrectIssuer` while the Cluster, already on the per-database client CA, failed with `x509: certificate signed by unknown authority`. The other chart Secrets (`<clusterName>-client-ca`, `-server-tls`, `-server-ca`) do not collide and are unchanged. New value `replication.secretName` (default empty = `<clusterName>-replication-tls`). A cluster already adopted under the old name keeps working only if the operator Secret `<clusterName>-replication` was replaced by the cert-manager one; to keep that Secret, set `replication.secretName: <clusterName>-replication` (for `app-db`: `app-db-replication`), which renders exactly as before. Otherwise expect a new Secret and one reload of the instances. After adoption, the unused operator Secrets may be deleted once the Cluster is Ready; see `docs/adoption.md`.
- **Feature:** `cnpg-cluster` renders its `PodMonitor` by default. `monitoring.podMonitor.enabled` is now `true`, but the object is rendered only when the cluster serves the `monitoring.coreos.com/v1` `PodMonitor` API (`.Capabilities.APIVersions`), so a cluster without the Prometheus-operator CRDs installs unchanged; `helm template` needs `--api-versions monitoring.coreos.com/v1/PodMonitor` to show it. Set `enabled: false` to opt out. Where the CRD is served, the render gains one `PodMonitor` (job `<namespace>/<clusterName>`) and nothing else.
- **Feature:** the default `PodMonitor` drops the `cnpg_pg_settings_*` family (roughly 500-700 series per instance) before storage, except the eight settings the upstream CloudNativePG Grafana dashboard reads. New values `monitoring.podMonitor.dropPgSettings` (`true`) and `keepPgSettings`; your `metricRelabelings` are rendered after the drop and keep working. Set `dropPgSettings: false` to keep every series.
- **Docs:** no `cluster` label is added by the scrape. The exporter already emits `cluster="<clusterName>"` on its own series, which is what the upstream dashboard's `cluster` variable reads; a target label of that name would turn the exporter's into `exported_cluster`. See `docs/reference.md`.
- **Feature:** `cnpg-cluster` can render an ingress `NetworkPolicy` for the metrics port: `monitoring.networkPolicy.from` (NetworkPolicyPeer list, default `[]`). It is rendered, for TCP 9187 on this cluster's instance pods, only when `from` is non-empty; `monitoring.networkPolicy.enabled: true` with an empty `from` fails the render, because an empty peer list means allow-all.
- **Feature:** `cnpg-platform` baseline alerts guard against silent absence: `CnpgInstanceExporterDown` (`cnpg_collector_up == 0`) and `CnpgInstanceScrapeDown` (`up{container="postgres",job=~".+/.+"} == 0`, `alerts.scrapeDown.jobRegex`), each with its own `enabled` and `for`, both following `alerts.selector`. They are on by default, so an install that renders the alerts gains two rules; set `alerts.exporterDown.enabled` / `alerts.scrapeDown.enabled` to `false` to keep the old set. Golden renders and the rulecheck gate cover them.

## v2.3.0

- **Feature:** `cnpg-cluster` can use an existing server TLS Secret and server CA Secret by name. New optional values `serverTLS.existingSecret` (a `kubernetes.io/tls` Secret) and `serverTLS.existingCASecret` (a Secret holding `ca.crt`), both empty by default: when set, the Cluster gets `spec.certificates.serverTLSSecret` / `serverCASecret` with those names and the chart renders no `Certificate`, `Issuer` or CA `Secret` for the server side, so a cluster whose clients already verify an existing CA keeps that CA. The render fails when only one of the two is set, or when they are combined with `serverTLS.issuerRef`. The chart does not own these Secrets: their producer must label them `cnpg.io/reload`, or CloudNativePG does not serve a renewed certificate until the instance restarts. A default render is byte-identical. See `docs/reference.md`.

## v2.2.1

- **Fix:** `CnpgBackupTooOld` in `cnpg-platform` read `cnpg_collector_last_available_backup_timestamp`, which CloudNativePG deprecated in 1.26 and which only moves for the in-core Barman Cloud backup or volume snapshots; with the barman-cloud plugin (what `examples/operator/` installs) it stays 0, so the rule fired permanently and could never detect a stale backup. It now reads `barman_cloud_cloudnative_pg_io_last_available_backup_timestamp`, the plugin sidecar's own metric (plugin-barman-cloud v0.13.0, chart 0.7.0). A cluster that backs up another way should set `alerts.backupAge.enabled=false`. Every other rule is unchanged, and each keeps its own `enabled`, threshold and `for` inputs.
- **Test:** golden renders for the PrometheusRule and VMRule variants (all rules on, moved thresholds, single rule), and a `rulecheck` recipe and CI job that parses every rendered VMRule expression with the real VictoriaMetrics binary (truvity/observability `rulecheck` v0.19.0).

## v2.2.0

- **Feature:** new command `cnpgctl` (`cmd/cnpgctl`, archives for linux and darwin, amd64 and arm64, attached to each release; also `go run github.com/truvity/cnpg/v2/cmd/cnpgctl@<tag>`). `cnpgctl verify` runs read-only assertions against a live cluster, each printed PASS/FAIL with a reason, non-zero exit on any FAIL, `--output json` for machines: phase, ready instances and primary; the ContinuousArchiving condition and recoverability window; last successful backup within `--max-backup-age`; server CA, client CA and replication certificates labelled `cnpg.io/reload` and valid for `--min-cert-validity`; the `cnpg.io/reload` label on every user-provided Secret the Cluster names; and pg_hba sanity (no `trust`, every `map=` has pg_ident rows, the catch-all is last). The logic is the reusable package `pkg/verify`. See `docs/cnpgctl.md`. Nothing changes for the charts except that `cnpg-cluster` and `cnpg-database` now commit `appVersion: 0.0.0` like the other two, because the repository now builds a binary and the release stamps `appVersion` from the tag (the field was the upstream operator's version and no template reads it).
- **Test:** a kind conformance suite, `conformance/` (`just conformance`, and `.github/workflows/conformance.yaml` on changes to the charts, weekly, on tags and on demand). It installs cert-manager with its own approver disabled, approver-policy, trust-manager, the upstream CloudNativePG operator with `examples/operator/` values, then `cnpg-platform`, `cnpg-cluster` (trust objects on), `cnpg-database` (two roles with client certificates) and a pod using the `cnpg-client` helpers, and asserts: verify-full login with a role certificate; refusal of the same common name from another CA; approver-policy refusing another namespace and a common name outside the allow-list; the admission guard refusing a superuser `DatabaseRole`; healthy streaming replication with a key-less client CA; a renewed server certificate served without an instance restart; no password fallback for a certificate-only role; and, opt-in, a barman-cloud backup with a point-in-time restore. Gated by `CNPG_CONFORMANCE`, so `just check` and `go test ./...` stay free of docker and a cluster. See `docs/conformance.md`. No chart changes.
- **Feature:** new library chart `cnpg-client`, for application charts that connect to a cluster with a client certificate. Named templates `cnpg-client.volumes`, `cnpg-client.volumeMounts`, `cnpg-client.env` (libpq, plus an optional `postgresql://` or JDBC URL variable) and `cnpg-client.networkPolicy`; it refuses a missing cluster or role, a `pg_` role and any `sslmode` but `verify-full`. Contract: client certificate Secret `<cluster>-role-<role>`, server CA Secret `<cluster>-server-ca`. See `docs/connecting.md`. Nothing changes for existing charts.
- **Feature:** `cnpg-database` is the chart a product installs: besides `Database` objects it renders unprivileged `DatabaseRole` objects and a cert-manager client `Certificate` per role that asks for one (Secret `{cluster}-role-{role}`, `rotationPolicy: Always`, the `cnpg.io/reload` label, ECDSA P-256 by default, optional DER output, issuer `cnpg-{namespace}-{cluster}` unless overridden), behind ONE render-time guard that replaces per-product copies: cluster name, release-namespace match, database and owner shape, optional `expect.*` assertions, and refusal of `superuser`/`replication`/`bypassrls`/`createrole`/`createdb` and of any `pg_*` role name or membership. The default render is byte-identical to v2.1.1; `namespace` must now equal the release namespace and `databaseName`/`owner` are shape-checked (see `docs/adoption.md`).
- **Feature:** `cnpg-cluster` can give every database its own client CA. New optional values, all off by default (a default render is byte-identical): `trust.ca` (a self-signed root, a CA in cert-manager's namespace and ClusterIssuer `cnpg-<namespace>-<cluster>`), `trust.policy` (an approver-policy `CertificateRequestPolicy`, admitting only the database namespace, client-auth, the listed role CNs plus `streaming_replica`, ECDSA, with its RBAC), `trust.bundle` (a trust-manager `Bundle` delivering `<cluster>-client-ca`), `replication` (a `<cluster>-replication` certificate plus `certificates.clientCASecret` and `replicationTLSSecret` on the Cluster) and `people` (a `people` pg_ident map with an hba line before the catch-all). See `docs/authentication.md` and `docs/decisions/0004-per-database-ca.md`.
- **Fix:** `postgresql.extra_pg_hba` lines were appended after the catch-all `hostssl all all all cert` and so matched nothing (hba is first-match). The key is unchanged, for compatibility, and documented as reaching nothing; the new `postgresql.pgHba.beforeCatchAll` places lines where they can match. A `map=` with no pg_ident rows, and `pg_`-prefixed role names under `people` or `trust.policy.roles`, fail the render.
- **Feature:** new chart `cnpg-platform`, the cluster-wide platform add-ons that upstream's charts lack: optional StorageClasses, a metrics NetworkPolicy, baseline alert rules (PrometheusRule or VMRule) and a ValidatingAdmissionPolicy that refuses privileged `DatabaseRole`s. The CloudNativePG operator itself stays upstream's `cloudnative-pg` chart (and `plugin-barman-cloud`), installed directly and tracked by renovate; this repository ships only recommended values for them under `examples/operator/`. Everything is off by default and nothing is an estate default. The chart ships no subcharts. `cnpg-cluster` and `cnpg-database` render byte-identical to v2.1.1.
- **Change:** the repository was renamed from `truvity/cnpg-cluster` to `truvity/cnpg` (GitHub redirects the old name); the Go module path is `github.com/truvity/cnpg/v2` from the next release, and the GoReleaser `project_name` is now `cnpg`. No Go package imports it; chart OCI paths are chart-name based and do not change.
- First `docs/decisions/` records: `0001` (absorb and rename, charts by owner), `0002` (the operator chart), `0003` (the platform chart is `cnpg-platform`).

## v2.1.1

- **Fix:** both `values.schema.json` files admit `global`. Helm hands `global` to every subchart, and the strict schema refused it (`additional properties 'global' not allowed`), so v2.0.0 to v2.1.0 could not be embedded as a dependency at all. Render diff: none; a consumer that pins the chart as a subchart can move to this release.

## v2.1.0

- **Feature:** `monitoring.podMonitor.enabled` (default `false`) renders a `monitoring.coreos.com/v1` `PodMonitor` for the cluster's instance pods (port `metrics`, `/metrics`), with optional `interval`, `metricRelabelings` and `labels`. The operator's own `spec.monitoring.enablePodMonitor` is deprecated upstream, so the chart renders the object itself. With the value at its default the render is byte-identical to v2.0.0; nothing to do on upgrade.

## v2.0.1

- **Fix:** with `serverTLS.issuerRef` set, the server `Certificate` now carries `secretTemplate.labels: {cnpg.io/reload: "true"}` and the `-server-ca` Secret carries the same label. CloudNativePG reloads a user-provided server Secret only when it has that label; without it a cert-manager renewal was not served until the instance restarted, so a client using `sslmode=verify-full` would fail once the old certificate expired. Render diff: the label on those two objects, nothing else. Charts that never set `serverTLS.issuerRef` are unchanged.

## v2.0.0

- **Breaking:** the `database` nodeSelector default and the unconditional `arch` toleration are gone; `scheduling.databasePool` defaults to empty and `scheduling.tolerations` to `[]`. Estates that relied on them set the values shown in `docs/adoption.md`.
- Both charts gain `values.schema.json`; negative Go tests cover rejected values.
- `appVersion` aligned to `1.30.0` for both charts.
- Repository brought onto the component template: `renovate.json` extends the shared preset, `.envrc`, `.editorconfig`, template recipe names (`charts`, `package`) with the old names kept as aliases, `docs/adoption.md` and `docs/reference.md`, README in the contract's heading order.

## v1.2.1

- No chart change. Routine CI dependency maintenance (ci-workflows pin
  bumps, a shared import-ban lint rule).

## v1.2.0

- **Backups and recovery can target any S3-compatible object store**,
  not only AWS. `backup.endpoint` (and `bootstrap.recovery.source.endpoint`)
  points the `ObjectStore` at Cloudflare R2, MinIO or Ceph RGW;
  `backup.existingSecret` supplies static keys where there is no pod
  identity to inherit; `backup.encryption: ""` omits the
  `x-amz-server-side-encryption` header for stores that reject it (R2
  among them). With every new value at its default the render is
  byte-identical to v1.1.8 — an AWS install sees no diff on upgrade.
- Documentation rework: the README gained a "Backups on an S3-compatible
  store" section and a public-consumer-facing pass over the rest.

## v1.1.8

- No chart change. CI dependency bump only.

## v1.1.7

- No chart change. CI dependency bump only.

## v1.1.6

- **Optional server certificate from a cert-manager issuer.** Setting
  `serverTLS.issuerRef.name` moves the cluster's server certificate to a
  named issuer; the client CA, replication and `pg_hba` are unaffected.
  Unset, the render is unchanged from v1.1.5 — the operator keeps
  minting its own CA.

## v1.1.5

- No chart change. CI dependency bump only.

## v1.1.4

- No chart change. CI and release-automation moves only: renovate and
  the devbox-update job now run from the shared fleet caller instead of
  a per-repo workflow, and auto-release mints its own token instead of
  holding a long-lived key.

## v1.1.3

- No chart change. Devbox package alignment.

## v1.1.2

- No chart change. Devbox package alignment and CI dependency bumps.

## v1.1.1

- No chart change. CI unified on the shared `ci-workflows` release lane
  (a security-labelled merge releases same-day instead of waiting for
  the weekly cron); the Go toolchain moved to a pinned 1.27 triad.

## v1.1.0

- **Profile-aware pool selector.** `scheduling.databasePool` becomes
  settable on the `devel` profile too, not only `prod`: an explicit
  value pins either profile to that Karpenter node pool; left unset,
  `prod` still defaulted to the `database` pool and `devel` rode the
  cluster's default pools (the pre-1.1.0 behaviour); an empty string
  opted out on either profile.

  (Superseded in the current chart: `scheduling.databasePool` no longer
  carries a profile-aware default at all — see `docs/adoption.md` for
  the migration from this release's behaviour.)

## v1.0.0

- Initial release. `charts/cnpg-cluster` (a CNPG `Cluster` with the
  devel/prod profile model, roles, declared databases, backup
  `ObjectStore`s and `ScheduledBackup`s) and `charts/cnpg-database` (a
  logical `Database` inside an existing cluster), extracted from their
  maintainers' internal estate.
