// Copyright 2026 Truvity B.V.. All rights reserved.
// SPDX-License-Identifier: MIT

package drill

import (
	"context"
	"errors"
	"fmt"
	"io"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// ErrNotFound is what a Kube returns, wrapped or not, for an object that does
// not exist.
var ErrNotFound = errors.New("not found")

// The API resources the drill reads and writes.
var (
	ClusterGVR     = schema.GroupVersionResource{Group: "postgresql.cnpg.io", Version: "v1", Resource: "clusters"}
	DatabaseGVR    = schema.GroupVersionResource{Group: "postgresql.cnpg.io", Version: "v1", Resource: "databases"}
	ObjectStoreGVR = schema.GroupVersionResource{Group: "barmancloud.cnpg.io", Version: "v1", Resource: "objectstores"}
)

// Kube is the part of the Kubernetes API a drill uses. Get methods return an
// error wrapping ErrNotFound for a missing object; Delete methods return nil
// for one.
type Kube interface {
	// Get, Create and Delete a namespaced custom object (ClusterGVR,
	// ObjectStoreGVR).
	Get(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, error)
	Create(ctx context.Context, gvr schema.GroupVersionResource, obj *unstructured.Unstructured) error
	Delete(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) error
	// List lists a namespaced custom object; a resource the API does not
	// serve yields no objects and no error.
	List(ctx context.Context, gvr schema.GroupVersionResource, namespace string) ([]unstructured.Unstructured, error)

	ListPVCs(ctx context.Context, namespace, selector string) ([]corev1.PersistentVolumeClaim, error)
	ListPods(ctx context.Context, namespace, selector string) ([]corev1.Pod, error)
	ListEvents(ctx context.Context, namespace string) ([]corev1.Event, error)
	// Logs streams a container's log. tailLines < 0 means all of it.
	Logs(ctx context.Context, namespace, pod, container string, follow bool, tailLines int64) (io.ReadCloser, error)
	GetSecret(ctx context.Context, namespace, name string) (*corev1.Secret, error)

	GetConfigMap(ctx context.Context, namespace, name string) (*corev1.ConfigMap, error)
	CreateConfigMap(ctx context.Context, cm *corev1.ConfigMap) error
	UpdateConfigMap(ctx context.Context, cm *corev1.ConfigMap) error

	// ReclaimPolicy returns a StorageClass's reclaim policy, or "" when it
	// cannot be read (the caller then warns instead of refusing).
	ReclaimPolicy(ctx context.Context, storageClass string) (string, error)
}

// KubeClient is the client-go implementation of Kube.
type KubeClient struct {
	dyn  dynamic.Interface
	core kubernetes.Interface
}

var _ Kube = (*KubeClient)(nil)

// NewKubeClient builds a KubeClient from a kubeconfig path (empty means the
// default loading rules: in a pod its ServiceAccount, else $KUBECONFIG, then
// ~/.kube/config) and an optional context name.
func NewKubeClient(kubeconfig, kubeContext string) (*KubeClient, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != "" {
		rules.ExplicitPath = kubeconfig
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		rules, &clientcmd.ConfigOverrides{CurrentContext: kubeContext}).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}
	return NewKubeClientForConfig(cfg)
}

// NewKubeClientForConfig builds a KubeClient from a rest.Config.
func NewKubeClientForConfig(cfg *rest.Config) (*KubeClient, error) {
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	core, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return NewKubeClientFrom(dyn, core), nil
}

// NewKubeClientFrom wraps existing clients (tests pass client-go fakes).
func NewKubeClientFrom(dyn dynamic.Interface, core kubernetes.Interface) *KubeClient {
	return &KubeClient{dyn: dyn, core: core}
}

func notFound(err error, what string) error {
	if apierrors.IsNotFound(err) {
		return fmt.Errorf("%w: %s", ErrNotFound, what)
	}
	return err
}

// Get implements Kube.
func (k *KubeClient) Get(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, error) {
	u, err := k.dyn.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, notFound(err, fmt.Sprintf("%s %s/%s", gvr.Resource, namespace, name))
	}
	return u, nil
}

