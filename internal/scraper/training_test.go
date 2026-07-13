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
	"fmt"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestTrainingSpecScraper_HandlesKind(t *testing.T) {
	s := NewTrainingSpecScraper()
	cases := []struct {
		kind WorkloadKind
		want bool
	}{
		{WorkloadKind{Group: "batch", Version: "v1", Kind: "Job"}, true},
		{WorkloadKind{Group: "kubeflow.org", Version: "v1", Kind: "PyTorchJob"}, true},
		{WorkloadKind{Group: "ray.io", Version: "v1", Kind: "RayJob"}, true},
		{WorkloadKind{Group: "apps", Version: "v1", Kind: "Deployment"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.kind.Kind, func(t *testing.T) {
			if got := s.HandlesKind(tc.kind); got != tc.want {
				t.Errorf("HandlesKind(%+v) = %v, want %v", tc.kind, got, tc.want)
			}
		})
	}
}

func TestTrainingSpecScraper_Scrape(t *testing.T) {
	s := NewTrainingSpecScraper()

	cases := []struct {
		name           string
		image          string
		env            []corev1.EnvVar
		wantCategory   WorkloadCategory
		wantComponents []Component
		isFallback     bool
		numReplicas    int
	}{
		{
			name:         "PyTorch Image - Match",
			image:        "pytorch/pytorch:2.1.0-cuda12.1-cudnn8-runtime",
			wantCategory: CategoryTraining,
			wantComponents: []Component{
				{
					Type:       ComponentApplication,
					Name:       "pytorch",
					Confidence: ConfidenceInferred,
					Evidence: Evidence{
						Source:  SourceImagePattern,
						Locator: "spec.template.spec.containers[0].image",
					},
				},
			},
		},
		{
			name:         "Wandb Env Key - Match",
			image:        "ubuntu:latest",
			env: []corev1.EnvVar{
				{
					Name:  "WANDB_API_KEY",
					Value: "secret-wandb-key",
				},
			},
			wantCategory: CategoryTraining,
			wantComponents: []Component{
				{
					Type:       ComponentApplication,
					Name:       "wandb",
					Confidence: ConfidenceInferred,
					Evidence: Evidence{
						Source:  SourceEnvVarNamePresent,
						Locator: "spec.template.spec.containers[0].env[WANDB_API_KEY]",
					},
				},
			},
		},
		{
			name:  "Hugging Face Token - No Match",
			image: "ubuntu:latest",
			env: []corev1.EnvVar{
				{
					Name:  "HUGGING_FACE_HUB_TOKEN",
					Value: "hf_token123",
				},
			},
			wantCategory:   "",
			wantComponents: nil,
		},
		{
			name:        "Fallback Multi-Replica Deduplication - Match Once",
			image:       "pytorch/pytorch:2.1.0-cuda12.1-cudnn8-runtime",
			isFallback:  true,
			numReplicas: 3,
			env: []corev1.EnvVar{
				{
					Name:  "WANDB_API_KEY",
					Value: "secret-wandb-key",
				},
			},
			wantCategory: CategoryTraining,
			wantComponents: []Component{
				{
					Type:       ComponentApplication,
					Name:       "pytorch",
					Confidence: ConfidenceInferred,
					Evidence: Evidence{
						Source:  SourceImagePattern,
						Locator: "spec.containers.image",
					},
				},
				{
					Type:       ComponentApplication,
					Name:       "wandb",
					Confidence: ConfidenceInferred,
					Evidence: Evidence{
						Source:  SourceEnvVarNamePresent,
						Locator: "spec.containers.env[WANDB_API_KEY]",
					},
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var w Workload
			if tc.isFallback {
				var pods []corev1.Pod
				n := tc.numReplicas
				if n == 0 {
					n = 1
				}
				for i := 0; i < n; i++ {
					pods = append(pods, corev1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Name:      fmt.Sprintf("test-pod-%d", i),
							Namespace: "test-ns",
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "trainer",
									Image: tc.image,
									Env:   tc.env,
								},
							},
						},
					})
				}
				w = Workload{
					Kind:   WorkloadKind{Group: "ray.io", Version: "v1", Kind: "RayJob"},
					Object: &corev1.Pod{}, // triggers default case
					Pods:   pods,
				}
			} else {
				w = Workload{
					Kind: WorkloadKind{Group: "batch", Version: "v1", Kind: "Job"},
					Object: &batchv1.Job{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "test-job",
							Namespace: "test-ns",
						},
						Spec: batchv1.JobSpec{
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{
										{
											Name:  "trainer",
											Image: tc.image,
											Env:   tc.env,
										},
									},
								},
							},
						},
					},
				}
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
