// Copyright 2026 Truvity B.V.. All rights reserved.
// SPDX-License-Identifier: MIT

// Package conformance_test stands the platform up on a disposable kind
// cluster and asserts what its charts promise when the real components act
// on them: cert-manager issues, approver-policy decides, trust-manager
// delivers, CloudNativePG serves. The render tests cannot reach any of it.
//
// It is gated. Without CNPG_CONFORMANCE every test skips, so `go test ./...`
// (and so `just check`) needs neither docker nor a cluster. With
// CNPG_CONFORMANCE=required -- which `just conformance` sets -- a missing
// tool is a failure, because a contract test that quietly skipped would
// prove nothing. See docs/conformance.md.
package conformance_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	requireVariable = "CNPG_CONFORMANCE"
	// keepVariable leaves the kind cluster (and its kubeconfig) in place
	// after the run, for debugging.
	keepVariable = "CNPG_CONFORMANCE_KEEP"
	// backupVariable opts into the barman-cloud backup and point-in-time
	// restore assertion (MinIO in the cluster). It pulls two more images and
	// starts a second database, so it is off by default.
	backupVariable = "CNPG_CONFORMANCE_BACKUP"

	clusterName = "cnpg-conformance"

	// Pinned: a kind upgrade would otherwise move Kubernetes under the suite.
	nodeImage = "kindest/node:v1.34.0@sha256:7416a61b42b1662ca6ca89f02028ac133a309a2a30ba309614e8ec94d976dc5a"

	jetstackRepo = "https://charts.jetstack.io"
	cnpgRepo     = "https://cloudnative-pg.github.io/charts"

	certManagerVersion    = "v1.21.2"
	approverPolicyVersion = "v0.28.0"
	trustManagerVersion   = "v0.25.0"
	// The versions examples/operator/ is documented against.
	operatorChartVersion = "0.29.0"
	pluginChartVersion   = "0.7.0"

	// The product namespace, the cluster and the roles the suite installs.
	appNS       = "app"
	otherNS     = "other"
	pgName      = "pg"
	roleRW      = "app_rw"
	roleRO      = "app_ro"
	database    = "conformance_db"
	platformNS  = "cnpg-system"
	certManager = "cert-manager"
)

// mode is what the environment asks of the suite.
type mode int

const (
	modeOff mode = iota
	modeOn
	modeRequired
)

func requested() mode {
	switch os.Getenv(requireVariable) {
	case "":
		return modeOff
	case "required":
		return modeRequired
	default:
		return modeOn
	}
}

// suite is the one kind cluster every test shares.
type suite struct {
	root       string // repository root
	dir        string // scratch directory, removed at the end
	kubeconfig string
	env        []string
	created    bool
	serverCA   *authority // signs the server certificate (Issuer conformance-server)
}

var (
	shared    suite
	setupOnce sync.Once
	setupErr  error
)

func TestMain(m *testing.M) {
	code := m.Run()
	shared.teardown()
	os.Exit(code)
}

// need returns the path of a tool, or skips (or, when required, fails).
func need(t *testing.T, name string) string {
	t.Helper()

	path, err := exec.LookPath(name)
	if err != nil {
		if requested() == modeRequired {
			t.Fatalf("%s=required and no `%s` on PATH: run the suite in the dev shell (devbox)", requireVariable, name)
		}

		t.Skipf("no `%s` on PATH; %s=required makes this a failure", name, requireVariable)
	}

	return path
}

// gate skips the test unless the suite is asked for, and returns the
// cluster once it is up. The first caller builds it; a failure to build is
// every caller's failure.
func gate(t *testing.T) *suite {
	t.Helper()

	if requested() == modeOff {
		t.Skipf("set %s=required (just conformance) to run the kind conformance suite", requireVariable)
	}

	for _, tool := range []string{"docker", "kind", "kubectl", "helm"} {
		need(t, tool)
	}

	setupOnce.Do(func() { setupErr = shared.up(t) })

	if setupErr != nil {
		t.Fatalf("the platform did not come up: %v", setupErr)
	}

	return &shared
}

