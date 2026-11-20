// Copyright 2026 Truvity B.V.. All rights reserved.
// SPDX-License-Identifier: MIT

// Package chart_test pins cnpg-backup-access to the Go library: the inline
// policy of every Role the chart renders must equal, byte for byte, what
// backupstore.TenantRolePolicy renders for the same bucket, prefix and
// purpose (an SSE-S3, SingleProject bucket). The template builds the JSON on
// its own, so this test is what keeps the two in step.
package chart_test

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	"github.com/truvity/cnpg/v2/pkg/aws/backupstore"
	"github.com/truvity/cnpg/v2/pkg/backupnaming"
)

const (
	account = "123456789012"
	bucket  = "example-backup"
)

func render(t *testing.T, values string) []map[string]any {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), "helm", "template", "x", ".", "-f", "-")
	cmd.Stdin = strings.NewReader(values)

	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "helm template failed:\n%s", out)

	var objs []map[string]any

	for _, d := range strings.Split(string(out), "\n---") {
		var m map[string]any
		if yaml.Unmarshal([]byte(d), &m) == nil && len(m) > 0 {
			objs = append(objs, m)
		}
	}

	return objs
}

func spec(o map[string]any) map[string]any { return o["spec"].(map[string]any) }
func name(o map[string]any) string         { return o["metadata"].(map[string]any)["name"].(string) }

// TestInlinePolicyEqualsBackupstoreTenantRolePolicy renders several clusters
// (default prefix, custom prefix) and compares each Role's policy with the
// library's.
func TestInlinePolicyEqualsBackupstoreTenantRolePolicy(t *testing.T) {
	objs := render(t, `
global: {accountID: "`+account+`", eksClusterName: example-eks, region: eu-west-1, rolePrefix: example}
bucket: {name: `+bucket+`}
clusters:
  - {name: app-pg, namespace: app}
  - {name: ledger-pg, namespace: ledger, prefix: custom/ledger/}
`)

	want := map[string]struct {
		purpose backupstore.Purpose
		prefix  string
	}{
		"example-app-pg-archive":    {backupstore.PurposeArchive, "app/app-pg/"},
		"example-app-pg-restore":    {backupstore.PurposeRestore, "app/app-pg/"},
		"example-ledger-pg-archive": {backupstore.PurposeArchive, "custom/ledger/"},
		"example-ledger-pg-restore": {backupstore.PurposeRestore, "custom/ledger/"},
	}

	seen := 0

	for _, o := range objs {
		if o["kind"] != "Role" {
			continue
		}

		w, ok := want[name(o)]
		require.Truef(t, ok, "unexpected Role %s", name(o))

		expected, err := backupstore.TenantRolePolicy(backupstore.TenantRolePolicyArgs{
			BucketARN:     "arn:aws:s3:::" + bucket,
			Encryption:    backupstore.EncryptionSSES3,
			SingleProject: true,
			Tenants: []backupstore.Tenant{{
				RoleARN: fmt.Sprintf("arn:aws:iam::%s:role/%s", account, name(o)),
				Prefix:  w.prefix,
				Purpose: w.purpose,
			}},
		})
		require.NoError(t, err)

		policies := spec(o)["inlinePolicies"].(map[string]any)
		require.Len(t, policies, 1)
		assert.Equal(t, expected, policies["cnpg-backup-"+string(w.purpose)], name(o))

		seen++
	}

	assert.Equal(t, len(want), seen)
}

