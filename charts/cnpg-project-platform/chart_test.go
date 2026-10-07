// Copyright 2026 Truvity B.V.. All rights reserved.
// SPDX-License-Identifier: MIT

// Package chart_test holds cnpg-project-platform to what the goldens cannot
// say in words: every object is rendered from the entry that names it and
// from nothing else, an entry the caller switched off renders nothing, and
// nothing is added that was not asked for (no standard labels, no default
// that names an estate). Objects are compared PARSED, so a reordering of
// keys is not a change.
package chart_test

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

const root = `-----BEGIN CERTIFICATE-----
MIIBexample
-----END CERTIFICATE-----`

// render runs helm template with the values file content and returns the
// objects by kind, then name.
func render(t *testing.T, values string) map[string]map[string]map[string]any {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), "helm", "template", "x", ".", "-f", "-")
	cmd.Stdin = strings.NewReader(values)

	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "helm template failed:\n%s", out)

	objs := map[string]map[string]map[string]any{}

	for _, d := range strings.Split(string(out), "\n---") {
		var m map[string]any
		if yaml.Unmarshal([]byte(d), &m) != nil || len(m) == 0 {
			continue
		}

		kind := m["kind"].(string)
		name := m["metadata"].(map[string]any)["name"].(string)

		if objs[kind] == nil {
			objs[kind] = map[string]map[string]any{}
		}

		objs[kind][name] = m
	}

	return objs
}

func parse(t *testing.T, doc string) map[string]any {
	t.Helper()

	var m map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(doc), &m))

	return m
}

func TestNothingAskedForRendersNothing(t *testing.T) {
	assert.Empty(t, render(t, "{}"))
}

// TestObjectStoreIsExactlyWhatTheEntryNames compares the whole object, not
// probes: a key that appeared (an empty endpoint, a second credentials
// source) would be behaviour the barman-cloud plugin acts on.
func TestObjectStoreIsExactlyWhatTheEntryNames(t *testing.T) {
	objs := render(t, `
objectStores:
  - name: app-objectstore
    namespace: app
    bucketName: example-bucket
    prefix: app/app-pg
    labels: {example.com/project: app}
    annotations: {example.com/wave: "20"}
`)
	require.Len(t, objs["ObjectStore"], 1)

	want := parse(t, `
apiVersion: barmancloud.cnpg.io/v1
kind: ObjectStore
metadata:
  name: app-objectstore
  namespace: app
  labels: {example.com/project: app}
  annotations: {example.com/wave: "20"}
spec:
  configuration:
    destinationPath: s3://example-bucket/app/app-pg
    s3Credentials: {inheritFromIAMRole: true}
    data: {compression: snappy, encryption: AES256, jobs: 2}
    wal: {compression: zstd, encryption: AES256, maxParallel: 4}
  retentionPolicy: 30d
`)
	assert.Equal(t, want, objs["ObjectStore"]["app-objectstore"])
}

func TestObjectStoreEmptyEncryptionSendsNoHeader(t *testing.T) {
	objs := render(t, `
objectStores:
  - {name: s, namespace: ns, bucketName: example-bucket, prefix: p, encryption: "", dataCompression: "", retentionDays: 7}
`)
	cfg := objs["ObjectStore"]["s"]["spec"].(map[string]any)["configuration"].(map[string]any)
	assert.Equal(t, map[string]any{"jobs": float64(2)}, cfg["data"])
	assert.Equal(t, map[string]any{"compression": "zstd", "maxParallel": float64(4)}, cfg["wal"])
	assert.Equal(t, "7d", objs["ObjectStore"]["s"]["spec"].(map[string]any)["retentionPolicy"])
}

func TestScheduledBackupIsExactlyWhatTheEntryNames(t *testing.T) {
	objs := render(t, `
scheduledBackups:
  - name: app-pg-scheduled-backup
    namespace: app
    clusterName: app-pg
    objectStoreName: app-objectstore
    annotations: {example.com/wave: "25"}
`)

	want := parse(t, `
apiVersion: postgresql.cnpg.io/v1
kind: ScheduledBackup
metadata:
  name: app-pg-scheduled-backup
  namespace: app
  annotations: {example.com/wave: "25"}
spec:
  schedule: "0 0 2 * * *"
  immediate: true
  backupOwnerReference: self
  method: plugin
  cluster: {name: app-pg}
  pluginConfiguration:
    name: barman-cloud.cloudnative-pg.io
    parameters: {barmanObjectName: app-objectstore}
`)
	assert.Equal(t, want, objs["ScheduledBackup"]["app-pg-scheduled-backup"])
}

