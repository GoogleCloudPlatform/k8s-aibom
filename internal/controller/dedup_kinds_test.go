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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	aibomv1alpha1 "github.com/GoogleCloudPlatform/k8s-aibom/api/v1alpha1"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/sink"
)

// dedupTestCase captures the per-kind specifics for
// runDedupCosmeticThenSubstantive. Each per-kind dedup test
// constructs a dedupTestCase and hands it to the runner.
//
// The factory and mutator closures capture the test context (env,
// namespace, workload name); the runner is then kind-agnostic.
type dedupTestCase struct {
	kind      string
	aibomName string
	namespace string
	name      string

	// newWorkload returns the typed K8s object to create.
	newWorkload func() client.Object

	// addCosmeticAnnotation mutates the workload to add a
	// non-model annotation. The scraper does not extract such
	// annotations, so BOMInputs is unchanged → dedup must fire.
	addCosmeticAnnotation func()

	// applySubstantiveChange mutates the workload in a way that
	// CHANGES BOMInputs. For Deployment / StatefulSet / DaemonSet this
	// is a container image tag change; for KServe it is a
	// spec.predictor.model.storageUri change. The closure is
	// kind-specific; the runner just calls it and asserts the
	// resulting re-emit.
	applySubstantiveChange func()

	// fetchGeneration returns the workload's current Generation.
	fetchGeneration func() int64
}

// runDedupCosmeticThenSubstantive is the kind-agnostic body of the
// three-phase dedup test (also used by the original
// TestIntegration_FastReconcile_DedupsCosmeticThenReemitsSubstantive
// for the Deployment path). Locks both halves of the dedup property:
// cosmetic changes do not re-emit, substantive changes do.
//
// The structure of this test is part of the project's regression-guard
// surface; changes to the assertion order or to the locked Status
// field contract are API-surface changes per the same rule documented
// on TestIntegration_ExternalSink_FailurePath_CRDStatusStillUpdated.
func runDedupCosmeticThenSubstantive(t *testing.T, env *envTestEnv, rs *recordingSink, tc dedupTestCase) {
	t.Helper()
	ctx := context.Background()

	mustCreateOptedInNamespace(t, env.k8sClient, ctx, tc.namespace)
	mustCreate(t, env.k8sClient, ctx, tc.newWorkload())

	aibomKey := types.NamespacedName{Name: tc.aibomName, Namespace: tc.namespace}

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
		t.Fatal("Phase A: BOMHash empty")
	}
	if first.Status.LastReconciled == nil {
		t.Fatal("Phase A: LastReconciled nil")
	}
	initialLastReconciled := first.Status.LastReconciled.Time
	initialReadyTransition := mustFindCondition(t, first.Status.Conditions, aibomv1alpha1.ConditionReady).LastTransitionTime

	// metav1.Time has second precision on the wire; sleep past the
	// second boundary so LastReconciled-advanced is observable. See
	// docs/phase-deferrals.md (Phase 14, metav1.MicroTime note).
	time.Sleep(1100 * time.Millisecond)

	// ---------- Phase B: cosmetic change → dedup fast path ----------
	tc.addCosmeticAnnotation()
	_ = tc.fetchGeneration() // sanity: ensure workload still exists

	// Wait directly for the dedup reconcile to fire (LastReconciled
	// advances past initialLastReconciled). K8s does not always bump
	// metadata.generation for metadata.annotations updates, so polling
	// on ObservedGeneration is racy. Polling on LastReconciled is the
	// direct, deterministic signal: the dedup reconcile's Status().Update
	// is exactly what advances LastReconciled.
	var afterCosmetic aibomv1alpha1.AIBOM
	eventually(t, 30*time.Second, 200*time.Millisecond, func() error {
		if err := env.k8sClient.Get(ctx, aibomKey, &afterCosmetic); err != nil {
			return err
		}
		if afterCosmetic.Status.LastReconciled == nil {
			return errors.New("LastReconciled nil after cosmetic change")
		}
		if !afterCosmetic.Status.LastReconciled.After(initialLastReconciled) {
			return fmt.Errorf("LastReconciled (%v) not yet after initial (%v); waiting for dedup reconcile",
				afterCosmetic.Status.LastReconciled.Time, initialLastReconciled)
		}
		return nil
	})
	if got := rs.emitCount(); got != 1 {
		t.Errorf("Phase B (%s): emit count = %d after cosmetic change, want 1 (dedup must hit)", tc.kind, got)
	}
	if afterCosmetic.Status.InputHash != initialInputHash {
		t.Errorf("Phase B (%s): InputHash changed on cosmetic change: %q -> %q",
			tc.kind, initialInputHash, afterCosmetic.Status.InputHash)
	}
	if afterCosmetic.Status.BOMHash != initialBOMHash {
		t.Errorf("Phase B (%s): BOMHash changed on cosmetic change: %q -> %q",
			tc.kind, initialBOMHash, afterCosmetic.Status.BOMHash)
	}
	if afterCosmetic.Status.LastReconciled == nil ||
		!afterCosmetic.Status.LastReconciled.After(initialLastReconciled) {
		t.Errorf("Phase B (%s): LastReconciled did not advance: %v -> %v",
			tc.kind, initialLastReconciled, afterCosmetic.Status.LastReconciled)
	}
	ready2 := mustFindCondition(t, afterCosmetic.Status.Conditions, aibomv1alpha1.ConditionReady)
	if !ready2.LastTransitionTime.Time.Equal(initialReadyTransition.Time) {
		t.Errorf("Phase B (%s): Ready.LastTransitionTime changed on dedup (should not change without a status transition): %v -> %v",
			tc.kind, initialReadyTransition, ready2.LastTransitionTime)
	}

	// ---------- Phase C: substantive change → full reconcile ----------
	tc.applySubstantiveChange()

	var afterSubstantive aibomv1alpha1.AIBOM
	eventually(t, 30*time.Second, 200*time.Millisecond, func() error {
		if got := rs.emitCount(); got < 2 {
			return fmt.Errorf("emit count = %d, want 2", got)
		}
		if err := env.k8sClient.Get(ctx, aibomKey, &afterSubstantive); err != nil {
			return err
		}
		if afterSubstantive.Status.InputHash == initialInputHash {
			return errors.New("InputHash unchanged after substantive change")
		}
		return nil
	})
	if afterSubstantive.Status.InputHash == initialInputHash {
		t.Errorf("Phase C (%s): InputHash unchanged on substantive change: %q",
			tc.kind, afterSubstantive.Status.InputHash)
	}
	if afterSubstantive.Status.BOMHash == initialBOMHash {
		t.Errorf("Phase C (%s): BOMHash unchanged on substantive change: %q",
			tc.kind, afterSubstantive.Status.BOMHash)
	}
	if rs.emitCount() < 2 {
		t.Errorf("Phase C (%s): emit count = %d, want >= 2", tc.kind, rs.emitCount())
	}
}

