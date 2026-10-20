// Copyright 2026 Truvity B.V.. All rights reserved.
// SPDX-License-Identifier: MIT

package drill

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// PluginName is the barman-cloud plugin's name in a Cluster's spec.
const PluginName = "barman-cloud.cloudnative-pg.io"

// Plan is everything a drill creates, resolved from the Options and the
// source Cluster before anything is written.
type Plan struct {
	Options Options

	DestinationPath string
	ServerName      string
	EndpointURL     string
	// EndpointCA is the source ObjectStore's endpointCA reference, copied
	// (the Secret must exist in the drill namespace).
	EndpointCA map[string]any

	Image           string
	ImageCatalogRef map[string]any
	PullSecrets     []any

	StorageClass   string
	StorageSize    string
	WALStorageSize string // empty: no separate WAL volume
	Resources      corev1.ResourceRequirements
	NodeSelector   map[string]string
	Tolerations    []corev1.Toleration

	// Databases expected, and where the list came from.
	Databases       []string
	DatabasesSource string

	// Notes are things the operator of the drill should know (printed).
	Notes []string
}

// MakePlan reads the source Cluster (and its ObjectStore, volumes and
// Database objects) and resolves every input of the drill. It only reads.
func MakePlan(ctx context.Context, k Kube, o Options) (*Plan, error) {
	src, err := k.Get(ctx, ClusterGVR, o.Namespace, o.Cluster)
	if err != nil {
		return nil, fmt.Errorf("read the source Cluster %s/%s: %w", o.Namespace, o.Cluster, err)
	}
	p := &Plan{Options: o}
	if err := p.coordinates(ctx, k, src); err != nil {
		return nil, err
	}
	if err := p.image(src); err != nil {
		return nil, err
	}
	if err := p.storage(ctx, k, src); err != nil {
		return nil, err
	}
	p.scheduling(src)
	if err := p.databases(ctx, k, src); err != nil {
		return nil, err
	}
	if o.Resources != nil {
		p.Resources = *o.Resources
	} else {
		p.Resources = DefaultResources()
	}
	return p, nil
}

// archivePlugin returns the parameters of the source's barman-cloud plugin
// entry that archives WAL (or the only barman-cloud entry).
func archivePlugin(src *unstructured.Unstructured) map[string]any {
	plugins, _, _ := unstructured.NestedSlice(src.Object, "spec", "plugins")
	var only map[string]any
	n := 0
	for _, raw := range plugins {
		pl, ok := raw.(map[string]any)
		if !ok || pl["name"] != PluginName {
			continue
		}
		if enabled, ok := pl["enabled"].(bool); ok && !enabled {
			continue
		}
		params, _ := pl["parameters"].(map[string]any)
		if params == nil {
			params = map[string]any{}
		}
		if archiver, _ := pl["isWALArchiver"].(bool); archiver {
			return params
		}
		only = params
		n++
	}
	if n == 1 {
		return only
	}
	return nil
}

