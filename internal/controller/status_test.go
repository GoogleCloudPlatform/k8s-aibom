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

package controller

import (
	"errors"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	aibomv1alpha1 "github.com/GoogleCloudPlatform/k8s-aibom/api/v1alpha1"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/bom"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/scraper"
)

var fixedNow = time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)

// testInlineThresholdBytes is the threshold used in status-builder tests.
// 8 KiB: comfortably above the minimum vLLM-fixture BOM (~1.8 KB) so inline
// tests pass, comfortably below the padded sizes used in truncation tests.
// Passed per-call to BuildStatus per the Checkpoint 5 load-once convention.
const testInlineThresholdBytes int64 = 8192

func newTestStatusBuilder() *StatusBuilder {
	return &StatusBuilder{
		Now: func() time.Time { return fixedNow },
	}
}

func testSummaryOptions() SummaryOptions {
	return SummaryOptions{
		WorkloadKind:       "Deployment",
		WorkloadAPIVersion: "apps/v1",
		WorkloadName:       "vllm-llama3",
		WorkloadNamespace:  "prod-inference",
		WorkloadCategory:   "inference",
	}
}

// buildTestBOM produces a small valid bom.Document for testing the
// StatusBuilder without standing up a full BOM builder pipeline.
func buildTestBOM(t *testing.T, payloadSize int) *bom.Document {
	t.Helper()
	// Use the real BOM builder for a small workload, then pad if needed
	// to exercise threshold logic.
	b := bom.NewBuilder().WithClock(func() time.Time { return fixedNow })
	inputs := &scraper.BOMInputs{
		ScraperName:     "inference.spec",
		ScrapeTimestamp: fixedNow,
		Confidence:      scraper.ConfidenceInferred,
		Components: []scraper.Component{{
			Type:       scraper.ComponentApplication,
			Name:       "vllm",
			Version:    "v0.6.3",
			Confidence: scraper.ConfidenceInferred,
			Evidence:   scraper.Evidence{Source: scraper.SourceImagePattern, Locator: "..."},
			Properties: map[string]string{"runtime.name": "vllm"},
		}, {
			Type:       scraper.ComponentMLModel,
			Name:       "meta-llama/Llama-3.1-8B-Instruct",
			Confidence: scraper.ConfidenceDeclared,
			Evidence:   scraper.Evidence{Source: scraper.SourceContainerArg, Locator: "args[--model]"},
		}},
		Provenance: []scraper.Provenance{{
			ScraperName:     "inference.spec",
			ScraperVersion:  "0.1.0",
			ScrapeMethod:    "spec",
			ScrapeTimestamp: fixedNow,
		}},
	}
	doc, err := b.Build(inputs, bom.BuildOptions{
		WorkloadKind:      "Deployment",
		WorkloadGroup:     "apps",
		WorkloadAPIVer:    "v1",
		WorkloadNamespace: "prod-inference",
		WorkloadName:      "vllm-llama3",
		WorkloadCategory:  "inference",
		ControllerName:    "k8s-aibom",
		ControllerVersion: "0.1.0",
	})
	if err != nil {
		t.Fatalf("buildTestBOM: %v", err)
	}
	// Pad JSON to exceed threshold if payloadSize requires it. Padding
	// is appended as harmless trailing whitespace on the JSON; we don't
	// re-parse or schema-validate the padded form (it's only used to
	// exercise size-threshold logic, not to assert validity).
	for int(doc.Size()) < payloadSize {
		doc.JSON = append(doc.JSON, ' ')
	}
	return doc
}

func TestStatusBuilder_InlineUnderThreshold(t *testing.T) {
	b := newTestStatusBuilder()
	doc := buildTestBOM(t, 0)
	status := b.BuildStatus(doc, testSummaryOptions(), nil, 1, "test-input-hash", testInlineThresholdBytes)

	if status.BOMDocument == nil {
		t.Fatal("BOMDocument is nil")
	}
	if status.BOMDocument.Inline == nil {
		t.Errorf("expected Inline to be set when size %d <= threshold %d",
			doc.Size(), testInlineThresholdBytes)
	}
	if status.BOMDocument.External != nil {
		t.Error("External should be nil when inline succeeds")
	}
	if status.BOMDocument.Truncated {
		t.Error("Truncated should be false when inline succeeds")
	}
	if status.BOMHash != doc.SHA256 {
		t.Errorf("BOMHash = %q, want %q", status.BOMHash, doc.SHA256)
	}
}

