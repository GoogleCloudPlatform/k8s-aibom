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
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// kserveHandledKinds enumerates the WorkloadKinds this scraper handles.
// Pinned to serving.kserve.io/v1beta1.InferenceService — see
// docs/external-crd-versions.md for the version-pinning policy and the
// upgrade obligations when KServe ships a new spec version.
var kserveHandledKinds = []WorkloadKind{
	{Group: "serving.kserve.io", Version: "v1beta1", Kind: "InferenceService"},
}

// KServeInferenceServiceScraper extracts BOM inputs from KServe
// InferenceService CRs.
//
// Unlike InferenceSpecScraper, which infers runtime from container
// image patterns and resolves digests from running pod status, this
// scraper relies entirely on DECLARED fields in the InferenceService
// spec:
//
//   - spec.predictor.model.modelFormat.name  → runtime (Declared)
//   - spec.predictor.model.modelFormat.version → runtime version (Declared)
//   - spec.predictor.model.storageUri        → model identity (Declared)
//   - spec.predictor.model.runtime           → referenced ServingRuntime
//   - spec.predictor.serviceAccountName      → IAM identity
//   - metadata.annotations (model.k8saibom.dev/*)  → additional model claims
//
// Confidence is correspondingly higher (declared rather than inferred)
// for the attributes the scraper does extract. v1 does NOT resolve
// container image digests for KServe workloads because KServe
// materializes pods indirectly via its own controller, and the
// InferenceService CR has no direct pod-template field. Image digest
// resolution would require following InferenceService → KServe-managed
// Deployment → ReplicaSet → Pod, which is deferred to post-v1 polish.
//
// Access uses *unstructured.Unstructured rather than a typed kserve
// Go client. The unstructured surface is intentionally small (4 nested
// field paths + workload annotations) and the dependency weight of the
// kserve Go module is significant. See docs/external-crd-versions.md
// for the rationale and migration plan.
type KServeInferenceServiceScraper struct {
	verifier SignatureVerifier
	now      func() time.Time
}

// NewKServeInferenceServiceScraper constructs a scraper. Pass nil
// verifier for NoopVerifier (the v1 default; see
// docs/schema-divergences.md D-001).
func NewKServeInferenceServiceScraper(verifier SignatureVerifier) *KServeInferenceServiceScraper {
	if verifier == nil {
		verifier = NoopVerifier{}
	}
	return &KServeInferenceServiceScraper{
		verifier: verifier,
		now:      time.Now,
	}
}

// Name returns the stable scraper identifier.
func (s *KServeInferenceServiceScraper) Name() string { return "inference.kserve" }

// HandlesKind reports whether this scraper produces BOM inputs for
// the given workload kind. Only matches serving.kserve.io/v1beta1.InferenceService.
func (s *KServeInferenceServiceScraper) HandlesKind(k WorkloadKind) bool {
	for _, kk := range kserveHandledKinds {
		if kk == k {
			return true
		}
	}
	return false
}

// Scrape extracts BOM inputs from the InferenceService CR. The
// Workload.Object MUST be a *unstructured.Unstructured (no typed kserve
// client dependency).
//
// The cfg parameter is accepted to satisfy the Scraper interface but is
// not consumed: v1 KServe scraping extracts declared values (storageUri,
// predictor.model.runtime) from the InferenceService CR rather than
// pattern-matching against images. When KServe gains pod-template
// scraping (post-v1), this is where cfg starts being consulted —
// changing it is local to this scraper, not the interface.
func (s *KServeInferenceServiceScraper) Scrape(_ context.Context, w Workload, _ *InferenceConfig) (*BOMInputs, error) {
	if w.Object == nil {
		return nil, fmt.Errorf("inference.kserve: workload Object is nil for kind %s/%s/%s",
			w.Kind.Group, w.Kind.Version, w.Kind.Kind)
	}
	u, ok := w.Object.(*unstructured.Unstructured)
	if !ok {
		return nil, fmt.Errorf("inference.kserve: workload Object is %T, want *unstructured.Unstructured",
			w.Object)
	}
	t := s.now().UTC()
	inputs := &BOMInputs{
		ScraperName:     s.Name(),
		ScrapeTimestamp: t,
	}

	// Workload-level annotations (model.k8saibom.dev/* prefix).
	inputs.Components = append(inputs.Components,
		extractAnnotationModels(u.GetAnnotations(), SourceWorkloadAnnotation, "metadata.annotations")...)

	// spec.predictor.model + spec.predictor.serviceAccountName extraction.
	inputs.Components = append(inputs.Components, s.extractPredictor(u)...)

	sortComponents(inputs.Components)
	inputs.Confidence = aggregateConfidence(inputs.Components)
	inputs.Provenance = []Provenance{{
		ScraperName:     s.Name(),
		ScraperVersion:  ScraperVersion,
		ScrapeMethod:    "spec",
		ScrapeTimestamp: t,
	}}
	return inputs, nil
}

