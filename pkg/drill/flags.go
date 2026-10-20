// Copyright 2026 Truvity B.V.. All rights reserved.
// SPDX-License-Identifier: MIT

package drill

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// CLI is what the cnpgctl drill flags parse to. The flags live here, not in
// the command, so that the chart's render test can prove the arguments its
// CronJobs pass are ones the command accepts.
type CLI struct {
	Options    Options
	Kubeconfig string
	Context    string
	Output     string
}

// listFlag is a repeatable string flag.
type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

// ParseArgs parses the cnpgctl drill flags and validates the result as Run
// would (without a cluster). Usage errors go to stderr.
func ParseArgs(args []string, stderr io.Writer) (*CLI, error) {
	fs := flag.NewFlagSet("drill", flag.ContinueOnError)
	fs.SetOutput(stderr)
	c := &CLI{}
	o := &c.Options
	var (
		cpu, memReq, memLim                          string
		nodeSel, tols, podAnn, sideEnv, dbs, minRows listFlag
	)
	fs.StringVar(&c.Kubeconfig, "kubeconfig", "", "kubeconfig path (default: in a pod its ServiceAccount, else $KUBECONFIG, then ~/.kube/config)")
	fs.StringVar(&c.Context, "context", "", "kubeconfig context (default: the current one)")
	fs.StringVar(&c.Output, "output", "text", "text or json")
	fs.StringVar(&o.Namespace, "namespace", "", "namespace of the SOURCE Cluster (required)")
	fs.StringVar(&o.Cluster, "cluster", "", "name of the SOURCE Cluster (required)")
	fs.StringVar(&o.DrillNamespace, "drill-namespace", "", "namespace of the drill Cluster (default: --namespace)")
	fs.StringVar(&o.DrillName, "drill-name", "", "name of the drill Cluster = the restore ServiceAccount (default <cluster>-restore)")
	fs.StringVar(&o.DestinationPath, "destination-path", "", "s3://<bucket>/<prefix> of the archive (default: the source's ObjectStore)")
	fs.StringVar(&o.ServerName, "server-name", "", "barman server name in the archive, generation suffix included (default: the source's plugin entry)")
	fs.StringVar(&o.EndpointURL, "endpoint-url", "", "S3 endpoint URL (default: the source's ObjectStore, else AWS)")
	fs.StringVar(&o.Image, "image", "", "PostgreSQL image (default: the source's status.image)")
	fs.StringVar(&o.StorageClass, "storage-class", "", "StorageClass of the drill volumes; must delete its volumes (default: the source's)")
	fs.BoolVar(&o.AllowRetainedVolumes, "allow-retained-volumes", false, "allow a StorageClass that retains its volumes (each drill then leaves its volumes behind)")
	fs.StringVar(&o.StorageSize, "storage-size", "", "data volume size (default: the source's, the larger of spec and actual)")
	fs.StringVar(&o.WALStorageSize, "wal-storage-size", "", "WAL volume size, when the source has one (default: the source's)")
	fs.StringVar(&cpu, "cpu-request", "", "CPU request of the drill instance (default 250m)")
	fs.StringVar(&memReq, "memory-request", "", "memory request of the drill instance (default 512Mi)")
	fs.StringVar(&memLim, "memory-limit", "", "memory limit of the drill instance (default 1Gi)")
	fs.Var(&nodeSel, "node-selector", "key=value for the drill pods, repeatable (default: the source's nodeSelector and tolerations)")
	fs.Var(&tols, "toleration", "key[=value]:Effect for the drill pods, repeatable (default: the source's)")
	fs.Var(&podAnn, "pod-annotation", "key=value on the drill pods and volumes, repeatable")
	fs.Var(&sideEnv, "sidecar-env", "NAME=value for the barman-cloud sidecar (e.g. AWS_REGION), repeatable; static credentials are refused")
	fs.BoolVar(&o.AllowStaticCredentials, "allow-static-credentials", false, "TEST ENDPOINTS ONLY: allow AWS credential variables in --sidecar-env (the drill then does not read with the restore identity)")
	fs.Var(&dbs, "database", "a database expected in the restore, repeatable (default: from the source's Database objects and bootstrap)")
	fs.Var(&minRows, "min-rows", "<db>=<n> (rows over all user tables) or <db>/<schema>.<table>=<n>, repeatable (default <db>=1)")
	fs.StringVar(&o.CountMode, "count", CountExact, "exact (count(*) of every user table: reads all the data) or estimate (reltuples)")
	fs.DurationVar(&o.MaxRecoveryPointAge, "max-recovery-point-age", DefaultMaxRecoveryPointAge, "how old the recovered data may be")
	fs.DurationVar(&o.StatementTimeout, "statement-timeout", DefaultStatementTimeout, "bound on each SQL statement")
	fs.DurationVar(&o.Timeout, "timeout", DefaultTimeout, "bound on the drill up to the end of the checks")
	fs.DurationVar(&o.TeardownTimeout, "teardown-timeout", DefaultTeardownTimeout, "bound on the teardown, after the drill")
	fs.IntVar(&o.MaxRecoveryAttempts, "max-recovery-attempts", DefaultMaxRecoveryAttempts, "failed recovery Job pods that end the drill early")
	fs.DurationVar(&o.PollInterval, "poll-interval", DefaultPollInterval, "how often to look at the drill Cluster")
	fs.StringVar(&o.PGHost, "pg-host", "", "connect the checks here (default <drill-name>-rw.<drill-namespace>.svc)")
	fs.IntVar(&o.PGPort, "pg-port", DefaultPGPort, "port for the checks")
	fs.BoolVar(&o.NoRecord, "no-record", false, "do not write the result ConfigMap")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	if o.Namespace == "" || o.Cluster == "" {
		return nil, errors.New("--namespace and --cluster are required")
	}
	if c.Output != "text" && c.Output != "json" {
		return nil, fmt.Errorf("--output must be text or json, got %q", c.Output)
	}
	if cpu != "" || memReq != "" || memLim != "" {
		r := DefaultResources()
		for _, q := range []struct {
			v    string
			list corev1.ResourceList
			name corev1.ResourceName
			flag string
		}{
			{cpu, r.Requests, corev1.ResourceCPU, "--cpu-request"},
			{memReq, r.Requests, corev1.ResourceMemory, "--memory-request"},
			{memLim, r.Limits, corev1.ResourceMemory, "--memory-limit"},
		} {
			if q.v == "" {
				continue
			}
			parsed, err := resource.ParseQuantity(q.v)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", q.flag, err)
			}
			q.list[q.name] = parsed
		}
		o.Resources = &r
	}
	for _, kv := range nodeSel {
		k, v, err := ParseKeyValue(kv)
		if err != nil {
			return nil, fmt.Errorf("--node-selector: %w", err)
		}
		if o.NodeSelector == nil {
			o.NodeSelector = map[string]string{}
		}
		o.NodeSelector[k] = v
	}
	for _, t := range tols {
		tol, err := ParseToleration(t)
		if err != nil {
			return nil, fmt.Errorf("--toleration: %w", err)
		}
		o.Tolerations = append(o.Tolerations, tol)
	}
	for _, kv := range podAnn {
		k, v, err := ParseKeyValue(kv)
		if err != nil {
			return nil, fmt.Errorf("--pod-annotation: %w", err)
		}
		if o.PodAnnotations == nil {
			o.PodAnnotations = map[string]string{}
		}
		o.PodAnnotations[k] = v
	}
	env := map[string]string{}
	for _, kv := range sideEnv {
		k, v, err := ParseKeyValue(kv)
		if err != nil {
			return nil, fmt.Errorf("--sidecar-env: %w", err)
		}
		env[k] = v
	}
	o.SidecarEnv = EnvFromPairs(env)
	o.Databases = dbs
	for _, m := range minRows {
		k, n, err := ParseMinRows(m)
		if err != nil {
			return nil, fmt.Errorf("--min-rows: %w", err)
		}
		if o.MinRows == nil {
			o.MinRows = map[string]int64{}
		}
		o.MinRows[k] = n
	}
	if _, err := o.withDefaults(); err != nil {
		return nil, err
	}
	return c, nil
}

