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
	"errors"
	"testing"
)

type fakeVerifier struct {
	result SignatureResult
	err    error
	claims []SignatureClaim
}

func (f *fakeVerifier) Name() string { return "fake" }
func (f *fakeVerifier) Verify(_ context.Context, c SignatureClaim) (SignatureResult, error) {
	f.claims = append(f.claims, c)
	return f.result, f.err
}

func TestReservedAnnotationsAreNotModels(t *testing.T) {
	comps := extractAnnotationModels(map[string]string{
		AnnotationSignatureRef:              "https://models.example.com/sig.json",
		AnnotationModelDigest:               "sha256:abcd",
		modelAnnotationPrefix + "name":      "meta-llama/Llama-3.1-8B-Instruct",
		"unrelated.example.com/other":       "ignored",
	}, SourceWorkloadAnnotation, "metadata.annotations")

	if len(comps) != 1 {
		t.Fatalf("want exactly 1 model (reserved keys skipped), got %d: %+v", len(comps), comps)
	}
	if comps[0].Name != "meta-llama/Llama-3.1-8B-Instruct" {
		t.Fatalf("wrong model extracted: %q", comps[0].Name)
	}
}

func TestApplySignaturesWiresClaimAndRecordsFacts(t *testing.T) {
	fv := &fakeVerifier{result: SignatureResult{
		Status:     SignatureVerified,
		Outcome:    "verified",
		Identity:   "release@models.example.com",
		RekorEntry: "108000001",
	}}
	comps := []Component{
		{Type: ComponentMLModel, Name: "meta-llama/Llama-3.1-8B-Instruct", Confidence: ConfidenceDeclared},
		{Type: ComponentApplication, Name: "vllm"},
	}
	workloadAnn := map[string]string{
		AnnotationSignatureRef: "https://models.example.com/sig.json",
	}
	templateAnn := map[string]string{
		AnnotationModelDigest: "sha256:aaaa", // fallback map contributes digest
	}
	applySignatures(context.Background(), fv, comps, workloadAnn, templateAnn)

	if len(fv.claims) != 1 {
		t.Fatalf("verifier called %d times, want 1 (models only)", len(fv.claims))
	}
	c := fv.claims[0]
	if c.ModelIdentity != "meta-llama/Llama-3.1-8B-Instruct" ||
		c.SignatureRef != "https://models.example.com/sig.json" ||
		c.DeclaredDigest != "sha256:aaaa" {
		t.Fatalf("claim wiring wrong: %+v", c)
	}
	p := comps[0].Properties
	if p[propSignatureStatus] != "verified" || p[propSignatureIdentity] == "" || p[propSignatureRekorEntry] == "" {
		t.Fatalf("facts not recorded: %v", p)
	}
	if comps[1].Properties[propSignatureStatus] != "" {
		t.Fatalf("non-model component got signature properties")
	}
}

func TestApplySignaturesNoClaimWritesNothing(t *testing.T) {
	fv := &fakeVerifier{}
	comps := []Component{{Type: ComponentMLModel, Name: "m"}}
	applySignatures(context.Background(), fv, comps, map[string]string{}, nil)
	if len(fv.claims) != 0 {
		t.Fatalf("verifier called with no claim present")
	}
	if len(comps[0].Properties) != 0 {
		t.Fatalf("properties written with no claim: %v", comps[0].Properties)
	}
}

func TestApplySignaturesVerifierErrorDegrades(t *testing.T) {
	fv := &fakeVerifier{err: errors.New("boom")}
	comps := []Component{{Type: ComponentMLModel, Name: "m"}}
	applySignatures(context.Background(), fv, comps,
		map[string]string{AnnotationSignatureRef: "base64:xx"})
	p := comps[0].Properties
	if p[propSignatureStatus] != string(SignatureClaimed) || p[propSignatureOutcome] != "error" {
		t.Fatalf("error did not degrade to claimed+error: %v", p)
	}
}