// TestServerTLSNamesEveryServiceAndReloads: the Certificate covers the
// three Services the operator creates and both objects carry the reload
// marker the operator needs to serve a renewal.
func TestServerTLSNamesEveryServiceAndReloads(t *testing.T) {
	objs := render(t, `
serverTLS:
  - clusterName: app-pg
    namespace: app
    issuerRef: {name: example-issuer, kind: ClusterIssuer, group: cert-manager.io}
    caCertificates:
      - "`+strings.ReplaceAll(root, "\n", `\n`)+`"
    duration: 720h
    renewBefore: 240h
    privateKey: {algorithm: ECDSA, size: 384}
`)
	cert := objs["Certificate"]["app-pg-server-tls"]
	require.NotNil(t, cert)

	wantCert := parse(t, `
apiVersion: cert-manager.io/v1
kind: Certificate
metadata: {name: app-pg-server-tls, namespace: app}
spec:
  secretName: app-pg-server-tls
  dnsNames:
    - app-pg-rw.app.svc.cluster.local
    - app-pg-ro.app.svc.cluster.local
    - app-pg-r.app.svc.cluster.local
  duration: 720h
  renewBefore: 240h
  privateKey: {algorithm: ECDSA, size: 384}
  issuerRef: {name: example-issuer, kind: ClusterIssuer, group: cert-manager.io}
  secretTemplate:
    labels: {cnpg.io/reload: "true"}
`)
	assert.Equal(t, wantCert, cert)

	ca := objs["Secret"]["app-pg-server-ca"]
	require.NotNil(t, ca)

	assert.Equal(t, map[string]any{"cnpg.io/reload": "true"}, ca["metadata"].(map[string]any)["labels"])
	assert.Equal(t, "Opaque", ca["type"])
	assert.Equal(t, root+"\n", ca["stringData"].(map[string]any)["ca.crt"])
}

// TestServerTLSKeepsAnAdoptedObjectsNames: the names a project's own chart
// gave the live objects can be kept, so the platform takes them over rather
// than minting a second pair beside them.
func TestServerTLSKeepsAnAdoptedObjectsNames(t *testing.T) {
	objs := render(t, `
serverTLS:
  - clusterName: app-pg
    namespace: app
    certificateName: app-pg-server
    secretName: app-pg-tls
    caSecretName: app-pg-ca
    issuerRef: {name: example-issuer}
    caCertificates: ["-----BEGIN CERTIFICATE-----\nx\n-----END CERTIFICATE-----"]
`)
	require.Contains(t, objs["Certificate"], "app-pg-server")
	assert.Equal(t, "app-pg-tls", objs["Certificate"]["app-pg-server"]["spec"].(map[string]any)["secretName"])
	require.Contains(t, objs["Secret"], "app-pg-ca")
}

func TestSeveralRootsAreOneBundleInOrder(t *testing.T) {
	objs := render(t, `
serverTLS:
  - clusterName: app-pg
    namespace: app
    issuerRef: {name: example-issuer}
    caCertificates:
      - "-----BEGIN CERTIFICATE-----\nfirst\n-----END CERTIFICATE-----\n"
      - "  -----BEGIN CERTIFICATE-----\nsecond\n-----END CERTIFICATE-----"
`)
	assert.Equal(t,
		"-----BEGIN CERTIFICATE-----\nfirst\n-----END CERTIFICATE-----\n-----BEGIN CERTIFICATE-----\nsecond\n-----END CERTIFICATE-----\n",
		objs["Secret"]["app-pg-server-ca"]["stringData"].(map[string]any)["ca.crt"])
}

