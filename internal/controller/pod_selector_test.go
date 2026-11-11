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
	"sort"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// A workload selector may use matchExpressions instead of, or as well
// as, matchLabels. listOwnedPods used to list pods by matchLabels only
// and returned no pods when matchLabels was empty, so a workload
// selecting its pods by expression never had its image digests
// resolved.
func TestListOwnedPods_SelectorExpressions(t *testing.T) {
	const ns = "ai-team"
	ctx := context.Background()
	workloadUID := types.UID("workload-uid")
	rsUID := types.UID("rs-uid")

	exprOnly := &metav1.LabelSelector{
		MatchExpressions: []metav1.LabelSelectorRequirement{{
			Key: "app", Operator: metav1.LabelSelectorOpIn, Values: []string{"vllm"},
		}},
	}
	// matchLabels and matchExpressions together: both must hold.
	mixed := &metav1.LabelSelector{
		MatchLabels: map[string]string{"app": "vllm"},
		MatchExpressions: []metav1.LabelSelectorRequirement{{
			Key: "track", Operator: metav1.LabelSelectorOpNotIn, Values: []string{"canary"},
		}},
	}

	pod := func(name string, labels map[string]string, owner types.UID) *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: ns, Labels: labels,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "apps/v1", Kind: "Owner", Name: "owner", UID: owner, Controller: boolPtr(true),
			}},
		}}
	}
	// own and canary carry the workload as controller owner (or its
	// ReplicaSet, for a Deployment); other matches no selector.
	pods := func(owner types.UID) []client.Object {
		return []client.Object{
			pod("own", map[string]string{"app": "vllm"}, owner),
			pod("canary", map[string]string{"app": "vllm", "track": "canary"}, owner),
			pod("other", map[string]string{"app": "tgi"}, owner),
		}
	}
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{
		Name: "vllm-rs", Namespace: ns, UID: rsUID,
		OwnerReferences: []metav1.OwnerReference{controllerRef("Deployment", "vllm", workloadUID)},
	}}
	meta := metav1.ObjectMeta{Name: "vllm", Namespace: ns, UID: workloadUID}

	type lister func(c client.Client, sel *metav1.LabelSelector) ([]corev1.Pod, error)
	kinds := []struct {
		kind  string
		owner types.UID
		extra []client.Object
		list  lister
	}{
		{"Deployment", rsUID, []client.Object{rs}, func(c client.Client, sel *metav1.LabelSelector) ([]corev1.Pod, error) {
			r := &DeploymentReconciler{WorkloadReconciler{Client: c}}
			return r.listOwnedPods(ctx, &appsv1.Deployment{ObjectMeta: meta, Spec: appsv1.DeploymentSpec{Selector: sel}})
		}},
		{"StatefulSet", workloadUID, nil, func(c client.Client, sel *metav1.LabelSelector) ([]corev1.Pod, error) {
			r := &StatefulSetReconciler{WorkloadReconciler{Client: c}}
			return r.listOwnedPods(ctx, &appsv1.StatefulSet{ObjectMeta: meta, Spec: appsv1.StatefulSetSpec{Selector: sel}})
		}},
		{"DaemonSet", workloadUID, nil, func(c client.Client, sel *metav1.LabelSelector) ([]corev1.Pod, error) {
			r := &DaemonSetReconciler{WorkloadReconciler{Client: c}}
			return r.listOwnedPods(ctx, &appsv1.DaemonSet{ObjectMeta: meta, Spec: appsv1.DaemonSetSpec{Selector: sel}})
		}},
		{"Job", workloadUID, nil, func(c client.Client, sel *metav1.LabelSelector) ([]corev1.Pod, error) {
			r := &JobReconciler{WorkloadReconciler{Client: c}}
			return r.listOwnedPods(ctx, &batchv1.Job{ObjectMeta: meta, Spec: batchv1.JobSpec{Selector: sel}})
		}},
	}
	cases := []struct {
		name string
		sel  *metav1.LabelSelector
		want []string
	}{
		{"matchExpressions only", exprOnly, []string{"canary", "own"}},
		{"matchLabels and matchExpressions", mixed, []string{"own"}},
		{"matchLabels only", &metav1.LabelSelector{MatchLabels: map[string]string{"app": "vllm"}}, []string{"canary", "own"}},
		{"empty selector", &metav1.LabelSelector{}, nil},
		{"nil selector", nil, nil},
	}
	for _, k := range kinds {
		for _, tc := range cases {
			t.Run(k.kind+"/"+tc.name, func(t *testing.T) {
				c := fake.NewClientBuilder().
					WithScheme(newConfigFakeScheme(t)).
					WithObjects(append(pods(k.owner), k.extra...)...).
					Build()
				got, err := k.list(c, tc.sel)
				if err != nil {
					t.Fatalf("listOwnedPods: %v", err)
				}
				names := make([]string, 0, len(got))
				for i := range got {
					names = append(names, got[i].Name)
				}
				sort.Strings(names)
				if len(names) != len(tc.want) {
					t.Fatalf("pods = %v, want %v", names, tc.want)
				}
				for i := range names {
					if names[i] != tc.want[i] {
						t.Fatalf("pods = %v, want %v", names, tc.want)
					}
				}
			})
		}
	}
}
