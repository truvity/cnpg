package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/truvity/cnpg/v2/pkg/guard"
)

type noCluster struct{}

func (noCluster) Cluster(_ context.Context, _, name string) (*unstructured.Unstructured, error) {
	return nil, apierrors.NewNotFound(schema.GroupResource{Group: "postgresql.cnpg.io", Resource: "clusters"}, name)
}

type keys []string

func (k keys) HasObjects(_ context.Context, _, prefix string) (bool, error) {
	for _, key := range k {
		if strings.HasPrefix(key, prefix) {
			return true, nil
		}
	}

	return false, nil
}

func withSources(t *testing.T, archive guard.ArchiveLister, got *guard.S3Options) {
	t.Helper()

	old := guardSources
	guardSources = func(_ context.Context, _, _ string, opt guard.S3Options) (guard.ClusterSource, guard.ArchiveLister, error) {
		if got != nil {
			*got = opt
		}

		return noCluster{}, archive, nil
	}

	t.Cleanup(func() { guardSources = old })
}

func TestGuardEmptyBootstrap(t *testing.T) {
	args := []string{"guard", "empty-bootstrap", "--namespace", "app", "--cluster", "pg", "--destination", "s3://b/app/pg", "--server-name", "pg-g2", "--region", "eu-west-1"}

	t.Run("refused: exit 1", func(t *testing.T) {
		var s3opt guard.S3Options
		withSources(t, keys{"app/pg/pg-g2/wals/0001"}, &s3opt)

		var out, errb bytes.Buffer
		err := run(t.Context(), args, &out, &errb, nil)
		require.ErrorIs(t, err, errFailed)
		assert.Contains(t, out.String(), "FAIL  empty-bootstrap: REFUSED")
		assert.Equal(t, "eu-west-1", s3opt.Region)
	})

	t.Run("empty archive: passes", func(t *testing.T) {
		withSources(t, keys{"app/pg/pg-g1/wals/0001"}, nil)

		var out, errb bytes.Buffer
		require.NoError(t, run(t.Context(), args, &out, &errb, nil))
		assert.Contains(t, out.String(), "PASS  empty-bootstrap")
	})

	t.Run("override: passes", func(t *testing.T) {
		withSources(t, keys{"app/pg/pg-g2/base/x"}, nil)

		var out, errb bytes.Buffer
		require.NoError(t, run(t.Context(), append(args, "--allow-non-empty-archive", "--reason", "drill"), &out, &errb, nil))
		assert.Contains(t, out.String(), "drill")
	})
}

func TestGuardFlagErrors(t *testing.T) {
	withSources(t, keys{}, nil)

	var out, errb bytes.Buffer
	for _, args := range [][]string{
		{"guard"},
		{"guard", "nope"},
		{"guard", "empty-bootstrap"},
		{"guard", "empty-bootstrap", "--namespace", "a", "--cluster", "b"},
		{"guard", "empty-bootstrap", "--namespace", "a", "--cluster", "b", "--destination", "s3://x", "--bootstrap", "clone"},
		{"guard", "empty-bootstrap", "--namespace", "a", "--cluster", "b", "--destination", "s3://x", "--allow-non-empty-archive"},
	} {
		err := run(t.Context(), args, &out, &errb, nil)
		require.Error(t, err, args)
		assert.NotErrorIs(t, err, errFailed, "a usage error is exit 2, not a refusal: %v", args)
	}
}
