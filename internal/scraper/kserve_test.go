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
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// fixedKServeTime is the pinned clock for KServe scraper tests so
// timestamps in BOMInputs are deterministic.
var fixedKServeTime = time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC)

func newKServeScraper() *KServeInferenceServiceScraper {
	s := NewKServeInferenceServiceScraper(nil)
	s.now = func() time.Time { return fixedKServeTime }
	return s
}

// minimalKServeISVC returns a *unstructured.Unstructured shaped like a
// KServe InferenceService with the field paths the scraper reads.
// Tests override individual paths via unstructured.SetNestedField.
func minimalKServeISVC(namespace, name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("serving.kserve.io/v1beta1")
	u.SetKind("InferenceService")
	u.SetName(name)
	u.SetNamespace(namespace)
	return u
}

func TestKServeInferenceServiceScraper_Name(t *testing.T) {
	if got := newKServeScraper().Name(); got != "inference.kserve" {
		t.Errorf("Name() = %q, want %q", got, "inference.kserve")
	}
}

func TestKServeInferenceServiceScraper_HandlesKind(t *testing.T) {
	s := newKServeScraper()
	cases := []struct {
		kind WorkloadKind
		want bool
	}{
		{WorkloadKind{Group: "serving.kserve.io", Version: "v1beta1", Kind: "InferenceService"}, true},
		// Different version → not handled. A future v1beta2 or v1
		// requires explicit upgrade work; see docs/external-crd-versions.md.
		{WorkloadKind{Group: "serving.kserve.io", Version: "v1", Kind: "InferenceService"}, false},
		{WorkloadKind{Group: "serving.kserve.io", Version: "v1beta2", Kind: "InferenceService"}, false},
		// Adjacent KServe CRDs not yet supported.
		{WorkloadKind{Group: "serving.kserve.io", Version: "v1beta1", Kind: "ServingRuntime"}, false},
		// Other inference kinds handled by InferenceSpecScraper.
		{WorkloadKind{Group: "apps", Version: "v1", Kind: "Deployment"}, false},
		{WorkloadKind{}, false},
	}
	for _, tc := range cases {
		name := tc.kind.Kind + "_" + tc.kind.Version
		t.Run(name, func(t *testing.T) {
			if got := s.HandlesKind(tc.kind); got != tc.want {
				t.Errorf("HandlesKind(%+v) = %v, want %v", tc.kind, got, tc.want)
			}
		})
	}
}

func TestKServeInferenceServiceScraper_NilObject(t *testing.T) {
	s := newKServeScraper()
	_, err := s.Scrape(context.Background(), Workload{
		Kind: WorkloadKind{Group: "serving.kserve.io", Version: "v1beta1", Kind: "InferenceService"},
	}, nil)
	if err == nil {
		t.Fatal("expected error for nil Object")
	}
}

func TestKServeInferenceServiceScraper_WrongObjectType(t *testing.T) {
	// The KServe reconciler must hand us *unstructured.Unstructured.
	// If it ever passes a typed object instead, we fail loudly rather
	// than silently emit nothing.
	s := newKServeScraper()
	w := Workload{
		Kind:   WorkloadKind{Group: "serving.kserve.io", Version: "v1beta1", Kind: "InferenceService"},
		Object: nil,
	}
	// Wrap with a non-Unstructured client.Object: use a corev1 type
	// to ensure the type assertion fails predictably.
	_, err := s.Scrape(context.Background(), w, nil)
	if err == nil {
		t.Fatal("expected error for nil object")
	}
}

