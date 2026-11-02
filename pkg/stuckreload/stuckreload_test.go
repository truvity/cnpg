package stuckreload_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/truvity/cnpg/v2/pkg/stuckreload"
)

var now = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

const stuckPhase = "Waiting for the instances to become active"

func cluster(mut ...func(map[string]any)) *unstructured.Unstructured {
	obj := map[string]any{
		"metadata": map[string]any{"name": "pg", "namespace": "app", "annotations": map[string]any{}},
		"spec":     map[string]any{"instances": int64(3)},
		"status": map[string]any{
			"phase":          stuckPhase,
			"readyInstances": int64(3),
			"conditions": []any{
				map[string]any{"type": "Ready", "status": "False", "lastTransitionTime": now.Add(-10 * time.Minute).Format(time.RFC3339)},
			},
		},
	}
	for _, m := range mut {
		m(obj)
	}

	return &unstructured.Unstructured{Object: obj}
}

func pod(name string, phase corev1.PodPhase, pgReady bool) corev1.Pod {
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: corev1.PodStatus{Phase: phase, ContainerStatuses: []corev1.ContainerStatus{
			{Name: "bootstrap-controller", Ready: true}, {Name: "postgres", Ready: pgReady},
		}},
	}
}

func pods() []corev1.Pod {
	return []corev1.Pod{pod("pg-1", corev1.PodRunning, true), pod("pg-2", corev1.PodRunning, true), pod("pg-3", corev1.PodRunning, true)}
}

func status(o map[string]any) map[string]any { return o["status"].(map[string]any) }

func TestDecide(t *testing.T) {
	for name, tc := range map[string]struct {
		cluster *unstructured.Unstructured
		reload  bool
		reason  string
	}{
		"all hold":         {cluster: cluster(), reload: true},
		"switchover phase": {cluster: cluster(func(o map[string]any) { status(o)["phase"] = "Switchover in progress" }), reload: true},
		"plugin phase": {cluster: cluster(func(o map[string]any) {
			status(o)["phase"] = "Cluster cannot proceed to reconciliation due to an error while interacting with plugins"
		}), reload: true},
		"healthy phase":      {cluster: cluster(func(o map[string]any) { status(o)["phase"] = "Cluster in healthy state" }), reason: "allowlist"},
		"no phase":           {cluster: cluster(func(o map[string]any) { delete(status(o), "phase") }), reason: "allowlist"},
		"instance not ready": {cluster: cluster(func(o map[string]any) { status(o)["readyInstances"] = int64(2) }), reason: "not all instances"},
		"zero instances": {cluster: cluster(func(o map[string]any) {
			o["spec"].(map[string]any)["instances"] = int64(0)
			status(o)["readyInstances"] = int64(0)
		}), reason: "not all instances"},
		"no Ready condition": {cluster: cluster(func(o map[string]any) { status(o)["conditions"] = []any{} }), reason: "no Ready condition"},
		"Ready true": {cluster: cluster(func(o map[string]any) {
			status(o)["conditions"] = []any{map[string]any{"type": "Ready", "status": "True", "lastTransitionTime": now.Add(-time.Hour).Format(time.RFC3339)}}
		}), reason: "Ready is True"},
		"Ready unknown is not True": {cluster: cluster(func(o map[string]any) {
			status(o)["conditions"] = []any{map[string]any{"type": "Ready", "status": "Unknown", "lastTransitionTime": now.Add(-time.Hour).Format(time.RFC3339)}}
		}), reload: true},
		"too recent": {cluster: cluster(func(o map[string]any) {
			status(o)["conditions"] = []any{map[string]any{"type": "Ready", "status": "False", "lastTransitionTime": now.Add(-time.Minute).Format(time.RFC3339)}}
		}), reason: "less than"},
		"no lastTransitionTime": {cluster: cluster(func(o map[string]any) {
			status(o)["conditions"] = []any{map[string]any{"type": "Ready", "status": "False"}}
		}), reason: "lastTransitionTime"},
		"bad lastTransitionTime": {cluster: cluster(func(o map[string]any) {
			status(o)["conditions"] = []any{map[string]any{"type": "Ready", "status": "False", "lastTransitionTime": "yesterday"}}
		}), reason: "lastTransitionTime"},
		"hibernated": {cluster: cluster(func(o map[string]any) {
			o["metadata"].(map[string]any)["annotations"].(map[string]any)["cnpg.io/hibernation"] = "on"
		}), reason: "hibernated"},
		"hibernation off": {cluster: cluster(func(o map[string]any) {
			o["metadata"].(map[string]any)["annotations"].(map[string]any)["cnpg.io/hibernation"] = "off"
		}), reload: true},
		"fenced": {cluster: cluster(func(o map[string]any) {
			o["metadata"].(map[string]any)["annotations"].(map[string]any)["cnpg.io/fencedInstances"] = `["pg-2"]`
		}), reason: "fenced"},
		"fenced all": {cluster: cluster(func(o map[string]any) {
			o["metadata"].(map[string]any)["annotations"].(map[string]any)["cnpg.io/fencedInstances"] = `["*"]`
		}), reason: "fenced"},
		"fenced empty list": {cluster: cluster(func(o map[string]any) {
			o["metadata"].(map[string]any)["annotations"].(map[string]any)["cnpg.io/fencedInstances"] = `[]`
		}), reload: true},
		"fenced empty string": {cluster: cluster(func(o map[string]any) {
			o["metadata"].(map[string]any)["annotations"].(map[string]any)["cnpg.io/fencedInstances"] = ``
		}), reload: true},
	} {
		t.Run(name, func(t *testing.T) {
			d := stuckreload.Decide(tc.cluster, pods(), stuckreload.Options{}, now)
			assert.Equal(t, tc.reload, d.Reload, d.Reason)

			if tc.reload {
				assert.Equal(t, []string{"pg-1", "pg-2", "pg-3"}, d.Pods)
				return
			}

			assert.Empty(t, d.Pods)
			assert.Contains(t, d.Reason, tc.reason)
		})
	}
}

