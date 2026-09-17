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
)

// Signature claim annotations (Design 002 §3 step 1). These share the
// model.k8saibom.dev/ prefix but are NOT model identities — they are
// reserved keys that extractAnnotationModels must skip.
const (
	// AnnotationSignatureRef declares where the model's signature
	// bundle lives: an HTTPS URL, or inline as "base64:<data>".
	AnnotationSignatureRef = modelAnnotationPrefix + "oms-signature"

	// AnnotationModelDigest optionally declares the model's content
	// digest ("sha256:..."). When present, the verifier requires it to
	// equal the signed manifest's root digest (digest-over-name
	// precedence, Design 002 amendment 2026-09-09).
	AnnotationModelDigest = modelAnnotationPrefix + "digest"
)

// reservedModelAnnotation reports whether a model.k8saibom.dev/* key is
// a claim-metadata key rather than a model identity declaration.
func reservedModelAnnotation(key string) bool {
	return key == AnnotationSignatureRef || key == AnnotationModelDigest
}

// Signature property keys emitted on ML-model components. summary.go
// surfaces "signature.status" as ModelSummary.Signed; the rest are the
// recorded outcome facts from the Design 002 §4 taxonomy.
const (
	propSignatureStatus     = "signature.status"
	propSignatureOutcome    = "signature.outcome"
	propSignatureReason     = "signature.reason"
	propSignatureIdentity   = "signature.identity"
	propSignatureRekorEntry = "signature.rekorEntry"
	propSignatureNameNote   = "signature.subjectNameMismatch"
)

// applySignatures runs the configured SignatureVerifier over every
// ML-model component, using the workload-level signature annotations.
// Annotation maps are consulted in order; the first map carrying
// AnnotationSignatureRef wins (workload metadata before pod template,
// matching the declared-model precedence elsewhere).
//
// Outcomes are facts on the component, never errors: a verifier error
// degrades to `claimed` with the reason recorded, and can never fail
// the scrape (Design 002 §4 — enabling verification never makes
// inventory worse).
func applySignatures(ctx context.Context, v SignatureVerifier, comps []Component, annotationMaps ...map[string]string) {
	if v == nil {
		v = NoopVerifier{}
	}
	var ref, digest string
	for _, m := range annotationMaps {
		if m == nil {
			continue
		}
		if ref == "" {
			ref = m[AnnotationSignatureRef]
		}
		if digest == "" {
			digest = m[AnnotationModelDigest]
		}
	}

	// No claim → no properties: absence of signature.* preserves the
	// byte-identical-to-v1.4.0 contract for workloads without
	// signature annotations (Design 002 §1); readers treat absence as
	// unsigned, which SignatureUnsigned also encodes.
	if ref == "" {
		return
	}

	for i := range comps {
		if comps[i].Type != ComponentMLModel {
			continue
		}
		claim := SignatureClaim{
			ModelIdentity:  comps[i].Name,
			SignatureRef:   ref,
			DeclaredDigest: digest,
			Evidence: Evidence{
				Source:  SourceWorkloadAnnotation,
				Locator: "metadata.annotations[" + AnnotationSignatureRef + "]",
			},
		}
		res, err := v.Verify(ctx, claim)
		if comps[i].Properties == nil {
			comps[i].Properties = map[string]string{}
		}
		p := comps[i].Properties
		if err != nil {
			// Defensive: adapters map failures to outcomes and should
			// not error. If one does, degrade honestly.
			p[propSignatureStatus] = string(SignatureClaimed)
			p[propSignatureOutcome] = "error"
			p[propSignatureReason] = TruncateString(err.Error(), 256)
			continue
		}
		p[propSignatureStatus] = string(res.Status)
		if res.Outcome != "" {
			p[propSignatureOutcome] = res.Outcome
		}
		if res.Reason != "" {
			p[propSignatureReason] = TruncateString(res.Reason, 256)
		}
		if res.Identity != "" {
			p[propSignatureIdentity] = res.Identity
		}
		if res.RekorEntry != "" {
			p[propSignatureRekorEntry] = res.RekorEntry
		}
		if res.SubjectNameMismatch {
			p[propSignatureNameNote] = "true"
		}
	}
}
