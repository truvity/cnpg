package guard

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// fakeClusters answers Cluster from a fixed state.
type fakeClusters struct {
	exists   bool
	deleting bool
	err      error
	calls    int
}

func (f *fakeClusters) Cluster(_ context.Context, namespace, name string) (*unstructured.Unstructured, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}

	if !f.exists {
		return nil, apierrors.NewNotFound(schema.GroupResource{Group: "postgresql.cnpg.io", Resource: "clusters"}, name)
	}

	u := &unstructured.Unstructured{}
	u.SetNamespace(namespace)
	u.SetName(name)

	if f.deleting {
		now := metav1.Now()
		u.SetDeletionTimestamp(&now)
	}

	return u, nil
}

// fakeArchive holds keys per bucket and records the prefixes it was asked for.
type fakeArchive struct {
	keys  map[string][]string
	err   error
	asked []string
}

func (f *fakeArchive) HasObjects(_ context.Context, bucket, prefix string) (bool, error) {
	f.asked = append(f.asked, bucket+"/"+prefix)
	if f.err != nil {
		return false, f.err
	}

	for _, k := range f.keys[bucket] {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			return true, nil
		}
	}

	return false, nil
}

func opts() EmptyBootstrapOptions {
	return EmptyBootstrapOptions{
		Namespace:   "app",
		Cluster:     "app-pg",
		Bootstrap:   BootstrapInitdb,
		Destination: "s3://example-backups/app/app-pg",
		ServerName:  "app-pg-g2",
	}
}

func TestEmptyBootstrap(t *testing.T) {
	base := "app/app-pg/app-pg-g2/base/20260101T000000/data.tar"
	wal := "app/app-pg/app-pg-g2/wals/0000000100000000/000000010000000000000001.zst"
	other := "app/app-pg/app-pg-g1/wals/0000000100000000/000000010000000000000001.zst"

	for _, tc := range []struct {
		name     string
		mutate   func(*EmptyBootstrapOptions)
		clusters fakeClusters
		keys     []string
		pass     bool
		contains string
		noList   bool
	}{
		{name: "cluster exists: upgrade passes", clusters: fakeClusters{exists: true}, keys: []string{base, wal}, pass: true, contains: "exists", noList: true},
		{name: "new cluster, empty archive passes", keys: []string{other}, pass: true, contains: "is empty"},
		{name: "new cluster, base backups refused", keys: []string{base}, contains: "already holds base"},
		{name: "new cluster, WAL only refused", keys: []string{wal}, contains: "already holds wals"},
		{name: "new cluster, both refused", keys: []string{base, wal}, contains: "base and wals"},
		{name: "being deleted counts as new", clusters: fakeClusters{exists: true, deleting: true}, keys: []string{wal}, contains: "REFUSED"},
		{
			name: "recovery passes without looking", mutate: func(o *EmptyBootstrapOptions) { o.Bootstrap = BootstrapRecovery },
			keys: []string{base}, pass: true, contains: "recovery", noList: true,
		},
		{
			name: "override passes without looking", mutate: func(o *EmptyBootstrapOptions) { o.AllowNonEmptyArchive = true; o.Reason = "restored by hand" },
			keys: []string{base}, pass: true, contains: "restored by hand", noList: true,
		},
		{
			name: "serverName defaults to the cluster", mutate: func(o *EmptyBootstrapOptions) { o.ServerName = "" },
			keys: []string{"app/app-pg/app-pg/wals/x"}, contains: "s3://example-backups/app/app-pg/app-pg/",
		},
		{
			name: "trailing slash on the path", mutate: func(o *EmptyBootstrapOptions) { o.Destination = "s3://example-backups/app/app-pg/" },
			keys: []string{wal}, contains: "s3://example-backups/app/app-pg/app-pg-g2/ already",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := opts()
			if tc.mutate != nil {
				tc.mutate(&o)
			}

			clusters := tc.clusters
			archive := &fakeArchive{keys: map[string][]string{"example-backups": tc.keys}}

			v, err := EmptyBootstrap(t.Context(), o, &clusters, archive)
			require.NoError(t, err)
			assert.Equal(t, tc.pass, v.Pass, v.Message)
			assert.Contains(t, v.Message, tc.contains)

			if tc.noList {
				assert.Empty(t, archive.asked, "the archive must not be read")
			} else {
				assert.NotEmpty(t, archive.asked)
			}
		})
	}
}

