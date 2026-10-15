package verify

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type fullFake struct {
	fake
	pods    map[string]string
	nodes   map[string]map[string]string
	objs    []*unstructured.Unstructured
	podsErr error
}

func (f fullFake) InstanceNodes(context.Context, string, string) (map[string]string, error) {
	return f.pods, f.podsErr
}

func (f fullFake) NodeLabels(_ context.Context, n string) (map[string]string, error) {
	if l, ok := f.nodes[n]; ok {
		return l, nil
	}
	return nil, ErrNotFound
}

func (f fullFake) DeclarativeObjects(context.Context, string, string) ([]*unstructured.Unstructured, error) {
	return f.objs, nil
}

func obj(kind, name string, status map[string]any) *unstructured.Unstructured {
	o := map[string]any{"kind": kind, "metadata": map[string]any{"name": name}}
	if status != nil {
		o["status"] = status
	}
	return &unstructured.Unstructured{Object: o}
}

func runWith(t *testing.T, src Source, opt Options) Report {
	t.Helper()
	opt.Namespace, opt.Cluster, opt.Now = "db", "pg", now
	rep, err := Run(context.Background(), src, opt)
	require.NoError(t, err)
	return rep
}

func find(r Report, name string) *Result {
	for i := range r.Results {
		if r.Results[i].Name == name {
			return &r.Results[i]
		}
	}
	return nil
}

func base(t *testing.T) fullFake {
	return fullFake{fake: fake{cluster: healthy(), secrets: goodSecrets(t)}}
}

func TestPlacement(t *testing.T) {
	pool := map[string]map[string]string{
		"n1": {"pool": "database"}, "n2": {"pool": "database"}, "n3": {"pool": "general"},
	}
	opt := Options{PoolLabel: "pool=database"}

	f := base(t)
	f.nodes = pool
	f.pods = map[string]string{"pg-1": "n1", "pg-2": "n2"}
	r := find(runWith(t, f, opt), namePlacement)
	require.NotNil(t, r)
	assert.Equal(t, Pass, r.Status, r.Message)

	f.pods = map[string]string{"pg-1": "n1", "pg-2": "n3"}
	r = find(runWith(t, f, opt), namePlacement)
	assert.Equal(t, Fail, r.Status)
	assert.Contains(t, r.Message, "pg-2 on node n3")
	assert.NotContains(t, r.Message, "pg-1")

	f.pods = map[string]string{"pg-1": ""}
	assert.Contains(t, find(runWith(t, f, opt), namePlacement).Message, "not scheduled")

	f.pods = map[string]string{"pg-1": "gone"}
	assert.Contains(t, find(runWith(t, f, opt), namePlacement).Message, "not found")

	f.pods = map[string]string{}
	assert.Equal(t, Fail, find(runWith(t, f, opt), namePlacement).Status)

	f.podsErr = errors.New("forbidden")
	assert.Contains(t, find(runWith(t, f, opt), namePlacement).Message, "forbidden")
}

func TestPlacementOffByDefaultAndBadInput(t *testing.T) {
	f := base(t)
	assert.Nil(t, find(runWith(t, f, Options{}), namePlacement))
	assert.Nil(t, find(runWith(t, f, Options{}), nameApplied))

	r := find(runWith(t, f, Options{PoolLabel: "nokey"}), namePlacement)
	assert.Equal(t, Fail, r.Status)

	// A Source that predates the optional interfaces fails, it does not panic.
	plain := fake{cluster: healthy(), secrets: goodSecrets(t)}
	assert.Equal(t, Fail, find(runWith(t, plain, Options{PoolLabel: "a=b"}), namePlacement).Status)
	assert.Equal(t, Fail, find(runWith(t, plain, Options{CheckApplied: true}), nameApplied).Status)
}

func TestApplied(t *testing.T) {
	opt := Options{CheckApplied: true}
	f := base(t)
	f.objs = []*unstructured.Unstructured{
		obj("Database", "pg-app", map[string]any{"applied": true}),
		obj("DatabaseRole", "pg-role-a", map[string]any{"applied": true}),
	}
	r := find(runWith(t, f, opt), nameApplied)
	assert.Equal(t, Pass, r.Status, r.Message)

	f.objs = append(f.objs,
		obj("Database", "pg-bad", map[string]any{"applied": false, "message": "role missing"}),
		obj("DatabaseRole", "pg-new", nil),
		obj("DatabaseRole", "pg-silent", map[string]any{"applied": false}))
	r = find(runWith(t, f, opt), nameApplied)
	assert.Equal(t, Fail, r.Status)
	assert.Contains(t, r.Message, "Database/pg-bad (applied false: role missing)")
	assert.Contains(t, r.Message, "DatabaseRole/pg-new (no status.applied yet)")
	assert.Contains(t, r.Message, "DatabaseRole/pg-silent (applied false)")
	assert.NotContains(t, r.Message, "pg-app")
	assert.NotContains(t, r.Message, "pg-role-a")

	f.objs = nil
	assert.Equal(t, Skip, find(runWith(t, f, opt), nameApplied).Status)
}
