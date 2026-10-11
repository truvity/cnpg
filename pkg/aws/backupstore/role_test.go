package backupstore_test

import (
	"fmt"
	"strings"
	"testing"

	pulumiaws "github.com/pulumi/pulumi-aws/sdk/v7/go/aws"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/truvity/k8s/pkg/aws/podidentity"

	"github.com/truvity/cnpg/v2/pkg/aws/backupstore"
)

// wantRolePolicy pins the exact bytes of the policy: a caller that adopted a
// role keeps its inline policy only while the document is unchanged (the
// provider compares strings), so a re-ordered key is an in-place update of
// every adopted role.
const wantRolePolicy = `{"Statement":[{"Action":["s3:PutObject","s3:GetObject","s3:DeleteObject","s3:AbortMultipartUpload","s3:ListMultipartUploadParts"],"Effect":"Allow","Resource":"BUCKET/proj/proj-pg/*","Sid":"WriteOwnSubtree"},{"Action":["s3:GetObject"],"Effect":"Allow","Resource":"BUCKET/proj/*","Sid":"ReadProjectPrefix"},{"Action":["s3:ListBucket","s3:ListBucketMultipartUploads"],"Condition":{"StringLikeIfExists":{"s3:prefix":"proj/*"}},"Effect":"Allow","Resource":"BUCKET","Sid":"ListProjectPrefix"},{"Action":["kms:Encrypt","kms:Decrypt","kms:ReEncrypt*","kms:GenerateDataKey*","kms:DescribeKey"],"Effect":"Allow","Resource":"KEY","Sid":"UseBackupKey"}],"Version":"2012-10-17"}`

func policyArgs() backupstore.RolePolicyArgs {
	return backupstore.RolePolicyArgs{
		BucketARN:   "BUCKET",
		KMSKeyARN:   "KEY",
		WritePrefix: "proj/proj-pg/",
		ReadPrefix:  "proj/",
	}
}

func TestRolePolicyBytes(t *testing.T) {
	got, err := backupstore.RolePolicy(policyArgs())
	if err != nil {
		t.Fatal(err)
	}

	if got != wantRolePolicy {
		t.Errorf("policy changed:\n got %s\nwant %s", got, wantRolePolicy)
	}
}

func TestRolePolicyRequiresEveryField(t *testing.T) {
	_, err := backupstore.RolePolicy(backupstore.RolePolicyArgs{})
	if err == nil {
		t.Fatal("want an error")
	}

	for _, f := range []string{"BucketARN", "KMSKeyARN", "WritePrefix", "ReadPrefix"} {
		if !strings.Contains(err.Error(), f) {
			t.Errorf("error does not name %s: %v", f, err)
		}
	}
}

func roleArgs(p pulumi.ProviderResource) *backupstore.RoleArgs {
	return &backupstore.RoleArgs{
		Policy:              policyArgs(),
		Provider:            p,
		ClusterName:         pulumi.String("cluster"),
		AccountID:           src,
		ClusterARN:          fmt.Sprintf("arn:%s:eks:r:%s:cluster/cluster", partition, src),
		RoleName:            "env-pg-backup-proj",
		PermissionsBoundary: "boundary",
		Namespace:           "proj",
		ServiceAccounts:     []string{"proj-pg", "proj-rehearsal-pg"},
	}
}

func runRole(t *testing.T, opts ...backupstore.RoleOption) []registration {
	t.Helper()

	rec := &recorder{}

	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		p, err := pulumiaws.NewProvider(ctx, "primary", &pulumiaws.ProviderArgs{})
		if err != nil {
			return err
		}

		_, err = backupstore.NewRole(ctx, "pg-backup/env-pg-backup-proj", roleArgs(p), opts...)

		return err
	}, pulumi.WithMocks("example", "dev", rec))
	if err != nil {
		t.Fatal(err)
	}

	return rec.regs
}

func names(regs []registration) []string {
	var out []string

	for _, r := range regs {
		if !strings.HasPrefix(r.typ, "pulumi:providers:") {
			out = append(out, r.name)
		}
	}

	return out
}

func TestNewRoleDefaultNames(t *testing.T) {
	got := strings.Join(names(runRole(t)), ",")
	want := "pg-backup/env-pg-backup-proj,pg-backup/env-pg-backup-proj-role,pg-backup/env-pg-backup-proj-role-policy," +
		"pg-backup/env-pg-backup-proj-pia-proj-pg,pg-backup/env-pg-backup-proj-pia-proj-rehearsal-pg"

	if got != want {
		t.Errorf("names:\n got %s\nwant %s", got, want)
	}
}

func TestNewRoleAdoptionOptions(t *testing.T) {
	const c = "pg-backup/env-pg-backup-proj"

	regs := runRole(t,
		backupstore.WithLegacyTopLevel(),
		backupstore.WithChildNames(func(ch podidentity.Child) string {
			switch ch.Kind {
			case podidentity.KindRolePolicy:
				return c + "/policy"
			case podidentity.KindAssociation:
				return c + "/pia-" + ch.Key
			default:
				return c + "/role"
			}
		}),
	)

	got := strings.Join(names(regs), ",")
	want := c + "," + c + "/role," + c + "/policy," + c + "/pia-proj-pg," + c + "/pia-proj-rehearsal-pg"

	if got != want {
		t.Errorf("names:\n got %s\nwant %s", got, want)
	}

	for _, r := range regs {
		if r.typ == backupstore.RoleTypeToken || strings.HasPrefix(r.typ, "pulumi:providers:") {
			continue
		}

		if len(r.aliases) != 1 || !r.aliases[0].noParent {
			t.Errorf("%s: want one no-parent alias, got %+v", r.name, r.aliases)
		}
	}
}

func TestNewRoleValidates(t *testing.T) {
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		_, err := backupstore.NewRole(ctx, "r", &backupstore.RoleArgs{})

		return err
	}, pulumi.WithMocks("example", "dev", &recorder{}))
	if err == nil {
		t.Fatal("want an error for empty args")
	}
}
