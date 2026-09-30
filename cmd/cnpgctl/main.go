// Command cnpgctl operates on CloudNativePG clusters built from these
// charts. Today it has one command, read-only:
//
//	cnpgctl verify --namespace <ns> --cluster <name> [--context <ctx>]
//
// See docs/cnpgctl.md.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"

	"github.com/truvity/cnpg/v2/pkg/verify"
)

// Version is stamped by the release.
var Version = "dev"

// errFailed marks a run whose assertions failed; the report already said why.
var errFailed = errors.New("assertions failed")

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	err := run(ctx, os.Args[1:], os.Stdout, os.Stderr, nil)
	switch {
	case err == nil:
	case errors.Is(err, errFailed):
		os.Exit(1)
	case errors.Is(err, flag.ErrHelp):
	default:
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}
}

const usage = `cnpgctl %s

Usage:
  cnpgctl verify [flags]   read-only assertions against a live cluster
  cnpgctl version

Run "cnpgctl verify -h" for the flags.
`

// run dispatches the command. src overrides the Kubernetes client (tests).
func run(ctx context.Context, args []string, stdout, stderr io.Writer, src verify.Source) error {
	if len(args) == 0 {
		_, _ = fmt.Fprintf(stderr, usage, Version)
		return errors.New("a command is required")
	}
	switch args[0] {
	case "verify":
		return runVerify(ctx, args[1:], stdout, stderr, src)
	case "version", "--version":
		_, _ = fmt.Fprintln(stdout, Version)
		return nil
	case "-h", "--help", "help":
		_, _ = fmt.Fprintf(stdout, usage, Version)
		return nil
	}
	_, _ = fmt.Fprintf(stderr, usage, Version)
	return fmt.Errorf("unknown command %q", args[0])
}

func runVerify(ctx context.Context, args []string, stdout, stderr io.Writer, src verify.Source) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		kubeconfig = fs.String("kubeconfig", "", "kubeconfig path (default: $KUBECONFIG, then ~/.kube/config)")
		kctx       = fs.String("context", "", "kubeconfig context (default: the current one)")
		namespace  = fs.String("namespace", "", "namespace of the Cluster (required)")
		cluster    = fs.String("cluster", "", "name of the Cluster (required)")
		backupAge  = fs.Duration("max-backup-age", verify.DefaultMaxBackupAge, "how old the last successful backup may be")
		certValid  = fs.Duration("min-cert-validity", verify.DefaultMinCertValidity, "how long each certificate must remain valid")
		output     = fs.String("output", "text", "text or json")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *namespace == "" || *cluster == "" {
		return errors.New("--namespace and --cluster are required")
	}
	if *output != "text" && *output != "json" {
		return fmt.Errorf("--output must be text or json, got %q", *output)
	}
	if src == nil {
		ks, err := verify.NewKubeSource(*kubeconfig, *kctx)
		if err != nil {
			return err
		}
		src = ks
	}
	rep, err := verify.Run(ctx, src, verify.Options{
		Namespace: *namespace, Cluster: *cluster,
		MaxBackupAge: *backupAge, MinCertValidity: *certValid,
		Now: time.Now(),
	})
	if err != nil {
		return err
	}
	if *output == "json" {
		if err := verify.WriteJSON(stdout, rep); err != nil {
			return err
		}
	} else {
		verify.WriteText(stdout, rep)
	}
	if rep.Failed() {
		return errFailed
	}
	return nil
}
