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

// DefaultPartition is the AWS partition used in ARNs when Args.Partition is
// empty.
const DefaultPartition = "aws"

// Encryption is how the bucket encrypts objects at rest.
type Encryption string

const (
	// EncryptionKMS (the zero value too) encrypts with a customer-managed
	// KMS key the component creates, with rotation on. It is what the
	// component always did.
	EncryptionKMS Encryption = "kms"
	// EncryptionSSES3 encrypts with S3-managed keys (AES256). No KMS key is
	// created, role policies carry no KMS statements, and the bucket policy
	// denies any upload that does not state AES256.
	EncryptionSSES3 Encryption = "sse-s3"
)

func (e Encryption) valid() bool { return e == "" || e == EncryptionKMS || e == EncryptionSSES3 }

func (e Encryption) kms() bool { return e != EncryptionSSES3 }

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
	// ObjectLockGovernance creates the replica bucket WITH S3 Object Lock and
	// a GOVERNANCE default retention of ObjectLockDays (Lifecycle.RetentionDays
	// when that is zero), adds the delete-protection bucket policy and, when
	// Args.Lifecycle is set, the lifecycle rules.
	//
	// Object Lock can only be enabled when a bucket is created. Set on a
	// replica that already exists without it, this makes Pulumi REPLACE the
	// bucket (a protected bucket refuses): it never changes a replica in
	// place. Create a new replica bucket instead and retire the old one on
	// your own schedule. See docs/backupstore.md.
	ObjectLockGovernance bool
	// ObjectLockDays is the governance retention. Zero means
	// Lifecycle.RetentionDays. Ignored without ObjectLockGovernance.
	ObjectLockDays int
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

	// Encryption selects KMS (the default, "") or SSE-S3.
	Encryption Encryption
	// Tenants, when set, replace the writer glob: the bucket policy admits
	// exactly these (role, prefix, purpose) triples, each with an explicit
	// deny outside its prefix, and adds the global denies of delete and
	// bucket configuration for principals outside the backup account.
	// WriterRoleName and WriterObjectActions must then be empty.
	Tenants []Tenant
	// SingleProject declares that the bucket holds ONE project (the standard
	// shape: a bucket per project per environment). Every tenant then gets
	// s3:ListBucket on the bucket without an s3:prefix condition, which
	// barman-cloud's HeadBucket needs (it carries no s3:prefix), and the
	// list-outside-prefix deny is dropped. Object access stays per prefix:
	// put and get only on the tenant's own prefix, and the explicit deny of
	// everything else on the role stays. The cost is that a tenant sees the
	// key names of the other clusters of the same project, never their
	// contents. Requires Tenants. See docs/backupstore.md.
	SingleProject bool
	// DenyDestructive adds the global denies (delete of objects and versions,
	// lifecycle, bucket policy, versioning, replication configuration) for
	// principals outside the backup account without any tenant. Tenants
	// imply it.
	DenyDestructive bool

	// KeyDescription is the description of the bucket's KMS key (KMS mode).
	KeyDescription string
	// Tags are applied to the bucket (compliance tags, for example).
	Tags map[string]string
	// LifecycleRules is the bucket's lifecycle configuration. Backstop builds
	// the common shape. At least one rule must come from here or from
	// Lifecycle.
	LifecycleRules s3.BucketLifecycleConfigurationV2RuleArray
	// Lifecycle, when set, adds the parametrized, tag-based rules (see
	// Lifecycle) after LifecycleRules, to the bucket and, with
	// Replica.ObjectLockGovernance, to the replica.
	Lifecycle *Lifecycle
	// ObjectLockDays, when above zero, creates the bucket (and the replica)
	// with Object Lock and a COMPLIANCE default retention of that many days.
	ObjectLockDays int
	// Replica, when set, replicates the bucket to another region.
	Replica *Replica

	// Protect marks the buckets and the KMS keys protected, so a preview
	// that would delete one refuses. Nil means true: set it to a pointer to
	// false only to retire a bucket.
	Protect *bool
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
	// KMSKeyARN is the ARN of the bucket's key; empty in SSE-S3 mode.
	KMSKeyARN pulumi.StringOutput
	// TenantRolePolicies maps each tenant role ARN to the identity policy
	// (JSON) the caller attaches to it in the source account. Empty without
	// tenants.
	TenantRolePolicies pulumi.StringMapOutput
}

