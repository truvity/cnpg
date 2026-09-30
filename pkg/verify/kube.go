package verify

import (
	"context"
	"fmt"

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

var clusterGVR = schema.GroupVersionResource{Group: "postgresql.cnpg.io", Version: "v1", Resource: "clusters"}

// KubeSource reads through client-go. It only ever issues GETs.
type KubeSource struct {
	dyn  dynamic.Interface
	core kubernetes.Interface
}

// NewKubeSource builds a KubeSource from a kubeconfig path (empty means
// the default loading rules: $KUBECONFIG, then ~/.kube/config) and an
// optional context name.
func NewKubeSource(kubeconfig, kubeContext string) (*KubeSource, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != "" {
		rules.ExplicitPath = kubeconfig
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		rules, &clientcmd.ConfigOverrides{CurrentContext: kubeContext}).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}
	return newKubeSource(cfg)
}

func newKubeSource(cfg *rest.Config) (*KubeSource, error) {
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	core, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &KubeSource{dyn: dyn, core: core}, nil
}

// Cluster implements Source.
func (k *KubeSource) Cluster(ctx context.Context, namespace, name string) (*unstructured.Unstructured, error) {
	return k.dyn.Resource(clusterGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
}

// Secret implements Source.
func (k *KubeSource) Secret(ctx context.Context, namespace, name string) (*corev1.Secret, error) {
	s, err := k.core.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("%w: secret %s/%s", ErrNotFound, namespace, name)
	}
	return s, err
}
