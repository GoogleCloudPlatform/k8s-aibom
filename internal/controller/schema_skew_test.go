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
	"fmt"
	"strings"
	"testing"
)

// openAPIDocWithSpecFields builds the minimal OpenAPI v3 shape the
// checker reads: components.schemas[<name>].properties.spec.properties.
func openAPIDocWithSpecFields(fields ...string) []byte {
	props := make([]string, 0, len(fields))
	for _, f := range fields {
		props = append(props, fmt.Sprintf(`%q: {"type":"object"}`, f))
	}
	return []byte(fmt.Sprintf(`{"components":{"schemas":{%q:{"properties":{"spec":{"properties":{%s}}}}}}}`,
		openAPIConfigSchemaName, strings.Join(props, ",")))
}

func TestExpectedSpecFields_IncludesVerification(t *testing.T) {
	got := expectedSpecFields()
	found := false
	for _, f := range got {
		if f == "verification" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expectedSpecFields() = %v; must include verification (derived from the Go type, not hard-coded)", got)
	}
}

func TestServedSpecFields_ParsesAndDiffs(t *testing.T) {
	expected := expectedSpecFields()

	// Current schema: every compiled-in field present → no skew.
	served, err := servedSpecFieldsFromOpenAPI(openAPIDocWithSpecFields(expected...))
	if err != nil {
		t.Fatalf("parse current: %v", err)
	}
	if m := missingFields(expected, served); len(m) != 0 {
		t.Fatalf("current schema reported skew: %v", m)
	}

	// v1.3.0-era schema: verification absent → exactly that field.
	var older []string
	for _, f := range expected {
		if f != "verification" {
			older = append(older, f)
		}
	}
	served, err = servedSpecFieldsFromOpenAPI(openAPIDocWithSpecFields(older...))
	if err != nil {
		t.Fatalf("parse older: %v", err)
	}
	if m := missingFields(expected, served); len(m) != 1 || m[0] != "verification" {
		t.Fatalf("older schema skew = %v, want [verification]", m)
	}
}

func TestServedSpecFields_ErrorsAreNotSkew(t *testing.T) {
	// Missing schema / malformed doc must surface as errors (callers
	// treat errors as "unknown"), never as an empty served set that
	// would read as "everything missing".
	if _, err := servedSpecFieldsFromOpenAPI([]byte(`{"components":{"schemas":{}}}`)); err == nil {
		t.Fatal("schema-not-found must error")
	}
	if _, err := servedSpecFieldsFromOpenAPI([]byte(`not json`)); err == nil {
		t.Fatal("malformed doc must error")
	}
}

// openAPIDocWithNestedSpec builds a spec whose "discovery" object declares
// the given child properties, next to a plain "verification" object.
func openAPIDocWithNestedSpec(discoveryChildren ...string) []byte {
	props := make([]string, 0, len(discoveryChildren))
	for _, f := range discoveryChildren {
		props = append(props, fmt.Sprintf(`%q: {"type":"string"}`, f))
	}
	return []byte(fmt.Sprintf(`{"components":{"schemas":{%q:{"properties":{"spec":{"properties":{"discovery":{"type":"object","properties":{%s}},"verification":{"type":"object"},"bomGeneration":{"type":"object"},"logging":{"type":"object"},"sinks":{"type":"array"}}}}}}}}`,
		openAPIConfigSchemaName, strings.Join(props, ",")))
}

func TestExpectedSpecFields_IncludesNestedWorkloadKinds(t *testing.T) {
	got := expectedSpecFields()
	for _, want := range []string{"discovery", "discovery.workloadKinds", "discovery.namespaceSelector", "verification.enabled"} {
		found := false
		for _, g := range got {
			if g == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expectedSpecFields() = %v; must include %q (derived from the Go types)", got, want)
		}
	}
}

// The 1.6.0 shape: 1.5.1 CRDs declare discovery with its two older
// children and no workloadKinds. The nested path must be reported.
func TestServedSpecFields_NestedFieldMissingIsSkew(t *testing.T) {
	served, err := servedSpecFieldsFromOpenAPI(openAPIDocWithNestedSpec("namespaceSelector", "inferenceRuntimeImagePatterns"))
	if err != nil {
		t.Fatal(err)
	}
	m := missingFields([]string{"discovery", "discovery.workloadKinds", "discovery.namespaceSelector"}, served)
	if len(m) != 1 || m[0] != "discovery.workloadKinds" {
		t.Fatalf("missing = %v, want [discovery.workloadKinds]", m)
	}
	served, err = servedSpecFieldsFromOpenAPI(openAPIDocWithNestedSpec("namespaceSelector", "inferenceRuntimeImagePatterns", "workloadKinds"))
	if err != nil {
		t.Fatal(err)
	}
	if m := missingFields([]string{"discovery", "discovery.workloadKinds"}, served); len(m) != 0 {
		t.Fatalf("missing = %v, want none once the child is declared", m)
	}
}

// A parent whose children the served schema does not describe (no
// properties, as with a $ref or preserve-unknown-fields object) must not
// have its children reported: "cannot tell" is not skew.
func TestServedSpecFields_UndescribedChildrenAreNotSkew(t *testing.T) {
	served, err := servedSpecFieldsFromOpenAPI(openAPIDocWithSpecFields("discovery", "verification"))
	if err != nil {
		t.Fatal(err)
	}
	if m := missingFields([]string{"discovery", "discovery.workloadKinds", "verification", "verification.enabled"}, served); len(m) != 0 {
		t.Fatalf("missing = %v, want none when the parents declare no properties", m)
	}
}

func TestSchemaSkewMessage_NamesFieldsAndRemedy(t *testing.T) {
	msg := schemaSkewMessage([]string{"verification"})
	for _, want := range []string{
		"verification", "pruned", "OFF", "restart",
		"If the k8s-aibom-crds chart manages these CRDs, upgrade it",
		"kubectl apply --server-side --force-conflicts --field-manager=k8s-aibom-crds -f -",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q: %s", want, msg)
		}
	}
}