func (a *Args) partition() string {
	if a.Partition == "" {
		return DefaultPartition
	}

	return a.Partition
}

func (a *Args) protect() bool { return a.Protect == nil || *a.Protect }

func (a *Args) globalDenies() bool { return a.DenyDestructive || len(a.Tenants) > 0 }

func (a *Args) replicaGlobalDenies() bool {
	return a.globalDenies() || (a.Replica != nil && a.Replica.ObjectLockGovernance)
}

// TenantRolePolicies renders the identity policy of every tenant role, keyed
// by role ARN, for the caller to attach in the source account. kmsKeyARN is
// the bucket's key in KMS mode and ignored in SSE-S3 mode.
func (a *Args) TenantRolePolicies(kmsKeyARN string) (map[string]string, error) {
	return a.tenantRolePolicies(kmsKeyARN, true)
}

// tenantRolePolicies is TenantRolePolicies; strict false accepts an empty
// key ARN, which is what an output not yet resolved (a preview, a mock) hands
// the apply.
func (a *Args) tenantRolePolicies(kmsKeyARN string, strict bool) (map[string]string, error) {
	out := map[string]string{}
	byRole := map[string][]Tenant{}

	var order []string

	for _, t := range a.Tenants {
		if _, ok := byRole[t.RoleARN]; !ok {
			order = append(order, t.RoleARN)
		}

		byRole[t.RoleARN] = append(byRole[t.RoleARN], t)
	}

	for _, arn := range order {
		if !strict && a.Encryption.kms() && kmsKeyARN == "" {
			kmsKeyARN = "<unresolved>"
		}

		doc, err := TenantRolePolicy(TenantRolePolicyArgs{
			BucketARN: bucketARN(a.partition(), a.BucketName), Encryption: a.Encryption,
			KMSKeyARN: kmsKeyARN, Tenants: byRole[arn], SingleProject: a.SingleProject,
		})
		if err != nil {
			return nil, err
		}

		out[arn] = doc
	}

	return out, nil
}

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
	if !a.Encryption.valid() {
		errs = append(errs, fmt.Errorf("args: Encryption %q is unknown; use %q or %q", a.Encryption, EncryptionKMS, EncryptionSSES3))
	}

	if len(a.Tenants) == 0 {
		required("WriterRoleName", a.WriterRoleName)
	} else {
		if a.WriterRoleName != "" || a.WriterObjectActions != nil {
			errs = append(errs, errors.New("args: Tenants replace the writer; leave WriterRoleName and WriterObjectActions empty"))
		}

		errs = append(errs, validateTenants(a.Tenants)...)
	}

	if a.SingleProject && len(a.Tenants) == 0 {
		errs = append(errs, errors.New("args: SingleProject needs Tenants"))
	}

	if a.Encryption.kms() {
		required("KeyDescription", a.KeyDescription)
	}

	if a.Lifecycle != nil {
		errs = append(errs, a.Lifecycle.validate()...)
	}

	if len(a.LifecycleRules) == 0 && a.Lifecycle == nil {
		errs = append(errs, errors.New("args: LifecycleRules is empty; set LifecycleRules or Lifecycle"))
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

		if a.Encryption.kms() {
			required("Replica.KeyDescription", r.KeyDescription)
		}

		if r.ObjectLockDays < 0 {
			errs = append(errs, fmt.Errorf("args: Replica.ObjectLockDays is %d, want 0 or more", r.ObjectLockDays))
		}

		if r.ObjectLockGovernance && r.ObjectLockDays == 0 && (a.Lifecycle == nil || a.Lifecycle.RetentionDays <= 0) {
			errs = append(errs, errors.New("args: Replica.ObjectLockGovernance needs Replica.ObjectLockDays or Lifecycle.RetentionDays"))
		}

		if r.ObjectLockGovernance && a.ObjectLockDays > 0 {
			errs = append(errs, errors.New("args: Replica.ObjectLockGovernance and ObjectLockDays (COMPLIANCE on both buckets) are exclusive"))
		}
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
	comp.KMSKeyARN = pulumi.String("").ToStringOutput()

	if b.key != nil {
		comp.KMSKeyARN = b.key.Arn
	}

	policies, err := b.tenantPolicies()
	if err != nil {
		return nil, fmt.Errorf("backupstore %s: %w", name, err)
	}

	comp.TenantRolePolicies = policies

	if err := ctx.RegisterResourceOutputs(comp, pulumi.Map{
		"bucketName":         comp.BucketName,
		"bucketArn":          comp.BucketARN,
		"kmsKeyArn":          comp.KMSKeyARN,
		"tenantRolePolicies": comp.TenantRolePolicies,
	}); err != nil {
		return nil, err
	}

	return comp, nil
}

