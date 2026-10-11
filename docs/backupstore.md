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
| `WriterRoleName` | `string` | The role in the source account the bucket and key policies admit. A glob (`*`, `?`) is allowed. Required. |
| `WriterObjectActions` | `[]string` | Replaces the writer's object actions. Nil grants put, get, delete and the two multipart actions; an append-only log passes put and get. Empty is refused. |
| `ReaderRoleName` | `string` | One more role in the source account, read-only (get, list, decrypt). An exact name; a glob is refused. |
| `ListerRoleNames` | `[]string` | Roles that may list the bucket (and the replica) and nothing else: no object, no decrypt. Exact names; a glob or empty name is refused. |
| `KeyDescription` | `string` | The KMS key's description. Required, and part of the key's state: a change is an in-place update. |
| `Tags` | `map[string]string` | Tags on the bucket, for example compliance tags. The component adds none. |
| `LifecycleRules` | `s3.BucketLifecycleConfigurationV2RuleArray` | The lifecycle configuration; at least one rule. `Backstop` builds one whole-bucket rule. |
| `ObjectLockDays` | `int` | Above zero: Object Lock with a COMPLIANCE default retention of that many days, on the bucket and the replica. Negative is refused. |
| `Replica` | `*Replica` | Replicates to another region; see below. |
| `Protect` | `*bool` | Protect the buckets and the KMS keys. Nil means true; point it at false only to retire a bucket. |
| `LegacyTopLevel` | `bool` | Alias every child from the URN it has as a loose resource directly under the stack. For adopting existing resources. |

`Replica` takes `Provider` (the AWS provider of the replica's region, created
by the caller so it keeps its own URN), `BucketName`, `KeyDescription`,
`RoleName` (the replication role's IAM name) and an optional
`RolePermissionsBoundary` (`pulumi.StringInput`; nil sets none). The
replica gets a bucket policy only when `ListerRoleNames` is not empty.

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
| key | `aws:kms/key:Key` | `<b>-kms` | always (protected, retained on delete) |
| key alias | `aws:kms/alias:Alias` | `<b>-kms-alias` | always |
| bucket | `aws:s3/bucket:Bucket` | `<b>` | always (protected) |
| versioning | `aws:s3/bucketVersioningV2:BucketVersioningV2` | `<b>-versioning` | always |
| encryption | `aws:s3/bucketServerSideEncryptionConfigurationV2:BucketServerSideEncryptionConfigurationV2` | `<b>-encryption` | always |
| lifecycle | `aws:s3/bucketLifecycleConfigurationV2:BucketLifecycleConfigurationV2` | `<b>-lifecycle` | always |
| object lock | `aws:s3/bucketObjectLockConfigurationV2:BucketObjectLockConfigurationV2` | `<b>-object-lock` | `ObjectLockDays > 0` |
| public access block | `aws:s3/bucketPublicAccessBlock:BucketPublicAccessBlock` | `<b>-public-access` | always |
| bucket policy | `aws:s3/bucketPolicy:BucketPolicy` | `<b>-policy` | always |
| replica key | `aws:kms/key:Key` | `<r>-kms` | `Replica` (protected, retained on delete) |
| replica bucket | `aws:s3/bucketV2:BucketV2` | `<r>` | `Replica` (protected) |
| replica versioning | `aws:s3/bucketVersioningV2:BucketVersioningV2` | `<r>-versioning` | `Replica` |
| replica public access block | `aws:s3/bucketPublicAccessBlock:BucketPublicAccessBlock` | `<r>-public-access` | `Replica` |
| replica policy | `aws:s3/bucketPolicy:BucketPolicy` | `<r>-policy` | `Replica` and `ListerRoleNames` |
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

`BucketName`, `BucketARN`, `KMSKeyARN` (`pulumi.StringOutput`). The component
exports no stack output of its own: the caller names those.

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
