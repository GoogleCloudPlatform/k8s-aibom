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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	aibomv1alpha1 "github.com/GoogleCloudPlatform/k8s-aibom/api/v1alpha1"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/bom"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/scraper"
)

// kserveInferenceServiceGVK is the GroupVersionKind this reconciler
// watches. Pinned to v1beta1 per docs/external-crd-versions.md.
var kserveInferenceServiceGVK = schema.GroupVersionKind{
	Group:   "serving.kserve.io",
	Version: "v1beta1",
	Kind:    "InferenceService",
}

// KServeInferenceServiceReconciler watches KServe InferenceService CRs
// in opted-in namespaces and produces AIBOM CRs from the declared
// spec.predictor.* fields via KServeInferenceServiceScraper.
//
// The reconciler differs from the apps/v1 reconcilers in two ways:
//
//   - **Unstructured object handling.** KServe's typed Go module is
//     not a dependency; the reconciler fetches and watches
//     *unstructured.Unstructured with an explicit GVK. The scraper
//     accepts this directly (it uses unstructured.NestedString to
//     reach the four documented field paths).
//
//   - **No pod listing.** KServe materializes pods indirectly via a
//     KServe-managed Deployment, not directly via the InferenceService
//     CR. v1 does NOT follow this chain. Workload.Pods is set to an
//     empty (non-nil) slice for defensive correctness — downstream
//     code that range-loops over Pods sees a stable empty result.
type KServeInferenceServiceReconciler struct {
	WorkloadReconciler
}

// +kubebuilder:rbac:groups=serving.kserve.io,resources=inferenceservices,verbs=get;list;watch

func (r *KServeInferenceServiceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctx = log.IntoContext(ctx, log.FromContext(ctx).WithValues("inferenceservice", req.NamespacedName))

	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(kserveInferenceServiceGVK)
	if err := r.Get(ctx, req.NamespacedName, u); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	workload := scraper.Workload{
		Kind:      scraper.WorkloadKind{Group: "serving.kserve.io", Version: "v1beta1", Kind: "InferenceService"},
		Category:  scraper.CategoryInference,
		Namespace: u.GetNamespace(),
		Name:      u.GetName(),
		UID:       u.GetUID(),
		Object:    u,
		// Empty (non-nil) slice. KServe pods are not listed at the
		// InferenceService level; downstream code should see a stable
		// zero-length slice rather than nil to avoid surprise NPE if
		// future code calls a method on the slice that doesn't handle
		// nil gracefully.
		Pods: []corev1.Pod{},
	}
	return r.reconcileWorkload(ctx, WorkloadReconcileRequest{
		Workload:  workload,
		AIBOMName: AIBOMNameForWorkload("serving.kserve.io", "InferenceService", u.GetName()),
		SetOwnerReference: func(a *aibomv1alpha1.AIBOM) error {
			return controllerutil.SetControllerReference(u, a, r.Scheme)
		},
		BOMBuildOptions: bom.BuildOptions{
			WorkloadKind:      "InferenceService",
			WorkloadGroup:     "serving.kserve.io",
			WorkloadAPIVer:    "v1beta1",
			WorkloadNamespace: u.GetNamespace(),
			WorkloadName:      u.GetName(),
			WorkloadUID:       string(u.GetUID()),
			WorkloadCategory:  string(scraper.CategoryInference),
			ControllerName:    r.ControllerName,
			ControllerVersion: r.ControllerVersion,
		},
		SummaryOptions: SummaryOptions{
			WorkloadKind:       "InferenceService",
			WorkloadAPIVersion: "serving.kserve.io/v1beta1",
			WorkloadName:       u.GetName(),
			WorkloadNamespace:  u.GetNamespace(),
			WorkloadCategory:   string(scraper.CategoryInference),
		},
		Generation: u.GetGeneration(),
	})
}

// SetupWithManager registers this reconciler with the controller-runtime
// manager. The watch is on *unstructured.Unstructured with the
// pinned GVK; no scheme registration of the KServe types is required.
func (r *KServeInferenceServiceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(kserveInferenceServiceGVK)
	return ctrl.NewControllerManagedBy(mgr).
		For(u).
		Owns(&aibomv1alpha1.AIBOM{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(
			&corev1.Namespace{},
			handler.EnqueueRequestsFromMapFunc(r.EnqueueWorkloadsForNamespace(
				func() client.ObjectList {
					list := &unstructured.UnstructuredList{}
					list.SetGroupVersionKind(schema.GroupVersionKind{
						Group:   "serving.kserve.io",
						Version: "v1beta1",
						Kind:    "InferenceServiceList",
					})
					return list
				},
				func(objList client.ObjectList) []client.Object {
					uList, ok := objList.(*unstructured.UnstructuredList)
					if !ok {
						return nil
					}
					var objs []client.Object
					for i := range uList.Items {
						objs = append(objs, &uList.Items[i])
					}
					return objs
				},
			)),
			builder.WithPredicates(NamespaceOptInChangedPredicate()),
		).
		Complete(r)
}
