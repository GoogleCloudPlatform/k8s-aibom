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

package scraper

import "context"

// Scraper is the workload-type-neutral contract for producing BOM inputs
// from a tracked workload. v1 ships InferenceSpecScraper; v2+ contributors
// add TrainingSpecScraper, AgentSpecScraper, PipelineSpecScraper, and
// EBPFScraper without modifying the core controller logic.
//
// Implementations MUST:
//
//   - Be safe for concurrent invocation (the pipeline may scrape multiple
//     workloads in parallel).
//   - Be deterministic: given identical Workload inputs (including pod
//     state), return identical BOMInputs (modulo ScrapeTimestamp). This is
//     a precondition for the hash-based input dedup in the reconciler.
//   - Treat per-attribute extraction failures as non-fatal: record them in
//     BOMInputs.Errors and continue. Return an error from Scrape only when
//     the entire scrape cannot proceed (e.g., the Workload's underlying
//     object type is unrecognized).
//   - Set BOMInputs.ScraperName to the same value as Name() and set
//     ScrapeTimestamp to the time the scrape started.
//   - Populate Evidence on every Component, Service, and attribute-bearing
//     value they emit. Auditors must be able to trace each output value to
//     its source without external context.
//
// Implementations MUST NOT assume the workload is inference. They must rely
// on Workload.Category set by the discovery layer to select their behavior.
//
// Configuration model: Scrape accepts the active *InferenceConfig as a
// per-call parameter rather than reading it from scraper-side mutable
// state. This makes the load-once invariant a property of the type
// system: the reconciler loads its snapshot at the top of
// reconcileWorkload and passes the same *InferenceConfig to every
// scraper invocation within that reconcile. A scraper CANNOT see
// configuration change mid-scrape because there is no internal pointer
// to swap. Hot-reload is the reconciler's job (load a fresh snapshot
// next reconcile); scrapers stay stateless w.r.t. configuration.
//
// Implementations that do not consume any *InferenceConfig fields
// (e.g., KServeInferenceServiceScraper, which reads declared values
// from the InferenceService CR rather than pattern-matching images)
// accept the parameter and ignore it. Keeping the interface uniform
// avoids per-scraper interface variants and means the day a scraper
// gains a need for the config (e.g., KServe pod-template scraping
// post-v1) the change is local to the scraper, not the interface.
type Scraper interface {
	// Name returns a stable identifier for this scraper, used in
	// BOMInputs.ScraperName, in provenance entries, and in logs/metrics.
	// Naming convention: lowercase, dot-separated, no version
	// (e.g., "inference.spec", "inference.ebpf", "training.spec").
	Name() string

	// HandlesKind returns true if this scraper produces BOM inputs for the
	// given workload kind. The pipeline uses this to route workloads to
	// relevant scrapers without invoking every scraper for every workload.
	HandlesKind(kind WorkloadKind) bool

	// Scrape extracts BOM inputs from the workload. Implementations must
	// honor the rules in the type-level documentation above. The cfg
	// parameter is the active inference configuration; implementations
	// MUST NOT mutate it. cfg MUST NOT be nil — the reconciler is
	// responsible for substituting a default config when its snapshot
	// is nil-shaped.
	Scrape(ctx context.Context, workload Workload, cfg *InferenceConfig) (*BOMInputs, error)
}
