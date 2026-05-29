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

package bom

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/k8s-aibom/internal/scraper"
)

var fixedBuildTime = time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)

// testDigest is a deterministic 64-char hex string used as a stand-in for
// a real sha256 in fixtures. The CycloneDX schema validates hash content
// against ^[a-fA-F0-9]{N}$ for N in {32,40,64,96,128}; this value is
// guaranteed to pass for SHA-256.
const testDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func newTestBuilder() *Builder {
	return NewBuilder().WithClock(func() time.Time { return fixedBuildTime })
}

func testBuildOptions() BuildOptions {
	return BuildOptions{
		WorkloadKind:      "Deployment",
		WorkloadGroup:     "apps",
		WorkloadAPIVer:    "v1",
		WorkloadNamespace: "prod-inference",
		WorkloadName:      "vllm-llama3",
		WorkloadUID:       "abc-123",
		WorkloadCategory:  "inference",
		ControllerName:    "k8s-aibom",
		ControllerVersion: "0.1.0",
	}
}

func TestBuild_NilInputsReturnsError(t *testing.T) {
	b := newTestBuilder()
	if _, err := b.Build(nil, testBuildOptions()); err == nil {
		t.Fatal("expected error for nil BOMInputs")
	}
}

func TestBuild_EmptyInputs_ValidatesAgainstSchema(t *testing.T) {
	b := newTestBuilder()
	inputs := &scraper.BOMInputs{
		ScraperName:     "inference.spec",
		ScrapeTimestamp: fixedBuildTime,
		Confidence:      scraper.ConfidenceUnresolved,
		Provenance: []scraper.Provenance{{
			ScraperName:     "inference.spec",
			ScraperVersion:  "0.1.0",
			ScrapeMethod:    "spec",
			ScrapeTimestamp: fixedBuildTime,
		}},
	}
	doc, err := b.Build(inputs, testBuildOptions())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	assertValidCycloneDX(t, doc.JSON)
	if doc.Format != FormatCycloneDX {
		t.Errorf("Format = %q, want %q", doc.Format, FormatCycloneDX)
	}
	if doc.Version != "1.6" {
		t.Errorf("Version = %q, want %q", doc.Version, "1.6")
	}
	if len(doc.SHA256) != 64 {
		t.Errorf("SHA256 length = %d, want 64", len(doc.SHA256))
	}
	if doc.CDX == nil {
		t.Error("CDX field is nil")
	}
}

func TestBuild_VLLMWorkload_ValidatesAgainstSchema(t *testing.T) {
	b := newTestBuilder()
	inputs := vllmFixtureBOMInputs()
	doc, err := b.Build(inputs, testBuildOptions())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	assertValidCycloneDX(t, doc.JSON)
}

func TestBuild_AllInternalComponentTypesValidate(t *testing.T) {
	// Exercise every internal ComponentType to make sure each maps to a
	// CycloneDX type the schema accepts. If a future internal type is
	// added without a mapping, the toCDXComponent fail-closed path emits
	// application — this test makes sure that's at least valid.
	b := newTestBuilder()
	inputs := &scraper.BOMInputs{
		ScraperName:     "inference.spec",
		ScrapeTimestamp: fixedBuildTime,
		Confidence:      scraper.ConfidenceDeclared,
		Components: []scraper.Component{
			{
				Type: scraper.ComponentContainer, Name: "vllm/vllm-openai",
				Version:    "v0.6.3",
				Hashes:     map[string]string{"sha256": testDigest},
				Evidence:   scraper.Evidence{Source: scraper.SourceImageReference, Locator: "spec.template.spec.containers[0].image"},
				Confidence: scraper.ConfidenceDeclared,
			},
			{
				Type: scraper.ComponentApplication, Name: "vllm",
				Version:    "v0.6.3",
				Evidence:   scraper.Evidence{Source: scraper.SourceImagePattern, Locator: "..."},
				Confidence: scraper.ConfidenceInferred,
			},
			{
				Type: scraper.ComponentMLModel, Name: "meta-llama/Llama-3.1-8B-Instruct",
				Evidence:   scraper.Evidence{Source: scraper.SourceEnvVar, Locator: "...env[HF_MODEL_ID]"},
				Confidence: scraper.ConfidenceInferred,
			},
			{
				Type: scraper.ComponentData, Name: "llama-weights",
				Evidence:   scraper.Evidence{Source: scraper.SourceVolumeSource, Locator: "...volumeMounts[0]"},
				Confidence: scraper.ConfidenceInferred,
			},
		},
		Provenance: []scraper.Provenance{{
			ScraperName:     "inference.spec",
			ScraperVersion:  "0.1.0",
			ScrapeMethod:    "spec",
			ScrapeTimestamp: fixedBuildTime,
		}},
	}
	doc, err := b.Build(inputs, testBuildOptions())
	if err != nil {
		t.Fatal(err)
	}
	assertValidCycloneDX(t, doc.JSON)
	// Spot-check that the workload metadata.component is populated.
	if doc.CDX.Metadata == nil || doc.CDX.Metadata.Component == nil {
		t.Fatal("missing metadata.component")
	}
	if doc.CDX.Metadata.Component.Name != "vllm-llama3" {
		t.Errorf("metadata.component.Name = %q, want %q",
			doc.CDX.Metadata.Component.Name, "vllm-llama3")
	}
	// Components should include all four internal kinds.
	if doc.CDX.Components == nil || len(*doc.CDX.Components) != 4 {
		t.Errorf("expected 4 components, got %d", componentCount(doc))
	}
}

