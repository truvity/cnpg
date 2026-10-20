// Copyright 2026 Truvity B.V.. All rights reserved.
// SPDX-License-Identifier: MIT

package drill

import (
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

// Labels and annotations the drill puts on what it creates.
const (
	// LabelManagedBy marks every object the drill creates; the drill deletes
	// or replaces only objects that carry it.
	LabelManagedBy = "app.kubernetes.io/managed-by"
	ManagedBy      = "cnpgctl-drill"
	// LabelSourceNamespace and LabelSourceCluster name the drilled cluster.
	LabelSourceNamespace = "cnpg-restore-drill/source-namespace"
	LabelSourceCluster   = "cnpg-restore-drill/source-cluster"
	// LabelDrill is on every pod and volume of a drill Cluster.
	LabelDrill = "cnpg-restore-drill/drill"
	// LabelResult marks a result ConfigMap.
	LabelResult = "cnpg-restore-drill/result"

	// externalClusterName is the drill's name for the archive it recovers.
	externalClusterName = "origin"
)

// StoreName is the drill ObjectStore's name.
func StoreName(drillName string) string { return drillName + "-source" }

func mustQuantity(s string) resource.Quantity { return resource.MustParse(s) }

func runtimeFromUnstructured(m map[string]any, out any) error {
	return runtime.DefaultUnstructuredConverter.FromUnstructured(m, out)
}

func toUnstructured(in any) (map[string]any, error) {
	return runtime.DefaultUnstructuredConverter.ToUnstructured(in)
}

func (p *Plan) labels() map[string]any {
	return map[string]any{
		LabelManagedBy:       ManagedBy,
		LabelSourceNamespace: p.Options.Namespace,
		LabelSourceCluster:   p.Options.Cluster,
	}
}

// ObjectStore builds the drill's ObjectStore: the source's archive location,
// read with the pod's ambient identity, with no retention policy (the plugin
// would apply one by deleting backups) and no backup or WAL upload settings.
func (p *Plan) ObjectStore() (*unstructured.Unstructured, error) {
	o := p.Options
	conf := map[string]any{
		"destinationPath": p.DestinationPath,
		"s3Credentials":   map[string]any{"inheritFromIAMRole": true},
	}
	if p.EndpointURL != "" {
		conf["endpointURL"] = p.EndpointURL
	}
	if p.EndpointCA != nil {
		conf["endpointCA"] = p.EndpointCA
	}
	spec := map[string]any{"configuration": conf}
	if len(o.SidecarEnv) > 0 {
		var env []any
		for _, e := range o.SidecarEnv {
			m, err := toUnstructured(&e)
			if err != nil {
				return nil, err
			}
			env = append(env, m)
		}
		spec["instanceSidecarConfiguration"] = map[string]any{"env": env}
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "barmancloud.cnpg.io/v1",
		"kind":       "ObjectStore",
		"metadata": map[string]any{
			"name":      StoreName(o.DrillName),
			"namespace": o.DrillNamespace,
			"labels":    p.labels(),
		},
		"spec": spec,
	}}, nil
}

// Cluster builds the drill Cluster: one instance recovered from the archive
// to its end (no recovery target), promoted, with no WAL archiving, no
// backups, no PodDisruptionBudget, and superuser access for the checks. It
// runs as the ServiceAccount named like itself: no serviceAccountName is set,
// because the barman-cloud plugin binds its ObjectStore RBAC to the
// ServiceAccount named after the Cluster, whatever the spec says.
func (p *Plan) Cluster(started time.Time) (*unstructured.Unstructured, error) {
	o := p.Options
	resources, err := toUnstructured(&p.Resources)
	if err != nil {
		return nil, err
	}
	storage := map[string]any{"size": p.StorageSize}
	if p.StorageClass != "" {
		storage["storageClass"] = p.StorageClass
	}
	spec := map[string]any{
		"instances":             int64(1),
		"enableSuperuserAccess": true,
		"enablePDB":             false,
		"storage":               storage,
		"resources":             resources,
		"inheritedMetadata": map[string]any{
			"labels": map[string]any{LabelDrill: "true"},
		},
		"bootstrap": map[string]any{
			"recovery": map[string]any{"source": externalClusterName},
		},
		"externalClusters": []any{map[string]any{
			"name": externalClusterName,
			"plugin": map[string]any{
				"name": PluginName,
				"parameters": map[string]any{
					"barmanObjectName": StoreName(o.DrillName),
					"serverName":       p.ServerName,
				},
			},
		}},
	}
	if len(o.PodAnnotations) > 0 {
		ann := map[string]any{}
		for k, v := range o.PodAnnotations {
			ann[k] = v
		}
		spec["inheritedMetadata"].(map[string]any)["annotations"] = ann
	}
	if p.WALStorageSize != "" {
		wal := map[string]any{"size": p.WALStorageSize}
		if p.StorageClass != "" {
			wal["storageClass"] = p.StorageClass
		}
		spec["walStorage"] = wal
	}
	switch {
	case p.Image != "":
		spec["imageName"] = p.Image
	case p.ImageCatalogRef != nil:
		spec["imageCatalogRef"] = p.ImageCatalogRef
	}
	if len(p.PullSecrets) > 0 {
		spec["imagePullSecrets"] = p.PullSecrets
	}
	affinity := map[string]any{}
	if len(p.NodeSelector) > 0 {
		ns := map[string]any{}
		for k, v := range p.NodeSelector {
			ns[k] = v
		}
		affinity["nodeSelector"] = ns
	}
	if len(p.Tolerations) > 0 {
		var tols []any
		for i := range p.Tolerations {
			m, err := toUnstructured(&p.Tolerations[i])
			if err != nil {
				return nil, err
			}
			tols = append(tols, m)
		}
		affinity["tolerations"] = tols
	}
	if len(affinity) > 0 {
		spec["affinity"] = affinity
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "postgresql.cnpg.io/v1",
		"kind":       "Cluster",
		"metadata": map[string]any{
			"name":      o.DrillName,
			"namespace": o.DrillNamespace,
			"labels":    p.labels(),
			"annotations": map[string]any{
				"cnpg-restore-drill/started-at": started.UTC().Format(time.RFC3339),
			},
		},
		"spec": spec,
	}}, nil
}