// TestPartitionFollowsTheValue: the policy takes the partition from values.
func TestPartitionFollowsTheValue(t *testing.T) {
	objs := render(t, `
global: {accountID: "`+account+`", eksClusterName: example-eks, region: eu-west-1, rolePrefix: example, partition: aws-cn}
bucket: {name: `+bucket+`}
clusters: [{name: pg, namespace: ns}]
`)

	for _, o := range objs {
		if o["kind"] != "Role" {
			continue
		}

		purpose := backupstore.PurposeArchive
		if strings.HasSuffix(name(o), "-restore") {
			purpose = backupstore.PurposeRestore
		}

		expected, err := backupstore.TenantRolePolicy(backupstore.TenantRolePolicyArgs{
			BucketARN: "arn:aws-cn:s3:::" + bucket, Encryption: backupstore.EncryptionSSES3, SingleProject: true,
			Tenants: []backupstore.Tenant{{
				RoleARN: fmt.Sprintf("arn:aws-cn:iam::%s:role/%s", account, name(o)),
				Prefix:  "ns/pg/", Purpose: purpose,
			}},
		})
		require.NoError(t, err)
		assert.Equal(t, expected, spec(o)["inlinePolicies"].(map[string]any)["cnpg-backup-"+string(purpose)])
	}
}

// TestNoDeleteAndTrust: no policy holds a delete, and every trust policy is
// Pod Identity pinned to the source account.
func TestNoDeleteAndTrust(t *testing.T) {
	objs := render(t, `
global: {accountID: "`+account+`", eksClusterName: example-eks, region: eu-west-1, rolePrefix: example}
bucket: {name: `+bucket+`}
clusters: [{name: pg, namespace: ns}]
`)

	for _, o := range objs {
		if o["kind"] != "Role" {
			continue
		}

		for _, p := range spec(o)["inlinePolicies"].(map[string]any) {
			assert.NotContains(t, p, "Delete")
		}

		sa := strings.TrimPrefix(name(o), "example-")
		sa = strings.TrimSuffix(strings.TrimSuffix(sa, "-archive"), "-restore")
		if strings.HasSuffix(name(o), "-restore") {
			sa += "-restore"
		}

		assert.JSONEq(t, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"pods.eks.amazonaws.com"},
"Action":["sts:AssumeRole","sts:TagSession"],"Condition":{
"StringEquals":{"aws:SourceAccount":"`+account+`","aws:RequestTag/kubernetes-namespace":"ns","aws:RequestTag/kubernetes-service-account":"`+sa+`"},
"ArnEquals":{"aws:SourceArn":"arn:aws:eks:eu-west-1:`+account+`:cluster/example-eks"}}}]}`,
			spec(o)["assumeRolePolicyDocument"].(string))
	}
}

// TestAssociationsBindTheRightServiceAccounts: archive to the cluster's own
// ServiceAccount, restore to its dedicated one in its own namespace; a
// disabled restore renders neither Role nor association.
func TestAssociationsBindTheRightServiceAccounts(t *testing.T) {
	objs := render(t, `
global: {accountID: "`+account+`", eksClusterName: example-eks, region: eu-west-1, rolePrefix: example}
bucket: {name: `+bucket+`}
clusters:
  - {name: a, namespace: ns, restore: {namespace: drill, createServiceAccount: true}}
  - {name: b, namespace: ns, archive: {serviceAccount: b-sa}, restore: {enabled: false}}
`)

	type bind struct{ ns, sa, role string }

	got := map[string]bind{}
	kinds := map[string]int{}

	for _, o := range objs {
		kinds[o["kind"].(string)]++

		if o["kind"] == "PodIdentityAssociation" {
			s := spec(o)
			got[name(o)] = bind{s["namespace"].(string), s["serviceAccount"].(string), s["roleARN"].(string)}
		}
	}

	arn := func(r string) string { return "arn:aws:iam::" + account + ":role/" + r }

	assert.Equal(t, map[string]bind{
		"example-a-archive": {"ns", "a", arn("example-a-archive")},
		"example-a-restore": {"drill", "a-restore", arn("example-a-restore")},
		"example-b-archive": {"ns", "b-sa", arn("example-b-archive")},
	}, got)
	assert.Equal(t, 3, kinds["Role"])
	assert.Equal(t, 1, kinds["ServiceAccount"])
}

func TestNothingListedRendersNothing(t *testing.T) {
	assert.Empty(t, render(t, "{}"))
}

// TestArchiveAssociationCanBeSkipped: archive.association=false renders the
// archive Role and no archive PodIdentityAssociation (the restore pair is
// unchanged); the default renders both.
func TestArchiveAssociationCanBeSkipped(t *testing.T) {
	kinds := func(assoc string) map[string]bool {
		objs := render(t, `
