// Copyright 2026 Truvity B.V.. All rights reserved.
// SPDX-License-Identifier: MIT

package conformance_test

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"

	"github.com/truvity/cnpg/v2/pkg/drill"
)

// restoreDrill is assertion 9, run by TestBackupAndRestore once the source
// pgb has a base backup and an archived WAL segment holding rows 1-3: cnpgctl
// drill (the Go package, against the suite's kubeconfig) recovers the whole
// archive into pgb-restore, the real operator and plugin accept the drill's
// Cluster and ObjectStore, the checks find all three rows (a full replay, not
// the point-in-time target the restore above stopped at), and everything the
// drill created is gone afterwards, its result recorded.
//
// kind has no Pod Identity, so the drill's sidecar is given the S3 endpoint's
// test credentials through --sidecar-env with --allow-static-credentials, the
// switch that exists for test endpoints only. The checks reach the drill
// instance through a port-forward from this process.
func (s *suite) restoreDrill(t *testing.T) {
	t.Helper()

	k, err := drill.NewKubeClient(s.kubeconfig, "")
	require.NoError(t, err)

	port := freePort(t)
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		// The Service has no endpoint until the drill's instance is up; keep
		// trying until the drill is over.
		for ctx.Err() == nil {
			cmd := exec.CommandContext(ctx, "kubectl", "port-forward", "-n", backupNS, "svc/pgb-restore-rw", strconv.Itoa(port)+":5432")
			cmd.Env = s.env
			_ = cmd.Run()
			select {
			case <-ctx.Done():
			case <-time.After(2 * time.Second):
			}
		}
	}()

	var progress bytes.Buffer
	rep, err := drill.Run(context.Background(), drill.Deps{Kube: k, Out: &progress}, drill.Options{
		Namespace: backupNS, Cluster: "pgb",
		SidecarEnv: []corev1.EnvVar{
			{Name: "AWS_ACCESS_KEY_ID", Value: "test"},
			{Name: "AWS_SECRET_ACCESS_KEY", Value: "test"},
		},
		AllowStaticCredentials: true,
		MinRows:                map[string]int64{"app/public.points": 3},
		PGHost:                 "127.0.0.1",
		PGPort:                 port,
		Timeout:                15 * time.Minute,
		PollInterval:           5 * time.Second,
	})
	cancel()
	wg.Wait()
	t.Logf("cnpgctl drill progress:\n%s", progress.String())
	require.NoError(t, err)

	var text bytes.Buffer
	drill.WriteText(&text, rep)
	t.Logf("%s", text.String())
	require.False(t, rep.Failed(), "the drill failed: %s", rep.FirstFailure())
	assert.Equal(t, []drill.DatabaseResult{{Name: "app", Tables: 1, Rows: 3}}, rep.Databases, "all three rows: the drill replays the whole archive")
	require.NotNil(t, rep.RecoveryPointAgeSeconds)
	assert.Less(t, *rep.RecoveryPointAgeSeconds, time.Hour.Seconds())

	for _, gvr := range []string{"cluster.postgresql.cnpg.io/pgb-restore", "objectstore.barmancloud.cnpg.io/pgb-restore-source"} {
		out, _ := s.run(context.Background(), "", "kubectl", "get", "-n", backupNS, gvr, "--ignore-not-found", "-o", "name")
		assert.Empty(t, strings.TrimSpace(out), "%s is deleted by the teardown", gvr)
	}

	cm, err := k.GetConfigMap(context.Background(), backupNS, drill.ResultName("pgb-restore"))
	require.NoError(t, err)
	assert.Equal(t, "1", cm.Data[drill.KeyLastResult])
	assert.Equal(t, cm.Data[drill.KeyLastRun], cm.Data[drill.KeyLastSuccess])

	// The exporter's view of it.
	var metrics bytes.Buffer
	results, err := drill.ReadResults(context.Background(), k, backupNS, []drill.Source{{Namespace: backupNS, Cluster: "pgb", DrillName: "pgb-restore"}})
	require.NoError(t, err)
	require.NoError(t, drill.WriteMetrics(&metrics, []drill.Source{{Namespace: backupNS, Cluster: "pgb", DrillName: "pgb-restore"}}, results))
	assert.Contains(t, metrics.String(), `cnpg_restore_drill_last_result{namespace="backup",cluster="pgb"} 1`)

	_, err = k.Get(context.Background(), drill.ClusterGVR, backupNS, "pgb-restore")
	assert.True(t, errors.Is(err, drill.ErrNotFound), "the drill Cluster is gone")
}

func freePort(t *testing.T) int {
	t.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	defer func() { _ = l.Close() }()

	return l.Addr().(*net.TCPAddr).Port
}
