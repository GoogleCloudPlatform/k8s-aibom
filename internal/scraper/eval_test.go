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

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestEvalSpecScraper_HandlesKind(t *testing.T) {
	s := NewEvalSpecScraper()
	cases := []struct {
		kind WorkloadKind
		want bool
	}{
		{WorkloadKind{Group: "batch", Version: "v1", Kind: "Job"}, true},
		{WorkloadKind{Group: "batch", Version: "v1", Kind: "CronJob"}, true},
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

func TestEvalSpecScraper_Scrape(t *testing.T) {
	s := NewEvalSpecScraper()

	cases := []struct {
		name           string
		image          string
		wantCategory   WorkloadCategory
		wantComponents []Component
	}{
		{
			name:         "LM-Eval - Match",
			image:        "eleutherai/lm-eval:v0.4.0",
			wantCategory: CategoryEval,
			wantComponents: []Component{
				{
					Type:       ComponentApplication,
					Name:       "lm-eval",
					Confidence: ConfidenceInferred,
					Evidence: Evidence{
						Source:  SourceImagePattern,
						Locator: "spec.containers.image",
					},
				},
			},
		},
		{
			name:         "Ragas Official - Match",
			image:        "explodinggradients/ragas:latest",
			wantCategory: CategoryEval,
			wantComponents: []Component{
				{
					Type:       ComponentApplication,
					Name:       "ragas",
					Confidence: ConfidenceInferred,
					Evidence: Evidence{
						Source:  SourceImagePattern,
						Locator: "spec.containers.image",
					},
				},
			},
		},
		{
			name:           "Custom Ragas Substring - No Match",
			image:          "gcr.io/my-registry/my-custom-ragas-helper:latest",
			wantCategory:   "",
			wantComponents: nil,
		},
		{
			name:         "TruLens Official - Match",
			image:        "trulens/trulens:v0.2.0",
			wantCategory: CategoryEval,
			wantComponents: []Component{
				{
					Type:       ComponentApplication,
					Name:       "trulens",
					Confidence: ConfidenceInferred,
					Evidence: Evidence{
						Source:  SourceImagePattern,
						Locator: "spec.containers.image",
					},
				},
			},
		},
		{
			name:           "Custom TruLens Substring - No Match",
			image:          "gcr.io/my-registry/trulens-dashboard-custom:latest",
			wantCategory:   "",
			wantComponents: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := Workload{
				Kind: WorkloadKind{Group: "batch", Version: "v1", Kind: "Job"},
				Object: &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod",
						Namespace: "test-ns",
					},
				},
				Pods: []corev1.Pod{
					{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "evaluator",
									Image: tc.image,
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
