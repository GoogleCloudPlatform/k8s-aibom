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
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	aibomv1alpha1 "github.com/GoogleCloudPlatform/k8s-aibom/api/v1alpha1"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/config"
)

// TestIntegration_HotReload_SwapWebhookSink_NextReconcileGoesToNewSink
// locks the central customer-facing hot-reload promise: a CR edit
// swapping the configured webhook sink takes effect on the next
// workload reconcile, without a process restart.
//
// End-to-end chain exercised:
//
//	customer edits AIBOMControllerConfig
//	  ↓ (watch event)
//	AIBOMControllerConfigReconciler.Reconcile
//	  ↓ (loader builds snapshot via ClientSinkFactory)
//	ConfigStore.Store(newSnapshot)
//	  ↓ (next workload reconcile)
//	WorkloadReconciler.reconcileWorkload reads snap from Store
//	  ↓ (snap.ExternalSinks threaded through)
//	emitToExternalSinks POSTs to the NEW endpoint
//
// If any link in this chain regresses — Store not rotated, snapshot
// not threaded through, reconciler holding a stale pointer — this
// test fails. It's the integration counterpart to the unit-level
// load-once invariant checks.
func TestIntegration_HotReload_SwapWebhookSink_NextReconcileGoesToNewSink(t *testing.T) {
	env := startCombinedEnvTest(t)
	ctx := env.mgrCtx

	// Two httptest servers stand in for two distinct customer sinks.
	// Each one's emit counter is incremented on every POST so the
	// test can verify which one is currently receiving.
	var serverACount, serverBCount atomic.Int32
	serverA := newCountingHTTPServer(&serverACount)
	defer serverA.Close()
	serverB := newCountingHTTPServer(&serverBCount)
	defer serverB.Close()

	// Apply AIBOMControllerConfig with sink pointing at server A.
	cr := &aibomv1alpha1.AIBOMControllerConfig{
		ObjectMeta: metav1.ObjectMeta{Name: config.DefaultConfigName},
		Spec: aibomv1alpha1.AIBOMControllerConfigSpec{
			Sinks: []aibomv1alpha1.SinkConfig{{
				Name: "primary",
				Type: aibomv1alpha1.SinkTypeWebhook,
				Webhook: &aibomv1alpha1.WebhookSinkSpec{
					Endpoint: serverA.URL,
				},
			}},
		},
	}
	mustCreate(t, env.k8sClient, ctx, cr)

	// Wait for ConfigStore to reflect the CR (Source becomes config-cr
	// and the snapshot has exactly one ExternalSink).
	eventually(t, 15*time.Second, 100*time.Millisecond, func() error {
		snap := env.configStore.Load()
		if snap.Source != config.SourceConfigCR {
			return fmt.Errorf("Source = %q, want config-cr", snap.Source)
		}
		if len(snap.ExternalSinks) != 1 {
			return fmt.Errorf("len(ExternalSinks) = %d, want 1", len(snap.ExternalSinks))
		}
		return nil
	})

	// Create opted-in namespace + vllm Deployment.
	nsName := "prod-hotreload"
	depName := "vllm-hotreload"
	mustCreateOptedInNamespace(t, env.k8sClient, ctx, nsName)
	mustCreate(t, env.k8sClient, ctx, vllmDeployment(nsName, depName))

	// Wait for the first BOM to land at server A.
	eventually(t, 30*time.Second, 200*time.Millisecond, func() error {
		if serverACount.Load() < 1 {
			return errors.New("server A has not received a BOM yet")
		}
		return nil
	})

	initialA := serverACount.Load()
	initialB := serverBCount.Load()
	if initialB != 0 {
		t.Fatalf("setup invariant: server B should not have received any BOMs before the swap; got %d", initialB)
	}

	// SWAP: patch the CR to point at server B.
	var fetched aibomv1alpha1.AIBOMControllerConfig
	if err := env.k8sClient.Get(ctx, types.NamespacedName{Name: config.DefaultConfigName}, &fetched); err != nil {
		t.Fatalf("get CR for swap: %v", err)
	}
	fetched.Spec.Sinks[0].Webhook.Endpoint = serverB.URL
	if err := env.k8sClient.Update(ctx, &fetched); err != nil {
		t.Fatalf("update CR to swap sink: %v", err)
	}

	// Wait for ConfigStore to rotate. We assert on a sentinel that's
	// derivable without inspecting the sink's internal endpoint: the
	// snapshot's SourceGeneration bumps when the CR is updated.
	eventually(t, 10*time.Second, 100*time.Millisecond, func() error {
		snap := env.configStore.Load()
		if snap.SourceGeneration < 2 {
			return fmt.Errorf("SourceGeneration = %d, want >= 2 (CR was updated)", snap.SourceGeneration)
		}
		return nil
	})

	// Trigger a workload reconcile that defeats the input-hash dedup
	// fast path. Annotations under model.k8saibom.dev/* are scraped into
	// BOM Components (see scraper.extractAnnotationModels); adding one
	// changes BOMInputs and therefore the input hash. A non-scraped
	// annotation like "test/touch" would bump generation but leave the
	// hash unchanged, hitting the dedup fast path and skipping emission.
	var dep = vllmDeployment(nsName, depName) // placeholder for type
	depKey := types.NamespacedName{Namespace: nsName, Name: depName}
	if err := env.k8sClient.Get(ctx, depKey, dep); err != nil {
		t.Fatalf("get Deployment for touch: %v", err)
	}
	if dep.Spec.Template.Annotations == nil {
		dep.Spec.Template.Annotations = map[string]string{}
	}
	dep.Spec.Template.Annotations["model.k8saibom.dev/touch"] = "hotreload-test"
	if err := env.k8sClient.Update(ctx, dep); err != nil {
		t.Fatalf("touch Deployment: %v", err)
	}

	// Wait for server B to receive a BOM.
	eventually(t, 30*time.Second, 200*time.Millisecond, func() error {
		if serverBCount.Load() < 1 {
			return errors.New("server B has not received a BOM after CR swap + workload touch")
		}
		return nil
	})

	// Critical assertion: server A must not have received any
	// additional BOMs after the CR swap. The new emission goes to B,
	// not A.
	finalA := serverACount.Load()
	if finalA != initialA {
		t.Errorf("server A received %d additional BOM(s) after CR swap; want 0. "+
			"This indicates the WorkloadReconciler held a stale snapshot reference past the rotation — "+
			"the load-once invariant must be lifted to the start of every reconcile, not cached longer.",
			finalA-initialA)
	}
}

// newCountingHTTPServer returns an httptest.Server that increments
// the given counter on every POST and returns 200 OK. Used by the
// hot-reload test to verify which sink-server received emissions.
func newCountingHTTPServer(counter *atomic.Int32) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			counter.Add(1)
		}
		w.WriteHeader(http.StatusOK)
	}))
}
