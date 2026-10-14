package backupstore

import (
	"errors"
	"fmt"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/truvity/cnpg/v2/pkg/backupnaming"
)

// ProjectCluster is one CloudNativePG cluster that archives into its
// project's bucket, under <Namespace>/<Name>/.
type ProjectCluster struct {
	// Name is the Cluster resource's name. Its archive and restore roles are
	// named from it (backupnaming.ArchiveRoleName, RestoreRoleName).
	Name string
	// Namespace is where the cluster runs.
	Namespace string
	// ServiceAccount is the ServiceAccount the archive role is bound to, which
	// the bucket pins as a session tag. Empty means Name: the operator names
	// an instance's ServiceAccount after the cluster.
	ServiceAccount string
}

// Project is one project and the clusters inside its bucket.
type Project struct {
	// Name is the project's name, the middle of the bucket name.
	Name     string
	Clusters []ProjectCluster
}

// ProjectBucketsArgs describes the bucket of every project of one
// environment. The shape is the documented standard: a SingleProject bucket
// per project, append-only (DenyDestructive), the tenants' exact role ARNs,
// one expiry age.
type ProjectBucketsArgs struct {
	// BucketPrefix is the stem of every bucket name:
	// <BucketPrefix>-<project>-<Env>-<Region>. Required: the library has no
	// estate name of its own.
	BucketPrefix string
	// Env is the environment. It names the roles (backupnaming.RolePrefix)
	// and the bucket. Required.
	Env string
	// Region is the buckets' region, the last part of their names. Required.
	Region string
	// SessionClusterName is the EKS cluster the archive roles are bound on,
	// pinned as the eks-cluster-name session tag. Required.
	SessionClusterName string

	// SourceAccountID is the workload account, where the roles live.
	SourceAccountID string
	// BackupAccountID is the account the buckets live in.
	BackupAccountID string
	// Partition is the AWS partition in ARNs. Empty is DefaultPartition.
	Partition string

	// Projects are the buckets to make, in order.
	Projects []Project

	// Encryption is the buckets' mode. Empty is EncryptionSSES3 (the standard
	// shape); set EncryptionKMS to give every bucket its own key, and
	// KeyDescription then names it.
	Encryption Encryption
	// KeyDescription returns the KMS key description of a bucket. Used in KMS
	// mode only; nil leaves it empty.
	KeyDescription func(project, bucketName string) string
	// Lifecycle is every bucket's lifecycle. RetentionDays is required.
	Lifecycle Lifecycle
	// Tags returns the tags of a project's bucket. Nil means none.
	Tags func(project string) map[string]string
	// Protect and LegacyTopLevel are passed to every Args unchanged.
	Protect        *bool
	LegacyTopLevel bool

	// ResourceName returns the logical name of a project's component. Nil
	// means the bucket's name, which is what a caller that registered the
	// buckets one by one with New(ctx, bucketName, ...) already has.
	ResourceName func(bucketName string) string
	// ResourceOptions are added to every component: the provider of the
	// backup account (pulumi.Providers), a parent, a protect.
	ResourceOptions []pulumi.ResourceOption
	// ProjectOptions, when set, returns further options for one project's
	// component, such as aliases from the URN it had before.
	ProjectOptions func(project Project, bucketName string) []pulumi.ResourceOption
}

// Validate reports every problem at once, or returns nil.
func (a *ProjectBucketsArgs) Validate() error {
	var errs []error

	for _, f := range []struct{ name, v string }{
		{"BucketPrefix", a.BucketPrefix}, {"Env", a.Env}, {"Region", a.Region},
		{"SessionClusterName", a.SessionClusterName},
		{"SourceAccountID", a.SourceAccountID}, {"BackupAccountID", a.BackupAccountID},
	} {
		if f.v == "" {
			errs = append(errs, fmt.Errorf("project buckets: %s is empty", f.name))
		}
	}

	seen := map[string]bool{}

	for _, p := range a.Projects {
		if seen[p.Name] {
			errs = append(errs, fmt.Errorf("project buckets: project %q is listed twice", p.Name))
		}

		seen[p.Name] = true

		if len(p.Clusters) == 0 {
			errs = append(errs, fmt.Errorf("project buckets: project %q has no cluster", p.Name))
		}
	}

	return errors.Join(errs...)
}

// ProjectBucket is one project's bucket as built by BuildProjectBuckets.
type ProjectBucket struct {
	Project Project
	// Args is what New is called with; Args.BucketName is the bucket's name.
	Args *Args
}

