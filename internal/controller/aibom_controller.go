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
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
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

// DefaultExternalSinkTimeout bounds each external Sink.Emit call so a
// slow or hung sink cannot stall reconciliation. The deadline is per-
// sink, not per-reconcile, so two sinks in parallel each get the full
// budget. A failure here is recorded as a SinkFailed condition; the
// next reconcile cycle retries.
const DefaultExternalSinkTimeout = 30 * time.Second

// OptInLabel is the namespace label that opts into AIBOM generation.
// Matches PRD FR1.3.
const OptInLabel = "aibom.k8saibom.dev/enabled"

// DeploymentReconciler watches apps/v1.Deployment objects in opted-in
// namespaces, classifies them as inference workloads, and creates or
// updates a v1alpha1.AIBOM CR with the resulting BOM in its status.
//
// Reconciliation flow (kind-neutral logic in
// internal/controller/workload_reconciler.go reconcileWorkload):
//
//  1. Fetch the Deployment. If not found, return without error
//     (owner-reference garbage collection handles AIBOM cleanup).
//  2. Look up the Deployment's Namespace. If the OptInLabel is not
//     "true", skip — the namespace is not opted in.
//  3. List the Pods owned by the Deployment so the scraper can resolve
//     digests from pod.status.containerStatuses[].imageID (per A1).
//  4. Run the configured Scraper. If BOMInputs.Confidence is
//     Unresolved, skip — the workload has no inference signal and is
//     not classified as inference.
//  5. Compute input hash; dedup against existing AIBOM.Status.InputHash.
//  6. Run the BOM Builder to produce a CycloneDX 1.6 ML-BOM.
//  7. Fan out to external sinks; build the AIBOMStatus.
//  8. CreateOrUpdate the AIBOM CR with an owner reference to the
//     Deployment, then Status().Update() to persist the status
//     subresource.
type DeploymentReconciler struct {
	WorkloadReconciler
}

// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=replicasets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
// +kubebuilder:rbac:groups=aibom.k8saibom.dev,resources=aiboms,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=aibom.k8saibom.dev,resources=aiboms/status,verbs=get;update;patch

func (r *DeploymentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctx = log.IntoContext(ctx, log.FromContext(ctx).WithValues("deployment", req.NamespacedName))

	var dep appsv1.Deployment
	if err := r.Get(ctx, req.NamespacedName, &dep); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Gather pods owned by this Deployment (best-effort; the scraper
	// tolerates an empty pod list by marking digests Unresolved).
	pods, err := r.listOwnedPods(ctx, &dep)
	if err != nil {
		return ctrl.Result{}, err
	}

	workload := scraper.Workload{
		Kind:      scraper.WorkloadKind{Group: "apps", Version: "v1", Kind: "Deployment"},
		Category:  scraper.CategoryInference,
		Namespace: dep.Namespace,
		Name:      dep.Name,
		UID:       dep.UID,
		Object:    &dep,
		Pods:      pods,
	}
	return r.reconcileWorkload(ctx, WorkloadReconcileRequest{
		Workload:  workload,
		AIBOMName: AIBOMNameForDeployment(&dep),
		SetOwnerReference: func(a *aibomv1alpha1.AIBOM) error {
			return controllerutil.SetControllerReference(&dep, a, r.Scheme)
		},
		BOMBuildOptions: bom.BuildOptions{
			WorkloadKind:      "Deployment",
			WorkloadGroup:     "apps",
			WorkloadAPIVer:    "v1",
			WorkloadNamespace: dep.Namespace,
			WorkloadName:      dep.Name,
			WorkloadUID:       string(dep.UID),
			WorkloadCategory:  string(scraper.CategoryInference),
			ControllerName:    r.ControllerName,
			ControllerVersion: r.ControllerVersion,
		},
		SummaryOptions: SummaryOptions{
			WorkloadKind:       "Deployment",
			WorkloadAPIVersion: "apps/v1",
			WorkloadName:       dep.Name,
			WorkloadNamespace:  dep.Namespace,
			WorkloadCategory:   string(scraper.CategoryInference),
		},
		Generation: dep.Generation,
	})
}

// listOwnedPods returns pods in the Deployment's namespace that match
// the Deployment's selector. The match is selector-based, not strictly
// owner-reference-based, because the Deployment owns Pods transitively
// via ReplicaSet — but selector match is what the BOM cares about
// (running pods with the right container names).
func (r *DeploymentReconciler) listOwnedPods(ctx context.Context, dep *appsv1.Deployment) ([]corev1.Pod, error) {
	if dep.Spec.Selector == nil {
		return nil, nil
	}
	if len(dep.Spec.Selector.MatchLabels) == 0 {
		return nil, nil
	}
	var pods corev1.PodList
	if err := r.List(ctx, &pods,
		client.InNamespace(dep.Namespace),
		client.MatchingLabels(dep.Spec.Selector.MatchLabels),
	); err != nil {
		return nil, fmt.Errorf("list pods: %w", err)
	}
	return pods.Items, nil
}

// AIBOMNameForDeployment returns the canonical AIBOM resource name for a
// given Deployment. Convenience wrapper around AIBOMNameForWorkload.
//
// This wrapper exists for backward compatibility with existing test code
// that takes a typed *appsv1.Deployment. New per-kind reconcilers (and
// any new tests) MUST call AIBOMNameForWorkload(kind, name) directly
// rather than adding AIBOMNameForStatefulSet, AIBOMNameForDaemonSet,
// etc. — proliferating typed wrappers defeats the generic helper's
// purpose. This function should be considered frozen.
func AIBOMNameForDeployment(dep *appsv1.Deployment) string {
	return AIBOMNameForWorkload("apps", "Deployment", dep.Name)
}

// SetupWithManager registers this reconciler with the controller-runtime
// manager. The watch is on apps/v1.Deployment with no predicate filter
// at this level — the opt-in label check happens inside Reconcile so
// label changes on namespaces are picked up by re-reconciling already-
// known Deployments on the next event.
func (r *DeploymentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	listFactory := func() client.ObjectList { return &appsv1.DeploymentList{} }
	extractItems := func(l client.ObjectList) []client.Object {
		dl := l.(*appsv1.DeploymentList)
		var res []client.Object
		for i := range dl.Items {
			res = append(res, &dl.Items[i])
		}
		return res
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&appsv1.Deployment{}).
		Owns(&aibomv1alpha1.AIBOM{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(
			&corev1.Namespace{},
			handler.EnqueueRequestsFromMapFunc(r.EnqueueWorkloadsForNamespace(listFactory, extractItems)),
			builder.WithPredicates(r.NamespaceWatchPredicate()),
		).
		Watches(
			&corev1.Pod{},
			handler.EnqueueRequestsFromMapFunc(r.EnqueueWorkloadForPod("Deployment")),
			builder.WithPredicates(PodImageIDChangedPredicate()),
		).
		Complete(r)
}
