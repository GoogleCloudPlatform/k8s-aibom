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
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	aibomv1alpha1 "github.com/GoogleCloudPlatform/k8s-aibom/api/v1alpha1"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/config"
)

// These tests are the integration-level counterparts to the
// state-machine unit tests in aibomcontrollerconfig_reconciler_test.go.
// They exercise the full chain — watch event → reconciler → ConfigStore
// rotation → WorkloadReconciler observes new snapshot → BOM emission
// outcome — and lock the customer-protection properties as tested
// contracts.
//
// The valid→invalid LKG test is the highest-value: it locks the
// promise that a CR typo does NOT silently swap sinks. The same
// property is asserted at the snapshot level by the Checkpoint 4
// unit test; this is the integration twin, verifying the property
// also holds with real reconcilers, real watch propagation, and the
// real ClientSinkFactory in the loop.

// ---------- Fresh start ----------

// TestIntegration_Bootstrap_NoCR_BOMLandsInStatus locks the
// fresh-start-without-CR behavior at the integration level: with no
// AIBOMControllerConfig in the cluster, ConfigStore holds defaults
// (no sinks), so a workload's BOM lands inline in the AIBOM CR status
// — no external sinks were configured to land it elsewhere.
func TestIntegration_Bootstrap_NoCR_BOMLandsInStatus(t *testing.T) {
	env := startCombinedEnvTest(t)
	ctx := env.mgrCtx

	// No AIBOMControllerConfig CR. ConfigStore is at compiled defaults.
	snap := env.configStore.Load()
	if snap.Source != config.SourceCompiledDefaults {
		t.Fatalf("setup: snap.Source = %q, want compiled-defaults", snap.Source)
	}
	if len(snap.ExternalSinks) != 0 {
		t.Fatalf("setup: ExternalSinks = %d, want 0", len(snap.ExternalSinks))
	}

	// Workload should still produce an AIBOM CR (no sinks ≠ no BOM).
	nsName := "prod-no-cr"
	depName := "vllm-no-cr"
	mustCreateOptedInNamespace(t, env.k8sClient, ctx, nsName)
	mustCreate(t, env.k8sClient, ctx, vllmDeployment(nsName, depName))

	aibomKey := types.NamespacedName{Name: "apps-deployment-" + depName, Namespace: nsName}
	var got aibomv1alpha1.AIBOM
	eventually(t, 30*time.Second, 200*time.Millisecond, func() error {
		if err := env.k8sClient.Get(ctx, aibomKey, &got); err != nil {
			return err
		}
		if got.Status.BOMDocument == nil || got.Status.BOMDocument.Inline == nil {
			return errors.New("BOMDocument.Inline not yet populated")
		}
		return nil
	})

	// No external sink: BOM is inline in the CR status. ExternalBOMRef
	// is nil.
	if got.Status.BOMDocument.External != nil {
		t.Errorf("ExternalBOMRef is %+v, want nil (no sinks configured)",
			got.Status.BOMDocument.External)
	}
	if got.Status.BOMDocument.Truncated {
		t.Errorf("BOM truncated, but with no size pressure it should be inline")
	}
}

// ---------- Fresh start: valid CR ----------

