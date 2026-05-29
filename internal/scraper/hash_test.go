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
	"strings"
	"testing"
	"time"
)

// fixturePopulatedBOMInputs returns a BOMInputs with every type-graph
// branch exercised (slices, structs, both Component map fields, multiple
// children, Service, multiple Provenance entries). Used as the input
// to determinism, sensitivity, and timestamp-insensitivity tests.
func fixturePopulatedBOMInputs() *BOMInputs {
	t1 := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	return &BOMInputs{
		ScraperName:     "inference.spec",
		ScrapeTimestamp: t1,
		Confidence:      ConfidenceInferred,
		Components: []Component{
			{
				Type: ComponentApplication, Name: "vllm", Version: "v0.6.3",
				Confidence: ConfidenceInferred,
				Evidence:   Evidence{Source: SourceImagePattern, Locator: "x"},
				Properties: map[string]string{"runtime.name": "vllm", "image.tag": "v0.6.3"},
			},
			{
				Type: ComponentContainer, Name: "vllm/vllm-openai", Version: "v0.6.3",
				Hashes:     map[string]string{"sha256": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
				Confidence: ConfidenceDeclared,
				Evidence:   Evidence{Source: SourcePodStatus, Locator: "y"},
				Properties: map[string]string{"image.reference": "vllm/vllm-openai:v0.6.3"},
				Children: []Component{
					{Type: ComponentData, Name: "child-data"},
				},
			},
		},
		Services: []Service{
			{Name: "vllm-svc", Endpoints: []string{"http://svc.ns.svc/"}, Evidence: Evidence{Source: SourceCRDField, Locator: "z"}},
		},
		Provenance: []Provenance{
			{ScraperName: "inference.spec", ScraperVersion: "0.1.0", ScrapeMethod: "spec", ScrapeTimestamp: t1},
		},
	}
}

// TestHashBOMInputs_DeterministicAcrossInvocations is the runtime
// property check: hashing the same fixture many times yields the
// same digest every time. If a future refactor accidentally breaks
// determinism — e.g., by introducing a non-sorted map iteration
// somewhere — this test catches it directly.
func TestHashBOMInputs_DeterministicAcrossInvocations(t *testing.T) {
	in := fixturePopulatedBOMInputs()
	first, err := HashBOMInputs(in)
	if err != nil {
		t.Fatalf("first hash: %v", err)
	}
	for i := 0; i < 100; i++ {
		got, err := HashBOMInputs(in)
		if err != nil {
			t.Fatalf("hash iteration %d: %v", i, err)
		}
		if got != first {
			t.Fatalf("hash diverged at iteration %d: %q != %q", i, got, first)
		}
	}
	if len(first) != 64 {
		t.Errorf("hash length = %d, want 64 (hex sha256)", len(first))
	}
}

// TestHashBOMInputs_TimestampInsensitive confirms the core dedup
// promise: the hash is the same when the only thing that differs
// between two BOMInputs is timestamp fields.
func TestHashBOMInputs_TimestampInsensitive(t *testing.T) {
	a := fixturePopulatedBOMInputs()
	b := fixturePopulatedBOMInputs()
	// Move every timestamp by an hour.
	b.ScrapeTimestamp = b.ScrapeTimestamp.Add(time.Hour)
	for i := range b.Provenance {
		b.Provenance[i].ScrapeTimestamp = b.Provenance[i].ScrapeTimestamp.Add(time.Hour)
	}
	ha, _ := HashBOMInputs(a)
	hb, _ := HashBOMInputs(b)
	if ha != hb {
		t.Errorf("timestamps affected hash: %q vs %q (must not happen — dedup design depends on this)", ha, hb)
	}
}

// TestHashBOMInputs_SensitiveToContentChanges asserts that every
// content-bearing field, when changed, changes the hash. The list
// here is the per-field expectation surface auditors would care
// about: if a future refactor accidentally drops some field from
// the hash input, this test catches it.
func TestHashBOMInputs_SensitiveToContentChanges(t *testing.T) {
	base := fixturePopulatedBOMInputs()
	baseHash, _ := HashBOMInputs(base)

	cases := []struct {
		name   string
		mutate func(*BOMInputs)
	}{
		{"scraperName", func(b *BOMInputs) { b.ScraperName = "other" }},
		{"confidence", func(b *BOMInputs) { b.Confidence = ConfidenceDeclared }},
		{"component name", func(b *BOMInputs) { b.Components[0].Name = "different" }},
		{"component version", func(b *BOMInputs) { b.Components[0].Version = "v9.9.9" }},
		{"component hash value", func(b *BOMInputs) {
			b.Components[1].Hashes["sha256"] = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
		}},
		{"component property value", func(b *BOMInputs) {
			b.Components[0].Properties["runtime.name"] = "triton"
		}},
		{"new property key", func(b *BOMInputs) {
			b.Components[0].Properties["zzz.new"] = "added"
		}},
		{"evidence source", func(b *BOMInputs) {
			b.Components[0].Evidence.Source = SourceEnvVar
		}},
		{"child component", func(b *BOMInputs) {
			b.Components[1].Children = append(b.Components[1].Children, Component{Name: "added-child"})
		}},
		{"new service", func(b *BOMInputs) {
			b.Services = append(b.Services, Service{Name: "new-svc"})
		}},
		{"provenance method", func(b *BOMInputs) {
			b.Provenance[0].ScrapeMethod = "ebpf"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cp := *fixturePopulatedBOMInputs()
			tc.mutate(&cp)
			got, _ := HashBOMInputs(&cp)
			if got == baseHash {
				t.Errorf("mutation %q did not change the hash; field is not contributing to dedup", tc.name)
			}
		})
	}
}

// TestHashBOMInputs_MapKeyOrderInsensitive asserts that two
// BOMInputs whose Properties/Hashes maps have the same content but
// were populated in different orders produce the same hash. This
// is a stronger version of the json.Marshal-sorts-map-keys
// assumption — it locks the behavior as observed property rather
// than just-relying-on-docs.
func TestHashBOMInputs_MapKeyOrderInsensitive(t *testing.T) {
	a := fixturePopulatedBOMInputs()
	b := fixturePopulatedBOMInputs()
	// Replace the maps in b with same-content maps populated in a
	// different physical order. Go map literal order is undefined
	// at runtime, but the encoder must sort regardless.
	b.Components[0].Properties = map[string]string{"image.tag": "v0.6.3", "runtime.name": "vllm"}
	b.Components[1].Properties = map[string]string{"image.reference": "vllm/vllm-openai:v0.6.3"}

	ha, _ := HashBOMInputs(a)
	hb, _ := HashBOMInputs(b)
	if ha != hb {
		t.Errorf("hash differed across map population orders: %q vs %q", ha, hb)
	}
}

// TestHashBOMInputs_Nil returns empty string with no error.
func TestHashBOMInputs_Nil(t *testing.T) {
	got, err := HashBOMInputs(nil)
	if err != nil {
		t.Errorf("nil input returned error: %v", err)
	}
	if got != "" {
		t.Errorf("nil input returned hash %q, want empty", got)
	}
}

// TestHashBOMInputs_DoesNotMutateInput ensures HashBOMInputs leaves
// the caller's BOMInputs untouched. The function mutates a local
// copy; this test guards against a future refactor accidentally
// operating on the input directly.
func TestHashBOMInputs_DoesNotMutateInput(t *testing.T) {
	in := fixturePopulatedBOMInputs()
	originalTimestamp := in.ScrapeTimestamp
	originalProvTimestamp := in.Provenance[0].ScrapeTimestamp
	_, _ = HashBOMInputs(in)
	if !in.ScrapeTimestamp.Equal(originalTimestamp) {
		t.Errorf("BOMInputs.ScrapeTimestamp mutated by hash: %v → %v",
			originalTimestamp, in.ScrapeTimestamp)
	}
	if !in.Provenance[0].ScrapeTimestamp.Equal(originalProvTimestamp) {
		t.Errorf("Provenance[0].ScrapeTimestamp mutated by hash: %v → %v",
			originalProvTimestamp, in.Provenance[0].ScrapeTimestamp)
	}
}

// ---------------------------------------------------------------------------
// Field-type safety guard
// ---------------------------------------------------------------------------
//
// TestHashBOMInputs_FieldTypeSafety enforces that every field type
// reachable through BOMInputs serializes deterministically under
// encoding/json. The function HashBOMInputs's correctness depends on
// this property holding across future struct additions; if a
// contributor adds (for example) an interface{} field somewhere in
// the BOMInputs type graph, this test fails before any determinism
// violation can manifest in production.
//
// Scope deviation from the original specification: the spec rejected
// "any map[K]V". In practice, encoding/json sorts map keys with
// string or numeric types, so string/integer-keyed maps ARE
// deterministic. We allow those and reject only the cases that
// genuinely break determinism:
//
//   - interface{} (concrete type can vary between calls)
//   - chan, func, unsafe.Pointer (json.Marshal errors anyway)
//   - map with non-string/non-integer keys (json.Marshal handles
//     keys that satisfy TextMarshaler, but the determinism story
//     for arbitrary TextMarshaler keys depends on the type's impl)
//
// Recursion stops at types implementing json.Marshaler (notably
// time.Time): they serialize via their own method, which we trust
// to be deterministic per-type.

var jsonMarshalerType = reflect.TypeOf((*json.Marshaler)(nil)).Elem()

func TestHashBOMInputs_FieldTypeSafety(t *testing.T) {
	visited := map[reflect.Type]bool{}
	var walk func(rt reflect.Type, path string)
	walk = func(rt reflect.Type, path string) {
		if rt == nil || visited[rt] {
			return
		}
		visited[rt] = true

		// Types with a custom MarshalJSON: trust them, don't recurse.
		if rt.Implements(jsonMarshalerType) || reflect.PointerTo(rt).Implements(jsonMarshalerType) {
			return
		}

		switch rt.Kind() {
		case reflect.Bool,
			reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
			reflect.Float32, reflect.Float64,
			reflect.String:
			return // primitive — deterministic
		case reflect.Pointer:
			walk(rt.Elem(), path+"*")
		case reflect.Slice, reflect.Array:
			walk(rt.Elem(), path+"[]")
		case reflect.Map:
			kk := rt.Key().Kind()
			switch kk {
			case reflect.String,
				reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
				reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
				walk(rt.Elem(), path+"[map_value]")
			default:
				t.Errorf("%s: map with key kind %s would break canonical hashing; encoding/json only guarantees deterministic key order for string/integer keys",
					path, kk)
			}
		case reflect.Struct:
			for i := 0; i < rt.NumField(); i++ {
				f := rt.Field(i)
				if !f.IsExported() {
					continue
				}
				tag := f.Tag.Get("json")
				// Skip fields explicitly excluded from JSON (e.g.,
				// BOMInputs.Errors []error has json:"-").
				if tag == "-" || strings.HasPrefix(tag, "-,") {
					continue
				}
				walk(f.Type, path+"."+f.Name)
			}
		case reflect.Interface:
			t.Errorf("%s: interface{} field would break canonical hashing; the concrete type's serialization varies between calls",
				path)
		case reflect.Chan, reflect.Func, reflect.UnsafePointer:
			t.Errorf("%s: %s field is not JSON-serializable (json.Marshal would error)",
				path, rt.Kind())
		default:
			t.Errorf("%s: unhandled kind %s — extend TestHashBOMInputs_FieldTypeSafety to classify it",
				path, rt.Kind())
		}
	}
	walk(reflect.TypeOf(BOMInputs{}), "BOMInputs")
}
