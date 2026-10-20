// Copyright 2026 Truvity B.V.. All rights reserved.
// SPDX-License-Identifier: MIT

package drill

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// ErrInProgress is returned when a drill of the same source is already
// running (its Cluster exists and is younger than the timeout). Nothing was
// created, deleted or recorded.
var ErrInProgress = errors.New("a drill of this cluster is already running")

// Deps are what Run talks to.
type Deps struct {
	Kube Kube
	// NewProber opens the SQL side (default NewPGProber).
	NewProber func(ConnInfo) (Prober, error)
	// Out receives progress and diagnostics (default: discarded).
	Out io.Writer
}

type driller struct {
	k         Kube
	plan      *Plan
	out       io.Writer
	log       *recoveryLog
	newProber func(ConnInfo) (Prober, error)
}

func (d *driller) logf(format string, args ...any) {
	_, _ = fmt.Fprintf(d.out, "%s  %s\n", time.Now().UTC().Format(time.RFC3339), fmt.Sprintf(format, args...))
}

// Run performs one drill: plan (read-only), refuse or clear an existing
// drill, create the ObjectStore and the Cluster, wait for the recovery, run
// the checks, delete everything it created, and record the result. A drill
// that fails is a Report with a failed step, not an error; the error is for
// invalid options and ErrInProgress. The teardown runs on success, failure,
// timeout and cancellation alike, on a context of its own.
func Run(ctx context.Context, deps Deps, opt Options) (*Report, error) {
	o, err := opt.withDefaults()
	if err != nil {
		return nil, err
	}
	if deps.Kube == nil {
		return nil, errors.New("no Kubernetes client")
	}
	if deps.NewProber == nil {
		deps.NewProber = func(i ConnInfo) (Prober, error) { return NewPGProber(i) }
	}
	if deps.Out == nil {
		deps.Out = io.Discard
	}
	rep := &Report{
		SourceNamespace: o.Namespace, SourceCluster: o.Cluster,
		DrillNamespace: o.DrillNamespace, DrillName: o.DrillName,
		StartedAt: o.Now().UTC(),
	}
	d := &driller{k: deps.Kube, plan: &Plan{Options: o}, out: deps.Out, log: newRecoveryLog(), newProber: deps.NewProber}

	runCtx, cancel := context.WithTimeout(ctx, o.Timeout)
	err = d.run(runCtx, rep)
	cancel()
	if errors.Is(err, ErrInProgress) {
		return rep, err
	}
	rep.FinishedAt = o.Now().UTC()
	rep.DurationSeconds = rep.FinishedAt.Sub(rep.StartedAt).Seconds()
	if !o.NoRecord {
		rctx, rcancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		if err := Record(rctx, deps.Kube, rep); err != nil {
			rep.fail("record result", "%v", err)
		}
		rcancel()
	}
	return rep, nil
}

func (d *driller) run(ctx context.Context, rep *Report) error {
	o := d.plan.Options
	plan, err := MakePlan(ctx, d.k, o)
	if err != nil {
		rep.fail("plan", "%v", err)
		return nil
	}
	d.plan = plan
	rep.DestinationPath, rep.ServerName = plan.DestinationPath, plan.ServerName
	for _, n := range plan.Notes {
		d.logf("note: %s", n)
	}
	image := plan.Image
	if image == "" {
		image = "(operator default)"
	}
	storage := plan.StorageSize
	if plan.WALStorageSize != "" {
		storage += " + WAL " + plan.WALStorageSize
	}
	rep.pass("plan", "archive %s server %s; image %s; storage %s (class %q)", plan.DestinationPath, plan.ServerName, image, storage, plan.StorageClass)

	if err := d.clearExisting(ctx); err != nil {
		if errors.Is(err, ErrInProgress) {
			rep.add("one drill at a time", Skip, "%v", err)
			return err
		}
		rep.fail("one drill at a time", "%v", err)
		return nil
	}

	cluster, err := plan.Cluster(rep.StartedAt)
	if err != nil {
		rep.fail("build", "%v", err)
		return nil
	}
	store, err := plan.ObjectStore()
	if err != nil {
		rep.fail("build", "%v", err)
		return nil
	}
	if o.mutate != nil {
		o.mutate(cluster, store)
	}
	if err := AssertNoArchive(cluster, store); err != nil {
		rep.fail("no archiving", "%v", err)
		return nil
	}
	rep.pass("no archiving", "the drill Cluster has no backup section and no plugin entry; its ObjectStore has no retention policy and reads with the ambient identity of ServiceAccount %s", o.DrillName)

	defer d.teardown(ctx, rep)

	if err := d.k.Create(ctx, ObjectStoreGVR, store); err != nil {
		rep.fail("create", "ObjectStore %s/%s: %v", o.DrillNamespace, store.GetName(), err)
		return nil
	}
	if err := d.k.Create(ctx, ClusterGVR, cluster); err != nil {
		rep.fail("create", "Cluster %s/%s: %v", o.DrillNamespace, o.DrillName, err)
		return nil
	}
	d.logf("created ObjectStore %s and Cluster %s in %s; recovering %s (server %s)", store.GetName(), o.DrillName, o.DrillNamespace, plan.DestinationPath, plan.ServerName)
	rep.pass("create", "ObjectStore %s and Cluster %s in %s", store.GetName(), o.DrillName, o.DrillNamespace)

	started := time.Now()
	if err := d.waitHealthy(ctx); err != nil {
		rep.fail("recovery", "%v", err)
		dctx, dcancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
		d.diagnose(dctx)
		dcancel()
		return nil
	}
	rep.pass("recovery", "the drill Cluster is healthy with a promoted primary after %s", time.Since(started).Round(time.Second))
	d.awaitLogs(30 * time.Second)

	d.check(ctx, rep)
	if ctx.Err() != nil && !rep.Failed() {
		rep.fail("checks", "timed out: %v", ctx.Err())
	}
	return nil
}

