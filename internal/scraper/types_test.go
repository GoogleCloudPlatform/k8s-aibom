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

import "testing"

// These tests pin the wire-visible string values of the public enums. The
// enum values surface in BOM output and (transitively) in CR-status field
// values that auditors read, so changing them is a backward-incompatible
// API change. If you intend to rename one, update the test deliberately
// and bump the API version.

func TestWorkloadCategoryStringValues(t *testing.T) {
	cases := map[WorkloadCategory]string{
		CategoryInference:  "inference",
		CategoryTraining:   "training",
		CategoryAgent:      "agent",
		CategoryEvaluation: "evaluation",
		CategoryPipeline:   "pipeline",
		CategoryNotebook:   "notebook",
	}
	for got, want := range cases {
		if string(got) != want {
			t.Errorf("WorkloadCategory %q got string %q, want %q", got, string(got), want)
		}
	}
}

func TestEvidenceSourceStringValues(t *testing.T) {
	cases := map[EvidenceSource]string{
		SourcePodAnnotation:      "pod_annotation",
		SourceWorkloadAnnotation: "workload_annotation",
		SourceContainerArg:       "container_arg",
		SourceInitContainerArg:   "init_container_arg",
		SourceEnvVar:             "env_var",
		SourceEnvVarNamePresent:  "env_var_name_present",
		SourceImagePattern:       "image_pattern",
		SourceImageLabel:         "image_label",
		SourceCRDField:           "crd_field",
		SourceVolumeSource:       "volume_source",
		SourceResourceRequest:    "resource_request",
		SourceNodeSelector:       "node_selector",
		SourcePodStatus:          "pod_status",
	}
	for got, want := range cases {
		if string(got) != want {
			t.Errorf("EvidenceSource %q got string %q, want %q", got, string(got), want)
		}
	}
}

func TestConfidenceStringValues(t *testing.T) {
	cases := map[Confidence]string{
		ConfidenceDeclared:   "declared",
		ConfidenceInferred:   "inferred",
		ConfidenceUnresolved: "unresolved",
	}
	for got, want := range cases {
		if string(got) != want {
			t.Errorf("Confidence %q got string %q, want %q", got, string(got), want)
		}
	}
}

func TestComponentTypeStringValues(t *testing.T) {
	cases := map[ComponentType]string{
		ComponentContainer:   "container",
		ComponentApplication: "application",
		ComponentMLModel:     "machine-learning-model",
		ComponentData:        "data",
	}
	for got, want := range cases {
		if string(got) != want {
			t.Errorf("ComponentType %q got string %q, want %q", got, string(got), want)
		}
	}
}

// TestWorkloadKindZeroValue documents the zero-value behavior of
// WorkloadKind. A zero WorkloadKind has all-empty strings, which would
// never match any scraper's HandlesKind — that's intentional so accidental
// zero values fail closed (no scraper runs) rather than silently matching.
func TestWorkloadKindZeroValue(t *testing.T) {
	var k WorkloadKind
	if k.Group != "" || k.Version != "" || k.Kind != "" {
		t.Errorf("zero WorkloadKind has non-empty fields: %+v", k)
	}
}
