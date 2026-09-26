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
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Pod attribution is ownership-based, not selector-based. A digest may
// only enter a workload's BOM from a pod positively tied to that
// workload through its controller ownerReference chain: two workloads
// in one namespace with overlapping selectors and a shared container
// name must never contaminate each other's audit records, whether by
// accident (canary and blue/green pairs, charts with loose labels) or
// by intent (a tenant planting a chosen digest in another workload's
// record). Pods with no controller owner are excluded for the same
// reason — fewer resolved digests is degradation; a wrong digest in an
// audit record is corruption.

// controllerOwnerUID returns the UID of the pod's controller owner
// reference, if it has one.
func controllerOwnerUID(pod *corev1.Pod) (types.UID, bool) {
	for _, or := range pod.OwnerReferences {
		if or.Controller != nil && *or.Controller {
			return or.UID, true
		}
	}
	return "", false
}

// filterPodsOwnedBy returns the subset of pods whose controller
// ownerReference UID is present in owners.
func filterPodsOwnedBy(pods []corev1.Pod, owners map[types.UID]struct{}) []corev1.Pod {
	if len(owners) == 0 {
		return nil
	}
	var out []corev1.Pod
	for i := range pods {
		uid, ok := controllerOwnerUID(&pods[i])
		if !ok {
			continue
		}
		if _, ok := owners[uid]; ok {
			out = append(out, pods[i])
		}
	}
	return out
}

// singleOwner is a convenience constructor for the direct-ownership
// kinds (StatefulSet, DaemonSet, Job), whose pods carry the workload
// itself as their controller owner.
func singleOwner(uid types.UID) map[types.UID]struct{} {
	return map[types.UID]struct{}{uid: {}}
}

// replicaSetOwnerUIDs returns the UIDs of ReplicaSets in the namespace
// that are controller-owned by the given Deployment. Deployments own
// pods transitively (Deployment → ReplicaSet → Pod), so the pod filter
// for a Deployment is membership in this set. The replicasets
// get/list/watch RBAC this requires is already part of the
// controller's role.
func replicaSetOwnerUIDs(ctx context.Context, c client.Client, dep *appsv1.Deployment) (map[types.UID]struct{}, error) {
	var rsList appsv1.ReplicaSetList
	if err := c.List(ctx, &rsList, client.InNamespace(dep.Namespace)); err != nil {
		return nil, fmt.Errorf("list replicasets: %w", err)
	}
	owners := make(map[types.UID]struct{})
	for i := range rsList.Items {
		for _, or := range rsList.Items[i].OwnerReferences {
			if or.Controller != nil && *or.Controller && or.UID == dep.UID {
				owners[rsList.Items[i].UID] = struct{}{}
				break
			}
		}
	}
	return owners, nil
}
