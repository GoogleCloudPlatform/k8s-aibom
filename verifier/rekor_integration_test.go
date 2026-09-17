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
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"testing"
)

// Real-cryptography integration tests against sigstore-go's published
// example bundle (a DSSE attestation for pkg:npm/sigstore@1.3.0, signed
// keylessly by the sigstore-js release workflow, with a Rekor entry) and
// its paired trusted-root snapshot. Everything runs offline: the trust
// root is a static bundle and the signature reference is inline.
//
// Note the fixture's statement subject carries a sha512 digest only, so
// these tests also pin the "no sha256 root digest → name is the only
// binding" path.

const (
	fixtureSubjectName = "pkg:npm/sigstore@1.3.0"
	fixtureSANPattern  = `^https://github\.com/sigstore/sigstore-js/`
	fixtureIssuer      = "https://token.actions.githubusercontent.com"
)

func fixtureVerifier(t *testing.T, identities []IdentityConstraint) *RekorVerifier {
	t.Helper()
	v, err := NewRekorVerifier(Config{
		TrustRootMode:    TrustStaticBundle,
		StaticBundlePath: "testdata/trusted-root-public-good.json",
		Identities:       identities,
	})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func fixtureRef(t *testing.T, mutate func([]byte) []byte) string {
	t.Helper()
	raw, err := os.ReadFile("testdata/bundle-provenance.json")
	if err != nil {
		t.Fatal(err)
	}
	if mutate != nil {
		raw = mutate(raw)
	}
	return inlinePrefix + base64.StdEncoding.EncodeToString(raw)
}

func TestRekorVerifierRealBundle(t *testing.T) {
	ctx := context.Background()

	t.Run("static root is an implicit constraint: name match verifies", func(t *testing.T) {
		v := fixtureVerifier(t, nil)
		res := v.Verify(ctx, Claim{ModelIdentity: fixtureSubjectName, SignatureRef: fixtureRef(t, nil)})
		if !res.Verified || res.Outcome != OutcomeVerified {
			t.Fatalf("want verified, got %+v", res)
		}
		if res.Identity == "" || res.RekorEntry == "" {
			t.Fatalf("facts not recorded: %+v", res)
		}
		if res.SubjectName != fixtureSubjectName {
			t.Fatalf("subject name %q", res.SubjectName)
		}
	})

	t.Run("explicit identity constraint satisfied", func(t *testing.T) {
		v := fixtureVerifier(t, []IdentityConstraint{{
			Issuer:         fixtureIssuer,
			SubjectPattern: fixtureSANPattern,
		}})
		res := v.Verify(ctx, Claim{ModelIdentity: fixtureSubjectName, SignatureRef: fixtureRef(t, nil)})
		if !res.Verified {
			t.Fatalf("want verified, got %+v", res)
		}
	})

	t.Run("identity constraint unsatisfied", func(t *testing.T) {
		v := fixtureVerifier(t, []IdentityConstraint{{
			SubjectPattern: `^release@nobody\.example$`,
		}})
		res := v.Verify(ctx, Claim{ModelIdentity: fixtureSubjectName, SignatureRef: fixtureRef(t, nil)})
		if res.Verified || res.Outcome != OutcomeIdentityMismatch {
			t.Fatalf("want identity-mismatch, got %+v", res)
		}
		if res.Identity == "" {
			t.Fatalf("identity fact must be recorded on mismatch")
		}
	})

	t.Run("name mismatch with no digest binding blocks", func(t *testing.T) {
		v := fixtureVerifier(t, nil)
		res := v.Verify(ctx, Claim{ModelIdentity: "meta-llama/Llama-3.1-8B-Instruct", SignatureRef: fixtureRef(t, nil)})
		if res.Verified || res.Outcome != OutcomeSubjectNameMismatch {
			t.Fatalf("want subject-name-mismatch, got %+v", res)
		}
	})

	t.Run("tampered payload fails cryptographically", func(t *testing.T) {
		v := fixtureVerifier(t, nil)
		ref := fixtureRef(t, func(raw []byte) []byte {
			// Flip one byte inside the DSSE payload's base64 to corrupt
			// the signed content without breaking JSON structure.
			i := bytes.Index(raw, []byte(`"payload"`))
			if i < 0 {
				t.Fatal("payload field not found")
			}
			out := append([]byte(nil), raw...)
			j := i + 30
			if out[j] == 'A' {
				out[j] = 'B'
			} else {
				out[j] = 'A'
			}
			return out
		})
		res := v.Verify(ctx, Claim{ModelIdentity: fixtureSubjectName, SignatureRef: ref})
		if res.Verified || res.Outcome != OutcomeFailed {
			t.Fatalf("want failed, got %+v", res)
		}
	})

	t.Run("cache returns identical result without rework", func(t *testing.T) {
		v := fixtureVerifier(t, nil)
		c := Claim{ModelIdentity: fixtureSubjectName, SignatureRef: fixtureRef(t, nil)}
		first := v.Verify(ctx, c)
		second := v.Verify(ctx, c)
		if first.Outcome != second.Outcome || first.VerifiedAt != second.VerifiedAt {
			t.Fatalf("cache miss on identical claim: %v vs %v", first.VerifiedAt, second.VerifiedAt)
		}
	})
}