// diagnostics is printed when a test fails: what a reader of a CI log needs
// first, without a cluster to poke at.
func (s *suite) diagnostics(t *testing.T) {
	t.Helper()

	if !t.Failed() {
		return
	}

	for _, args := range [][]string{
		{"get", "pods,certificates,certificaterequests,clusters.postgresql.cnpg.io,databaseroles.postgresql.cnpg.io", "-A", "-o", "wide"},
		{"get", "events", "-A", "--sort-by=.lastTimestamp"},
		{"describe", "clusters.postgresql.cnpg.io", "-n", appNS},
		{"logs", "-n", platformNS, "-l", "app.kubernetes.io/name=cloudnative-pg", "--tail=80"},
		{"logs", "-n", certManager, "-l", "app.kubernetes.io/name=approver-policy", "--tail=80"},
	} {
		out, _ := s.run(context.Background(), "", "kubectl", args...)
		t.Logf("--- kubectl %s\n%s", strings.Join(args, " "), out)
	}
}

// run executes a command against the suite's cluster and returns its
// combined output. KUBECONFIG is always the suite's own file: the suite never
// reads or writes the caller's.
func (s *suite) run(ctx context.Context, stdin string, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = s.root
	cmd.Env = s.env

	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}

	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	err := cmd.Run()
	if err != nil {
		err = fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, out.String())
	}

	return out.String(), err
}

// must runs a command and fails the test on error.
func (s *suite) must(t *testing.T, timeout time.Duration, name string, args ...string) string {
	t.Helper()

	return s.mustIn(t, timeout, "", name, args...)
}

func (s *suite) mustIn(t *testing.T, timeout time.Duration, stdin, name string, args ...string) string {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	out, err := s.run(ctx, stdin, name, args...)
	if err != nil {
		t.Fatal(err)
	}

	return out
}

func (s *suite) kubectl(t *testing.T, args ...string) string {
	t.Helper()

	return s.must(t, 5*time.Minute, "kubectl", args...)
}

// apply feeds a manifest to kubectl and returns the outcome without failing,
// for the assertions that expect a refusal.
func (s *suite) apply(manifest string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	return s.run(ctx, manifest, "kubectl", "apply", "-f", "-")
}

// eventually polls fn until it returns nil or the time is up.
func eventually(t *testing.T, timeout, interval time.Duration, what string, fn func() error) {
	t.Helper()

	deadline := time.Now().Add(timeout)

	var err error

	for {
		if err = fn(); err == nil {
			return
		}

		if time.Now().After(deadline) {
			t.Fatalf("%s: not true after %s: %v", what, timeout, err)
		}

		time.Sleep(interval)
	}
}

// psql runs a query in a pod's psql container as the pod's own libpq
// environment describes (the cnpg-client helpers set it), with optional
// overrides.
func (s *suite) psql(pod, query string, overrides ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	args := []string{"exec", "-n", appNS, pod, "-c", "psql", "--", "env"}
	args = append(args, overrides...)
	args = append(args, "psql", "-X", "-A", "-t", "-v", "ON_ERROR_STOP=1", "-c", query)

	out, err := s.run(ctx, "", "kubectl", args...)

	return strings.TrimSpace(out), err
}

// superuserSQL runs a query inside an instance pod over the local socket as
// the postgres user (peer authentication): the suite's window onto the
// server's own view, with no password and no network.
func (s *suite) superuserSQL(t *testing.T, pod, query string) string {
	t.Helper()

	out := s.kubectl(t, "exec", "-n", appNS, pod, "-c", "postgres", "--",
		"psql", "-X", "-A", "-t", "-U", "postgres", "-d", database, "-v", "ON_ERROR_STOP=1", "-c", query)

	return strings.TrimSpace(out)
}

// primaryName names the current primary instance of a cluster.
func (s *suite) primaryName(ns, cluster string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	out, err := s.run(ctx, "", "kubectl", "get", "pods", "-n", ns,
		"-l", "cnpg.io/cluster="+cluster+",cnpg.io/instanceRole=primary",
		"-o", "jsonpath={.items[*].metadata.name}")
	if err != nil {
		return "", err
	}

	name := strings.TrimSpace(out)
	if name == "" || strings.Contains(name, " ") {
		return "", fmt.Errorf("want exactly one primary of %s/%s, got %q", ns, cluster, name)
	}

	return name, nil
}

func (s *suite) primaryPod(t *testing.T, ns, cluster string) string {
	t.Helper()

	name, err := s.primaryName(ns, cluster)
	if err != nil {
		t.Fatal(err)
	}

	return name
}

func (s *suite) path(parts ...string) string {
	return filepath.Join(append([]string{s.root}, parts...)...)
}
