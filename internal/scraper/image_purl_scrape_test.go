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
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Container components carry a pkg:oci purl: versioned by the digest
// when one is resolved, unversioned when only a tag is known.
func TestInferenceSpecScraper_ContainerPURL(t *testing.T) {
	s := newScraper()
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "vllm", Namespace: "ns"},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "pinned", Image: "ghcr.io/org/Serving:v1.2.3@" + testFullDigest},
						{Name: "tagged", Image: "vllm/vllm-openai:v0.6.3"},
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
	want := map[string]string{
		"pinned": "pkg:oci/serving@sha256:" + testFullDigestHex + "?repository_url=ghcr.io%2Forg%2FServing&tag=v1.2.3",
		"tagged": "pkg:oci/vllm-openai?repository_url=vllm%2Fvllm-openai&tag=v0.6.3",
	}
	seen := map[string]bool{}
	for _, c := range got.Components {
		if c.Type != ComponentContainer {
			if c.PURL != "" {
				t.Errorf("%s %s: unexpected purl %q", c.Type, c.Name, c.PURL)
			}
			continue
		}
		name := c.Properties["container.name"]
		seen[name] = true
		if c.PURL != want[name] {
			t.Errorf("container %s purl = %q, want %q", name, c.PURL, want[name])
		}
	}
	if len(seen) != 2 {
		t.Fatalf("expected two container components, saw %v", seen)
	}
}

func TestNIMServiceScraper_ContainerPURL(t *testing.T) {
	u := nimService("ns", "llama", "nvcr.io/nim/meta/llama-3.1-8b-instruct", "1.3.0")
	got, err := newNIMScraper().Scrape(context.Background(), nimWorkload(u), testConfig())
	if err != nil {
		t.Fatalf("Scrape: %v", err)
	}
	img := findComponent(t, got.Components, func(c Component) bool { return c.Type == ComponentContainer })
	want := "pkg:oci/llama-3.1-8b-instruct?repository_url=nvcr.io%2Fnim%2Fmeta%2Fllama-3.1-8b-instruct&tag=1.3.0"
	if img.PURL != want {
		t.Errorf("tag-only NIM image purl = %q, want %q", img.PURL, want)
	}

	pinned := nimService("ns", "llama", "nvcr.io/nim/meta/llama-3.1-8b-instruct@"+testFullDigest, "")
	_ = unstructured.SetNestedField(pinned.Object, "", "spec", "image", "tag")
	got, err = newNIMScraper().Scrape(context.Background(), nimWorkload(pinned), testConfig())
	if err != nil {
		t.Fatalf("Scrape: %v", err)
	}
	img = findComponent(t, got.Components, func(c Component) bool { return c.Type == ComponentContainer })
	want = "pkg:oci/llama-3.1-8b-instruct@sha256:" + testFullDigestHex + "?repository_url=nvcr.io%2Fnim%2Fmeta%2Fllama-3.1-8b-instruct"
	if img.PURL != want {
		t.Errorf("digest-pinned NIM image purl = %q, want %q", img.PURL, want)
	}
}
