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

	batchv1 "k8s.io/api/batch/v1"
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
						Locator: "spec.template.spec.containers[0].image",
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
						Locator: "spec.template.spec.containers[0].image",
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
						Locator: "spec.template.spec.containers[0].image",
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
										Name:  "evaluator",
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

// A CronJob's pod spec lives at spec.jobTemplate.spec.template.spec, so
// its evidence locator must name that path, not the Job's
// spec.template.spec (which does not exist on a CronJob).
func TestEvalSpecScraper_LocatorNamesThePathInTheKind(t *testing.T) {
	podTemplate := corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "setup", Image: "busybox:1.36"},
				{Name: "evaluator", Image: "eleutherai/lm-eval:v0.4.0"},
			},
		},
	}
	cases := []struct {
		name string
		w    Workload
		want string
	}{
		{
			name: "Job",
			w: Workload{
				Kind:   WorkloadKind{Group: "batch", Version: "v1", Kind: "Job"},
				Object: &batchv1.Job{Spec: batchv1.JobSpec{Template: podTemplate}},
			},
			want: "spec.template.spec.containers[1].image",
		},
		{
			name: "CronJob",
			w: Workload{
				Kind: WorkloadKind{Group: "batch", Version: "v1", Kind: "CronJob"},
				Object: &batchv1.CronJob{Spec: batchv1.CronJobSpec{
					JobTemplate: batchv1.JobTemplateSpec{Spec: batchv1.JobSpec{Template: podTemplate}},
				}},
			},
			want: "spec.jobTemplate.spec.template.spec.containers[1].image",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NewEvalSpecScraper().Scrape(context.Background(), tc.w, nil)
			if err != nil {
				t.Fatalf("Scrape failed: %v", err)
			}
			if got.Category != CategoryEval || len(got.Components) != 1 {
				t.Fatalf("got category %q with %d components, want %q with 1", got.Category, len(got.Components), CategoryEval)
			}
			if loc := got.Components[0].Evidence.Locator; loc != tc.want {
				t.Errorf("Evidence.Locator = %q, want %q", loc, tc.want)
			}
		})
	}
}
