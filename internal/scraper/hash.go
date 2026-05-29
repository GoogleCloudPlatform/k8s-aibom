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

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// HashBOMInputs returns the canonical sha256 hex digest of a BOMInputs
// value, computed in a way that is INSENSITIVE to scrape timestamps
// but SENSITIVE to every other field that could affect the generated
// BOM's content.
//
// **Why timestamps are excluded.** BOMInputs.ScrapeTimestamp and each
// Provenance.ScrapeTimestamp record WHEN the scrape happened, not
// WHAT was observed. The reconciler dedups on inputs to decide
// whether the BOM content has materially changed; if only the
// scrape clock moved, no re-emit is warranted. Future maintainers
// adding new timestamp-like fields should add them to the zeroing
// pass below — the property "BOMInputs hash is insensitive to time"
// is core to the dedup design.
//
// **Determinism guarantees.** The function relies on encoding/json's
// well-defined behavior:
//   - Struct fields are emitted in declared order.
//   - Map keys are sorted alphabetically (Go stdlib explicitly
//     normalizes this).
//   - No interface{} fields are reachable from BOMInputs.
//   - No types in this package implement a custom MarshalJSON
//     except via the standard library's time.Time, whose output is
//     deterministic per time value.
//
// **Field-type constraints (enforced by TestHashBOMInputs_FieldTypeSafety).**
// Types reachable from BOMInputs MUST NOT contain `interface{}`, `chan`,
// `func`, or `unsafe.Pointer` fields. `map[K]V` is acceptable only when
// K is a string or numeric type (encoding/json's deterministic-key-order
// guarantee is scoped to those key kinds). Future contributors adding
// a new field that violates these constraints will see the safety test
// fail before any determinism violation can manifest in production.
//
// TestHashBOMInputs_FieldTypeSafety enforces these constraints
// reflectively so the property survives future struct additions.
// TestHashBOMInputs_DeterministicAcrossInvocations exercises the
// runtime determinism property on a populated fixture.
//
// Returns a lowercase hex sha256 (64 chars), or "" if in is nil.
func HashBOMInputs(in *BOMInputs) (string, error) {
	if in == nil {
		return "", nil
	}
	h := *in // shallow copy; we don't mutate any shared slice/map below
	h.ScrapeTimestamp = time.Time{}
	h.Provenance = zeroProvenanceTimestamps(in.Provenance)

	b, err := json.Marshal(&h)
	if err != nil {
		return "", fmt.Errorf("hash BOMInputs: marshal: %w", err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// zeroProvenanceTimestamps returns a copy of the provenance slice with
// every entry's ScrapeTimestamp zeroed. The original slice is not
// mutated, so the caller's BOMInputs is unaffected.
func zeroProvenanceTimestamps(in []Provenance) []Provenance {
	if len(in) == 0 {
		return in
	}
	out := make([]Provenance, len(in))
	for i, p := range in {
		p.ScrapeTimestamp = time.Time{}
		out[i] = p
	}
	return out
}
