# Backup store (Pulumi)

`github.com/truvity/cnpg/v2/pkg/aws/backupstore`

`truvity:cnpg/aws:BackupStore` deploys a cross-account backup bucket: a
versioned S3 bucket in a backup account that a workload account may write to
and may never permanently delete from. It needs an AWS provider for the backup
account: pass it with `pulumi.Providers(p)`.

```go
b, err := backupstore.New(ctx, "backup", &backupstore.Args{
	BucketName:      "example-backup",
	SourceAccountID: sourceAccountID,
	BackupAccountID: backupAccountID,
	WriterRoleName:  "example-writer-*",
	KeyDescription:  "example backups example-backup",
	Tags:            map[string]string{"Owner": "platform"},
	LifecycleRules: backupstore.Backstop(backupstore.BackstopArgs{
		ExpireDays: 180, NoncurrentDays: 180,
		Transitions: []backupstore.Transition{{Days: 30, StorageClass: "STANDARD_IA"}},
	}),
}, pulumi.Providers(backupAccountProvider))
// b.BucketName, b.BucketARN, b.KMSKeyARN: grant the writer from these.
```

### `Args`

| Field | Type | Meaning |
| --- | --- | --- |
| `BucketName` | `string` | The bucket's name; also the stem of every child's logical name. Required. |
| `SourceAccountID`, `BackupAccountID` | `string` | The account whose roles write, and the account the bucket lives in. Only principals in the backup account may delete an object version. Required. |
| `Partition` | `string` | The AWS partition in ARNs. Empty is `aws`. |
| `WriterRoleName` | `string` | The role in the source account the bucket and key policies admit. A glob (`*`, `?`) is allowed. Required, unless `Tenants` is set (then it must be empty). |
| `WriterObjectActions` | `[]string` | Replaces the writer's object actions. Nil grants put, get, delete and the two multipart actions; an append-only log passes put and get. Empty is refused. |
| `Encryption` | `Encryption` | `""`/`EncryptionKMS` (default): a component-owned KMS key. `EncryptionSSES3`: S3-managed keys, no KMS key, see "SSE-S3 mode". |
| `Tenants` | `[]Tenant` | The per-cluster access model; replaces the writer glob. See "Tenants". |
| `DenyDestructive` | `bool` | The global denies without tenants (tenants imply it). |
| `Lifecycle` | `*Lifecycle` | Parametrized, tag-based rules, added after `LifecycleRules`. See "Lifecycle". |
| `ReaderRoleName` | `string` | One more role in the source account, read-only (get, list, decrypt). An exact name; a glob is refused. |
| `ListerRoleNames` | `[]string` | Roles that may list the bucket (and the replica) and nothing else: no object, no decrypt. Exact names; a glob or empty name is refused. |
| `KeyDescription` | `string` | The KMS key's description. Required in KMS mode, and part of the key's state: a change is an in-place update. |
| `Tags` | `map[string]string` | Tags on the bucket, for example compliance tags. The component adds none. |
| `LifecycleRules` | `s3.BucketLifecycleConfigurationV2RuleArray` | The lifecycle configuration; at least one rule from here or from `Lifecycle`. `Backstop` builds one whole-bucket rule. |
| `ObjectLockDays` | `int` | Above zero: Object Lock with a COMPLIANCE default retention of that many days, on the bucket and the replica. Negative is refused. |
| `Replica` | `*Replica` | Replicates to another region; see below. |
| `Protect` | `*bool` | Protect the buckets and the KMS keys. Nil means true; point it at false only to retire a bucket. |
| `LegacyTopLevel` | `bool` | Alias every child from the URN it has as a loose resource directly under the stack. For adopting existing resources. |