// extractPredictor reads the four documented spec.predictor.* fields
// from the unstructured InferenceService and emits the corresponding
// Components.
func (s *KServeInferenceServiceScraper) extractPredictor(u *unstructured.Unstructured) []Component {
	var out []Component

	modelFormatName, _, _ := unstructured.NestedString(u.Object, "spec", "predictor", "model", "modelFormat", "name")
	modelFormatVersion, _, _ := unstructured.NestedString(u.Object, "spec", "predictor", "model", "modelFormat", "version")
	runtimeRef, _, _ := unstructured.NestedString(u.Object, "spec", "predictor", "model", "runtime")
	storageUri, _, _ := unstructured.NestedString(u.Object, "spec", "predictor", "model", "storageUri")
	serviceAccountName, _, _ := unstructured.NestedString(u.Object, "spec", "predictor", "serviceAccountName")

	// Runtime application component, when modelFormat.name is declared.
	// Confidence is Declared (not Inferred like InferenceSpecScraper's
	// image-pattern detection) because the customer explicitly stated
	// the runtime via the modelFormat field — there is no inference.
	if modelFormatName != "" {
		props := map[string]string{
			"runtime.name":   modelFormatName,
			"runtime.source": "kserve.predictor.model.modelFormat",
		}
		if runtimeRef != "" {
			// The runtime field references a ServingRuntime CR by name.
			// v1 does NOT follow this reference (would require
			// fetching the ServingRuntime CR + interpreting its
			// container template). Just record the reference.
			props["kserve.runtime.ref"] = runtimeRef
		}
		if serviceAccountName != "" {
			props["kserve.serviceAccountName"] = serviceAccountName
		}
		out = append(out, Component{
			Type:       ComponentApplication,
			Name:       TruncateString(modelFormatName, MaxComponentNameLength),
			Version:    TruncateString(modelFormatVersion, MaxComponentNameLength),
			Confidence: ConfidenceDeclared,
			Evidence: Evidence{
				Source:  SourceCRDField,
				Locator: "spec.predictor.model.modelFormat.name",
			},
			Properties: props,
		})
	}

	// Model identity component from storageUri. The URI is the model
	// identity claim verbatim — gs://, s3://, hf://, pvc://, etc. all
	// preserved as the customer authored them.
	if storageUri != "" {
		props := map[string]string{
			"identity.confidence": "claimed",
			"identity.source":     "kserve.storageUri",
		}
		if serviceAccountName != "" {
			// Recorded on the model component too so auditors can see
			// "which IAM identity loaded this model" without
			// cross-referencing the runtime component.
			props["kserve.serviceAccountName"] = serviceAccountName
		}
		out = append(out, Component{
			Type:       ComponentMLModel,
			Name:       TruncateString(storageUri, MaxComponentNameLength),
			Confidence: ConfidenceDeclared,
			Evidence: Evidence{
				Source:  SourceCRDField,
				Locator: "spec.predictor.model.storageUri",
			},
			Properties: props,
		})
	}

	return out
}
