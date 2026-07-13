// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package controller

import (
	"context"

	batchv1 "k8s.io/api/batch/v1"
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

type JobReconciler struct {
	WorkloadReconciler
}

// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch

func (r *JobReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctx = log.IntoContext(ctx, log.FromContext(ctx).WithValues("job", req.NamespacedName))

	var job batchv1.Job
	if err := r.Get(ctx, req.NamespacedName, &job); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	pods, err := r.listOwnedPods(ctx, &job)
	if err != nil {
		return ctrl.Result{}, err
	}

	workload := scraper.Workload{
		Kind:      scraper.WorkloadKind{Group: "batch", Version: "v1", Kind: "Job"},
		Category:  scraper.CategoryInference, // Overridden by scraper
		Namespace: job.Namespace,
		Name:      job.Name,
		UID:       job.UID,
		Object:    &job,
		Pods:      append(pods, corev1.Pod{Spec: job.Spec.Template.Spec}),
	}

	return r.reconcileWorkload(ctx, WorkloadReconcileRequest{
		Workload:  workload,
		AIBOMName: AIBOMNameForWorkload("batch", "Job", job.Name),
		SetOwnerReference: func(a *aibomv1alpha1.AIBOM) error {
			return controllerutil.SetControllerReference(&job, a, r.Scheme)
		},
		BOMBuildOptions: bom.BuildOptions{
			WorkloadKind:      "Job",
			WorkloadGroup:     "batch",
			WorkloadAPIVer:    "v1",
			WorkloadNamespace: job.Namespace,
			WorkloadName:      job.Name,
			WorkloadUID:       string(job.UID),
			WorkloadCategory:  string(scraper.CategoryInference),
			ControllerName:    r.ControllerName,
			ControllerVersion: r.ControllerVersion,
		},
		SummaryOptions: SummaryOptions{
			WorkloadKind:       "Job",
			WorkloadAPIVersion: "batch/v1",
			WorkloadName:       job.Name,
			WorkloadNamespace:  job.Namespace,
			WorkloadCategory:   string(scraper.CategoryInference),
		},
		Generation: job.Generation,
	})
}

func (r *JobReconciler) listOwnedPods(ctx context.Context, job *batchv1.Job) ([]corev1.Pod, error) {
	if job.Spec.Selector == nil {
		return nil, nil
	}
	if len(job.Spec.Selector.MatchLabels) == 0 {
		return nil, nil
	}
	var pods corev1.PodList
	if err := r.List(ctx, &pods,
		client.InNamespace(job.Namespace),
		client.MatchingLabels(job.Spec.Selector.MatchLabels),
	); err != nil {
		return nil, err
	}
	return pods.Items, nil
}

func (r *JobReconciler) SetupWithManager(mgr ctrl.Manager) error {
	listFactory := func() client.ObjectList { return &batchv1.JobList{} }
	extractItems := func(l client.ObjectList) []client.Object {
		jl := l.(*batchv1.JobList)
		var res []client.Object
		for i := range jl.Items {
			res = append(res, &jl.Items[i])
		}
		return res
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&batchv1.Job{}).
		Owns(&aibomv1alpha1.AIBOM{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(
			&corev1.Namespace{},
			handler.EnqueueRequestsFromMapFunc(r.EnqueueWorkloadsForNamespace(listFactory, extractItems)),
			builder.WithPredicates(r.NamespaceWatchPredicate()),
		).
		Watches(
			&corev1.Pod{},
			handler.EnqueueRequestsFromMapFunc(r.EnqueueWorkloadForPod("Job")),
			builder.WithPredicates(PodImageIDChangedPredicate()),
		).
		Complete(r)
}
