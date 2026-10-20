// Copyright 2026 Truvity B.V.. All rights reserved.
// SPDX-License-Identifier: MIT

package drill

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// CloudNativePG's names this package relies on (pkg/utils, api/v1).
const (
	phaseHealthy       = "Cluster in healthy state"
	phaseUnrecoverable = "Cluster is unrecoverable and needs manual intervention"
	labelCluster       = "cnpg.io/cluster"
	labelJobRole       = "cnpg.io/jobRole"
	jobRoleRecovery    = "full-recovery"
	// The recovery Job's container is named after its role.
	recoveryContainer = jobRoleRecovery

	heartbeatEvery = 5 * time.Minute
	ringSize       = 60
)

// errUnrecoverable ends a wait early: waiting longer cannot help.
var errUnrecoverable = errors.New("the recovery cannot succeed")

// recoveryLog follows the recovery Job's PostgreSQL log: it keeps the last
// lines for diagnostics and the commit time of the last replayed transaction.
type recoveryLog struct {
	mu       sync.Mutex
	lines    []string
	lastTx   time.Time
	followed map[string]bool
	wg       sync.WaitGroup
}

func newRecoveryLog() *recoveryLog { return &recoveryLog{followed: map[string]bool{}} }

func (l *recoveryLog) add(line string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if t, ok := ParseLastTransaction(line); ok && t.After(l.lastTx) {
		l.lastTx = t
	}
	l.lines = append(l.lines, line)
	if len(l.lines) > ringSize {
		l.lines = l.lines[len(l.lines)-ringSize:]
	}
}

func (l *recoveryLog) lastTransaction() time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.lastTx
}

func (l *recoveryLog) tail() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.lines...)
}

func (l *recoveryLog) read(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		l.add(sc.Text())
	}
}

// watch starts reading a recovery pod's log once: following it while the pod
// runs, or reading it whole once it has finished (a short recovery can end
// between two polls; the log stays readable until the operator deletes the
// Job).
func (l *recoveryLog) watch(ctx context.Context, k Kube, ns string, pod corev1.Pod) {
	follow := pod.Status.Phase == corev1.PodRunning
	if !follow && pod.Status.Phase != corev1.PodSucceeded && pod.Status.Phase != corev1.PodFailed {
		return
	}
	l.mu.Lock()
	if l.followed[pod.Name] {
		l.mu.Unlock()
		return
	}
	l.followed[pod.Name] = true
	l.mu.Unlock()
	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		rc, err := k.Logs(ctx, ns, pod.Name, recoveryContainer, follow, -1)
		if err != nil {
			l.add(fmt.Sprintf("(cnpgctl: cannot read the log of %s: %v)", pod.Name, err))
			return
		}
		defer func() { _ = rc.Close() }()
		l.read(rc)
	}()
}

func clusterStatus(c *unstructured.Unstructured) (phase, reason string, ready int64, primary string) {
	phase, _, _ = unstructured.NestedString(c.Object, "status", "phase")
	reason, _, _ = unstructured.NestedString(c.Object, "status", "phaseReason")
	ready, _, _ = unstructured.NestedInt64(c.Object, "status", "readyInstances")
	primary, _, _ = unstructured.NestedString(c.Object, "status", "currentPrimary")
	return
}

// waitHealthy polls the drill Cluster until it is healthy with a ready,
// known primary, the recovery Job fails MaxRecoveryAttempts times, the
// Cluster is reported unrecoverable, or ctx ends.
func (d *driller) waitHealthy(ctx context.Context) error {
	o := d.plan.Options
	var lastPhase string
	lastBeat := time.Now()
	failed := map[string]bool{}
	for {
		c, err := d.k.Get(ctx, ClusterGVR, o.DrillNamespace, o.DrillName)
		switch {
		case errors.Is(err, ErrNotFound):
			return fmt.Errorf("%w: the drill Cluster disappeared while recovering", errUnrecoverable)
		case err != nil:
			d.logf("reading the drill Cluster: %v (retrying)", err)
		default:
			phase, reason, ready, primary := clusterStatus(c)
			if phase != lastPhase {
				d.logf("drill Cluster phase: %q %s", phase, reason)
				lastPhase = phase
			}
			if phase == phaseHealthy && ready >= 1 && primary != "" {
				return nil
			}
			if phase == phaseUnrecoverable {
				return fmt.Errorf("%w: the operator reports %q: %s", errUnrecoverable, phase, reason)
			}
		}
		pods, err := d.k.ListPods(ctx, o.DrillNamespace, labelCluster+"="+o.DrillName)
		if err == nil {
			for _, p := range pods {
				if p.Labels[labelJobRole] != jobRoleRecovery {
					continue
				}
				d.log.watch(ctx, d.k, o.DrillNamespace, p)
				if p.Status.Phase == corev1.PodFailed {
					failed[p.Name] = true
				}
			}
			if len(failed) >= o.MaxRecoveryAttempts {
				names := sortedKeys(failed)
				return fmt.Errorf("%w: the recovery Job failed %d times (%s)", errUnrecoverable, len(failed), strings.Join(names, ", "))
			}
		}
		if time.Since(lastBeat) >= heartbeatEvery {
			lastBeat = time.Now()
			d.logf("still recovering: phase %q, pods: %s", lastPhase, podSummary(pods))
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for the drill Cluster to become healthy (phase %q): %w", lastPhase, ctx.Err())
		case <-time.After(o.PollInterval):
		}
	}
}

