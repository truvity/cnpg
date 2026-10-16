package backupstore

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/truvity/k8s/pkg/aws/podidentity"
)

// RoleTypeToken is the Pulumi type of the role component. It is the Pod
// Identity component of truvity/k8s: the role, its inline policy and its
// associations are that component's children.
const RoleTypeToken = podidentity.TypeToken

// DefaultRolePolicyName is the name of the role's inline policy when
// RoleArgs.PolicyName is empty.
const DefaultRolePolicyName = "cnpg-backup"

// RolePolicyArgs is what the identity policy of a backup role needs. Every
// field is required.
type RolePolicyArgs struct {
	// BucketARN is the ARN of the backup bucket.
	BucketARN string
	// KMSKeyARN is the ARN of the bucket's key.
	KMSKeyARN string
	// WritePrefix is the key prefix the role may write, ending in "/": the
	// subtree the archiver of one Postgres cluster owns.
	WritePrefix string
	// ReadPrefix is the key prefix the role may read and list, ending in
	// "/": every real restore is a different cluster reading an old
	// cluster's prefix.
	ReadPrefix string
}

// Validate reports every problem at once, or returns nil.
func (a *RolePolicyArgs) Validate() error {
	var errs []error

	required := func(field, v string) {
		if v == "" {
			errs = append(errs, fmt.Errorf("role policy args: %s is empty", field))
		}
	}

	required("BucketARN", a.BucketARN)
	required("KMSKeyARN", a.KMSKeyARN)
	required("WritePrefix", a.WritePrefix)
	required("ReadPrefix", a.ReadPrefix)

	return errors.Join(errs...)
}

