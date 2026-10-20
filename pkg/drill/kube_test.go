// Copyright 2026 Truvity B.V.. All rights reserved.
// SPDX-License-Identifier: MIT

package drill

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

func TestKubeClient(t *testing.T) {
	ctx := context.Background()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		ClusterGVR: "ClusterList", ObjectStoreGVR: "ObjectStoreList", DatabaseGVR: "DatabaseList",
	})
	retain := corev1.PersistentVolumeReclaimRetain
	core := k8sfake.NewClientset(&storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "keep"}, ReclaimPolicy: &retain},
		&storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "plain"}})
	k := NewKubeClientFrom(dyn, core)

	_, err := k.Get(ctx, ClusterGVR, "ns", "pg")
	assert.ErrorIs(t, err, ErrNotFound)
	require.NoError(t, k.Create(ctx, ClusterGVR, managedCluster(0, map[string]any{LabelManagedBy: ManagedBy})))
	got, err := k.Get(ctx, ClusterGVR, drillNS, drillNm)
	require.NoError(t, err)
	assert.True(t, managed(got))
	l, err := k.List(ctx, ClusterGVR, drillNS)
	require.NoError(t, err)
	assert.Len(t, l, 1)
	require.NoError(t, k.Delete(ctx, ClusterGVR, drillNS, drillNm))
	require.NoError(t, k.Delete(ctx, ClusterGVR, drillNS, drillNm), "deleting what is gone is no error")

	_, err = k.GetConfigMap(ctx, "ns", "r")
	assert.ErrorIs(t, err, ErrNotFound)
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "ns"}, Data: map[string]string{"a": "1"}}
	require.NoError(t, k.CreateConfigMap(ctx, cm))
	cm.Data["a"] = "2"
	require.NoError(t, k.UpdateConfigMap(ctx, cm))
	back, err := k.GetConfigMap(ctx, "ns", "r")
	require.NoError(t, err)
	assert.Equal(t, "2", back.Data["a"])
	_, err = k.GetSecret(ctx, "ns", "s")
	assert.ErrorIs(t, err, ErrNotFound)

	for sc, want := range map[string]string{"keep": "Retain", "plain": "Delete", "missing": ""} {
		got, err := k.ReclaimPolicy(ctx, sc)
		require.NoError(t, err)
		assert.Equal(t, want, got, sc)
	}
}
