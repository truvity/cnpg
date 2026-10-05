package backupstore

import (
	"errors"
	"fmt"
	"strings"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/iam"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/kms"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/s3"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// TypeToken is the Pulumi type of the component.
const TypeToken = "truvity:cnpg/aws:BackupStore"

// FormerTypeToken is the type the component had while it lived in
// truvity/k8s as pkg/aws/backupbucket. New aliases the component from it, so
// a state written by that version adopts with no replace.
const FormerTypeToken = "truvity:k8s/aws:BackupBucket"

// DefaultPartition is the AWS partition used in ARNs when Args.Partition is
// empty.
const DefaultPartition = "aws"

// Replica configures the same-account replica in another region.
type Replica struct {
	// Provider is the AWS provider of the replica's region, in the backup
	// account. The caller creates it, so it keeps its own URN.
	Provider pulumi.ProviderResource
	// BucketName is the replica bucket's name.
	BucketName string
	// KeyDescription is the description of the replica's KMS key.
	KeyDescription string
	// RoleName is the name of the IAM role S3 assumes to replicate.
	RoleName string
	// RolePermissionsBoundary is the ARN of the permissions boundary of
	// that role, when the account requires one. Nil sets none.
	RolePermissionsBoundary pulumi.StringInput
}

// Args configures the component.
type Args struct {
	// BucketName is the bucket's name. It is also the stem of every child's
	// logical name.
	BucketName string
	// SourceAccountID is the account whose roles write the backups.
	SourceAccountID string
	// BackupAccountID is the account the bucket lives in. Only principals in
	// it may delete an object version.
	BackupAccountID string
	// Partition is the AWS partition in ARNs. Empty is DefaultPartition.
	Partition string

	// WriterRoleName is the role name, inside the source account, that the
	// bucket and key policies admit. It may be a glob ("*" and "?").
	WriterRoleName string
	// WriterObjectActions replaces the object actions the bucket policy
	// grants the writer. Nil grants put, get, delete and the multipart pair.
	// An append-only log names put and get only, so no delete is admitted at
	// the resource gate whatever the writer's own policy later says.
	WriterObjectActions []string
	// ReaderRoleName is one more role in the source account, admitted
	// read-only: get, list and decrypt. An exact name, never a glob.
	ReaderRoleName string
	// ListerRoleNames are roles in the source account that may list the
	// bucket (and the replica) and nothing else: no object, no decrypt.
	// Exact names, never globs.
	ListerRoleNames []string

	// KeyDescription is the description of the bucket's KMS key.
	KeyDescription string
	// Tags are applied to the bucket (compliance tags, for example).
	Tags map[string]string
	// LifecycleRules is the bucket's lifecycle configuration. At least one
	// rule. Backstop builds the common shape.
	LifecycleRules s3.BucketLifecycleConfigurationV2RuleArray
	// ObjectLockDays, when above zero, creates the bucket (and the replica)
	// with Object Lock and a COMPLIANCE default retention of that many days.
	ObjectLockDays int
	// Replica, when set, replicates the bucket to another region.
	Replica *Replica

	// Protect marks the buckets and the KMS keys protected, so a preview
	// that would delete one refuses. Nil means true: set it to a pointer to
	// false only to retire a bucket.
	Protect *bool
	// LegacyTopLevel makes every child carry the aliases of the URN it has
	// when registered directly under the stack (no parent): the current type
	// and name, and for a child whose provider SDK renames its type, the
	// former type as well. Set it when adopting resources created before
	// they were wrapped in this component.
	LegacyTopLevel bool
}

// BackstopArgs shapes the lifecycle rule Backstop builds.
type BackstopArgs struct {
	// ExpireDays expires current objects after that many days. Zero omits
	// the expiration.
	ExpireDays int
	// NoncurrentDays expires noncurrent versions after that many days.
	NoncurrentDays int
	// Transitions moves objects to a cheaper storage class.
	Transitions []Transition
}

// Transition moves objects to StorageClass after Days.
type Transition struct {
	Days         int
	StorageClass string
}

// Backstop returns one enabled whole-bucket lifecycle rule with the id
// "tiering", for a caller whose backup tool does the real retention.
func Backstop(a BackstopArgs) s3.BucketLifecycleConfigurationV2RuleArray {
	rule := &s3.BucketLifecycleConfigurationV2RuleArgs{
		Id:     pulumi.String("tiering"),
		Status: pulumi.String("Enabled"),
		Filter: &s3.BucketLifecycleConfigurationV2RuleFilterArgs{Prefix: pulumi.String("")},
		NoncurrentVersionExpiration: &s3.BucketLifecycleConfigurationV2RuleNoncurrentVersionExpirationArgs{
			NoncurrentDays: pulumi.Int(a.NoncurrentDays),
		},
	}

	if len(a.Transitions) > 0 {
		var ts s3.BucketLifecycleConfigurationV2RuleTransitionArray
		for _, t := range a.Transitions {
			ts = append(ts, &s3.BucketLifecycleConfigurationV2RuleTransitionArgs{
				Days:         pulumi.Int(t.Days),
				StorageClass: pulumi.String(t.StorageClass),
			})
		}

		rule.Transitions = ts
	}

	if a.ExpireDays > 0 {
		rule.Expiration = &s3.BucketLifecycleConfigurationV2RuleExpirationArgs{Days: pulumi.Int(a.ExpireDays)}
	}

	return s3.BucketLifecycleConfigurationV2RuleArray{rule}
}

// BackupStore is the component.
type BackupStore struct {
	pulumi.ResourceState

	// BucketName is the bucket's name.
	BucketName pulumi.StringOutput
	// BucketARN is the bucket's ARN.
	BucketARN pulumi.StringOutput
	// KMSKeyARN is the ARN of the bucket's key.
	KMSKeyARN pulumi.StringOutput
}

func (a *Args) partition() string {
	if a.Partition == "" {
		return DefaultPartition
	}

	return a.Partition
}

func (a *Args) protect() bool { return a.Protect == nil || *a.Protect }

// Validate reports every problem with args at once, or returns nil.
func (a *Args) Validate() error {
	var errs []error

	required := func(field, v string) {
		if v == "" {
			errs = append(errs, fmt.Errorf("args: %s is empty", field))
		}
	}

	required("BucketName", a.BucketName)
	required("SourceAccountID", a.SourceAccountID)
	required("BackupAccountID", a.BackupAccountID)
	required("WriterRoleName", a.WriterRoleName)
	required("KeyDescription", a.KeyDescription)

	if len(a.LifecycleRules) == 0 {
		errs = append(errs, errors.New("args: LifecycleRules is empty"))
	}

	if a.ObjectLockDays < 0 {
		errs = append(errs, fmt.Errorf("args: ObjectLockDays is %d, want 0 or more", a.ObjectLockDays))
	}

	if a.ReaderRoleName != "" && strings.ContainsAny(a.ReaderRoleName, "*?") {
		errs = append(errs, fmt.Errorf("args: ReaderRoleName %q is a glob; it must be an exact name", a.ReaderRoleName))
	}

	for i, r := range a.ListerRoleNames {
		switch {
		case r == "":
			errs = append(errs, fmt.Errorf("args: ListerRoleNames[%d] is empty", i))
		case strings.ContainsAny(r, "*?"):
			errs = append(errs, fmt.Errorf("args: ListerRoleNames[%d] %q is a glob; it must be an exact name", i, r))
		}
	}

	if a.WriterObjectActions != nil && len(a.WriterObjectActions) == 0 {
		errs = append(errs, errors.New("args: WriterObjectActions is empty; leave it nil for the default set"))
	}

	if r := a.Replica; r != nil {
		if r.Provider == nil {
			errs = append(errs, errors.New("args: Replica.Provider is nil"))
		}

		required("Replica.BucketName", r.BucketName)
		required("Replica.KeyDescription", r.KeyDescription)
		required("Replica.RoleName", r.RoleName)

		if r.BucketName != "" && r.BucketName == a.BucketName {
			errs = append(errs, errors.New("args: Replica.BucketName equals BucketName"))
		}
	}

	return errors.Join(errs...)
}

// New registers the component and its children. It returns an error,
// registering nothing, when args.Validate does.
//
// Pass the AWS provider of the backup account with pulumi.Providers(p) so
// every child uses it.
func New(ctx *pulumi.Context, name string, args *Args, opts ...pulumi.ResourceOption) (*BackupStore, error) {
	if args == nil {
		return nil, errors.New("backupstore: args is nil")
	}

	if err := args.Validate(); err != nil {
		return nil, fmt.Errorf("backupstore %s: %w", name, err)
	}

	comp := &BackupStore{}
	opts = append(opts, pulumi.Aliases([]pulumi.Alias{{Type: pulumi.String(FormerTypeToken)}}))
	if err := ctx.RegisterComponentResource(TypeToken, name, comp, opts...); err != nil {
		return nil, err
	}

	b := &builder{ctx: ctx, comp: comp, args: args}

	if err := b.primary(); err != nil {
		return nil, err
	}

	if args.Replica != nil {
		if err := b.replica(); err != nil {
			return nil, err
		}
	}

	comp.BucketName = b.bucket.Bucket
	comp.BucketARN = b.bucket.Arn
	comp.KMSKeyARN = b.key.Arn

	if err := ctx.RegisterResourceOutputs(comp, pulumi.Map{
		"bucketName": comp.BucketName,
		"bucketArn":  comp.BucketARN,
		"kmsKeyArn":  comp.KMSKeyARN,
	}); err != nil {
		return nil, err
	}

	return comp, nil
}

type builder struct {
	ctx  *pulumi.Context
	comp *BackupStore
	args *Args

	bucket *s3.Bucket
	key    *kms.Key
}

// childOpts is what every child registers with: the component as parent,
// the caller's extra options, and the adoption aliases when asked for.
// sdkType is the former type the provider SDK itself aliases this child's
// type from, or "".
func (b *builder) childOpts(sdkTypes []string, extra ...pulumi.ResourceOption) []pulumi.ResourceOption {
	out := append([]pulumi.ResourceOption{pulumi.Parent(b.comp)}, extra...)

	if b.args.LegacyTopLevel {
		out = append(out, pulumi.Aliases(legacyAliases(sdkTypes)))
	}

	return out
}

// legacyAliases is the loose-under-the-stack alias, plus one per former
// type. The SDK declares its own type aliases without a parent, which
// resolves them under the NEW parent; a state that still holds the former
// type at the top level needs the type and no parent together.
func legacyAliases(formerTypes []string) []pulumi.Alias {
	out := []pulumi.Alias{{NoParent: pulumi.Bool(true)}}

	for _, t := range formerTypes {
		out = append(out, pulumi.Alias{Type: pulumi.String(t), NoParent: pulumi.Bool(true)})
	}

	return out
}

func (b *builder) protectOpt() []pulumi.ResourceOption {
	if b.args.protect() {
		return []pulumi.ResourceOption{pulumi.Protect(true)}
	}

	return nil
}

// Former types the AWS SDK aliases its own resources from. A test pins this
// list against what the SDK registers, so an SDK that adds one fails it.
const (
	formerBucket     = "aws:s3/bucketV2:BucketV2"
	formerVersioning = "aws:s3/bucketVersioningV2:BucketVersioningV2"
	formerEncryption = "aws:s3/bucketServerSideEncryptionConfigurationV2:BucketServerSideEncryptionConfigurationV2"
	formerLifecycle  = "aws:s3/bucketLifecycleConfigurationV2:BucketLifecycleConfigurationV2"
	formerObjectLock = "aws:s3/bucketObjectLockConfigurationV2:BucketObjectLockConfigurationV2"
)

func (b *builder) primary() error {
	a := b.args
	name := a.BucketName
	part := a.partition()

	// RetainOnDelete: kms:ScheduleKeyDeletion is denied in the accounts this
	// is built for (key destruction is a break-glass act), so Pulumi drops a
	// key from state instead of deleting it.
	key, err := kms.NewKey(b.ctx, name+"-kms", &kms.KeyArgs{
		Description:       pulumi.String(a.KeyDescription),
		EnableKeyRotation: pulumi.Bool(true),
		Policy:            pulumi.String(buildKeyPolicy(part, a.SourceAccountID, a.BackupAccountID, a.WriterRoleName, a.ReaderRoleName)),
	}, b.childOpts(nil, append(b.protectOpt(), pulumi.RetainOnDelete(true))...)...)
	if err != nil {
		return fmt.Errorf("create KMS key: %w", err)
	}

	b.key = key

	if _, err := kms.NewAlias(b.ctx, name+"-kms-alias", &kms.AliasArgs{
		Name:        pulumi.String("alias/" + name),
		TargetKeyId: key.KeyId,
	}, b.childOpts(nil)...); err != nil {
		return fmt.Errorf("create KMS alias: %w", err)
	}

	bucketArgs := &s3.BucketArgs{
		Bucket: pulumi.String(name),
		Tags:   pulumi.ToStringMap(a.Tags),
	}
	if a.ObjectLockDays > 0 {
		bucketArgs.ObjectLockEnabled = pulumi.Bool(true)
	}

	// The SDK aliases this type from BucketV2 twice; one alias resolves to
	// the same URN, so the legacy set below carries it once.
	bucket, err := s3.NewBucket(b.ctx, name, bucketArgs, b.childOpts([]string{formerBucket}, b.protectOpt()...)...)
	if err != nil {
		return fmt.Errorf("create backup bucket: %w", err)
	}

	b.bucket = bucket

	if _, err := s3.NewBucketVersioningV2(b.ctx, name+"-versioning", &s3.BucketVersioningV2Args{
		Bucket: bucket.ID(),
		VersioningConfiguration: &s3.BucketVersioningV2VersioningConfigurationArgs{
			Status: pulumi.String("Enabled"),
		},
	}, b.childOpts([]string{formerVersioning})...); err != nil {
		return fmt.Errorf("enable bucket versioning: %w", err)
	}

	if _, err := s3.NewBucketServerSideEncryptionConfigurationV2(b.ctx, name+"-encryption", &s3.BucketServerSideEncryptionConfigurationV2Args{
		Bucket: bucket.ID(),
		Rules: s3.BucketServerSideEncryptionConfigurationV2RuleArray{
			&s3.BucketServerSideEncryptionConfigurationV2RuleArgs{
				ApplyServerSideEncryptionByDefault: &s3.BucketServerSideEncryptionConfigurationV2RuleApplyServerSideEncryptionByDefaultArgs{
					SseAlgorithm:   pulumi.String("aws:kms"),
					KmsMasterKeyId: key.Arn,
				},
			},
		},
	}, b.childOpts([]string{formerEncryption})...); err != nil {
		return fmt.Errorf("configure bucket encryption: %w", err)
	}

	if _, err := s3.NewBucketLifecycleConfigurationV2(b.ctx, name+"-lifecycle", &s3.BucketLifecycleConfigurationV2Args{
		Bucket: bucket.ID(),
		Rules:  a.LifecycleRules,
	}, b.childOpts([]string{formerLifecycle})...); err != nil {
		return fmt.Errorf("configure bucket lifecycle: %w", err)
	}

	if a.ObjectLockDays > 0 {
		if _, err := s3.NewBucketObjectLockConfigurationV2(b.ctx, name+"-object-lock", &s3.BucketObjectLockConfigurationV2Args{
			Bucket: bucket.ID(),
			Rule: &s3.BucketObjectLockConfigurationV2RuleArgs{
				DefaultRetention: &s3.BucketObjectLockConfigurationV2RuleDefaultRetentionArgs{
					Mode: pulumi.String("COMPLIANCE"),
					Days: pulumi.Int(a.ObjectLockDays),
				},
			},
		}, b.childOpts([]string{formerObjectLock})...); err != nil {
			return fmt.Errorf("configure object lock: %w", err)
		}
	}

	if _, err := s3.NewBucketPublicAccessBlock(b.ctx, name+"-public-access", blockPublicAccess(bucket.ID()), b.childOpts(nil)...); err != nil {
		return fmt.Errorf("configure bucket public access block: %w", err)
	}

	policy := buildBucketPolicy(bucketPolicyParams{
		Partition:       part,
		BucketName:      name,
		SourceAccountID: a.SourceAccountID,
		BackupAccountID: a.BackupAccountID,
		WriterRole:      a.WriterRoleName,
		ReaderRole:      a.ReaderRoleName,
		ListerRoles:     a.ListerRoleNames,
		ObjectActions:   a.WriterObjectActions,
	})

	if _, err := s3.NewBucketPolicy(b.ctx, name+"-policy", &s3.BucketPolicyArgs{
		Bucket: bucket.ID(),
		Policy: pulumi.String(policy),
	}, b.childOpts(nil)...); err != nil {
		return fmt.Errorf("attach bucket policy: %w", err)
	}

	return nil
}

func blockPublicAccess(bucket pulumi.StringInput) *s3.BucketPublicAccessBlockArgs {
	return &s3.BucketPublicAccessBlockArgs{
		Bucket:                bucket,
		BlockPublicAcls:       pulumi.Bool(true),
		IgnorePublicAcls:      pulumi.Bool(true),
		BlockPublicPolicy:     pulumi.Bool(true),
		RestrictPublicBuckets: pulumi.Bool(true),
	}
}

// replica builds the same-account copy: its own key, bucket, versioning,
// public access block, a policy when something may list it, the replication
// role and its policy, and the replication configuration on the primary.
func (b *builder) replica() error {
	a := b.args
	r := a.Replica
	name := a.BucketName
	rname := r.BucketName
	part := a.partition()
	ropts := func(sdkTypes []string, extra ...pulumi.ResourceOption) []pulumi.ResourceOption {
		return b.childOpts(sdkTypes, append([]pulumi.ResourceOption{pulumi.Provider(r.Provider)}, extra...)...)
	}

	replicaKey, err := kms.NewKey(b.ctx, rname+"-kms", &kms.KeyArgs{
		Description:       pulumi.String(r.KeyDescription),
		EnableKeyRotation: pulumi.Bool(true),
	}, ropts(nil, append(b.protectOpt(), pulumi.RetainOnDelete(true))...)...)
	if err != nil {
		return fmt.Errorf("create replica KMS key: %w", err)
	}

	replicaArgs := &s3.BucketV2Args{Bucket: pulumi.String(rname)}
	if a.ObjectLockDays > 0 {
		// Replicating locked objects requires Object Lock on the destination.
		replicaArgs.ObjectLockEnabled = pulumi.Bool(true)
	}

	replica, err := s3.NewBucketV2(b.ctx, rname, replicaArgs, ropts(nil, b.protectOpt()...)...)
	if err != nil {
		return fmt.Errorf("create replica bucket: %w", err)
	}

	if _, err := s3.NewBucketVersioningV2(b.ctx, rname+"-versioning", &s3.BucketVersioningV2Args{
		Bucket: replica.ID(),
		VersioningConfiguration: &s3.BucketVersioningV2VersioningConfigurationArgs{
			Status: pulumi.String("Enabled"),
		},
	}, ropts([]string{formerVersioning})...); err != nil {
		return fmt.Errorf("replica versioning: %w", err)
	}

	if _, err := s3.NewBucketPublicAccessBlock(b.ctx, rname+"-public-access", blockPublicAccess(replica.ID()), ropts(nil)...); err != nil {
		return fmt.Errorf("replica public access block: %w", err)
	}

	// A resource policy on the replica only when something in the source
	// account is meant to list it. It carries the delete-version deny, so
	// the replica's immutability reads the same as the primary's. The deny
	// binds principals outside the backup account only: replication writes
	// from inside it, and lifecycle expiry is the service's own action.
	if len(a.ListerRoleNames) > 0 {
		doc, err := buildReplicaPolicy(part, rname, a.SourceAccountID, a.BackupAccountID, a.ListerRoleNames)
		if err != nil {
			return fmt.Errorf("marshal replica bucket policy: %w", err)
		}

		if _, err := s3.NewBucketPolicy(b.ctx, rname+"-policy", &s3.BucketPolicyArgs{
			Bucket: replica.ID(),
			Policy: pulumi.String(doc),
		}, ropts(nil)...); err != nil {
			return fmt.Errorf("attach replica bucket policy: %w", err)
		}
	}

	roleArgs := &iam.RoleArgs{
		Name:             pulumi.String(r.RoleName),
		AssumeRolePolicy: pulumi.String(replicationTrust),
	}
	if r.RolePermissionsBoundary != nil {
		roleArgs.PermissionsBoundary = r.RolePermissionsBoundary
	}

	role, err := iam.NewRole(b.ctx, name+"-replication-role", roleArgs, b.childOpts(nil)...)
	if err != nil {
		return fmt.Errorf("replication role: %w", err)
	}

	policy := pulumi.All(b.bucket.Arn, replica.Arn, b.key.Arn, replicaKey.Arn).ApplyT(func(vs []any) (string, error) {
		return buildReplicationPolicy(vs[0].(string), vs[1].(string), vs[2].(string), vs[3].(string), a.ObjectLockDays > 0)
	}).(pulumi.StringOutput)

	if _, err := iam.NewRolePolicy(b.ctx, name+"-replication-policy", &iam.RolePolicyArgs{
		Role:   role.Name,
		Policy: policy,
	}, b.childOpts(nil)...); err != nil {
		return fmt.Errorf("replication policy: %w", err)
	}

	if _, err := s3.NewBucketReplicationConfig(b.ctx, name+"-replication", &s3.BucketReplicationConfigArgs{
		Bucket: b.bucket.ID(),
		Role:   role.Arn,
		Rules: s3.BucketReplicationConfigRuleArray{
			&s3.BucketReplicationConfigRuleArgs{
				Id:     pulumi.String("crr"),
				Status: pulumi.String("Enabled"),
				Filter: &s3.BucketReplicationConfigRuleFilterArgs{Prefix: pulumi.String("")},
				DeleteMarkerReplication: &s3.BucketReplicationConfigRuleDeleteMarkerReplicationArgs{
					Status: pulumi.String("Enabled"),
				},
				SourceSelectionCriteria: &s3.BucketReplicationConfigRuleSourceSelectionCriteriaArgs{
					SseKmsEncryptedObjects: &s3.BucketReplicationConfigRuleSourceSelectionCriteriaSseKmsEncryptedObjectsArgs{
						Status: pulumi.String("Enabled"),
					},
				},
				Destination: &s3.BucketReplicationConfigRuleDestinationArgs{
					Bucket:       replica.Arn,
					StorageClass: pulumi.String("STANDARD_IA"),
					EncryptionConfiguration: &s3.BucketReplicationConfigRuleDestinationEncryptionConfigurationArgs{
						ReplicaKmsKeyId: replicaKey.Arn,
					},
				},
			},
		},
	}, b.childOpts(nil)...); err != nil {
		return fmt.Errorf("replication config: %w", err)
	}

	return nil
}