// MetricsCLI is what the cnpgctl drill-metrics flags parse to.
type MetricsCLI struct {
	Kubeconfig string
	Context    string
	Namespace  string
	Listen     string
	Sources    []Source
}

// ParseMetricsArgs parses the cnpgctl drill-metrics flags.
func ParseMetricsArgs(args []string, stderr io.Writer) (*MetricsCLI, error) {
	fs := flag.NewFlagSet("drill-metrics", flag.ContinueOnError)
	fs.SetOutput(stderr)
	c := &MetricsCLI{}
	var sources listFlag
	fs.StringVar(&c.Kubeconfig, "kubeconfig", "", "kubeconfig path (default: in a pod its ServiceAccount, else $KUBECONFIG, then ~/.kube/config)")
	fs.StringVar(&c.Context, "context", "", "kubeconfig context (default: the current one)")
	fs.StringVar(&c.Namespace, "namespace", "", "the drill namespace, where the result ConfigMaps are (required)")
	fs.StringVar(&c.Listen, "listen", ":8080", "address to serve /metrics and /healthz on")
	fs.Var(&sources, "source", "<namespace>/<cluster>[=<drill name>] of a drilled cluster, repeatable (required)")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	if c.Namespace == "" || len(sources) == 0 {
		return nil, errors.New("--namespace and at least one --source are required")
	}
	for _, s := range sources {
		src, err := ParseSource(s)
		if err != nil {
			return nil, fmt.Errorf("--source: %w", err)
		}
		c.Sources = append(c.Sources, src)
	}
	return c, nil
}