// TestIntegration_Bootstrap_ValidCR_BOMLandsInSinks locks the
// fresh-start-with-valid-CR behavior: a CR applied BEFORE the workload
// is created results in the BOM being delivered to the configured
// sink (not just stashed inline).
func TestIntegration_Bootstrap_ValidCR_BOMLandsInSinks(t *testing.T) {
	env := startCombinedEnvTest(t)
	ctx := env.mgrCtx

	var sinkCount atomic.Int32
	server := newCountingHTTPServer(&sinkCount)
	defer server.Close()

	cr := &aibomv1alpha1.AIBOMControllerConfig{
		ObjectMeta: metav1.ObjectMeta{Name: config.DefaultConfigName},
		Spec: aibomv1alpha1.AIBOMControllerConfigSpec{
			Sinks: []aibomv1alpha1.SinkConfig{{
				Name: "primary",
				Type: aibomv1alpha1.SinkTypeWebhook,
				Webhook: &aibomv1alpha1.WebhookSinkSpec{
					Endpoint: server.URL,
				},
			}},
		},
	}
	mustCreate(t, env.k8sClient, ctx, cr)

	// Wait for the config reconciler to rotate the Store.
	eventually(t, 15*time.Second, 100*time.Millisecond, func() error {
		snap := env.configStore.Load()
		if snap.Source != config.SourceConfigCR {
			return fmt.Errorf("Source = %q, want config-cr", snap.Source)
		}
		return nil
	})

	// Workload after the CR is in place.
	nsName := "prod-valid-cr"
	depName := "vllm-valid-cr"
	mustCreateOptedInNamespace(t, env.k8sClient, ctx, nsName)
	mustCreate(t, env.k8sClient, ctx, vllmDeployment(nsName, depName))

	eventually(t, 30*time.Second, 200*time.Millisecond, func() error {
		if sinkCount.Load() < 1 {
			return errors.New("sink has not received the BOM yet")
		}
		return nil
	})

	// CR's Ready condition must be True/ConfigLoaded.
	var fetched aibomv1alpha1.AIBOMControllerConfig
	if err := env.k8sClient.Get(ctx, types.NamespacedName{Name: config.DefaultConfigName}, &fetched); err != nil {
		t.Fatalf("get CR: %v", err)
	}
	readyCond := meta.FindStatusCondition(fetched.Status.Conditions, aibomv1alpha1.AIBOMControllerConfigConditionReady)
	if readyCond == nil || readyCond.Status != metav1.ConditionTrue {
		t.Errorf("Ready condition = %+v, want Status=True", readyCond)
	}
	if readyCond != nil && readyCond.Reason != aibomv1alpha1.ReasonConfigLoaded {
		t.Errorf("Ready Reason = %q, want %q", readyCond.Reason, aibomv1alpha1.ReasonConfigLoaded)
	}
}

// ---------- Fresh start: invalid CR ----------

// TestIntegration_Bootstrap_InvalidCR_ReadyFalse locks the
// fresh-start-with-invalid-CR behavior: ConfigStore stays on defaults
// (no LKG to preserve), Ready=False/ConfigInvalid, Degraded=True/
// RunningOnDefaults.
func TestIntegration_Bootstrap_InvalidCR_ReadyFalse(t *testing.T) {
	env := startCombinedEnvTest(t)
	ctx := env.mgrCtx

	// Invalid CR: Type=GCS but GCS body absent. Loader-detectable
	// shape error.
	cr := &aibomv1alpha1.AIBOMControllerConfig{
		ObjectMeta: metav1.ObjectMeta{Name: config.DefaultConfigName},
		Spec: aibomv1alpha1.AIBOMControllerConfigSpec{
			Sinks: []aibomv1alpha1.SinkConfig{{
				Name: "broken-gcs",
				Type: aibomv1alpha1.SinkTypeGCS,
				// GCS body intentionally nil.
			}},
		},
	}
	mustCreate(t, env.k8sClient, ctx, cr)

	// Wait for the config reconciler to observe and patch status.
	eventually(t, 15*time.Second, 100*time.Millisecond, func() error {
		var fetched aibomv1alpha1.AIBOMControllerConfig
		if err := env.k8sClient.Get(ctx, types.NamespacedName{Name: config.DefaultConfigName}, &fetched); err != nil {
			return err
		}
		readyCond := meta.FindStatusCondition(fetched.Status.Conditions, aibomv1alpha1.AIBOMControllerConfigConditionReady)
		if readyCond == nil {
			return errors.New("Ready condition not yet set")
		}
		if readyCond.Status != metav1.ConditionFalse {
			return fmt.Errorf("Ready Status = %q, want False", readyCond.Status)
		}
		if readyCond.Reason != aibomv1alpha1.ReasonConfigInvalid {
			return fmt.Errorf("Ready Reason = %q, want %q", readyCond.Reason, aibomv1alpha1.ReasonConfigInvalid)
		}
		return nil
	})

	// ConfigStore must remain on compiled defaults (no LKG to fall back to).
	snap := env.configStore.Load()
	if snap.Source != config.SourceCompiledDefaults {
		t.Errorf("Source = %q, want compiled-defaults (fresh-start invalid; no LKG)", snap.Source)
	}

	// Degraded condition: True / RunningOnDefaults.
	var fetched aibomv1alpha1.AIBOMControllerConfig
	_ = env.k8sClient.Get(ctx, types.NamespacedName{Name: config.DefaultConfigName}, &fetched)
	degraded := meta.FindStatusCondition(fetched.Status.Conditions, aibomv1alpha1.AIBOMControllerConfigConditionDegraded)
	if degraded == nil || degraded.Status != metav1.ConditionTrue {
		t.Errorf("Degraded = %+v, want Status=True", degraded)
	}
	if degraded != nil && degraded.Reason != aibomv1alpha1.ReasonRunningOnDefaults {
		t.Errorf("Degraded Reason = %q, want %q", degraded.Reason, aibomv1alpha1.ReasonRunningOnDefaults)
	}
}

