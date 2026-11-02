package stuckreload

import (
	"bytes"
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/remotecommand"
)

var clusterGVR = schema.GroupVersionResource{Group: "postgresql.cnpg.io", Version: "v1", Resource: "clusters"}

// execTimeout bounds one reload exec.
const execTimeout = 30 * time.Second

// Kube implements Source and Executor through client-go.
type Kube struct {
	cfg  *rest.Config
	dyn  dynamic.Interface
	core kubernetes.Interface
}

// NewKube builds a Kube from a kubeconfig path (empty: $KUBECONFIG, then
// ~/.kube/config, then the in-cluster configuration) and an optional context.
func NewKube(kubeconfig, kubeContext string) (*Kube, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != "" {
		rules.ExplicitPath = kubeconfig
	}

	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		rules, &clientcmd.ConfigOverrides{CurrentContext: kubeContext}).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}

	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}

	core, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}

	return &Kube{cfg: cfg, dyn: dyn, core: core}, nil
}

// Cluster implements Source.
func (k *Kube) Cluster(ctx context.Context, namespace, name string) (*unstructured.Unstructured, error) {
	return k.dyn.Resource(clusterGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
}

// InstancePods implements Source.
func (k *Kube) InstancePods(ctx context.Context, namespace, cluster string) ([]corev1.Pod, error) {
	l, err := k.core.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: InstanceSelector(cluster)})
	if err != nil {
		return nil, err
	}

	return l.Items, nil
}

// Event implements Source with a core/v1 Event.
func (k *Kube) Event(ctx context.Context, cluster *unstructured.Unstructured, message string) error {
	now := metav1.NewTime(time.Now())
	ev := &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{GenerateName: cluster.GetName() + ".", Namespace: cluster.GetNamespace()},
		InvolvedObject: corev1.ObjectReference{
			APIVersion: cluster.GetAPIVersion(), Kind: "Cluster", Name: cluster.GetName(),
			Namespace: cluster.GetNamespace(), UID: cluster.GetUID(),
		},
		Reason:         EventReason,
		Message:        message,
		Type:           corev1.EventTypeNormal,
		Source:         corev1.EventSource{Component: "cnpgctl-stuck-reload"},
		FirstTimestamp: now,
		LastTimestamp:  now,
		Count:          1,
	}
	_, err := k.core.CoreV1().Events(cluster.GetNamespace()).Create(ctx, ev, metav1.CreateOptions{})

	return err
}

// Exec implements Executor over the exec subresource: a websocket stream,
// falling back to SPDY when the API server does not upgrade it.
func (k *Kube) Exec(ctx context.Context, namespace, pod, container string, command []string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, execTimeout)
	defer cancel()

	req := k.core.CoreV1().RESTClient().Post().
		Resource("pods").Namespace(namespace).Name(pod).SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: container, Command: command, Stdout: true, Stderr: true,
		}, scheme.ParameterCodec)

	ws, err := remotecommand.NewWebSocketExecutor(k.cfg, "GET", req.URL().String())
	if err != nil {
		return "", err
	}

	spdy, err := remotecommand.NewSPDYExecutor(k.cfg, "POST", req.URL())
	if err != nil {
		return "", err
	}

	ex, err := remotecommand.NewFallbackExecutor(ws, spdy, httpstream.IsUpgradeFailure)
	if err != nil {
		return "", err
	}

	var stdout, stderr bytes.Buffer
	if err := ex.StreamWithContext(ctx, remotecommand.StreamOptions{Stdout: &stdout, Stderr: &stderr}); err != nil {
		return stdout.String(), fmt.Errorf("exec in %s: %w (stderr: %s)", pod, err, bytes.TrimSpace(stderr.Bytes()))
	}

	return stdout.String(), nil
}
