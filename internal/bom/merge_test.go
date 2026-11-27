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

func modelComp(name string, src scraper.EvidenceSource, locator string, conf scraper.Confidence, props map[string]string) scraper.Component {
	return scraper.Component{
		Type:       scraper.ComponentMLModel,
		Name:       name,
		Evidence:   scraper.Evidence{Source: src, Locator: locator},
		Confidence: conf,
		Properties: props,
	}
}

func buildComps(t *testing.T, comps ...scraper.Component) []cdx.Component {
	t.Helper()
	inputs := &scraper.BOMInputs{
		ScraperName:     "inference.spec",
		ScrapeTimestamp: fixedBuildTime,
		Confidence:      scraper.ConfidenceInferred,
		Components:      comps,
	}
	doc, err := newTestBuilder().Build(inputs, testBuildOptions())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	assertValidCycloneDX(t, doc.JSON)
	if doc.CDX.Components == nil {
		return nil
	}
	return *doc.CDX.Components
}

func propsOf(c cdx.Component) map[string]string {
	m := map[string]string{}
	if c.Properties != nil {
		for _, p := range *c.Properties {
			m[p.Name] = p.Value
		}
	}
	return m
}

// TestBuild_MergesModelComponentsByName pins #362: a model named by
// several sources is one machine-learning-model component. The
// first-found locator and source stay unnumbered, the others are indexed
// from 1 with their sources, each source's confidence sits beside it,
// the component takes the highest tier, and identity properties from
// every source are kept.
func TestBuild_MergesModelComponentsByName(t *testing.T) {
	const llama = "meta-llama/Llama-3.1-8B-Instruct"
	comps := buildComps(t,
		modelComp(llama, scraper.SourceContainerArg, "spec.template.spec.containers[0].args[0 1](--model)", scraper.ConfidenceDeclared,
			map[string]string{"identity.argFlag": "--model", "container.name": "vllm"}),
		modelComp(llama, scraper.SourceEnvVar, "spec.template.spec.containers[0].env[0](HF_MODEL_ID)", scraper.ConfidenceInferred,
			map[string]string{"identity.envVarName": "HF_MODEL_ID", "container.name": "vllm"}),
	)
	if len(comps) != 1 {
		t.Fatalf("emitted %d components, want 1: %+v", len(comps), comps)
	}
	got := propsOf(comps[0])
	want := map[string]string{
		"k8s-aibom:confidence":            "declared",
		"k8s-aibom:evidence:source":       "container_arg",
		"k8s-aibom:evidence:locator":      "spec.template.spec.containers[0].args[0 1](--model)",
		"k8s-aibom:evidence:confidence":   "declared",
		"k8s-aibom:evidence:source:1":     "env_var",
		"k8s-aibom:evidence:locator:1":    "spec.template.spec.containers[0].env[0](HF_MODEL_ID)",
		"k8s-aibom:evidence:confidence:1": "inferred",
		"k8s-aibom:identity:argFlag":      "--model",
		"k8s-aibom:identity:envVarName":   "HF_MODEL_ID",
		"k8s-aibom:container:name":        "vllm",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if len(got) != len(want) {
		t.Errorf("properties = %v, want exactly %v", got, want)
	}
}

// TestBuild_MergedModelTakesHighestTier checks the confidence and the
// precedence of conflicting properties when the stronger source is not
// the first-found one: the first locator still stays unnumbered.
func TestBuild_MergedModelTakesHighestTier(t *testing.T) {
	comps := buildComps(t,
		modelComp("m", scraper.SourceEnvVar, "a-env", scraper.ConfidenceInferred,
			map[string]string{"signature.status": "claimed"}),
		modelComp("m", scraper.SourceCRDField, "b-crd", scraper.ConfidenceVerified,
			map[string]string{"signature.status": "verified"}),
		modelComp("m", scraper.SourceContainerArg, "c-arg", scraper.ConfidenceDeclared, nil),
	)
	if len(comps) != 1 {
		t.Fatalf("emitted %d components, want 1", len(comps))
	}
	got := propsOf(comps[0])
	for k, v := range map[string]string{
		"k8s-aibom:confidence":            "verified",
		"k8s-aibom:evidence:locator":      "a-env",
		"k8s-aibom:evidence:source":       "env_var",
		"k8s-aibom:evidence:confidence":   "inferred",
		"k8s-aibom:evidence:locator:1":    "b-crd",
		"k8s-aibom:evidence:confidence:1": "verified",
		"k8s-aibom:evidence:locator:2":    "c-arg",
		"k8s-aibom:evidence:source:2":     "container_arg",
		"k8s-aibom:evidence:confidence:2": "declared",
		PropSigningStatus:                 "verified",
	} {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

// TestBuild_MergedModelCarriesServedModelName: the inference scraper
// records identity.servedModelName (#413) on every model component of a
// container, so a merged model keeps it like the other identity
// properties: once when every source agrees, from a later source when the
// first-found one has none, and from the highest-tier source when two
// sources disagree.
func TestBuild_MergedModelCarriesServedModelName(t *testing.T) {
	served := func(name string) map[string]string {
		if name == "" {
			return map[string]string{"container.name": "vllm"}
		}
		return map[string]string{"container.name": "vllm", "identity.servedModelName": name}
	}
	cases := []struct {
		name  string
		comps []scraper.Component
		want  string
	}{
		{
			name: "every source has the same alias",
			comps: []scraper.Component{
				modelComp("m", scraper.SourceContainerArg, "args", scraper.ConfidenceDeclared, served("llama")),
				modelComp("m", scraper.SourceEnvVar, "env", scraper.ConfidenceInferred, served("llama")),
			},
			want: "llama",
		},
		{
			name: "only a later, lower-tier source has an alias",
			comps: []scraper.Component{
				modelComp("m", scraper.SourceContainerArg, "a-args", scraper.ConfidenceDeclared, served("")),
				modelComp("m", scraper.SourceEnvVar, "b-env", scraper.ConfidenceInferred, served("llama")),
			},
			want: "llama",
		},
		{
			name: "different aliases: the highest-tier source wins",
			comps: []scraper.Component{
				modelComp("m", scraper.SourceEnvVar, "a-env", scraper.ConfidenceInferred, served("first")),
				modelComp("m", scraper.SourceContainerArg, "b-args", scraper.ConfidenceDeclared, served("strongest")),
			},
			want: "strongest",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			comps := buildComps(t, tc.comps...)
			if len(comps) != 1 {
				t.Fatalf("emitted %d components, want 1", len(comps))
			}
			if got := propsOf(comps[0])[PropIdentityServedModelName]; got != tc.want {
				t.Errorf("%s = %q, want %q", PropIdentityServedModelName, got, tc.want)
			}
		})
	}
}

// TestBuild_MergeLeavesOtherComponentsAlone: only top-level
// machine-learning-model components with the same name are merged.
// Different model names, other types and nested children keep one
// component each, without per-source evidence properties.
func TestBuild_MergeLeavesOtherComponentsAlone(t *testing.T) {
	app := func(locator string) scraper.Component {
		return scraper.Component{
			Type:       scraper.ComponentApplication,
			Name:       "vllm",
			Evidence:   scraper.Evidence{Source: scraper.SourceImagePattern, Locator: locator},
			Confidence: scraper.ConfidenceInferred,
		}
	}
	parent := app("p")
	parent.Children = []scraper.Component{
		modelComp("child", scraper.SourceContainerArg, "x", scraper.ConfidenceInferred, nil),
		modelComp("child", scraper.SourceEnvVar, "y", scraper.ConfidenceInferred, nil),
	}
	comps := buildComps(t,
		app("a"), app("b"), parent,
		modelComp("m1", scraper.SourceContainerArg, "x", scraper.ConfidenceDeclared, nil),
		modelComp("m2", scraper.SourceEnvVar, "y", scraper.ConfidenceInferred, nil),
	)
	if len(comps) != 5 {
		t.Fatalf("emitted %d components, want 5", len(comps))
	}
	if comps[2].Components == nil || len(*comps[2].Components) != 2 {
		t.Fatalf("nested children were merged: %+v", comps[2].Components)
	}
	for _, c := range comps {
		if _, ok := propsOf(c)[PropEvidenceConfidence]; ok {
			t.Errorf("%s/%s has %s; only merged models carry it", c.Type, c.Name, PropEvidenceConfidence)
		}
	}
}

// TestBuild_MergedLocatorsAreRedacted: an indexed locator crosses the
// same output-sanitization boundary as the unnumbered one.
func TestBuild_MergedLocatorsAreRedacted(t *testing.T) {
	comps := buildComps(t,
		modelComp("m", scraper.SourceContainerArg, "args[0]", scraper.ConfidenceDeclared, nil),
		modelComp("m", scraper.SourceEnvVar, "env[0](https://user:hunter2@host/model)", scraper.ConfidenceInferred, nil),
	)
	got := propsOf(comps[0])
	if loc := got["k8s-aibom:evidence:locator:1"]; loc == "" || strings.Contains(loc, "hunter2") {
		t.Errorf("indexed locator not redacted: %q", loc)
	}
	if got[PropRedactionApplied] == "" {
		t.Errorf("%s missing on a component with a redacted locator", PropRedactionApplied)
	}
}
