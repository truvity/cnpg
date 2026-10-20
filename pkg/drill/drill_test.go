// Copyright 2026 Truvity B.V.. All rights reserved.
// SPDX-License-Identifier: MIT

package drill

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

var now = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

const (
	srcNS    = "shop"
	srcName  = "shop-pg"
	drillNS  = "drills"
	drillNm  = "shop-pg-restore"
	storeSrc = "shop-pg-objectstore"
)

func sourceCluster() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "postgresql.cnpg.io/v1", "kind": "Cluster",
		"metadata": map[string]any{"name": srcName, "namespace": srcNS},
		"spec": map[string]any{
			"instances": int64(3),
			"storage":   map[string]any{"size": "10Gi", "storageClass": "fast-delete"},
			"affinity": map[string]any{
				"nodeSelector": map[string]any{"pool": "database"},
				"tolerations":  []any{map[string]any{"key": "pool", "operator": "Equal", "value": "database", "effect": "NoSchedule"}},
			},
			"bootstrap": map[string]any{"initdb": map[string]any{"database": "shop", "owner": "shop"}},
			"plugins": []any{map[string]any{
				"name": PluginName, "enabled": true, "isWALArchiver": true,
				"parameters": map[string]any{"barmanObjectName": storeSrc, "serverName": "shop-pg-g2"},
			}},
		},
		"status": map[string]any{"image": "registry.example.com/postgresql:17.6"},
	}}
}

func sourceStore() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "barmancloud.cnpg.io/v1", "kind": "ObjectStore",
		"metadata": map[string]any{"name": storeSrc, "namespace": srcNS},
		"spec": map[string]any{
			"configuration": map[string]any{
				"destinationPath": "s3://example-backups/shop/shop-pg",
				"s3Credentials":   map[string]any{"inheritFromIAMRole": true},
				"data":            map[string]any{"encryption": "AES256"},
				"wal":             map[string]any{"encryption": "AES256"},
				"tags":            map[string]any{"cnpg-backup-retention": "managed"},
			},
		},
	}}
}

func database(name, cluster, ensure string) *unstructured.Unstructured {
	spec := map[string]any{"name": name, "cluster": map[string]any{"name": cluster}}
	if ensure != "" {
		spec["ensure"] = ensure
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "postgresql.cnpg.io/v1", "kind": "Database",
		"metadata": map[string]any{"name": cluster + "-" + name, "namespace": srcNS},
		"spec":     spec,
	}}
}

func fixture() *fakeKube {
	f := newFakeKube()
	f.put(ClusterGVR, sourceCluster())
	f.put(ObjectStoreGVR, sourceStore())
	f.put(DatabaseGVR, database("orders", srcName, ""))
	f.put(DatabaseGVR, database("gone", srcName, "absent"))
	f.put(DatabaseGVR, database("other", "another-pg", ""))
	f.pvcs = []corev1.PersistentVolumeClaim{{
		ObjectMeta: metav1.ObjectMeta{Name: srcName + "-1", Namespace: srcNS, Labels: map[string]string{labelCluster: srcName, "cnpg.io/pvcRole": "PG_DATA"}},
		Status:     corev1.PersistentVolumeClaimStatus{Capacity: corev1.ResourceList{corev1.ResourceStorage: qty("20Gi")}},
	}}
	f.reclaim["fast-delete"] = "Delete"
	f.reclaim["fast-retain"] = "Retain"
	return f
}

func healthyDB() *fakeProber {
	return &fakeProber{
		dbs: map[string][]Table{
			"shop":   {{Schema: "public", Name: "carts", Estimate: 5}, {Schema: "public", Name: "items", Estimate: 0}},
			"orders": {{Schema: "public", Name: "orders", Estimate: 42}},
		},
		label: "START WAL LOCATION: 0/2000028 (file 000000010000000000000002)\nSTART TIME: 2026-10-20 02:00:01 UTC\nLABEL: Barman backup cloud 20261020T020001\n",
	}
}

func opts() Options {
	return Options{
		Namespace: srcNS, Cluster: srcName, DrillNamespace: drillNS,
		PollInterval: time.Millisecond, Timeout: 5 * time.Second, TeardownTimeout: 2 * time.Second,
		Now: func() time.Time { return now },
	}
}

