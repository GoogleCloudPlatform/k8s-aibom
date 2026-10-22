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
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/sink"
)

// TestReconcile_SinkMetaCarriesScrapedCategory pins that the category
// handed to external sinks (the GCS {category} path token and the
// webhook category header) is the one the scraper assigned, the same
// value the document's aibom.workload.category and the AIBOM status
// carry. The per-kind reconcilers seed every workload as "inference";
// the MultiScraper may reclassify it.
func TestReconcile_SinkMetaCarriesScrapedCategory(t *testing.T) {
	cases := []struct {
		name  string
		image string
		want  string
	}{
		{name: "agent", image: "langflowai/langflow:1.1.1", want: "agent"},
		{name: "vector database", image: "qdrant/qdrant:v1.12.4", want: string(scraper.CategoryVectorDB)},
		{name: "inference", image: "vllm/vllm-openai:v0.6.3", want: "inference"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
				Name:   "ai-team",
				Labels: map[string]string{configpkg.DefaultNamespaceOptInLabel: "true"},
			}}
			dep := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "ai-team", UID: "uid-app", Generation: 1},
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: tc.image}}},
					},
				},
			}
			c := fake.NewClientBuilder().
				WithScheme(newConfigFakeScheme(t)).
				WithObjects(ns, dep).
				WithStatusSubresource(&aibomv1beta1.AIBOM{}).
				Build()

			rs := &recordingSink{name: "recording", url: "gs://test-bucket/path.json"}
			snap := configpkg.DefaultSnapshot()
			snap.ExternalSinks = []sink.Sink{rs}
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
				ConfigStore:       configpkg.NewStore(snap),
				ControllerName:    "k8s-aibom",
				ControllerVersion: "0.1.0-test",
			}}

			if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ai-team", Name: "app"}}); err != nil {
				t.Fatalf("Reconcile: %v", err)
			}

			var got aibomv1beta1.AIBOM
			if err := c.Get(ctx, types.NamespacedName{Namespace: "ai-team", Name: AIBOMNameForDeployment(dep)}, &got); err != nil {
				t.Fatalf("get AIBOM: %v", err)
			}
			if got.Status.Summary == nil || got.Status.Summary.Workload.Category != tc.want {
				t.Fatalf("status.summary.workload.category = %+v, want %q", got.Status.Summary, tc.want)
			}
			if rs.emitCount() != 1 {
				t.Fatalf("sink emits = %d, want 1", rs.emitCount())
			}
			if cat := rs.emits[0].WorkloadCategory; cat != tc.want {
				t.Errorf("SinkMeta.WorkloadCategory = %q, want %q (the category the document and status carry)", cat, tc.want)
			}
		})
	}
}
