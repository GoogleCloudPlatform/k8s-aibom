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
	"testing"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	aibomv1alpha1 "github.com/GoogleCloudPlatform/k8s-aibom/api/v1alpha1"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/scraper"
)

func failureStatusRequest() WorkloadReconcileRequest {
	return WorkloadReconcileRequest{
		AIBOMName: "apps-deployment-vllm",
		Workload: scraper.Workload{
			Kind:      scraper.WorkloadKind{Group: "apps", Version: "v1", Kind: "Deployment"},
			Namespace: "ai-team",
			Name:      "vllm",
		},
		Generation: 7,
	}
}

// A scrape/build failure must flip Ready=False on an EXISTING AIBOM —
// preserving prior status fields — so the failure is observable in
// status, not only in logs and requeue backoff.
func TestPersistFailureStatus_FlipsReadyFalseOnExisting(t *testing.T) {
	existing := &aibomv1alpha1.AIBOM{
		ObjectMeta: metav1.ObjectMeta{Name: "apps-deployment-vllm", Namespace: "ai-team"},
		Status: aibomv1alpha1.AIBOMStatus{
			ConsecutiveErrors: 1,
			InputHash:         "prior-hash-preserved",
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(newConfigFakeScheme(t)).
		WithObjects(existing).
		WithStatusSubresource(&aibomv1alpha1.AIBOM{}).
		Build()
	r := &WorkloadReconciler{Client: c}

	r.persistFailureStatus(context.Background(), failureStatusRequest(), "BuildFailed", "BOM build failed: boom")

	var got aibomv1alpha1.AIBOM
	if err := c.Get(context.Background(), types.NamespacedName{Name: "apps-deployment-vllm", Namespace: "ai-team"}, &got); err != nil {
		t.Fatalf("get AIBOM: %v", err)
	}
	cond := apimeta.FindStatusCondition(got.Status.Conditions, aibomv1alpha1.ConditionReady)
	if cond == nil {
		t.Fatal("Ready condition not written")
	}
	if cond.Status != metav1.ConditionFalse {
		t.Errorf("Ready status = %s, want False", cond.Status)
	}
	if cond.Reason != "BuildFailed" {
		t.Errorf("Ready reason = %q, want BuildFailed", cond.Reason)
	}
	if cond.ObservedGeneration != 7 {
		t.Errorf("Ready observedGeneration = %d, want 7", cond.ObservedGeneration)
	}
	if got.Status.ConsecutiveErrors != 2 {
		t.Errorf("ConsecutiveErrors = %d, want 2 (incremented)", got.Status.ConsecutiveErrors)
	}
	// Prior status fields survive: a failed cycle makes inventory
	// stale, not wrong.
	if got.Status.InputHash != "prior-hash-preserved" {
		t.Errorf("InputHash = %q, want preserved prior value", got.Status.InputHash)
	}
}

// The failure path must never CREATE an AIBOM: a workload with no
// published inventory gets one only via the success path (detection may
// not apply to it at all).
func TestPersistFailureStatus_NeverCreates(t *testing.T) {
	c := fake.NewClientBuilder().
		WithScheme(newConfigFakeScheme(t)).
		WithStatusSubresource(&aibomv1alpha1.AIBOM{}).
		Build()
	r := &WorkloadReconciler{Client: c}

	r.persistFailureStatus(context.Background(), failureStatusRequest(), "ScrapeFailed", "scrape failed: boom")

	var list aibomv1alpha1.AIBOMList
	if err := c.List(context.Background(), &list); err != nil {
		t.Fatalf("list AIBOMs: %v", err)
	}
	if len(list.Items) != 0 {
		t.Errorf("AIBOMs created = %d, want 0", len(list.Items))
	}
}