func TestKServeInferenceServiceScraper_FullySpecified_HappyPath(t *testing.T) {
	// All four documented fields populated. Verifies the canonical
	// happy-path extraction shape.
	s := newKServeScraper()
	u := minimalKServeISVC("prod-inference", "llama-svc")
	_ = unstructured.SetNestedField(u.Object, "pytorch", "spec", "predictor", "model", "modelFormat", "name")
	_ = unstructured.SetNestedField(u.Object, "2.1", "spec", "predictor", "model", "modelFormat", "version")
	_ = unstructured.SetNestedField(u.Object, "kserve-mlserver", "spec", "predictor", "model", "runtime")
	_ = unstructured.SetNestedField(u.Object, "gs://my-models/llama-3.1-8b/", "spec", "predictor", "model", "storageUri")
	_ = unstructured.SetNestedField(u.Object, "kserve-sa", "spec", "predictor", "serviceAccountName")

	w := Workload{
		Kind:      WorkloadKind{Group: "serving.kserve.io", Version: "v1beta1", Kind: "InferenceService"},
		Category:  CategoryInference,
		Namespace: "prod-inference",
		Name:      "llama-svc",
		Object:    u,
	}
	got, err := s.Scrape(context.Background(), w, nil)
	if err != nil {
		t.Fatalf("Scrape: %v", err)
	}

	// Runtime application Component
	runtime := findComponent(t, got.Components, func(c Component) bool {
		return c.Type == ComponentApplication && c.Name == "pytorch"
	})
	if runtime.Version != "2.1" {
		t.Errorf("runtime.Version = %q, want %q", runtime.Version, "2.1")
	}
	if runtime.Confidence != ConfidenceDeclared {
		t.Errorf("runtime.Confidence = %q, want %q (KServe modelFormat is Declared)",
			runtime.Confidence, ConfidenceDeclared)
	}
	if runtime.Evidence.Source != SourceCRDField {
		t.Errorf("runtime.Evidence.Source = %q, want %q", runtime.Evidence.Source, SourceCRDField)
	}
	if runtime.Properties["kserve.runtime.ref"] != "kserve-mlserver" {
		t.Errorf("runtime ref property = %q, want kserve-mlserver", runtime.Properties["kserve.runtime.ref"])
	}
	if runtime.Properties["kserve.serviceAccountName"] != "kserve-sa" {
		t.Errorf("runtime SA property = %q, want kserve-sa", runtime.Properties["kserve.serviceAccountName"])
	}

	// Model identity Component from storageUri
	model := findComponent(t, got.Components, func(c Component) bool {
		return c.Type == ComponentMLModel && c.Name == "gs://my-models/llama-3.1-8b/"
	})
	if model.Confidence != ConfidenceDeclared {
		t.Errorf("model.Confidence = %q, want %q", model.Confidence, ConfidenceDeclared)
	}
	if model.Evidence.Source != SourceCRDField {
		t.Errorf("model.Evidence.Source = %q, want %q", model.Evidence.Source, SourceCRDField)
	}
	if model.Properties["kserve.serviceAccountName"] != "kserve-sa" {
		t.Errorf("model SA property = %q, want kserve-sa", model.Properties["kserve.serviceAccountName"])
	}

	// Workload-level confidence
	if got.Confidence != ConfidenceDeclared {
		t.Errorf("workload Confidence = %q, want %q", got.Confidence, ConfidenceDeclared)
	}

	// Provenance
	if len(got.Provenance) != 1 {
		t.Fatalf("Provenance entries = %d, want 1", len(got.Provenance))
	}
	if got.Provenance[0].ScraperName != "inference.kserve" {
		t.Errorf("Provenance.ScraperName = %q", got.Provenance[0].ScraperName)
	}
}

