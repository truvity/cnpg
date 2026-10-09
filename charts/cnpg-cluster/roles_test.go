// Copyright 2026 Truvity B.V.. All rights reserved.
// SPDX-License-Identifier: MIT

package chart_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRoles_PeopleRolesHaveNoCredential(t *testing.T) {
	docs, err := renderDocs(t, map[string]any{
		"clusterName": "pg",
		"profile":     "devel",
		"namespace":   "ns",
		"roles": []any{
			map[string]any{"name": "people_read_all", "auth": "none", "inRoles": []any{"pg_read_all_data"}},
			map[string]any{"name": "people_grp", "auth": "none", "login": false},
		},
		"people": []any{map[string]any{"email": "ada@example.com", "role": "people_read_all"}},
	})
	require.NoError(t, err)

	byName := map[string]map[string]any{}
	for _, d := range docs["DatabaseRole"] {
		byName[d["metadata"].(map[string]any)["name"].(string)] = d["spec"].(map[string]any)
	}

	require.Contains(t, byName, "pg-people-read-all")
	assert.Equal(t, "people_read_all", byName["pg-people-read-all"]["name"])
	assert.Equal(t, true, byName["pg-people-read-all"]["login"])
	assert.NotContains(t, byName["pg-people-read-all"], "passwordSecret")
	assert.NotContains(t, byName["pg-people-read-all"], "clientCertificate")
	assert.Equal(t, []any{"pg_read_all_data"}, byName["pg-people-read-all"]["inRoles"])

	require.Contains(t, byName, "pg-people-grp")
	assert.Equal(t, false, byName["pg-people-grp"]["login"])
}

func TestRoles_PeopleProjectConvention(t *testing.T) {
	docs, err := renderDocs(t, map[string]any{
		"clusterName":   "dms-pg",
		"profile":       "devel",
		"namespace":     "dms",
		"peopleProject": "dms",
		"bootstrap":     map[string]any{"initdb": map[string]any{"database": "dms", "owner": "dms"}},
	})
	require.NoError(t, err)

	got := map[string]map[string]any{}
	for _, d := range docs["DatabaseRole"] {
		spec := d["spec"].(map[string]any)
		got[spec["name"].(string)] = spec
	}

	for _, n := range []string{"dms_admin", "dms_ddl", "dms_observer", "dms_read"} {
		require.Contains(t, got, n)
		assert.Equal(t, true, got[n]["login"], n)
		assert.NotContains(t, got[n], "passwordSecret")
	}

	assert.Equal(t, []any{"dms"}, got["dms_admin"]["inRoles"])
	assert.Equal(t, []any{"pg_read_all_data"}, got["dms_read"]["inRoles"])

	pg := docs["Cluster"][0]["spec"].(map[string]any)["postgresql"].(map[string]any)
	hba := pg["pg_hba"].([]any)
	assert.Contains(t, hba, "hostssl all /^dms_.*$ all cert clientname=DN map=people")
	assert.Less(t, indexOf(hba, "hostssl all /^dms_.*$ all cert clientname=DN map=people"), indexOf(hba, "hostssl all all all cert"))
	assert.Equal(t, []any{`people "/^OU=(dms_admin|dms_ddl|dms_observer|dms_read),CN=[^,\\]+$" \1`}, pg["pg_ident"])
}

func indexOf(l []any, v string) int {
	for i, x := range l {
		if x == v {
			return i
		}
	}

	return -1
}