// BuildProjectBuckets returns the Args of every project's bucket without
// registering anything, so a caller or a test can read them. Each bucket has
// two tenants per cluster: the archive role (with the session tags of the
// association's own cluster, namespace and ServiceAccount) and the restore
// role (no session tags: its ServiceAccount may move to a drill namespace).
// Every Args is validated.
func BuildProjectBuckets(a *ProjectBucketsArgs) ([]ProjectBucket, error) {
	if a == nil {
		return nil, errors.New("backupstore: project buckets args is nil")
	}

	if err := a.Validate(); err != nil {
		return nil, err
	}

	partition := a.Partition
	if partition == "" {
		partition = DefaultPartition
	}

	rolePrefix := "arn:" + partition + ":iam::" + a.SourceAccountID + ":role/"

	encryption := a.Encryption
	if encryption == "" {
		encryption = EncryptionSSES3
	}

	var (
		out  []ProjectBucket
		errs []error
	)

	for _, p := range a.Projects {
		name, err := backupnaming.ProjectBucketName(a.BucketPrefix, p.Name, a.Env, a.Region)
		if err != nil {
			errs = append(errs, err)

			continue
		}

		var tenants []Tenant

		for _, cl := range p.Clusters {
			prefix := backupnaming.ClusterPrefix(cl.Namespace, cl.Name)

			sa := cl.ServiceAccount
			if sa == "" {
				sa = cl.Name
			}

			tenants = append(tenants,
				Tenant{
					RoleARN: rolePrefix + backupnaming.ArchiveRoleName(a.Env, cl.Name),
					Prefix:  prefix,
					Purpose: PurposeArchive,
					SessionTags: &SessionTags{
						ClusterName:    a.SessionClusterName,
						Namespace:      cl.Namespace,
						ServiceAccount: sa,
					},
				},
				Tenant{
					RoleARN: rolePrefix + backupnaming.RestoreRoleName(a.Env, cl.Name),
					Prefix:  prefix,
					Purpose: PurposeRestore,
				},
			)
		}

		lc := a.Lifecycle

		args := &Args{
			BucketName:      name,
			SourceAccountID: a.SourceAccountID,
			BackupAccountID: a.BackupAccountID,
			Partition:       a.Partition,
			Encryption:      encryption,
			SingleProject:   true,
			DenyDestructive: true,
			Tenants:         tenants,
			Lifecycle:       &lc,
			Protect:         a.Protect,
			LegacyTopLevel:  a.LegacyTopLevel,
		}
		if a.Tags != nil {
			args.Tags = a.Tags(p.Name)
		}

		if a.KeyDescription != nil && encryption == EncryptionKMS {
			args.KeyDescription = a.KeyDescription(p.Name, name)
		}

		if err := args.Validate(); err != nil {
			errs = append(errs, fmt.Errorf("project %s: %w", p.Name, err))

			continue
		}

		out = append(out, ProjectBucket{Project: p, Args: args})
	}

	if err := errors.Join(errs...); err != nil {
		return nil, err
	}

	return out, nil
}

// NewProjectBuckets registers one BackupStore per project, built by
// BuildProjectBuckets, and returns them in the order of a.Projects. It
// registers nothing when any project is invalid.
//
// Each component is named a.ResourceName(bucketName), which is the bucket's
// name when ResourceName is nil: the URN a caller gets from
// New(ctx, bucketName, args, pulumi.Providers(p)). Parent, provider and
// aliases come from the options, so adopting existing resources never needs
// a rename.
func NewProjectBuckets(ctx *pulumi.Context, a *ProjectBucketsArgs) ([]*BackupStore, error) {
	built, err := BuildProjectBuckets(a)
	if err != nil {
		return nil, err
	}

	stores := make([]*BackupStore, 0, len(built))

	for _, b := range built {
		name := b.Args.BucketName
		if a.ResourceName != nil {
			name = a.ResourceName(name)
		}

		opts := append([]pulumi.ResourceOption(nil), a.ResourceOptions...)
		if a.ProjectOptions != nil {
			opts = append(opts, a.ProjectOptions(b.Project, b.Args.BucketName)...)
		}

		s, err := New(ctx, name, b.Args, opts...)
		if err != nil {
			return nil, fmt.Errorf("create bucket for project %s: %w", b.Project.Name, err)
		}

		stores = append(stores, s)
	}

	return stores, nil
}
