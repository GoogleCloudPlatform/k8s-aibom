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
	"context"
	"time"
)

// SignatureStatus is the three-state encoding of OMS signature presence and
// verification for a model identity. v1 distinguishes Unsigned and Claimed.
// Verified is reserved for a future Rekor-aware implementation and MUST NOT
// be returned by any verifier shipped in v1 — see docs/schema-divergences.md
// entry D-001 and the project memory entry "v1 OMS signature scope".
type SignatureStatus string

const (
	// SignatureUnsigned means the scraper found no signature reference for
	// this model identity (no signature annotation, no signed-blob volume
	// mount, no claimed attestation). The model is treated as unsigned.
	SignatureUnsigned SignatureStatus = "unsigned"

	// SignatureClaimed means the scraper found a self-declared signature
	// reference (e.g., a `model.k8saibom.dev/oms-signature` annotation or a
	// claimed Rekor entry) but did NOT cryptographically verify it. v1
	// reports this state; auditors must read it as "claimed, unverified".
	SignatureClaimed SignatureStatus = "claimed"

	// SignatureVerified means a verifier has fetched the referenced
	// signature artifact, validated the signing chain, and confirmed
	// inclusion in a transparency log. v1 MUST NOT emit this status.
	SignatureVerified SignatureStatus = "verified"
)

// SignatureClaim is the per-model input handed to a SignatureVerifier. The
// scraper populates it from whatever evidence it found (annotations,
// volume metadata, etc.).
//
// JSON tags are present so this type round-trips cleanly; see
// signature_test.go.
type SignatureClaim struct {
	// ModelIdentity is the claimed model identity the signature applies to,
	// in the same form the BOM emits (e.g., "meta-llama/Llama-3.1-8B-Instruct").
	ModelIdentity string `json:"modelIdentity,omitempty"`

	// SignatureRef is the self-declared signature reference exactly as
	// found in the source. For an annotation, this is the annotation value.
	// Empty means "no signature claimed."
	SignatureRef string `json:"signatureRef,omitempty"`

	// Evidence records where the signature claim was extracted from. Used
	// to populate the BOM's per-attribute evidence field for the resulting
	// signature record.
	Evidence Evidence `json:"evidence,omitzero"`
}

// SignatureResult is what a SignatureVerifier returns. Identity, RekorEntry,
// and Timestamp are populated only when Status is SignatureVerified; v1
// NoopVerifier leaves them empty.
//
// JSON tags are present so this type round-trips cleanly; see
// signature_test.go.
type SignatureResult struct {
	Status     SignatureStatus `json:"status,omitempty"`
	Identity   string          `json:"identity,omitempty"`
	RekorEntry string          `json:"rekorEntry,omitempty"`
	Timestamp  time.Time       `json:"timestamp,omitzero"`
}

// SignatureVerifier is the v2 extension point for cryptographic signature
// verification. v1 ships only NoopVerifier, which never returns
// SignatureVerified. A future RekorVerifier (Phase 2c.2) replaces NoopVerifier
// to perform actual Rekor inclusion-proof verification.
//
// Implementations MUST:
//   - Return SignatureUnsigned when SignatureClaim.SignatureRef is empty.
//   - Return SignatureClaimed when a claim is present but not verified.
//   - Return SignatureVerified ONLY after cryptographic validation.
//   - Be safe for concurrent invocation.
//   - Never block longer than necessary; verification should be quick or
//     bounded by context cancellation.
type SignatureVerifier interface {
	Name() string
	Verify(ctx context.Context, claim SignatureClaim) (SignatureResult, error)
}

// NoopVerifier is the v1 SignatureVerifier implementation. It never performs
// cryptographic verification. For any non-empty SignatureClaim.SignatureRef
// it returns SignatureClaimed; for an empty ref it returns SignatureUnsigned.
// It is structurally incapable of emitting SignatureVerified.
type NoopVerifier struct{}

// Name returns the scraper-style stable identifier for this verifier.
func (NoopVerifier) Name() string { return "noop" }

// Verify implements SignatureVerifier.
func (NoopVerifier) Verify(_ context.Context, claim SignatureClaim) (SignatureResult, error) {
	if claim.SignatureRef == "" {
		return SignatureResult{Status: SignatureUnsigned}, nil
	}
	return SignatureResult{Status: SignatureClaimed}, nil
}

// Compile-time interface assertion.
var _ SignatureVerifier = NoopVerifier{}
