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
	"k8s.io/apimachinery/pkg/types"
)

// TestInferenceSpecScraper_StatefulSet_HappyPath confirms the scraper
// extracts the same Components from a StatefulSet as from a structurally
// equivalent Deployment. Since the extraction path shares scrapePodSpec,
// the test validates the type-switch case routing rather than re-testing
// the underlying extraction (which TestInferenceSpecScraper_VLLMDeployment_HappyPath
// already covers).
func TestInferenceSpecScraper_StatefulSet_HappyPath(t *testing.T) {
	s := newScraper()
	ss := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: "vllm-stateful", Namespace: "prod-inference",
			UID: types.UID("ss-123"),
		},
		Spec: appsv1.StatefulSetSpec{
			ServiceName: "vllm-stateful",
			Selector:    &metav1.LabelSelector{MatchLabels: map[string]string{"app": "vllm-stateful"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "vllm-stateful"}},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "vllm",
						Image: "vllm/vllm-openai:v0.6.3",
						Args:  []string{"--model", "meta-llama/Llama-3.1-8B-Instruct"},
						Env: []corev1.EnvVar{
							{Name: "HF_MODEL_ID", Value: "meta-llama/Llama-3.1-8B-Instruct"},
						},
					}},
				},
			},
		},
	}
	w := Workload{
		Kind:      WorkloadKind{Group: "apps", Version: "v1", Kind: "StatefulSet"},
		Category:  CategoryInference,
		Namespace: ss.Namespace,
		Name:      ss.Name,
		UID:       ss.UID,
		Object:    ss,
	}
	got, err := s.Scrape(context.Background(), w, testConfig())
	if err != nil {
		t.Fatalf("Scrape StatefulSet: %v", err)
	}

	// Same shape as the Deployment happy path: runtime application,
	// container, --model arg as Declared model, HF_MODEL_ID as
	// Inferred model. The scraper doesn't differentiate the parent
	// kind in the extracted Components themselves; the workload-level
	// summary (built by the controller's status builder) is where the
	// kind appears.
	mustFindOne(t, got.Components, func(c Component) bool {
		return c.Type == ComponentApplication && c.Name == "vllm" &&
			c.Confidence == ConfidenceInferred
	}, "expected vllm runtime application Component")
	mustFindOne(t, got.Components, func(c Component) bool {
		return c.Type == ComponentMLModel &&
			c.Name == "meta-llama/Llama-3.1-8B-Instruct" &&
			c.Confidence == ConfidenceDeclared &&
			c.Evidence.Source == SourceContainerArg
	}, "expected --model arg Component")
	mustFindOne(t, got.Components, func(c Component) bool {
		return c.Type == ComponentMLModel &&
			c.Name == "meta-llama/Llama-3.1-8B-Instruct" &&
			c.Confidence == ConfidenceInferred &&
			c.Evidence.Source == SourceEnvVar
	}, "expected HF_MODEL_ID env Component")
}

// TestInferenceSpecScraper_DaemonSet_HappyPath confirms the scraper
// extracts the same Components from a DaemonSet. DaemonSet is a
// node-local deployment shape (one pod per node) commonly used for
// edge inference and per-node GPU workers.
func TestInferenceSpecScraper_DaemonSet_HappyPath(t *testing.T) {
	s := newScraper()
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: "vllm-edge", Namespace: "prod-edge",
			UID: types.UID("ds-456"),
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "vllm-edge"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "vllm-edge"}},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "vllm",
						Image: "vllm/vllm-openai:v0.6.3",
						Args:  []string{"--model", "meta-llama/Llama-3.1-8B-Instruct"},
					}},
				},
			},
		},
	}
	w := Workload{
		Kind:      WorkloadKind{Group: "apps", Version: "v1", Kind: "DaemonSet"},
		Category:  CategoryInference,
		Namespace: ds.Namespace,
		Name:      ds.Name,
		UID:       ds.UID,
		Object:    ds,
	}
	got, err := s.Scrape(context.Background(), w, testConfig())
	if err != nil {
		t.Fatalf("Scrape DaemonSet: %v", err)
	}
	mustFindOne(t, got.Components, func(c Component) bool {
		return c.Type == ComponentApplication && c.Name == "vllm" &&
			c.Confidence == ConfidenceInferred
	}, "expected vllm runtime application Component")
	mustFindOne(t, got.Components, func(c Component) bool {
		return c.Type == ComponentMLModel &&
			c.Name == "meta-llama/Llama-3.1-8B-Instruct" &&
			c.Evidence.Source == SourceContainerArg
	}, "expected --model arg Component")
}
