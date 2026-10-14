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
| `SingleProject` | `bool` | The bucket holds ONE project (the standard shape, a bucket per project per environment): every tenant may list the whole bucket, which barman-cloud's HeadBucket needs. Requires `Tenants`. See "One bucket per project". |
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
separate the Postgres clusters of a project. The standard layout is one bucket
per project per environment ("One bucket per project"), so the example below
sets `SingleProject`; every cluster still has its own prefix and its own
`archive` and `restore` roles. Existing callers set none of
this and see no change (see "What stays the same").

```go
b, err := backupstore.New(ctx, "backup", &backupstore.Args{
	BucketName:      "<bucket>",
	SourceAccountID: "<source-account-id>",
	BackupAccountID: "<backup-account-id>",
	Encryption:      backupstore.EncryptionSSES3,
	SingleProject:   true, // one bucket per project
	Lifecycle:       &backupstore.Lifecycle{RetentionDays: 30},
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
A role may be
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

### One bucket per project

**The standard shape is a bucket per project per environment**, with its own
replica in the second region. A project is the set of CNPG clusters of one
team or namespace. Inside the bucket every cluster keeps its own
`<namespace>/<cluster>/` prefix and its own `archive` and `restore` tenant
roles. Why a bucket per project:

- **HeadBucket.** barman-cloud opens every command (`barman-cloud-backup`,
  `-wal-archive`, `-check-wal-archive`, `-restore`) with a HeadBucket, which S3
  authorizes as `s3:ListBucket` and which carries **no `s3:prefix` key**. A
  prefix-limited list therefore returns 403 and the command stops
  (`Permission denied when accessing bucket`). S3 has no condition that tells
  HeadBucket from an unprefixed ListObjects. In a bucket that holds only one
  project's objects the unprefixed list is harmless: it shows the key names of
  that project's own clusters.
- **Blast radius.** A wrong policy, a lost key, or a compromised role reaches
  one project's bucket, not the estate's.
- **Retention per project.** One expiry age per bucket (see "Lifecycle"), so a
  project picks its own.
- **Lock and deletion.** Object Lock, the replica, and the retirement of a
  bucket are per project: a project can be locked, replicated or retired
  without touching another.

**API.** `Args.SingleProject` is a bucket-level flag, not a per-tenant one. It
was chosen over keeping a per-tenant `AllowHeadBucket` as the default because
the property it states is about the bucket (it holds one project), not about a
role: a per-tenant option invites a half-open bucket in which one tenant lists
everything and its neighbour does not, and it needed a `Null`-condition list,
a second deny and a role-policy twin per tenant. The flag does one thing: every
tenant gets `s3:ListBucket` on the bucket without an `s3:prefix` condition (in
the bucket policy and in the role policy), and the list-outside-prefix deny is
dropped. Everything else is unchanged: put and get only on the tenant's own
prefix, and the explicit deny of everything else for the role (`s3:*` with
`NotResource` the bucket and its prefixes). Without the flag the list stays
limited to the prefix (`StringLike s3:prefix <prefix>*`, no `IfExists`), for a
bucket shared by several projects whose roles do not run barman-cloud.

What a tenant can see in a per-project bucket: the key names of the other
clusters of the same project. What it cannot do: read, write, tag or delete any
object outside its prefix, and no tenant can delete anything.

### A bucket set per environment: `NewProjectBuckets`

`ProjectBucketsArgs` builds the standard shape for a list of projects in one
environment, so a consumer passes facts and not the policy wiring:

```go
stores, err := backupstore.NewProjectBuckets(ctx, &backupstore.ProjectBucketsArgs{
    BucketPrefix:       "<stem>",        // required: <stem>-<project>-<env>-<region>
    Env:                "<env>",
    Region:             "<region>",
    SessionClusterName: "<eks-cluster>", // the eks-cluster-name session tag
    SourceAccountID:    "<source-account-id>",
    BackupAccountID:    "<backup-account-id>",
    Lifecycle:          backupstore.Lifecycle{RetentionDays: 30},
    Projects: []backupstore.Project{{
        Name:     "<project>",
        Clusters: []backupstore.ProjectCluster{{Name: "<cluster>", Namespace: "<namespace>"}},
    }},
    Tags:            func(project string) map[string]string { return nil },
    ResourceOptions: []pulumi.ResourceOption{pulumi.Providers(backupAccountProvider)},
})
```

Per project it makes one `BackupStore`: SSE-S3 (`Encryption` empty), `SingleProject`,
`DenyDestructive`, and two tenants per cluster on `<namespace>/<cluster>/`: the
archive role (session tags: the EKS cluster, the namespace and the ServiceAccount,
which is the cluster's name unless `ProjectCluster.ServiceAccount` says otherwise)
and the restore role (no session tags, since its ServiceAccount may move to a
drill namespace). Role and bucket names come from `pkg/backupnaming`, the same
rules the `cnpg-backup-access` chart's `global.rolePrefix` follows. `BuildProjectBuckets`
returns the validated `Args` without registering anything.

**Pulumi identity.** By default each component is named after its bucket and
registered under the options you pass, the identity of
`New(ctx, bucketName, args, pulumi.Providers(p))` called once per bucket: a
caller that registered its buckets that way keeps every URN. A test pins this
(`testdata/projects/urns.json`, and an equality check against per-bucket `New`).
A caller with other names, parents or history passes them:
`ResourceName(bucketName)` renames the component, `ResourceOptions` and
`ProjectOptions(project, bucketName)` add parents, providers and aliases, and
`LegacyTopLevel` and `Protect` pass through to every `Args`.

`pkg/backupnaming` (no cloud SDK) holds the naming rules for callers that need only
the strings: `ProjectBucketName(prefix, project, env, region)` (the prefix is
required; the result is checked by `ValidateBucketName`), `ClusterPrefix(namespace,
cluster)` (`<namespace>/<cluster>/`), `RolePrefix(env)` (`<env>-pg`),
`ArchiveRoleName` and `RestoreRoleName` (`<env>-pg-<cluster>-archive|restore`),
`RestoreServiceAccount(cluster)` and `ServerName(project, generation)`
(`<project>-pg-<generation>`).

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
`TenantRolePolicy(TenantRolePolicyArgs)` renders one role from its tenants
(set `SingleProject` there to match the bucket).
They are plain JSON, so the estate can put the same string in a Pulumi
`iam.RolePolicy` or into an ACK `Role` object.

| Purpose | Grants |
| --- | --- |
| `archive` | `s3:PutObject` (with the `AES256` condition in SSE-S3 mode), `s3:AbortMultipartUpload`, `s3:ListMultipartUploadParts`, `s3:GetObject`, `s3:PutObjectTagging` on `<bucket>/<prefix>*`; `s3:ListBucket` with `s3:prefix` like `<prefix>*` (the whole bucket with `SingleProject`) |
| `restore` | `s3:GetObject` on `<bucket>/<prefix>*`; `s3:ListBucket`, limited the same way |

KMS mode adds the key. No document holds a delete action; a test enforces it.

## Lifecycle

`Args.Lifecycle` (a `Lifecycle`) replaces hand-written rules with parameters:

| Field | Default | Meaning |
| --- | --- | --- |
| `RetentionDays` | required | managed objects (base backups and WAL) expire after this many days |
| `NoncurrentDays` | 7 | noncurrent versions expire |
| `AbortMultipartDays` | 7 | incomplete multipart uploads are aborted |
| `Transitions` | none | the optional IA / Glacier-IR tiering rule (primary only) |

`LifecycleRules` is still honoured; the generated rules are added after it
(rule ids differ from `Backstop`'s `tiering`).

**One age per bucket.** There is a single expiry age for everything the plugin
tags `managed`: no WAL margin and no per-path (base versus WAL) rules. The WAL
a base backup needs is archived *after* the backup starts, so it is never older
than the backup. S3 lifecycle counts whole days and expires an object at
midnight UTC once the day count has elapsed, so two objects created on the same
UTC day expire in the same sweep. With the backup scheduled away from midnight
UTC (for example 02:00), a backup and its WAL are created on the same UTC day
and expire together; the oldest backup that still exists keeps its WAL.

**Requirement: keep the backup schedule away from midnight UTC.** A backup
that starts shortly before 00:00 UTC can have its WAL written on the next day
and outlive it, and one that runs across midnight can lose its first WAL
segment a day early. Schedule the `ScheduledBackup` at a time such as `02:00`
UTC, and keep the run well inside that day.

**Tags.** The rule selects objects by tag, so nothing the plugin did not write
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
long as any backup spans it, so they never expire here.

**Rules.** `managed`: expiry at `RetentionDays` of objects tagged
`cnpg-backup-retention=managed`, bucket-wide (a bucket is one project's).
`housekeeping`: noncurrent versions expire after `NoncurrentDays`, incomplete
multipart uploads are aborted after `AbortMultipartDays`, and expired object
delete markers are removed. With `Transitions`, `tier-storage-class`. The
replica gets `managed` and `housekeeping` with the same numbers.

A lifecycle expiry of a current object on a versioned bucket adds a delete
marker; the noncurrent rule removes the version `NoncurrentDays` later, and the
delete-marker rule removes the marker once nothing is left behind it. Only the
lifecycle does this: it is S3's own action, so the delete denies do not bind it.

## The locked replica

`Replica.ObjectLockGovernance` creates the replica bucket with S3 Object Lock,
a **GOVERNANCE** default retention of `Replica.ObjectLockDays` (default
`Lifecycle.RetentionDays`), the delete-protection bucket policy (the global
denies above; `s3:BypassGovernanceRetention` is denied outside the backup
account) and, with `Lifecycle`, the lifecycle rules (expiry and housekeeping,
no tiering). Equal to the retention, the lock ends when the objects expire.

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
