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
			wantCategory:   "",
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

// TestAgentSpecScraper_RemoteAPIDependencyType pins dependency.type on
// both forms of an API key. A key set through valueFrom (the usual
// Secret reference) is unresolved because its value is not read, but the
// variable name still says the workload calls a remote API.
func TestAgentSpecScraper_RemoteAPIDependencyType(t *testing.T) {
	w := Workload{
		Kind: WorkloadKind{Group: "apps", Version: "v1", Kind: "Deployment"},
		Object: &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "agent", Namespace: "test-ns"},
			Spec: appsv1.DeploymentSpec{
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{
							Name:  "agent",
							Image: "langflowai/langflow:1.0.0",
							Env: []corev1.EnvVar{
								{Name: "OPENAI_API_KEY", Value: "sk-proj-12345"},
								{Name: "ANTHROPIC_API_KEY", ValueFrom: &corev1.EnvVarSource{
									SecretKeyRef: &corev1.SecretKeySelector{
										LocalObjectReference: corev1.LocalObjectReference{Name: "llm-keys"},
										Key:                  "anthropic",
									},
								}},
							},
						}},
					},
				},
			},
		},
	}
	got, err := NewAgentSpecScraper().Scrape(context.Background(), w, nil)
	if err != nil {
		t.Fatalf("Scrape failed: %v", err)
	}
	want := map[string]struct {
		confidence Confidence
		locator    string
	}{
		"openai-api":    {ConfidenceInferred, "spec.template.spec.containers[0].env[OPENAI_API_KEY]"},
		"anthropic-api": {ConfidenceUnresolved, "spec.template.spec.containers[0].env[ANTHROPIC_API_KEY].valueFrom"},
	}
	seen := 0
	for _, c := range got.Components {
		if c.Type != ComponentMLModel {
			continue
		}
		exp, ok := want[c.Name]
		if !ok {
			t.Errorf("unexpected model component %q", c.Name)
			continue
		}
		seen++
		if c.Confidence != exp.confidence {
			t.Errorf("%s confidence = %q, want %q", c.Name, c.Confidence, exp.confidence)
		}
		if c.Evidence.Locator != exp.locator {
			t.Errorf("%s locator = %q, want %q", c.Name, c.Evidence.Locator, exp.locator)
		}
		if v := c.Properties["dependency.type"]; v != "remote-api" {
			t.Errorf("%s dependency.type = %q, want %q", c.Name, v, "remote-api")
		}
	}
	if seen != len(want) {
		t.Errorf("found %d API dependency components, want %d", seen, len(want))
	}
}

// TestAgentSpecScraper_ImageNameBoundary: each framework image pattern
// ends at the image-name boundary (a tag, a digest or the end of the
// reference), so the framework's own images match in every form and
// other images in the vendor's namespace whose name starts with the
// framework's do not.
func TestAgentSpecScraper_ImageNameBoundary(t *testing.T) {
	const digest = "@sha256:4f53cda18c2baa0c0354bb5f9a3ecbe5ed12ab4d8e11ba873c2f11161202b945"
	match := []struct{ image, want string }{
		{"langflowai/langflow:1.13.4", "langflow"},
		{"langflowai/langflow" + digest, "langflow"},
		{"langflowai/langflow", "langflow"},
		{"langflowai/langflow-nightly:1.14.0.dev41", "langflow"},
		{"langflowai/langflow-backend:1.13.4", "langflow"},
		{"langflowai/langflow-frontend:1.13.4-arm64", "langflow"},
		{"langflowai/langflow-backend-nightly:1.0.19.dev19", "langflow"},
		{"langflowai/langflow-frontend-nightly:1.0.19.dev19", "langflow"},
		{"langflowai/langflow-all:1.13.4", "langflow"},
		{"langflowai/langflow-all-nightly:v1.4.2.dev7", "langflow"},
		{"langflowai/langflow-ep:1.13.4", "langflow"},
		{"flowiseai/flowise:3.1.4", "flowise"},
		{"flowiseai/flowise" + digest, "flowise"},
		{"flowiseai/flowise-worker:3.1.4", "flowise"},
		{"chainlit/chainlit:1.0.0", "chainlit"},
		{"chainlit/chainlit" + digest, "chainlit"},
		{"deepset/haystack:base-v2.5.0", "haystack"},
		{"deepset/haystack" + digest, "haystack"},
		{"deepset/haystack-cpu:latest", "haystack"},
		{"deepset/haystack-gpu:minimal-latest", "haystack"},
		{"deepset/haystack-m1:0.10.0", "haystack"},
	}
	for _, tc := range match {
		t.Run(tc.image, func(t *testing.T) {
			got := scrapeImage(t, NewAgentSpecScraper(), tc.image)
			if got == nil || got.Name != tc.want {
				t.Fatalf("%q matched %+v, want %q", tc.image, got, tc.want)
			}
		})
	}

	quiet := []string{
		// deepset's annotation tool and the Haystack demo images (the
		// Streamlit UI, the Elasticsearch instance preloaded with demo
		// documents) run next to Haystack but are not Haystack.
		"deepset/haystack-annotation:0.1.75",
		"deepset/haystack-streamlit-ui:0.10.0",
		"deepset/haystack-demo-ui:latest",
		"deepset/haystack-demo-elasticsearch:0.2.1",
		// Names that only start with the framework's.
		"langflowai/langflow-docs:latest",
		"flowiseai/flowise-embed:latest",
		"chainlit/chainlit-cookbook:latest",
	}
	for _, img := range quiet {
		t.Run(img, func(t *testing.T) {
			if got := scrapeImage(t, NewAgentSpecScraper(), img); got != nil {
				t.Errorf("%q matched %q via %q, want no match", img, got.Name, got.Properties["runtime.pattern"])
			}
		})
	}
}
