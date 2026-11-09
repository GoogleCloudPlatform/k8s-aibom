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
	"encoding/json"
	"testing"

	"github.com/GoogleCloudPlatform/k8s-aibom/internal/scraper"
)

// emittedGraph is the part of the emitted JSON that consumers such as
// Dependency-Track and GUAC read to build the dependency graph.
type emittedGraph struct {
	Metadata struct {
		Component struct {
			BOMRef string `json:"bom-ref"`
		} `json:"component"`
	} `json:"metadata"`
	Components []struct {
		BOMRef     string `json:"bom-ref"`
		Components []struct {
			BOMRef string `json:"bom-ref"`
		} `json:"components"`
	} `json:"components"`
	Dependencies []struct {
		Ref       string    `json:"ref"`
		DependsOn *[]string `json:"dependsOn"`
	} `json:"dependencies"`
}

func buildGraph(t *testing.T, inputs *scraper.BOMInputs) emittedGraph {
	t.Helper()
	doc, err := newTestBuilder().Build(inputs, testBuildOptions())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	assertValidCycloneDX(t, doc.JSON)
	var g emittedGraph
	if err := json.Unmarshal(doc.JSON, &g); err != nil {
		t.Fatalf("unmarshal emitted BOM: %v", err)
	}
	return g
}

// TestBuild_DependenciesLinkWorkloadToEveryTopLevelComponent checks the
// emitted graph: a single entry for metadata.component that lists every
// top-level component's bom-ref exactly once, in document order, and no
// nested component.
func TestBuild_DependenciesLinkWorkloadToEveryTopLevelComponent(t *testing.T) {
	inputs := vllmFixtureBOMInputs()
	// A second model component with the same name and type (found from an
	// env var as well as the argument) has its own bom-ref and is listed.
	inputs.Components = append(inputs.Components, scraper.Component{
		Type:       scraper.ComponentMLModel,
		Name:       "meta-llama/Llama-3.1-8B-Instruct",
		Evidence:   scraper.Evidence{Source: scraper.SourceEnvVar, Locator: "spec.template.spec.containers[0].env[HF_MODEL_ID]"},
		Confidence: scraper.ConfidenceInferred,
	})
	// A component with a nested child: the child is not a direct
	// dependency of the workload.
	inputs.Components = append(inputs.Components, scraper.Component{
		Type:       scraper.ComponentApplication,
		Name:       "triton",
		Evidence:   scraper.Evidence{Source: scraper.SourceImagePattern, Locator: "spec.template.spec.containers[1].image"},
		Confidence: scraper.ConfidenceInferred,
		Children: []scraper.Component{{
			Type:       scraper.ComponentMLModel,
			Name:       "resnet50",
			Evidence:   scraper.Evidence{Source: scraper.SourceContainerArg, Locator: "spec.template.spec.containers[1].args[0]"},
			Confidence: scraper.ConfidenceInferred,
		}},
	})

	g := buildGraph(t, inputs)

	if len(g.Dependencies) != 1 {
		t.Fatalf("dependencies has %d entries, want 1: %+v", len(g.Dependencies), g.Dependencies)
	}
	entry := g.Dependencies[0]
	if entry.Ref == "" || entry.Ref != g.Metadata.Component.BOMRef {
		t.Errorf("dependencies[0].ref = %q, want metadata.component bom-ref %q", entry.Ref, g.Metadata.Component.BOMRef)
	}
	if entry.DependsOn == nil {
		t.Fatal("dependencies[0].dependsOn is missing")
	}
	got := *entry.DependsOn

	if len(g.Components) != len(inputs.Components) {
		t.Fatalf("emitted %d top-level components, want %d", len(g.Components), len(inputs.Components))
	}
	want := make([]string, 0, len(g.Components))
	for _, c := range g.Components {
		want = append(want, c.BOMRef)
	}
	if len(got) != len(want) {
		t.Fatalf("dependsOn = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("dependsOn[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	seen := map[string]int{}
	for _, ref := range got {
		seen[ref]++
	}
	for _, c := range g.Components {
		if seen[c.BOMRef] != 1 {
			t.Errorf("component %q appears %d times in dependsOn, want 1", c.BOMRef, seen[c.BOMRef])
		}
		for _, child := range c.Components {
			if seen[child.BOMRef] != 0 {
				t.Errorf("nested component %q is listed as a workload dependency", child.BOMRef)
			}
		}
	}
}

// TestBuild_DependenciesNameWorkloadWithoutComponents checks that a
// document with no components still has the workload in its graph,
// with no dependsOn.
func TestBuild_DependenciesNameWorkloadWithoutComponents(t *testing.T) {
	g := buildGraph(t, &scraper.BOMInputs{
		ScraperName:     "inference.spec",
		ScrapeTimestamp: fixedBuildTime,
		Confidence:      scraper.ConfidenceUnresolved,
	})
	if len(g.Components) != 0 {
		t.Fatalf("emitted %d components, want 0", len(g.Components))
	}
	if len(g.Dependencies) != 1 {
		t.Fatalf("dependencies has %d entries, want 1: %+v", len(g.Dependencies), g.Dependencies)
	}
	if g.Dependencies[0].Ref != g.Metadata.Component.BOMRef {
		t.Errorf("dependencies[0].ref = %q, want %q", g.Dependencies[0].Ref, g.Metadata.Component.BOMRef)
	}
	if g.Dependencies[0].DependsOn != nil {
		t.Errorf("dependencies[0].dependsOn = %q, want it omitted", *g.Dependencies[0].DependsOn)
	}
}
