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

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	aibomv1beta1 "github.com/GoogleCloudPlatform/k8s-aibom/api/v1beta1"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/bom"
	configpkg "github.com/GoogleCloudPlatform/k8s-aibom/internal/config"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/scraper"
)

// erroringScraper returns the same matched inputs on every scrape,
// with extraction errors while errs is set: an unchanged spec that
// keeps producing the same non-fatal errors.
type erroringScraper struct {
	errs []error
}

func (s *erroringScraper) Name() string                            { return "erroring.spec" }
func (s *erroringScraper) HandlesKind(_ scraper.WorkloadKind) bool { return true }
func (s *erroringScraper) Scrape(_ context.Context, _ scraper.Workload, _ *scraper.InferenceConfig) (*scraper.BOMInputs, error) {
	return &scraper.BOMInputs{
		ScraperName: s.Name(),
		Confidence:  scraper.ConfidenceInferred,
		Components: []scraper.Component{{
			Type:       scraper.ComponentApplication,
			Name:       "vllm",
			Confidence: scraper.ConfidenceInferred,
			Evidence:   scraper.Evidence{Source: scraper.SourceImagePattern, Locator: "spec.template.spec.containers[0].image"},
		}},
		Errors: s.errs,
	}, nil
}

// Extraction errors are not part of the input hash, so from the second
// reconcile on an unchanged workload takes the dedup fast path. The
// fast path must still count the errors, or ConsecutiveErrors stays at
// 1 and Stale never reaches staleThresholdReconciles. Once the errors
// are gone, the fast path must clear the Stale it set.
func TestReconcile_ExtractionErrorsCountedOnFastPath(t *testing.T) {
	ctx := context.Background()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:   "ai-team",
		Labels: map[string]string{configpkg.DefaultNamespaceOptInLabel: "true"},
	}}
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "ai-team", UID: "uid-app", Generation: 1},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "vllm/vllm-openai:v0.6.3"}}},
			},
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(newConfigFakeScheme(t)).
		WithObjects(ns, dep).
		WithStatusSubresource(&aibomv1beta1.AIBOM{}).
		Build()

	sc := &erroringScraper{errs: []error{&scraper.ExtractionError{
		EvidenceSource: scraper.SourceCRDField,
		Message:        "spec.roles[0]: not an object; skipped",
	}}}
	snap := configpkg.DefaultSnapshot()
	threshold := int(configpkg.DefaultStaleThresholdReconciles)
	r := &DeploymentReconciler{WorkloadReconciler: WorkloadReconciler{
		Client:            c,
		Scheme:            c.Scheme(),
		Scraper:           sc,
		BOMBuilder:        bom.NewBuilder(),
		StatusBuilder:     NewStatusBuilder(),
		ConfigStore:       configpkg.NewStore(snap),
		ControllerName:    "k8s-aibom",
		ControllerVersion: "0.1.0-test",
	}}
	reconcile := func() aibomv1beta1.AIBOMStatus {
		t.Helper()
		if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ai-team", Name: "app"}}); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		var got aibomv1beta1.AIBOM
		if err := c.Get(ctx, types.NamespacedName{Namespace: "ai-team", Name: AIBOMNameForDeployment(dep)}, &got); err != nil {
			t.Fatalf("get AIBOM: %v", err)
		}
		return got.Status
	}

	var st aibomv1beta1.AIBOMStatus
	for i := 1; i <= threshold; i++ {
		st = reconcile()
		if st.ConsecutiveErrors != int32(i) {
			t.Fatalf("reconcile %d: ConsecutiveErrors = %d, want %d", i, st.ConsecutiveErrors, i)
		}
	}
	stale := apimeta.FindStatusCondition(st.Conditions, aibomv1beta1.ConditionStale)
	if stale == nil || stale.Status != metav1.ConditionTrue || stale.Reason != "ExtractionErrorsPersistent" {
		t.Fatalf("after %d reconciles with extraction errors: Stale = %+v, want True/ExtractionErrorsPersistent", threshold, stale)
	}

	// The spec is fixed in a way that leaves the inputs unchanged:
	// still the fast path, and the errors are gone.
	sc.errs = nil
	st = reconcile()
	if st.ConsecutiveErrors != 0 {
		t.Errorf("after the errors cleared: ConsecutiveErrors = %d, want 0", st.ConsecutiveErrors)
	}
	stale = apimeta.FindStatusCondition(st.Conditions, aibomv1beta1.ConditionStale)
	if stale == nil || stale.Status != metav1.ConditionFalse {
		t.Errorf("after the errors cleared: Stale = %+v, want False", stale)
	}
}