// TestIntegration_FastReconcile_StatefulSet_Dedup verifies the dedup
// property holds for StatefulSet workloads. The dedup mechanism is
// kind-neutral by design (it operates on BOMInputs, not on the
// workload type), but locking that via per-kind tests prevents a
// future scraper or reconciler change from accidentally breaking
// dedup for a specific kind.
func TestIntegration_FastReconcile_StatefulSet_Dedup(t *testing.T) {
	rs := &recordingSink{name: "recording-ss", url: "gs://test/ss.json"}
	env := startEnvTestWithSinks(t, []sink.Sink{rs})
	ctx := context.Background()

	nsName := "dedup-ss"
	ssName := "vllm-dedup-ss"

	tc := dedupTestCase{
		kind:      "StatefulSet",
		aibomName: AIBOMNameForWorkload("apps", "StatefulSet", ssName),
		namespace: nsName,
		name:      ssName,
		newWorkload: func() client.Object {
			return vllmStatefulSet(nsName, ssName)
		},
		addCosmeticAnnotation: func() {
			var ss appsv1.StatefulSet
			if err := env.k8sClient.Get(ctx, types.NamespacedName{Name: ssName, Namespace: nsName}, &ss); err != nil {
				t.Fatalf("get statefulset: %v", err)
			}
			if ss.Annotations == nil {
				ss.Annotations = map[string]string{}
			}
			ss.Annotations["k8s-aibom.test/cosmetic"] = "1"
			if err := env.k8sClient.Update(ctx, &ss); err != nil {
				t.Fatalf("update statefulset annotation: %v", err)
			}
		},
		applySubstantiveChange: func() {
			var ss appsv1.StatefulSet
			if err := env.k8sClient.Get(ctx, types.NamespacedName{Name: ssName, Namespace: nsName}, &ss); err != nil {
				t.Fatalf("get statefulset for image change: %v", err)
			}
			ss.Spec.Template.Spec.Containers[0].Image = "vllm/vllm-openai:v0.5.0"
			if err := env.k8sClient.Update(ctx, &ss); err != nil {
				t.Fatalf("update statefulset image: %v", err)
			}
		},
		fetchGeneration: func() int64 {
			var ss appsv1.StatefulSet
			if err := env.k8sClient.Get(ctx, types.NamespacedName{Name: ssName, Namespace: nsName}, &ss); err != nil {
				t.Fatalf("get statefulset for generation: %v", err)
			}
			return ss.Generation
		},
	}
	runDedupCosmeticThenSubstantive(t, env, rs, tc)
}

