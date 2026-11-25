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
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
)

// scrapeImage runs s on a one-container workload of the kind s handles
// (a Deployment for vectordb.spec and agent.spec, a Job for
// training.spec and eval.spec) and returns the application component
// matched from the image, or nil.
func scrapeImage(t *testing.T, s Scraper, image string) *Component {
	t.Helper()
	tmpl := corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: image}}}}
	var w Workload
	if s.HandlesKind(WorkloadKind{Group: "apps", Version: "v1", Kind: "Deployment"}) {
		w = Workload{Kind: WorkloadKind{Group: "apps", Version: "v1", Kind: "Deployment"},
			Object: &appsv1.Deployment{Spec: appsv1.DeploymentSpec{Template: tmpl}}}
	} else {
		w = Workload{Kind: WorkloadKind{Group: "batch", Version: "v1", Kind: "Job"},
			Object: &batchv1.Job{Spec: batchv1.JobSpec{Template: tmpl}}}
	}
	got, err := s.Scrape(context.Background(), w, testConfig())
	if err != nil {
		t.Fatal(err)
	}
	for i := range got.Components {
		if c := &got.Components[i]; c.Evidence.Source == SourceImagePattern {
			return c
		}
	}
	return nil
}

// TestSpecScrapers_DockerHubQualifiedImage is the vectordb.spec,
// agent.spec, training.spec and eval.spec counterpart of #399: a Docker
// Hub image written with its registry ("docker.io/", "index.docker.io/")
// is matched through the same pattern as its short form. Only those
// prefixes are equated; every other registry keeps its exact-prefix
// semantics.
func TestSpecScrapers_DockerHubQualifiedImage(t *testing.T) {
	// One image per default image pattern of each scraper.
	cases := []struct {
		scraper Scraper
		images  []string
	}{
		{NewVectorDBSpecScraper(), []string{
			"milvusdb/milvus:v2.4.0",
			"milvusdb/milvus-dev:2.4-latest",
			"qdrant/qdrant:v1.12.0",
			"semitechnologies/weaviate:1.25.0",
			"chromadb/chroma:0.5.0",
			"pgvector/pgvector:pg16",
			"vespaengine/vespa:8.400.15",
			"vespaengine/vespa-generic-intel-x86_64:8.400.15",
		}},
		{NewAgentSpecScraper(), []string{
			"langflowai/langflow:1.1.0",
			"flowiseai/flowise:2.1.0",
			"chainlit/chainlit:1.0.0",
			"deepset/haystack:base-v2.5.0",
			"run-llama/llama-index:0.10.0",
		}},
		{NewTrainingSpecScraper(), []string{
			"pytorch/pytorch:2.3.0-cuda12.1-cudnn8-runtime",
			"rayproject/ray:2.9.0",
			"huggingface/accelerate:gpu-nightly",
		}},
		{NewEvalSpecScraper(), []string{
			"eleutherai/lm-eval:0.4.2",
			"explodinggradients/ragas:0.1.0",
			"truera/trulens:0.30.0",
		}},
	}
	for _, tc := range cases {
		for _, img := range tc.images {
			want := scrapeImage(t, tc.scraper, img)
			if want == nil {
				t.Fatalf("%s: %q matched no image pattern, the short form must match", tc.scraper.Name(), img)
			}
			for _, prefix := range []string{"docker.io/", "index.docker.io/"} {
				t.Run(tc.scraper.Name()+"/"+prefix+img, func(t *testing.T) {
					got := scrapeImage(t, tc.scraper, prefix+img)
					if got == nil {
						t.Fatalf("%q matched no image pattern, want %q", prefix+img, want.Name)
					}
					if got.Name != want.Name || got.Properties["runtime.pattern"] != want.Properties["runtime.pattern"] {
						t.Errorf("%q matched %q via %q, want %q via %q", prefix+img,
							got.Name, got.Properties["runtime.pattern"], want.Name, want.Properties["runtime.pattern"])
					}
				})
			}
		}
	}

	// Other registries, a Docker Hub prefix that is not at the start, and
	// a registry host written after docker.io/ stay quiet.
	quiet := []struct {
		scraper Scraper
		image   string
	}{
		{NewVectorDBSpecScraper(), "ghcr.io/qdrant/qdrant:v1.12.0"},
		{NewVectorDBSpecScraper(), "mirror.example.com/docker.io/qdrant/qdrant:v1.12.0"},
		{NewVectorDBSpecScraper(), "quay.io/docker.io/milvusdb/milvus:v2.4.0"},
		{NewVectorDBSpecScraper(), "docker.io/milvusdb/milvus-operator:v1.0.0"},
		{NewAgentSpecScraper(), "ghcr.io/langflowai/langflow:1.1.0"},
		{NewAgentSpecScraper(), "docker.io/localhost/langflowai/langflow:1.1.0"},
		{NewTrainingSpecScraper(), "docker.io/ghcr.io/google/jax:latest"},
		{NewTrainingSpecScraper(), "registry-1.docker.io.example.com/pytorch/pytorch:2.3.0"},
		{NewEvalSpecScraper(), "ghcr.io/eleutherai/lm-eval:0.4.2"},
	}
	for _, tc := range quiet {
		t.Run(tc.scraper.Name()+"/"+tc.image, func(t *testing.T) {
			if got := scrapeImage(t, tc.scraper, tc.image); got != nil {
				t.Errorf("%q matched %q via %q, want no match", tc.image, got.Name, got.Properties["runtime.pattern"])
			}
		})
	}

	// The GHCR-anchored jax pattern still matches the image as written.
	if got := scrapeImage(t, NewTrainingSpecScraper(), "ghcr.io/google/jax:latest"); got == nil || got.Name != "jax" {
		t.Errorf("ghcr.io/google/jax matched %+v, want jax", got)
	}
}