func TestDecide_Pods(t *testing.T) {
	terminating := pod("pg-4", corev1.PodRunning, true)
	ts := metav1.Now()
	terminating.DeletionTimestamp = &ts

	noPostgres := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pg-5"}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}

	d := stuckreload.Decide(cluster(), []corev1.Pod{
		pod("pg-1", corev1.PodRunning, true),
		pod("pg-2", corev1.PodRunning, false),
		pod("pg-3", corev1.PodPending, true),
		terminating, noPostgres,
	}, stuckreload.Options{}, now)
	require.True(t, d.Reload)
	assert.Equal(t, []string{"pg-1"}, d.Pods)

	d = stuckreload.Decide(cluster(), []corev1.Pod{pod("pg-1", corev1.PodRunning, false)}, stuckreload.Options{}, now)
	assert.False(t, d.Reload)
	assert.Contains(t, d.Reason, "no running instance pod")
}

func TestDecide_Options(t *testing.T) {
	c := cluster(func(o map[string]any) { status(o)["phase"] = "Custom phase" })
	assert.False(t, stuckreload.Decide(c, pods(), stuckreload.Options{}, now).Reload)
	assert.True(t, stuckreload.Decide(c, pods(), stuckreload.Options{Phases: []string{"Custom phase"}}, now).Reload)
	// 10 minutes stuck: acts at 5m (default) and 10m, not at 11m.
	assert.True(t, stuckreload.Decide(cluster(), pods(), stuckreload.Options{StuckFor: 10 * time.Minute}, now).Reload)
	assert.False(t, stuckreload.Decide(cluster(), pods(), stuckreload.Options{StuckFor: 11 * time.Minute}, now).Reload)
}

func TestParsePhases(t *testing.T) {
	assert.Equal(t, stuckreload.DefaultPhases, stuckreload.ParsePhases(nil))
	assert.Equal(t, stuckreload.DefaultPhases, stuckreload.ParsePhases([]string{" ", ","}))
	assert.Equal(t, []string{"A b", "C", "D"}, stuckreload.ParsePhases([]string{"A b, C", "D", "C", ""}))
	assert.Len(t, stuckreload.DefaultPhases, 3)
}