// TestIntegration_FastReconcile_DaemonSet_Dedup verifies the dedup
// property holds for DaemonSet workloads.
func TestIntegration_FastReconcile_DaemonSet_Dedup(t *testing.T) {
	rs := &recordingSink{name: "recording-ds", url: "gs://test/ds.json"}
	env := startEnvTestWithSinks(t, []sink.Sink{rs})
	ctx := context.Background()

	nsName := "dedup-ds"
	dsName := "vllm-dedup-ds"

	tc := dedupTestCase{
		kind:      "DaemonSet",
		aibomName: AIBOMNameForWorkload("apps", "DaemonSet", dsName),
		namespace: nsName,
		name:      dsName,
		newWorkload: func() client.Object {
			return vllmDaemonSet(nsName, dsName)
		},
		addCosmeticAnnotation: func() {
			var ds appsv1.DaemonSet
			if err := env.k8sClient.Get(ctx, types.NamespacedName{Name: dsName, Namespace: nsName}, &ds); err != nil {
				t.Fatalf("get daemonset: %v", err)
			}
			if ds.Annotations == nil {
				ds.Annotations = map[string]string{}
			}
			ds.Annotations["k8s-aibom.test/cosmetic"] = "1"
			if err := env.k8sClient.Update(ctx, &ds); err != nil {
				t.Fatalf("update daemonset annotation: %v", err)
			}
		},
		applySubstantiveChange: func() {
			var ds appsv1.DaemonSet
			if err := env.k8sClient.Get(ctx, types.NamespacedName{Name: dsName, Namespace: nsName}, &ds); err != nil {
				t.Fatalf("get daemonset for image change: %v", err)
			}
			ds.Spec.Template.Spec.Containers[0].Image = "vllm/vllm-openai:v0.5.0"
			if err := env.k8sClient.Update(ctx, &ds); err != nil {
				t.Fatalf("update daemonset image: %v", err)
			}
		},
		fetchGeneration: func() int64 {
			var ds appsv1.DaemonSet
			if err := env.k8sClient.Get(ctx, types.NamespacedName{Name: dsName, Namespace: nsName}, &ds); err != nil {
				t.Fatalf("get daemonset for generation: %v", err)
			}
			return ds.Generation
		},
	}
	runDedupCosmeticThenSubstantive(t, env, rs, tc)
}

// TestIntegration_FastReconcile_KServeInferenceService_Dedup verifies
// the dedup property holds for KServe InferenceService workloads.
//
// Cosmetic change: add a non-model annotation. The scraper does not
// extract such annotations → BOMInputs unchanged → dedup hits.
//
// Substantive change: update spec.predictor.model.storageUri to a new
// URI. The scraper extracts storageUri as a model identity → BOMInputs
// changes → dedup does NOT fire.
//
// This locks the dedup property for the KServe path. The dedup
// mechanism is kind-neutral (it operates on BOMInputs hash, not on
// the workload type), but per-kind tests prevent a future
// KServe-specific change from accidentally breaking it.
func TestIntegration_FastReconcile_KServeInferenceService_Dedup(t *testing.T) {
	rs := &recordingSink{name: "recording-isvc", url: "gs://test/isvc.json"}
	env := startEnvTestWithSinks(t, []sink.Sink{rs})
	ctx := context.Background()

	nsName := "dedup-isvc"
	isvcName := "vllm-dedup-isvc"

	tc := dedupTestCase{
		kind:      "InferenceService",
		aibomName: AIBOMNameForWorkload("serving.kserve.io", "InferenceService", isvcName),
		namespace: nsName,
		name:      isvcName,
		newWorkload: func() client.Object {
			return kserveInferenceService(nsName, isvcName,
				"pytorch", "gs://my-models/llama-3.1-8b/")
		},
		addCosmeticAnnotation: func() {
			u := minimalUnstructuredISVC(nsName, isvcName)
			if err := env.k8sClient.Get(ctx, types.NamespacedName{Name: isvcName, Namespace: nsName}, u); err != nil {
				t.Fatalf("get InferenceService: %v", err)
			}
			anns := u.GetAnnotations()
			if anns == nil {
				anns = map[string]string{}
			}
			anns["k8s-aibom.test/cosmetic"] = "1"
			u.SetAnnotations(anns)
			if err := env.k8sClient.Update(ctx, u); err != nil {
				t.Fatalf("update InferenceService annotation: %v", err)
			}
		},
		applySubstantiveChange: func() {
			u := minimalUnstructuredISVC(nsName, isvcName)
			if err := env.k8sClient.Get(ctx, types.NamespacedName{Name: isvcName, Namespace: nsName}, u); err != nil {
				t.Fatalf("get InferenceService for substantive change: %v", err)
			}
			_ = unstructured.SetNestedField(u.Object, "gs://my-models/different-model/",
				"spec", "predictor", "model", "storageUri")
			if err := env.k8sClient.Update(ctx, u); err != nil {
				t.Fatalf("update InferenceService storageUri: %v", err)
			}
		},
		fetchGeneration: func() int64 {
			u := minimalUnstructuredISVC(nsName, isvcName)
			if err := env.k8sClient.Get(ctx, types.NamespacedName{Name: isvcName, Namespace: nsName}, u); err != nil {
				t.Fatalf("get InferenceService for generation: %v", err)
			}
			return u.GetGeneration()
		},
	}
	runDedupCosmeticThenSubstantive(t, env, rs, tc)
}