global: {accountID: "`+account+`", eksClusterName: example-eks, region: eu-west-1, rolePrefix: example}
bucket: {name: `+bucket+`}
clusters:
  - {name: app-pg, namespace: app`+assoc+`}
`)
		got := map[string]bool{}
		for _, o := range objs {
			got[fmt.Sprintf("%v/%s", o["kind"], name(o))] = true
		}

		return got
	}

	def := kinds("")
	assert.True(t, def["PodIdentityAssociation/example-app-pg-archive"])

	skip := kinds(", archive: {association: false}")
	assert.True(t, skip["Role/example-app-pg-archive"])
	assert.False(t, skip["PodIdentityAssociation/example-app-pg-archive"])
	assert.True(t, skip["PodIdentityAssociation/example-app-pg-restore"])
	assert.Len(t, skip, len(def)-1)
}

func adoption(o map[string]any) any {
	md := o["metadata"].(map[string]any)
	a, _ := md["annotations"].(map[string]any)

	return a["services.k8s.aws/adoption-policy"]
}

// Roles are adopt-or-create (their names are unique); associations are not
// adopted unless global.adoptAssociations is set, because an adopted
// association of the same (cluster, namespace, ServiceAccount) is deleted in
// AWS when the legacy object that also owns it is pruned.
func TestAssociationsAreNotAdoptedByDefault(t *testing.T) {
	for _, tc := range []struct {
		extra string
		want  any
	}{
		{"", nil},
		{", adoptAssociations: false", nil},
		{", adoptAssociations: true", "adopt-or-create"},
	} {
		objs := render(t, `
global: {accountID: "`+account+`", eksClusterName: example-eks, region: eu-west-1, rolePrefix: example`+tc.extra+`}
bucket: {name: `+bucket+`}
clusters:
  - {name: app-pg, namespace: app}
`)

		var roles, assocs int

		for _, o := range objs {
			switch o["kind"] {
			case "Role":
				roles++

				assert.Equal(t, "adopt-or-create", adoption(o), "role %s%s", name(o), tc.extra)
			case "PodIdentityAssociation":
				assocs++

				assert.Equal(t, tc.want, adoption(o), "association %s%s", name(o), tc.extra)
			}
		}

		assert.Equal(t, 2, roles)
		assert.Equal(t, 2, assocs)
	}
}

// TestRoleNamesAddRolesBesideTheDefaults: roleNames renders one more Role per
// purpose under the explicit name, with the same trust and the library's
// policy; the default Roles and their associations are exactly as before. The
// names come from backupnaming.TenantRoleName, and the bucket-side library
// (ProjectCluster.Extra*RoleNames) admits the same ARNs the chart creates.
func TestRoleNamesAddRolesBesideTheDefaults(t *testing.T) {
	archive, err := backupnaming.TenantRoleName("prod", "acme", "shop", "eu", "", backupnaming.RoleClassArchive)
	require.NoError(t, err)

	restore, err := backupnaming.TenantRoleName("prod", "acme", "shop", "eu", "", backupnaming.RoleClassRestore)
	require.NoError(t, err)

	objs := render(t, `
global: {accountID: "`+account+`", eksClusterName: example-eks, region: eu-west-1, rolePrefix: prod-pg}
bucket: {name: `+bucket+`}
clusters:
  - name: shop-pg
    namespace: shop
    roleNames: {archive: `+archive+`, restore: `+restore+`}