// awaitLogs gives the recovery log readers a bounded moment to finish: the
// Job has ended once the Cluster is healthy, so their streams are at EOF.
func (d *driller) awaitLogs(limit time.Duration) {
	done := make(chan struct{})
	go func() { d.log.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(limit):
		d.logf("the recovery log is still being read after %s; going on without it", limit)
	}
}

// clearExisting enforces one drill at a time per source. An existing drill
// Cluster younger than the timeout is a drill in progress (ErrInProgress);
// an older one is stale and deleted first, with a leftover ObjectStore. An
// object of either name the drill did not create is never touched.
func (d *driller) clearExisting(ctx context.Context) error {
	o := d.plan.Options
	c, err := d.k.Get(ctx, ClusterGVR, o.DrillNamespace, o.DrillName)
	switch {
	case errors.Is(err, ErrNotFound):
	case err != nil:
		return fmt.Errorf("read Cluster %s/%s: %w", o.DrillNamespace, o.DrillName, err)
	case !managed(c):
		return fmt.Errorf("a Cluster %s/%s exists that no drill created (no %s=%s label): refusing to touch it", o.DrillNamespace, o.DrillName, LabelManagedBy, ManagedBy)
	default:
		age := o.Now().Sub(c.GetCreationTimestamp().Time)
		if age < o.Timeout {
			return fmt.Errorf("%w: Cluster %s/%s was created %s ago, within the timeout %s", ErrInProgress, o.DrillNamespace, o.DrillName, age.Round(time.Second), o.Timeout)
		}
		d.logf("deleting a stale drill: Cluster %s/%s was created %s ago, beyond the timeout %s", o.DrillNamespace, o.DrillName, age.Round(time.Second), o.Timeout)
	}
	s, err := d.k.Get(ctx, ObjectStoreGVR, o.DrillNamespace, StoreName(o.DrillName))
	switch {
	case errors.Is(err, ErrNotFound):
	case err != nil:
		return fmt.Errorf("read ObjectStore %s/%s: %w", o.DrillNamespace, StoreName(o.DrillName), err)
	case !managed(s):
		return fmt.Errorf("an ObjectStore %s/%s exists that no drill created: refusing to touch it", o.DrillNamespace, StoreName(o.DrillName))
	}
	if c == nil && s == nil {
		return nil
	}
	tctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), o.TeardownTimeout)
	defer cancel()
	return d.deleteAll(tctx)
}

// teardown deletes the drill's Cluster and ObjectStore and waits until the
// Cluster, its pods and its volume claims are gone.
func (d *driller) teardown(ctx context.Context, rep *Report) {
	o := d.plan.Options
	tctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), o.TeardownTimeout)
	defer cancel()
	started := time.Now()
	if err := d.deleteAll(tctx); err != nil {
		rep.fail("teardown", "%v", err)
		return
	}
	rep.pass("teardown", "Cluster %s and ObjectStore %s deleted, with their pods and volume claims, in %s", o.DrillName, StoreName(o.DrillName), time.Since(started).Round(time.Second))
	d.logf("teardown done")
}

func (d *driller) deleteAll(ctx context.Context) error {
	o := d.plan.Options
	var errs []string
	// Only what a drill created: never an object without the label.
	if c, err := d.k.Get(ctx, ClusterGVR, o.DrillNamespace, o.DrillName); err == nil && managed(c) {
		if err := d.k.Delete(ctx, ClusterGVR, o.DrillNamespace, o.DrillName); err != nil {
			errs = append(errs, fmt.Sprintf("delete Cluster: %v", err))
		}
	}
	if s, err := d.k.Get(ctx, ObjectStoreGVR, o.DrillNamespace, StoreName(o.DrillName)); err == nil && managed(s) {
		if err := d.k.Delete(ctx, ObjectStoreGVR, o.DrillNamespace, StoreName(o.DrillName)); err != nil {
			errs = append(errs, fmt.Sprintf("delete ObjectStore: %v", err))
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	selector := labelCluster + "=" + o.DrillName
	for {
		var left []string
		if _, err := d.k.Get(ctx, ClusterGVR, o.DrillNamespace, o.DrillName); !errors.Is(err, ErrNotFound) {
			left = append(left, "Cluster")
		}
		if pods, err := d.k.ListPods(ctx, o.DrillNamespace, selector); err != nil || len(pods) > 0 {
			left = append(left, fmt.Sprintf("%d pods", len(pods)))
		}
		if pvcs, err := d.k.ListPVCs(ctx, o.DrillNamespace, selector); err != nil || len(pvcs) > 0 {
			left = append(left, fmt.Sprintf("%d volume claims", len(pvcs)))
		}
		if len(left) == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("still present after the teardown timeout: %s (delete them by hand: kubectl -n %s delete cluster.postgresql.cnpg.io/%s objectstore.barmancloud.cnpg.io/%s)",
				strings.Join(left, ", "), o.DrillNamespace, o.DrillName, StoreName(o.DrillName))
		case <-time.After(o.PollInterval):
		}
	}
}
