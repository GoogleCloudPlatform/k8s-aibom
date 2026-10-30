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
)

// managerMultiScraper is the scraper set cmd/manager wires for the
// apps/v1 and batch/v1 kinds, in its order.
func managerMultiScraper() *MultiScraper {
	return NewMultiScraper(NewInferenceSpecScraper(nil), NewVectorDBSpecScraper(), NewAgentSpecScraper(), NewTrainingSpecScraper(), NewEvalSpecScraper())
}

func multiDeployment(containers ...corev1.Container) Workload {
	dep := &appsv1.Deployment{Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{Containers: containers},
	}}}
	return Workload{Kind: WorkloadKind{Group: "apps", Version: "v1", Kind: "Deployment"}, Name: "w", Namespace: "ns", Object: dep}
}

// A workload another scraper recognizes keeps the container components
// inference.spec built for it, even though inference.spec found no
// runtime or model of its own: the image and its digest are what the
// workload runs, whichever scraper gave it a category.
func TestMultiScraper_KeepsContainerComponentsForNonInferenceWorkloads(t *testing.T) {
	const hex = "1111111111111111111111111111111111111111111111111111111111111111"
	cases := []struct {
		name, image, wantName, wantCategory string
		env                                 []corev1.EnvVar
	}{
		{name: "vector database", image: "qdrant/qdrant:v1.12.0@sha256:" + hex, wantName: "qdrant/qdrant", wantCategory: string(CategoryVectorDB)},
		{name: "agent image", image: "langflowai/langflow:1.1.0@sha256:" + hex, wantName: "langflowai/langflow", wantCategory: string(CategoryAgent)},
		{name: "agent env signature", image: "example.com/my-agent:2.0@sha256:" + hex, wantName: "example.com/my-agent", wantCategory: string(CategoryAgent),
			env: []corev1.EnvVar{{Name: "LANGCHAIN_TRACING_V2", Value: "true"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := multiDeployment(corev1.Container{Name: "app", Image: tc.image, Env: tc.env})
			got, err := managerMultiScraper().Scrape(context.Background(), w, testConfig())
			if err != nil {
				t.Fatal(err)
			}
			if string(got.Category) != tc.wantCategory {
				t.Fatalf("Category = %q, want %q", got.Category, tc.wantCategory)
			}
			containers := componentsOf(got.Components, ComponentContainer)
			if len(containers) != 1 {
				t.Fatalf("container components = %d, want 1: %+v", len(containers), got.Components)
			}
			c := containers[0]
			if c.Name != tc.wantName || c.Hashes["sha256"] != hex || c.Confidence != ConfidenceDeclared {
				t.Errorf("container = %+v, want name %q, sha256 %q, declared", c, tc.wantName, hex)
			}
			if c.Evidence.Locator != "spec.template.spec.containers[0].image" {
				t.Errorf("container locator = %q", c.Evidence.Locator)
			}
			var scrapers []string
			for _, p := range got.Provenance {
				scrapers = append(scrapers, p.ScraperName)
			}
			if len(scrapers) == 0 || scrapers[0] != "inference.spec" {
				t.Errorf("provenance = %v, want inference.spec first", scrapers)
			}
		})
	}
}

// A container is listed once when inference.spec itself found a signal.
func TestMultiScraper_ContainerListedOnceForInferenceWorkload(t *testing.T) {
	w := multiDeployment(corev1.Container{Name: "vllm", Image: "vllm/vllm-openai:v0.6.3",
		Env: []corev1.EnvVar{{Name: "LANGCHAIN_TRACING_V2", Value: "true"}}})
	got, err := managerMultiScraper().Scrape(context.Background(), w, testConfig())
	if err != nil {
		t.Fatal(err)
	}
	if n := len(componentsOf(got.Components, ComponentContainer)); n != 1 {
		t.Errorf("container components = %d, want 1: %+v", n, got.Components)
	}
}

// A workload no scraper recognizes stays unresolved with no components:
// container components alone never make a workload an AI workload.
func TestMultiScraper_ContainerAloneStaysUnresolved(t *testing.T) {
	w := multiDeployment(corev1.Container{Name: "web", Image: "nginx:1.27"})
	got, err := managerMultiScraper().Scrape(context.Background(), w, testConfig())
	if err != nil {
		t.Fatal(err)
	}
	if got.Confidence != ConfidenceUnresolved || len(got.Components) != 0 {
		t.Errorf("got confidence %q with %d components, want unresolved and none", got.Confidence, len(got.Components))
	}
}
