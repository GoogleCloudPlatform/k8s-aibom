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
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func ownerRefTo(apiVersion, kind, name string, uid types.UID) metav1.OwnerReference {
	return metav1.OwnerReference{APIVersion: apiVersion, Kind: kind, Name: name, UID: uid, Controller: ptr.To(true)}
}

func unstructuredObj(apiVersion, kind, ns, name string, uid types.UID, owners ...metav1.OwnerReference) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion(apiVersion)
	u.SetKind(kind)
	u.SetNamespace(ns)
	u.SetName(name)
	u.SetUID(uid)
	u.SetOwnerReferences(owners)
	return u
}

func rollupTracked() *TrackedKinds {
	t := NewTrackedKinds()
	for _, gk := range []schema.GroupKind{
		{Group: "apps", Kind: "Deployment"}, {Group: "apps", Kind: "StatefulSet"},
		{Group: "batch", Kind: "Job"}, {Group: "batch", Kind: "CronJob"},
		{Group: "nvidia.com", Kind: "DynamoGraphDeployment"}, {Group: "nvidia.com", Kind: "DynamoComponentDeployment"},
		{Group: "leaderworkerset.x-k8s.io", Kind: "LeaderWorkerSet"},
	} {
		t.Add(gk)
	}
	return t
}

func rollupScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	return s
}

func TestResolveTrackedOwner(t *testing.T) {
	ctx := context.Background()
	ns := "ns"
	dgd := unstructuredObj("nvidia.com/v1beta1", "DynamoGraphDeployment", ns, "graph", "uid-dgd")
	dcd := unstructuredObj("nvidia.com/v1beta1", "DynamoComponentDeployment", ns, "worker", "uid-dcd",
		ownerRefTo("nvidia.com/v1beta1", "DynamoGraphDeployment", "graph", "uid-dgd"))
	// An untracked intermediate (Grove) between a Deployment and the graph.
	clique := unstructuredObj("grove.io/v1alpha1", "PodClique", ns, "clique", "uid-clique",
		ownerRefTo("nvidia.com/v1beta1", "DynamoComponentDeployment", "worker", "uid-dcd"))
	c := fake.NewClientBuilder().WithScheme(rollupScheme()).WithObjects(dgd, dcd, clique).Build()

	dep := func(name string, owners ...metav1.OwnerReference) *appsv1.Deployment {
		return &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, UID: types.UID("uid-" + name), OwnerReferences: owners}}
	}

	cases := []struct {
		name        string
		obj         client.Object
		tracked     *TrackedKinds
		wantOutcome ownerOutcome
		wantOwner   string
	}{
		{"root deployment: no request, none", dep("plain"), rollupTracked(), ownerNone, ""},
		{"nil tracked set disables suppression", dep("x", ownerRefTo("nvidia.com/v1beta1", "DynamoComponentDeployment", "worker", "uid-dcd")), nil, ownerNone, ""},
		{"direct tracked owner (hop 1)", dep("w", ownerRefTo("nvidia.com/v1beta1", "DynamoComponentDeployment", "worker", "uid-dcd")), rollupTracked(), ownerTracked, "DynamoComponentDeployment/worker"},
		{"tracked owner through an untracked intermediate (hop 2)", dep("g", ownerRefTo("grove.io/v1alpha1", "PodClique", "clique", "uid-clique")), rollupTracked(), ownerTracked, "DynamoComponentDeployment/worker"},
		{"owner kind not readable (no such object) → unresolved", dep("u", ownerRefTo("foo.example.com/v1", "Widget", "w", "uid-w")), rollupTracked(), ownerUnresolved, ""},
		{"dangling uid → unresolved", dep("d", ownerRefTo("grove.io/v1alpha1", "PodClique", "clique", "uid-other")), rollupTracked(), ownerUnresolved, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			owner, outcome, _ := resolveTrackedOwner(ctx, c, tc.obj, tc.tracked)
			if outcome != tc.wantOutcome {
				t.Fatalf("outcome = %s, want %s", outcome, tc.wantOutcome)
			}
			got := ""
			if owner != nil {
				got = owner.String()
			}
			if got != tc.wantOwner {
				t.Errorf("owner = %q, want %q", got, tc.wantOwner)
			}
		})
	}

	// DCD itself: owned by the tracked graph → tracked at hop 1.
	owner, outcome, _ := resolveTrackedOwner(ctx, c, dcd, rollupTracked())
	if outcome != ownerTracked || owner.String() != "DynamoGraphDeployment/graph" {
		t.Errorf("dcd owner = %v/%s", owner, outcome)
	}
}