func TestBuild_Deterministic_ByteIdenticalAcrossInvocations(t *testing.T) {
	// Determinism is a precondition for the reconciler's hash-based dedup.
	// Identical inputs + identical clock must produce byte-identical JSON
	// and identical SHA256.
	b := newTestBuilder()
	inputs := vllmFixtureBOMInputs()
	opts := testBuildOptions()

	first, err := b.Build(inputs, opts)
	if err != nil {
		t.Fatal(err)
	}
	second, err := b.Build(inputs, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.JSON, second.JSON) {
		t.Errorf("Build is not byte-deterministic.\nfirst:\n%s\n\nsecond:\n%s",
			first.JSON, second.JSON)
	}
	if first.SHA256 != second.SHA256 {
		t.Errorf("SHA256 differs across invocations: %q vs %q", first.SHA256, second.SHA256)
	}
}

func TestBuild_EvidenceAndConfidence_AppearOnEveryComponent(t *testing.T) {
	// The core auditor promise: every component carries Evidence.source,
	// Evidence.locator, and aibom.confidence as properties. Walk the
	// produced BOM and verify.
	b := newTestBuilder()
	doc, err := b.Build(vllmFixtureBOMInputs(), testBuildOptions())
	if err != nil {
		t.Fatal(err)
	}
	if doc.CDX.Components == nil {
		t.Fatal("no components in BOM")
	}
	for i, c := range *doc.CDX.Components {
		got := map[string]bool{}
		if c.Properties != nil {
			for _, p := range *c.Properties {
				got[p.Name] = true
			}
		}
		for _, required := range []string{"aibom.confidence", "aibom.evidence.source", "aibom.evidence.locator"} {
			if !got[required] {
				t.Errorf("component[%d] (%s/%s) missing required property %q", i, c.Type, c.Name, required)
			}
		}
	}
}

func TestBuild_MetadataTimestampUsesInjectedClock(t *testing.T) {
	b := newTestBuilder()
	doc, err := b.Build(vllmFixtureBOMInputs(), testBuildOptions())
	if err != nil {
		t.Fatal(err)
	}
	wantTS := "2026-05-10T12:00:00Z"
	if doc.CDX.Metadata.Timestamp != wantTS {
		t.Errorf("metadata.timestamp = %q, want %q", doc.CDX.Metadata.Timestamp, wantTS)
	}
}

func TestBuild_BOMFormatAndSpecVersion(t *testing.T) {
	// Smoke-test the two top-level wire-format fields. Reaching the
	// schema validation step requires these to be exactly right.
	b := newTestBuilder()
	doc, err := b.Build(vllmFixtureBOMInputs(), testBuildOptions())
	if err != nil {
		t.Fatal(err)
	}
	if doc.CDX.BOMFormat != "CycloneDX" {
		t.Errorf("bomFormat = %q, want %q", doc.CDX.BOMFormat, "CycloneDX")
	}
	// Parse the JSON to confirm the wire encoding of specVersion is "1.6".
	var raw map[string]any
	if err := json.Unmarshal(doc.JSON, &raw); err != nil {
		t.Fatal(err)
	}
	if v, _ := raw["specVersion"].(string); v != "1.6" {
		t.Errorf("specVersion (wire) = %q, want %q", v, "1.6")
	}
}

func TestBuild_HashesPresentForResolvedDigest(t *testing.T) {
	b := newTestBuilder()
	doc, err := b.Build(vllmFixtureBOMInputs(), testBuildOptions())
	if err != nil {
		t.Fatal(err)
	}
	// At least one component must carry a SHA256 hash (the container).
	found := false
	for _, c := range *doc.CDX.Components {
		if c.Hashes != nil {
			for _, h := range *c.Hashes {
				if string(h.Algorithm) == "SHA-256" && h.Value == testDigest {
					found = true
				}
			}
		}
	}
	if !found {
		t.Errorf("expected a SHA-256 hash with the test digest on the container component; got components: %+v", *doc.CDX.Components)
	}
}

func TestBuild_WorkloadMetadataPropertiesPresent(t *testing.T) {
	b := newTestBuilder()
	doc, err := b.Build(vllmFixtureBOMInputs(), testBuildOptions())
	if err != nil {
		t.Fatal(err)
	}
	props := map[string]string{}
	if doc.CDX.Metadata.Component.Properties != nil {
		for _, p := range *doc.CDX.Metadata.Component.Properties {
			props[p.Name] = p.Value
		}
	}
	cases := map[string]string{
		"aibom.workload.kind":      "Deployment",
		"aibom.workload.group":     "apps",
		"aibom.workload.namespace": "prod-inference",
		"aibom.workload.name":      "vllm-llama3",
		"aibom.workload.category":  "inference",
	}
	for k, want := range cases {
		if got := props[k]; got != want {
			t.Errorf("workload metadata property %q = %q, want %q", k, got, want)
		}
	}
}