// AssertNoArchive is the hard check that the drill can never write to an
// archive: the Cluster has no backup section, no plugin entries (the
// barman-cloud plugin archives WAL only for a Cluster that lists it there),
// is not a replica cluster, and recovers only from the drill's own
// ObjectStore; the ObjectStore has no retention policy and reads with the
// ambient identity. It runs on the objects exactly as they are about to be
// created. The restore role is read-only as well, but that is the second
// layer, not this one.
func AssertNoArchive(cluster, store *unstructured.Unstructured) error {
	spec, _, _ := unstructured.NestedMap(cluster.Object, "spec")
	if v, ok := spec["backup"]; ok && v != nil {
		return fmt.Errorf("refused: the drill Cluster has a backup section (%v); a drill must never archive or back up", v)
	}
	if v, ok := spec["plugins"]; ok {
		if l, _ := v.([]any); len(l) > 0 {
			return fmt.Errorf("refused: the drill Cluster lists plugins (%v); a plugin entry is how WAL archiving is turned on", l)
		}
	}
	if v, ok := spec["replica"]; ok && v != nil {
		return fmt.Errorf("refused: the drill Cluster is a replica cluster (%v)", v)
	}
	if v, ok := spec["bootstrap"].(map[string]any); !ok || len(v) != 1 || v["recovery"] == nil {
		return fmt.Errorf("refused: the drill Cluster must bootstrap by recovery and nothing else (%v)", spec["bootstrap"])
	}
	storeName := store.GetName()
	ext, _ := spec["externalClusters"].([]any)
	if len(ext) != 1 {
		return fmt.Errorf("refused: the drill Cluster must have exactly one external cluster, its archive (has %d)", len(ext))
	}
	e, _ := ext[0].(map[string]any)
	for k := range e {
		if k != "name" && k != "plugin" {
			return fmt.Errorf("refused: the drill's external cluster has %q; only the barman-cloud plugin source is allowed", k)
		}
	}
	pl, _ := e["plugin"].(map[string]any)
	params, _ := pl["parameters"].(map[string]any)
	if pl["name"] != PluginName || params["barmanObjectName"] != storeName {
		return fmt.Errorf("refused: the drill must recover through %s from ObjectStore %s only", PluginName, storeName)
	}
	if store.GetNamespace() != cluster.GetNamespace() {
		return fmt.Errorf("refused: the drill ObjectStore is not in the drill Cluster's namespace")
	}
	sspec, _, _ := unstructured.NestedMap(store.Object, "spec")
	if v, ok := sspec["retentionPolicy"]; ok && v != nil {
		return fmt.Errorf("refused: the drill ObjectStore has a retention policy (%v); retention deletes backups", v)
	}
	creds, _, _ := unstructured.NestedMap(store.Object, "spec", "configuration", "s3Credentials")
	if len(creds) != 1 || creds["inheritFromIAMRole"] != true {
		return fmt.Errorf("refused: the drill ObjectStore must read with the pod's ambient identity only (s3Credentials {inheritFromIAMRole: true}), has %v", creds)
	}
	conf, _, _ := unstructured.NestedMap(store.Object, "spec", "configuration")
	for _, k := range []string{"azureCredentials", "googleCredentials"} {
		if _, ok := conf[k]; ok {
			return fmt.Errorf("refused: the drill ObjectStore has %s", k)
		}
	}
	return nil
}

// managed reports whether the drill created obj.
func managed(obj *unstructured.Unstructured) bool {
	return obj.GetLabels()[LabelManagedBy] == ManagedBy
}

// EnvFromPairs builds container environment from name=value pairs, sorted.
func EnvFromPairs(pairs map[string]string) []corev1.EnvVar {
	var out []corev1.EnvVar
	for _, k := range sortedKeys(pairs) {
		out = append(out, corev1.EnvVar{Name: k, Value: pairs[k]})
	}
	return out
}
