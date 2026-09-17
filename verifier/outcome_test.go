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
	"testing"
	"time"
)

// One test case per row of the Design 002 §4 outcome taxonomy, as
// amended 2026-09-08 (identity constraint required) and 2026-09-09
// (digest over name). If a review-window revision changes the table,
// this file is the other half of the change.
func TestDecideOutcome(t *testing.T) {
	now := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)

	goodFindings := findings{
		chainValid:  true,
		identity:    "release@models.example.com",
		issuer:      "https://accounts.example.com",
		rekorProven: true,
		rekorEntry:  "108000001",
		subjectName: "meta-llama/Llama-3.1-8B-Instruct",
		rootDigest:  "sha256:aaaa",
	}
	constrained := Config{
		TrustRootMode: TrustPublic,
		Identities: []IdentityConstraint{{
			Issuer:         "https://accounts.example.com",
			SubjectPattern: `^release@models\.example\.com$`,
		}},
	}
	claim := Claim{ModelIdentity: "meta-llama/Llama-3.1-8B-Instruct"}

	cases := []struct {
		name         string
		cfg          Config
		claim        Claim
		f            findings
		wantOutcome  Outcome
		wantVerified bool
		wantNameFact bool
	}{
		{
			name: "infrastructure error is error, not evidence",
			cfg:  constrained, claim: claim,
			f:           findings{errClass: "network"},
			wantOutcome: OutcomeError,
		},
		{
			name: "invalid chain fails",
			cfg:  constrained, claim: claim,
			f:           findings{chainValid: false, chainErr: "certificate chain does not verify"},
			wantOutcome: OutcomeFailed,
		},
		{
			name: "no rekor proof fails",
			cfg:  constrained, claim: claim,
			f: func() findings {
				f := goodFindings
				f.rekorProven = false
				return f
			}(),
			wantOutcome: OutcomeFailed,
		},
		{
			name: "public root + empty identities: verified unattainable",
			cfg:  Config{TrustRootMode: TrustPublic}, claim: claim,
			f:           goodFindings,
			wantOutcome: OutcomeUnconstrained,
		},
		{
			name: "identity constraint unsatisfied",
			cfg: Config{TrustRootMode: TrustPublic, Identities: []IdentityConstraint{{
				SubjectPattern: `^only-this-signer@corp\.example$`,
			}}},
			claim:       claim,
			f:           goodFindings,
			wantOutcome: OutcomeIdentityMismatch,
		},
		{
			name: "tufMirror is an implicit identity constraint",
			cfg:  Config{TrustRootMode: TrustTUFMirror}, claim: claim,
			f:            goodFindings,
			wantOutcome:  OutcomeVerified,
			wantVerified: true,
		},
		{
			name: "declared digest mismatch always blocks",
			cfg:  constrained,
			claim: Claim{
				ModelIdentity:  "meta-llama/Llama-3.1-8B-Instruct",
				DeclaredDigest: "sha256:bbbb",
			},
			f:           goodFindings,
			wantOutcome: OutcomeDigestMismatch,
		},
		{
			name: "name mismatch with no digest binding blocks",
			cfg:  constrained,
			claim: Claim{
				ModelIdentity: "a-different/model",
			},
			f:           goodFindings,
			wantOutcome: OutcomeSubjectNameMismatch,
		},
		{
			name: "name mismatch overridden by digest match (rename case)",
			cfg:  constrained,
			claim: Claim{
				ModelIdentity:  "a-different/model",
				DeclaredDigest: "sha256:aaaa",
			},
			f:            goodFindings,
			wantOutcome:  OutcomeVerified,
			wantVerified: true,
			wantNameFact: true,
		},
		{
			name: "full success",
			cfg:  constrained,
			claim: Claim{
				ModelIdentity:  "meta-llama/Llama-3.1-8B-Instruct",
				DeclaredDigest: "aaaa", // prefixless form tolerated
			},
			f:            goodFindings,
			wantOutcome:  OutcomeVerified,
			wantVerified: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := decideOutcome(tc.cfg, tc.claim, tc.f, now)
			if got.Outcome != tc.wantOutcome {
				t.Fatalf("outcome = %q, want %q (reason=%q)", got.Outcome, tc.wantOutcome, got.Reason)
			}
			if got.Verified != tc.wantVerified {
				t.Fatalf("verified = %v, want %v", got.Verified, tc.wantVerified)
			}
			if got.SubjectNameMismatch != tc.wantNameFact {
				t.Fatalf("subjectNameMismatch = %v, want %v", got.SubjectNameMismatch, tc.wantNameFact)
			}
			// Facts are recorded on every chain-parsed outcome.
			if tc.f.chainValid && got.Identity == "" {
				t.Fatalf("identity fact not recorded")
			}
		})
	}
}