func TestStatusBuilder_TruncatedWhenOverThresholdAndNoExternalSink(t *testing.T) {
	b := newTestStatusBuilder()
	doc := buildTestBOM(t, int(testInlineThresholdBytes)+1)
	status := b.BuildStatus(doc, testSummaryOptions(), nil, 1, "test-input-hash", testInlineThresholdBytes)

	if status.BOMDocument == nil {
		t.Fatal("BOMDocument is nil")
	}
	if status.BOMDocument.Inline != nil {
		t.Error("Inline should be nil when over threshold")
	}
	if status.BOMDocument.External != nil {
		t.Error("External should be nil when no sink configured")
	}
	if !status.BOMDocument.Truncated {
		t.Error("Truncated should be true when over threshold and no external sink")
	}
	if status.BOMDocument.TruncationReason == "" {
		t.Error("TruncationReason must be populated")
	}
	// Summary must still be present even when BOM is truncated.
	if status.Summary == nil {
		t.Fatal("Summary should be populated even on truncation")
	}
	if status.Summary.Workload.Name != "vllm-llama3" {
		t.Errorf("Summary.Workload.Name = %q, want %q", status.Summary.Workload.Name, "vllm-llama3")
	}
}

func TestStatusBuilder_ExternalWhenOverThresholdAndSinkSucceeded(t *testing.T) {
	b := newTestStatusBuilder()
	doc := buildTestBOM(t, int(testInlineThresholdBytes)+1)
	results := []SinkResult{
		{Sink: "gcs", URL: "gs://bucket/path.json"},
	}
	status := b.BuildStatus(doc, testSummaryOptions(), results, 1, "test-input-hash", testInlineThresholdBytes)

	if status.BOMDocument == nil || status.BOMDocument.External == nil {
		t.Fatal("expected External BOMDocumentRef when over threshold with successful sink")
	}
	if status.BOMDocument.External.Sink != "gcs" {
		t.Errorf("External.Sink = %q, want %q", status.BOMDocument.External.Sink, "gcs")
	}
	if status.BOMDocument.External.URL != "gs://bucket/path.json" {
		t.Errorf("External.URL = %q", status.BOMDocument.External.URL)
	}
	if status.BOMDocument.External.WriteOnly {
		t.Error("External.WriteOnly should be false for a non-WriteOnly sink result")
	}
	if status.BOMDocument.Truncated {
		t.Error("Truncated should be false when External is set")
	}
}

func TestStatusBuilder_PrefersNonWriteOnlySinks(t *testing.T) {
	// When both a write-only sink (webhook) and a retrievable sink
	// (GCS) succeed, External must point at the retrievable one.
	// Order in the input is webhook-first to verify the selection is
	// based on WriteOnly, not on iteration order.
	b := newTestStatusBuilder()
	doc := buildTestBOM(t, int(testInlineThresholdBytes)+1)
	results := []SinkResult{
		{Sink: "webhook", URL: "https://example.com/sink", WriteOnly: true},
		{Sink: "gcs", URL: "gs://bucket/path.json", WriteOnly: false},
	}
	status := b.BuildStatus(doc, testSummaryOptions(), results, 1, "test-input-hash", testInlineThresholdBytes)

	if status.BOMDocument.External == nil {
		t.Fatal("External is nil")
	}
	if status.BOMDocument.External.Sink != "gcs" {
		t.Errorf("External.Sink = %q, want gcs (non-WriteOnly preferred over WriteOnly)",
			status.BOMDocument.External.Sink)
	}
	if status.BOMDocument.External.WriteOnly {
		t.Error("External.WriteOnly should be false when GCS sink wins")
	}
}

func TestStatusBuilder_FallsBackToWriteOnlySink(t *testing.T) {
	// When only a write-only sink is configured (no GCS), External
	// must point at the write-only sink with WriteOnly=true so
	// auditors know the URL is informational.
	b := newTestStatusBuilder()
	doc := buildTestBOM(t, int(testInlineThresholdBytes)+1)
	results := []SinkResult{
		{Sink: "webhook", URL: "https://example.com/sink", WriteOnly: true},
	}
	status := b.BuildStatus(doc, testSummaryOptions(), results, 1, "test-input-hash", testInlineThresholdBytes)

	if status.BOMDocument.External == nil {
		t.Fatal("External is nil; should fall back to write-only sink")
	}
	if status.BOMDocument.External.Sink != "webhook" {
		t.Errorf("External.Sink = %q, want webhook", status.BOMDocument.External.Sink)
	}
	if !status.BOMDocument.External.WriteOnly {
		t.Error("External.WriteOnly should be true to flag URL as informational")
	}
	if status.BOMDocument.Truncated {
		t.Error("Truncated should be false; webhook delivered the BOM")
	}
}

