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

func TestAgentSpecScraper_Name(t *testing.T) {
	s := NewAgentSpecScraper()
	if s.Name() != "agent.spec" {
		t.Errorf("Name() = %q, want %q", s.Name(), "agent.spec")
	}
}

func TestAgentSpecScraper_HandlesKind(t *testing.T) {
	s := NewAgentSpecScraper()
	cases := []struct {
		kind WorkloadKind
		want bool
	}{
		{WorkloadKind{Group: "apps", Version: "v1", Kind: "Deployment"}, true},
		{WorkloadKind{Group: "apps", Version: "v1", Kind: "StatefulSet"}, true},
		{WorkloadKind{Group: "apps", Version: "v1", Kind: "DaemonSet"}, true},
		{WorkloadKind{Group: "apps", Version: "v1", Kind: "ReplicaSet"}, false},
		{WorkloadKind{Group: "serving.kserve.io", Version: "v1beta1", Kind: "InferenceService"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.kind.Kind, func(t *testing.T) {
			if got := s.HandlesKind(tc.kind); got != tc.want {
				t.Errorf("HandlesKind(%+v) = %v, want %v", tc.kind, got, tc.want)
			}
		})
	}
}

func TestAgentSpecScraper_Scrape(t *testing.T) {
	s := NewAgentSpecScraper()

	cases := []struct {
		name           string
		containers     []corev1.Container
		wantCategory   WorkloadCategory
		wantConfidence Confidence
		wantComponents []Component
	}{
		{
			name: "No Agent Signal",
			containers: []corev1.Container{
				{
					Name:  "web",
					Image: "nginx:latest",
				},
			},
			wantCategory:   "",
			wantConfidence: ConfidenceUnresolved,
			wantComponents: nil,
		},
		{
			name: "Langflow Image",
			containers: []corev1.Container{
				{
					Name:  "agent-ui",
					Image: "langflowai/langflow:v1.0.0",
				},
			},
			wantCategory:   CategoryAgent,
			wantConfidence: ConfidenceInferred,
			wantComponents: []Component{
				{
					Type:       ComponentApplication,
					Name:       "langflow",
					Confidence: ConfidenceInferred,
					Evidence: Evidence{
						Source:  SourceImagePattern,
						Locator: "spec.template.spec.containers[0].image",
					},
				},
			},
		},
		{
			name: "Haystack Image and Env",
			containers: []corev1.Container{
				{
					Name:  "agent-runner",
					Image: "deepset/haystack:base-v2.0.0",
					Env: []corev1.EnvVar{
						{
							Name:  "PIPELINE_YAML_PATH",
							Value: "/app/pipeline.yaml",
						},
					},
				},
			},
			wantCategory:   CategoryAgent,
			wantConfidence: ConfidenceInferred,
			wantComponents: []Component{
				{
					Type:       ComponentApplication,
					Name:       "haystack",
					Confidence: ConfidenceInferred,
					Evidence: Evidence{
						Source:  SourceImagePattern,
						Locator: "spec.template.spec.containers[0].image",
					},
				},
				{
					Type:       ComponentApplication,
					Name:       "haystack",
					Confidence: ConfidenceInferred,
					Evidence: Evidence{
						Source:  SourceEnvVarNamePresent,
						Locator: "spec.template.spec.containers[0].env[PIPELINE_YAML_PATH]",
					},
				},
			},
		},
		{
			name: "LlamaIndex and DSPy Env Vars and OpenAI API",
			containers: []corev1.Container{
				{
					Name:  "agent",
					Image: "my-custom-agent:latest",
					Env: []corev1.EnvVar{
						{
							Name:  "LLAMA_CLOUD_API_KEY",
							Value: "secret-key",
						},
						{
							Name:  "DSPY_PROFILES_PATH",
							Value: "/app/profiles.toml",
						},
						{
							Name:  "OPENAI_API_KEY",
							Value: "sk-proj-12345",
						},
					},
				},
			},
			wantCategory:   CategoryAgent,
			wantConfidence: ConfidenceInferred,
			wantComponents: []Component{
				{
					Type:       ComponentApplication,
					Name:       "llamaindex",
					Confidence: ConfidenceInferred,
					Evidence: Evidence{
						Source:  SourceEnvVarNamePresent,
						Locator: "spec.template.spec.containers[0].env[LLAMA_CLOUD_API_KEY]",
					},
				},
				{
					Type:       ComponentApplication,
					Name:       "dspy",
					Confidence: ConfidenceInferred,
					Evidence: Evidence{
						Source:  SourceEnvVarNamePresent,
						Locator: "spec.template.spec.containers[0].env[DSPY_PROFILES_PATH]",
					},
				},
				{
					Type:       ComponentMLModel,
					Name:       "openai-api",
					Confidence: ConfidenceInferred,
					Evidence: Evidence{
						Source:  SourceEnvVarNamePresent,
						Locator: "spec.template.spec.containers[0].env[OPENAI_API_KEY]",
					},
				},
			},
		},
		{
			name: "Azure OpenAI API Key and Endpoint",
			containers: []corev1.Container{
				{
					Name:  "agent-azure",
					Image: "my-azure-agent:latest",
					Env: []corev1.EnvVar{
						{
							Name:  "AZURE_OPENAI_API_KEY",
							Value: "secret-key",
						},
						{
							Name:  "AZURE_OPENAI_ENDPOINT",
							Value: "https://my-resource.openai.azure.com/",
						},
					},
				},
			},
			wantCategory:   CategoryAgent,
			wantConfidence: ConfidenceInferred,
			wantComponents: []Component{
				{
					Type:       ComponentMLModel,
					Name:       "azure-openai-api",
					Confidence: ConfidenceInferred,
					Evidence: Evidence{
						Source:  SourceEnvVarNamePresent,
						Locator: "spec.template.spec.containers[0].env[AZURE_OPENAI_API_KEY]",
					},
				},
				{
					Type:       ComponentMLModel,
					Name:       "azure-openai-api",
					Confidence: ConfidenceInferred,
					Evidence: Evidence{
						Source:  SourceEnvVarNamePresent,
						Locator: "spec.template.spec.containers[0].env[AZURE_OPENAI_ENDPOINT]",
					},
				},
			},
		},
		{
			name: "GOOGLE_API_KEY False Positive Check",
			containers: []corev1.Container{
				{
					Name:  "app",
					Image: "my-generic-app:latest",
					Env: []corev1.EnvVar{
						{
							Name:  "GOOGLE_API_KEY",
							Value: "ai_somekey123",
						},
					},
				},
			},
			wantCategory:   "",
			wantConfidence: ConfidenceUnresolved,
			wantComponents: nil,
		},
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
								Containers: tc.containers,
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
			if got.Confidence != tc.wantConfidence {
				t.Errorf("Confidence = %q, want %q", got.Confidence, tc.wantConfidence)
			}
			if len(got.Components) != len(tc.wantComponents) {
				t.Fatalf("len(Components) = %d, want %d", len(got.Components), len(tc.wantComponents))
			}

			// Validate each expected component
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
