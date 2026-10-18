package verify

import (
	"context"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// BackupSidecarName is the container the barman-cloud plugin
// (plugin-barman-cloud) injects into every instance Pod of a Cluster that
// archives through it (internal/cnpgi/operator/lifecycle.go in that
// repository names it).
const BackupSidecarName = "plugin-barman-cloud"

// podIdentityEnv are the variables EKS's pod-identity webhook sets on every
// container of a Pod it admits with an association. Credentials are
// injected at admission only, so a Pod admitted before the webhook could
// see the association lacks them for good, and restarting a container does
// not admit it again: the Pod has to be recreated.
var podIdentityEnv = []string{
	"AWS_CONTAINER_CREDENTIALS_FULL_URI",
	"AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE",
}

// IdentitySource is the read side that the Pod Identity assertion needs.
// Like PlacementSource it is optional for a Source; Run fails the assertion
// (it does not panic) when Options.CheckIdentity is set and the Source does
// not implement it. KubeSource implements it.
type IdentitySource interface {
	// InstancePods returns the instance Pods of the Cluster (pods
	// labelled cnpg.io/cluster=<cluster>).
	InstancePods(ctx context.Context, namespace, cluster string) ([]corev1.Pod, error)
}

const nameIdentity = "backup sidecar has Pod Identity"

// checkIdentity asserts that the backup sidecar of every instance Pod has
// the Pod Identity environment. Only variable NAMES are inspected and
// reported; a value (a URL, a token path) is never printed.
func checkIdentity(ctx context.Context, src Source, opt Options) []Result {
	if !opt.CheckIdentity {
		return nil
	}
	is, ok := src.(IdentitySource)
	if !ok {
		return []Result{{Name: nameIdentity, Status: Fail, Message: "this Source cannot read Pods"}}
	}
	pods, err := is.InstancePods(ctx, opt.Namespace, opt.Cluster)
	if err != nil {
		return []Result{{Name: nameIdentity, Status: Fail, Message: "list instance Pods: " + err.Error()}}
	}
	if len(pods) == 0 {
		return []Result{{Name: nameIdentity, Status: Fail, Message: "no instance Pods found (label cnpg.io/cluster=" + opt.Cluster + ")"}}
	}
	sort.Slice(pods, func(i, j int) bool { return pods[i].Name < pods[j].Name })

	var bad []string
	withSidecar := 0
	for i := range pods {
		c := sidecar(&pods[i])
		if c == nil {
			bad = append(bad, pods[i].Name+" (no "+BackupSidecarName+" container)")
			continue
		}
		withSidecar++
		if missing := missingIdentityEnv(c); len(missing) > 0 {
			bad = append(bad, fmt.Sprintf("%s (%s lacks %s)", pods[i].Name, BackupSidecarName, strings.Join(missing, ", ")))
		}
	}
	switch {
	case withSidecar == 0:
		return []Result{{Name: nameIdentity, Status: Skip,
			Message: "no instance Pod has a " + BackupSidecarName + " container: the cluster does not archive through the plugin"}}
	case len(bad) > 0:
		return []Result{{Name: nameIdentity, Status: Fail,
			Message: strings.Join(bad, "; ") + ": the Pod was admitted before EKS could see its Pod Identity association; delete it (one at a time, replicas first) so it is recreated and admitted afresh"}}
	}
	return []Result{{Name: nameIdentity, Status: Pass,
		Message: fmt.Sprintf("%d instance Pod(s): %s has %s", len(pods), BackupSidecarName, strings.Join(podIdentityEnv, " and "))}}
}

func sidecar(p *corev1.Pod) *corev1.Container {
	for i := range p.Spec.Containers {
		if p.Spec.Containers[i].Name == BackupSidecarName {
			return &p.Spec.Containers[i]
		}
	}
	for i := range p.Spec.InitContainers {
		if p.Spec.InitContainers[i].Name == BackupSidecarName {
			return &p.Spec.InitContainers[i]
		}
	}
	return nil
}

func missingIdentityEnv(c *corev1.Container) []string {
	have := map[string]bool{}
	for _, e := range c.Env {
		// A variable sourced from elsewhere (valueFrom) counts as set;
		// only an absent or empty literal is missing.
		if e.Value != "" || e.ValueFrom != nil {
			have[e.Name] = true
		}
	}
	var missing []string
	for _, n := range podIdentityEnv {
		if !have[n] {
			missing = append(missing, n)
		}
	}
	return missing
}