func runDrill(t *testing.T, f *fakeKube, p *fakeProber, o Options) (*Report, string, error) {
	t.Helper()
	var out bytes.Buffer
	rep, err := Run(context.Background(), Deps{Kube: f, Out: &out, NewProber: func(ConnInfo) (Prober, error) { return p, nil }}, o)
	return rep, out.String(), err
}

func step(t *testing.T, rep *Report, name string) Step {
	t.Helper()
	for _, s := range rep.Steps {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("no step %q in %+v", name, rep.Steps)
	return Step{}
}

func TestDrillSucceeds(t *testing.T) {
	f := fixture()
	rep, out, err := runDrill(t, f, healthyDB(), opts())
	require.NoError(t, err)
	assert.False(t, rep.Failed(), "%+v\n%s", rep.Steps, out)

	assert.Equal(t, []string{"clusters/drills/shop-pg-restore", "objectstores/drills/shop-pg-restore-source"}, f.createdKinds())
	assert.ElementsMatch(t, []string{"clusters/drills/shop-pg-restore", "objectstores/drills/shop-pg-restore-source"}, f.deleted, "teardown deletes both")
	assert.Equal(t, Pass, step(t, rep, "teardown").Status)

	// The recovery point is the last replayed transaction, 5 minutes old.
	require.NotNil(t, rep.RecoveryPointAgeSeconds)
	assert.InDelta(t, 300-0.123456, *rep.RecoveryPointAgeSeconds, 0.001)
	assert.Equal(t, "last replayed transaction", rep.RecoveryPointSource)
	assert.Equal(t, "s3://example-backups/shop/shop-pg", rep.DestinationPath)
	assert.Equal(t, "shop-pg-g2", rep.ServerName)
	// orders from the Database object, shop from bootstrap.initdb; gone
	// (ensure absent) and other (another cluster) are not expected.
	assert.Equal(t, []DatabaseResult{{Name: "orders", Tables: 1, Rows: 42}, {Name: "shop", Tables: 2, Rows: 5}}, rep.Databases)

	cm := f.cms[drillNS+"/"+ResultName(drillNm)]
	require.NotNil(t, cm)
	assert.Equal(t, "1", cm.Data[KeyLastResult])
	assert.Equal(t, cm.Data[KeyLastRun], cm.Data[KeyLastSuccess])
	assert.Equal(t, "300", cm.Data[KeyRecoveryPointAge])
	assert.Equal(t, srcNS, cm.Labels[LabelSourceNamespace])
	assert.Contains(t, cm.Data[KeyReport], `"sourceCluster":"shop-pg"`)
}

func TestTheDrillClusterIsBuiltFromTheSource(t *testing.T) {
	f := fixture()
	o, err := opts().withDefaults()
	require.NoError(t, err)
	p, err := MakePlan(context.Background(), f, o)
	require.NoError(t, err)
	c, err := p.Cluster(now)
	require.NoError(t, err)
	s, err := p.ObjectStore()
	require.NoError(t, err)
	require.NoError(t, AssertNoArchive(c, s))

	assert.Equal(t, drillNm, c.GetName())
	assert.Equal(t, drillNS, c.GetNamespace())
	assert.Equal(t, ManagedBy, c.GetLabels()[LabelManagedBy])
	spec := c.Object["spec"].(map[string]any)
	assert.NotContains(t, spec, "plugins")
	assert.NotContains(t, spec, "backup")
	assert.NotContains(t, spec, "serviceAccountName", "the plugin binds its RBAC to the ServiceAccount named after the Cluster")
	assert.Equal(t, int64(1), spec["instances"])
	assert.Equal(t, false, spec["enablePDB"])
	assert.Equal(t, "registry.example.com/postgresql:17.6", spec["imageName"])
	assert.Equal(t, map[string]any{"size": "20Gi", "storageClass": "fast-delete"}, spec["storage"], "the PVC grew past the spec")
	assert.Equal(t, map[string]any{"pool": "database"}, spec["affinity"].(map[string]any)["nodeSelector"])
	params := spec["externalClusters"].([]any)[0].(map[string]any)["plugin"].(map[string]any)["parameters"]
	assert.Equal(t, map[string]any{"barmanObjectName": "shop-pg-restore-source", "serverName": "shop-pg-g2"}, params)
	assert.Equal(t, map[string]any{"recovery": map[string]any{"source": "origin"}}, spec["bootstrap"], "no recovery target: replay to the end")

	conf := s.Object["spec"].(map[string]any)["configuration"].(map[string]any)
	assert.Equal(t, map[string]any{
		"destinationPath": "s3://example-backups/shop/shop-pg",
		"s3Credentials":   map[string]any{"inheritFromIAMRole": true},
	}, conf, "only the location: no tags, encryption or upload settings, no static credentials")
}

func TestAssertNoArchiveRefuses(t *testing.T) {
	o, err := opts().withDefaults()
	require.NoError(t, err)
	p, err := MakePlan(context.Background(), fixture(), o)
	require.NoError(t, err)
	cases := map[string]func(c, s *unstructured.Unstructured){
		"a plugin entry": func(c, _ *unstructured.Unstructured) {
			_ = unstructured.SetNestedSlice(c.Object, []any{map[string]any{"name": PluginName, "isWALArchiver": true}}, "spec", "plugins")
		},
		"a backup section": func(c, _ *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(c.Object, map[string]any{"barmanObjectStore": map[string]any{"destinationPath": "s3://x"}}, "spec", "backup")
		},
		"a replica cluster": func(c, _ *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(c.Object, map[string]any{"enabled": true, "source": "origin"}, "spec", "replica")
		},
		"an initdb bootstrap": func(c, _ *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(c.Object, map[string]any{"initdb": map[string]any{}}, "spec", "bootstrap")
		},
		"another object store": func(c, _ *unstructured.Unstructured) {
			ext := c.Object["spec"].(map[string]any)["externalClusters"].([]any)[0].(map[string]any)
			ext["plugin"].(map[string]any)["parameters"].(map[string]any)["barmanObjectName"] = storeSrc
		},
		"an in-tree barman source": func(c, _ *unstructured.Unstructured) {
			ext := c.Object["spec"].(map[string]any)["externalClusters"].([]any)[0].(map[string]any)
			ext["barmanObjectStore"] = map[string]any{"destinationPath": "s3://x"}
		},
		"a retention policy": func(_, s *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(s.Object, "30d", "spec", "retentionPolicy")
		},
		"static credentials": func(_, s *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(s.Object, map[string]any{"accessKeyId": map[string]any{"name": "s3", "key": "id"}}, "spec", "configuration", "s3Credentials")
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			c, err := p.Cluster(now)
			require.NoError(t, err)
			s, err := p.ObjectStore()
			require.NoError(t, err)
			mutate(c, s)
			assert.ErrorContains(t, AssertNoArchive(c, s), "refused")
		})
	}
}

