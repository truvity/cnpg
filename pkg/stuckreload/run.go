package stuckreload

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// ReloadSQL is the one statement the guard ever executes.
const ReloadSQL = "SELECT pg_reload_conf()"

// EventReason is the reason of the Event created on the Cluster.
const EventReason = "StuckReload"

// ReloadCommand is the command run in the postgres container.
var ReloadCommand = []string{"psql", "-U", "postgres", "-XAtqc", ReloadSQL}

// Executor runs a command in a container of a pod and returns its stdout.
type Executor interface {
	Exec(ctx context.Context, namespace, pod, container string, command []string) (stdout string, err error)
}

// Source reads the Cluster and its instance pods and records the Event.
type Source interface {
	Cluster(ctx context.Context, namespace, name string) (*unstructured.Unstructured, error)
	InstancePods(ctx context.Context, namespace, cluster string) ([]corev1.Pod, error)
	// Event creates a Normal Event with EventReason on the Cluster.
	Event(ctx context.Context, cluster *unstructured.Unstructured, message string) error
}

// RunOptions configure Run.
type RunOptions struct {
	Namespace string
	Cluster   string
	Options
	DryRun bool
	// Now is the clock; zero means time.Now.
	Now func() time.Time
	// Log receives one structured line per decision; nil discards.
	Log *slog.Logger
}

// CheckReload verifies the stdout of the reload statement: `t` (psql -At
// prints the boolean result) and nothing else.
func CheckReload(stdout string) error {
	if got := strings.TrimSpace(stdout); got != "t" {
		return fmt.Errorf("pg_reload_conf() answered %q, not \"t\"", got)
	}

	return nil
}

// ErrReload marks a run in which an API call or an exec failed.
var ErrReload = errors.New("stuck-reload failed")

// Run reads the Cluster, decides and, when the signature matches, reloads
// every target pod. It returns nil when there was nothing to do or the
// reload is done, and an error wrapping ErrReload (or the API error) when
// the API or an exec failed.
func Run(ctx context.Context, o RunOptions, src Source, ex Executor) error {
	log := o.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	now := time.Now
	if o.Now != nil {
		now = o.Now
	}

	cluster, err := src.Cluster(ctx, o.Namespace, o.Cluster)
	if err != nil {
		return fmt.Errorf("get cluster: %w", err)
	}

	pods, err := src.InstancePods(ctx, o.Namespace, o.Cluster)
	if err != nil {
		return fmt.Errorf("list instance pods: %w", err)
	}

	d := Decide(cluster, pods, o.Options, now())
	if !d.Reload {
		log.Info("stuck-reload decision", "cluster", o.Cluster, "phase", d.Phase, "action", "none", "reason", d.Reason, "pods", []string{})
		return nil
	}

	if o.DryRun {
		log.Info("stuck-reload decision", "cluster", o.Cluster, "phase", d.Phase, "action", "dry-run", "reason", d.Reason, "pods", d.Pods)
		return nil
	}

	var done []string

	var failed []string

	for _, pod := range d.Pods {
		out, err := ex.Exec(ctx, o.Namespace, pod, Container, ReloadCommand)
		if err == nil {
			err = CheckReload(out)
		}

		if err != nil {
			log.Error("stuck-reload exec failed", "cluster", o.Cluster, "pod", pod, "error", err.Error())

			failed = append(failed, pod)

			continue
		}

		done = append(done, pod)
	}

	log.Info("stuck-reload decision", "cluster", o.Cluster, "phase", d.Phase, "action", "reload", "reason", d.Reason, "pods", done)

	if len(done) > 0 {
		msg := fmt.Sprintf("pg_reload_conf() run on %s: the Cluster showed phase %q with Ready=False and every instance ready (cloudnative-pg#11627)", strings.Join(done, ", "), d.Phase)
		if err := src.Event(ctx, cluster, msg); err != nil {
			log.Warn("stuck-reload event not created", "cluster", o.Cluster, "error", err.Error())
		}
	}

	if len(failed) > 0 {
		return fmt.Errorf("%w: reload failed on %s", ErrReload, strings.Join(failed, ", "))
	}

	return nil
}
