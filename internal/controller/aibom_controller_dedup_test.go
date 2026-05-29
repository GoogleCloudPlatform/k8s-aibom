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

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	aibomv1alpha1 "github.com/GoogleCloudPlatform/k8s-aibom/api/v1alpha1"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/sink"
)

// TestIntegration_FastReconcile_DedupsCosmeticThenReemitsSubstantive locks
// both halves of the input-hash dedup property:
//
//  1. Cosmetic spec change (an annotation the scraper doesn't extract)
//     produces a fast-path reconcile: ObservedGeneration advances,
//     LastReconciled is bumped, sink is NOT re-invoked, BOMHash and
//     InputHash are unchanged, and the Ready condition's
//     LastTransitionTime is NOT touched.
//
//  2. Substantive spec change (a new image tag) produces a full
//     reconcile: sink IS re-invoked, BOMHash and InputHash change.
//
// Either half failing in isolation would be a regression:
//   - Half 1 catches over-dedup-by-accident: if dedup never fires, the
//     sink is called on every reconcile and the original GKE smoke-test
//     bug returns.
//   - Half 2 catches over-dedup-by-design: if dedup fires when it
//     shouldn't, customers stop getting fresh BOMs on real changes.
//
// The integration runs against envtest so the K8s API-server-driven
// reconcile pacing is more realistic than a synthetic test loop.
func TestIntegration_FastReconcile_DedupsCosmeticThenReemitsSubstantive(t *testing.T) {
	rs := &recordingSink{name: "recording", url: "gs://test-bucket/path.json"}
	env := startEnvTestWithSinks(t, []sink.Sink{rs})
	ctx := context.Background()

	nsName := "dedup-test"
	depName := "vllm-dedup"
	mustCreateOptedInNamespace(t, env.k8sClient, ctx, nsName)
	mustCreate(t, env.k8sClient, ctx, vllmDeployment(nsName, depName))

	aibomKey := types.NamespacedName{
		Name:      "apps-deployment-" + depName,
		Namespace: nsName,
	}

	// ---------- Phase A: initial reconcile ----------
	var first aibomv1alpha1.AIBOM
	eventually(t, 30*time.Second, 200*time.Millisecond, func() error {
		if err := env.k8sClient.Get(ctx, aibomKey, &first); err != nil {
			return err
		}
		if first.Status.InputHash == "" {
			return errors.New("InputHash not yet populated")
		}
		if rs.emitCount() != 1 {
			return fmt.Errorf("initial emit count = %d, want 1", rs.emitCount())
		}
		return nil
	})

	initialInputHash := first.Status.InputHash
	initialBOMHash := first.Status.BOMHash
	if initialBOMHash == "" {
		t.Fatal("Phase A: BOMHash is empty after initial reconcile")
	}
	if first.Status.LastReconciled == nil {
		t.Fatal("Phase A: LastReconciled is nil")
	}
	initialLastReconciled := first.Status.LastReconciled.Time
	initialReadyTransition := mustFindCondition(t, first.Status.Conditions, aibomv1alpha1.ConditionReady).LastTransitionTime

	// ---------- Phase B: cosmetic change → dedup fast path ----------
	// Annotation key is intentionally NOT under model.k8saibom.dev/ so the
	// scraper doesn't pick it up as a model claim. BOMInputs is
	// therefore unchanged → input hash matches → dedup hits.
	//
	// metav1.Time wire-serializes at second precision. Two reconciles
	// within the same second produce identical Status.LastReconciled
	// values even though in-memory times differ; sleep past the
	// second boundary so the "LastReconciled advanced" assertion is
	// observable. Not a controller behavior — a test-side
	// accommodation for K8s's standard time type.
	time.Sleep(1100 * time.Millisecond)

	mustUpdateDeploymentWithAnnotation(t, env.k8sClient, ctx, depName, nsName,
		"k8s-aibom.test/cosmetic", "1")

	// Wait directly for the dedup reconcile to fire by polling on
	// LastReconciled advancement. K8s does NOT bump metadata.generation
	// for metadata.annotations updates, so polling on ObservedGeneration
	// (a previous version of this test) was racy — the loop could exit
	// against the Phase A Status before the dedup reconcile had fired,
	// then the subsequent LastReconciled-advanced assertion happened to
	// pass only because of fortunate scheduling. The LastReconciled
	// signal is the deterministic one: the dedup reconcile's
	// Status().Update is exactly what advances it.
	var afterCosmetic aibomv1alpha1.AIBOM
	eventually(t, 30*time.Second, 200*time.Millisecond, func() error {
		if err := env.k8sClient.Get(ctx, aibomKey, &afterCosmetic); err != nil {
			return err
		}
		if afterCosmetic.Status.LastReconciled == nil {
			return errors.New("LastReconciled nil after cosmetic change")
		}
		if !afterCosmetic.Status.LastReconciled.Time.After(initialLastReconciled) {
			return fmt.Errorf("LastReconciled (%v) not yet after initial (%v); waiting for dedup reconcile",
				afterCosmetic.Status.LastReconciled.Time, initialLastReconciled)
		}
		return nil
	})

	// Phase B assertions:
	if got := rs.emitCount(); got != 1 {
		t.Errorf("Phase B: emit count = %d after cosmetic change, want 1 (dedup must hit)", got)
	}
	if afterCosmetic.Status.InputHash != initialInputHash {
		t.Errorf("Phase B: InputHash changed on cosmetic change: %q -> %q",
			initialInputHash, afterCosmetic.Status.InputHash)
	}
	if afterCosmetic.Status.BOMHash != initialBOMHash {
		t.Errorf("Phase B: BOMHash changed on cosmetic change: %q -> %q",
			initialBOMHash, afterCosmetic.Status.BOMHash)
	}
	if afterCosmetic.Status.LastReconciled == nil ||
		!afterCosmetic.Status.LastReconciled.Time.After(initialLastReconciled) {
		t.Errorf("Phase B: LastReconciled did not advance: %v -> %v",
			initialLastReconciled, afterCosmetic.Status.LastReconciled)
	}
	ready2 := mustFindCondition(t, afterCosmetic.Status.Conditions, aibomv1alpha1.ConditionReady)
	if !ready2.LastTransitionTime.Time.Equal(initialReadyTransition.Time) {
		t.Errorf("Phase B: Ready.LastTransitionTime changed on dedup (should not change without a status transition): %v -> %v",
			initialReadyTransition, ready2.LastTransitionTime)
	}

	// ---------- Phase C: substantive change → full reconcile ----------
	// Image tag change → new image.reference property → new BOMInputs →
	// new InputHash → no dedup → re-emit.
	mustUpdateDeploymentImage(t, env.k8sClient, ctx, depName, nsName, "vllm/vllm-openai:v0.5.0")

	var afterSubstantive aibomv1alpha1.AIBOM
	eventually(t, 30*time.Second, 200*time.Millisecond, func() error {
		if got := rs.emitCount(); got < 2 {
			return fmt.Errorf("emit count = %d, want 2 (sink should be re-invoked on substantive change)", got)
		}
		if err := env.k8sClient.Get(ctx, aibomKey, &afterSubstantive); err != nil {
			return err
		}
		if afterSubstantive.Status.InputHash == initialInputHash {
			return errors.New("InputHash unchanged after substantive change")
		}
		return nil
	})

	// Phase C assertions:
	if afterSubstantive.Status.InputHash == initialInputHash {
		t.Errorf("Phase C: InputHash unchanged on substantive change: %q", afterSubstantive.Status.InputHash)
	}
	if afterSubstantive.Status.BOMHash == initialBOMHash {
		t.Errorf("Phase C: BOMHash unchanged on substantive change: %q", afterSubstantive.Status.BOMHash)
	}
	if rs.emitCount() < 2 {
		t.Errorf("Phase C: emit count = %d, want >= 2", rs.emitCount())
	}
}

