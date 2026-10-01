// Copyright 2026 Truvity B.V.. All rights reserved.
// SPDX-License-Identifier: MIT

package chart_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func replicationValues(replication map[string]any) map[string]any {
	return map[string]any{
		"clusterName": "app-db",
		"namespace":   "app",
		"profile":     "devel",
		"replication": replication,
	}
}

// replicationCert returns the Certificate issued for streaming_replica.
func replicationCert(t *testing.T, docs map[string][]map[string]any) map[string]any {
	t.Helper()

	for _, c := range docs["Certificate"] {
		if field(c, "spec", "commonName") == "streaming_replica" {
			return c
		}
	}

	require.Fail(t, "no replication Certificate rendered")

	return nil
}

// TestReplication_DefaultSecretNameAvoidsOperatorNames: the chart-issued
// replication Secret is <cluster>-replication-tls, never one of the names
// the CNPG operator generates (<cluster>-replication, -ca, -server), and
// the Cluster is wired to it. <cluster>-client-ca is the chart's own name.
func TestReplication_DefaultSecretNameAvoidsOperatorNames(t *testing.T) {
	docs, err := renderDocs(t, replicationValues(map[string]any{"enabled": true}))
	require.NoError(t, err)

	cert := replicationCert(t, docs)
	assert.Equal(t, "app-db-replication-tls", field(cert, "spec", "secretName"))
	assert.Equal(t, "app-db-replication-tls", field(cert, "metadata", "name"))

	require.Len(t, docs["Cluster"], 1)
	certs := field(docs["Cluster"][0], "spec", "certificates")
	assert.Equal(t, map[string]any{
		"clientCASecret":       "app-db-client-ca",
		"replicationTLSSecret": "app-db-replication-tls",
	}, certs)

	operatorNames := []string{"app-db-replication", "app-db-ca", "app-db-server"}

	m, _ := certs.(map[string]any)
	for _, v := range m {
		assert.NotContains(t, operatorNames, v, "a chart Secret name equals an operator default")
	}

	for _, c := range docs["Certificate"] {
		assert.NotContains(t, operatorNames, field(c, "spec", "secretName"))
	}
}

// TestReplication_SecretNameOverride: replication.secretName pins the
// name on both the Certificate and the Cluster, e.g. the old
// <cluster>-replication of an already adopted cluster.
func TestReplication_SecretNameOverride(t *testing.T) {
	docs, err := renderDocs(t, replicationValues(map[string]any{"enabled": true, "secretName": "app-db-replication"}))
	require.NoError(t, err)

	assert.Equal(t, "app-db-replication", field(replicationCert(t, docs), "spec", "secretName"))
	assert.Equal(t, "app-db-replication",
		field(docs["Cluster"][0], "spec", "certificates", "replicationTLSSecret"))
}

// TestReplication_SecretNameShape: a malformed name is refused by the schema.
func TestReplication_SecretNameShape(t *testing.T) {
	_, err := renderDocs(t, replicationValues(map[string]any{"enabled": true, "secretName": "Not A Name"}))
	require.Error(t, err)
}
