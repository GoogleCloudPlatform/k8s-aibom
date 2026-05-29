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
	"testing"
)

func TestSignatureStatusStringValues(t *testing.T) {
	cases := map[SignatureStatus]string{
		SignatureUnsigned: "unsigned",
		SignatureClaimed:  "claimed",
		SignatureVerified: "verified",
	}
	for got, want := range cases {
		if string(got) != want {
			t.Errorf("SignatureStatus %q got string %q, want %q", got, string(got), want)
		}
	}
}

func TestNoopVerifier_EmptyClaim_ReturnsUnsigned(t *testing.T) {
	v := NoopVerifier{}
	res, err := v.Verify(context.Background(), SignatureClaim{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Status != SignatureUnsigned {
		t.Errorf("empty claim: got status %q, want %q", res.Status, SignatureUnsigned)
	}
}

func TestNoopVerifier_NonEmptyClaim_ReturnsClaimed(t *testing.T) {
	v := NoopVerifier{}
	claim := SignatureClaim{
		ModelIdentity: "meta-llama/Llama-3.1-8B-Instruct",
		SignatureRef:  "rekor://example/12345",
		Evidence:      Evidence{Source: SourceWorkloadAnnotation, Locator: "metadata.annotations[model.k8saibom.dev/oms-signature]"},
	}
	res, err := v.Verify(context.Background(), claim)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Status != SignatureClaimed {
		t.Errorf("non-empty claim: got status %q, want %q", res.Status, SignatureClaimed)
	}
}

// TestNoopVerifier_NeverReturnsVerified is the core property test for v1.
// A bug that emits SignatureVerified would mislead auditors into believing
// cryptographic verification occurred. We sample a broad range of claim
// shapes; for v1, NONE of them should ever produce SignatureVerified.
func TestNoopVerifier_NeverReturnsVerified(t *testing.T) {
	v := NoopVerifier{}
	inputs := []SignatureClaim{
		{},
		{ModelIdentity: "foo"},
		{SignatureRef: "rekor://example/1"},
		{SignatureRef: "verified", ModelIdentity: "verified"}, // even adversarial strings
		{ModelIdentity: "meta-llama/Llama-3.1-8B-Instruct", SignatureRef: "rekor://example/12345"},
	}
	for i, in := range inputs {
		res, err := v.Verify(context.Background(), in)
		if err != nil {
			t.Fatalf("input %d: unexpected error: %v", i, err)
		}
		if res.Status == SignatureVerified {
			t.Errorf("input %d: NoopVerifier returned SignatureVerified for %+v; this MUST NEVER happen in v1", i, in)
		}
	}
}

func TestNoopVerifier_Name(t *testing.T) {
	if got := (NoopVerifier{}).Name(); got != "noop" {
		t.Errorf("NoopVerifier.Name() = %q, want %q", got, "noop")
	}
}
