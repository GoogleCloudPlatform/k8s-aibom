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

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	aibomv1beta1 "github.com/GoogleCloudPlatform/k8s-aibom/api/v1beta1"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/metrics"
)

// Design 011: the identity series exist while the AIBOM exists, follow
// the pod set, carry the served-model alias, and disappear when the
// AIBOM is deleted behind the workload reconcilers' back (owner GC).
func TestIntegration_WorkloadInfoSeries_Lifecycle(t *testing.T) {
	env := startEnvTest(t)
	ctx := context.Background()
	ns := "info-series"
	mustCreateOptedInNamespace(t, env.k8sClient, ctx, ns)

	dep := expVllmDeployment(ns, "serve")
	dep.Spec.Template.Spec.Containers[0].Args = append(dep.Spec.Template.Spec.Containers[0].Args, "--served-model-name", "qwen")
	mustCreate(t, env.k8sClient, ctx, dep)
	key := types.NamespacedName{Name: AIBOMNameForWorkload("apps", "Deployment", "serve"), Namespace: ns}
	waitAIBOM(t, env, ctx, key, "vLLM Deployment")

	var a aibomv1beta1.AIBOM
	if err := env.k8sClient.Get(ctx, key, &a); err != nil {
		t.Fatal(err)
	}
	if a.Status.Summary == nil || len(a.Status.Summary.Models) == 0 {
		t.Fatalf("summary has no models: %+v", a.Status.Summary)
	}
	if got := a.Status.Summary.Models[0].ServedName; got != "qwen" {
		t.Fatalf("summary servedName = %q, want qwen", got)
	}

	workloadLabels := prometheus.Labels{
		"namespace": ns, "workload_kind": "Deployment", "workload_name": "serve", "category": "inference",
		"runtime": a.Status.Summary.Runtime.Name, "runtime_version": a.Status.Summary.Runtime.Version,
		"model": a.Status.Summary.Models[0].Identity, "model_confidence": a.Status.Summary.Models[0].Confidence, "served_model": "qwen",
	}
	eventually(t, 10*time.Second, 200*time.Millisecond, func() error {
		if v := testutil.ToFloat64(metrics.WorkloadInfo.With(workloadLabels)); v != 1 {
			return fmt.Errorf("aibom_workload_info = %v, want 1", v)
		}
		return nil
	})

	// No pods exist in envtest (no scheduler, no kubelet) until we make
	// one: a pod selected by the Deployment appears in aibom_pod_info
	// after the next reconcile, and leaves when deleted.
	// envtest runs no ReplicaSet controller, so build the ownership chain
	// the Deployment reconciler requires (Pod -> ReplicaSet -> Deployment)
	// by hand; selector match alone is deliberately not enough.
	if err := env.k8sClient.Get(ctx, types.NamespacedName{Name: dep.Name, Namespace: ns}, dep); err != nil {
		t.Fatal(err)
	}
	rs := &appsv1.ReplicaSet{}
	rs.Name, rs.Namespace = "serve-abc12", ns
	rs.Labels = dep.Spec.Selector.MatchLabels
	rs.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(dep, appsv1.SchemeGroupVersion.WithKind("Deployment"))}
	rs.Spec.Selector = dep.Spec.Selector
	rs.Spec.Template = dep.Spec.Template
	mustCreate(t, env.k8sClient, ctx, rs)
	pod := &corev1.Pod{}
	pod.Name, pod.Namespace = "serve-abc12-x7k2p", ns
	pod.Labels = dep.Spec.Selector.MatchLabels
	pod.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(rs, appsv1.SchemeGroupVersion.WithKind("ReplicaSet"))}
	pod.Spec = dep.Spec.Template.Spec
	mustCreate(t, env.k8sClient, ctx, pod)
	podLabels := prometheus.Labels{
		"namespace": ns, "pod": pod.Name, "workload_kind": "Deployment", "workload_name": "serve",
		"runtime": a.Status.Summary.Runtime.Name, "model": a.Status.Summary.Models[0].Identity, "served_model": "qwen",
	}
	eventually(t, 15*time.Second, 250*time.Millisecond, func() error {
		if v := testutil.ToFloat64(metrics.PodInfo.With(podLabels)); v != 1 {
			return fmt.Errorf("aibom_pod_info for %s = %v, want 1", pod.Name, v)
		}
		return nil
	})
	if err := env.k8sClient.Delete(ctx, pod); err != nil {
		t.Fatal(err)
	}
	eventually(t, 15*time.Second, 250*time.Millisecond, func() error {
		if metrics.PodInfo.Delete(podLabels) {
			return fmt.Errorf("aibom_pod_info for deleted pod %s still published", pod.Name)
		}
		return nil
	})

	// Remove the workload, then the AIBOM directly: in a real cluster
	// owner garbage collection does the second step and the workload
	// reconcilers never see it (their reconcile returns on NotFound). The
	// delete-only reconciler is what forgets the series. The metrics are
	// process-global, so assert on this workload's own series only.
	if err := env.k8sClient.Delete(ctx, dep); err != nil {
		t.Fatal(err)
	}
	eventually(t, 15*time.Second, 250*time.Millisecond, func() error {
		if aibomExists(env.k8sClient, ctx, key) {
			var cur aibomv1beta1.AIBOM
			if err := env.k8sClient.Get(ctx, key, &cur); err == nil {
				_ = env.k8sClient.Delete(ctx, &cur) // stand in for owner GC, absent in envtest
			}
			return fmt.Errorf("AIBOM still present")
		}
		if metrics.WorkloadInfo.Delete(workloadLabels) {
			return fmt.Errorf("aibom_workload_info still published after the AIBOM was deleted")
		}
		return nil
	})
}
