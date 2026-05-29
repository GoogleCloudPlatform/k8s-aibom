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
	"fmt"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	aibomv1alpha1 "github.com/GoogleCloudPlatform/k8s-aibom/api/v1alpha1"
)

// TestIntegration_DeploymentInOptedInNamespace_ProducesAIBOM is the
// Phase 7 first-integration-milestone test: a Deployment that exhibits
// inference signals, in a namespace labeled with the opt-in label,
// must produce an AIBOM CR with a populated summary in its status.
//
// This is the end-to-end proof of scraper → builder → status writer.
func TestIntegration_DeploymentInOptedInNamespace_ProducesAIBOM(t *testing.T) {
	env := startEnvTest(t)
	ctx := context.Background()

	nsName := "prod-inference"
	depName := "vllm-llama3"

	mustCreateOptedInNamespace(t, env.k8sClient, ctx, nsName)
	dep := vllmDeployment(nsName, depName)
	mustCreate(t, env.k8sClient, ctx, dep)

	// Wait up to 30 seconds for the AIBOM CR to appear with a populated
	// status. controller-runtime cache+reconcile should be much faster
	// than this in envtest, but the timeout absorbs startup variance.
	var got aibomv1alpha1.AIBOM
	aibomKey := types.NamespacedName{
		Name:      "apps-deployment-" + depName,
		Namespace: nsName,
	}
	eventually(t, 30*time.Second, 200*time.Millisecond, func() error {
		if err := env.k8sClient.Get(ctx, aibomKey, &got); err != nil {
			return fmt.Errorf("get AIBOM %s: %w", aibomKey, err)
		}
		if got.Status.Summary == nil {
			return fmt.Errorf("AIBOM exists but status.summary not yet populated")
		}
		return nil
	})

	// Spec assertions.
	if got.Spec.WorkloadRef.Name != depName {
		t.Errorf("Spec.WorkloadRef.Name = %q, want %q", got.Spec.WorkloadRef.Name, depName)
	}
	if got.Spec.WorkloadRef.Kind != "Deployment" {
		t.Errorf("Spec.WorkloadRef.Kind = %q, want %q", got.Spec.WorkloadRef.Kind, "Deployment")
	}
	if got.Spec.BOMFormat != "CycloneDX" {
		t.Errorf("Spec.BOMFormat = %q, want %q", got.Spec.BOMFormat, "CycloneDX")
	}
	if got.Spec.BOMSpecVersion != "1.6" {
		t.Errorf("Spec.BOMSpecVersion = %q, want %q", got.Spec.BOMSpecVersion, "1.6")
	}

	// Status assertions.
	if got.Status.Summary.Workload.Name != depName {
		t.Errorf("Summary.Workload.Name = %q, want %q", got.Status.Summary.Workload.Name, depName)
	}
	if got.Status.Summary.Workload.Category != "inference" {
		t.Errorf("Summary.Workload.Category = %q, want %q", got.Status.Summary.Workload.Category, "inference")
	}
	if got.Status.Summary.Runtime == nil {
		t.Fatal("Summary.Runtime is nil; expected vllm runtime detected from image pattern")
	}
	if got.Status.Summary.Runtime.Name != "vllm" {
		t.Errorf("Runtime.Name = %q, want %q", got.Status.Summary.Runtime.Name, "vllm")
	}
	if len(got.Status.Summary.Models) == 0 {
		t.Error("expected at least one Model from --model arg or HF_MODEL_ID env var")
	}
	if got.Status.BOMHash == "" {
		t.Error("BOMHash is empty")
	}
	if got.Status.BOMDocument == nil {
		t.Fatal("BOMDocument is nil")
	}
	if got.Status.BOMDocument.Inline == nil {
		t.Error("expected inline BOM document for this small workload")
	}

	// Owner reference assertions.
	if len(got.OwnerReferences) != 1 {
		t.Fatalf("expected 1 owner reference, got %d", len(got.OwnerReferences))
	}
	owner := got.OwnerReferences[0]
	if owner.Kind != "Deployment" || owner.Name != depName {
		t.Errorf("owner reference = %+v, want Deployment/%s", owner, depName)
	}
	if owner.Controller == nil || !*owner.Controller {
		t.Error("owner reference should be Controller=true")
	}
}

func TestIntegration_DeploymentInNonOptedInNamespace_NoAIBOM(t *testing.T) {
	env := startEnvTest(t)
	ctx := context.Background()

	nsName := "no-optin"
	depName := "vllm-skipped"

	// Namespace WITHOUT the opt-in label.
	mustCreate(t, env.k8sClient, ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: nsName},
	})
	mustCreate(t, env.k8sClient, ctx, vllmDeployment(nsName, depName))

	// Wait a brief moment to let any spurious reconcile race; then
	// assert no AIBOM exists.
	time.Sleep(500 * time.Millisecond)
	aibomKey := types.NamespacedName{
		Name:      "apps-deployment-" + depName,
		Namespace: nsName,
	}
	var aibom aibomv1alpha1.AIBOM
	err := env.k8sClient.Get(ctx, aibomKey, &aibom)
	if err == nil {
		t.Fatalf("AIBOM was created for non-opted-in namespace: %+v", aibom)
	}
}

func TestIntegration_NonInferenceDeployment_NoAIBOM(t *testing.T) {
	env := startEnvTest(t)
	ctx := context.Background()

	nsName := "prod-other"
	depName := "nginx"

	mustCreateOptedInNamespace(t, env.k8sClient, ctx, nsName)
	// A plain nginx Deployment has no inference signal.
	mustCreate(t, env.k8sClient, ctx, &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: depName, Namespace: nsName},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": depName}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": depName}},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "nginx", Image: "nginx:1.27"},
					},
				},
			},
		},
	})
	time.Sleep(500 * time.Millisecond)
	aibomKey := types.NamespacedName{Name: "apps-deployment-" + depName, Namespace: nsName}
	var aibom aibomv1alpha1.AIBOM
	err := env.k8sClient.Get(ctx, aibomKey, &aibom)
	if err == nil {
		t.Fatalf("AIBOM was created for non-inference workload: %+v", aibom)
	}
}

// ---------- helpers ----------

func mustCreateOptedInNamespace(t *testing.T, c client.Client, ctx context.Context, name string) {
	t.Helper()
	mustCreate(t, c, ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: map[string]string{OptInLabel: "true"},
		},
	})
}

func mustCreate(t *testing.T, c client.Client, ctx context.Context, obj client.Object) {
	t.Helper()
	if err := c.Create(ctx, obj); err != nil {
		t.Fatalf("create %s/%s: %v", obj.GetNamespace(), obj.GetName(), err)
	}
}

func vllmDeployment(namespace, name string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, UID: "test-uid-123"},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
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
}
