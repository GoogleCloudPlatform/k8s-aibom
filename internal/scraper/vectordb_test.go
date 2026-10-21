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
)

func TestVectorDBSpecScraper_HandlesKind(t *testing.T) {
	s := NewVectorDBSpecScraper()
	cases := []struct {
		kind WorkloadKind
		want bool
	}{
		{WorkloadKind{Group: "apps", Version: "v1", Kind: "Deployment"}, true},
		{WorkloadKind{Group: "apps", Version: "v1", Kind: "StatefulSet"}, true},
		{WorkloadKind{Group: "apps", Version: "v1", Kind: "DaemonSet"}, true},
		{WorkloadKind{Group: "apps", Version: "v1", Kind: "ReplicaSet"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.kind.Kind, func(t *testing.T) {
			if got := s.HandlesKind(tc.kind); got != tc.want {
				t.Errorf("HandlesKind(%+v) = %v, want %v", tc.kind, got, tc.want)
			}
		})
	}
}

func TestVectorDBSpecScraper_Scrape(t *testing.T) {
	s := NewVectorDBSpecScraper()

	cases := []struct {
		name           string
		image          string
		wantCategory   WorkloadCategory
		wantComponents []Component
	}{
		{
			name:           "Standard Postgres Image - No Match",
			image:          "postgres:15-alpine",
			wantCategory:   "",
			wantComponents: nil,
		},
		{
			name:           "Postgres with pgvector tag - No Match",
			image:          "postgres:16-pgvector",
			wantCategory:   "",
			wantComponents: nil,
		},
		{
			name:           "Postgres with pgvector tag alpine - No Match",
			image:          "library/postgres:15-alpine-pgvector",
			wantCategory:   "",
			wantComponents: nil,
		},
		{
			name:           "Custom image with pgvector substring - No Match",
			image:          "gcr.io/my-registry/my-pgvector-helper:latest",
			wantCategory:   "",
			wantComponents: nil,
		},
		{
			name:         "Ankane pgvector - Match",
			image:        "ankane/pgvector:latest",
			wantCategory: CategoryVectorDB,
			wantComponents: []Component{
				{
					Type:       ComponentApplication,
					Name:       "pgvector",
					Confidence: ConfidenceInferred,
					Evidence: Evidence{
						Source:  SourceImagePattern,
						Locator: "spec.template.spec.containers[0].image",
					},
				},
			},
		},
		{
			name:         "Official pgvector namespace - Match",
			image:        "pgvector/pgvector:0.5.0",
			wantCategory: CategoryVectorDB,
			wantComponents: []Component{
				{
					Type:       ComponentApplication,
					Name:       "pgvector",
					Confidence: ConfidenceInferred,
					Evidence: Evidence{
						Source:  SourceImagePattern,
						Locator: "spec.template.spec.containers[0].image",
					},
				},
			},
		},
		{
			name:         "Weaviate Official - Match",
			image:        "semitechnologies/weaviate:1.24.0",
			wantCategory: CategoryVectorDB,
			wantComponents: []Component{
				{
					Type:       ComponentApplication,
					Name:       "weaviate",
					Confidence: ConfidenceInferred,
					Evidence: Evidence{
						Source:  SourceImagePattern,
						Locator: "spec.template.spec.containers[0].image",
					},
				},
			},
		},
		{
			name:           "Custom image containing weaviate substring - No Match",
			image:          "gcr.io/my-registry/my-weaviate-adapter:latest",
			wantCategory:   "",
			wantComponents: nil,
		},
		vectorDBMatchCase("Milvus Official - Match", "milvusdb/milvus:v2.4.15", "milvus"),
		vectorDBMatchCase("Milvus by digest - Match", "milvusdb/milvus@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", "milvus"),
		vectorDBMatchCase("Milvus dev build - Match", "milvusdb/milvus-dev:master-20251020-1a2b3c4d-amd64", "milvus"),
		vectorDBMatchCase("Qdrant Official - Match", "qdrant/qdrant:v1.12.4", "qdrant"),
		vectorDBMatchCase("Chroma Official - Match", "chromadb/chroma:0.6.3", "chroma"),
		// Sibling images in the vendors' namespaces that are not the
		// database itself: the Milvus operator, its backup, CDC and
		// config tools and the Attu predecessor GUI, and an
		// unrelated repository under the qdrant namespace.
		{name: "Milvus operator - No Match", image: "milvusdb/milvus-operator:v1.1.3"},
		{name: "Milvus backup tool - No Match", image: "milvusdb/milvus-backup:v0.5.0"},
		{name: "Milvus config tool - No Match", image: "milvusdb/milvus-config-tool:v0.1.2"},
		{name: "Milvus CDC - No Match", image: "milvusdb/milvus-cdc:v2.0.0"},
		{name: "Milvus insight GUI - No Match", image: "milvusdb/milvus-insight:v0.1.0"},
		{name: "Qdrant-prefixed other repo - No Match", image: "qdrant/qdrant-baseten-docker:latest"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := Workload{
				Kind: WorkloadKind{Group: "apps", Version: "v1", Kind: "Deployment"},
				Object: &appsv1.Deployment{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-deployment",
						Namespace: "test-ns",
					},
					Spec: appsv1.DeploymentSpec{
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{
									{
										Name:  "db",
										Image: tc.image,
									},
								},
							},
						},
					},
				},
			}
			got, err := s.Scrape(context.Background(), w, nil)
			if err != nil {
				t.Fatalf("Scrape failed: %v", err)
			}
			if got.Category != tc.wantCategory {
				t.Errorf("Category = %q, want %q", got.Category, tc.wantCategory)
			}
			if len(got.Components) != len(tc.wantComponents) {
				t.Fatalf("len(Components) = %d, want %d", len(got.Components), len(tc.wantComponents))
			}
			for _, wantComp := range tc.wantComponents {
				found := false
				for _, gotComp := range got.Components {
					if gotComp.Type == wantComp.Type && gotComp.Name == wantComp.Name && gotComp.Confidence == wantComp.Confidence {
						if gotComp.Evidence.Source == wantComp.Evidence.Source && gotComp.Evidence.Locator == wantComp.Evidence.Locator {
							found = true
							break
						}
					}
				}
				if !found {
					t.Errorf("expected component not found: %+v", wantComp)
				}
			}
		})
	}
}

// vectorDBMatchCase is a TestVectorDBSpecScraper_Scrape row for an
// image that is the named vector database, in container 0.
func vectorDBMatchCase(name, image, db string) struct {
	name           string
	image          string
	wantCategory   WorkloadCategory
	wantComponents []Component
} {
	return struct {
		name           string
		image          string
		wantCategory   WorkloadCategory
		wantComponents []Component
	}{
		name:         name,
		image:        image,
		wantCategory: CategoryVectorDB,
		wantComponents: []Component{{
			Type:       ComponentApplication,
			Name:       db,
			Confidence: ConfidenceInferred,
			Evidence: Evidence{
				Source:  SourceImagePattern,
				Locator: "spec.template.spec.containers[0].image",
			},
		}},
	}
}