func (p *Plan) coordinates(ctx context.Context, k Kube, src *unstructured.Unstructured) error {
	o := p.Options
	params := archivePlugin(src)
	p.ServerName, p.DestinationPath, p.EndpointURL = o.ServerName, o.DestinationPath, o.EndpointURL
	if p.ServerName == "" && params != nil {
		p.ServerName, _ = params["serverName"].(string)
		if p.ServerName == "" {
			// The plugin's default: the Cluster's name.
			p.ServerName = o.Cluster
		}
	}
	if p.DestinationPath == "" {
		storeName, _ := params["barmanObjectName"].(string)
		if storeName == "" {
			return fmt.Errorf("the source Cluster %s/%s has no %s plugin entry naming an ObjectStore: give the destination path and server name explicitly", o.Namespace, o.Cluster, PluginName)
		}
		store, err := k.Get(ctx, ObjectStoreGVR, o.Namespace, storeName)
		if err != nil {
			return fmt.Errorf("read the source ObjectStore %s/%s: %w", o.Namespace, storeName, err)
		}
		conf, _, _ := unstructured.NestedMap(store.Object, "spec", "configuration")
		for _, other := range []string{"azureCredentials", "googleCredentials"} {
			if _, ok := conf[other]; ok {
				return fmt.Errorf("ObjectStore %s/%s is not on S3 (%s): the drill supports S3 only", o.Namespace, storeName, other)
			}
		}
		p.DestinationPath, _ = conf["destinationPath"].(string)
		if p.EndpointURL == "" {
			p.EndpointURL, _ = conf["endpointURL"].(string)
		}
		if ca, ok := conf["endpointCA"].(map[string]any); ok {
			p.EndpointCA = ca
			if o.DrillNamespace != o.Namespace {
				p.Notes = append(p.Notes, fmt.Sprintf("the source ObjectStore names endpointCA Secret %v: it must exist in %s too", ca["name"], o.DrillNamespace))
			}
		}
		if creds, ok := conf["s3Credentials"].(map[string]any); ok {
			if inherit, _ := creds["inheritFromIAMRole"].(bool); !inherit {
				p.Notes = append(p.Notes, "the source archives with static S3 credentials; the drill does not copy them and reads with the pod's ambient identity (the restore ServiceAccount)")
			}
		}
	}
	if !strings.HasPrefix(p.DestinationPath, "s3://") || len(p.DestinationPath) <= len("s3://") {
		return fmt.Errorf("destination path %q: only s3://<bucket>[/<prefix>] is supported", p.DestinationPath)
	}
	if p.ServerName == "" {
		return fmt.Errorf("no server name: the source Cluster %s/%s has no %s plugin entry and none was given", o.Namespace, o.Cluster, PluginName)
	}
	return nil
}

func (p *Plan) image(src *unstructured.Unstructured) error {
	o := p.Options
	if o.Image != "" {
		p.Image = o.Image
		return nil
	}
	if img, _, _ := unstructured.NestedString(src.Object, "status", "image"); img != "" {
		p.Image = img
	} else if img, _, _ := unstructured.NestedString(src.Object, "spec", "imageName"); img != "" {
		p.Image = img
	} else if ref, ok, _ := unstructured.NestedMap(src.Object, "spec", "imageCatalogRef"); ok {
		if ref["kind"] != "ClusterImageCatalog" && o.DrillNamespace != o.Namespace {
			return fmt.Errorf("the source uses a namespaced ImageCatalog and reports no status.image: give the image explicitly")
		}
		p.ImageCatalogRef = ref
	} else {
		p.Notes = append(p.Notes, "the source names no image: the drill uses the operator's default PostgreSQL image, which must be the source's major version")
	}
	if o.DrillNamespace == o.Namespace {
		p.PullSecrets, _, _ = unstructured.NestedSlice(src.Object, "spec", "imagePullSecrets")
	}
	return nil
}

// volumeSize is the larger of the spec size and the capacity of the source's
// PersistentVolumeClaims of that role (a volume may have been grown).
func volumeSize(ctx context.Context, k Kube, o Options, spec, role string) (string, error) {
	best := resource.Quantity{}
	if spec != "" {
		q, err := resource.ParseQuantity(spec)
		if err != nil {
			return "", fmt.Errorf("the source's %s size %q: %w", role, spec, err)
		}
		best = q
	}
	pvcs, err := k.ListPVCs(ctx, o.Namespace, "cnpg.io/cluster="+o.Cluster+",cnpg.io/pvcRole="+role)
	if err != nil {
		return "", fmt.Errorf("list the source's volumes: %w", err)
	}
	for _, pvc := range pvcs {
		if c, ok := pvc.Status.Capacity[corev1.ResourceStorage]; ok && c.Cmp(best) > 0 {
			best = c
		}
	}
	if best.IsZero() {
		return "", nil
	}
	return best.String(), nil
}

