// Copyright 2026 Truvity B.V.. All rights reserved.
// SPDX-License-Identifier: MIT

package conformance_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	backupNS = "backup"
	bucket   = "conformance-backups"
)

// TestBackupAndRestore is assertion 8: a barman-cloud base backup and WAL
// archive to an S3 endpoint inside the cluster, and a point-in-time restore
// of a second cluster from them, stopping before a row written after the
// target. Opt-in (CNPG_CONFORMANCE_BACKUP): it pulls a further image and runs
// two more databases.
func TestBackupAndRestore(t *testing.T) {
	s := gate(t)

	if os.Getenv(backupVariable) == "" {
		t.Skipf("set %s=1 (or run in CI) to exercise barman-cloud backup and point-in-time restore against an in-cluster S3 endpoint", backupVariable)
	}

	t.Cleanup(func() { s.diagnostics(t) })

	endpoint := "http://" + strings.Join([]string{"s3.object-store.svc", "4566"}, ":")

	// The S3 endpoint and its bucket.
	s.kubectl(t, "apply", "-f", s.path("conformance", "fixtures", "s3.yaml"))
	s.kubectl(t, "rollout", "status", "deployment/s3", "-n", "object-store", "--timeout=300s")
	s.kubectl(t, "exec", "-n", "object-store", "deployment/s3", "--", "awslocal", "s3", "mb", "s3://"+bucket)

	_, err := s.apply(fmt.Sprintf(`apiVersion: v1
kind: Namespace
metadata: {name: %s}
---
apiVersion: v1
kind: Secret
metadata: {name: s3-credentials, namespace: %s}
stringData:
  AWS_ACCESS_KEY_ID: test
  AWS_SECRET_ACCESS_KEY: test
`, backupNS, backupNS))
	require.NoError(t, err)

	// The source: a database that archives WAL and takes a base backup.
	source := filepath.Join(s.dir, "backup-source.values.yaml")
	require.NoError(t, os.WriteFile(source, []byte(fmt.Sprintf(`clusterName: pgb
namespace: %[1]s
profile: devel
storage: {size: 1Gi, storageClass: standard}
bootstrap:
  initdb: {database: app, owner: app_owner}
backup:
  enabled: true
  bucketName: %[2]s
  s3Prefix: backup/pgb
  endpoint: %[3]s
  existingSecret: s3-credentials
  retentionDays: 7
`, backupNS, bucket, endpoint)), 0o600))

	s.mustIn(t, 5*time.Minute, "", "helm", "upgrade", "--install", "pgb", s.path("charts", "cnpg-cluster"), "--namespace", backupNS, "-f", source)
	s.kubectl(t, "wait", "--for=condition=Ready", "cluster.postgresql.cnpg.io/pgb", "-n", backupNS, "--timeout=600s")

	sql := func(cluster, query string) string {
		t.Helper()

		pod := s.primaryPod(t, backupNS, cluster)

		return strings.TrimSpace(s.kubectl(t, "exec", "-n", backupNS, pod, "-c", "postgres", "--",
			"psql", "-X", "-A", "-t", "-U", "postgres", "-d", "app", "-v", "ON_ERROR_STOP=1", "-c", query))
	}

	sql("pgb", "create table points (n int primary key); insert into points values (1)")

	_, err = s.apply(`apiVersion: postgresql.cnpg.io/v1
kind: Backup
metadata: {name: base, namespace: backup}
spec:
  method: plugin
  cluster: {name: pgb}
  pluginConfiguration: {name: barman-cloud.cloudnative-pg.io}
`)
	require.NoError(t, err)

	eventually(t, 10*time.Minute, 5*time.Second, "the base backup completes", func() error {
		phase := strings.TrimSpace(s.kubectl(t, "get", "backup", "base", "-n", backupNS, "-o", "jsonpath={.status.phase}"))
		if phase == "failed" {
			t.Fatalf("the backup failed: %s", s.kubectl(t, "get", "backup", "base", "-n", backupNS, "-o", "jsonpath={.status.error}"))
		}

		if phase != "completed" {
			return fmt.Errorf("phase %q", phase)
		}

		return nil
	})

	// Row 2 is before the recovery target, row 3 after it.
	sql("pgb", "insert into points values (2)")
	time.Sleep(3 * time.Second)

	target := sql("pgb", "select to_char(now() at time zone 'UTC', 'YYYY-MM-DD HH24:MI:SS.US+00')")

	time.Sleep(3 * time.Second)
	sql("pgb", "insert into points values (3)")

	// Force the segment holding all three rows into the archive, and wait for
	// the archiver to say it is there.
	archived := sql("pgb", "select archived_count from pg_stat_archiver")
	sql("pgb", "select pg_switch_wal()")

	eventually(t, 5*time.Minute, 3*time.Second, "the WAL segment is archived", func() error {
		if now := sql("pgb", "select archived_count from pg_stat_archiver"); now == archived {
			return fmt.Errorf("archived_count still %s", now)
		}

		return nil
	})

	// The restore: a new cluster bootstrapped from that archive, stopping at
	// the target.
	restore := filepath.Join(s.dir, "backup-restore.values.yaml")
	require.NoError(t, os.WriteFile(restore, []byte(fmt.Sprintf(`clusterName: pgr
namespace: %[1]s
profile: devel
storage: {size: 1Gi, storageClass: standard}
bootstrap:
  mode: recovery
  recovery:
    source:
      bucketName: %[2]s
      s3Prefix: backup/pgb
      serverName: pgb
      endpoint: %[3]s
      existingSecret: s3-credentials
    target:
      targetTime: %[4]q
`, backupNS, bucket, endpoint, target)), 0o600))

	s.mustIn(t, 5*time.Minute, "", "helm", "upgrade", "--install", "pgr", s.path("charts", "cnpg-cluster"), "--namespace", backupNS, "-f", restore)
	s.kubectl(t, "wait", "--for=condition=Ready", "cluster.postgresql.cnpg.io/pgr", "-n", backupNS, "--timeout=900s")

	rows := sql("pgr", "select string_agg(n::text, ',' order by n) from points")
	assert.Equal(t, "1,2", rows, "the restore stops at %s: row 2 is in, row 3 is not", target)
}
