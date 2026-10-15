package verify

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// PlacementSource is the read side that the node-pool assertion needs.
// It is a separate interface so that a Source written before the
// assertion existed still satisfies Source; Run fails the assertion
// (it does not panic) when Options.PoolLabel is set and the Source does
// not implement it. KubeSource implements it.
type PlacementSource interface {
	// InstanceNodes returns the node name of each instance Pod of the
	// Cluster (pods labelled cnpg.io/cluster=<cluster>), keyed by Pod name.
	// A Pod that is not scheduled has the empty node name.
	InstanceNodes(ctx context.Context, namespace, cluster string) (map[string]string, error)
	// NodeLabels returns the labels of a node, or an error wrapping
	// ErrNotFound.
	NodeLabels(ctx context.Context, node string) (map[string]string, error)
}

// DeclarativeSource is the read side that the applied assertion needs.
// Like PlacementSource it is optional for a Source; KubeSource
// implements it.
type DeclarativeSource interface {
	// DeclarativeObjects returns the postgresql.cnpg.io Database and
	// DatabaseRole objects of the namespace whose spec.cluster.name is
	// the cluster. An absent CRD yields none, not an error.
	DeclarativeObjects(ctx context.Context, namespace, cluster string) ([]*unstructured.Unstructured, error)
}

// ParsePoolLabel splits "key=value". Both parts must be non-empty.
func ParsePoolLabel(s string) (key, value string, err error) {
	key, value, ok := strings.Cut(s, "=")
	if !ok || key == "" || value == "" {
		return "", "", fmt.Errorf("pool label %q: want key=value", s)
	}
	return key, value, nil
}

const (
	namePlacement = "instances on pool"
	nameApplied   = "Database/DatabaseRole applied"
)

func checkPlacement(ctx context.Context, src Source, opt Options) []Result {
	if opt.PoolLabel == "" {
		return nil
	}
	key, value, err := ParsePoolLabel(opt.PoolLabel)
	if err != nil {
		return []Result{{Name: namePlacement, Status: Fail, Message: err.Error()}}
	}
	ps, ok := src.(PlacementSource)
	if !ok {
		return []Result{{Name: namePlacement, Status: Fail, Message: "this Source cannot read Pods and Nodes"}}
	}
	nodes, err := ps.InstanceNodes(ctx, opt.Namespace, opt.Cluster)
	if err != nil {
		return []Result{{Name: namePlacement, Status: Fail, Message: "list instance Pods: " + err.Error()}}
	}
	if len(nodes) == 0 {
		return []Result{{Name: namePlacement, Status: Fail, Message: "no instance Pods found (label cnpg.io/cluster=" + opt.Cluster + ")"}}
	}
	pods := make([]string, 0, len(nodes))
	for p := range nodes {
		pods = append(pods, p)
	}
	sort.Strings(pods)
	var bad []string
	for _, p := range pods {
		n := nodes[p]
		if n == "" {
			bad = append(bad, p+" (not scheduled)")
			continue
		}
		labels, err := ps.NodeLabels(ctx, n)
		if err != nil {
			bad = append(bad, fmt.Sprintf("%s (node %s: %v)", p, n, err))
			continue
		}
		if got := labels[key]; got != value {
			bad = append(bad, fmt.Sprintf("%s on node %s with %s=%q", p, n, key, got))
		}
	}
	if len(bad) > 0 {
		return []Result{{Name: namePlacement, Status: Fail,
			Message: fmt.Sprintf("want %s=%s: %s", key, value, strings.Join(bad, "; "))}}
	}
	return []Result{{Name: namePlacement, Status: Pass,
		Message: fmt.Sprintf("%d instance(s) on nodes with %s=%s", len(pods), key, value)}}
}

// checkApplied asserts that every Database and DatabaseRole of the
// cluster reports status.applied true. A cluster with none passes with a
// SKIP, since not every cluster declares any.
func checkApplied(ctx context.Context, src Source, opt Options) []Result {
	if !opt.CheckApplied {
		return nil
	}
	ds, ok := src.(DeclarativeSource)
	if !ok {
		return []Result{{Name: nameApplied, Status: Fail, Message: "this Source cannot read Database and DatabaseRole objects"}}
	}
	objs, err := ds.DeclarativeObjects(ctx, opt.Namespace, opt.Cluster)
	if err != nil {
		return []Result{{Name: nameApplied, Status: Fail, Message: "list objects: " + err.Error()}}
	}
	if len(objs) == 0 {
		return []Result{{Name: nameApplied, Status: Skip, Message: "the cluster has no Database or DatabaseRole objects"}}
	}
	var bad []string
	for _, o := range objs {
		id := o.GetKind() + "/" + o.GetName()
		applied, found, _ := unstructured.NestedBool(o.Object, "status", "applied")
		msg, _, _ := unstructured.NestedString(o.Object, "status", "message")
		switch {
		case found && applied:
		case !found:
			bad = append(bad, id+" (no status.applied yet)")
		case msg != "":
			bad = append(bad, fmt.Sprintf("%s (applied false: %s)", id, msg))
		default:
			bad = append(bad, id+" (applied false)")
		}
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		return []Result{{Name: nameApplied, Status: Fail, Message: "not applied: " + strings.Join(bad, "; ")}}
	}
	return []Result{{Name: nameApplied, Status: Pass, Message: fmt.Sprintf("%d object(s) applied", len(objs))}}
}