// TestEmptyBootstrap_ListsBaseAndWals: the two prefixes asked for are the
// barman server directory's base/ and wals/.
func TestEmptyBootstrap_ListsBaseAndWals(t *testing.T) {
	archive := &fakeArchive{}
	_, err := EmptyBootstrap(t.Context(), opts(), &fakeClusters{}, archive)
	require.NoError(t, err)
	assert.Equal(t, []string{
		"example-backups/app/app-pg/app-pg-g2/base/",
		"example-backups/app/app-pg/app-pg-g2/wals/",
	}, archive.asked)
}

// TestEmptyBootstrap_FailsClosed: a guard that cannot look never passes.
func TestEmptyBootstrap_FailsClosed(t *testing.T) {
	t.Run("API error", func(t *testing.T) {
		_, err := EmptyBootstrap(t.Context(), opts(), &fakeClusters{err: apierrors.NewForbidden(schema.GroupResource{Resource: "clusters"}, "app-pg", errors.New("no"))}, &fakeArchive{})
		require.Error(t, err)
	})
	t.Run("object store error", func(t *testing.T) {
		_, err := EmptyBootstrap(t.Context(), opts(), &fakeClusters{}, &fakeArchive{err: errors.New("AccessDenied")})
		require.ErrorContains(t, err, "AccessDenied")
	})

	for name, mutate := range map[string]func(*EmptyBootstrapOptions){
		"unknown bootstrap":      func(o *EmptyBootstrapOptions) { o.Bootstrap = "pg_basebackup" },
		"no namespace":           func(o *EmptyBootstrapOptions) { o.Namespace = "" },
		"bad destination":        func(o *EmptyBootstrapOptions) { o.Destination = "https://example-backups/app" },
		"override and no reason": func(o *EmptyBootstrapOptions) { o.AllowNonEmptyArchive = true; o.Reason = " " },
	} {
		t.Run(name, func(t *testing.T) {
			o := opts()
			mutate(&o)
			_, err := EmptyBootstrap(t.Context(), o, &fakeClusters{}, &fakeArchive{})
			require.Error(t, err)
		})
	}
}

func TestArchiveDir(t *testing.T) {
	assert.Equal(t, "ns/pg/pg-g2/", ArchiveDir("ns/pg", "pg-g2"))
	assert.Equal(t, "ns/pg/pg-g2/", ArchiveDir("ns/pg/", "pg-g2"))
	assert.Equal(t, "pg/", ArchiveDir("", "pg"))
}

func TestParseDestination(t *testing.T) {
	b, p, err := ParseDestination("s3://bucket/ns/pg")
	require.NoError(t, err)
	assert.Equal(t, "bucket", b)
	assert.Equal(t, "ns/pg", p)

	b, p, err = ParseDestination("s3://bucket")
	require.NoError(t, err)
	assert.Equal(t, "bucket", b)
	assert.Empty(t, p)

	for _, bad := range []string{"", "bucket/ns", "s3:///ns", "gs://bucket/ns"} {
		_, _, err := ParseDestination(bad)
		assert.Error(t, err, bad)
	}
}

// TestS3Lister_NoRegion: without a region the first listing fails (an error,
// never a pass), and building the lister needs none.
func TestS3Lister_NoRegion(t *testing.T) {
	t.Setenv("AWS_REGION", "")
	t.Setenv("AWS_DEFAULT_REGION", "")
	t.Setenv("AWS_CONFIG_FILE", t.TempDir()+"/none")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", t.TempDir()+"/none")
	t.Setenv("AWS_PROFILE", "")

	l := NewS3Lister(S3Options{})
	_, err := l.HasObjects(t.Context(), "b", "p/")
	require.ErrorContains(t, err, "no region")
}