// The closure walks typed and unstructured intermediates alike and
// reports tracked descendants sorted, with the pods reachable through
// the chain and none from outside it.
func TestRootDescendants_ClosureAndPods(t *testing.T) {
	ctx := context.Background()
	ns := "ns"
	dgd := unstructuredObj("nvidia.com/v1beta1", "DynamoGraphDeployment", ns, "graph", "uid-dgd")
	dcd := unstructuredObj("nvidia.com/v1beta1", "DynamoComponentDeployment", ns, "worker", "uid-dcd",
		ownerRefTo("nvidia.com/v1beta1", "DynamoGraphDeployment", "graph", "uid-dgd"))
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "worker-dep", Namespace: ns, UID: "uid-dep",
		OwnerReferences: []metav1.OwnerReference{ownerRefTo("nvidia.com/v1beta1", "DynamoComponentDeployment", "worker", "uid-dcd")}}}
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "worker-rs", Namespace: ns, UID: "uid-rs",
		OwnerReferences: []metav1.OwnerReference{ownerRefTo("apps/v1", "Deployment", "worker-dep", "uid-dep")}}}
	other := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "unrelated", Namespace: ns, UID: "uid-other"}}
	pod := func(name string, ownerUID types.UID) *corev1.Pod {
		p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, UID: types.UID("uid-" + name)}}
		if ownerUID != "" {
			p.OwnerReferences = []metav1.OwnerReference{ownerRefTo("apps/v1", "ReplicaSet", "x", ownerUID)}
		}
		return p
	}
	c := fake.NewClientBuilder().WithScheme(rollupScheme()).
		WithObjects(dgd, dcd, dep, rs, other, pod("mine", "uid-rs"), pod("theirs", "uid-other"), pod("orphan", "")).Build()
	r := &WorkloadReconciler{Client: c, Tracked: rollupTracked()}

	owned, pods := r.rootDescendants(ctx, dgd)
	if len(owned) != 2 || owned[0].String() != "Deployment/worker-dep" || owned[1].String() != "DynamoComponentDeployment/worker" {
		t.Fatalf("owned = %+v, want [Deployment/worker-dep DynamoComponentDeployment/worker] (sorted by kind, name)", owned)
	}
	if len(pods) != 1 || pods[0].Name != "mine" {
		t.Fatalf("pods = %v, want only the pod reachable through RS → Deployment → DCD → DGD", podNames(pods))
	}
	// A root with no tracked descendants yields no properties; pods it
	// owns directly are still its own (the StatefulSet/DaemonSet shape).
	owned, pods = r.rootDescendants(ctx, other)
	if len(owned) != 0 || ownedStrings(owned) != nil {
		t.Errorf("unrelated root must absorb nothing: owned=%v", owned)
	}
	if len(pods) != 1 || pods[0].Name != "theirs" {
		t.Errorf("directly owned pods belong to the root: %v", podNames(pods))
	}
}

func podNames(pods []corev1.Pod) []string {
	out := make([]string, 0, len(pods))
	for _, p := range pods {
		out = append(out, p.Name)
	}
	return out
}

func TestEnqueueRootForPod(t *testing.T) {
	ctx := context.Background()
	ns := "ns"
	dcd := unstructuredObj("nvidia.com/v1beta1", "DynamoComponentDeployment", ns, "worker", "uid-dcd",
		ownerRefTo("nvidia.com/v1beta1", "DynamoGraphDeployment", "graph", "uid-dgd"))
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "worker-dep", Namespace: ns, UID: "uid-dep",
		OwnerReferences: []metav1.OwnerReference{ownerRefTo("nvidia.com/v1beta1", "DynamoComponentDeployment", "worker", "uid-dcd")}}}
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "worker-rs", Namespace: ns, UID: "uid-rs",
		OwnerReferences: []metav1.OwnerReference{ownerRefTo("apps/v1", "Deployment", "worker-dep", "uid-dep")}}}
	c := fake.NewClientBuilder().WithScheme(rollupScheme()).WithObjects(dcd, dep, rs).Build()
	r := &WorkloadReconciler{Client: c}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: ns,
		OwnerReferences: []metav1.OwnerReference{ownerRefTo("apps/v1", "ReplicaSet", "worker-rs", "uid-rs")}}}

	reqs := r.EnqueueRootForPod(schema.GroupKind{Group: "nvidia.com", Kind: "DynamoGraphDeployment"})(ctx, pod)
	if len(reqs) != 1 || reqs[0].Name != "graph" || reqs[0].Namespace != ns {
		t.Errorf("DGD requests = %v, want [ns/graph]", reqs)
	}
	if reqs := r.EnqueueRootForPod(schema.GroupKind{Group: "leaderworkerset.x-k8s.io", Kind: "LeaderWorkerSet"})(ctx, pod); len(reqs) != 0 {
		t.Errorf("a chain that never reaches the kind must map to nothing, got %v", reqs)
	}
	if reqs := r.EnqueueRootForPod(schema.GroupKind{Group: "nvidia.com", Kind: "DynamoGraphDeployment"})(ctx, &corev1.Pod{}); len(reqs) != 0 {
		t.Errorf("ownerless pod must map to nothing, got %v", reqs)
	}
}