func TestKServeInferenceServiceScraper_ModelFormatOnly(t *testing.T) {
	// modelFormat declared, no storageUri. Common for ServingRuntime-
	// backed predictors where the model is supplied externally.
	s := newKServeScraper()
	u := minimalKServeISVC("ns", "isvc")
	_ = unstructured.SetNestedField(u.Object, "tensorflow", "spec", "predictor", "model", "modelFormat", "name")

	got, err := s.Scrape(context.Background(), Workload{
		Kind:   WorkloadKind{Group: "serving.kserve.io", Version: "v1beta1", Kind: "InferenceService"},
		Object: u,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Components) != 1 {
		t.Fatalf("len(Components) = %d, want 1 (runtime only, no storage)", len(got.Components))
	}
	if got.Components[0].Type != ComponentApplication || got.Components[0].Name != "tensorflow" {
		t.Errorf("expected tensorflow application Component, got %+v", got.Components[0])
	}
}

func TestKServeInferenceServiceScraper_StorageUriOnly(t *testing.T) {
	// storageUri declared but no modelFormat. Less common but valid
	// (some KServe predictors set runtime via a separate field).
	s := newKServeScraper()
	u := minimalKServeISVC("ns", "isvc")
	_ = unstructured.SetNestedField(u.Object, "s3://bucket/model/", "spec", "predictor", "model", "storageUri")

	got, err := s.Scrape(context.Background(), Workload{
		Kind:   WorkloadKind{Group: "serving.kserve.io", Version: "v1beta1", Kind: "InferenceService"},
		Object: u,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Components) != 1 {
		t.Fatalf("len(Components) = %d, want 1 (storage only)", len(got.Components))
	}
	if got.Components[0].Type != ComponentMLModel || got.Components[0].Name != "s3://bucket/model/" {
		t.Errorf("expected model Component for storageUri, got %+v", got.Components[0])
	}
}

func TestKServeInferenceServiceScraper_NoneDeclared_ProducesUnresolvedWorkload(t *testing.T) {
	// An InferenceService with no extractable fields — the scraper
	// produces a workload-level Unresolved confidence. The discovery
	// layer treats this as "no inference signal" and skips AIBOM
	// creation.
	s := newKServeScraper()
	u := minimalKServeISVC("ns", "isvc-empty")

	got, err := s.Scrape(context.Background(), Workload{
		Kind:   WorkloadKind{Group: "serving.kserve.io", Version: "v1beta1", Kind: "InferenceService"},
		Object: u,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Confidence != ConfidenceUnresolved {
		t.Errorf("workload Confidence = %q, want %q (no signal)", got.Confidence, ConfidenceUnresolved)
	}
	if len(got.Components) != 0 {
		t.Errorf("len(Components) = %d, want 0", len(got.Components))
	}
}

func TestKServeInferenceServiceScraper_AnnotationModels(t *testing.T) {
	// model.k8saibom.dev/* annotations on the workload object produce
	// ml-model Components, same as for InferenceSpecScraper.
	s := newKServeScraper()
	u := minimalKServeISVC("ns", "isvc")
	u.SetAnnotations(map[string]string{
		"model.k8saibom.dev/artifact": "meta-llama/Llama-3.1-8B-Instruct",
		"app.kubernetes.io/owner":     "alice", // must be ignored
		"model.k8saibom.dev/source":   "huggingface",
	})
	_ = unstructured.SetNestedField(u.Object, "pytorch", "spec", "predictor", "model", "modelFormat", "name")

	got, err := s.Scrape(context.Background(), Workload{
		Kind:   WorkloadKind{Group: "serving.kserve.io", Version: "v1beta1", Kind: "InferenceService"},
		Object: u,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	annModelCount := 0
	for _, c := range got.Components {
		if c.Type == ComponentMLModel && c.Evidence.Source == SourceWorkloadAnnotation {
			annModelCount++
		}
	}
	if annModelCount != 2 {
		t.Errorf("expected 2 model.k8saibom.dev/* annotation components, got %d", annModelCount)
	}
}

func TestKServeInferenceServiceScraper_Deterministic(t *testing.T) {
	// Same Workload twice produces byte-equal BOMInputs (modulo the
	// fixed clock). Same property the InferenceSpecScraper tests lock.
	s := newKServeScraper()
	u := minimalKServeISVC("ns", "isvc")
	_ = unstructured.SetNestedField(u.Object, "pytorch", "spec", "predictor", "model", "modelFormat", "name")
	_ = unstructured.SetNestedField(u.Object, "gs://bucket/model/", "spec", "predictor", "model", "storageUri")
	u.SetAnnotations(map[string]string{
		"model.k8saibom.dev/artifact": "meta-llama/Llama-3.1-8B-Instruct",
	})

	w := Workload{
		Kind:   WorkloadKind{Group: "serving.kserve.io", Version: "v1beta1", Kind: "InferenceService"},
		Object: u,
	}
	first, err := s.Scrape(context.Background(), w, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Scrape(context.Background(), w, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Hash should match — this is the input the reconciler dedups on.
	hashFirst, _ := HashBOMInputs(first)
	hashSecond, _ := HashBOMInputs(second)
	if hashFirst != hashSecond {
		t.Errorf("KServe scrape is not deterministic: %q vs %q", hashFirst, hashSecond)
	}
}

// findComponent returns the first matching Component or fatals the
// test. Used for KServe extraction tests that look up a single
// expected Component by predicate.
func findComponent(t *testing.T, cs []Component, pred func(Component) bool) Component {
	t.Helper()
	for _, c := range cs {
		if pred(c) {
			return c
		}
	}
	t.Fatalf("no matching component in:\n%s", dumpComponents(cs))
	return Component{}
}
