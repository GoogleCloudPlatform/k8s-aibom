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
	"strings"
	"testing"

	cdx "github.com/CycloneDX/cyclonedx-go"

	"github.com/GoogleCloudPlatform/k8s-aibom/internal/scraper"
)

func TestPropertyName(t *testing.T) {
	cases := map[string]string{
		"aibom.workload.kind":            "k8s-aibom:workload:kind",
		"aibom.confidence":               "k8s-aibom:confidence",
		"aibom.evidence.locator":         "k8s-aibom:evidence:locator",
		"aibom.scrape.0.scraperName":     "k8s-aibom:scrape:0:scraperName",
		"aibom.rollup.owned.3":           "k8s-aibom:rollup:owned:3",
		"aibom.redaction.applied":        "k8s-aibom:redaction:applied",
		"aibom.truncation.applied":       "k8s-aibom:truncation:applied",
		"signature.status":               "k8s-aibom:security:signing:status",
		"signature.subjectNameMismatch":  "k8s-aibom:security:signing:subjectNameMismatch",
		"runtime.name":                   "k8s-aibom:runtime:name",
		"identity.envVarName":            "k8s-aibom:identity:envVarName",
		"image.reference":                "k8s-aibom:image:reference",
		"container.init":                 "k8s-aibom:container:init",
		"volume.mountPath":               "k8s-aibom:volume:mountPath",
		"dataset.path":                   "k8s-aibom:dataset:path",
		"dependency.type":                "k8s-aibom:dependency:type",
		"model.revision":                 "k8s-aibom:model:revision",
		"kserve.runtime.ref":             "k8s-aibom:kserve:runtime:ref",
		"nim.storage.nimCache.profile":   "k8s-aibom:nim:storage:nimCache:profile",
		"lws.size":                       "k8s-aibom:lws:size",
		"disaggregatedset.slices":        "k8s-aibom:disaggregatedset:slices",
		"dynamo.component.name":          "k8s-aibom:dynamo:component:name",
		"dynamo.component.type":          "k8s-aibom:dynamo:component:type",
		"dynamo.component.Frontend.type": "k8s-aibom:dynamo:component:Frontend:type",
		// Owner-written graph component names are kept verbatim.
		"dynamo.component.prefill.v2.type": "k8s-aibom:dynamo:component:prefill.v2:type",
		// Idempotent on an already-mapped name.
		"k8s-aibom:runtime:name": "k8s-aibom:runtime:name",
		"":                       "",
	}
	for in, want := range cases {
		if got := PropertyName(in); got != want {
			t.Errorf("PropertyName(%q) = %q, want %q", in, got, want)
		}
	}
	for _, c := range []string{PropConfidence, PropEvidenceSource, PropEvidenceLocator, PropRedactionApplied,
		PropTruncationApplied, PropRuntimeName, PropImageReference, PropSigningStatus, PropWorkloadCategory} {
		if PropertyName(c) != c || !strings.HasPrefix(c, Namespace+":") {
			t.Errorf("constant %q is not a stable taxonomy name", c)
		}
	}
	if PropertyName(redactionPropertyKey) != PropRedactionApplied {
		t.Errorf("redaction marker emitted as %q, want %q", PropertyName(redactionPropertyKey), PropRedactionApplied)
	}
}

// Every property in an emitted document is under the registered
// namespace; no pre-v1.7 dotted name survives at any depth.
func TestBuild_AllPropertiesUnderTaxonomyNamespace(t *testing.T) {
	inputs := vllmFixtureBOMInputs()
	inputs.Components = append(inputs.Components, inputs.Components[0]) // force a child-bearing shape below
	inputs.Components[len(inputs.Components)-1].Children = []scraper.Component{{
		Type: inputs.Components[0].Type, Name: "child", Properties: map[string]string{"signature.status": "claimed", "dynamo.component.a.b.type": "worker"},
	}}
	opts := testBuildOptions()
	opts.OwnedWorkloads = []string{"StatefulSet/group-0"}
	doc, err := newTestBuilder().Build(inputs, opts)
	if err != nil {
		t.Fatal(err)
	}
	check := func(path string, props *[]cdx.Property) {
		if props == nil {
			return
		}
		for _, p := range *props {
			if !strings.HasPrefix(p.Name, Namespace+":") {
				t.Errorf("%s: property %q is outside the %s namespace", path, p.Name, Namespace)
			}
			if strings.HasPrefix(p.Name, Namespace+":dynamo:component:") {
				continue // owner-written graph component names may contain dots
			}
			ns := p.Name
			if i := strings.LastIndex(ns, ":"); i >= 0 {
				ns = ns[:i]
			}
			if strings.Contains(ns, ".") {
				t.Errorf("%s: property %q still uses a dotted namespace", path, p.Name)
			}
		}
	}
	check("metadata", doc.CDX.Metadata.Properties)
	check("metadata.component", doc.CDX.Metadata.Component.Properties)
	var walk func(path string, comps *[]cdx.Component)
	walk = func(path string, comps *[]cdx.Component) {
		if comps == nil {
			return
		}
		for _, c := range *comps {
			check(path+"["+c.BOMRef+"]", c.Properties)
			walk(path+"["+c.BOMRef+"]", c.Components)
		}
	}
	walk("components", doc.CDX.Components)
	if !strings.Contains(string(doc.JSON), `"k8s-aibom:rollup:owned:0"`) || strings.Contains(string(doc.JSON), "aibom.") {
		t.Errorf("document still carries a pre-v1.7 name:\n%s", doc.JSON)
	}
}