func (b *builder) tenantPolicies() (pulumi.StringMapOutput, error) {
	if len(b.args.Tenants) == 0 {
		return pulumi.StringMap{}.ToStringMapOutput(), nil
	}

	if b.key == nil {
		docs, err := b.args.tenantRolePolicies("", false)
		if err != nil {
			return pulumi.StringMapOutput{}, err
		}

		return pulumi.ToStringMap(docs).ToStringMapOutput(), nil
	}

	return b.key.Arn.ApplyT(func(arn string) (map[string]string, error) {
		return b.args.tenantRolePolicies(arn, false)
	}).(pulumi.StringMapOutput), nil
}

type builder struct {
	ctx  *pulumi.Context
	comp *BackupStore
	args *Args

	bucket *s3.Bucket
	key    *kms.Key
}

// childOpts is what every child registers with: the component as parent and
// the caller's extra options.
func (b *builder) childOpts(extra ...pulumi.ResourceOption) []pulumi.ResourceOption {
	return append([]pulumi.ResourceOption{pulumi.Parent(b.comp)}, extra...)
}

func (b *builder) protectOpt() []pulumi.ResourceOption {
	if b.args.protect() {
		return []pulumi.ResourceOption{pulumi.Protect(true)}
	}

	return nil
}

func (b *builder) primary() error {
	a := b.args
	name := a.BucketName
	part := a.partition()

	if a.Encryption.kms() {
		// RetainOnDelete: kms:ScheduleKeyDeletion is denied in the accounts this
		// is built for (key destruction is a break-glass act), so Pulumi drops a
		// key from state instead of deleting it.
		key, err := kms.NewKey(b.ctx, name+"-kms", &kms.KeyArgs{
			Description:       pulumi.String(a.KeyDescription),
			EnableKeyRotation: pulumi.Bool(true),
			Policy:            pulumi.String(buildKeyPolicy(part, a.SourceAccountID, a.BackupAccountID, a.WriterRoleName, a.ReaderRoleName, a.Tenants)),
		}, b.childOpts(append(b.protectOpt(), pulumi.RetainOnDelete(true))...)...)
		if err != nil {
			return fmt.Errorf("create KMS key: %w", err)
		}

		b.key = key

		if _, err := kms.NewAlias(b.ctx, name+"-kms-alias", &kms.AliasArgs{
			Name:        pulumi.String("alias/" + name),
			TargetKeyId: key.KeyId,
		}, b.childOpts()...); err != nil {
			return fmt.Errorf("create KMS alias: %w", err)
		}
	}

	bucketArgs := &s3.BucketArgs{
		Bucket: pulumi.String(name),
		Tags:   pulumi.ToStringMap(a.Tags),
	}
	if a.ObjectLockDays > 0 {
		bucketArgs.ObjectLockEnabled = pulumi.Bool(true)
	}

	bucket, err := s3.NewBucket(b.ctx, name, bucketArgs, b.childOpts(b.protectOpt()...)...)
	if err != nil {
		return fmt.Errorf("create backup bucket: %w", err)
	}

	b.bucket = bucket

	if _, err := s3.NewBucketVersioningV2(b.ctx, name+"-versioning", &s3.BucketVersioningV2Args{
		Bucket: bucket.ID(),
		VersioningConfiguration: &s3.BucketVersioningV2VersioningConfigurationArgs{
			Status: pulumi.String("Enabled"),
		},
	}, b.childOpts()...); err != nil {
		return fmt.Errorf("enable bucket versioning: %w", err)
	}

	if _, err := s3.NewBucketServerSideEncryptionConfigurationV2(b.ctx, name+"-encryption", &s3.BucketServerSideEncryptionConfigurationV2Args{
		Bucket: bucket.ID(),
		Rules: s3.BucketServerSideEncryptionConfigurationV2RuleArray{
			&s3.BucketServerSideEncryptionConfigurationV2RuleArgs{
				ApplyServerSideEncryptionByDefault: b.encryptionDefault(),
				// S3 Bucket Keys cut the KMS requests of a busy backup bucket
				// by about 99%: one data key serves many objects. (Only KMS
				// has a bucket key.)
				BucketKeyEnabled: bucketKeyEnabled(a.Encryption),
				// What S3 now sets on every bucket, stated so the input matches
				// it: no customer-provided keys (SSE-C).
				BlockedEncryptionTypes: pulumi.StringArray{pulumi.String("SSE-C")},
			},
		},
	}, b.childOpts()...); err != nil {
		return fmt.Errorf("configure bucket encryption: %w", err)
	}

	rules, err := b.lifecycleRules(true)
	if err != nil {
		return err
	}

	if _, err := s3.NewBucketLifecycleConfigurationV2(b.ctx, name+"-lifecycle", &s3.BucketLifecycleConfigurationV2Args{
		Bucket: bucket.ID(),
		Rules:  rules,
	}, b.childOpts()...); err != nil {
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
		}, b.childOpts()...); err != nil {
			return fmt.Errorf("configure object lock: %w", err)
		}
	}

	if _, err := s3.NewBucketPublicAccessBlock(b.ctx, name+"-public-access", blockPublicAccess(bucket.ID()), b.childOpts()...); err != nil {
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
		Tenants:         a.Tenants,
		SingleProject:   a.SingleProject,
		GlobalDenies:    a.globalDenies(),
		SSES3:           !a.Encryption.kms(),
	})

	if _, err := s3.NewBucketPolicy(b.ctx, name+"-policy", &s3.BucketPolicyArgs{
		Bucket: bucket.ID(),
		Policy: pulumi.String(policy),
	}, b.childOpts()...); err != nil {
		return fmt.Errorf("attach bucket policy: %w", err)
	}

	return nil
}