// RolePolicy renders the identity policy of a backup role, as JSON:
//   - WRITE (put, get, delete, multipart) only under WritePrefix. Delete is
//     what the backup plugin's retention pruning needs; on a versioned
//     bucket it only creates a delete marker, and the bucket policy denies
//     DeleteObjectVersion, so noncurrent versions stay the recovery pool;
//   - READ (get) under ReadPrefix, and list of the bucket scoped to that
//     prefix;
//   - encrypt and decrypt on the bucket's key, the second gate.
//
// The list statement uses StringLikeIfExists: HeadBucket, which barman's
// check-wal-archive runs on a fresh cluster, carries no s3:prefix key, and a
// plain StringLike fails closed so archiving gets a 403. A prefix-scoped
// ListObjects stays enforced.
func RolePolicy(a RolePolicyArgs) (string, error) {
	if err := a.Validate(); err != nil {
		return "", err
	}

	raw, err := json.Marshal(map[string]any{
		"Version": "2012-10-17",
		"Statement": []map[string]any{
			{
				"Sid":    "WriteOwnSubtree",
				"Effect": "Allow",
				"Action": []string{
					"s3:PutObject", "s3:GetObject", "s3:DeleteObject",
					"s3:AbortMultipartUpload", "s3:ListMultipartUploadParts",
				},
				"Resource": a.BucketARN + "/" + a.WritePrefix + "*",
			},
			{
				"Sid":      "ReadProjectPrefix",
				"Effect":   "Allow",
				"Action":   []string{"s3:GetObject"},
				"Resource": a.BucketARN + "/" + a.ReadPrefix + "*",
			},
			{
				"Sid":      "ListProjectPrefix",
				"Effect":   "Allow",
				"Action":   []string{"s3:ListBucket", "s3:ListBucketMultipartUploads"},
				"Resource": a.BucketARN,
				"Condition": map[string]any{
					"StringLikeIfExists": map[string]any{
						"s3:prefix": a.ReadPrefix + "*",
					},
				},
			},
			{
				"Sid":    "UseBackupKey",
				"Effect": "Allow",
				"Action": []string{
					"kms:Encrypt", "kms:Decrypt", "kms:ReEncrypt*",
					"kms:GenerateDataKey*", "kms:DescribeKey",
				},
				"Resource": a.KMSKeyARN,
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("render role policy: %w", err)
	}

	return string(raw), nil
}

// RoleArgs configures a backup role: the identity of the Postgres cluster
// that archives to a bucket made by New, in the workload account.
type RoleArgs struct {
	// Policy is the permissions: bucket, key and prefixes.
	Policy RolePolicyArgs

	// Provider is the AWS provider of the workload cluster's account and
	// region. Required.
	Provider pulumi.ProviderResource
	// ClusterName is the EKS cluster the associations are in. Pass its Name
	// output to order them after the cluster. Required.
	ClusterName pulumi.StringInput
	// Region is set on each association when not empty.
	Region string
	// AccountID and ClusterARN pin the trust policy to the workload cluster.
	AccountID  string
	ClusterARN string

	// RoleName is the IAM role's name. It must match the writer glob the
	// bucket was made with (Args.WriterRoleName). Required.
	RoleName string
	// PermissionsBoundary is the ARN of the role's boundary, when the
	// account requires one.
	PermissionsBoundary string
	// PolicyName is the inline policy's name. Empty is DefaultRolePolicyName.
	PolicyName string

	// Namespace and ServiceAccounts say which pods assume the role: the
	// instance ServiceAccount of the Postgres cluster (the primary) and any
	// other, such as a rehearsal identity. Required.
	Namespace       string
	ServiceAccounts []string
}

// RoleOption adjusts how NewRole registers its resources. The library never
// names an estate's resources: a caller that adopts existing ones passes the
// logical names they already have (and any alias, WithResourceOptions).
type RoleOption func(*roleConfig)

type roleConfig struct {
	names        podidentity.NameFunc
	protect      bool
	ignoreTags   bool
	resourceOpts []pulumi.ResourceOption
}

// WithChildNames sets the logical names of the role, its policy and its
// associations (the children of the Pod Identity component). Without it they
// are named from the component name by podidentity.DefaultName.
func WithChildNames(f podidentity.NameFunc) RoleOption {
	return func(c *roleConfig) { c.names = f }
}

// WithProtect protects the role and the associations.
func WithProtect() RoleOption {
	return func(c *roleConfig) { c.protect = true }
}

// WithIgnoreAssociationTags ignores changes to the associations' tags, for
// associations another tool tagged.
func WithIgnoreAssociationTags() RoleOption {
	return func(c *roleConfig) { c.ignoreTags = true }
}

// WithResourceOptions adds resource options (aliases, parent, dependsOn) to
// the Pod Identity component itself.
func WithResourceOptions(opts ...pulumi.ResourceOption) RoleOption {
	return func(c *roleConfig) { c.resourceOpts = append(c.resourceOpts, opts...) }
}

// Validate reports every problem with args at once, or returns nil.
func (a *RoleArgs) Validate() error {
	return errors.Join(a.Policy.Validate(), a.podIdentity(nil, roleConfig{}).Validate())
}

// RolePod is the Pod Identity component NewRole registers.
type RolePod = podidentity.PodIdentity

// NewRole registers the backup role, its inline policy and one Pod Identity
// association per service account, as one Pod Identity component named name.
func NewRole(ctx *pulumi.Context, name string, args *RoleArgs, opts ...RoleOption) (*RolePod, error) {
	if args == nil {
		return nil, errors.New("backupstore: role args is nil")
	}

	var cfg roleConfig
	for _, o := range opts {
		o(&cfg)
	}

	doc, err := RolePolicy(args.Policy)
	if err != nil {
		return nil, fmt.Errorf("backupstore role %s: %w", name, err)
	}

	return podidentity.New(ctx, name, args.podIdentity(&doc, cfg), cfg.resourceOpts...)
}

func (a *RoleArgs) podIdentity(doc *string, cfg roleConfig) *podidentity.Args {
	policyName := a.PolicyName
	if policyName == "" {
		policyName = DefaultRolePolicyName
	}

	var document pulumi.StringInput = pulumi.String("")
	if doc != nil {
		document = pulumi.String(*doc)
	}

	return &podidentity.Args{
		Provider:              a.Provider,
		ClusterName:           a.ClusterName,
		Region:                a.Region,
		Namespace:             a.Namespace,
		ServiceAccounts:       a.ServiceAccounts,
		RoleName:              a.RoleName,
		PermissionsBoundary:   a.PermissionsBoundary,
		AccountID:             a.AccountID,
		ClusterARN:            a.ClusterARN,
		InlinePolicy:          &podidentity.InlinePolicy{Name: policyName, Document: document},
		IgnoreAssociationTags: cfg.ignoreTags,
		Names:                 cfg.names,
		Protect:               cfg.protect,
	}
}
