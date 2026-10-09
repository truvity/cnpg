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

	for _, n := range []string{"dms_admin", "dms_observer", "dms_read"} {
		require.Contains(t, got, n)
		assert.Equal(t, true, got[n]["login"], n)
		assert.NotContains(t, got[n], "passwordSecret")
	}

	// The dropped ddl level: the retained v2.17.x role is marked for the
	// drop (reclaim policy delete); the operator refuses ensure: absent.
	require.Contains(t, got, "dms_ddl")
	assert.Equal(t, "present", got["dms_ddl"]["ensure"])
	assert.Equal(t, "delete", got["dms_ddl"]["databaseRoleReclaimPolicy"])
	assert.Equal(t, "present", got["dms_read"]["ensure"])

	assert.Equal(t, []any{"dms"}, got["dms_admin"]["inRoles"])
	assert.Equal(t, []any{"pg_read_all_data"}, got["dms_read"]["inRoles"])

	pg := docs["Cluster"][0]["spec"].(map[string]any)["postgresql"].(map[string]any)
	hba := pg["pg_hba"].([]any)
	people := "hostssl all dms_admin,dms_observer,dms_read all cert clientname=DN map=people"
	assert.Contains(t, hba, people)
	assert.Less(t, indexOf(hba, people), indexOf(hba, "hostssl all all all cert"))
	// Both DN orders (PostgreSQL 18 prints CN first, 17 OU first).
	assert.Equal(t, []any{
		`people "/^CN=[^,\\]+,OU=(dms_admin|dms_observer|dms_read)$" \1`,
		`people "/^OU=(dms_admin|dms_observer|dms_read),CN=[^,\\]+$" \1`,
	}, pg["pg_ident"])
}

// The incident of 2.17.0: the people line used a regex that matched dms_app
// and sat before the caller's own line. It must name only the three roles and
// come after the caller's beforeCatchAll lines.
func TestRoles_PeopleLineNamesThreeRolesAfterCallerLines(t *testing.T) {
	docs, err := renderDocs(t, map[string]any{
		"clusterName":   "dms-pg",
		"profile":       "devel",
		"namespace":     "dms",
		"peopleProject": "dms",
		"bootstrap":     map[string]any{"initdb": map[string]any{"database": "dms", "owner": "dms"}},
		"postgresql":    map[string]any{"pgHba": map[string]any{"beforeCatchAll": []any{"hostssl all dms_app all scram-sha-256"}}},
	})
	require.NoError(t, err)
	hba := docs["Cluster"][0]["spec"].(map[string]any)["postgresql"].(map[string]any)["pg_hba"].([]any)
	people := "hostssl all dms_admin,dms_observer,dms_read all cert clientname=DN map=people"
	assert.Equal(t, []any{
		"hostssl all dms all scram-sha-256",
		"hostssl all dms_app all scram-sha-256",
		people,
		"hostssl all all all cert",
	}, hba)
	for _, l := range hba {
		assert.NotContains(t, l, "/^")
	}
}

func TestRoles_PeopleProjectRefusesCollidingRole(t *testing.T) {
	base := func() map[string]any {
		return map[string]any{
			"clusterName":   "dms-pg",
			"profile":       "devel",
			"namespace":     "dms",
			"peopleProject": "dms",
			"bootstrap":     map[string]any{"initdb": map[string]any{"database": "dms", "owner": "dms"}},
		}
	}
	v := base()
	v["roles"] = []any{map[string]any{"name": "dms-read", "auth": "cert"}}
	_, err := renderDocs(t, v)
	require.ErrorContains(t, err, "dms_read")

	v = base()
	v["postgresql"] = map[string]any{"pgHba": map[string]any{"beforeCatchAll": []any{"hostssl all dms_observer all scram-sha-256"}}}
	_, err = renderDocs(t, v)
	require.ErrorContains(t, err, "dms_observer")

	v = base()
	v["bootstrap"] = map[string]any{"initdb": map[string]any{"database": "dms", "owner": "dms_admin"}}
	_, err = renderDocs(t, v)
	require.ErrorContains(t, err, "dms_admin")
}

func indexOf(l []any, v string) int {
	for i, x := range l {
		if x == v {
			return i
		}
	}

	return -1
}