func TestStatusBuilder_FallsBackToWriteOnlyWhenRetrievableSinkFailed(t *testing.T) {
	// GCS failed, webhook succeeded. External must point at webhook
	// (the only successful sink) with WriteOnly=true.
	b := newTestStatusBuilder()
	doc := buildTestBOM(t, int(testInlineThresholdBytes)+1)
	results := []SinkResult{
		{Sink: "gcs", URL: "", WriteOnly: false, Err: errors.New("bucket gone")},
		{Sink: "webhook", URL: "https://example.com/sink", WriteOnly: true},
	}
	status := b.BuildStatus(doc, testSummaryOptions(), results, 1, "test-input-hash", testInlineThresholdBytes)

	if status.BOMDocument.External == nil {
		t.Fatal("External is nil; webhook should fill in for failed GCS")
	}
	if status.BOMDocument.External.Sink != "webhook" {
		t.Errorf("External.Sink = %q, want webhook", status.BOMDocument.External.Sink)
	}
	if !status.BOMDocument.External.WriteOnly {
		t.Error("External.WriteOnly should be true when webhook is the only successful sink")
	}
}

func TestStatusBuilder_Conditions_HappyPath(t *testing.T) {
	b := newTestStatusBuilder()
	doc := buildTestBOM(t, 0)
	status := b.BuildStatus(doc, testSummaryOptions(), nil, 1, "test-input-hash", testInlineThresholdBytes)

	conds := conditionsByType(status.Conditions)
	if conds[aibomv1alpha1.ConditionReady].Status != metav1.ConditionTrue {
		t.Errorf("Ready = %q, want True", conds[aibomv1alpha1.ConditionReady].Status)
	}
	if conds[aibomv1alpha1.ConditionSynced].Status != metav1.ConditionTrue {
		t.Errorf("Synced = %q, want True", conds[aibomv1alpha1.ConditionSynced].Status)
	}
	if conds[aibomv1alpha1.ConditionSinkFailed].Status != metav1.ConditionFalse {
		t.Errorf("SinkFailed = %q, want False", conds[aibomv1alpha1.ConditionSinkFailed].Status)
	}
	if conds[aibomv1alpha1.ConditionStale].Status != metav1.ConditionFalse {
		t.Errorf("Stale = %q, want False", conds[aibomv1alpha1.ConditionStale].Status)
	}
	// No-external-sink reason on Synced
	if got, want := conds[aibomv1alpha1.ConditionSynced].Reason, aibomv1alpha1.ReasonCRDStatusOnly; got != want {
		t.Errorf("Synced.Reason = %q, want %q", got, want)
	}
}

func TestStatusBuilder_Conditions_SinkFailed(t *testing.T) {
	b := newTestStatusBuilder()
	doc := buildTestBOM(t, 0)
	results := []SinkResult{
		{Sink: "gcs", Err: errors.New("auth failed")},
	}
	status := b.BuildStatus(doc, testSummaryOptions(), results, 1, "test-input-hash", testInlineThresholdBytes)
	conds := conditionsByType(status.Conditions)

	if conds[aibomv1alpha1.ConditionSynced].Status != metav1.ConditionFalse {
		t.Errorf("Synced = %q, want False when sink failed", conds[aibomv1alpha1.ConditionSynced].Status)
	}
	if conds[aibomv1alpha1.ConditionSinkFailed].Status != metav1.ConditionTrue {
		t.Errorf("SinkFailed = %q, want True", conds[aibomv1alpha1.ConditionSinkFailed].Status)
	}
	if got := conds[aibomv1alpha1.ConditionSinkFailed].Message; got == "" {
		t.Error("SinkFailed.Message must name the failure")
	}
}

func TestStatusBuilder_SummaryExtractsRuntimeAndModels(t *testing.T) {
	b := newTestStatusBuilder()
	doc := buildTestBOM(t, 0)
	status := b.BuildStatus(doc, testSummaryOptions(), nil, 1, "test-input-hash", testInlineThresholdBytes)

	if status.Summary == nil {
		t.Fatal("Summary is nil")
	}
	if status.Summary.Runtime == nil {
		t.Fatal("Summary.Runtime is nil; expected vllm runtime detected")
	}
	if status.Summary.Runtime.Name != "vllm" {
		t.Errorf("Runtime.Name = %q, want vllm", status.Summary.Runtime.Name)
	}
	if len(status.Summary.Models) == 0 {
		t.Fatal("expected at least one Model in summary")
	}
	if status.Summary.Models[0].Identity != "meta-llama/Llama-3.1-8B-Instruct" {
		t.Errorf("Models[0].Identity = %q", status.Summary.Models[0].Identity)
	}
}

// ---------- helpers ----------

func conditionsByType(conds []metav1.Condition) map[string]metav1.Condition {
	m := make(map[string]metav1.Condition, len(conds))
	for _, c := range conds {
		m[c.Type] = c
	}
	return m
}
