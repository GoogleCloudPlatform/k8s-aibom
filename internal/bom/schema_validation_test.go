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
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// CycloneDX 1.6 schema validation infrastructure.
//
// The schemas live in testdata/cyclonedx-1.6/ and are pinned to a specific
// upstream tag (see testdata/cyclonedx-1.6/README.md). They are loaded
// from disk for every test run. The schema is the source of truth — the
// cyclonedx-go library is treated as one implementation that may be
// lenient or stale relative to the spec. If the library and the schema
// disagree, this test catches it before customers do.

var (
	loadOnce   sync.Once
	cdxSchema  *jsonschema.Schema
	schemaErr  error
	schemaDir  = filepath.Join("testdata", "cyclonedx-1.6")
	mainSchema = "http://cyclonedx.org/schema/bom-1.6.schema.json"
)

func loadCycloneDXSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	loadOnce.Do(func() {
		c := jsonschema.NewCompiler()
		for _, item := range []struct {
			path string
			url  string
		}{
			{"bom-1.6.schema.json", "http://cyclonedx.org/schema/bom-1.6.schema.json"},
			{"spdx.schema.json", "http://cyclonedx.org/schema/spdx.schema.json"},
			{"jsf-0.82.schema.json", "http://cyclonedx.org/schema/jsf-0.82.schema.json"},
		} {
			b, err := os.ReadFile(filepath.Join(schemaDir, item.path))
			if err != nil {
				schemaErr = err
				return
			}
			doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
			if err != nil {
				schemaErr = err
				return
			}
			if err := c.AddResource(item.url, doc); err != nil {
				schemaErr = err
				return
			}
		}
		cdxSchema, schemaErr = c.Compile(mainSchema)
	})
	if schemaErr != nil {
		t.Fatalf("loading CycloneDX 1.6 schemas: %v", schemaErr)
	}
	return cdxSchema
}

// assertValidCycloneDX validates jsonBytes against the vendored CycloneDX
// 1.6 JSON schema. It reports validation errors with detail so the
// failure message points at the offending JSON pointer.
func assertValidCycloneDX(t *testing.T, jsonBytes []byte) {
	t.Helper()
	sch := loadCycloneDXSchema(t)
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(jsonBytes))
	if err != nil {
		t.Fatalf("unmarshal generated BOM for validation: %v\nbom:\n%s", err, jsonBytes)
	}
	if err := sch.Validate(doc); err != nil {
		t.Fatalf("BOM failed CycloneDX 1.6 schema validation:\n%v\n\nBOM JSON:\n%s", err, jsonBytes)
	}
}

func TestSchemaLoadsSuccessfully(t *testing.T) {
	// This test exists so a missing or malformed schema file fails with
	// a clear "the schemas are broken" message rather than appearing as a
	// validation failure in every other test.
	if got := loadCycloneDXSchema(t); got == nil {
		t.Fatal("loadCycloneDXSchema returned nil schema with no error")
	}
}
