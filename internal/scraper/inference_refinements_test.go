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
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Phase 5 refinements:
//
//   - SourceImageReference distinguishes the image field from arg/env sources.
//   - SourcePodTemplateAnnotation distinguishes pod-template annotations from
//     pod-instance annotations.
//   - Runtime app Component is Inferred (not Declared) and Version only set
//     when the image tag is semver-shaped.
//   - Nil Object on a Workload is a hard error rather than NPE.
//   - omitzero behavior on Evidence and Timestamp fields is exercised
//     explicitly so a breakage produces a clear failure rather than a
//     transitive surprise elsewhere.

func TestLooksSemverTag(t *testing.T) {
	cases := []struct {
		tag  string
		want bool
	}{
		{"", false},
		{"v0.6.3", true},
		{"0.6.3", true},
		{"v1.0", true},
		{"1.0", true},
		{"v0.6.3-rc1", true},
		{"24.01-py3", true},
		{"v1.0.0-rc.1", true},
		{"latest", false},
		{"nightly-20251025", false},
		{"main", false},
		{"abc1234", false},
		{"v", false},
		{"v.", false},
		{"vv1.0", false},
	}
	for _, tc := range cases {
		t.Run(tc.tag, func(t *testing.T) {
			if got := looksSemverTag(tc.tag); got != tc.want {
				t.Errorf("looksSemverTag(%q) = %v, want %v", tc.tag, got, tc.want)
			}
		})
	}
}

func TestInferenceSpecScraper_RuntimeVersion_SemverTag(t *testing.T) {
	s := newScraper()
	dep := vllmDeploymentFixtureWithTag("v0.6.3")
	w := Workload{
		Kind:   WorkloadKind{Group: "apps", Version: "v1", Kind: "Deployment"},
		Object: dep,
	}
	got, err := s.Scrape(context.Background(), w, testConfig())
	if err != nil {
		t.Fatal(err)
	}
	app := findRuntimeApp(t, got.Components)
	if app.Version != "v0.6.3" {
		t.Errorf("runtime app Version = %q, want %q", app.Version, "v0.6.3")
	}
	if app.Confidence != ConfidenceInferred {
		t.Errorf("runtime app Confidence = %q, want %q", app.Confidence, ConfidenceInferred)
	}
}

func TestInferenceSpecScraper_RuntimeVersion_NonSemverTag(t *testing.T) {
	s := newScraper()
	dep := vllmDeploymentFixtureWithTag("latest")
	w := Workload{
		Kind:   WorkloadKind{Group: "apps", Version: "v1", Kind: "Deployment"},
		Object: dep,
	}
	got, err := s.Scrape(context.Background(), w, testConfig())
	if err != nil {
		t.Fatal(err)
	}
	app := findRuntimeApp(t, got.Components)
	if app.Version != "" {
		t.Errorf("runtime app Version = %q, want empty (tag 'latest' is not semver-shaped)", app.Version)
	}
	if app.Properties["image.tag"] != "latest" {
		t.Errorf("expected image.tag property = 'latest', got %q", app.Properties["image.tag"])
	}
	if app.Confidence != ConfidenceInferred {
		t.Errorf("runtime app Confidence = %q, want %q", app.Confidence, ConfidenceInferred)
	}
}

func TestInferenceSpecScraper_NilObject(t *testing.T) {
	s := newScraper()
	w := Workload{
		Kind: WorkloadKind{Group: "apps", Version: "v1", Kind: "Deployment"},
		// Object intentionally nil
	}
	_, err := s.Scrape(context.Background(), w, testConfig())
	if err == nil {
		t.Fatal("expected error for nil Object")
	}
}

func TestInferenceSpecScraper_PodTemplateVsWorkloadAnnotationSourcesDistinct(t *testing.T) {
	s := newScraper()
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "x", Namespace: "ns",
			Annotations: map[string]string{
				"model.k8saibom.dev/workload-level": "from-workload",
			},
		},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"model.k8saibom.dev/template-level": "from-template",
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "vllm", Image: "vllm/vllm-openai:v0.6.3"},
					},
				},
			},
		},
	}
	w := Workload{
		Kind:   WorkloadKind{Group: "apps", Version: "v1", Kind: "Deployment"},
		Object: dep,
	}
	got, err := s.Scrape(context.Background(), w, testConfig())
	if err != nil {
		t.Fatal(err)
	}
	var fromWorkload, fromTemplate *Component
	for i := range got.Components {
		c := &got.Components[i]
		if c.Type != ComponentMLModel {
			continue
		}
		switch c.Evidence.Source {
		case SourceWorkloadAnnotation:
			fromWorkload = c
		case SourcePodTemplateAnnotation:
			fromTemplate = c
		}
	}
	if fromWorkload == nil {
		t.Error("missing component sourced from workload annotation")
	} else if fromWorkload.Name != "from-workload" {
		t.Errorf("workload-annotation component name = %q, want %q", fromWorkload.Name, "from-workload")
	}
	if fromTemplate == nil {
		t.Error("missing component sourced from pod-template annotation")
	} else if fromTemplate.Name != "from-template" {
		t.Errorf("pod-template-annotation component name = %q, want %q", fromTemplate.Name, "from-template")
	}
}