`Replica` takes `Provider` (the AWS provider of the replica's region, created
by the caller so it keeps its own URN), `BucketName`, `KeyDescription`,
`RoleName` (the replication role's IAM name) and an optional
`RolePermissionsBoundary` (`pulumi.StringInput`; nil sets none). The
replica gets a bucket policy only when `ListerRoleNames` is not empty, or the
global denies apply (tenants, `DenyDestructive`, `ObjectLockGovernance`).
`ObjectLockGovernance` and `ObjectLockDays` are the locked replica; see
"The locked replica". `KeyDescription` is required in KMS mode only.

`Args.Validate()` (called by the constructor before anything is registered)
returns one error joining every problem: a missing required field, no
lifecycle rules, a negative lock, a glob in a reader or lister name, an
empty list of writer actions, a replica without a provider, name, key
description or role name, or a replica named as the primary.

### Children

Logical names derive from the bucket name `<b>` and, for the replica, the
replica bucket name `<r>`. They are API.

| Child | Type | Name | Present when |
| --- | --- | --- | --- |
| key | `aws:kms/key:Key` | `<b>-kms` | KMS mode (protected, retained on delete) |
| key alias | `aws:kms/alias:Alias` | `<b>-kms-alias` | KMS mode |
| bucket | `aws:s3/bucket:Bucket` | `<b>` | always (protected) |
| versioning | `aws:s3/bucketVersioningV2:BucketVersioningV2` | `<b>-versioning` | always |
| encryption | `aws:s3/bucketServerSideEncryptionConfigurationV2:BucketServerSideEncryptionConfigurationV2` | `<b>-encryption` | always |
| lifecycle | `aws:s3/bucketLifecycleConfigurationV2:BucketLifecycleConfigurationV2` | `<b>-lifecycle` | always |
| object lock | `aws:s3/bucketObjectLockConfigurationV2:BucketObjectLockConfigurationV2` | `<b>-object-lock` | `ObjectLockDays > 0` |
| public access block | `aws:s3/bucketPublicAccessBlock:BucketPublicAccessBlock` | `<b>-public-access` | always |
| bucket policy | `aws:s3/bucketPolicy:BucketPolicy` | `<b>-policy` | always |
| replica key | `aws:kms/key:Key` | `<r>-kms` | `Replica` in KMS mode (protected, retained on delete) |
| replica bucket | `aws:s3/bucketV2:BucketV2` | `<r>` | `Replica` (protected) |
| replica versioning | `aws:s3/bucketVersioningV2:BucketVersioningV2` | `<r>-versioning` | `Replica` |
| replica public access block | `aws:s3/bucketPublicAccessBlock:BucketPublicAccessBlock` | `<r>-public-access` | `Replica` |
| replica object lock | `aws:s3/bucketObjectLockConfigurationV2:BucketObjectLockConfigurationV2` | `<r>-object-lock` | `Replica.ObjectLockGovernance` |
| replica lifecycle | `aws:s3/bucketLifecycleConfigurationV2:BucketLifecycleConfigurationV2` | `<r>-lifecycle` | `Replica.ObjectLockGovernance` and `Lifecycle` |
| replica policy | `aws:s3/bucketPolicy:BucketPolicy` | `<r>-policy` | `Replica` and (`ListerRoleNames`, the global denies, or `ObjectLockGovernance`) |
| replication role | `aws:iam/role:Role` | `<b>-replication-role` | `Replica` |
| replication role policy | `aws:iam/rolePolicy:RolePolicy` | `<b>-replication-policy` | `Replica` |
| replication | `aws:s3/bucketReplicationConfig:BucketReplicationConfig` | `<b>-replication` | `Replica` |

The replica's children use the replica provider; every other child uses the
provider the component was given. Every key is retained on delete: key
destruction is a break-glass act in the accounts this is built for.

### Aliases

The component itself carries an alias from its former type, `truvity:k8s/aws:BackupBucket`, so a state written when it lived in `truvity/k8s` adopts it with no replace.

With `LegacyTopLevel` every child carries
`pulumi.Aliases([]pulumi.Alias{{NoParent: pulumi.Bool(true)}})`: the same
name and type, no parent.

The AWS SDK also aliases some of its own types from the type they had before
the v2 split: the bucket from `aws:s3/bucketV2:BucketV2` (twice, an
identical pair), and versioning, encryption, lifecycle and object lock each
from their own type. The SDK declares those with no parent, so under a
component they resolve beneath the component, not the stack. To keep a state
that still holds the former type at the top level resolvable, `LegacyTopLevel`
adds, for each of those children, one alias with that former type and no
parent. The SDK's identical pair on the bucket resolves to one URN, so the
component declares it once. A test fails when an SDK upgrade declares an
alias the component does not mirror.

### Outputs

`BucketName`, `BucketARN`, `KMSKeyARN` (`pulumi.StringOutput`; the key ARN is
empty in SSE-S3 mode) and `TenantRolePolicies` (`pulumi.StringMapOutput`, role
ARN to identity policy JSON; empty without tenants). The component exports no
stack output of its own: the caller names those.

## The writer's side: `NewRole` and `RolePolicy`

The bucket admits one writer role by name (`Args.WriterRoleName`, a glob).
`NewRole` makes that role in the workload account: an IAM role with a Pod
Identity trust policy, an inline policy and one Pod Identity association per
service account, all as one `truvity/k8s` Pod Identity component
(`truvity:k8s/aws:PodIdentity`, exported as `RoleTypeToken`).

`RolePolicy` renders the inline policy alone, as JSON, for a caller that makes
the role itself:

| Sid | Grants |
|---|---|
| `WriteOwnSubtree` | put, get, delete, multipart on `<bucket>/<WritePrefix>*` |
| `ReadProjectPrefix` | get on `<bucket>/<ReadPrefix>*` |
| `ListProjectPrefix` | list the bucket, `s3:prefix` like `<ReadPrefix>*` if present |
| `UseBackupKey` | encrypt, decrypt, re-encrypt, generate data key on the key |

Delete is on the write subtree because retention pruning needs it; on the
versioned bucket it only creates a delete marker, and the bucket policy denies
`DeleteObjectVersion`. The list statement is `StringLikeIfExists` because
`HeadBucket` carries no `s3:prefix`. The document's bytes are pinned by a
test: an adopted role keeps its policy only while the string is unchanged.

Bucket and key ARNs, both prefixes, the role and policy names, the namespace
and the service accounts are inputs. Options adjust the registration without
the library knowing any estate's names: `WithChildNames` (the logical names of
the role, policy and associations), `WithLegacyTopLevel` (alias each child
from the URN it has directly under the stack), `WithProtect`,
`WithIgnoreAssociationTags` and `WithResourceOptions` (aliases, parent,
dependsOn on the component).