func (b *builder) encryptionDefault() *s3.BucketServerSideEncryptionConfigurationV2RuleApplyServerSideEncryptionByDefaultArgs {
	if !b.args.Encryption.kms() {
		return &s3.BucketServerSideEncryptionConfigurationV2RuleApplyServerSideEncryptionByDefaultArgs{
			SseAlgorithm: pulumi.String("AES256"),
		}
	}

	return &s3.BucketServerSideEncryptionConfigurationV2RuleApplyServerSideEncryptionByDefaultArgs{
		SseAlgorithm:   pulumi.String("aws:kms"),
		KmsMasterKeyId: b.key.Arn,
	}
}

func bucketKeyEnabled(e Encryption) pulumi.BoolPtrInput {
	if e.kms() {
		return pulumi.Bool(true)
	}

	return nil
}

// lifecycleRules is the caller's rules followed by the parametrized ones.
// The replica takes only the parametrized ones, and no tiering.
func (b *builder) lifecycleRules(primary bool) (s3.BucketLifecycleConfigurationV2RuleArray, error) {
	a := b.args

	var rules s3.BucketLifecycleConfigurationV2RuleArray
	if primary {
		rules = append(rules, a.LifecycleRules...)
	}

	if a.Lifecycle != nil {
		gen, err := LifecycleRules(*a.Lifecycle, primary)
		if err != nil {
			return nil, fmt.Errorf("lifecycle rules: %w", err)
		}

		rules = append(rules, gen...)
	}

	return rules, nil
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
	ropts := func(extra ...pulumi.ResourceOption) []pulumi.ResourceOption {
		return b.childOpts(append([]pulumi.ResourceOption{pulumi.Provider(r.Provider)}, extra...)...)
	}

	var replicaKey *kms.Key

	if a.Encryption.kms() {
		var err error

		replicaKey, err = kms.NewKey(b.ctx, rname+"-kms", &kms.KeyArgs{
			Description:       pulumi.String(r.KeyDescription),
			EnableKeyRotation: pulumi.Bool(true),
		}, ropts(append(b.protectOpt(), pulumi.RetainOnDelete(true))...)...)
		if err != nil {
			return fmt.Errorf("create replica KMS key: %w", err)
		}
	}

	replicaArgs := &s3.BucketV2Args{Bucket: pulumi.String(rname)}
	if a.ObjectLockDays > 0 || r.ObjectLockGovernance {
		// Replicating locked objects requires Object Lock on the destination.
		// Object Lock can only be switched on when a bucket is created.
		replicaArgs.ObjectLockEnabled = pulumi.Bool(true)
	}

	replica, err := s3.NewBucketV2(b.ctx, rname, replicaArgs, ropts(b.protectOpt()...)...)
	if err != nil {
		return fmt.Errorf("create replica bucket: %w", err)
	}

	if _, err := s3.NewBucketVersioningV2(b.ctx, rname+"-versioning", &s3.BucketVersioningV2Args{
		Bucket: replica.ID(),
		VersioningConfiguration: &s3.BucketVersioningV2VersioningConfigurationArgs{
			Status: pulumi.String("Enabled"),
		},
	}, ropts()...); err != nil {
		return fmt.Errorf("replica versioning: %w", err)
	}

	if _, err := s3.NewBucketPublicAccessBlock(b.ctx, rname+"-public-access", blockPublicAccess(replica.ID()), ropts()...); err != nil {
		return fmt.Errorf("replica public access block: %w", err)
	}

	if r.ObjectLockGovernance {
		days := r.ObjectLockDays
		if days == 0 {
			days = a.Lifecycle.RetentionDays
		}

		if _, err := s3.NewBucketObjectLockConfigurationV2(b.ctx, rname+"-object-lock", &s3.BucketObjectLockConfigurationV2Args{
			Bucket: replica.ID(),
			Rule: &s3.BucketObjectLockConfigurationV2RuleArgs{
				DefaultRetention: &s3.BucketObjectLockConfigurationV2RuleDefaultRetentionArgs{
					Mode: pulumi.String("GOVERNANCE"),
					Days: pulumi.Int(days),
				},
			},
		}, ropts()...); err != nil {
			return fmt.Errorf("replica object lock: %w", err)
		}
	}

	if a.Lifecycle != nil && r.ObjectLockGovernance {
		rules, err := b.lifecycleRules(false)
		if err != nil {
			return err
		}

		if _, err := s3.NewBucketLifecycleConfigurationV2(b.ctx, rname+"-lifecycle", &s3.BucketLifecycleConfigurationV2Args{
			Bucket: replica.ID(),
			Rules:  rules,
		}, ropts()...); err != nil {
			return fmt.Errorf("replica lifecycle: %w", err)
		}
	}

	// A resource policy on the replica only when something in the source
	// account is meant to list it, or the global denies apply. It carries the delete-version deny, so
	// the replica's immutability reads the same as the primary's. The deny
	// binds principals outside the backup account only: replication writes
	// from inside it, and lifecycle expiry is the service's own action.
	if len(a.ListerRoleNames) > 0 || a.replicaGlobalDenies() {
		doc, err := buildReplicaPolicy(part, rname, a.SourceAccountID, a.BackupAccountID, a.ListerRoleNames, a.replicaGlobalDenies())
		if err != nil {
			return fmt.Errorf("marshal replica bucket policy: %w", err)
		}

		if _, err := s3.NewBucketPolicy(b.ctx, rname+"-policy", &s3.BucketPolicyArgs{
			Bucket: replica.ID(),
			Policy: pulumi.String(doc),
		}, ropts()...); err != nil {
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

	role, err := iam.NewRole(b.ctx, name+"-replication-role", roleArgs, b.childOpts()...)
	if err != nil {
		return fmt.Errorf("replication role: %w", err)
	}

	lock := a.ObjectLockDays > 0
	srcKey, dstKey := pulumi.String("").ToStringOutput(), pulumi.String("").ToStringOutput()

	if replicaKey != nil {
		srcKey, dstKey = b.key.Arn, replicaKey.Arn
	}

	policy := pulumi.All(b.bucket.Arn, replica.Arn, srcKey, dstKey).ApplyT(func(vs []any) (string, error) {
		return buildReplicationPolicy(vs[0].(string), vs[1].(string), vs[2].(string), vs[3].(string), lock, replicaKey != nil)
	}).(pulumi.StringOutput)

	if _, err := iam.NewRolePolicy(b.ctx, name+"-replication-policy", &iam.RolePolicyArgs{
		Role:   role.Name,
		Policy: policy,
	}, b.childOpts()...); err != nil {
		return fmt.Errorf("replication policy: %w", err)
	}

	rule := &s3.BucketReplicationConfigRuleArgs{
		Id:     pulumi.String("crr"),
		Status: pulumi.String("Enabled"),
		Filter: &s3.BucketReplicationConfigRuleFilterArgs{Prefix: pulumi.String("")},
		DeleteMarkerReplication: &s3.BucketReplicationConfigRuleDeleteMarkerReplicationArgs{
			Status: pulumi.String("Enabled"),
		},
		Destination: &s3.BucketReplicationConfigRuleDestinationArgs{
			Bucket:       replica.Arn,
			StorageClass: pulumi.String("STANDARD_IA"),
		},
	}

	// KMS mode only: the replica has its own key. SSE-S3 objects replicate
	// under the destination's default encryption, with no key to name.
	if replicaKey != nil {
		rule.SourceSelectionCriteria = &s3.BucketReplicationConfigRuleSourceSelectionCriteriaArgs{
			SseKmsEncryptedObjects: &s3.BucketReplicationConfigRuleSourceSelectionCriteriaSseKmsEncryptedObjectsArgs{
				Status: pulumi.String("Enabled"),
			},
		}
		rule.Destination = &s3.BucketReplicationConfigRuleDestinationArgs{
			Bucket:       replica.Arn,
			StorageClass: pulumi.String("STANDARD_IA"),
			EncryptionConfiguration: &s3.BucketReplicationConfigRuleDestinationEncryptionConfigurationArgs{
				ReplicaKmsKeyId: replicaKey.Arn,
			},
		}
	}

	if _, err := s3.NewBucketReplicationConfig(b.ctx, name+"-replication", &s3.BucketReplicationConfigArgs{
		Bucket: b.bucket.ID(),
		Role:   role.Arn,
		Rules:  s3.BucketReplicationConfigRuleArray{rule},
	}, b.childOpts()...); err != nil {
		return fmt.Errorf("replication config: %w", err)
	}

	return nil
}
