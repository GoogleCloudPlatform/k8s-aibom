/*
Copyright 2026 Google LLC

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	aibomv1beta1 "github.com/GoogleCloudPlatform/k8s-aibom/api/v1beta1"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/bom"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/config"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/scraper"
)

// Repeated reconciles of an unchanged deployment (as from pending pod
// churn) should refresh status only when the last touch is due.
func TestReconcile_ThrottlesUnchangedStatusWrites(t *testing.T) {
	ctx := t.Context()
	dep := vllmDeployment("models", "vllm")
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := start
	var writes []time.Duration
	c := fake.NewClientBuilder().
		WithScheme(newConfigFakeScheme(t)).
		WithObjects(dep).
		WithStatusSubresource(&aibomv1beta1.AIBOM{}).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourceUpdate: func(ctx context.Context, cl client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
				if aibom, ok := obj.(*aibomv1beta1.AIBOM); ok && sub == "status" {
					writes = append(writes, now.Sub(start))
					ready := mustFindCondition(t, aibom.Status.Conditions, aibomv1beta1.ConditionReady)
					if !ready.LastTransitionTime.Time.Equal(start) {
						t.Errorf("unchanged Ready condition transitioned at %v, want %v", ready.LastTransitionTime, start)
					}
				}
				return cl.SubResource(sub).Update(ctx, obj, opts...)
			},
		}).Build()
	mustCreateOptedInNamespace(t, c, ctx, dep.Namespace)
	r := &DeploymentReconciler{WorkloadReconciler: WorkloadReconciler{
		Client: c, Scheme: c.Scheme(),
		Scraper:       scraper.NewInferenceSpecScraper(nil),
		BOMBuilder:    bom.NewBuilder(),
		StatusBuilder: &StatusBuilder{Now: func() time.Time { return now }},
		ConfigStore:   config.NewStore(config.DefaultSnapshot()),
	}}
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(dep)}
	for tick := 0; tick <= 16; tick++ {
		now = start.Add(time.Duration(tick) * 30 * time.Second)
		if result, err := r.Reconcile(ctx, req); err != nil || !result.IsZero() {
			t.Fatalf("reconcile at %v: result = %v, err = %v", now, result, err)
		}
	}
	if want := []time.Duration{0, 5 * time.Minute}; !slices.Equal(writes, want) {
		t.Errorf("status writes at %v, want %v", writes, want)
	}
}

func TestReconcile_NewPodDigestInsideStatusTouchInterval(t *testing.T) {
	ctx := t.Context()
	ss := rollupStatefulSet("models", "vllm")
	ss.UID = "vllm-uid"
	c := fake.NewClientBuilder().WithScheme(newConfigFakeScheme(t)).
		WithStatusSubresource(&aibomv1beta1.AIBOM{}).WithObjects(ss).Build()
	mustCreateOptedInNamespace(t, c, ctx, ss.Namespace)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r := &StatefulSetReconciler{WorkloadReconciler: WorkloadReconciler{
		Client:        c,
		Scheme:        c.Scheme(),
		Scraper:       scraper.NewInferenceSpecScraper(nil),
		BOMBuilder:    bom.NewBuilder(),
		StatusBuilder: &StatusBuilder{Now: func() time.Time { return now }},
		ConfigStore:   config.NewStore(config.DefaultSnapshot()),
	}}
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(ss)}
	key := client.ObjectKey{Namespace: ss.Namespace, Name: AIBOMNameForWorkload("apps", "StatefulSet", ss.Name)}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	before, err := inlineBOMOf(c, ctx, key)
	if err != nil {
		t.Fatalf("initial record: %v", err)
	}
	if strings.Contains(before, rollupDigest) {
		t.Fatal("initial inventory already contains the new pod digest")
	}

	// A new pod's image digest changes inventory without bumping the workload generation.
	now = now.Add(30 * time.Second)
	createPodWithDigest(t, c, ctx, ss.Namespace, "vllm-0", ss.Spec.Selector.MatchLabels,
		controllerRef("StatefulSet", ss.Name, ss.UID), rollupDigest)
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	document, err := inlineBOMOf(c, ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(document, rollupDigest) {
		t.Error("published inventory does not contain the new pod digest")
	}
}