func TestTheCheckRunsBeforeAnythingIsCreated(t *testing.T) {
	f := fixture()
	o := opts()
	o.mutate = func(c, _ *unstructured.Unstructured) {
		_ = unstructured.SetNestedSlice(c.Object, []any{map[string]any{"name": PluginName, "isWALArchiver": true}}, "spec", "plugins")
	}
	rep, _, err := runDrill(t, f, healthyDB(), o)
	require.NoError(t, err)
	assert.True(t, rep.Failed())
	assert.Equal(t, Fail, step(t, rep, "no archiving").Status)
	assert.Empty(t, f.createdKinds(), "nothing is created")
	assert.Empty(t, f.deleted)
	assert.Equal(t, "0", f.cms[drillNS+"/"+ResultName(drillNm)].Data[KeyLastResult])
}

func managedCluster(age time.Duration, labels map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "postgresql.cnpg.io/v1", "kind": "Cluster",
		"metadata": map[string]any{"name": drillNm, "namespace": drillNS, "labels": labels},
	}}
	u.SetCreationTimestamp(metav1.NewTime(now.Add(-age)))
	return u
}

func TestADrillInProgressIsLeftAlone(t *testing.T) {
	f := fixture()
	f.put(ClusterGVR, managedCluster(10*time.Minute, map[string]any{LabelManagedBy: ManagedBy}))
	f.neverHealthy = true
	o := opts()
	o.Timeout = time.Hour
	rep, _, err := runDrill(t, f, healthyDB(), o)
	assert.ErrorIs(t, err, ErrInProgress)
	assert.Equal(t, Skip, step(t, rep, "one drill at a time").Status)
	assert.Empty(t, f.createdKinds())
	assert.Empty(t, f.deleted)
	assert.Empty(t, f.cms, "a refused run records nothing")
}

