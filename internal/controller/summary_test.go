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

package controller

import (
	"context"
	"encoding/json"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	aibomv1beta1 "github.com/GoogleCloudPlatform/k8s-aibom/api/v1beta1"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/bom"
	configpkg "github.com/GoogleCloudPlatform/k8s-aibom/internal/config"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/scraper"
)

// TestReconcile_SummaryOmitsUnresolvedModels pins the ModelSummary
// contract: status.summary.models never carries confidence
// "unresolved". agent.spec emits an unresolved machine-learning-model
// component for an API-key variable set with valueFrom (the usual way,
// through a Secret). That component stays in the document, with its
// locator; only the summary leaves it out. The same variable set with
// a literal value is inferred and is summarized.
func TestReconcile_SummaryOmitsUnresolvedModels(t *testing.T) {
	cases := []struct {
		name        string
		apiKey      corev1.EnvVar
		wantModels  []aibomv1beta1.ModelSummary
		wantDocConf string
	}{
		{
			name: "valueFrom",
			apiKey: corev1.EnvVar{Name: "OPENAI_API_KEY", ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: "s"},
					Key:                  "k",
				},
			}},
			wantModels:  nil,
			wantDocConf: "unresolved",
		},
		{
			name:   "literal value",
			apiKey: corev1.EnvVar{Name: "OPENAI_API_KEY", Value: "not-a-real-key"},
			wantModels: []aibomv1beta1.ModelSummary{{
				Identity:   "openai-api",
				Source:     string(scraper.SourceEnvVarNamePresent),
				Confidence: "inferred",
			}},
			wantDocConf: "inferred",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
				Name:   "ai-team",
				Labels: map[string]string{configpkg.DefaultNamespaceOptInLabel: "true"},
			}}
			dep := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Name: "agent", Namespace: "ai-team", UID: "uid-agent", Generation: 1},
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{Containers: []corev1.Container{{
							Name:  "app",
							Image: "example.com/agent:1.0",
							Env: []corev1.EnvVar{
								{Name: "LANGCHAIN_TRACING_V2", Value: "true"},
								tc.apiKey,
							},
						}}},
					},
				},
			}
			c := fake.NewClientBuilder().
				WithScheme(newConfigFakeScheme(t)).
				WithObjects(ns, dep).
				WithStatusSubresource(&aibomv1beta1.AIBOM{}).
				Build()

			r := &DeploymentReconciler{WorkloadReconciler: WorkloadReconciler{
				Client: c,
				Scheme: c.Scheme(),
				// The manager's production scraper set (cmd/manager/main.go).
				Scraper: scraper.NewMultiScraper(
					scraper.NewInferenceSpecScraper(nil),
					scraper.NewVectorDBSpecScraper(),
					scraper.NewAgentSpecScraper(),
					scraper.NewTrainingSpecScraper(),
					scraper.NewEvalSpecScraper(),
				),
				BOMBuilder:        bom.NewBuilder(),
				StatusBuilder:     NewStatusBuilder(),
				ConfigStore:       configpkg.NewStore(configpkg.DefaultSnapshot()),
				ControllerName:    "k8s-aibom",
				ControllerVersion: "0.1.0-test",
			}}

			if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ai-team", Name: "agent"}}); err != nil {
				t.Fatalf("Reconcile: %v", err)
			}

			var got aibomv1beta1.AIBOM
			if err := c.Get(ctx, types.NamespacedName{Namespace: "ai-team", Name: AIBOMNameForDeployment(dep)}, &got); err != nil {
				t.Fatalf("get AIBOM: %v", err)
			}
			if got.Status.Summary == nil {
				t.Fatal("status.summary is nil")
			}
			if cat := got.Status.Summary.Workload.Category; cat != string(scraper.CategoryAgent) {
				t.Fatalf("summary category = %q, want %q", cat, scraper.CategoryAgent)
			}
			for _, m := range got.Status.Summary.Models {
				if m.Confidence == string(scraper.ConfidenceUnresolved) {
					t.Errorf("summary model %+v has confidence unresolved; ModelSummary says unresolved attributes are not summarized", m)
				}
			}
			if len(got.Status.Summary.Models) != len(tc.wantModels) {
				t.Fatalf("summary models = %+v, want %+v", got.Status.Summary.Models, tc.wantModels)
			}
			for i, want := range tc.wantModels {
				if m := got.Status.Summary.Models[i]; m.Identity != want.Identity || m.Source != want.Source || m.Confidence != want.Confidence {
					t.Errorf("summary models[%d] = %+v, want %+v", i, m, want)
				}
			}

			// The document still records the dependency either way.
			if got.Status.BOMDocument == nil || got.Status.BOMDocument.Inline == nil {
				t.Fatal("expected an inline BOM document")
			}
			if conf := docModelConfidence(t, got.Status.BOMDocument.Inline.Data, "openai-api"); conf != tc.wantDocConf {
				t.Errorf("document openai-api confidence = %q, want %q", conf, tc.wantDocConf)
			}
		})
	}
}

// docModelConfidence returns the aibom.confidence property of the
// machine-learning-model component named name in a CycloneDX JSON
// document, failing the test if there is none.
func docModelConfidence(t *testing.T, data []byte, name string) string {
	t.Helper()
	var doc struct {
		Components []struct {
			Type       string `json:"type"`
			Name       string `json:"name"`
			Properties []struct {
				Name  string `json:"name"`
				Value string `json:"value"`
			} `json:"properties"`
		} `json:"components"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("decode inline document: %v", err)
	}
	for _, c := range doc.Components {
		if c.Type != "machine-learning-model" || c.Name != name {
			continue
		}
		for _, p := range c.Properties {
			if p.Name == "k8s-aibom:confidence" {
				return p.Value
			}
		}
		t.Fatalf("component %q has no k8s-aibom:confidence property", name)
	}
	t.Fatalf("document has no machine-learning-model component %q", name)
	return ""
}
