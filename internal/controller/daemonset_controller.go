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

// DaemonSetReconciler watches apps/v1.DaemonSet objects in opted-in
// namespaces. DaemonSets are a legitimate inference deployment shape
// for node-local serving (per-node GPU workers, edge inference, sidecar
// patterns) and the marginal cost of supporting them is small —
// extraction shares scrapePodSpec with the Deployment + StatefulSet paths.
//
// The reconciler shape mirrors DeploymentReconciler and
// StatefulSetReconciler: embed WorkloadReconciler, do kind-specific
// Get + pod listing + Workload preparation, delegate.
type DaemonSetReconciler struct {
	WorkloadReconciler
}

// +kubebuilder:rbac:groups=apps,resources=daemonsets,verbs=get;list;watch

func (r *DaemonSetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctx = log.IntoContext(ctx, log.FromContext(ctx).WithValues("daemonset", req.NamespacedName))

	var ds appsv1.DaemonSet
	if err := r.Get(ctx, req.NamespacedName, &ds); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	pods, err := r.listOwnedPods(ctx, &ds)
	if err != nil {
		return ctrl.Result{}, err
	}

	workload := scraper.Workload{
		Kind:      scraper.WorkloadKind{Group: "apps", Version: "v1", Kind: "DaemonSet"},
		Category:  scraper.CategoryInference,
		Namespace: ds.Namespace,
		Name:      ds.Name,
		UID:       ds.UID,
		Object:    &ds,
		Pods:      pods,
	}
	return r.reconcileWorkload(ctx, WorkloadReconcileRequest{
		Workload:  workload,
		AIBOMName: AIBOMNameForWorkload("apps", "DaemonSet", ds.Name),
		SetOwnerReference: func(a *aibomv1alpha1.AIBOM) error {
			return controllerutil.SetControllerReference(&ds, a, r.Scheme)
		},
		BOMBuildOptions: bom.BuildOptions{
			WorkloadKind:      "DaemonSet",
			WorkloadGroup:     "apps",
			WorkloadAPIVer:    "v1",
			WorkloadNamespace: ds.Namespace,
			WorkloadName:      ds.Name,
			WorkloadUID:       string(ds.UID),
			WorkloadCategory:  string(scraper.CategoryInference),
			ControllerName:    r.ControllerName,
			ControllerVersion: r.ControllerVersion,
		},
		SummaryOptions: SummaryOptions{
			WorkloadKind:       "DaemonSet",
			WorkloadAPIVersion: "apps/v1",
			WorkloadName:       ds.Name,
			WorkloadNamespace:  ds.Namespace,
			WorkloadCategory:   string(scraper.CategoryInference),
		},
		Generation: ds.Generation,
	})
}

func (r *DaemonSetReconciler) listOwnedPods(ctx context.Context, ds *appsv1.DaemonSet) ([]corev1.Pod, error) {
	if ds.Spec.Selector == nil {
		return nil, nil
	}
	if len(ds.Spec.Selector.MatchLabels) == 0 {
		return nil, nil
	}
	var pods corev1.PodList
	if err := r.List(ctx, &pods,
		client.InNamespace(ds.Namespace),
		client.MatchingLabels(ds.Spec.Selector.MatchLabels),
	); err != nil {
		return nil, fmt.Errorf("list pods: %w", err)
	}
	return pods.Items, nil
}

// SetupWithManager registers this reconciler with the controller-runtime
// manager.
func (r *DaemonSetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&appsv1.DaemonSet{}).
		Owns(&aibomv1alpha1.AIBOM{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Complete(r)
}