func TestAStaleDrillIsDeletedFirst(t *testing.T) {
	f := fixture()
	f.put(ClusterGVR, managedCluster(3*time.Hour, map[string]any{LabelManagedBy: ManagedBy}))
	rep, out, err := runDrill(t, f, healthyDB(), opts())
	require.NoError(t, err)
	assert.False(t, rep.Failed(), "%+v", rep.Steps)
	assert.Contains(t, out, "deleting a stale drill")
	assert.Equal(t, "clusters/drills/shop-pg-restore", f.deleted[0])
}

func TestAClusterNoDrillCreatedIsNeverTouched(t *testing.T) {
	f := fixture()
	f.put(ClusterGVR, managedCluster(3*time.Hour, nil))
	rep, _, err := runDrill(t, f, healthyDB(), opts())
	require.NoError(t, err)
	assert.Equal(t, Fail, step(t, rep, "one drill at a time").Status)
	assert.Contains(t, step(t, rep, "one drill at a time").Message, "no drill created")
	assert.Empty(t, f.deleted)
	assert.Empty(t, f.createdKinds())
}

func TestAFailedRecoveryEndsEarlyWithDiagnosticsAndKeepsTheLastSuccess(t *testing.T) {
	f := fixture()
	f.failedRecoveries = 2
	f.events = []corev1.Event{{InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: drillNm + "-1-full-recovery-f0"}, Reason: "BackOff", Message: "back-off restarting"}}
	f.cms[drillNS+"/"+ResultName(drillNm)] = &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: ResultName(drillNm), Namespace: drillNS},
		Data:       map[string]string{KeyLastSuccess: "1700000000", KeyLastResult: "1"},
	}
	o := opts()
	o.Timeout = time.Minute
	rep, out, err := runDrill(t, f, healthyDB(), o)
	require.NoError(t, err)
	assert.Equal(t, Fail, step(t, rep, "recovery").Status)
	assert.Contains(t, step(t, rep, "recovery").Message, "failed 2 times")
	assert.Equal(t, Pass, step(t, rep, "teardown").Status)
	for _, want := range []string{"diagnostics for drill drills/shop-pg-restore", "BackOff", "pod shop-pg-restore-1-full-recovery-f0: phase Failed", "a log line of"} {
		assert.Contains(t, out, want)
	}
	cm := f.cms[drillNS+"/"+ResultName(drillNm)]
	assert.Equal(t, "0", cm.Data[KeyLastResult])
	assert.Equal(t, "1700000000", cm.Data[KeyLastSuccess], "an earlier success survives a failure")
	assert.Contains(t, cm.Data[KeyLastFailure], "recovery")
	assert.NotContains(t, cm.Data, KeyRecoveryPointAge)
}

func TestTimeoutTearsDown(t *testing.T) {
	f := fixture()
	f.neverHealthy = true
	o := opts()
	o.Timeout = 200 * time.Millisecond
	rep, _, err := runDrill(t, f, healthyDB(), o)
	require.NoError(t, err)
	assert.Contains(t, step(t, rep, "recovery").Message, "timed out")
	assert.Equal(t, Pass, step(t, rep, "teardown").Status)
	assert.Len(t, f.deleted, 2)
}