func TestInstanceSelector(t *testing.T) {
	assert.Equal(t, "cnpg.io/cluster=pg,cnpg.io/podRole=instance", stuckreload.InstanceSelector("pg"))
}

func TestCheckReload(t *testing.T) {
	require.NoError(t, stuckreload.CheckReload("t\n"))
	require.NoError(t, stuckreload.CheckReload("t"))
	require.Error(t, stuckreload.CheckReload("f\n"))
	require.Error(t, stuckreload.CheckReload(""))
	require.Error(t, stuckreload.CheckReload("t\nt\n"))
}

type fakeSource struct {
	c      *unstructured.Unstructured
	pods   []corev1.Pod
	events []string
	evErr  error
}

func (f *fakeSource) Cluster(context.Context, string, string) (*unstructured.Unstructured, error) {
	return f.c, nil
}

func (f *fakeSource) InstancePods(context.Context, string, string) ([]corev1.Pod, error) {
	return f.pods, nil
}

func (f *fakeSource) Event(_ context.Context, _ *unstructured.Unstructured, msg string) error {
	f.events = append(f.events, msg)
	return f.evErr
}

type fakeExec struct {
	answers map[string]string
	errs    map[string]error
	calls   []string
	cmd     []string
}

func (f *fakeExec) Exec(_ context.Context, _, pod, container string, command []string) (string, error) {
	f.calls = append(f.calls, pod+"/"+container)
	f.cmd = command

	if err := f.errs[pod]; err != nil {
		return "", err
	}

	if a, ok := f.answers[pod]; ok {
		return a, nil
	}

	return "t\n", nil
}

func opts() stuckreload.RunOptions {
	return stuckreload.RunOptions{Namespace: "app", Cluster: "pg", Now: func() time.Time { return now }}
}

func TestRun_Reloads(t *testing.T) {
	src := &fakeSource{c: cluster(), pods: pods()}
	ex := &fakeExec{}

	require.NoError(t, stuckreload.Run(t.Context(), opts(), src, ex))
	assert.Equal(t, []string{"pg-1/postgres", "pg-2/postgres", "pg-3/postgres"}, ex.calls)
	assert.Equal(t, []string{"psql", "-U", "postgres", "-XAtqc", "SELECT pg_reload_conf()"}, ex.cmd)
	require.Len(t, src.events, 1)
	assert.Contains(t, src.events[0], "pg-1, pg-2, pg-3")
}

func TestRun_NothingToDo(t *testing.T) {
	src := &fakeSource{c: cluster(func(o map[string]any) { status(o)["phase"] = "Cluster in healthy state" }), pods: pods()}
	ex := &fakeExec{}

	require.NoError(t, stuckreload.Run(t.Context(), opts(), src, ex))
	assert.Empty(t, ex.calls)
	assert.Empty(t, src.events)
}

func TestRun_DryRun(t *testing.T) {
	src := &fakeSource{c: cluster(), pods: pods()}
	ex := &fakeExec{}
	o := opts()
	o.DryRun = true

	require.NoError(t, stuckreload.Run(t.Context(), o, src, ex))
	assert.Empty(t, ex.calls)
	assert.Empty(t, src.events)
}

func TestRun_ExecFailureAndWrongAnswer(t *testing.T) {
	src := &fakeSource{c: cluster(), pods: pods()}
	ex := &fakeExec{errs: map[string]error{"pg-1": errors.New("boom")}, answers: map[string]string{"pg-2": "f\n"}}

	err := stuckreload.Run(t.Context(), opts(), src, ex)
	require.ErrorIs(t, err, stuckreload.ErrReload)
	assert.Contains(t, err.Error(), "pg-1, pg-2")
	// every pod is still tried, and the one that worked is reported.
	assert.Len(t, ex.calls, 3)
	require.Len(t, src.events, 1)
	assert.Contains(t, src.events[0], "pg-3")
	assert.NotContains(t, src.events[0], "pg-1")
}

func TestRun_EventFailureIsNotFatal(t *testing.T) {
	src := &fakeSource{c: cluster(), pods: pods(), evErr: errors.New("forbidden")}

	require.NoError(t, stuckreload.Run(t.Context(), opts(), src, &fakeExec{}))
}
