package verify

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kfake "k8s.io/client-go/kubernetes/fake"
)

type identityFake struct {
	fullFake
	list []corev1.Pod
	err  error
}

func (f identityFake) InstancePods(context.Context, string, string) ([]corev1.Pod, error) {
	return f.list, f.err
}

func env(names ...string) []corev1.EnvVar {
	var out []corev1.EnvVar
	for _, n := range names {
		out = append(out, corev1.EnvVar{Name: n, Value: "placeholder"})
	}
	return out
}

func podWith(name string, containers ...corev1.Container) corev1.Pod {
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "db", Labels: map[string]string{"cnpg.io/cluster": "pg"}},
		Spec:       corev1.PodSpec{Containers: containers},
	}
}

func sidecarWith(names ...string) corev1.Container {
	return corev1.Container{Name: BackupSidecarName, Env: env(names...)}
}

var both = []string{"AWS_CONTAINER_CREDENTIALS_FULL_URI", "AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE"}

func identityResult(t *testing.T, pods []corev1.Pod, err error) *Result {
	t.Helper()
	f := identityFake{fullFake: base(t), list: pods, err: err}
	r := find(runWith(t, f, Options{CheckIdentity: true}), nameIdentity)
	require.NotNil(t, r)
	return r
}

func TestIdentityOffByDefault(t *testing.T) {
	assert.Nil(t, find(runWith(t, identityFake{fullFake: base(t)}, Options{}), nameIdentity))
}

func TestIdentityPass(t *testing.T) {
	r := identityResult(t, []corev1.Pod{
		podWith("pg-1", corev1.Container{Name: "postgres"}, sidecarWith(both...)),
		podWith("pg-2", sidecarWith(both...)),
	}, nil)
	assert.Equal(t, Pass, r.Status, r.Message)
	assert.Contains(t, r.Message, "2 instance Pod(s)")
}

func TestIdentityFailsOnAMissingVariable(t *testing.T) {
	r := identityResult(t, []corev1.Pod{
		podWith("pg-1", sidecarWith(both...)),
		podWith("pg-2", sidecarWith("AWS_CONTAINER_CREDENTIALS_FULL_URI")),
		podWith("pg-3", sidecarWith()),
	}, nil)
	assert.Equal(t, Fail, r.Status)
	assert.Contains(t, r.Message, "pg-2 (plugin-barman-cloud lacks AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE)")
	assert.Contains(t, r.Message, "pg-3 (plugin-barman-cloud lacks AWS_CONTAINER_CREDENTIALS_FULL_URI, AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE)")
	assert.NotContains(t, r.Message, "pg-1")
	assert.NotContains(t, r.Message, "placeholder", "a value is never printed")
}

func TestIdentityEmptyValueIsMissing(t *testing.T) {
	c := sidecarWith(both...)
	c.Env[0].Value = ""
	r := identityResult(t, []corev1.Pod{podWith("pg-1", c)}, nil)
	assert.Equal(t, Fail, r.Status)
	assert.Contains(t, r.Message, "AWS_CONTAINER_CREDENTIALS_FULL_URI")
}

func TestIdentityOtherContainersDoNotCount(t *testing.T) {
	// The identity on the postgres container does not stand for the sidecar's.
	pg := corev1.Container{Name: "postgres", Env: env(both...)}
	r := identityResult(t, []corev1.Pod{podWith("pg-1", pg, sidecarWith())}, nil)
	assert.Equal(t, Fail, r.Status)
}

func TestIdentitySidecarAbsent(t *testing.T) {
	all := identityResult(t, []corev1.Pod{podWith("pg-1", corev1.Container{Name: "postgres"})}, nil)
	assert.Equal(t, Skip, all.Status, "a cluster that does not archive through the plugin")

	mixed := identityResult(t, []corev1.Pod{
		podWith("pg-1", sidecarWith(both...)),
		podWith("pg-2", corev1.Container{Name: "postgres"}),
	}, nil)
	assert.Equal(t, Fail, mixed.Status)
	assert.Contains(t, mixed.Message, "pg-2 (no plugin-barman-cloud container)")
}

func TestIdentityNoPodsAndErrors(t *testing.T) {
	assert.Equal(t, Fail, identityResult(t, nil, nil).Status)
	assert.Equal(t, Fail, identityResult(t, nil, assert.AnError).Status)

	// A Source without Pod access fails the assertion; it does not panic.
	r := find(runWith(t, base(t), Options{CheckIdentity: true}), nameIdentity)
	require.NotNil(t, r)
	assert.Equal(t, Fail, r.Status)
}

// TestKubeSourceInstancePods: KubeSource lists only the cluster's own Pods,
// through a fake clientset (read-only: a List and nothing else).
func TestKubeSourceInstancePods(t *testing.T) {
	other := podWith("other-1", sidecarWith())
	other.Labels = map[string]string{"cnpg.io/cluster": "other"}
	cs := kfake.NewClientset(ptr(podWith("pg-1", sidecarWith(both...))), ptr(other))
	ks := &KubeSource{core: cs}

	pods, err := ks.InstancePods(context.Background(), "db", "pg")
	require.NoError(t, err)
	require.Len(t, pods, 1)
	assert.Equal(t, "pg-1", pods[0].Name)

	for _, a := range cs.Actions() {
		assert.Equal(t, "list", a.GetVerb())
	}
}

func ptr[T any](v T) *T { return &v }