## The tenant model

A tenant is one `(role, prefix, purpose)` the bucket admits. It replaces the
writer glob: the glob admits every matching role to the whole bucket, tenants
separate per project and per Postgres cluster. Existing callers set none of
this and see no change (see "What stays the same").

```go
b, err := backupstore.New(ctx, "backup", &backupstore.Args{
	BucketName:      "<bucket>",
	SourceAccountID: "<source-account-id>",
	BackupAccountID: "<backup-account-id>",
	Encryption:      backupstore.EncryptionSSES3,
	Lifecycle:       &backupstore.Lifecycle{BaseBackupDays: 30},
	Tenants: []backupstore.Tenant{
		{
			RoleARN: "arn:<partition>:iam::<source-account-id>:role/<archive-role>",
			Prefix:  "<namespace>/<cluster>/",
			Purpose: backupstore.PurposeArchive,
			SessionTags: &backupstore.SessionTags{
				ClusterName: "<eks-cluster>", Namespace: "<namespace>", ServiceAccount: "<service-account>",
			},
		},
		{
			RoleARN: "arn:<partition>:iam::<source-account-id>:role/<restore-role>",
			Prefix:  "<namespace>/<other-cluster>/",
			Purpose: backupstore.PurposeRestore,
		},
	},
}, pulumi.Providers(backupAccountProvider))

// b.TenantRolePolicies: role ARN -> identity policy JSON, to attach in the
// source account (a Pulumi RolePolicy, or an ACK Role's inline policy).
```

