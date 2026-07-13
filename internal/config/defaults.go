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

package config

import (
	"time"

	"k8s.io/apimachinery/pkg/labels"

	"github.com/GoogleCloudPlatform/k8s-aibom/internal/scraper"
)

// Compiled-in default values. These are the values the controller
// runs with when:
//   - No AIBOMControllerConfig CR exists (missing-CR fallback)
//   - The CR exists but is semantically invalid (invalid-CR fallback)
//   - The Store is constructed without an initial snapshot
//
// Per project memory "AIBOMControllerConfig v1 behavior", the
// fallback is all-or-nothing: when a CR is partially invalid, the
// controller falls back to ALL of these defaults rather than mixing
// partial-from-CR with defaults. The condition message on the CR
// enumerates the failing fields so a single fix-and-apply clears
// them.

const (
	// DefaultInlineThresholdBytes is the BOM size threshold below
	// which BOMs are stored inline in AIBOM.status.bomDocument.inline.
	// 256 KiB matches PRD §FR5.4 and stays safely under K8s'
	// 1.5MB etcd object size limit.
	DefaultInlineThresholdBytes int64 = 262144

	// DefaultStaleThresholdReconciles is the number of consecutive
	// reconciles with extraction errors before Stale=True.
	DefaultStaleThresholdReconciles int32 = 3

	// DefaultNamespaceOptInLabel is the namespace label that opts in
	// to AIBOM generation when no NamespaceSelector is configured in
	// the CR. Matches PRD §FR1.3.
	DefaultNamespaceOptInLabel = "aibom.k8saibom.dev/enabled"

	// DefaultConfigName is the AIBOMControllerConfig CR name the
	// controller consults (singleton convention; see godoc on the
	// AIBOMControllerConfig type).
	DefaultConfigName = "default"

	// MinInlineThresholdBytes is the minimum allowed inline threshold (1 KiB).
	MinInlineThresholdBytes int64 = 1024

	// MaxInlineThresholdBytes is the maximum allowed inline threshold (1 MiB).
	MaxInlineThresholdBytes int64 = 1048576
)

// DefaultSnapshot returns a fresh Snapshot wrapping the compiled-in
// safe defaults. The returned snapshot is marked with
// Source=SourceCompiledDefaults so consumers can distinguish it
// from a CR-derived snapshot.
//
// The function is safe to call repeatedly; each call constructs a
// new pointer (no shared state across snapshots).
func DefaultSnapshot() *Snapshot {
	return &Snapshot{
		Patterns:                 scraper.DefaultV1Config(),
		InlineThreshold:          DefaultInlineThresholdBytes,
		StaleThresholdReconciles: DefaultStaleThresholdReconciles,
		NamespaceSelector:        DefaultNamespaceSelector(),
		ExternalSinks:            nil, // PRD §FR6.6: no external sinks by default
		Source:                   SourceCompiledDefaults,
		SourceGeneration:         0,
		LoadedAt:                 time.Now(),
	}
}

// DefaultNamespaceSelector returns the pre-materialized labels.Selector
// matching the compiled-in opt-in label. Equivalent to applying
// metav1.LabelSelector{MatchLabels: {DefaultNamespaceOptInLabel: "true"}}.
//
// Returns a Selector that's never an error to evaluate; safe to call
// at controller startup before any client is configured.
func DefaultNamespaceSelector() labels.Selector {
	return labels.SelectorFromSet(labels.Set{
		DefaultNamespaceOptInLabel: "true",
	})
}
