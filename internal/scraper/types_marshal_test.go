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
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

// JSONExposedTypes are the internal types whose shape we want to keep
// JSON-stable so they can be mirrored into deliberately designed
// api/v1alpha1 types or used in BOM properties. Workload and BOMInputs are
// intentionally excluded — Workload carries a client.Object that doesn't
// round-trip generically, and BOMInputs.Errors is []error (internal-only).
//
// If you add a new type to this set, also add it to the marshal round-trip
// test cases below.
var JSONExposedTypes = []reflect.Type{
	reflect.TypeOf(Component{}),
	reflect.TypeOf(Service{}),
	reflect.TypeOf(Provenance{}),
	reflect.TypeOf(Evidence{}),
	reflect.TypeOf(SignatureClaim{}),
	reflect.TypeOf(SignatureResult{}),
}

// TestJSONExposedTypes_AllExportedFieldsHaveJSONTag walks the JSONExposedTypes
// reflectively and asserts every exported field carries a `json:` struct
// tag. Catches accidental new fields landing without a tag, which would
// expose Go-style PascalCase names in JSON output.
func TestJSONExposedTypes_AllExportedFieldsHaveJSONTag(t *testing.T) {
	for _, rt := range JSONExposedTypes {
		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
			if !f.IsExported() {
				continue
			}
			if _, ok := f.Tag.Lookup("json"); !ok {
				t.Errorf("%s.%s: missing json: struct tag", rt.Name(), f.Name)
			}
		}
	}
}

func TestComponentJSONRoundTrip(t *testing.T) {
	in := Component{
		Type:    ComponentMLModel,
		Name:    "meta-llama/Llama-3.1-8B-Instruct",
		Version: "main",
		PURL:    "pkg:huggingface/meta-llama/Llama-3.1-8B-Instruct@main",
		Hashes:  map[string]string{"sha256": "deadbeef"},
		Evidence: Evidence{
			Source:  SourceEnvVar,
			Locator: "spec.template.spec.containers[0].env[HF_MODEL_ID]",
		},
		Confidence: ConfidenceDeclared,
		Properties: map[string]string{
			"identity.confidence":  "claimed",
			"hardware.accelerator": "nvidia-h100-80gb",
		},
		Children: []Component{
			{
				Type:    ComponentApplication,
				Name:    "transformers",
				Version: "4.45.0",
				Evidence: Evidence{
					Source:  SourceImageLabel,
					Locator: "config.Labels.transformers-version",
				},
			},
		},
	}
	assertRoundTrip(t, &in, new(Component))
}

func TestServiceJSONRoundTrip(t *testing.T) {
	in := Service{
		Name:      "vllm-llama3",
		Endpoints: []string{"http://vllm-llama3.prod-inference.svc.cluster.local:8000"},
		Evidence:  Evidence{Source: SourceCRDField, Locator: "Service.spec.clusterIP"},
	}
	assertRoundTrip(t, &in, new(Service))
}

func TestProvenanceJSONRoundTrip(t *testing.T) {
	in := Provenance{
		ScraperName:     "inference.spec",
		ScraperVersion:  "0.1.0",
		ScrapeMethod:    "spec",
		ScrapeTimestamp: time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC),
	}
	assertRoundTrip(t, &in, new(Provenance))
}

func TestEvidenceJSONRoundTrip(t *testing.T) {
	in := Evidence{
		Source:  SourcePodStatus,
		Locator: "status.containerStatuses[0].imageID",
	}
	assertRoundTrip(t, &in, new(Evidence))
}

func TestSignatureClaimJSONRoundTrip(t *testing.T) {
	in := SignatureClaim{
		ModelIdentity: "meta-llama/Llama-3.1-8B-Instruct",
		SignatureRef:  "rekor://example/12345",
		Evidence: Evidence{
			Source:  SourceWorkloadAnnotation,
			Locator: "metadata.annotations[model.k8saibom.dev/oms-signature]",
		},
	}
	assertRoundTrip(t, &in, new(SignatureClaim))
}

func TestSignatureResultJSONRoundTrip(t *testing.T) {
	in := SignatureResult{
		Status:     SignatureClaimed,
		Identity:   "",
		RekorEntry: "",
		Timestamp:  time.Time{},
	}
	assertRoundTrip(t, &in, new(SignatureResult))
}

// TestZeroValuesOmitProperly checks that the omitempty / omitzero tags work
// the way we expect: marshaling a zero-valued instance of each type
// produces "{}". If this fails we are leaking zero fields into output,
// which is noisy for auditors and makes byte-stability harder.
func TestZeroValuesOmitProperly(t *testing.T) {
	cases := []struct {
		name string
		v    any
	}{
		{"Component", Component{}},
		{"Service", Service{}},
		{"Provenance", Provenance{}},
		{"Evidence", Evidence{}},
		{"SignatureClaim", SignatureClaim{}},
		{"SignatureResult", SignatureResult{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(tc.v)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(b) != "{}" {
				t.Errorf("zero-valued %s marshaled to %q, want %q", tc.name, string(b), "{}")
			}
		})
	}
}

// assertRoundTrip marshals in, unmarshals into out (same concrete type
// pointed-to), and verifies the result deep-equals the input.
func assertRoundTrip(t *testing.T, in, out any) {
	t.Helper()
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatalf("unmarshal: %v\nbytes: %s", err, b)
	}
	// Compare pointed-to values.
	gotV := reflect.ValueOf(out).Elem().Interface()
	inV := reflect.ValueOf(in).Elem().Interface()
	if !reflect.DeepEqual(inV, gotV) {
		t.Errorf("round-trip mismatch.\ninput : %#v\noutput: %#v\nbytes : %s", inV, gotV, b)
	}
}
