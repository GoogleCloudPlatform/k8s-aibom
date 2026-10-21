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
