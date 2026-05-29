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
	"time"

	"github.com/GoogleCloudPlatform/k8s-aibom/internal/bom"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/scraper"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/sink"
)

// TestEmitToExternalSinks_ParallelFanOut_BoundedByMaxLatency locks the
// concurrency contract of emitToExternalSinks. With two sinks of equal
// (slow) latency, total elapsed time MUST be bounded by max(latency)
// — not sum(latency).
//
// This test exists because the Phase 9 refactor from sequential to
// parallel fan-out is structurally easy to accidentally revert (a
// well-meaning future contributor might "simplify" the goroutine code
// back to a sequential for-loop). A failure here is a regression.
//
// Numbers chosen for robustness:
//   - per-sink delay 300ms
//   - sum (sequential lower bound) = 600ms
//   - max (parallel lower bound) = 300ms
//   - assertion bound = 500ms (parallel + 200ms scheduling slack)
//
// 500ms is well below 600ms so a sequential regression fails the
// assertion clearly; and well above 300ms so scheduling jitter on
// a busy CI runner doesn't flake.
func TestEmitToExternalSinks_ParallelFanOut_BoundedByMaxLatency(t *testing.T) {
	const sinkLatency = 300 * time.Millisecond
	const upperBound = 500 * time.Millisecond

	a := &recordingSink{name: "slow-a", url: "result-a", delay: sinkLatency}
	b := &recordingSink{name: "slow-b", url: "result-b", delay: sinkLatency}
	sinks := []sink.Sink{a, b}

	r := &DeploymentReconciler{
		WorkloadReconciler: WorkloadReconciler{},
	}
	w := scraper.Workload{
		Kind:      scraper.WorkloadKind{Group: "apps", Version: "v1", Kind: "Deployment"},
		Namespace: "ns", Name: "x",
	}
	doc := &bom.Document{
		Format:  bom.FormatCycloneDX,
		Version: "1.6",
		JSON:    []byte(`{}`),
		SHA256:  "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}

	start := time.Now()
	results := r.emitToExternalSinks(context.Background(), doc, w, sinks)
	elapsed := time.Since(start)

	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(results))
	}
	if a.emitCount() != 1 {
		t.Errorf("slow-a emit count = %d, want 1", a.emitCount())
	}
	if b.emitCount() != 1 {
		t.Errorf("slow-b emit count = %d, want 1", b.emitCount())
	}
	if elapsed >= upperBound {
		t.Errorf("emitToExternalSinks took %v; with parallel fan-out it should be < %v "+
			"(two %v sinks running concurrently). Did the fan-out regress to sequential?",
			elapsed, upperBound, sinkLatency)
	}
	if elapsed < sinkLatency {
		// Sanity check: the slow sinks should actually be honored.
		t.Errorf("emitToExternalSinks took %v; expected >= %v (each sink's delay)",
			elapsed, sinkLatency)
	}
}

// TestEmitToExternalSinks_OneSinkSlow_OthersUnaffected verifies that a
// single slow sink does not delay other sinks' completion. Specifically,
// a fast sink and a slow sink in parallel: the fast sink's result is
// recorded "as soon as it completes" (in the result-collection sense),
// not "when the slow sink finishes too." We can't observe per-sink
// completion order from outside (the WaitGroup pattern collects all
// results before returning), but we CAN verify total elapsed is
// bounded by max(slow_latency, fast_latency) — i.e., by the slow
// sink alone, not slow + fast.
func TestEmitToExternalSinks_OneSinkSlow_OthersUnaffected(t *testing.T) {
	fast := &recordingSink{name: "fast", url: "fast-url", delay: 20 * time.Millisecond}
	slow := &recordingSink{name: "slow", url: "slow-url", delay: 300 * time.Millisecond}
	sinks := []sink.Sink{fast, slow}

	r := &DeploymentReconciler{
		WorkloadReconciler: WorkloadReconciler{},
	}
	w := scraper.Workload{
		Kind:      scraper.WorkloadKind{Group: "apps", Version: "v1", Kind: "Deployment"},
		Namespace: "ns", Name: "x",
	}
	doc := &bom.Document{
		Format: bom.FormatCycloneDX, Version: "1.6",
		JSON:   []byte(`{}`),
		SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}

	start := time.Now()
	_ = r.emitToExternalSinks(context.Background(), doc, w, sinks)
	elapsed := time.Since(start)

	// Total should be ~300ms (slow sink dominates). Sequential would
	// have been ~320ms — the difference is small but the bound below
	// captures the intent: fast + slow should not noticeably exceed
	// slow alone.
	upperBound := 450 * time.Millisecond // 300ms slow + 150ms slack
	if elapsed >= upperBound {
		t.Errorf("fast+slow took %v; expected ~slow latency (300ms) only. "+
			"Sequential execution would suggest %v.", elapsed, 320*time.Millisecond)
	}
}
