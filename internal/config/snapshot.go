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

// Package config holds the controller's runtime configuration model:
// the in-memory Snapshot of AIBOMControllerConfig, the atomic Store
// that hosts the live snapshot for the reconcile path, and the Loader
// that translates a CR into a Snapshot (with the fallback semantics
// documented in the project memory entry "AIBOMControllerConfig v1
// behavior").
//
// The package is the boundary where the customer-facing CR contract
// (api/v1alpha1.AIBOMControllerConfig) meets the controller's
// internal pipeline (scraper.InferenceConfig, sink.Sink, etc.).
// Customer-facing types should NEVER appear directly in scraper or
// sink internals — the Loader is responsible for translation.
package config

import (
	"sync/atomic"
	"time"

	"k8s.io/apimachinery/pkg/labels"

	"github.com/GoogleCloudPlatform/k8s-aibom/internal/scraper"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/sink"
)

// SnapshotSource identifies where the active Snapshot came from. The
// reconciler consults this to decide which condition Reason to set
// on AIBOMControllerConfig.status (RunningOnDefaults vs.
// RunningOnLastKnownGood vs. ConfigLoaded).
type SnapshotSource string

const (
	// SourceConfigCR means the snapshot was constructed from a
	// successfully-parsed AIBOMControllerConfig CR.
	SourceConfigCR SnapshotSource = "config-cr"

	// SourceCompiledDefaults means the snapshot is the compiled-in
	// safe defaults. Used when the CR is missing OR when the CR is
	// invalid and no last-known-good snapshot exists yet. Both
	// converge on the same Snapshot kind by design — see project
	// memory "AIBOMControllerConfig v1 behavior".
	SourceCompiledDefaults SnapshotSource = "compiled-defaults"

	// SourceLastKnownGood means the snapshot is the previous
	// successfully-loaded Snapshot, retained because a later
	// reconcile encountered an invalid CR. Distinct from
	// SourceCompiledDefaults: the controller is running on
	// CUSTOMER configuration, just not the latest version.
	SourceLastKnownGood SnapshotSource = "last-known-good"
)

// Snapshot is an immutable, point-in-time runtime configuration. Once
// placed in a Store, the contained fields MUST NOT be mutated; the
// reconcile path may hold the snapshot pointer across a full reconcile
// cycle expecting stable values. The Loader produces fresh Snapshots
// on each CR change rather than mutating in-place.
//
// Fields:
//   - Patterns: inference-runtime detection ruleset for InferenceSpecScraper.
//   - InlineThreshold: BOM-inline size threshold (bytes).
//   - NamespaceSelector: pre-materialized labels.Selector for namespace
//     opt-in; the Loader produces this from the CR's metav1.LabelSelector
//     so consumers don't repeat materialization on every reconcile.
//   - ExternalSinks: ordered set of external sinks. May be empty
//     (CRD-status-only mode).
//   - Source: where this snapshot came from; see SnapshotSource.
//   - SourceGeneration: AIBOMControllerConfig.metadata.generation at
//     load time, or 0 for compiled-defaults.
//   - LoadedAt: when this snapshot was constructed (controller wall
//     clock).
type Snapshot struct {
	Patterns                 *scraper.InferenceConfig
	InlineThreshold          int64
	StaleThresholdReconciles int32
	NamespaceSelector        labels.Selector
	ExternalSinks            []sink.Sink
	Source                   SnapshotSource
	SourceGeneration         int64
	LoadedAt                 time.Time
}

// Store hosts the current Snapshot pointer with atomic semantics.
// Reads via Load() are lock-free; writes via Store() are atomic-pointer
// swaps. Safe for concurrent use.
//
// The reconcile path reads the snapshot once at the start of each
// Reconcile call and holds the pointer for the duration. Concurrent
// hot-reload swaps the pointer; in-flight reconciles complete with
// their snapshot, next reconcile uses the new one. This is the
// hot-reload contract documented in the Phase 12 proposal.
type Store struct {
	current atomic.Pointer[Snapshot]
}

// NewStore constructs a Store with the given initial Snapshot. The
// initial value MUST NOT be nil; the contract is that Store.Load()
// always returns a usable snapshot.
func NewStore(initial *Snapshot) *Store {
	if initial == nil {
		// Defensive: never let a Store host nil. Falling back to
		// compiled defaults is the right answer per the fresh-start
		// behavior.
		initial = DefaultSnapshot()
	}
	s := &Store{}
	s.current.Store(initial)
	return s
}

// Load returns the current snapshot. Lock-free; safe for concurrent
// use. The returned pointer is stable; the snapshot fields MUST NOT
// be mutated by the caller.
func (s *Store) Load() *Snapshot {
	return s.current.Load()
}

// Store atomically replaces the current snapshot. Callers MUST pass
// a fully-constructed Snapshot (no later mutation). Nil is rejected
// to maintain the load-always-returns-usable contract.
func (s *Store) Store(snap *Snapshot) {
	if snap == nil {
		return // defensive; documented in NewStore godoc
	}
	s.current.Store(snap)
}

// MarkAsLastKnownGood returns a shallow copy of the given Snapshot with
// Source rewritten to SourceLastKnownGood. Used by
// AIBOMControllerConfigReconciler on the valid→invalid transition to
// retain the customer's last working config rather than silently
// reverting to compiled defaults — which would, e.g., swap their
// configured sinks on a transient CR typo.
//
// The function returns a NEW Snapshot pointer rather than mutating the
// original; Snapshot immutability is the contract that lets the
// reconcile path hold a pointer across a full reconcile cycle. The
// SourceGeneration field is preserved from the original so condition
// messages can still report which generation the LKG snapshot
// originated from.
//
// Returns nil when the input is nil. Returns the input unchanged (no
// copy) when its Source is already SourceLastKnownGood; idempotent.
func MarkAsLastKnownGood(snap *Snapshot) *Snapshot {
	if snap == nil {
		return nil
	}
	if snap.Source == SourceLastKnownGood {
		return snap
	}
	clone := *snap // shallow copy is sufficient; fields are either
	// value types or stable references (Patterns is read-only after
	// construction per scraper convention; NamespaceSelector and
	// ExternalSinks similarly).
	clone.Source = SourceLastKnownGood
	return &clone
}
