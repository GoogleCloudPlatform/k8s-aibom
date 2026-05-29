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
	"errors"
	"fmt"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	aibomv1alpha1 "github.com/GoogleCloudPlatform/k8s-aibom/api/v1alpha1"
)

// TestIntegration_StatefulSetInOptedInNamespace_ProducesAIBOM is the
// StatefulSet equivalent of the Deployment-path test in
// aibom_controller_test.go. A vLLM-shaped StatefulSet in an opted-in
// namespace produces an AIBOM CR with summary, runtime detection,
// model components, and an owner reference back to the StatefulSet.
func TestIntegration_StatefulSetInOptedInNamespace_ProducesAIBOM(t *testing.T) {
	env := startEnvTest(t)
	ctx := context.Background()

	nsName := "prod-stateful"
	ssName := "vllm-stateful"

	mustCreateOptedInNamespace(t, env.k8sClient, ctx, nsName)
	mustCreate(t, env.k8sClient, ctx, vllmStatefulSet(nsName, ssName))

	aibomKey := types.NamespacedName{
		Name:      AIBOMNameForWorkload("apps", "StatefulSet", ssName),
		Namespace: nsName,
	}
	var got aibomv1alpha1.AIBOM
	eventually(t, 30*time.Second, 200*time.Millisecond, func() error {
		if err := env.k8sClient.Get(ctx, aibomKey, &got); err != nil {
			return fmt.Errorf("get AIBOM %s: %w", aibomKey, err)
		}
		if got.Status.Summary == nil {
			return errors.New("status.summary not yet populated")
		}
		return nil
	})

	// Spec
	if got.Spec.WorkloadRef.Kind != "StatefulSet" {
		t.Errorf("Spec.WorkloadRef.Kind = %q, want %q", got.Spec.WorkloadRef.Kind, "StatefulSet")
	}
	if got.Spec.WorkloadRef.Name != ssName {
		t.Errorf("Spec.WorkloadRef.Name = %q, want %q", got.Spec.WorkloadRef.Name, ssName)
	}
	if got.Spec.WorkloadRef.APIVersion != "apps/v1" {
		t.Errorf("Spec.WorkloadRef.APIVersion = %q, want %q", got.Spec.WorkloadRef.APIVersion, "apps/v1")
	}

	// Status summary
	if got.Status.Summary.Workload.Kind != "StatefulSet" {
		t.Errorf("Summary.Workload.Kind = %q, want %q",
			got.Status.Summary.Workload.Kind, "StatefulSet")
	}
	if got.Status.Summary.Runtime == nil || got.Status.Summary.Runtime.Name != "vllm" {
		t.Errorf("Summary.Runtime = %+v, want name=vllm", got.Status.Summary.Runtime)
	}
	if len(got.Status.Summary.Models) == 0 {
		t.Error("expected at least one model in summary")
	}

	// Owner reference is the StatefulSet.
	if len(got.OwnerReferences) != 1 {
		t.Fatalf("expected 1 owner reference, got %d", len(got.OwnerReferences))
	}
	owner := got.OwnerReferences[0]
	if owner.Kind != "StatefulSet" || owner.Name != ssName {
		t.Errorf("owner reference = %+v, want StatefulSet/%s", owner, ssName)
	}
}

func vllmStatefulSet(namespace, name string) *appsv1.StatefulSet {
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, UID: "test-uid-123"},
		Spec: appsv1.StatefulSetSpec{
			ServiceName: name,
			Selector:    &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
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