// ---------- Running: valid → invalid (the high-value LKG test) ----------

// TestIntegration_Bootstrap_ValidToInvalid_PreservesLKG is the
// integration twin of the Checkpoint-4 unit test that locks the
// customer-protection property. The unit test asserts at the
// snapshot-content level; THIS test asserts the property end-to-end
// by verifying the BOM continues to land at the customer's previously
// configured sink after a CR edit makes the spec invalid.
//
// Without LKG preservation, a typo in the CR would silently swap the
// customer's sink to "nowhere" — auditors would see SinkFailed
// conditions appearing across the fleet for what looks like a small
// edit.
func TestIntegration_Bootstrap_ValidToInvalid_PreservesLKG(t *testing.T) {
	env := startCombinedEnvTest(t)
	ctx := env.mgrCtx

	var sinkCount atomic.Int32
	server := newCountingHTTPServer(&sinkCount)
	defer server.Close()

	// 1. Apply valid CR pointing at the test sink.
	cr := &aibomv1alpha1.AIBOMControllerConfig{
		ObjectMeta: metav1.ObjectMeta{Name: config.DefaultConfigName},
		Spec: aibomv1alpha1.AIBOMControllerConfigSpec{
			Sinks: []aibomv1alpha1.SinkConfig{{
				Name: "primary",
				Type: aibomv1alpha1.SinkTypeWebhook,
				Webhook: &aibomv1alpha1.WebhookSinkSpec{
					Endpoint: server.URL,
				},
			}},
		},
	}
	mustCreate(t, env.k8sClient, ctx, cr)
	eventually(t, 15*time.Second, 100*time.Millisecond, func() error {
		if env.configStore.Load().Source != config.SourceConfigCR {
			return errors.New("Store not yet rotated to config-cr")
		}
		return nil
	})

	// 2. Create workload, wait for first BOM to land at the sink.
	nsName := "prod-lkg"
	depName := "vllm-lkg"
	mustCreateOptedInNamespace(t, env.k8sClient, ctx, nsName)
	mustCreate(t, env.k8sClient, ctx, vllmDeployment(nsName, depName))
	eventually(t, 30*time.Second, 200*time.Millisecond, func() error {
		if sinkCount.Load() < 1 {
			return errors.New("sink has not received the BOM yet")
		}
		return nil
	})
	initialCount := sinkCount.Load()

	// 3. Patch the CR to an INVALID spec. A different sink with a
	// shape error (Type=Webhook but no webhook body) is the most
	// realistic typo class: customer intended to change something
	// but malformed it.
	var fetched aibomv1alpha1.AIBOMControllerConfig
	if err := env.k8sClient.Get(ctx, types.NamespacedName{Name: config.DefaultConfigName}, &fetched); err != nil {
		t.Fatalf("get CR for invalidation: %v", err)
	}
	fetched.Spec.Sinks = []aibomv1alpha1.SinkConfig{{
		Name: "broken-after-edit",
		Type: aibomv1alpha1.SinkTypeWebhook,
		// Webhook body intentionally nil — shape error.
	}}
	if err := env.k8sClient.Update(ctx, &fetched); err != nil {
		t.Fatalf("update CR to invalid: %v", err)
	}

	// 4. Wait for config reconciler to observe the invalid CR and
	// mark Degraded=True/RunningOnLastKnownGood. This is the signal
	// that the LKG path was taken.
	eventually(t, 15*time.Second, 100*time.Millisecond, func() error {
		var got aibomv1alpha1.AIBOMControllerConfig
		if err := env.k8sClient.Get(ctx, types.NamespacedName{Name: config.DefaultConfigName}, &got); err != nil {
			return err
		}
		degraded := meta.FindStatusCondition(got.Status.Conditions, aibomv1alpha1.AIBOMControllerConfigConditionDegraded)
		if degraded == nil || degraded.Status != metav1.ConditionTrue {
			return errors.New("Degraded not yet True")
		}
		if degraded.Reason != aibomv1alpha1.ReasonRunningOnLastKnownGood {
			return fmt.Errorf("Degraded Reason = %q, want %q (LKG path NOT taken — customer's sinks were silently lost)",
				degraded.Reason, aibomv1alpha1.ReasonRunningOnLastKnownGood)
		}
		return nil
	})

	// 5. Critical assertion: the stored snapshot is still the prior
	// valid spec, not compiled defaults. ExternalSinks count == 1
	// (the LKG sink), not 0 (defaults).
	snap := env.configStore.Load()
	if snap.Source != config.SourceLastKnownGood {
		t.Errorf("Source = %q, want last-known-good", snap.Source)
	}
	if len(snap.ExternalSinks) != 1 {
		t.Errorf("len(ExternalSinks) = %d, want 1 (the LKG sink). "+
			"If this is 0, the customer's sink was silently dropped on a typo — "+
			"the property this test exists to guard.",
			len(snap.ExternalSinks))
	}

	// 6. Trigger a workload reconcile that defeats input-hash dedup;
	// verify the BOM still lands at the original sink (LKG in effect).
	dep := vllmDeployment(nsName, depName)
	depKey := types.NamespacedName{Namespace: nsName, Name: depName}
	if err := env.k8sClient.Get(ctx, depKey, dep); err != nil {
		t.Fatalf("get Deployment for touch: %v", err)
	}
	if dep.Spec.Template.Annotations == nil {
		dep.Spec.Template.Annotations = map[string]string{}
	}
	dep.Spec.Template.Annotations["model.k8saibom.dev/touch"] = "lkg-test"
	if err := env.k8sClient.Update(ctx, dep); err != nil {
		t.Fatalf("touch Deployment: %v", err)
	}
	eventually(t, 30*time.Second, 200*time.Millisecond, func() error {
		if sinkCount.Load() <= initialCount {
			return errors.New("sink has not received the post-invalidation BOM yet")
		}
		return nil
	})
	// Final check: sink count increased past initialCount, meaning
	// the LKG snapshot's sink is still being used for fan-out.
	if sinkCount.Load() <= initialCount {
		t.Fatalf("sinkCount = %d, want > %d (LKG sink should still receive BOMs)",
			sinkCount.Load(), initialCount)
	}
}

