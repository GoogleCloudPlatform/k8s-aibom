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
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	cdx "github.com/CycloneDX/cyclonedx-go"
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

// The consumer walkthroughs in docs/integrations upload these documents
// as-is, so they must stay valid as the schema pin moves.
func TestIntegrationFixturesAreValidCycloneDX(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "docs", "integrations", "fixtures", "*.cdx.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no fixtures found under docs/integrations/fixtures")
	}
	for _, p := range paths {
		t.Run(filepath.Base(p), func(t *testing.T) {
			b, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			assertValidCycloneDX(t, b)
			assertIntegrationFixtureDependencies(t, b)
		})
	}
}

// Schema validation alone permits a missing graph or dangling bom-refs.
// These walkthrough fixtures must carry the builder's one-level graph.
func assertIntegrationFixtureDependencies(t *testing.T, data []byte) {
	t.Helper()
	var doc cdx.BOM
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Metadata == nil || doc.Metadata.Component == nil || doc.Metadata.Component.BOMRef == "" {
		t.Fatal("fixture has no workload bom-ref")
	}
	if doc.Components == nil || len(*doc.Components) == 0 {
		t.Fatal("fixture has no top-level components")
	}
	if doc.Dependencies == nil || len(*doc.Dependencies) != 1 {
		t.Fatal("fixture must have exactly one workload dependency entry")
	}
	dep := (*doc.Dependencies)[0]
	if dep.Ref != doc.Metadata.Component.BOMRef {
		t.Fatalf("dependency ref = %q, want workload %q", dep.Ref, doc.Metadata.Component.BOMRef)
	}
	want := make([]string, 0, len(*doc.Components))
	for _, component := range *doc.Components {
		want = append(want, component.BOMRef)
	}
	var got []string
	if dep.Dependencies != nil {
		got = slices.Clone(*dep.Dependencies)
	}
	slices.Sort(want)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("workload dependencies = %q, want every top-level component %q", got, want)
	}
}
