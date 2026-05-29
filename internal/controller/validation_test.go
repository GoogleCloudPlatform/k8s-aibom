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
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	aibomv1alpha1 "github.com/GoogleCloudPlatform/k8s-aibom/api/v1alpha1"
)

// These tests exercise the API-server-enforced validation on the
// AIBOM CRD. They run against envtest (which carries a real
// kube-apiserver) so the assertions are server-side validation
// behavior, not Go-side struct validation.
//
// What these tests intentionally do NOT test:
//   - Status field shape (status is controller-written; no markers).
//   - Field defaults (we don't set any +kubebuilder:default markers).
//   - Field length compliance for fields the reconciler itself
//     populates (already exercised via the integration tests).

func TestValidation_RejectsMissingWorkloadRefName(t *testing.T) {
	env := startEnvTest(t)
	ctx := context.Background()
	mustCreateOptedInNamespace(t, env.k8sClient, ctx, "v-ns-1")

	// AIBOM with workloadRef.name="" should be rejected by API server.
	aibom := &aibomv1alpha1.AIBOM{
		ObjectMeta: metav1.ObjectMeta{Name: "bad-1", Namespace: "v-ns-1"},
		Spec: aibomv1alpha1.AIBOMSpec{
			WorkloadRef: aibomv1alpha1.WorkloadRef{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Name:       "", // missing
			},
			BOMFormat:      "CycloneDX",
			BOMSpecVersion: "1.6",
		},
	}
	err := env.k8sClient.Create(ctx, aibom)
	if err == nil {
		t.Fatal("API server accepted AIBOM with empty workloadRef.name")
	}
	if !strings.Contains(err.Error(), "name") {
		t.Errorf("error should mention 'name' field; got: %v", err)
	}
}

func TestValidation_RejectsUnknownBOMFormat(t *testing.T) {
	env := startEnvTest(t)
	ctx := context.Background()
	mustCreateOptedInNamespace(t, env.k8sClient, ctx, "v-ns-2")

	aibom := &aibomv1alpha1.AIBOM{
		ObjectMeta: metav1.ObjectMeta{Name: "bad-2", Namespace: "v-ns-2"},
		Spec: aibomv1alpha1.AIBOMSpec{
			WorkloadRef: aibomv1alpha1.WorkloadRef{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Name:       "x",
			},
			BOMFormat:      "SPDX", // not in the v1 enum
			BOMSpecVersion: "3.0",
		},
	}
	err := env.k8sClient.Create(ctx, aibom)
	if err == nil {
		t.Fatal("API server accepted AIBOM with unknown bomFormat")
	}
	if !strings.Contains(err.Error(), "bomFormat") {
		t.Errorf("error should mention 'bomFormat'; got: %v", err)
	}
}

func TestValidation_RejectsBadBOMSpecVersionPattern(t *testing.T) {
	env := startEnvTest(t)
	ctx := context.Background()
	mustCreateOptedInNamespace(t, env.k8sClient, ctx, "v-ns-3")

	aibom := &aibomv1alpha1.AIBOM{
		ObjectMeta: metav1.ObjectMeta{Name: "bad-3", Namespace: "v-ns-3"},
		Spec: aibomv1alpha1.AIBOMSpec{
			WorkloadRef: aibomv1alpha1.WorkloadRef{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Name:       "x",
			},
			BOMFormat:      "CycloneDX",
			BOMSpecVersion: "v1.6", // leading 'v' violates pattern ^\d+\.\d+$
		},
	}
	err := env.k8sClient.Create(ctx, aibom)
	if err == nil {
		t.Fatal("API server accepted AIBOM with malformed bomSpecVersion")
	}
	if !strings.Contains(err.Error(), "bomSpecVersion") {
		t.Errorf("error should mention 'bomSpecVersion'; got: %v", err)
	}
}

func TestValidation_RejectsWorkloadRefMutation(t *testing.T) {
	env := startEnvTest(t)
	ctx := context.Background()
	mustCreateOptedInNamespace(t, env.k8sClient, ctx, "v-ns-4")

	// Initial valid AIBOM.
	aibom := &aibomv1alpha1.AIBOM{
		ObjectMeta: metav1.ObjectMeta{Name: "imm-1", Namespace: "v-ns-4"},
		Spec: aibomv1alpha1.AIBOMSpec{
			WorkloadRef: aibomv1alpha1.WorkloadRef{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Name:       "originally-vllm",
			},
			BOMFormat:      "CycloneDX",
			BOMSpecVersion: "1.6",
		},
	}
	if err := env.k8sClient.Create(ctx, aibom); err != nil {
		t.Fatalf("create initial AIBOM: %v", err)
	}

	// Try to mutate workloadRef.name — must be rejected.
	var got aibomv1alpha1.AIBOM
	if err := env.k8sClient.Get(ctx, types.NamespacedName{
		Name: "imm-1", Namespace: "v-ns-4",
	}, &got); err != nil {
		t.Fatal(err)
	}
	got.Spec.WorkloadRef.Name = "renamed-vllm"
	err := env.k8sClient.Update(ctx, &got)
	if err == nil {
		t.Fatal("API server allowed mutation of immutable workloadRef")
	}
	if !strings.Contains(err.Error(), "immutable") && !strings.Contains(err.Error(), "workloadRef") {
		t.Errorf("error should mention immutability or workloadRef; got: %v", err)
	}
}

func TestValidation_AcceptsValidAIBOM(t *testing.T) {
	// Counter-test: a fully-valid AIBOM is accepted. Without this,
	// the rejection tests above would pass even if the API server
	// were misconfigured to reject every AIBOM.
	env := startEnvTest(t)
	ctx := context.Background()
	mustCreateOptedInNamespace(t, env.k8sClient, ctx, "v-ns-5")

	aibom := &aibomv1alpha1.AIBOM{
		ObjectMeta: metav1.ObjectMeta{Name: "good-1", Namespace: "v-ns-5"},
		Spec: aibomv1alpha1.AIBOMSpec{
			WorkloadRef: aibomv1alpha1.WorkloadRef{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Name:       "vllm",
			},
			BOMFormat:      "CycloneDX",
			BOMSpecVersion: "1.6",
		},
	}
	if err := env.k8sClient.Create(ctx, aibom); err != nil {
		t.Fatalf("API server rejected valid AIBOM: %v", err)
	}
}