`Tenant` fields: `RoleARN` (an exact ARN, in any source account),
`Prefix` (`<namespace>/<cluster>/`, trailing slash, no glob), `Purpose`
(`PurposeArchive` or `PurposeRestore`), `SessionTags` (optional),
`ServerName` (the barman server name, default the last element of the prefix;
only the lifecycle uses it) and `AllowHeadBucket` (see below). A role may be
in several tenants (archive on its own prefix, restore on an old cluster's);
two archive tenants on one prefix are refused.

### The authority chain

Who may touch which bytes is decided in six hops. Each hop is a different
object, and an error in one is caught by the next:

1. **Cluster.** The EKS cluster, named by the Pod Identity association and by
   the session tag `eks-cluster-name`.
2. **Namespace.** The Postgres cluster's namespace, the session tag
   `kubernetes-namespace`, and the first element of the prefix.
3. **ServiceAccount.** The instance ServiceAccount, the session tag
   `kubernetes-service-account`.
4. **Pod Identity.** The association maps (cluster, namespace,
   ServiceAccount) to one IAM role; EKS stamps the three session tags on every
   session of that role.
5. **Role.** The tenant's role in the source account. Its identity policy
   (`TenantRolePolicies`) allows only its own prefix. The bucket policy allows
   it only when `aws:PrincipalArn` equals the ARN AND each given
   `aws:PrincipalTag/...` matches, so a role reused by a second ServiceAccount
   or cluster is refused.
6. **Bucket policy, prefix.** Per tenant an allow on `<bucket>/<prefix>*` and
   on `s3:ListBucket` with `s3:prefix` like `<prefix>*`, and one explicit deny
   for the role on everything else (`s3:*` with `NotResource` the bucket and
   its prefixes; `s3:ListBucket` with a `s3:prefix` outside them). A
   permissive identity policy in the source account cannot widen it.

Cross-account access needs both sides, so the bucket policy admits the source
account root narrowed by the exact role ARN (a trust principal cannot be a
role that does not exist yet).

### ListBucket, HeadBucket and barman-cloud

The tenant list is `s3:ListBucket` with `StringLike s3:prefix <prefix>*`. This
replaces today's `StringLikeIfExists` for tenants, which let a request with no
`s3:prefix` key pass.

What the barman-cloud tools call (barman `REL_3_X_master`, which the
barman-cloud plugin runs):

- Every command (`barman-cloud-backup`, `-wal-archive`, `-check-wal-archive`,
  `-restore`) opens with `verify_cloud_connectivity_and_bucket_existence()` or
  `test_connectivity()`, which calls **HeadBucket**. A 403 is turned into
  `CloudProviderError: Permission denied when accessing bucket` and the
  command stops.
- Listing is `list_objects_v2` with `Prefix=<server>/...` and a delimiter:
  prefix-scoped, so the strict list suffices for it.

HeadBucket is authorized as `s3:ListBucket` and carries **no `s3:prefix`
key**. A condition on `s3:prefix` without `IfExists`/`Null` therefore denies
it. So the strict default breaks barman-cloud until it is relaxed. The two
choices:

- `Tenant.AllowHeadBucket: true` adds one statement, `s3:ListBucket` with
  `Null s3:prefix = true`, to the bucket policy and the role policy, and keeps
  the deny for any list WITH a prefix outside the tenant's. Price: an
  **unprefixed ListObjectsV2 passes too** (it has no `s3:prefix` either), so
  the tenant can list the key names (not read the contents) of the whole
  bucket. S3 has no condition key that tells the two apart.
- Leave it off for a role that does not run barman-cloud (a role that only
  lists and reads with the AWS SDK, which needs no HeadBucket): the strict
  prefix list is then the whole story.

Barman-cloud needs the first. Set `AllowHeadBucket` on tenants whose
workload runs the plugin, and accept key-name visibility; the contents stay
separated by the object grants and the deny outside the prefix.

Archive also gets `s3:PutObjectTagging`: barman sends `--tags` with the
upload, which S3 authorizes as a tagging action. It is not a delete.

### Append-only

Archive roles can put, read back, abort their multipart uploads and tag. They
can never delete: not in the role policy, not in the bucket policy, and the
bucket policy denies `s3:DeleteObject`, `s3:DeleteObjectVersion`,
`s3:BypassGovernanceRetention`, `PutLifecycleConfiguration`,
`PutBucketPolicy`, `DeleteBucketPolicy`, `PutBucketVersioning`,
`PutReplicationConfiguration` and `PutBucketObjectLockConfiguration` to every
principal whose `aws:PrincipalAccount` is not the backup account. Versioning
keeps every overwritten object. Expiry belongs to the bucket's lifecycle,
which is S3's own action and not a principal's request.

Because the role cannot delete, **the cluster's `retentionPolicy` must be
unset** (on the `ObjectStore` and on the `Cluster`/`ScheduledBackup`). The
plugin enforces `retentionPolicy` by deleting objects; with these roles every
delete is a 403, the plugin retries, and the backup is marked failed. Let the
lifecycle below do the deleting.

## SSE-S3 mode

`Encryption: EncryptionSSES3`:

- the bucket's default encryption is `AES256`; **no KMS key or alias is
  created** (neither for the replica), and `KMSKeyARN` is empty;
- the bucket policy denies `s3:PutObject` unless `s3:x-amz-server-side-encryption`
  is `AES256`. A request without the header counts as a mismatch, so **the
  client must send it**: set `encryption: AES256` in the plugin's `ObjectStore`
  (`spec.configuration.data.encryption`, and `wal.encryption`). The bucket's
  default encryption does not satisfy the deny;
- tenant role policies carry the same `AES256` condition on `s3:PutObject` and
  no KMS statements; the replication role has no key statements.

KMS mode stays the default and is unchanged. With tenants in KMS mode the key
policy admits the tenant roles (archive: encrypt and decrypt; restore: decrypt)
instead of the writer glob.

## The tenant role policies

`Args.TenantRolePolicies(kmsKeyARN)` and the component's `TenantRolePolicies`
output return one JSON document per role ARN, to attach in the source account;
`TenantRolePolicy(TenantRolePolicyArgs)` renders one role from its tenants.
They are plain JSON, so the estate can put the same string in a Pulumi
`iam.RolePolicy` or into an ACK `Role` object.

| Purpose | Grants |
| --- | --- |
| `archive` | `s3:PutObject` (with the `AES256` condition in SSE-S3 mode), `s3:AbortMultipartUpload`, `s3:ListMultipartUploadParts`, `s3:GetObject`, `s3:PutObjectTagging` on `<bucket>/<prefix>*`; `s3:ListBucket` with `s3:prefix` like `<prefix>*` |
| `restore` | `s3:GetObject` on `<bucket>/<prefix>*`; `s3:ListBucket` limited the same way |

KMS mode adds the key. No document holds a delete action; a test enforces it.

## Lifecycle

`Args.Lifecycle` (a `Lifecycle`) replaces hand-written rules with parameters:

| Field | Default | Meaning |
| --- | --- | --- |
| `BaseBackupDays` | required | N: base backups expire after N days |
| `WALMarginDays` | 7 | WAL expires after N + margin days |
| `NoncurrentDays` | 7 | noncurrent versions expire |
| `AbortMultipartDays` | 7 | incomplete multipart uploads are aborted |
| `Transitions` | none | the optional IA / Glacier-IR tiering rule (primary only) |

`LifecycleRules` is still honoured; the generated rules are added after it
(rule ids differ from `Backstop`'s `tiering`).

**Why a margin.** A base backup's first WAL segment is written before its
`backup.info`: it is the segment that was current when the backup started. S3
lifecycle works in whole days (midnight UTC). With one age for both, the oldest
backup that still exists could have lost the WAL it needs to be consistent.
WAL kept `WALMarginDays` longer means every backup that still exists has all
of its WAL.

**Tags.** The rules select objects by tag, so nothing the plugin did not write
is ever expired. The barman-cloud plugin's `ObjectStore` applies
`spec.configuration.tags` to base backups AND WAL files and
`spec.configuration.historyTags` to timeline history files only. Set:

```yaml
spec:
  configuration:
    tags:
      cnpg-backup-retention: managed   # LifecycleTagKey / LifecycleTagManage
    historyTags:
      cnpg-backup-retention: keep      # LifecycleTagKeep: no rule matches it
```

History files are tiny and PITR across a timeline switch needs them for as
long as any backup spans it, so they never expire here. (`tags` cannot tell a
base backup from WAL, so the N versus N+margin split is by prefix.)

**Rules.** For each archive tenant: `base-backups/<prefix><server>/`
(prefix `<tenant prefix><server>/base/` plus the managed tag, N days) and
`wal/<prefix><server>/` (`.../wals/` + tag, N + margin days), where `<server>`
is `Tenant.ServerName` (the plugin's `serverName`; CNPG defaults it to the
cluster name). Without archive tenants one rule `managed` covers the whole
bucket by the tag with the longer age, because without a prefix the two
cannot be told apart. Always, one rule `housekeeping`: noncurrent versions
expire after `NoncurrentDays`, incomplete multipart uploads are aborted after
`AbortMultipartDays`, and expired object delete markers are removed. With
`Transitions`, `tier-storage-class`.

A lifecycle expiry of a current object on a versioned bucket adds a delete
marker; the noncurrent rule removes the version `NoncurrentDays` later, and the
delete-marker rule removes the marker once nothing is left behind it. Only the
lifecycle does this: it is S3's own action, so the delete denies do not bind it.

## The locked replica

`Replica.ObjectLockGovernance` creates the replica bucket with S3 Object Lock,
a **GOVERNANCE** default retention of `Replica.ObjectLockDays` (default
`Lifecycle.BaseBackupDays`), the delete-protection bucket policy (the global
denies above; `s3:BypassGovernanceRetention` is denied outside the backup
account) and, with `Lifecycle`, the lifecycle rules (expiry and housekeeping,
no tiering). Equal to N, the lock ends when base backups expire, and WAL
outlives it.

**Object Lock can only be enabled when a bucket is created.** The option is
therefore explicit, and it does not change an existing replica in place: set
on a replica Pulumi already created without it, `objectLockEnabled` is a
change that forces replacing the bucket, and a protected bucket (the default)
refuses the preview. To adopt it, create a new replica bucket under a new
name, let replication fill it, then retire the old one on your own schedule.
It is exclusive with `ObjectLockDays` (COMPLIANCE on both buckets).

## What stays the same

A caller that sets none of `Encryption`, `Tenants`, `DenyDestructive`,
`Lifecycle` and `Replica.ObjectLockGovernance` gets the same resources and the
same policy bytes as before: a golden test over a minimal and a full
configuration pins every child's inputs. `RolePolicy`/`NewRole` are untouched
(their `StringLikeIfExists` list is the legacy behaviour).