// Create implements Kube.
func (k *KubeClient) Create(ctx context.Context, gvr schema.GroupVersionResource, obj *unstructured.Unstructured) error {
	_, err := k.dyn.Resource(gvr).Namespace(obj.GetNamespace()).Create(ctx, obj, metav1.CreateOptions{})
	return err
}

// Delete implements Kube. Dependents (the Cluster's pods, volumes, Secrets,
// Services and the plugin's Role) are removed by the garbage collector.
func (k *KubeClient) Delete(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) error {
	bg := metav1.DeletePropagationBackground
	err := k.dyn.Resource(gvr).Namespace(namespace).Delete(ctx, name, metav1.DeleteOptions{PropagationPolicy: &bg})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

// List implements Kube.
func (k *KubeClient) List(ctx context.Context, gvr schema.GroupVersionResource, namespace string) ([]unstructured.Unstructured, error) {
	l, err := k.dyn.Resource(gvr).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return l.Items, nil
}

// ListPVCs implements Kube.
func (k *KubeClient) ListPVCs(ctx context.Context, namespace, selector string) ([]corev1.PersistentVolumeClaim, error) {
	l, err := k.core.CoreV1().PersistentVolumeClaims(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, err
	}
	return l.Items, nil
}

// ListPods implements Kube.
func (k *KubeClient) ListPods(ctx context.Context, namespace, selector string) ([]corev1.Pod, error) {
	l, err := k.core.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, err
	}
	return l.Items, nil
}

// ListEvents implements Kube.
func (k *KubeClient) ListEvents(ctx context.Context, namespace string) ([]corev1.Event, error) {
	l, err := k.core.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	return l.Items, nil
}

// Logs implements Kube.
func (k *KubeClient) Logs(ctx context.Context, namespace, pod, container string, follow bool, tailLines int64) (io.ReadCloser, error) {
	opts := &corev1.PodLogOptions{Container: container, Follow: follow}
	if tailLines >= 0 {
		opts.TailLines = &tailLines
	}
	return k.core.CoreV1().Pods(namespace).GetLogs(pod, opts).Stream(ctx)
}

// GetSecret implements Kube.
func (k *KubeClient) GetSecret(ctx context.Context, namespace, name string) (*corev1.Secret, error) {
	s, err := k.core.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, notFound(err, "secret "+namespace+"/"+name)
	}
	return s, nil
}

// GetConfigMap implements Kube.
func (k *KubeClient) GetConfigMap(ctx context.Context, namespace, name string) (*corev1.ConfigMap, error) {
	c, err := k.core.CoreV1().ConfigMaps(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, notFound(err, "configmap "+namespace+"/"+name)
	}
	return c, nil
}

// CreateConfigMap implements Kube.
func (k *KubeClient) CreateConfigMap(ctx context.Context, cm *corev1.ConfigMap) error {
	_, err := k.core.CoreV1().ConfigMaps(cm.Namespace).Create(ctx, cm, metav1.CreateOptions{})
	return err
}

// UpdateConfigMap implements Kube.
func (k *KubeClient) UpdateConfigMap(ctx context.Context, cm *corev1.ConfigMap) error {
	_, err := k.core.CoreV1().ConfigMaps(cm.Namespace).Update(ctx, cm, metav1.UpdateOptions{})
	return err
}

// ReclaimPolicy implements Kube. A StorageClass the caller may not read, or
// that does not exist, yields "" and no error.
func (k *KubeClient) ReclaimPolicy(ctx context.Context, storageClass string) (string, error) {
	sc, err := k.core.StorageV1().StorageClasses().Get(ctx, storageClass, metav1.GetOptions{})
	if apierrors.IsForbidden(err) || apierrors.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if sc.ReclaimPolicy == nil {
		return string(corev1.PersistentVolumeReclaimDelete), nil
	}
	return string(*sc.ReclaimPolicy), nil
}
