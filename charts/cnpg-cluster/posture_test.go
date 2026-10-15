// Copyright 2026 Truvity B.V.. All rights reserved.
// SPDX-License-Identifier: MIT

package chart_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func hbaOf(t *testing.T, docs map[string][]map[string]any) []any {
	t.Helper()
	require.Len(t, docs["Cluster"], 1)

	hba, _ := field(docs["Cluster"][0], "spec", "postgresql", "pg_hba").([]any)

	return hba
}

func hbaValues() map[string]any {
	return map[string]any{
		"clusterName":   "dms-pg",
		"profile":       "devel",
		"namespace":     "dms",
		"peopleProject": "dms",
		"bootstrap":     map[string]any{"initdb": map[string]any{"database": "dms", "owner": "dms"}},
	}
}

const (
	hbaOwner  = "hostssl all dms all scram-sha-256"
	hbaApp    = "hostssl all dms_app all scram-sha-256"
	hbaPeople = "hostssl all dms_admin,dms_observer,dms_read all cert clientname=DN map=people"
	hbaCatch  = "hostssl all all all cert"
)

// TestHBA_PasswordRoleOrder pins the order that broke production once
// (a people line above an application's scram line): the owner's line, the
// password roles' lines, then the people line, then the catch-all. The
// external form (another release owns the DatabaseRole) and the hand-written
// beforeCatchAll form must render the SAME list.
func TestHBA_PasswordRoleOrder(t *testing.T) {
	want := []any{hbaOwner, hbaApp, hbaPeople, hbaCatch}

	v := hbaValues()
	v["roles"] = []any{map[string]any{"name": "dms_app", "auth": "password", "external": true}}
	docs, err := renderDocs(t, v)
	require.NoError(t, err)
	assert.Equal(t, want, hbaOf(t, docs), "roles[].auth=password external")
	assert.Empty(t, docs["DatabaseRole"][3:], "an external role renders no DatabaseRole") // only the three people roles

	v = hbaValues()
	v["postgresql"] = map[string]any{"pgHba": map[string]any{"beforeCatchAll": []any{hbaApp}}}
	docs, err = renderDocs(t, v)
	require.NoError(t, err)
	assert.Equal(t, want, hbaOf(t, docs), "beforeCatchAll")

	// A password role with its own DatabaseRole sits in the same place.
	v = hbaValues()
	v["roles"] = []any{map[string]any{"name": "dms_app", "auth": "password", "passwordSecret": "dms-app-pw"}}
	docs, err = renderDocs(t, v)
	require.NoError(t, err)
	assert.Equal(t, want, hbaOf(t, docs), "roles[].auth=password with a Secret")
}

func TestHBA_ExternalRoleRules(t *testing.T) {
	v := hbaValues()
	v["roles"] = []any{map[string]any{"name": "dms_app", "auth": "cert", "external": true}}
	_, err := renderDocs(t, v)
	require.ErrorContains(t, err, "external is for auth=password only")

	// A password role that is not external still needs its Secret.
	v = hbaValues()
	v["roles"] = []any{map[string]any{"name": "dms_app", "auth": "password"}}
	_, err = renderDocs(t, v)
	require.ErrorContains(t, err, "passwordSecret")

	// The people line cannot shadow an external role of the same name.
	v = hbaValues()
	v["roles"] = []any{map[string]any{"name": "dms_read", "auth": "password", "external": true}}
	_, err = renderDocs(t, v)
	require.ErrorContains(t, err, "dms_read")
}

func postureValues() map[string]any {
	v := hbaValues()
	v["profile"] = "prod"
	v["posture"] = "platform"
	v["roles"] = []any{map[string]any{"name": "dms_app", "auth": "password", "external": true}}

	return v
}

func TestPosture_Platform(t *testing.T) {
	docs, err := renderDocsAPI(t, postureValues(), "monitoring.coreos.com/v1/PodMonitor")
	require.NoError(t, err)

	c := docs["Cluster"][0]
	assert.Equal(t, "durable", field(c, "spec", "affinity", "nodeSelector", "karpenter.sh/nodepool"))
	assert.NotNil(t, field(c, "spec", "certificates", "replicationTLSSecret"), "replication on")
	assert.Empty(t, docs["PodMonitor"], "the platform scrapes the pods itself")
	assert.Len(t, docs["ClusterIssuer"], 1, "trust.ca")
	assert.Len(t, docs["CertificateRequestPolicy"], 1, "trust.policy")
	assert.Len(t, docs["Bundle"], 1, "trust.bundle")
	assert.Equal(t, []any{hbaOwner, hbaApp, hbaPeople, hbaCatch}, hbaOf(t, docs))
}

// TestPosture_ExplicitWins: every field the posture fills yields to an
// explicit value, `false` included.
func TestPosture_ExplicitWins(t *testing.T) {
	v := postureValues()
	v["replication"] = map[string]any{"enabled": false}
	v["monitoring"] = map[string]any{"podMonitor": map[string]any{"enabled": true}}
	v["trust"] = map[string]any{
		"namespace": "certs",
		"ca":        map[string]any{"enabled": false},
		"policy":    map[string]any{"enabled": false},
		"bundle":    map[string]any{"enabled": false},
	}
	v["scheduling"] = map[string]any{"databasePool": "other"}

	docs, err := renderDocsAPI(t, v, "monitoring.coreos.com/v1/PodMonitor")
	require.NoError(t, err)

	c := docs["Cluster"][0]
	assert.Equal(t, "other", field(c, "spec", "affinity", "nodeSelector", "karpenter.sh/nodepool"))
	assert.Nil(t, field(c, "spec", "certificates", "replicationTLSSecret"))
	assert.Len(t, docs["PodMonitor"], 1)
	assert.Empty(t, docs["ClusterIssuer"])
	assert.Empty(t, docs["CertificateRequestPolicy"])
	assert.Empty(t, docs["Bundle"])
}

func TestPosture_UnknownIsRefused(t *testing.T) {
	v := postureValues()
	v["posture"] = "plattform"
	_, err := renderDocs(t, v)
	require.Error(t, err)
}

// TestPosture_OffIsUnchanged: with no posture the PodMonitor stays on by
// default and nothing else appears.
func TestPosture_OffIsUnchanged(t *testing.T) {
	docs, err := renderDocsAPI(t, hbaValues(), "monitoring.coreos.com/v1/PodMonitor")
	require.NoError(t, err)
	assert.Len(t, docs["PodMonitor"], 1)
	assert.Empty(t, docs["ClusterIssuer"])
	assert.Nil(t, field(docs["Cluster"][0], "spec", "certificates", "replicationTLSSecret"))
}