// ---------- Running: CR deleted ----------

// TestIntegration_Bootstrap_CRDeleted_RevertsToDefaults locks the
// approved CR-deletion behavior: explicit customer deletion reverts
// the Store to compiled defaults rather than retaining LKG. The
// Checkpoint 4 design discussion decided this is the right answer
// because deletion = explicit signal; retaining LKG after deletion
// would surprise operators.
func TestIntegration_Bootstrap_CRDeleted_RevertsToDefaults(t *testing.T) {
	env := startCombinedEnvTest(t)
	ctx := env.mgrCtx

	var sinkCount atomic.Int32
	server := newCountingHTTPServer(&sinkCount)
	defer server.Close()

	cr := &aibomv1alpha1.AIBOMControllerConfig{
		ObjectMeta: metav1.ObjectMeta{Name: config.DefaultConfigName},
		Spec: aibomv1alpha1.AIBOMControllerConfigSpec{
			Sinks: []aibomv1alpha1.SinkConfig{{
				Name: "soon-to-be-deleted",
				Type: aibomv1alpha1.SinkTypeWebhook,
				Webhook: &aibomv1alpha1.WebhookSinkSpec{
					Endpoint: server.URL,
				},
			}},
		},
	}
	mustCreate(t, env.k8sClient, ctx, cr)
	eventually(t, 15*time.Second, 100*time.Millisecond, func() error {
		if env.configStore.Load().Source != config.SourceConfigCR {
			return errors.New("Store not yet rotated to config-cr")
		}
		return nil
	})

	// Delete the CR.
	if err := env.k8sClient.Delete(ctx, cr); err != nil {
		t.Fatalf("delete CR: %v", err)
	}

	// Wait for the Store to revert to compiled defaults (NOT LKG —
	// per the approved Checkpoint-4 design).
	eventually(t, 15*time.Second, 100*time.Millisecond, func() error {
		snap := env.configStore.Load()
		if snap.Source != config.SourceCompiledDefaults {
			return fmt.Errorf("Source = %q, want compiled-defaults (deletion is explicit; LKG retention would surprise)",
				snap.Source)
		}
		if len(snap.ExternalSinks) != 0 {
			return fmt.Errorf("ExternalSinks = %d, want 0", len(snap.ExternalSinks))
		}
		return nil
	})

	// CR no longer exists.
	var lookup aibomv1alpha1.AIBOMControllerConfig
	err := env.k8sClient.Get(ctx, types.NamespacedName{Name: config.DefaultConfigName}, &lookup)
	if !apierrors.IsNotFound(err) {
		t.Errorf("post-delete Get = %v, want NotFound", err)
	}
}
