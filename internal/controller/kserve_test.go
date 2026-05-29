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
	"errors"
	"fmt"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	aibomv1alpha1 "github.com/GoogleCloudPlatform/k8s-aibom/api/v1alpha1"
)

// TestIntegration_KServeInferenceServiceInOptedInNamespace_ProducesAIBOM
// is the KServe equivalent of the Deployment / StatefulSet / DaemonSet
// happy-path tests. A KServe InferenceService with declared
// modelFormat.name + storageUri produces an AIBOM CR with:
//   - Workload.Kind = "InferenceService"
//   - Summary.Runtime.Name = the modelFormat.name (Confidence: declared)
//   - At least one Model entry from storageUri
//   - Owner reference to the InferenceService
func TestIntegration_KServeInferenceServiceInOptedInNamespace_ProducesAIBOM(t *testing.T) {
	env := startEnvTest(t)
	ctx := context.Background()

	nsName := "prod-kserve"
	isvcName := "llama-isvc"

	mustCreateOptedInNamespace(t, env.k8sClient, ctx, nsName)
	mustCreate(t, env.k8sClient, ctx, kserveInferenceService(nsName, isvcName,
		"pytorch", "gs://my-models/llama-3.1-8b/"))

	aibomKey := types.NamespacedName{
		Name:      AIBOMNameForWorkload("serving.kserve.io", "InferenceService", isvcName),
		Namespace: nsName,
	}
	var got aibomv1alpha1.AIBOM
	eventually(t, 30*time.Second, 200*time.Millisecond, func() error {
		if err := env.k8sClient.Get(ctx, aibomKey, &got); err != nil {
			return fmt.Errorf("get AIBOM %s: %w", aibomKey, err)
		}
		if got.Status.Summary == nil {
			return errors.New("status.summary not yet populated")
		}
		return nil
	})

	// Spec: WorkloadRef points at KServe InferenceService.
	if got.Spec.WorkloadRef.Kind != "InferenceService" {
		t.Errorf("Spec.WorkloadRef.Kind = %q, want %q", got.Spec.WorkloadRef.Kind, "InferenceService")
	}
	if got.Spec.WorkloadRef.APIVersion != "serving.kserve.io/v1beta1" {
		t.Errorf("Spec.WorkloadRef.APIVersion = %q, want %q",
			got.Spec.WorkloadRef.APIVersion, "serving.kserve.io/v1beta1")
	}
	if got.Spec.WorkloadRef.Name != isvcName {
		t.Errorf("Spec.WorkloadRef.Name = %q, want %q", got.Spec.WorkloadRef.Name, isvcName)
	}

	// Status summary
	if got.Status.Summary.Workload.Kind != "InferenceService" {
		t.Errorf("Summary.Workload.Kind = %q, want %q",
			got.Status.Summary.Workload.Kind, "InferenceService")
	}
	if got.Status.Summary.Runtime == nil {
		t.Fatal("Summary.Runtime is nil; expected pytorch from declared modelFormat.name")
	}
	if got.Status.Summary.Runtime.Name != "pytorch" {
		t.Errorf("Runtime.Name = %q, want %q", got.Status.Summary.Runtime.Name, "pytorch")
	}
	// KServe scraper sets Confidence: declared (customer-declared
	// runtime via modelFormat.name). Distinct from inference-spec's
	// inferred (pattern-matched from image). This is the auditor-
	// facing distinction documented in docs/scraper-heuristics.md.
	if got.Status.Summary.Runtime.Confidence != "declared" {
		t.Errorf("Runtime.Confidence = %q, want %q (KServe is declared, not inferred)",
			got.Status.Summary.Runtime.Confidence, "declared")
	}
	if len(got.Status.Summary.Models) == 0 {
		t.Error("expected at least one model in summary (from storageUri)")
	}

	// Owner reference is the InferenceService.
	if len(got.OwnerReferences) != 1 {
		t.Fatalf("expected 1 owner reference, got %d", len(got.OwnerReferences))
	}
	owner := got.OwnerReferences[0]
	if owner.Kind != "InferenceService" || owner.Name != isvcName {
		t.Errorf("owner reference = %+v, want InferenceService/%s", owner, isvcName)
	}
}

// TestIntegration_KServeInferenceService_NoSignal_NoAIBOM verifies the
// discovery-layer policy: a bare InferenceService with no declared
// runtime, no storageUri, no model.k8saibom.dev/* annotations produces NO
// AIBOM. The KServe scraper returns Confidence: unresolved and the
// reconciler suppresses creation.
func TestIntegration_KServeInferenceService_NoSignal_NoAIBOM(t *testing.T) {
	env := startEnvTest(t)
	ctx := context.Background()

	nsName := "prod-kserve-empty"
	isvcName := "empty-isvc"

	mustCreateOptedInNamespace(t, env.k8sClient, ctx, nsName)
	// An InferenceService with no spec.predictor.* content at all.
	u := minimalUnstructuredISVC(nsName, isvcName)
	mustCreate(t, env.k8sClient, ctx, u)

	time.Sleep(500 * time.Millisecond)
	aibomKey := types.NamespacedName{
		Name:      AIBOMNameForWorkload("serving.kserve.io", "InferenceService", isvcName),
		Namespace: nsName,
	}
	var aibom aibomv1alpha1.AIBOM
	if err := env.k8sClient.Get(ctx, aibomKey, &aibom); err == nil {
		t.Fatalf("AIBOM unexpectedly created for empty InferenceService: %+v", aibom)
	}
}

// kserveInferenceService returns a populated InferenceService
// *unstructured.Unstructured ready for creation in envtest. Sets the
// two main fields the scraper extracts.
func kserveInferenceService(namespace, name, modelFormatName, storageUri string) *unstructured.Unstructured {
	u := minimalUnstructuredISVC(namespace, name)
	_ = unstructured.SetNestedField(u.Object, modelFormatName,
		"spec", "predictor", "model", "modelFormat", "name")
	_ = unstructured.SetNestedField(u.Object, storageUri,
		"spec", "predictor", "model", "storageUri")
	return u
}

// minimalUnstructuredISVC returns a bare InferenceService with no
// predictor content. Used for the no-signal test.
func minimalUnstructuredISVC(namespace, name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("serving.kserve.io/v1beta1")
	u.SetKind("InferenceService")
	u.SetName(name)
	u.SetNamespace(namespace)
	return u
}
