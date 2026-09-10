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

// Package verifier implements Design 002: Sigstore/Rekor verification of
// model-signing (OMS) signature claims, producing the `verified`
// signature tier.
//
// This is a nested Go module so the sigstore-go dependency tree stays out
// of the root module (a commitment on the public record — issue #8). It
// deliberately does not import the root module: the root wires an adapter
// from these types to internal/scraper.SignatureVerifier, keeping the
// dependency direction acyclic.
//
// The binding/outcome semantics live in outcome.go and implement the
// amended Design 002 rules (identity constraint required; digest over
// name). Everything else here — fetching, caching, chain verification —
// is the design's uncontested core.
package verifier

import (
	"context"
	"time"
)

// TrustRootMode selects where the Sigstore trust root comes from.
type TrustRootMode string

const (
	// TrustPublic uses the embedded Sigstore public-good TUF root.
	// Under this mode, Verified is unattainable unless Identities is
	// non-empty (Design 002 §3: nothing else constrains the signer).
	TrustPublic TrustRootMode = "public"
	// TrustTUFMirror uses a self-hosted Sigstore TUF mirror (the AICR
	// case). A non-public root is an implicit identity constraint.
	TrustTUFMirror TrustRootMode = "tufMirror"
	// TrustStaticBundle uses a trust bundle file (air-gapped clusters).
	TrustStaticBundle TrustRootMode = "staticBundle"
)

// IdentityConstraint matches the signing certificate's identity.
// Issuer is compared exactly; SubjectPattern is RE2 against the SAN.
type IdentityConstraint struct {
	Issuer         string `json:"issuer,omitempty"`
	SubjectPattern string `json:"subjectPattern,omitempty"`
}

// Config mirrors AIBOMControllerConfig.spec.verification (Design 002 §2).
type Config struct {
	TrustRootMode    TrustRootMode        `json:"trustRootMode,omitempty"`
	TUFMirrorURL     string               `json:"tufMirrorURL,omitempty"`
	StaticBundlePath string               `json:"staticBundlePath,omitempty"`
	RekorURL         string               `json:"rekorURL,omitempty"`
	Identities       []IdentityConstraint `json:"identities,omitempty"`
	// PerClaimTimeout bounds one verification attempt end to end,
	// including all network I/O. Zero means DefaultPerClaimTimeout.
	PerClaimTimeout time.Duration `json:"perClaimTimeout,omitempty"`
	// CacheTTL bounds how long a verification result is reused for an
	// identical (reference, trust-root epoch) pair. Zero means
	// DefaultCacheTTL.
	CacheTTL time.Duration `json:"cacheTTL,omitempty"`
}

const (
	DefaultPerClaimTimeout = 10 * time.Second
	DefaultCacheTTL        = 24 * time.Hour
	// MaxBundleBytes caps a fetched signature bundle (§6).
	MaxBundleBytes = 1 << 20 // 1 MiB
)

// Claim is the verifier-side mirror of the scraper's SignatureClaim: the
// declared model identity, the self-declared signature reference, and the
// optional declared content digest.
type Claim struct {
	// ModelIdentity is the workload's declared model identity, verbatim.
	ModelIdentity string
	// SignatureRef is the signature reference exactly as found (HTTPS
	// URL, or inline base64 with the "base64:" prefix).
	SignatureRef string
	// DeclaredDigest is the optional model.k8saibom.dev/digest value
	// ("sha256:..."). When present, it must equal the manifest root
	// digest for Verified (Design 002 §3, digest-over-name precedence).
	DeclaredDigest string
}

// Outcome is the recorded verification outcome fact — one row of the
// Design 002 §4 taxonomy. These strings are emitted into BOM properties
// and are part of the public contract once shipped.
type Outcome string

const (
	OutcomeVerified            Outcome = "verified"
	OutcomeUnconstrained       Outcome = "signature-valid-unconstrained"
	OutcomeFailed              Outcome = "failed"
	OutcomeIdentityMismatch    Outcome = "identity-mismatch"
	OutcomeSubjectNameMismatch Outcome = "subject-name-mismatch"
	OutcomeDigestMismatch      Outcome = "digest-mismatch"
	OutcomeError               Outcome = "error"
)

// Result is what Verify returns. Verified is true only when the full
// Design 002 §3 chain held: signature chain valid against the trust
// root, Rekor inclusion proven, identity constraint satisfied, and no
// declared binding contradicted.
type Result struct {
	Verified bool
	Outcome  Outcome
	// Reason carries the failure class or detail for non-verified
	// outcomes ("network", "tuf-refresh", chain errors, ...).
	Reason string
	// Identity is the signing certificate identity (SAN), recorded for
	// every outcome where the chain parsed, regardless of constraints.
	Identity string
	// RekorEntry is the transparency log entry ID when inclusion was
	// proven.
	RekorEntry string
	// SubjectName and RootDigest are the statement's subject fields as
	// signed, recorded as facts.
	SubjectName string
	RootDigest  string
	// SubjectNameMismatch records the format-permitted rename case:
	// true when verification proceeded on a digest binding while the
	// subject name disagreed with the declared identity.
	SubjectNameMismatch bool
	// VerifiedAt is when verification completed (zero for cache misses
	// that errored before verification).
	VerifiedAt time.Time
}

// Verifier verifies model-signature claims. Implementations must be safe
// for concurrent use; every method must respect the context deadline and
// must never panic on hostile input (references are workload-author
// controlled).
type Verifier interface {
	Verify(ctx context.Context, c Claim) Result
}
