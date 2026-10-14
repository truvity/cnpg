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