func TestInferenceSpecScraper_ContainerEvidenceSource_DigestFromSpec(t *testing.T) {
	// When the digest comes from the image field itself, evidence.source
	// must be image_reference (NOT container_arg, which would imply args[]).
	s := newScraper()
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "ns"},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "vllm", Image: "vllm/vllm-openai:v0.6.3@sha256:abc"},
					},
				},
			},
		},
	}
	w := Workload{Kind: WorkloadKind{Group: "apps", Version: "v1", Kind: "Deployment"}, Object: dep}
	got, err := s.Scrape(context.Background(), w, testConfig())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range got.Components {
		if c.Type == ComponentContainer {
			if c.Evidence.Source != SourceImageReference {
				t.Errorf("digest-from-spec container Evidence.Source = %q, want %q",
					c.Evidence.Source, SourceImageReference)
			}
			return
		}
	}
	t.Fatal("missing container component")
}

func TestInferenceSpecScraper_ContainerEvidenceSource_UnresolvedDigest(t *testing.T) {
	// When no digest is resolvable, evidence.source still points at the
	// image_reference field (we couldn't read further, but the locator
	// describes where we tried).
	s := newScraper()
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "ns"},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "vllm", Image: "vllm/vllm-openai:v0.6.3"},
					},
				},
			},
		},
	}
	w := Workload{Kind: WorkloadKind{Group: "apps", Version: "v1", Kind: "Deployment"}, Object: dep, Pods: nil}
	got, err := s.Scrape(context.Background(), w, testConfig())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range got.Components {
		if c.Type == ComponentContainer {
			if c.Confidence != ConfidenceUnresolved {
				t.Errorf("expected ConfidenceUnresolved, got %q", c.Confidence)
			}
			if c.Evidence.Source != SourceImageReference {
				t.Errorf("unresolved container Evidence.Source = %q, want %q",
					c.Evidence.Source, SourceImageReference)
			}
			return
		}
	}
	t.Fatal("missing container component")
}

// TestComponentOmitsEvidence_WhenZero pins the omitzero behavior on the
// Evidence field. Component is the canonical case (it carries an embedded
// Evidence struct). If omitzero ever regresses — for instance, if a future
// reviewer downgrades a tag to omitempty without realizing structs don't
// honor omitempty — this test fails with a clear message rather than the
// indirect "zero Component does not marshal to {}" message.
func TestComponentOmitsEvidence_WhenZero(t *testing.T) {
	// Component with one populated field but zero Evidence:
	in := Component{Name: "x"}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(b)
	if got != `{"name":"x"}` {
		t.Errorf("Component{Name: %q} marshaled to %q, want %q\n(Hint: did the json tag on Evidence change from omitzero to omitempty? Embedded structs don't honor omitempty.)",
			in.Name, got, `{"name":"x"}`)
	}
	// Sanity: populated Evidence does appear.
	in = Component{Name: "x", Evidence: Evidence{Source: SourceEnvVar}}
	b, _ = json.Marshal(in)
	if string(b) == `{"name":"x"}` {
		t.Errorf("populated Evidence was unexpectedly omitted")
	}
}

func TestProvenanceOmitsTimestamp_WhenZero(t *testing.T) {
	// Mirror test for the time.Time field.
	in := Provenance{ScraperName: "x"}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(b)
	if got != `{"scraperName":"x"}` {
		t.Errorf("zero Timestamp not omitted: %s\n(Hint: did the json tag on ScrapeTimestamp change from omitzero to omitempty? time.Time doesn't honor omitempty.)", got)
	}
}

// ---------- helpers ----------

func vllmDeploymentFixtureWithTag(tag string) *appsv1.Deployment {
	image := "vllm/vllm-openai"
	if tag != "" {
		image = image + ":" + tag
	}
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "vllm", Namespace: "ns"},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "vllm", Image: image},
					},
				},
			},
		},
	}
}

func findRuntimeApp(t *testing.T, cs []Component) *Component {
	t.Helper()
	for i := range cs {
		if cs[i].Type == ComponentApplication && cs[i].Properties["runtime.name"] != "" {
			return &cs[i]
		}
	}
	t.Fatal("missing runtime application component")
	return nil
}