func podSummary(pods []corev1.Pod) string {
	if len(pods) == 0 {
		return "none"
	}
	var parts []string
	for _, p := range pods {
		parts = append(parts, p.Name+"="+string(p.Status.Phase))
	}
	sort.Strings(parts)
	return strings.Join(parts, " ")
}

// diagnose prints what an operator needs to start from after a failure: the
// Cluster's phase and conditions, every drill pod's containers, the events of
// the drill's objects and the last log lines.
func (d *driller) diagnose(ctx context.Context) {
	o := d.plan.Options
	w := d.out
	_, _ = fmt.Fprintf(w, "---- diagnostics for drill %s/%s ----\n", o.DrillNamespace, o.DrillName)
	if c, err := d.k.Get(ctx, ClusterGVR, o.DrillNamespace, o.DrillName); err == nil {
		phase, reason, ready, primary := clusterStatus(c)
		_, _ = fmt.Fprintf(w, "cluster: phase %q reason %q readyInstances %d currentPrimary %q\n", phase, reason, ready, primary)
		conds, _, _ := unstructured.NestedSlice(c.Object, "status", "conditions")
		for _, raw := range conds {
			if m, ok := raw.(map[string]any); ok {
				_, _ = fmt.Fprintf(w, "  condition %v=%v reason %v: %v\n", m["type"], m["status"], m["reason"], m["message"])
			}
		}
	} else {
		_, _ = fmt.Fprintf(w, "cluster: %v\n", err)
	}
	pods, err := d.k.ListPods(ctx, o.DrillNamespace, labelCluster+"="+o.DrillName)
	if err != nil {
		_, _ = fmt.Fprintf(w, "pods: %v\n", err)
	}
	for _, p := range pods {
		_, _ = fmt.Fprintf(w, "pod %s: phase %s\n", p.Name, p.Status.Phase)
		for _, cond := range p.Status.Conditions {
			if cond.Status != corev1.ConditionTrue && cond.Message != "" {
				_, _ = fmt.Fprintf(w, "  condition %s=%s: %s\n", cond.Type, cond.Status, cond.Message)
			}
		}
		statuses := append(append([]corev1.ContainerStatus{}, p.Status.InitContainerStatuses...), p.Status.ContainerStatuses...)
		for _, cs := range statuses {
			_, _ = fmt.Fprintf(w, "  container %s: %s restarts %d\n", cs.Name, containerState(cs.State), cs.RestartCount)
		}
	}
	if events, err := d.k.ListEvents(ctx, o.DrillNamespace); err == nil {
		sort.Slice(events, func(i, j int) bool { return eventTime(events[i]).Before(eventTime(events[j])) })
		var mine []corev1.Event
		for _, e := range events {
			if strings.HasPrefix(e.InvolvedObject.Name, o.DrillName) {
				mine = append(mine, e)
			}
		}
		if len(mine) > 25 {
			mine = mine[len(mine)-25:]
		}
		for _, e := range mine {
			_, _ = fmt.Fprintf(w, "event %s %s/%s %s: %s\n", eventTime(e).UTC().Format(time.RFC3339), e.InvolvedObject.Kind, e.InvolvedObject.Name, e.Reason, oneLine(e.Message))
		}
	}
	for _, p := range pods {
		var names []string
		for _, c := range p.Spec.InitContainers {
			names = append(names, c.Name)
		}
		for _, c := range p.Spec.Containers {
			names = append(names, c.Name)
		}
		for _, c := range names {
			rc, err := d.k.Logs(ctx, o.DrillNamespace, p.Name, c, false, 20)
			if err != nil {
				continue
			}
			b, _ := io.ReadAll(io.LimitReader(rc, 64*1024))
			_ = rc.Close()
			if len(strings.TrimSpace(string(b))) == 0 {
				continue
			}
			_, _ = fmt.Fprintf(w, "log %s/%s (last 20 lines):\n", p.Name, c)
			for _, line := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
				_, _ = fmt.Fprintf(w, "  %s\n", line)
			}
		}
	}
	if lines := d.log.tail(); len(lines) > 0 {
		_, _ = fmt.Fprintf(w, "recovery log (last %d lines followed):\n", len(lines))
		for _, line := range lines {
			_, _ = fmt.Fprintf(w, "  %s\n", line)
		}
	}
	_, _ = fmt.Fprintln(w, "---- end of diagnostics ----")
}

func containerState(s corev1.ContainerState) string {
	switch {
	case s.Running != nil:
		return "running"
	case s.Waiting != nil:
		return "waiting " + s.Waiting.Reason + " " + oneLine(s.Waiting.Message)
	case s.Terminated != nil:
		return fmt.Sprintf("terminated %s exit %d %s", s.Terminated.Reason, s.Terminated.ExitCode, oneLine(s.Terminated.Message))
	}
	return "unknown"
}

func eventTime(e corev1.Event) time.Time {
	switch {
	case !e.LastTimestamp.IsZero():
		return e.LastTimestamp.Time
	case !e.EventTime.IsZero():
		return e.EventTime.Time
	}
	return e.CreationTimestamp.Time
}

func oneLine(s string) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	if len(s) > 400 {
		s = s[:400] + "..."
	}
	return s
}
