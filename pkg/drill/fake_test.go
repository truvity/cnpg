// Copyright 2026 Truvity B.V.. All rights reserved.
// SPDX-License-Identifier: MIT

package drill

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// fakeKube is an in-memory Kube. A drill Cluster it is asked to create goes
// through a scripted recovery: a running recovery pod, then (after
// healthyAfter reads) a healthy Cluster, unless failedRecoveries or
// neverHealthy say otherwise.
type fakeKube struct {
	mu sync.Mutex

	objs    map[string]*unstructured.Unstructured
	pvcs    []corev1.PersistentVolumeClaim
	secrets map[string]*corev1.Secret
	cms     map[string]*corev1.ConfigMap
	reclaim map[string]string
	events  []corev1.Event

	recoveryLog      string
	healthyAfter     int
	failedRecoveries int
	neverHealthy     bool

	reads   map[string]int
	created []string
	deleted []string
}

func newFakeKube() *fakeKube {
	return &fakeKube{
		objs:         map[string]*unstructured.Unstructured{},
		secrets:      map[string]*corev1.Secret{},
		cms:          map[string]*corev1.ConfigMap{},
		reclaim:      map[string]string{},
		reads:        map[string]int{},
		healthyAfter: 2,
		recoveryLog: strings.Join([]string{
			`{"level":"info","logger":"postgres","msg":"record","record":{"message":"redo done at 0/5000060 system usage: CPU: user: 0.00 s"}}`,
			`{"level":"info","logger":"postgres","msg":"record","record":{"message":"last completed transaction was at log time 2026-10-20 11:55:00.123456+00"}}`,
			`{"level":"info","logger":"postgres","msg":"record","record":{"message":"archive recovery complete"}}`,
		}, "\n"),
	}
}

func key(gvr schema.GroupVersionResource, ns, name string) string {
	return gvr.Resource + "/" + ns + "/" + name
}

func (f *fakeKube) put(gvr schema.GroupVersionResource, obj *unstructured.Unstructured) {
	f.objs[key(gvr, obj.GetNamespace(), obj.GetName())] = obj
}

func (f *fakeKube) Get(_ context.Context, gvr schema.GroupVersionResource, ns, name string) (*unstructured.Unstructured, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := key(gvr, ns, name)
	o, ok := f.objs[k]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, k)
	}
	if gvr == ClusterGVR && managed(o) {
		f.reads[k]++
		if !f.neverHealthy && f.failedRecoveries == 0 && f.reads[k] > f.healthyAfter {
			_ = unstructured.SetNestedField(o.Object, phaseHealthy, "status", "phase")
			_ = unstructured.SetNestedField(o.Object, int64(1), "status", "readyInstances")
			_ = unstructured.SetNestedField(o.Object, name+"-1", "status", "currentPrimary")
		} else {
			_ = unstructured.SetNestedField(o.Object, "Setting up primary", "status", "phase")
		}
	}
	return o.DeepCopy(), nil
}

func (f *fakeKube) Create(_ context.Context, gvr schema.GroupVersionResource, obj *unstructured.Unstructured) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := key(gvr, obj.GetNamespace(), obj.GetName())
	if _, ok := f.objs[k]; ok {
		return fmt.Errorf("%s already exists", k)
	}
	c := obj.DeepCopy()
	c.SetCreationTimestamp(metav1.NewTime(time.Now()))
	f.objs[k] = c
	f.created = append(f.created, k)
	return nil
}

func (f *fakeKube) Delete(_ context.Context, gvr schema.GroupVersionResource, ns, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := key(gvr, ns, name)
	if _, ok := f.objs[k]; ok {
		delete(f.objs, k)
		f.deleted = append(f.deleted, k)
	}
	return nil
}

func (f *fakeKube) List(_ context.Context, gvr schema.GroupVersionResource, ns string) ([]unstructured.Unstructured, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []unstructured.Unstructured
	keys := sortedKeys(f.objs)
	for _, k := range keys {
		if strings.HasPrefix(k, gvr.Resource+"/"+ns+"/") {
			out = append(out, *f.objs[k].DeepCopy())
		}
	}
	return out, nil
}

func matches(labels map[string]string, selector string) bool {
	for _, term := range strings.Split(selector, ",") {
		k, v, _ := strings.Cut(term, "=")
		if labels[k] != v {
			return false
		}
	}
	return true
}

func (f *fakeKube) ListPVCs(_ context.Context, ns, selector string) ([]corev1.PersistentVolumeClaim, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []corev1.PersistentVolumeClaim
	for _, p := range f.pvcs {
		if p.Namespace == ns && matches(p.Labels, selector) {
			out = append(out, p)
		}
	}
	return out, nil
}

