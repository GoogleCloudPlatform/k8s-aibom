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

package verifier

import (
	"regexp"
	"time"
)

// This file encodes the CONTESTED half of Design 002 — the §3 binding
// rules and §4 outcome taxonomy, as amended 2026-09-08 (identity
// constraint required) and 2026-09-09 (digest over name). It is
// deliberately a pure function over raw verification findings so that
// review-window revisions land in exactly one place. Do not spread
// outcome semantics anywhere else.

// findings is what chain verification produces before any binding
// decision: cryptographic facts, no judgments.
type findings struct {
	chainValid  bool
	chainErr    string // set when !chainValid; classifies the failure
	errClass    string // "network", "tuf-refresh", ... — infrastructure errors
	identity    string // signing certificate SAN
	issuer      string // signing certificate OIDC issuer
	rekorProven bool
	rekorEntry  string
	subjectName string
	rootDigest  string // "sha256:..."
}

// decideOutcome maps findings + config + claim to a Result, per the
// amended Design 002 §3/§4:
//
//  1. Infrastructure errors → OutcomeError (retry next resync).
//  2. Invalid chain / no Rekor proof → OutcomeFailed.
//  3. No identity constraint in effect (public root, empty identities)
//     → OutcomeUnconstrained: a real, logged signature exists, but
//     nothing binds the signer, so Verified is unattainable by design.
//  4. Identity constraint configured but unsatisfied → OutcomeIdentityMismatch.
//  5. Declared digest present and ≠ manifest root digest →
//     OutcomeDigestMismatch (always blocks — contradicts the strong binding).
//  6. Subject name ≠ declared identity:
//     - with a declared digest that matched: proceed (rename is a
//     format-permitted publisher action) — Verified with
//     SubjectNameMismatch recorded as fact.
//     - with no digest binding: OutcomeSubjectNameMismatch (the name is
//     the only correspondence on offer, and it disagrees).
//  7. Otherwise → Verified.
func decideOutcome(cfg Config, c Claim, f findings, now time.Time) Result {
	base := Result{
		Identity:    f.identity,
		RekorEntry:  f.rekorEntry,
		SubjectName: f.subjectName,
		RootDigest:  f.rootDigest,
		VerifiedAt:  now,
	}

	// 1. Infrastructure error: not evidence about the claim at all.
	if f.errClass != "" {
		base.Outcome = OutcomeError
		base.Reason = f.errClass
		return base
	}
	// 2. Cryptographic failure.
	if !f.chainValid {
		base.Outcome = OutcomeFailed
		base.Reason = f.chainErr
		return base
	}
	if !f.rekorProven {
		base.Outcome = OutcomeFailed
		base.Reason = "rekor-inclusion-not-proven"
		return base
	}

	// 3./4. Who signed. A non-public trust root is an implicit
	// constraint: only that PKI's identities can produce a valid chain.
	constrained := cfg.TrustRootMode != TrustPublic && cfg.TrustRootMode != ""
	if len(cfg.Identities) > 0 {
		constrained = true
		if !identityMatches(cfg.Identities, f.issuer, f.identity) {
			base.Outcome = OutcomeIdentityMismatch
			return base
		}
	}
	if !constrained {
		base.Outcome = OutcomeUnconstrained
		return base
	}

	// 5./6. What was signed — digest over name.
	digestDeclared := c.DeclaredDigest != ""
	if digestDeclared && !digestEqual(c.DeclaredDigest, f.rootDigest) {
		base.Outcome = OutcomeDigestMismatch
		return base
	}
	nameMatches := f.subjectName == c.ModelIdentity
	if !nameMatches && !digestDeclared {
		base.Outcome = OutcomeSubjectNameMismatch
		return base
	}

	// 7. Verified.
	base.Verified = true
	base.Outcome = OutcomeVerified
	base.SubjectNameMismatch = !nameMatches
	return base
}

func identityMatches(constraints []IdentityConstraint, issuer, san string) bool {
	for _, ic := range constraints {
		if ic.Issuer != "" && ic.Issuer != issuer {
			continue
		}
		if ic.SubjectPattern != "" {
			re, err := regexp.Compile(ic.SubjectPattern)
			if err != nil || !re.MatchString(san) {
				continue
			}
		}
		return true
	}
	return false
}

// digestEqual compares digests, tolerating a missing "sha256:" prefix on
// the declared side (workload authors write both forms).
func digestEqual(declared, root string) bool {
	if declared == root {
		return true
	}
	return "sha256:"+declared == root
}
