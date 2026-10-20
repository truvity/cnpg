// Copyright 2026 Truvity B.V.. All rights reserved.
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/truvity/cnpg/v2/pkg/drill"
)

func runDrill(ctx context.Context, args []string, stdout, stderr io.Writer, d drill.Deps) error {
	cli, err := drill.ParseArgs(args, stderr)
	if err != nil {
		return err
	}
	if d.Kube == nil {
		kc, err := drill.NewKubeClient(cli.Kubeconfig, cli.Context)
		if err != nil {
			return err
		}
		d.Kube = kc
	}
	// Progress goes to stderr, the report to stdout (JSON stays parseable).
	d.Out = stderr
	rep, err := drill.Run(ctx, d, cli.Options)
	if err != nil {
		return err
	}
	if cli.Output == "json" {
		if err := drill.WriteJSON(stdout, rep); err != nil {
			return err
		}
	} else {
		drill.WriteText(stdout, rep)
	}
	if rep.Failed() {
		return errFailed
	}
	return nil
}

func runDrillMetrics(ctx context.Context, args []string, stderr io.Writer, k drill.Kube) error {
	cli, err := drill.ParseMetricsArgs(args, stderr)
	if err != nil {
		return err
	}
	if k == nil {
		kc, err := drill.NewKubeClient(cli.Kubeconfig, cli.Context)
		if err != nil {
			return err
		}
		k = kc
	}
	mux := http.NewServeMux()
	mux.Handle("/metrics", drill.MetricsHandler(k, cli.Namespace, cli.Sources))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok\n") })
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	ln, err := net.Listen("tcp", cli.Listen)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stderr, "serving %d drill results from namespace %s on %s\n", len(cli.Sources), cli.Namespace, ln.Addr())
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		return srv.Shutdown(sctx)
	case err := <-errc:
		return err
	}
}