func TestBuild_AggregateConfidenceInMetadataProperties(t *testing.T) {
	b := newTestBuilder()
	inputs := vllmFixtureBOMInputs() // aggregate is Inferred
	doc, err := b.Build(inputs, testBuildOptions())
	if err != nil {
		t.Fatal(err)
	}
	found := ""
	if doc.CDX.Metadata.Properties != nil {
		for _, p := range *doc.CDX.Metadata.Properties {
			if p.Name == "aibom.confidence" {
				found = p.Value
			}
		}
	}
	if found != string(scraper.ConfidenceInferred) {
		t.Errorf("metadata aibom.confidence = %q, want %q", found, scraper.ConfidenceInferred)
	}
}

func TestBuild_UnknownHashAlgorithm_FailsFast(t *testing.T) {
	// Defensive-correctness contract: the builder returns an error rather
	// than passing an invalid BOM downstream. The error message must name
	// the offending key AND the supported set, so the fix is obvious.
	b := newTestBuilder()
	inputs := &scraper.BOMInputs{
		ScraperName:     "inference.spec",
		ScrapeTimestamp: fixedBuildTime,
		Components: []scraper.Component{{
			Type:     scraper.ComponentContainer,
			Name:     "x",
			Hashes:   map[string]string{"sha512_256": testDigest}, // not in supported set
			Evidence: scraper.Evidence{Source: scraper.SourceImageReference, Locator: "..."},
		}},
	}
	_, err := b.Build(inputs, testBuildOptions())
	if err == nil {
		t.Fatal("expected error for unknown hash algorithm")
	}
	msg := err.Error()
	for _, want := range []string{"sha512_256", "sha256", "supported"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message missing expected substring %q: %v", want, err)
		}
	}
}

func TestBuild_JSONIsCompactNoIndent(t *testing.T) {
	// The reconciler's hash-based dedup hashes the JSON bytes. Indented
	// JSON would produce different bytes for equivalent BOMs depending
	// on encoder settings. We commit to non-indented output.
	b := newTestBuilder()
	doc, err := b.Build(vllmFixtureBOMInputs(), testBuildOptions())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(doc.JSON), "\n  ") {
		t.Error("BOM JSON contains indentation; should be compact")
	}
}

// ---------- helpers ----------

func vllmFixtureBOMInputs() *scraper.BOMInputs {
	return &scraper.BOMInputs{
		ScraperName:     "inference.spec",
		ScrapeTimestamp: fixedBuildTime,
		Confidence:      scraper.ConfidenceInferred,
		Components: []scraper.Component{
			{
				Type:    scraper.ComponentApplication,
				Name:    "vllm",
				Version: "v0.6.3",
				Evidence: scraper.Evidence{
					Source: scraper.SourceImagePattern, Locator: "spec.template.spec.containers[0].image (pattern: ^vllm/.*)",
				},
				Confidence: scraper.ConfidenceInferred,
				Properties: map[string]string{
					"runtime.name":    "vllm",
					"runtime.pattern": "^vllm/.*",
					"container.name":  "vllm",
					"image.tag":       "v0.6.3",
				},
			},
			{
				Type:    scraper.ComponentContainer,
				Name:    "vllm/vllm-openai",
				Version: "v0.6.3",
				Hashes:  map[string]string{"sha256": testDigest},
				Evidence: scraper.Evidence{
					Source: scraper.SourcePodStatus, Locator: "status.containerStatuses[name=vllm].imageID",
				},
				Confidence: scraper.ConfidenceDeclared,
				Properties: map[string]string{
					"image.reference": "vllm/vllm-openai:v0.6.3",
					"container.name":  "vllm",
					"container.init":  "false",
				},
			},
			{
				Type: scraper.ComponentMLModel,
				Name: "meta-llama/Llama-3.1-8B-Instruct",
				Evidence: scraper.Evidence{
					Source: scraper.SourceContainerArg, Locator: "spec.template.spec.containers[0].args[0 1](--model)",
				},
				Confidence: scraper.ConfidenceDeclared,
				Properties: map[string]string{
					"identity.confidence": "claimed",
					"identity.argFlag":    "--model",
					"container.name":      "vllm",
				},
			},
		},
		Provenance: []scraper.Provenance{{
			ScraperName:     "inference.spec",
			ScraperVersion:  "0.1.0",
			ScrapeMethod:    "spec",
			ScrapeTimestamp: fixedBuildTime,
		}},
	}
}

func componentCount(d *Document) int {
	if d.CDX.Components == nil {
		return 0
	}
	return len(*d.CDX.Components)
}