func TestChecks(t *testing.T) {
	cases := []struct {
		name    string
		prober  func(*fakeProber)
		opts    func(*Options)
		kube    func(*fakeKube)
		failing string
		message string
	}{
		{name: "still in recovery", prober: func(p *fakeProber) { p.inRecovery = true }, failing: "promoted"},
		{name: "a database is missing", prober: func(p *fakeProber) { delete(p.dbs, "orders") }, failing: "databases", message: "missing orders"},
		{name: "a database has no table", prober: func(p *fakeProber) { p.dbs["orders"] = nil }, failing: "database orders", message: "no user table"},
		{name: "a database is empty", prober: func(p *fakeProber) { p.dbs["orders"] = []Table{{Schema: "public", Name: "orders"}} }, failing: "database orders", message: "fewer than the minimum 1"},
		{name: "a table minimum", opts: func(o *Options) { o.MinRows = map[string]int64{"orders/public.orders": 100} }, failing: "min rows orders/public.orders", message: "42 rows, fewer than the minimum 100"},
		{name: "a missing table", opts: func(o *Options) { o.MinRows = map[string]int64{"shop/public.nope": 1} }, failing: "min rows shop/public.nope", message: "not found"},
		{name: "a minimum for an unchecked database", opts: func(o *Options) { o.MinRows = map[string]int64{"nope": 1} }, failing: "min rows nope"},
		{name: "a database minimum", opts: func(o *Options) { o.MinRows = map[string]int64{"shop": 6} }, failing: "database shop", message: "5 rows, fewer than the minimum 6"},
		{name: "old data", opts: func(o *Options) { o.MaxRecoveryPointAge = time.Minute }, failing: "recovery point", message: "maximum 1m0s"},
		{name: "no recovery point", kube: func(f *fakeKube) { f.recoveryLog = "nothing\n" }, prober: func(p *fakeProber) { p.labelErr = ErrNotFound }, failing: "recovery point", message: "unknown"},
		{name: "explicit databases", opts: func(o *Options) { o.Databases = []string{"shop", "ledger"} }, failing: "databases", message: "missing ledger"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := fixture()
			p := healthyDB()
			o := opts()
			if c.prober != nil {
				c.prober(p)
			}
			if c.opts != nil {
				c.opts(&o)
			}
			if c.kube != nil {
				c.kube(f)
			}
			rep, _, err := runDrill(t, f, p, o)
			require.NoError(t, err)
			s := step(t, rep, c.failing)
			assert.Equal(t, Fail, s.Status, s.Message)
			assert.Contains(t, s.Message, c.message)
			assert.Equal(t, Pass, step(t, rep, "teardown").Status)
		})
	}
}

func TestEstimateMode(t *testing.T) {
	f := fixture()
	p := healthyDB()
	p.dbs["shop"] = []Table{{Schema: "public", Name: "carts", Estimate: -1}, {Schema: "public", Name: "items", Estimate: 7}}
	o := opts()
	o.CountMode = CountEstimate
	rep, _, err := runDrill(t, f, p, o)
	require.NoError(t, err)
	assert.Contains(t, step(t, rep, "database shop").Message, "7 rows (estimated)")
}

func TestRecoveryPointFallsBackToTheBaseBackup(t *testing.T) {
	f := fixture()
	f.recoveryLog = "no transaction was replayed\n"
	rep, _, err := runDrill(t, f, healthyDB(), opts())
	require.NoError(t, err)
	assert.False(t, rep.Failed(), "%+v", rep.Steps)
	assert.Equal(t, "base backup start (no later transaction replayed)", rep.RecoveryPointSource)
	assert.Equal(t, time.Date(2026, 10, 20, 2, 0, 1, 0, time.UTC), rep.RecoveryPoint.UTC())

	// A transaction older than the base backup loses to the backup's start.
	tx := time.Date(2026, 10, 19, 1, 0, 0, 0, time.UTC)
	bk := time.Date(2026, 10, 20, 2, 0, 0, 0, time.UTC)
	got, _ := recoveryPoint(tx, bk)
	assert.Equal(t, bk, got)
}