// TestNoStandardLabelsAreAdded: an object a platform already runs under
// another renderer keeps its metadata exactly; the chart adds only what the
// caller names, entry labels winning over commonLabels.
func TestNoStandardLabelsAreAdded(t *testing.T) {
	objs := render(t, `
commonLabels: {example.com/a: common, example.com/b: common}
commonAnnotations: {example.com/note: common}
objectStores:
  - {name: s, namespace: ns, bucketName: example-bucket, prefix: p, labels: {example.com/b: entry}}
scheduledBackups:
  - {name: b, namespace: ns, clusterName: c, objectStoreName: s}
`)
	assert.Equal(t, map[string]any{"example.com/a": "common", "example.com/b": "entry"},
		objs["ObjectStore"]["s"]["metadata"].(map[string]any)["labels"])
	assert.Equal(t, map[string]any{"example.com/note": "common"},
		objs["ObjectStore"]["s"]["metadata"].(map[string]any)["annotations"])

	plain := render(t, `
objectStores:
  - {name: s, namespace: ns, bucketName: example-bucket, prefix: p}
`)
	meta := plain["ObjectStore"]["s"]["metadata"].(map[string]any)
	assert.NotContains(t, meta, "labels")
	assert.NotContains(t, meta, "annotations")
}

// TestAnEntrySwitchedOffRendersNothingAndIsNotCounted: a caller that
// decides per project passes the decision as `enabled`.
func TestAnEntrySwitchedOffRendersNothingAndIsNotCounted(t *testing.T) {
	objs := render(t, `
objectStores:
  - {name: s, namespace: ns, bucketName: example-bucket, prefix: p, enabled: false}
  - {name: s, namespace: ns, bucketName: example-bucket, prefix: q}
scheduledBackups:
  - {name: b, namespace: ns, clusterName: c, objectStoreName: s, enabled: false}
serverTLS:
  - clusterName: c
    namespace: ns
    issuerRef: {name: i}
    caCertificates: ["-----BEGIN CERTIFICATE-----\nx\n-----END CERTIFICATE-----"]
    enabled: false
`)
	require.Len(t, objs, 1)
	require.Len(t, objs["ObjectStore"], 1)
	assert.Equal(t, "s3://example-bucket/q", objs["ObjectStore"]["s"]["spec"].(map[string]any)["configuration"].(map[string]any)["destinationPath"])
}

// TestCASecretAnnotationsAreForTheSecretOnly: caSecretAnnotations lands on the
// CA Secret alone (winning over the entry's annotations there); the
// Certificate keeps only the entry's annotations. Absent, both carry the
// entry's annotations as before.
func TestCASecretAnnotationsAreForTheSecretOnly(t *testing.T) {
	entry := func(extra string) map[string]map[string]map[string]any {
		return render(t, `
serverTLS:
  - clusterName: app-pg
    namespace: app
    issuerRef: {name: example-issuer, kind: ClusterIssuer, group: cert-manager.io}
    caCertificates:
      - "`+strings.ReplaceAll(root, "\n", `\n`)+`"
    annotations: {example.com/wave: "15", example.com/guard: entry}
`+extra)
	}

	objs := entry(`    caSecretAnnotations: {example.com/guard: secret}
`)
	assert.Equal(t, map[string]any{"example.com/wave": "15", "example.com/guard": "entry"},
		objs["Certificate"]["app-pg-server-tls"]["metadata"].(map[string]any)["annotations"])
	assert.Equal(t, map[string]any{"example.com/wave": "15", "example.com/guard": "secret"},
		objs["Secret"]["app-pg-server-ca"]["metadata"].(map[string]any)["annotations"])

	objs = entry("")
	assert.Equal(t, objs["Certificate"]["app-pg-server-tls"]["metadata"].(map[string]any)["annotations"],
		objs["Secret"]["app-pg-server-ca"]["metadata"].(map[string]any)["annotations"])
}

// TestScheduledBackupImmediate: the first base backup is taken on creation
// by default; the operator ignores the field once the ScheduledBackup has
// run. `immediate: false` on an entry leaves it out.
func TestScheduledBackupImmediate(t *testing.T) {
	objs := render(t, `
scheduledBackups:
  - {name: a, namespace: app, clusterName: app-pg, objectStoreName: o}
  - {name: b, namespace: app, clusterName: app-pg, objectStoreName: o, immediate: false}
`)
	spec := func(n string) map[string]any { return objs["ScheduledBackup"][n]["spec"].(map[string]any) }
	assert.Equal(t, true, spec("a")["immediate"])
	assert.NotContains(t, spec("b"), "immediate")
}