func (p *Plan) storage(ctx context.Context, k Kube, src *unstructured.Unstructured) error {
	o := p.Options
	specSize, _, _ := unstructured.NestedString(src.Object, "spec", "storage", "size")
	p.StorageClass = o.StorageClass
	if p.StorageClass == "" {
		p.StorageClass, _, _ = unstructured.NestedString(src.Object, "spec", "storage", "storageClass")
	}
	p.StorageSize = o.StorageSize
	if p.StorageSize == "" {
		s, err := volumeSize(ctx, k, o, specSize, "PG_DATA")
		if err != nil {
			return err
		}
		p.StorageSize = s
	}
	if p.StorageSize == "" {
		return errors.New("no storage size: the source reports none and none was given")
	}
	if _, err := resource.ParseQuantity(p.StorageSize); err != nil {
		return fmt.Errorf("storage size %q: %w", p.StorageSize, err)
	}
	if wal, ok, _ := unstructured.NestedMap(src.Object, "spec", "walStorage"); ok {
		p.WALStorageSize = o.WALStorageSize
		if p.WALStorageSize == "" {
			walSpec, _ := wal["size"].(string)
			s, err := volumeSize(ctx, k, o, walSpec, "PG_WAL")
			if err != nil {
				return err
			}
			p.WALStorageSize = s
		}
		if p.WALStorageSize == "" {
			return errors.New("the source has a WAL volume of no known size: give the WAL storage size")
		}
		if _, err := resource.ParseQuantity(p.WALStorageSize); err != nil {
			return fmt.Errorf("WAL storage size %q: %w", p.WALStorageSize, err)
		}
	}
	if p.StorageClass == "" {
		p.Notes = append(p.Notes, "no storage class: the drill volumes use the cluster's default class; make sure it deletes its volumes")
		return nil
	}
	policy, err := k.ReclaimPolicy(ctx, p.StorageClass)
	if err != nil {
		return fmt.Errorf("read StorageClass %s: %w", p.StorageClass, err)
	}
	switch {
	case policy == "":
		p.Notes = append(p.Notes, fmt.Sprintf("could not read StorageClass %s: make sure it deletes its volumes, or every drill leaves its volumes behind", p.StorageClass))
	case policy == string(corev1.PersistentVolumeReclaimRetain) && !o.AllowRetainedVolumes:
		return fmt.Errorf("StorageClass %s retains its volumes: every drill would leave its volumes behind. Use a class that deletes them, or allow retained volumes explicitly", p.StorageClass)
	}
	return nil
}

func (p *Plan) scheduling(src *unstructured.Unstructured) {
	o := p.Options
	if o.NodeSelector != nil || o.Tolerations != nil {
		p.NodeSelector, p.Tolerations = o.NodeSelector, o.Tolerations
		return
	}
	if ns, ok, _ := unstructured.NestedStringMap(src.Object, "spec", "affinity", "nodeSelector"); ok {
		p.NodeSelector = ns
	}
	tols, _, _ := unstructured.NestedSlice(src.Object, "spec", "affinity", "tolerations")
	for _, raw := range tols {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		var t corev1.Toleration
		if err := runtimeFromUnstructured(m, &t); err == nil {
			p.Tolerations = append(p.Tolerations, t)
		}
	}
}

func (p *Plan) databases(ctx context.Context, k Kube, src *unstructured.Unstructured) error {
	o := p.Options
	if len(o.Databases) > 0 {
		p.Databases, p.DatabasesSource = dedupe(o.Databases), "given"
		return nil
	}
	var names []string
	objs, err := k.List(ctx, DatabaseGVR, o.Namespace)
	if err != nil {
		return fmt.Errorf("list the source's Database objects: %w", err)
	}
	for _, d := range objs {
		cluster, _, _ := unstructured.NestedString(d.Object, "spec", "cluster", "name")
		ensure, _, _ := unstructured.NestedString(d.Object, "spec", "ensure")
		name, _, _ := unstructured.NestedString(d.Object, "spec", "name")
		if cluster == o.Cluster && ensure != "absent" && name != "" {
			names = append(names, name)
		}
	}
	for _, path := range [][]string{{"spec", "bootstrap", "initdb", "database"}, {"spec", "bootstrap", "recovery", "database"}} {
		if name, _, _ := unstructured.NestedString(src.Object, path...); name != "" {
			names = append(names, name)
		}
	}
	if len(names) > 0 {
		p.Databases, p.DatabasesSource = dedupe(names), "source"
		return nil
	}
	p.DatabasesSource = "restored"
	return nil
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}