func TestPlanRefusals(t *testing.T) {
	cases := []struct {
		name string
		kube func(*fakeKube)
		opts func(*Options)
		want string
	}{
		{name: "no source", kube: func(f *fakeKube) { delete(f.objs, key(ClusterGVR, srcNS, srcName)) }, want: "read the source Cluster"},
		{name: "no archive", kube: func(f *fakeKube) {
			c := sourceCluster()
			unstructured.RemoveNestedField(c.Object, "spec", "plugins")
			f.put(ClusterGVR, c)
		}, want: "no barman-cloud.cloudnative-pg.io plugin entry"},
		{name: "not S3", kube: func(f *fakeKube) {
			s := sourceStore()
			_ = unstructured.SetNestedField(s.Object, "https://example.blob.core.windows.net/c", "spec", "configuration", "destinationPath")
			_ = unstructured.SetNestedField(s.Object, map[string]any{"inheritFromAzureAD": true}, "spec", "configuration", "azureCredentials")
			f.put(ObjectStoreGVR, s)
		}, want: "not on S3"},
		{name: "a retaining class", opts: func(o *Options) { o.StorageClass = "fast-retain" }, want: "retains its volumes"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := fixture()
			o := opts()
			if c.kube != nil {
				c.kube(f)
			}
			if c.opts != nil {
				c.opts(&o)
			}
			rep, _, err := runDrill(t, f, healthyDB(), o)
			require.NoError(t, err)
			assert.Contains(t, step(t, rep, "plan").Message, c.want)
			assert.Empty(t, f.createdKinds())
			assert.Equal(t, "0", f.cms[drillNS+"/"+ResultName(drillNm)].Data[KeyLastResult], "a drill that cannot start is a failed drill")
		})
	}
}

func TestExplicitCoordinatesNeedNoSourceArchive(t *testing.T) {
	f := fixture()
	c := sourceCluster()
	unstructured.RemoveNestedField(c.Object, "spec", "plugins")
	f.put(ClusterGVR, c)
	delete(f.objs, key(ObjectStoreGVR, srcNS, storeSrc))
	o := opts()
	o.DestinationPath, o.ServerName = "s3://old-backups/shop/shop-pg", "shop-pg-g1"
	o.StorageClass = "fast-retain"
	o.AllowRetainedVolumes = true
	rep, _, err := runDrill(t, f, healthyDB(), o)
	require.NoError(t, err)
	assert.False(t, rep.Failed(), "%+v", rep.Steps)
	assert.Equal(t, "shop-pg-g1", rep.ServerName)
}

func TestOptions(t *testing.T) {
	_, err := Options{Namespace: "a"}.withDefaults()
	assert.Error(t, err)
	_, err = Options{Namespace: "a", Cluster: strings.Repeat("x", 43)}.withDefaults()
	assert.ErrorContains(t, err, "at most 50")
	_, err = Options{Namespace: "a", Cluster: "b", DrillName: "b"}.withDefaults()
	assert.ErrorContains(t, err, "the source cluster itself")
	_, err = Options{Namespace: "a", Cluster: "b", SidecarEnv: []corev1.EnvVar{{Name: "AWS_SECRET_ACCESS_KEY", Value: "x"}}}.withDefaults()
	assert.ErrorContains(t, err, "restore identity only")
	_, err = Options{Namespace: "a", Cluster: "b", AllowStaticCredentials: true, SidecarEnv: []corev1.EnvVar{{Name: "AWS_SECRET_ACCESS_KEY", Value: "x"}}}.withDefaults()
	assert.NoError(t, err)
	o, err := Options{Namespace: "a", Cluster: "b"}.withDefaults()
	require.NoError(t, err)
	assert.Equal(t, "b-restore", o.DrillName)
	assert.Equal(t, "a", o.DrillNamespace)
	assert.Equal(t, DefaultTimeout, o.Timeout)
	_, err = Options{Namespace: "a", Cluster: "b", CountMode: "guess"}.withDefaults()
	assert.Error(t, err)
}