// ---------- helpers ----------

// mustFindCondition returns the named condition or fails the test.
func mustFindCondition(t *testing.T, conds []metav1.Condition, condType string) metav1.Condition {
	t.Helper()
	for _, c := range conds {
		if c.Type == condType {
			return c
		}
	}
	t.Fatalf("condition %q not found among: %+v", condType, conds)
	return metav1.Condition{}
}

func mustUpdateDeploymentWithAnnotation(t *testing.T, c client.Client, ctx context.Context, name, namespace, annKey, annVal string) {
	t.Helper()
	var dep appsv1.Deployment
	if err := c.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, &dep); err != nil {
		t.Fatalf("get deployment for cosmetic update: %v", err)
	}
	if dep.Annotations == nil {
		dep.Annotations = map[string]string{}
	}
	dep.Annotations[annKey] = annVal
	if err := c.Update(ctx, &dep); err != nil {
		t.Fatalf("update deployment with cosmetic annotation: %v", err)
	}
}

func mustUpdateDeploymentImage(t *testing.T, c client.Client, ctx context.Context, name, namespace, newImage string) {
	t.Helper()
	var dep appsv1.Deployment
	if err := c.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, &dep); err != nil {
		t.Fatalf("get deployment for substantive update: %v", err)
	}
	if len(dep.Spec.Template.Spec.Containers) == 0 {
		t.Fatal("deployment has no containers")
	}
	dep.Spec.Template.Spec.Containers[0].Image = newImage
	if err := c.Update(ctx, &dep); err != nil {
		t.Fatalf("update deployment image: %v", err)
	}
}
