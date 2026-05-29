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
