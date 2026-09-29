// Copyright 2026 Truvity B.V.. All rights reserved.
// SPDX-License-Identifier: MIT

package chart_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// storeValues renders a cluster that archives to `backup` and, when
// source is non-nil, bootstraps by recovery from `source`.
func storeValues(backup, source map[string]any) map[string]any {
	backup["enabled"] = true
	backup["bucketName"] = "example-backups"

	values := map[string]any{
		"clusterName": "app-pg",
		"namespace":   "app",
		"profile":     "prod",
		"backup":      backup,
	}

	if source != nil {
		source["bucketName"] = "example-archive"
		source["s3Prefix"] = "app/app-pg-g1/"
		values["bootstrap"] = map[string]any{
			"mode":     "recovery",
			"recovery": map[string]any{"source": source},
		}
	}

	return values
}

func objectStores(t *testing.T, docs map[string][]map[string]any) (backup, source map[string]any) {
	t.Helper()

	for _, os := range docs["ObjectStore"] {
		switch field(os, "metadata", "name") {
		case "app-pg-objectstore":
			backup = os
		case "app-pg-source-objectstore":
			source = os
		}
	}

	require.NotNil(t, backup, "no backup ObjectStore rendered")

	return backup, source
}

// TestObjectStore_DefaultIsAWS: with none of the store values set the
// two ObjectStores are exactly what the chart always rendered — pod
// identity, no endpoint, AES256 on data and WAL — so an AWS caller who
// upgrades sees no diff. Whole-spec equality, not field probes: a key
// that appears (an empty endpointURL, an inheritFromIAMRole: false)
// would be a behaviour change the plugin acts on.
func TestObjectStore_DefaultIsAWS(t *testing.T) {
	docs, err := renderDocs(t, storeValues(map[string]any{}, map[string]any{}))
	require.NoError(t, err)

	backup, source := objectStores(t, docs)
	assert.Equal(t, map[string]any{
		"configuration": map[string]any{
			"destinationPath": "s3://example-backups/app/app-pg",
			"s3Credentials":   map[string]any{"inheritFromIAMRole": true},
			"data": map[string]any{
				"compression": "snappy",
				"encryption":  "AES256",
				"jobs":        float64(2),
			},
			"wal": map[string]any{
				"compression": "zstd",
				"encryption":  "AES256",
				"maxParallel": float64(4),
			},
		},
		"retentionPolicy": "30d",
	}, backup["spec"])

	require.NotNil(t, source, "recovery mode must render the source ObjectStore")
	assert.Equal(t, map[string]any{
		"configuration": map[string]any{
			"destinationPath": "s3://example-archive/app/app-pg-g1/",
			"s3Credentials":   map[string]any{"inheritFromIAMRole": true},
		},
	}, source["spec"])
}

// TestObjectStore_S3Compatible: an endpoint, a CA, static keys and no
// encryption — the shape of R2, MinIO or Ceph RGW — reach the
// ObjectStore as endpointURL, endpointCA, secret selectors and an
// OMITTED encryption field (the CRD's enum has no empty member; the
// header is withheld by leaving the field out). Both stores, since a
// recovery may read from a different store than it archives to.
func TestObjectStore_S3Compatible(t *testing.T) {
	// bootstrap.recovery.source has no encryption field (the archive
	// being recovered from was already written with whatever encryption
	// it was written with; this chart does not re-request one) — the
	// two shapes share endpoint/endpointCA/existingSecret only.
	store := func(secret string) map[string]any {
		return map[string]any{
			"endpoint":       "https://s3.example.test",
			"endpointCA":     map[string]any{"name": "store-ca", "key": "ca.crt"},
			"existingSecret": secret,
		}
	}
	backupStore := func(secret string) map[string]any {
		m := store(secret)
		m["encryption"] = ""
		return m
	}

	docs, err := renderDocs(t, storeValues(backupStore("backup-keys"), store("archive-keys")))
	require.NoError(t, err)

	backup, source := objectStores(t, docs)
	require.NotNil(t, source)

	for name, os := range map[string]map[string]any{"backup": backup, "source": source} {
		secret := map[string]string{"backup": "backup-keys", "source": "archive-keys"}[name]
		conf, _ := field(os, "spec", "configuration").(map[string]any)

		assert.Equal(t, "https://s3.example.test", conf["endpointURL"], name)
		assert.Equal(t, map[string]any{"name": "store-ca", "key": "ca.crt"}, conf["endpointCA"], name)
		assert.Equal(t, map[string]any{
			"accessKeyId":     map[string]any{"name": secret, "key": "AWS_ACCESS_KEY_ID"},
			"secretAccessKey": map[string]any{"name": secret, "key": "AWS_SECRET_ACCESS_KEY"},
		}, conf["s3Credentials"], "%s: static keys replace inheritFromIAMRole; no session token or region unless named", name)
	}

	data, _ := field(backup, "spec", "configuration", "data").(map[string]any)
	wal, _ := field(backup, "spec", "configuration", "wal").(map[string]any)
	assert.NotContains(t, data, "encryption", "empty encryption must omit the field, not send an empty string")
	assert.NotContains(t, wal, "encryption")
	assert.Equal(t, "snappy", data["compression"], "the rest of the block is untouched")
}

// TestObjectStore_SessionTokenAndRegionOnlyWhenNamed: naming the extra
// keys adds their selectors against the same Secret; renaming the
// mandatory pair is honoured too.
func TestObjectStore_SessionTokenAndRegionOnlyWhenNamed(t *testing.T) {
	docs, err := renderDocs(t, storeValues(map[string]any{
		"existingSecret": "sts-keys",
		"existingSecretKeys": map[string]any{
			"accessKeyId":     "id",
			"secretAccessKey": "secret",
			"sessionToken":    "AWS_SESSION_TOKEN",
			"region":          "AWS_DEFAULT_REGION",
		},
	}, nil))
	require.NoError(t, err)

	backup, source := objectStores(t, docs)
	assert.Nil(t, source, "initdb mode renders no source store")
	assert.Equal(t, map[string]any{
		"accessKeyId":     map[string]any{"name": "sts-keys", "key": "id"},
		"secretAccessKey": map[string]any{"name": "sts-keys", "key": "secret"},
		"sessionToken":    map[string]any{"name": "sts-keys", "key": "AWS_SESSION_TOKEN"},
		"region":          map[string]any{"name": "sts-keys", "key": "AWS_DEFAULT_REGION"},
	}, field(backup, "spec", "configuration", "s3Credentials"))
	assert.Nil(t, field(backup, "spec", "configuration", "endpointURL"), "keys without an endpoint keep the AWS endpoint")
}

// TestObjectStore_EndpointCARequiresBothHalves: a half-filled selector
// would render a CRD-invalid object that only fails at apply time, so
// the render refuses it.
func TestObjectStore_EndpointCARequiresBothHalves(t *testing.T) {
	_, err := renderDocs(t, storeValues(map[string]any{
		"endpoint":   "https://s3.example.test",
		"endpointCA": map[string]any{"name": "store-ca"},
	}, nil))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "backup.endpointCA.key is required")
}
