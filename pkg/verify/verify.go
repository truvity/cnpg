// Package verify runs read-only assertions against a live CloudNativePG
// Cluster: its health, continuous archiving and backups, the TLS and
// user-provided Secrets it depends on, and the pg_hba / pg_ident it was
// given. Nothing here writes to the cluster.
//
// The assertions read through the Source interface, so they are tested
// against fixtures; KubeSource is the client-go implementation.
package verify

import (
	"context"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// ErrNotFound is what a Source returns, wrapped or not, for an object
// that does not exist.
var ErrNotFound = errors.New("not found")

// Source is the read side of the Kubernetes API that the assertions need.
type Source interface {
	// Cluster returns the postgresql.cnpg.io Cluster.
	Cluster(ctx context.Context, namespace, name string) (*unstructured.Unstructured, error)
	// Secret returns a Secret, or an error wrapping ErrNotFound.
	Secret(ctx context.Context, namespace, name string) (*corev1.Secret, error)
}

// Status of one assertion.
type Status string

// The statuses. A Skip is an assertion that does not apply (an optional
// Secret that the cluster does not use); it never fails the run.
const (
	Pass Status = "PASS"
	Fail Status = "FAIL"
	Skip Status = "SKIP"
)

// Result is the outcome of one assertion.
type Result struct {
	Name    string `json:"name"`
	Status  Status `json:"status"`
	Message string `json:"message"`
}

// Options name the cluster and set the thresholds.
type Options struct {
	Namespace string
	Cluster   string
	// MaxBackupAge is how old the last successful backup may be.
	MaxBackupAge time.Duration
	// MinCertValidity is how long a certificate must remain valid.
	MinCertValidity time.Duration
	// Now is the clock; zero means time.Now.
	Now time.Time
}

// DefaultMaxBackupAge and DefaultMinCertValidity are the flag defaults.
const (
	DefaultMaxBackupAge    = 26 * time.Hour
	DefaultMinCertValidity = 14 * 24 * time.Hour
)

// Report is every Result of one run.
type Report struct {
	Namespace string   `json:"namespace"`
	Cluster   string   `json:"cluster"`
	Results   []Result `json:"results"`
}

// Failed reports whether any assertion failed.
func (r Report) Failed() bool {
	for _, x := range r.Results {
		if x.Status == Fail {
			return true
		}
	}
	return false
}

func (o Options) now() time.Time {
	if o.Now.IsZero() {
		return time.Now()
	}
	return o.Now
}

// Run evaluates every assertion. An error is returned only when the
// Cluster itself cannot be read; every other problem is a FAIL Result.
func Run(ctx context.Context, src Source, opt Options) (Report, error) {
	if opt.Namespace == "" || opt.Cluster == "" {
		return Report{}, errors.New("namespace and cluster are required")
	}
	if opt.MaxBackupAge <= 0 {
		opt.MaxBackupAge = DefaultMaxBackupAge
	}
	if opt.MinCertValidity <= 0 {
		opt.MinCertValidity = DefaultMinCertValidity
	}
	c, err := src.Cluster(ctx, opt.Namespace, opt.Cluster)
	if err != nil {
		return Report{}, fmt.Errorf("read Cluster %s/%s: %w", opt.Namespace, opt.Cluster, err)
	}
	rep := Report{Namespace: opt.Namespace, Cluster: opt.Cluster}
	rep.Results = append(rep.Results, checkHealth(c)...)
	rep.Results = append(rep.Results, checkArchiving(c)...)
	rep.Results = append(rep.Results, checkBackup(c, opt)...)
	rep.Results = append(rep.Results, checkTLS(ctx, src, c, opt)...)
	rep.Results = append(rep.Results, checkReloadLabels(ctx, src, c, opt)...)
	rep.Results = append(rep.Results, checkHBA(c)...)
	return rep, nil
}

func res(name string, ok bool, pass, fail string) Result {
	if ok {
		return Result{Name: name, Status: Pass, Message: pass}
	}
	return Result{Name: name, Status: Fail, Message: fail}
}