func TestParsers(t *testing.T) {
	for line, want := range map[string]time.Time{
		"LOG:  last completed transaction was at log time 2026-10-20 11:55:00.123456+00":   time.Date(2026, 10, 20, 11, 55, 0, 123456000, time.UTC),
		`"message":"last completed transaction was at log time 2026-10-20 13:55:00+02:00"`: time.Date(2026, 10, 20, 11, 55, 0, 0, time.UTC),
		"last completed transaction was at log time 2026-10-20 11:55:00.5":                 time.Date(2026, 10, 20, 11, 55, 0, 500000000, time.UTC),
	} {
		got, ok := ParseLastTransaction(line)
		assert.True(t, ok, line)
		assert.True(t, want.Equal(got), "%s: %s", line, got)
	}
	_, ok := ParseLastTransaction("redo done at 0/5000060")
	assert.False(t, ok)

	got, ok := ParseBackupStart("START TIME: 2026-10-20 02:00:01 UTC\n")
	assert.True(t, ok)
	assert.Equal(t, time.Date(2026, 10, 20, 2, 0, 1, 0, time.UTC), got.UTC())
	_, ok = ParseBackupStart("LABEL: x\n")
	assert.False(t, ok)

	k, n, err := ParseMinRows("shop/public.orders=10")
	require.NoError(t, err)
	assert.Equal(t, "shop/public.orders", k)
	assert.Equal(t, int64(10), n)
	for _, bad := range []string{"shop", "=1", "shop=-1", "shop=x", "shop/orders=1", "/public.t=1"} {
		_, _, err := ParseMinRows(bad)
		assert.Error(t, err, bad)
	}

	tol, err := ParseToleration("pool=database:NoSchedule")
	require.NoError(t, err)
	assert.Equal(t, corev1.Toleration{Key: "pool", Operator: corev1.TolerationOpEqual, Value: "database", Effect: corev1.TaintEffectNoSchedule}, tol)
	tol, err = ParseToleration("arch:NoExecute")
	require.NoError(t, err)
	assert.Equal(t, corev1.TolerationOpExists, tol.Operator)
	_, err = ParseToleration("pool=database")
	assert.Error(t, err)

	src, err := ParseSource("shop/shop-pg")
	require.NoError(t, err)
	assert.Equal(t, Source{Namespace: "shop", Cluster: "shop-pg", DrillName: "shop-pg-restore"}, src)
	src, err = ParseSource("shop/shop-pg=drill-a")
	require.NoError(t, err)
	assert.Equal(t, "drill-a", src.DrillName)
	_, err = ParseSource("shop")
	assert.Error(t, err)
}

func TestWriteMetrics(t *testing.T) {
	sources := []Source{{Namespace: "shop", Cluster: "shop-pg", DrillName: "shop-pg-restore"}, {Namespace: "ledger", Cluster: "ledger-pg", DrillName: "ledger-pg-restore"}}
	results := map[string]*corev1.ConfigMap{"shop-pg-restore": {Data: map[string]string{
		KeyLastRun: "1760961600", KeyLastSuccess: "1760961600", KeyLastResult: "1", KeyLastDuration: "1830", KeyRecoveryPointAge: "300",
	}}}
	var buf bytes.Buffer
	require.NoError(t, WriteMetrics(&buf, sources, results))
	got := buf.String()
	for _, want := range []string{
		"# TYPE cnpg_restore_drill_configured gauge\n",
		`cnpg_restore_drill_configured{namespace="shop",cluster="shop-pg"} 1`,
		`cnpg_restore_drill_configured{namespace="ledger",cluster="ledger-pg"} 1`,
		`cnpg_restore_drill_last_run_timestamp_seconds{namespace="shop",cluster="shop-pg"} 1760961600`,
		`cnpg_restore_drill_last_success_timestamp_seconds{namespace="shop",cluster="shop-pg"} 1760961600`,
		`cnpg_restore_drill_last_result{namespace="shop",cluster="shop-pg"} 1`,
		`cnpg_restore_drill_last_duration_seconds{namespace="shop",cluster="shop-pg"} 1830`,
		`cnpg_restore_drill_recovery_point_age_seconds{namespace="shop",cluster="shop-pg"} 300`,
	} {
		assert.Contains(t, got, want)
	}
	assert.NotContains(t, got, `last_run_timestamp_seconds{namespace="ledger"`, "a drill that never ran has no run series")

	results["shop-pg-restore"].Data[KeyLastResult] = "x"
	assert.Error(t, WriteMetrics(&bytes.Buffer{}, sources, results))
}

func TestMetricsHandlerReadsTheResultsByName(t *testing.T) {
	f := newFakeKube()
	require.NoError(t, f.CreateConfigMap(context.Background(), &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "shop-pg-restore-result", Namespace: drillNS},
		Data:       map[string]string{KeyLastRun: "1", KeyLastResult: "0", KeyLastSuccess: "0"},
	}))
	results, err := ReadResults(context.Background(), f, drillNS, []Source{{Namespace: "shop", Cluster: "shop-pg", DrillName: "shop-pg-restore"}, {Namespace: "x", Cluster: "y", DrillName: "y-restore"}})
	require.NoError(t, err)
	assert.Len(t, results, 1)
}
