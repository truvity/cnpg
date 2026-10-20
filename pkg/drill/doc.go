// Copyright 2026 Truvity B.V.. All rights reserved.
// SPDX-License-Identifier: MIT

// Package drill runs a restore drill for a CloudNativePG cluster that archives
// through the barman-cloud plugin: it creates a throwaway Cluster that
// recovers the source's archive (base backup and full WAL replay, no recovery
// target), checks that the result is a promoted, readable database holding
// the expected data, records the outcome, and deletes everything it created.
//
// Unlike package verify, this package WRITES to the Kubernetes API: it
// creates and deletes one Cluster and one ObjectStore in the drill namespace
// and writes one result ConfigMap there. It never writes to the source
// Cluster, and the drill Cluster it builds has no WAL archiving and no backup
// configuration, which AssertNoArchive checks before anything is created.
//
// Identity: the drill Cluster is named like the read-only restore
// ServiceAccount (<cluster>-restore by default). CloudNativePG runs a Cluster's
// pods, the recovery Job included, as the ServiceAccount named after the
// Cluster, and the barman-cloud plugin injects its sidecar into the same pod
// and binds the RBAC that lets it read the ObjectStore to that same name. So
// the archive is read with the restore identity bound to that ServiceAccount
// and nothing else. See docs/restore-drill.md.
//
// The Kubernetes and SQL sides are reached through the Kube and Prober
// interfaces, so the logic is tested against fakes; KubeClient and PGProber are
// the real implementations. Metrics (WriteMetrics, MetricsHandler) export the
// recorded results for a Prometheus-style scraper.
package drill
