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

// disaggregatedSetGVK is the GroupVersionKind this reconciler watches.
// Pinned to v1, the only served version, per docs/external-crd-versions.md.
var disaggregatedSetGVK = schema.GroupVersionKind{
	Group:   "disaggregatedset.x-k8s.io",
	Version: "v1",
	Kind:    "DisaggregatedSet",
}

// DisaggregatedSetReconciler watches kubernetes-sigs/lws DisaggregatedSet
// CRs (llm-d's prefill/decode primitive) in opted-in namespaces and
// produces one AIBOM per set via DisaggregatedSetScraper (Design 007
// §1). Same shape as the LeaderWorkerSet reconciler: unstructured watch
// with an explicit GVK under the WatchSupervisor, no lws Go module. The
// LeaderWorkerSets a set materializes (one per role), their
// StatefulSets and pods roll up to this AIBOM under Design 005, which
// is also how the set's document gets pod-status digests.
type DisaggregatedSetReconciler struct {
	WorkloadReconciler
}

// +kubebuilder:rbac:groups=disaggregatedset.x-k8s.io,resources=disaggregatedsets,verbs=get;list;watch

func (r *DisaggregatedSetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctx = log.IntoContext(ctx, log.FromContext(ctx).WithValues("disaggregatedset", req.NamespacedName))

	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(disaggregatedSetGVK)
	if err := r.Get(ctx, req.NamespacedName, u); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Design 005: the workloads and pods this CR owns, transitively.
	owned, pods := r.rootDescendants(ctx, u)
	if pods == nil {
		pods = []corev1.Pod{}
	}

	workload := scraper.Workload{
		Kind:      scraper.WorkloadKind{Group: "disaggregatedset.x-k8s.io", Version: "v1", Kind: "DisaggregatedSet"},
		Category:  scraper.CategoryInference,
		Namespace: u.GetNamespace(),
		Name:      u.GetName(),
		UID:       u.GetUID(),
		Object:    u,
		Pods:      pods,
	}
	return r.reconcileWorkload(ctx, WorkloadReconcileRequest{
		Workload:  workload,
		AIBOMName: AIBOMNameForWorkload("disaggregatedset.x-k8s.io", "DisaggregatedSet", u.GetName()),
		SetOwnerReference: func(a *aibomv1beta1.AIBOM) error {
			return controllerutil.SetControllerReference(u, a, r.Scheme)
		},
		BOMBuildOptions: bom.BuildOptions{
			WorkloadKind:      "DisaggregatedSet",
			WorkloadGroup:     "disaggregatedset.x-k8s.io",
			WorkloadAPIVer:    "v1",
			WorkloadNamespace: u.GetNamespace(),
			WorkloadName:      u.GetName(),
			WorkloadUID:       string(u.GetUID()),
			WorkloadCategory:  string(scraper.CategoryInference),
			ControllerName:    r.ControllerName,
			ControllerVersion: r.ControllerVersion,
		},
		SummaryOptions: SummaryOptions{
			WorkloadKind:       "DisaggregatedSet",
			WorkloadAPIVersion: "disaggregatedset.x-k8s.io/v1",
			WorkloadName:       u.GetName(),
			WorkloadNamespace:  u.GetNamespace(),
			WorkloadCategory:   string(scraper.CategoryInference),
		},
		Generation: u.GetGeneration(),
		Owned:      owned,
	})
}

// Watch describes this kind for the WatchSupervisor (Design 004): the
// kind runs on its own cache, isolated from the manager's, so a CRD
// that is present but unservable degrades only this kind.
func (r *DisaggregatedSetReconciler) Watch() ThirdPartyWatch {
	return ThirdPartyWatch{
		Name:       "DisaggregatedSet",
		GVK:        disaggregatedSetGVK,
		Reconciler: r,
		Base:       &r.WorkloadReconciler,
	}
}
