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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	aibomv1beta1 "github.com/GoogleCloudPlatform/k8s-aibom/api/v1beta1"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/metrics"
)

// AIBOMInfoGCReconciler forgets the identity series (Design 011) of an
// AIBOM that disappeared. The workload reconcilers forget the series when
// they delete an AIBOM themselves, but an AIBOM garbage-collected with
// its workload is never seen by them: the workload's reconcile returns
// on NotFound before any hook. This reconciler watches AIBOM delete
// events only and does one thing.
type AIBOMInfoGCReconciler struct {
	client.Client
}

// Reconcile forgets the series when the AIBOM no longer exists. An AIBOM
// that still exists (a delete event raced a recreate) is left alone; the
// workload reconcile that recreated it has already republished.
func (r *AIBOMInfoGCReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var a aibomv1beta1.AIBOM
	if err := r.Get(ctx, req.NamespacedName, &a); err != nil {
		if apierrors.IsNotFound(err) {
			metrics.ForgetWorkloadInfo(req.NamespacedName.String())
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// SetupWithManager registers the reconciler for AIBOM delete events only.
func (r *AIBOMInfoGCReconciler) SetupWithManager(mgr ctrl.Manager) error {
	deletesOnly := predicate.Funcs{
		CreateFunc:  func(event.CreateEvent) bool { return false },
		UpdateFunc:  func(event.UpdateEvent) bool { return false },
		GenericFunc: func(event.GenericEvent) bool { return false },
		DeleteFunc:  func(event.DeleteEvent) bool { return true },
	}
	return ctrl.NewControllerManagedBy(mgr).
		Named("aibom-info-gc").
		For(&aibomv1beta1.AIBOM{}, builder.WithPredicates(deletesOnly)).
		Complete(r)
}
