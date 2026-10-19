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
	"fmt"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	aibomv1beta1 "github.com/GoogleCloudPlatform/k8s-aibom/api/v1beta1"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/config"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/scraper"
)

// Tests for status.effectiveInferenceRuntimeImagePatterns (#106 item 3):
// the status must show the patterns the workload reconcilers are
// actually matching against, which is not always the spec.

func defaultPatternsForStatus() []aibomv1beta1.RuntimeImagePattern {
	var out []aibomv1beta1.RuntimeImagePattern
	for _, p := range scraper.DefaultV1Config().RuntimeImagePatterns {
		out = append(out, aibomv1beta1.RuntimeImagePattern{Runtime: p.Runtime, Pattern: p.Pattern})
	}
	return out
}

func customPatterns() []aibomv1beta1.RuntimeImagePattern {
	return []aibomv1beta1.RuntimeImagePattern{
		{Runtime: "vllm", Pattern: `^registry\.example\.com/mirror/vllm(:|@)`},
		{Runtime: "triton", Pattern: `^registry\.example\.com/mirror/tritonserver(:|@)`},
	}
}

func assertEffectivePatterns(t *testing.T, cr *aibomv1beta1.AIBOMControllerConfig, want []aibomv1beta1.RuntimeImagePattern) {
	t.Helper()
	got := cr.Status.EffectiveInferenceRuntimeImagePatterns
	if !equality.Semantic.DeepEqual(got, want) {
		t.Errorf("status.effectiveInferenceRuntimeImagePatterns =\n  %+v\nwant\n  %+v", got, want)
	}
}

func TestReconcile_EffectivePatterns_DefaultsWhenSpecHasNone(t *testing.T) {
	r, _ := newDirectReconciler(t, validCR())
	reconcileOnce(t, r)

	got := getCR(t, r.Client, config.DefaultConfigName)
	want := defaultPatternsForStatus()
	if len(want) == 0 {
		t.Fatal("setup: compiled-in pattern list is empty")
	}
	assertEffectivePatterns(t, got, want)
}

func TestReconcile_EffectivePatterns_SpecOverride(t *testing.T) {
	cr := validCR()
	cr.Spec.Discovery.InferenceRuntimeImagePatterns = customPatterns()
	r, _ := newDirectReconciler(t, cr)
	reconcileOnce(t, r)

	assertEffectivePatterns(t, getCR(t, r.Client, config.DefaultConfigName), customPatterns())
}

// While the spec is invalid the controller keeps matching with the
// last-known-good snapshot, so status must show that list and not the
// rejected spec (whose patterns may be the very thing that failed).
func TestReconcile_EffectivePatterns_LastKnownGoodWhileInvalid(t *testing.T) {
	cr := validCR()
	cr.Spec.Discovery.InferenceRuntimeImagePatterns = customPatterns()
	r, _ := newDirectReconciler(t, cr)
	reconcileOnce(t, r)

	updated := getCR(t, r.Client, config.DefaultConfigName)
	updated.Spec.Discovery.InferenceRuntimeImagePatterns = []aibomv1beta1.RuntimeImagePattern{
		{Runtime: "vllm", Pattern: `^vllm/(`}, // does not compile
	}
	updated.Generation = 2
	if err := r.Update(context.Background(), updated); err != nil {
		t.Fatalf("update CR to invalid: %v", err)
	}
	reconcileOnce(t, r)

	got := getCR(t, r.Client, config.DefaultConfigName)
	assertCondition(t, got, aibomv1beta1.AIBOMControllerConfigConditionDegraded, metav1.ConditionTrue, aibomv1beta1.ReasonRunningOnLastKnownGood)
	assertEffectivePatterns(t, got, customPatterns())
}

func TestReconcile_EffectivePatterns_DefaultsOnFreshInvalid(t *testing.T) {
	r, _ := newDirectReconciler(t, invalidCR())
	reconcileOnce(t, r)

	assertEffectivePatterns(t, getCR(t, r.Client, config.DefaultConfigName), defaultPatternsForStatus())
}

// The compiled-in list is written into a status field that reuses the
// spec's RuntimeImagePattern validation. A default longer than those
// limits would make the API server reject every config status write.
func TestCompiledPatterns_FitStatusSchema(t *testing.T) {
	for i, p := range scraper.DefaultV1Config().RuntimeImagePatterns {
		if n := len(p.Runtime); n < 1 || n > 64 {
			t.Errorf("default pattern %d: runtime %q length %d outside [1,64]", i, p.Runtime, n)
		}
		if n := len(p.Pattern); n < 1 || n > 256 {
			t.Errorf("default pattern %d (%s): pattern length %d outside [1,256]", i, p.Runtime, n)
		}
	}
}

// Against a real API server the status write must pass the CRD's
// schema validation and keep the list in match order.
func TestReconcileEnvtest_EffectivePatternsInStatus(t *testing.T) {
	env, _, _ := startConfigEnvTest(t)

	if err := env.k8sClient.Create(env.mgrCtx, validCR()); err != nil {
		t.Fatalf("create CR: %v", err)
	}
	want := defaultPatternsForStatus()
	eventually(t, 10*time.Second, 100*time.Millisecond, func() error {
		var got aibomv1beta1.AIBOMControllerConfig
		if err := env.k8sClient.Get(env.mgrCtx, types.NamespacedName{Name: config.DefaultConfigName}, &got); err != nil {
			return err
		}
		if !equality.Semantic.DeepEqual(got.Status.EffectiveInferenceRuntimeImagePatterns, want) {
			return fmt.Errorf("effective patterns = %d entries, want %d", len(got.Status.EffectiveInferenceRuntimeImagePatterns), len(want))
		}
		return nil
	})
}
