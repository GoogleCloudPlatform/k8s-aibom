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

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	aibomv1alpha1 "github.com/GoogleCloudPlatform/k8s-aibom/api/v1alpha1"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/bom"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/scraper"
)

// StatefulSetReconciler watches apps/v1.StatefulSet objects in
// opted-in namespaces. The reconciler shape mirrors DeploymentReconciler:
// embed WorkloadReconciler, do kind-specific Get + pod listing + Workload
// preparation, delegate to reconcileWorkload.
//
// StatefulSet's pod template shape is identical to Deployment's; the
// extraction logic in InferenceSpecScraper.scrapePodSpec applies
// unchanged. The only StatefulSet-specific concerns at this layer are
// the typed Get and the pod selector (which has the same form as
// Deployment but lives on a different parent type).
type StatefulSetReconciler struct {
	WorkloadReconciler
}

// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch

func (r *StatefulSetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctx = log.IntoContext(ctx, log.FromContext(ctx).WithValues("statefulset", req.NamespacedName))

	var ss appsv1.StatefulSet
	if err := r.Get(ctx, req.NamespacedName, &ss); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	pods, err := r.listOwnedPods(ctx, &ss)
	if err != nil {
		return ctrl.Result{}, err
	}

	workload := scraper.Workload{
		Kind:      scraper.WorkloadKind{Group: "apps", Version: "v1", Kind: "StatefulSet"},
		Category:  scraper.CategoryInference,
		Namespace: ss.Namespace,
		Name:      ss.Name,
		UID:       ss.UID,
		Object:    &ss,
		Pods:      pods,
	}
	return r.reconcileWorkload(ctx, WorkloadReconcileRequest{
		Workload:  workload,
		AIBOMName: AIBOMNameForWorkload("apps", "StatefulSet", ss.Name),
		SetOwnerReference: func(a *aibomv1alpha1.AIBOM) error {
			return controllerutil.SetControllerReference(&ss, a, r.Scheme)
		},
		BOMBuildOptions: bom.BuildOptions{
			WorkloadKind:      "StatefulSet",
			WorkloadGroup:     "apps",
			WorkloadAPIVer:    "v1",
			WorkloadNamespace: ss.Namespace,
			WorkloadName:      ss.Name,
			WorkloadUID:       string(ss.UID),
			WorkloadCategory:  string(scraper.CategoryInference),
			ControllerName:    r.ControllerName,
			ControllerVersion: r.ControllerVersion,
		},
		SummaryOptions: SummaryOptions{
			WorkloadKind:       "StatefulSet",
			WorkloadAPIVersion: "apps/v1",
			WorkloadName:       ss.Name,
			WorkloadNamespace:  ss.Namespace,
			WorkloadCategory:   string(scraper.CategoryInference),
		},
		Generation: ss.Generation,
	})
}

// listOwnedPods returns pods in the StatefulSet's namespace matching
// the StatefulSet's selector. Same pattern as
// DeploymentReconciler.listOwnedPods.
func (r *StatefulSetReconciler) listOwnedPods(ctx context.Context, ss *appsv1.StatefulSet) ([]corev1.Pod, error) {
	if ss.Spec.Selector == nil {
		return nil, nil
	}
	if len(ss.Spec.Selector.MatchLabels) == 0 {
		return nil, nil
	}
	var pods corev1.PodList
	if err := r.List(ctx, &pods,
		client.InNamespace(ss.Namespace),
		client.MatchingLabels(ss.Spec.Selector.MatchLabels),
	); err != nil {
		return nil, fmt.Errorf("list pods: %w", err)
	}
	return pods.Items, nil
}

// SetupWithManager registers this reconciler with the controller-runtime
// manager.
func (r *StatefulSetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&appsv1.StatefulSet{}).
		Owns(&aibomv1alpha1.AIBOM{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Complete(r)
}
