package backupplan_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/truvity/cnpg/v2/pkg/backupplan"
)

func TestPhaseEffects(t *testing.T) {
	for _, tc := range []struct {
		phase                                       backupplan.Phase
		onNewPath, associated, legacyBinds, isValid bool
	}{
		{backupplan.Legacy, false, false, true, true},
		{backupplan.Prepare, true, false, true, true},
		{backupplan.Detach, true, false, false, true},
		{backupplan.Switched, true, true, false, true},
		{"half", false, false, true, false},
		{"", false, false, true, false},
	} {
		t.Run(string(tc.phase), func(t *testing.T) {
			assert.Equal(t, tc.onNewPath, tc.phase.OnNewPath())
			assert.Equal(t, tc.associated, tc.phase.ArchiveAssociated())
			assert.Equal(t, tc.legacyBinds, tc.phase.LegacyBindsDatabase())
			assert.Equal(t, tc.isValid, tc.phase.Valid())
		})
	}
}

func TestParsePhase(t *testing.T) {
	for _, p := range backupplan.Phases() {
		got, err := backupplan.ParsePhase(string(p))
		require.NoError(t, err)
		assert.Equal(t, p, got)
	}

	_, err := backupplan.ParsePhase("half")
	assert.ErrorContains(t, err, `"half"`)
	assert.ErrorContains(t, err, "switched")
}

func TestBuildAccessFollowsThePhase(t *testing.T) {
	for _, tc := range []struct {
		phase       backupplan.Phase
		row         bool
		association bool
	}{
		{backupplan.Legacy, false, false},
		{backupplan.Prepare, true, false},
		{backupplan.Detach, true, false},
		{backupplan.Switched, true, true},
	} {
		t.Run(string(tc.phase), func(t *testing.T) {
			rows, err := backupplan.BuildAccess(backupplan.AccessArgs{
				BucketPrefix: "acme-db", Env: "prod", Region: "eu-west-1",
				Projects: []backupplan.Project{{
					Name:     "shop",
					Clusters: []backupplan.Cluster{{Name: "shop-pg", Namespace: "shop"}},
					Phase:    tc.phase,
				}},
			})
			require.NoError(t, err)

			if !tc.row {
				assert.Empty(t, rows)

				return
			}

			assert.Equal(t, []backupplan.AccessRow{{
				Project:    "shop",
				Bucket:     "acme-db-shop-prod-eu-west-1",
				RolePrefix: "prod-pg",
				Phase:      tc.phase,
				Clusters:   []backupplan.AccessCluster{{Name: "shop-pg", Namespace: "shop", Association: tc.association}},
			}}, rows)
		})
	}
}

func TestBuildAccessKeepsOrderAndSkipsLegacy(t *testing.T) {
	c := func(n string) []backupplan.Cluster { return []backupplan.Cluster{{Name: n + "-pg", Namespace: n}} }

	rows, err := backupplan.BuildAccess(backupplan.AccessArgs{
		BucketPrefix: "acme-db", Env: "stage", Region: "eu-west-1",
		Projects: []backupplan.Project{
			{Name: "zeta", Clusters: c("zeta"), Phase: backupplan.Switched},
			{Name: "old", Clusters: c("old"), Phase: backupplan.Legacy},
			{Name: "alpha", Clusters: append(c("alpha"), backupplan.Cluster{Name: "alpha-b", Namespace: "alpha"}), Phase: backupplan.Prepare},
		},
	})
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Equal(t, "zeta", rows[0].Project)
	assert.Equal(t, "alpha", rows[1].Project)
	assert.Len(t, rows[1].Clusters, 2)
	assert.True(t, rows[0].Clusters[0].Association)
	assert.False(t, rows[1].Clusters[0].Association)
}

func TestBuildAccessNoProjects(t *testing.T) {
	rows, err := backupplan.BuildAccess(backupplan.AccessArgs{BucketPrefix: "acme-db", Env: "stage", Region: "eu-west-1"})
	require.NoError(t, err)
	assert.Nil(t, rows)
}

func TestBuildAccessErrors(t *testing.T) {
	cl := []backupplan.Cluster{{Name: "x-pg", Namespace: "x"}}

	_, err := backupplan.BuildAccess(backupplan.AccessArgs{
		BucketPrefix: "acme-db", Env: "stage", Region: "eu-west-1",
		Projects: []backupplan.Project{{Name: "x", Clusters: cl, Phase: "half"}},
	})
	assert.ErrorContains(t, err, "not valid")

	_, err = backupplan.BuildAccess(backupplan.AccessArgs{
		BucketPrefix: "acme-db", Env: "stage", Region: "eu-west-1",
		Projects: []backupplan.Project{{Name: "Bad_Name", Clusters: cl, Phase: backupplan.Prepare}},
	})
	assert.Error(t, err)

	// A legacy project is never named into a bucket, so it cannot fail on one.
	_, err = backupplan.BuildAccess(backupplan.AccessArgs{
		BucketPrefix: "acme-db", Env: "stage", Region: "eu-west-1",
		Projects: []backupplan.Project{{Name: "Bad_Name", Clusters: cl, Phase: backupplan.Legacy}},
	})
	assert.NoError(t, err)
}
