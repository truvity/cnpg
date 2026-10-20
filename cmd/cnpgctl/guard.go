package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/truvity/cnpg/v2/pkg/guard"
	"github.com/truvity/cnpg/v2/pkg/verify"
)

// guardSources builds the readers `guard empty-bootstrap` uses; tests
// replace it with fakes.
var guardSources = func(_ context.Context, kubeconfig, kubeContext string, s3opt guard.S3Options) (guard.ClusterSource, guard.ArchiveLister, error) {
	ks, err := verify.NewKubeSource(kubeconfig, kubeContext)
	if err != nil {
		return nil, nil, err
	}

	return ks, guard.NewS3Lister(s3opt), nil
}

const guardUsage = `Usage:
  cnpgctl guard empty-bootstrap [flags]   refuse an initdb bootstrap onto a non-empty archive

Run "cnpgctl guard empty-bootstrap -h" for the flags.
`

func runGuard(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] != "empty-bootstrap" {
		_, _ = fmt.Fprint(stderr, guardUsage)
		if len(args) == 0 {
			return errors.New("a guard is required")
		}

		return fmt.Errorf("unknown guard %q", args[0])
	}

	fs := flag.NewFlagSet("guard empty-bootstrap", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var (
		kubeconfig  = fs.String("kubeconfig", "", "kubeconfig path (default: $KUBECONFIG, then ~/.kube/config, then the in-cluster config)")
		kctx        = fs.String("context", "", "kubeconfig context (default: the current one)")
		namespace   = fs.String("namespace", "", "namespace of the Cluster (required)")
		cluster     = fs.String("cluster", "", "name of the Cluster (required)")
		bootstrap   = fs.String("bootstrap", guard.BootstrapInitdb, "the Cluster's bootstrap: initdb or recovery")
		destination = fs.String("destination", "", "the ObjectStore's destinationPath, s3://<bucket>[/<path>] (required)")
		serverName  = fs.String("server-name", "", "the barman server directory under the destination (default: the cluster name)")
		region      = fs.String("region", "", "the bucket's region (default: the SDK's, AWS_REGION; required if neither is set)")
		endpoint    = fs.String("endpoint", "", "an S3-compatible endpoint URL (default: AWS)")
		allow       = fs.Bool("allow-non-empty-archive", false, "pass whatever the archive holds (needs --reason)")
		reason      = fs.String("reason", "", "why --allow-non-empty-archive is set")
	)
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	if *namespace == "" || *cluster == "" || *destination == "" {
		return errors.New("--namespace, --cluster and --destination are required")
	}

	clusters, archive, err := guardSources(ctx, *kubeconfig, *kctx, guard.S3Options{Region: *region, Endpoint: *endpoint})
	if err != nil {
		return err
	}

	v, err := guard.EmptyBootstrap(ctx, guard.EmptyBootstrapOptions{
		Namespace: *namespace, Cluster: *cluster, Bootstrap: *bootstrap,
		Destination: *destination, ServerName: *serverName,
		AllowNonEmptyArchive: *allow, Reason: *reason,
	}, clusters, archive)
	if err != nil {
		return err
	}

	if !v.Pass {
		_, _ = fmt.Fprintln(stdout, "FAIL  empty-bootstrap: "+v.Message)

		return errFailed
	}

	_, _ = fmt.Fprintln(stdout, "PASS  empty-bootstrap: "+v.Message)

	return nil
}
