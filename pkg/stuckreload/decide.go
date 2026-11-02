// Package stuckreload works around cloudnative-pg/cloudnative-pg#11627: after
// a primary change (a node rotation, say) the instance manager rewrites
// custom.conf while postgres is not yet accepting connections, and the reload
// it decided on is dropped and never retried. The Cluster then sits at a
// transient status.phase with Ready=False although every instance is ready,
// until something reloads the configuration. `SELECT pg_reload_conf()` on
// every instance does, within seconds; it changes no data and is idempotent.
//
// Decide is the pure decision (no API, no clock besides its argument); Run
// reads the Cluster and its pods, decides, and reloads through an Executor.
package stuckreload

import (
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	// DefaultStuckFor is how long Ready must have been not-True before the
	// guard acts.
	DefaultStuckFor = 5 * time.Minute

	// Container is the container of an instance pod that runs postgres.
	Container = "postgres"

	annotationHibernation = "cnpg.io/hibernation"
	annotationFenced      = "cnpg.io/fencedInstances"
	labelCluster          = "cnpg.io/cluster"
	labelPodRole          = "cnpg.io/podRole"
	podRoleInstance       = "instance"
)

// DefaultPhases are the status.phase values the lost reload is known to leave
// behind.
var DefaultPhases = []string{
	"Waiting for the instances to become active",
	"Switchover in progress",
	"Cluster cannot proceed to reconciliation due to an error while interacting with plugins",
}

// InstanceSelector is the label selector of the instance pods of a Cluster.
func InstanceSelector(cluster string) string {
	return labelCluster + "=" + cluster + "," + labelPodRole + "=" + podRoleInstance
}

// ParsePhases flattens repeated and comma-separated --phases values, trimming
// blanks and dropping empties and duplicates, keeping the first occurrence's
// order. No value at all gives DefaultPhases.
func ParsePhases(values []string) []string {
	var out []string

	seen := map[string]bool{}

	for _, v := range values {
		for _, p := range strings.Split(v, ",") {
			p = strings.TrimSpace(p)
			if p == "" || seen[p] {
				continue
			}

			seen[p] = true

			out = append(out, p)
		}
	}

	if len(out) == 0 {
		return append([]string(nil), DefaultPhases...)
	}

	return out
}

// Options are the inputs of Decide besides the objects.
type Options struct {
	// StuckFor is how long Ready must have been not-True. Zero means
	// DefaultStuckFor.
	StuckFor time.Duration
	// Phases is the allowlist of status.phase. Empty means DefaultPhases.
	Phases []string
}

// Decision is what Decide concludes.
type Decision struct {
	// Phase is the Cluster's status.phase, for the log line.
	Phase string
	// Reload is true when the signature matched.
	Reload bool
	// Reason says why nothing is done (Reload false), or what matched.
	Reason string
	// Pods are the instance pods to reload (Reload true), sorted as given.
	Pods []string
}

// Decide applies the signature of the bug to a Cluster and its instance pods.
// It acts only when all of these hold:
//  1. status.phase is in the allowlist;
//  2. spec.instances > 0 and status.readyInstances == spec.instances;
//  3. the Ready condition exists, is not "True", and its lastTransitionTime is
//     older than StuckFor (a missing or unparsable time means do nothing);
//  4. the Cluster is not hibernated and has no fenced instances.
//
// The target pods are the Running instance pods of the Cluster whose postgres
// container is ready (replicas and the primary alike).
func Decide(cluster *unstructured.Unstructured, pods []corev1.Pod, opts Options, now time.Time) Decision {
	stuckFor := opts.StuckFor
	if stuckFor <= 0 {
		stuckFor = DefaultStuckFor
	}

	phases := opts.Phases
	if len(phases) == 0 {
		phases = DefaultPhases
	}

	phase, _, _ := unstructured.NestedString(cluster.Object, "status", "phase")
	d := Decision{Phase: phase}

	if !contains(phases, phase) {
		d.Reason = fmt.Sprintf("phase %q is not in the allowlist", phase)
		return d
	}

	want, _, _ := unstructured.NestedInt64(cluster.Object, "spec", "instances")
	ready, _, _ := unstructured.NestedInt64(cluster.Object, "status", "readyInstances")

	if want <= 0 || ready != want {
		d.Reason = fmt.Sprintf("not all instances are ready (%d of %d)", ready, want)
		return d
	}

	cond := readyCondition(cluster)
	if cond == nil {
		d.Reason = "no Ready condition"
		return d
	}

	if status, _ := cond["status"].(string); status == "True" {
		d.Reason = "Ready is True"
		return d
	}

	since, _ := cond["lastTransitionTime"].(string)

	t, err := time.Parse(time.RFC3339, since)
	if since == "" || err != nil {
		d.Reason = "Ready has no usable lastTransitionTime"
		return d
	}

	if age := now.Sub(t); age < stuckFor {
		d.Reason = fmt.Sprintf("Ready has been not-True for %s, less than %s", age.Round(time.Second), stuckFor)
		return d
	}

	ann := cluster.GetAnnotations()
	if ann[annotationHibernation] == "on" {
		d.Reason = "the Cluster is hibernated"
		return d
	}

	if f := strings.TrimSpace(ann[annotationFenced]); f != "" && f != "[]" {
		d.Reason = "the Cluster has fenced instances"
		return d
	}

	for i := range pods {
		p := &pods[i]
		if p.Status.Phase != corev1.PodRunning || p.DeletionTimestamp != nil || !postgresReady(p) {
			continue
		}

		d.Pods = append(d.Pods, p.Name)
	}

	if len(d.Pods) == 0 {
		d.Reason = "no running instance pod with a ready postgres container"
		return d
	}

	d.Reload = true
	d.Reason = "the Cluster shows the signature of cloudnative-pg#11627"

	return d
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}

	return false
}

func readyCondition(cluster *unstructured.Unstructured) map[string]any {
	conds, _, _ := unstructured.NestedSlice(cluster.Object, "status", "conditions")
	for _, c := range conds {
		m, ok := c.(map[string]any)
		if ok && m["type"] == "Ready" {
			return m
		}
	}

	return nil
}

func postgresReady(p *corev1.Pod) bool {
	for _, cs := range p.Status.ContainerStatuses {
		if cs.Name == Container {
			return cs.Ready
		}
	}

	return false
}