// ListPods invents the drill's pods from the drill Cluster's state.
func (f *fakeKube) ListPods(_ context.Context, ns, selector string) ([]corev1.Pod, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := strings.TrimPrefix(selector, labelCluster+"=")
	c, ok := f.objs[key(ClusterGVR, ns, name)]
	if !ok || !managed(c) {
		return nil, nil
	}
	pod := func(n string, phase corev1.PodPhase, role string) corev1.Pod {
		labels := map[string]string{labelCluster: name}
		if role != "" {
			labels[labelJobRole] = role
		}
		return corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: n, Namespace: ns, Labels: labels},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "postgres"}}},
			Status:     corev1.PodStatus{Phase: phase},
		}
	}
	var out []corev1.Pod
	for i := 0; i < f.failedRecoveries; i++ {
		out = append(out, pod(fmt.Sprintf("%s-1-full-recovery-f%d", name, i), corev1.PodFailed, jobRoleRecovery))
	}
	phase, _, _ := unstructured.NestedString(c.Object, "status", "phase")
	if phase == phaseHealthy {
		out = append(out, pod(name+"-1-full-recovery-ok", corev1.PodSucceeded, jobRoleRecovery), pod(name+"-1", corev1.PodRunning, ""))
	} else if f.failedRecoveries == 0 {
		out = append(out, pod(name+"-1-full-recovery-ok", corev1.PodRunning, jobRoleRecovery))
	}
	return out, nil
}

func (f *fakeKube) ListEvents(context.Context, string) ([]corev1.Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]corev1.Event(nil), f.events...), nil
}

func (f *fakeKube) Logs(_ context.Context, _, pod, container string, _ bool, _ int64) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if strings.Contains(pod, "full-recovery") && container == recoveryContainer {
		return io.NopCloser(strings.NewReader(f.recoveryLog)), nil
	}
	return io.NopCloser(strings.NewReader("a log line of " + pod + "/" + container + "\n")), nil
}

func (f *fakeKube) GetSecret(_ context.Context, ns, name string) (*corev1.Secret, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// The operator creates these for a drill Cluster.
	if c, ok := f.objs[key(ClusterGVR, ns, strings.TrimSuffix(strings.TrimSuffix(name, "-superuser"), "-ca"))]; ok && managed(c) {
		return &corev1.Secret{Data: map[string][]byte{"username": []byte("postgres"), "password": []byte("pw"), "ca.crt": []byte("PEM")}}, nil
	}
	if s, ok := f.secrets[ns+"/"+name]; ok {
		return s, nil
	}
	return nil, fmt.Errorf("%w: secret %s/%s", ErrNotFound, ns, name)
}

func (f *fakeKube) GetConfigMap(_ context.Context, ns, name string) (*corev1.ConfigMap, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c, ok := f.cms[ns+"/"+name]; ok {
		return c.DeepCopy(), nil
	}
	return nil, fmt.Errorf("%w: configmap %s/%s", ErrNotFound, ns, name)
}

func (f *fakeKube) CreateConfigMap(_ context.Context, cm *corev1.ConfigMap) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cms[cm.Namespace+"/"+cm.Name] = cm.DeepCopy()
	return nil
}

func (f *fakeKube) UpdateConfigMap(_ context.Context, cm *corev1.ConfigMap) error {
	return f.CreateConfigMap(context.Background(), cm)
}

func (f *fakeKube) ReclaimPolicy(_ context.Context, sc string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reclaim[sc], nil
}

func (f *fakeKube) createdKinds() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := append([]string(nil), f.created...)
	sort.Strings(out)
	return out
}

// fakeProber serves canned databases.
type fakeProber struct {
	inRecovery bool
	dbs        map[string][]Table // rows in Table.Estimate double as the exact count
	label      string
	labelErr   error
}

type fakeSession struct {
	p  *fakeProber
	db string
}

func (p *fakeProber) Connect(_ context.Context, db string) (Session, error) {
	if _, ok := p.dbs[db]; !ok && db != "postgres" {
		return nil, fmt.Errorf("database %q does not exist", db)
	}
	return &fakeSession{p: p, db: db}, nil
}

func (s *fakeSession) InRecovery(context.Context) (bool, error) { return s.p.inRecovery, nil }
func (s *fakeSession) Databases(context.Context) ([]string, error) {
	out := []string{"postgres"}
	for db := range s.p.dbs {
		out = append(out, db)
	}
	sort.Strings(out)
	return out, nil
}
func (s *fakeSession) Tables(context.Context) ([]Table, error) { return s.p.dbs[s.db], nil }
func (s *fakeSession) CountRows(_ context.Context, t Table) (int64, error) {
	for _, x := range s.p.dbs[s.db] {
		if x == t {
			return x.Estimate, nil
		}
	}
	return 0, fmt.Errorf("no table %s", t)
}
func (s *fakeSession) BackupLabel(context.Context) (string, error) {
	if s.p.labelErr != nil {
		return "", s.p.labelErr
	}
	return s.p.label, nil
}
func (s *fakeSession) Version(context.Context) (string, error) { return "17.6", nil }
func (s *fakeSession) Close(context.Context) error             { return nil }

func qty(s string) resource.Quantity { return resource.MustParse(s) }
