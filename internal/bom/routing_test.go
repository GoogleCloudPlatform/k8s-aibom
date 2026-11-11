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
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/k8s-aibom/internal/scraper"
)

// Design 007 §2: pool membership is recorded on the root
// metadata.component, in the given (sorted) order, numbered from zero,
// with the Endpoint Picker only when present; nothing else changes.
func TestBuild_InferencePoolRoutingProperties(t *testing.T) {
	b := newTestBuilder()
	opts := testBuildOptions()
	plain, err := b.Build(vllmFixtureBOMInputs(), opts)
	if err != nil {
		t.Fatal(err)
	}
	opts.InferencePools = []scraper.InferencePoolMembership{
		{Pool: "prod-inference/pool-a", EndpointPicker: "pool-a-epp"},
		{Pool: "prod-inference/pool-b"},
	}
	doc, err := b.Build(vllmFixtureBOMInputs(), opts)
	if err != nil {
		t.Fatal(err)
	}
	assertValidCycloneDX(t, doc.JSON)

	var got []string
	for _, p := range *doc.CDX.Metadata.Component.Properties {
		if strings.HasPrefix(p.Name, "k8s-aibom:routing:") {
			got = append(got, p.Name+"="+p.Value)
		}
	}
	want := []string{
		"k8s-aibom:routing:inferencePool:0=prod-inference/pool-a",
		"k8s-aibom:routing:inferencePool:0:endpointPicker=pool-a-epp",
		"k8s-aibom:routing:inferencePool:1=prod-inference/pool-b",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("routing properties:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if doc.CDX.Metadata.Properties != nil {
		for _, p := range *doc.CDX.Metadata.Properties {
			if strings.Contains(p.Name, "routing") {
				t.Errorf("routing fact leaked into metadata.properties: %s", p.Name)
			}
		}
	}
	// Only the root component gains properties: components (models,
	// images, pods) are unchanged.
	if len(*doc.CDX.Components) != len(*plain.CDX.Components) {
		t.Errorf("components changed: %d vs %d", len(*doc.CDX.Components), len(*plain.CDX.Components))
	}
	if bytes.Contains(plain.JSON, []byte("routing")) {
		t.Error("a document with no pool facts carries routing properties")
	}
	// Determinism.
	again, _ := b.Build(vllmFixtureBOMInputs(), opts)
	if !bytes.Equal(doc.JSON, again.JSON) {
		t.Error("routing properties are not byte-deterministic")
	}
}