// G0.3 regression: tenant-controlled document growth is bounded.
func TestScrape_ComponentCapIsAppliedAndRecorded(t *testing.T) {
	s := NewInferenceSpecScraper(nil)
	// A pathological Deployment: one vllm container (real signal) plus
	// hundreds of model-claim env vars.
	env := []corev1.EnvVar{}
	for i := 0; i < MaxComponentsPerDocument+100; i++ {
		env = append(env, corev1.EnvVar{Name: "MODEL_NAME", Value: fmt.Sprintf("m-%04d", i)})
	}
	// MODEL_NAME appears once per container in a real spec; spread the
	// claims across many containers to build a legitimate-shaped but
	// enormous component list.
	containers := []corev1.Container{}
	for i := 0; i < MaxComponentsPerDocument+100; i++ {
		containers = append(containers, corev1.Container{
			Name:  fmt.Sprintf("c%04d", i),
			Image: "vllm/vllm-openai:v0.6.3",
			Env:   []corev1.EnvVar{{Name: "MODEL_NAME", Value: fmt.Sprintf("m-%04d", i)}},
		})
	}
	_ = env
	dep := &appsv1.Deployment{
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{Containers: containers},
			},
		},
	}
	w := Workload{Kind: WorkloadKind{Group: "apps", Version: "v1", Kind: "Deployment"}, Name: "huge", Namespace: "ns", Object: dep}
	inputs, err := s.Scrape(context.Background(), w, testConfig())
	if err != nil {
		t.Fatalf("scrape: %v", err)
	}
	if len(inputs.Components) != MaxComponentsPerDocument {
		t.Fatalf("components = %d, want capped at %d", len(inputs.Components), MaxComponentsPerDocument)
	}
	if inputs.TruncatedComponents == 0 {
		t.Fatalf("TruncatedComponents = 0; truncation must be recorded, never silent")
	}
	// Determinism: capping twice yields the identical surviving set.
	inputs2, _ := s.Scrape(context.Background(), w, testConfig())
	h1, _ := HashBOMInputs(inputs)
	h2, _ := HashBOMInputs(inputs2)
	if h1 != h2 {
		t.Fatalf("capped scrape is nondeterministic: %s != %s", h1, h2)
	}
}

// G0.3: the cap also bounds what the MultiScraper returns. agent.spec
// has no cap of its own, and merging appends its components to a list
// inference.spec has already capped.
func TestMultiScraper_ComponentCapAppliesToMergedResult(t *testing.T) {
	cases := []struct {
		name          string
		image         string
		containers    int
		wantTruncated int
	}{
		// Each container: a container and a vllm runtime component from
		// inference.spec (512 in all, 256 dropped there), plus an
		// openai-api component from agent.spec (256 more).
		{name: "inference and agent", image: "vllm/vllm-openai:v0.6.3", containers: MaxComponentsPerDocument, wantTruncated: 2 * MaxComponentsPerDocument},
		// No runtime image: inference.spec's result is unresolved, so
		// only its container components are kept (256, after its own
		// cap dropped 100), ahead of agent.spec's 356 openai-api
		// components; the merged 612 are capped at 256.
		{name: "agent only", image: "busybox:1.36", containers: MaxComponentsPerDocument + 100, wantTruncated: 100 + MaxComponentsPerDocument + 100},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			containers := make([]corev1.Container, 0, tc.containers)
			for i := 0; i < tc.containers; i++ {
				containers = append(containers, corev1.Container{
					Name:  fmt.Sprintf("c%04d", i),
					Image: tc.image,
					Env:   []corev1.EnvVar{{Name: "OPENAI_API_KEY", Value: "redacted"}},
				})
			}
			dep := &appsv1.Deployment{Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{Containers: containers},
			}}}
			w := Workload{Kind: WorkloadKind{Group: "apps", Version: "v1", Kind: "Deployment"}, Name: "huge", Namespace: "ns", Object: dep}
			m := NewMultiScraper(NewInferenceSpecScraper(nil), NewVectorDBSpecScraper(), NewAgentSpecScraper(), NewTrainingSpecScraper(), NewEvalSpecScraper())
			inputs, err := m.Scrape(context.Background(), w, testConfig())
			if err != nil {
				t.Fatalf("scrape: %v", err)
			}
			if len(inputs.Components) != MaxComponentsPerDocument {
				t.Fatalf("components = %d, want capped at %d", len(inputs.Components), MaxComponentsPerDocument)
			}
			if inputs.TruncatedComponents != tc.wantTruncated {
				t.Fatalf("TruncatedComponents = %d, want %d", inputs.TruncatedComponents, tc.wantTruncated)
			}
			inputs2, _ := m.Scrape(context.Background(), w, testConfig())
			h1, _ := HashBOMInputs(inputs)
			h2, _ := HashBOMInputs(inputs2)
			if h1 != h2 {
				t.Fatalf("capped scrape is nondeterministic: %s != %s", h1, h2)
			}
		})
	}
}

// G0.3: container component Name/Version are truncated like every
// other authored string.
func TestExtractContainerComponent_NameAndVersionTruncated(t *testing.T) {
	s := NewInferenceSpecScraper(nil)
	longName := strings.Repeat("a", MaxComponentNameLength+50)
	longTag := strings.Repeat("b", MaxComponentNameLength+50)
	c := corev1.Container{Name: "x", Image: longName + ":" + longTag}
	comps := s.extractContainerComponentAt(defaultPodSpecLocator, c, false, 0, nil, testConfig())
	if len(comps) == 0 {
		t.Fatal("no component extracted")
	}
	if len(comps[0].Name) > MaxComponentNameLength || len(comps[0].Version) > MaxComponentNameLength {
		t.Fatalf("name/version not truncated: %d / %d", len(comps[0].Name), len(comps[0].Version))
	}
}
