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
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	aibomv1beta1 "github.com/GoogleCloudPlatform/k8s-aibom/api/v1beta1"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/bom"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/scraper"
)

// nimServiceGVK is the GroupVersionKind this reconciler watches. Pinned
// to v1alpha1, the NIM Operator's only served version, per
// docs/external-crd-versions.md.
var nimServiceGVK = schema.GroupVersionKind{
	Group:   "apps.nvidia.com",
	Version: "v1alpha1",
	Kind:    "NIMService",
}

// NIMServiceReconciler watches NVIDIA NIM Operator NIMService CRs in
// opted-in namespaces and produces one AIBOM per service via
// NIMServiceScraper (Design 003 §1). Same shape as the KServe and Dynamo
// reconcilers: unstructured watch with an explicit GVK, no operator Go
// module, no pod listing (the operator materializes a Deployment or
// LeaderWorkerSet; the Design 003 §3 ownership roll-up follows that
// chain). Workload.Pods is an empty, non-nil slice.
type NIMServiceReconciler struct {
	WorkloadReconciler
}

// +kubebuilder:rbac:groups=apps.nvidia.com,resources=nimservices,verbs=get;list;watch

func (r *NIMServiceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctx = log.IntoContext(ctx, log.FromContext(ctx).WithValues("nimservice", req.NamespacedName))

	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(nimServiceGVK)
	if err := r.Get(ctx, req.NamespacedName, u); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	workload := scraper.Workload{
		Kind:      scraper.WorkloadKind{Group: "apps.nvidia.com", Version: "v1alpha1", Kind: "NIMService"},
		Category:  scraper.CategoryInference,
		Namespace: u.GetNamespace(),
		Name:      u.GetName(),
		UID:       u.GetUID(),
		Object:    u,
		Pods:      []corev1.Pod{},
	}
	return r.reconcileWorkload(ctx, WorkloadReconcileRequest{
		Workload:  workload,
		AIBOMName: AIBOMNameForWorkload("apps.nvidia.com", "NIMService", u.GetName()),
		SetOwnerReference: func(a *aibomv1beta1.AIBOM) error {
			return controllerutil.SetControllerReference(u, a, r.Scheme)
		},
		BOMBuildOptions: bom.BuildOptions{
			WorkloadKind:      "NIMService",
			WorkloadGroup:     "apps.nvidia.com",
			WorkloadAPIVer:    "v1alpha1",
			WorkloadNamespace: u.GetNamespace(),
			WorkloadName:      u.GetName(),
			WorkloadUID:       string(u.GetUID()),
			WorkloadCategory:  string(scraper.CategoryInference),
			ControllerName:    r.ControllerName,
			ControllerVersion: r.ControllerVersion,
		},
		SummaryOptions: SummaryOptions{
			WorkloadKind:       "NIMService",
			WorkloadAPIVersion: "apps.nvidia.com/v1alpha1",
			WorkloadName:       u.GetName(),
			WorkloadNamespace:  u.GetNamespace(),
			WorkloadCategory:   string(scraper.CategoryInference),
		},
		Generation: u.GetGeneration(),
	})
}

// Watch describes this kind for the WatchSupervisor (Design 004): the
// kind runs on its own cache, isolated from the manager's, so a CRD
// that is present but unservable degrades only this kind.
func (r *NIMServiceReconciler) Watch() ThirdPartyWatch {
	return ThirdPartyWatch{
		Name:       "NIMService",
		GVK:        nimServiceGVK,
		Reconciler: r,
		Base:       &r.WorkloadReconciler,
	}
}