`)

	roles := map[string]map[string]any{}
	assoc := map[string]string{}

	for _, o := range objs {
		switch o["kind"] {
		case "Role":
			roles[name(o)] = o
		case "PodIdentityAssociation":
			assoc[name(o)] = spec(o)["roleARN"].(string)
		}
	}

	arn := func(r string) string { return "arn:aws:iam::" + account + ":role/" + r }

	require.Len(t, roles, 4)

	for _, r := range []string{"prod-pg-shop-pg-archive", "prod-pg-shop-pg-restore", archive, restore} {
		require.Contains(t, roles, r)
		assert.Equal(t, r, spec(roles[r])["name"])
	}

	// The associations stay on the default Roles.
	assert.Equal(t, map[string]string{
		"prod-pg-shop-pg-archive": arn("prod-pg-shop-pg-archive"),
		"prod-pg-shop-pg-restore": arn("prod-pg-shop-pg-restore"),
	}, assoc)

	// Same trust and policy as the default Role of the purpose (the policy
	// names the bucket and the prefix, not the role).
	for _, pair := range [][2]string{{"prod-pg-shop-pg-archive", archive}, {"prod-pg-shop-pg-restore", restore}} {
		def, ext := spec(roles[pair[0]]), spec(roles[pair[1]])
		assert.Equal(t, def["assumeRolePolicyDocument"], ext["assumeRolePolicyDocument"])
		assert.Equal(t, def["inlinePolicies"], ext["inlinePolicies"])
	}

	// The bucket side admits both names of each purpose, on the chart's prefix.
	built, err := backupstore.BuildProjectBuckets(&backupstore.ProjectBucketsArgs{
		BucketPrefix: "example", Env: "prod", Region: "eu-west-1", SessionClusterName: "example-eks",
		SourceAccountID: account, BackupAccountID: "210987654321", Lifecycle: backupstore.Lifecycle{RetentionDays: 30},
		Projects: []backupstore.Project{{Name: "shop", Clusters: []backupstore.ProjectCluster{{
			Name: "shop-pg", Namespace: "shop", ExtraArchiveRoleNames: []string{archive}, ExtraRestoreRoleNames: []string{restore},
		}}}},
	})
	require.NoError(t, err)

	admitted := map[string]string{}
	for _, tn := range built[0].Args.Tenants {
		admitted[tn.RoleARN] = tn.Prefix
	}

	for r := range roles {
		assert.Equal(t, "shop/shop-pg/", admitted[arn(r)], "the bucket does not admit %s", r)
	}
}

// TestBindExplicitMovesTheAssociation: the ServiceAccount's association is
// bound to the explicit Role of the purpose, and the default Role is rendered
// without one (EKS allows one association per ServiceAccount).
func TestBindExplicitMovesTheAssociation(t *testing.T) {
	for _, tc := range []struct {
		names string
		want  map[string]string
	}{
		{"{archive: a.pg-archive, restore: a.pg-restore, bindExplicit: true}", map[string]string{"a.pg-archive": "app", "a.pg-restore": "app-restore"}},
		// Only the purposes that have an explicit name move.
		{"{archive: a.pg-archive, bindExplicit: true}", map[string]string{"a.pg-archive": "app", "example-app-restore": "app-restore"}},
		{"{archive: a.pg-archive}", map[string]string{"example-app-archive": "app", "example-app-restore": "app-restore"}},
	} {
		objs := render(t, `
global: {accountID: "`+account+`", eksClusterName: example-eks, region: eu-west-1, rolePrefix: example, useRoleRef: true}
bucket: {name: `+bucket+`}
clusters:
  - {name: app, namespace: ns, roleNames: `+tc.names+`}
`)

		got := map[string]string{}

		for _, o := range objs {
			if o["kind"] == "PodIdentityAssociation" {
				got[name(o)] = spec(o)["serviceAccount"].(string)

				assert.Equal(t, name(o), spec(o)["roleRef"].(map[string]any)["from"].(map[string]any)["name"])
			}
		}

		assert.Equal(t, tc.want, got, tc.names)
	}
}

// TestNoRoleNamesRendersNoExtraRole: the default render has exactly the Roles
// of the cluster list, none under an explicit name.
func TestNoRoleNamesRendersNoExtraRole(t *testing.T) {
	roles := 0

	for _, o := range render(t, `
global: {accountID: "`+account+`", eksClusterName: example-eks, region: eu-west-1, rolePrefix: example}
bucket: {name: `+bucket+`}
clusters: [{name: app, namespace: ns, roleNames: {}}]
`) {
		if o["kind"] == "Role" {
			roles++
		}
	}

	assert.Equal(t, 2, roles)
}
