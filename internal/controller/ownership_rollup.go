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
	"sort"
	"sync"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/GoogleCloudPlatform/k8s-aibom/internal/config"
)

// Design 005: ownership roll-up. One workload, one AIBOM. A tracked
// workload owned — directly or transitively via controller
// ownerReferences — by another tracked kind is not separately reported;
// the owner's document is the report, records what it absorbed
// (aibom.rollup.owned.<i>) and receives the descendants' pods for
// digest resolution. Ownership only: labels are never consulted (see
// pod_ownership.go for why).

// rollupMaxDepth bounds the upward ownerReference walk. Real chains are
// at most four hops (Pod → ReplicaSet → Deployment →
// DynamoComponentDeployment → DynamoGraphDeployment); six leaves room
// without letting a pathological graph turn into a request storm.
const rollupMaxDepth = 6

// TrackedKinds is the set of workload kinds this process reports on:
// the apps/v1 and batch kinds plus every third-party kind handed to the
// WatchSupervisor at startup. Membership is fixed at startup and does
// not follow watch health, so suppression stays stable through a
// Design 004 outage (the owner's existing AIBOM is kept as last known;
// un-suppressing its children would create duplicates that flip back).
//
// Whether a tracked kind may *absorb* its children does follow the
// spec.discovery.workloadKinds allowlist (Design 006 §4, via Owns): a
// kind the operator switched off produces no document, so the workloads
// it used to absorb are reported on their own again rather than
// vanishing with it. Has (structural membership) is what the
// descendant walk uses, so a root's document still lists and reads
// pods from every tracked workload under it regardless of the list.
type TrackedKinds struct {
	mu    sync.RWMutex
	kinds map[schema.GroupKind]struct{}
	store *config.Store
}

// NewTrackedKinds returns an empty set.
func NewTrackedKinds() *TrackedKinds {
	return &TrackedKinds{kinds: map[schema.GroupKind]struct{}{}}
}

// Add records a kind as tracked.
func (t *TrackedKinds) Add(gk schema.GroupKind) {
	t.mu.Lock()
	t.kinds[gk] = struct{}{}
	t.mu.Unlock()
}

// Has reports whether gk is tracked. A nil receiver tracks nothing,
// which disables suppression (the pre-Design-005 behavior).
func (t *TrackedKinds) Has(gk schema.GroupKind) bool {
	if t == nil {
		return false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	_, ok := t.kinds[gk]
	return ok
}

// Follow makes Owns consult store's live allowlist. Set once at
// startup; a nil store means every tracked kind owns.
func (t *TrackedKinds) Follow(store *config.Store) {
	t.mu.Lock()
	t.store = store
	t.mu.Unlock()
}

// Owns reports whether a workload owned by gk is rolled up into gk's
// document: gk is tracked and currently allowed by
// spec.discovery.workloadKinds.
func (t *TrackedKinds) Owns(gk schema.GroupKind) bool {
	if !t.Has(gk) {
		return false
	}
	t.mu.RLock()
	store := t.store
	t.mu.RUnlock()
	if store == nil {
		return true
	}
	return store.Load().WorkloadKinds.Allows(gk)
}

// OwnedWorkload identifies a tracked workload absorbed into an owner's
// document.
type OwnedWorkload struct {
	Kind string
	Name string
	UID  types.UID
}

// String renders Kind/name, the aibom.rollup.owned property value.
func (o OwnedWorkload) String() string { return o.Kind + "/" + o.Name }

// ownerOutcome is the result of resolveTrackedOwner.
type ownerOutcome string

const (
	// ownerNone: the workload is a root (no controller owner, or a
	// chain that ends at an untracked root). Report as today.
	ownerNone ownerOutcome = "none"
	// ownerTracked: a tracked kind owns this workload. Suppress.
	ownerTracked ownerOutcome = "tracked"
	// ownerUnresolved: the walk could not finish (NotFound, Forbidden,
	// NoMatch, depth). Report as today; never fewer AIBOMs because of a
	// missing permission.
	ownerUnresolved ownerOutcome = "unresolved"
)

// resolveTrackedOwner walks controller ownerReferences upward from obj
// and reports whether a tracked kind owns it. Each hop is one live Get
// of the owner as unstructured, using the reference's own
// apiVersion/kind/name, so no typed dependency on any intermediate
// kind is needed, and every hop — the final tracked owner included —
// is identity-checked by UID. Objects with no controller owner — the
// overwhelming majority of Deployments — return immediately with no
// request.
func resolveTrackedOwner(ctx context.Context, c client.Client, obj client.Object, tracked *TrackedKinds) (*OwnedWorkload, ownerOutcome, error) {
	if tracked == nil {
		return nil, ownerNone, nil
	}
	ns := obj.GetNamespace()
	cur := obj
	for depth := 0; depth < rollupMaxDepth; depth++ {
		ref := metav1.GetControllerOf(cur)
		if ref == nil {
			return nil, ownerNone, nil
		}
		gv, err := schema.ParseGroupVersion(ref.APIVersion)
		if err != nil {
			return nil, ownerUnresolved, fmt.Errorf("owner %s/%s apiVersion %q: %w", ref.Kind, ref.Name, ref.APIVersion, err)
		}
		// Every hop is fetched and identity-checked, including the final
		// tracked owner: a reference's name alone would let a lingering
		// child attach to an owner re-created under the same name.
		next := &unstructured.Unstructured{}
		next.SetGroupVersionKind(gv.WithKind(ref.Kind))
		if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: ref.Name}, next); err != nil {
			if apierrors.IsNotFound(err) || apierrors.IsForbidden(err) || meta.IsNoMatchError(err) {
				return nil, ownerUnresolved, fmt.Errorf("owner %s/%s unreadable: %w", ref.Kind, ref.Name, err)
			}
			return nil, ownerUnresolved, fmt.Errorf("get owner %s/%s: %w", ref.Kind, ref.Name, err)
		}
		if next.GetUID() != ref.UID {
			// A re-created object with the same name: the reference is
			// stale. Not this owner; report the child as a root until the
			// controller that owns it re-parents it.
			return nil, ownerUnresolved, fmt.Errorf("owner %s/%s uid %s does not match reference %s", ref.Kind, ref.Name, next.GetUID(), ref.UID)
		}
		if tracked.Owns(schema.GroupKind{Group: gv.Group, Kind: ref.Kind}) {
			return &OwnedWorkload{Kind: ref.Kind, Name: ref.Name, UID: ref.UID}, ownerTracked, nil
		}
		cur = next
	}
	return nil, ownerUnresolved, fmt.Errorf("owner chain deeper than %d", rollupMaxDepth)
}

// +kubebuilder:rbac:groups=grove.io,resources=podcliquesets;podcliques;podcliquescalinggroups,verbs=get;list

// intermediateKinds are the CRD kinds that sit between a root and its
// pods and are neither tracked nor watched: read (list) only, when
// present, to complete the descendant closure. Absent CRD or missing
// permission degrades to "pods and children behind it are not seen",
// which is today's behavior.
var intermediateKinds = []schema.GroupVersionKind{
	{Group: "nvidia.com", Version: "v1beta1", Kind: "DynamoComponentDeployment"},
	{Group: "grove.io", Version: "v1alpha1", Kind: "PodCliqueSet"},
	{Group: "grove.io", Version: "v1alpha1", Kind: "PodCliqueScalingGroup"},
	{Group: "grove.io", Version: "v1alpha1", Kind: "PodClique"},
}

// ownedNode is one object in the namespace ownership graph.
type ownedNode struct {
	kind  string
	group string
	name  string
	owner types.UID // controller owner UID, "" for roots
}

// rootDescendants resolves everything a root workload transitively owns
// in its namespace: the tracked workloads it absorbs (sorted, for the
// document) and the pods reachable through the chain (for digest
// resolution). It never fails the reconcile: list errors on optional
// kinds are logged once per kind and skipped.
func (r *WorkloadReconciler) rootDescendants(ctx context.Context, root client.Object) ([]OwnedWorkload, []corev1.Pod) {
	logger := log.FromContext(ctx)
	ns := root.GetNamespace()
	nodes := map[types.UID]ownedNode{}
	add := func(uid types.UID, kind, group, name string, refs []metav1.OwnerReference) {
		n := ownedNode{kind: kind, group: group, name: name}
		for _, or := range refs {
			if or.Controller != nil && *or.Controller {
				n.owner = or.UID
				break
			}
		}
		nodes[uid] = n
	}

	var deps appsv1.DeploymentList
	if err := r.List(ctx, &deps, client.InNamespace(ns)); err == nil {
		for i := range deps.Items {
			add(deps.Items[i].UID, "Deployment", "apps", deps.Items[i].Name, deps.Items[i].OwnerReferences)
		}
	} else {
		logger.V(1).Info("rollup: list deployments failed", "err", err.Error())
	}
	var rss appsv1.ReplicaSetList
	if err := r.List(ctx, &rss, client.InNamespace(ns)); err == nil {
		for i := range rss.Items {
			add(rss.Items[i].UID, "ReplicaSet", "apps", rss.Items[i].Name, rss.Items[i].OwnerReferences)
		}
	} else {
		logger.V(1).Info("rollup: list replicasets failed", "err", err.Error())
	}
	var sss appsv1.StatefulSetList
	if err := r.List(ctx, &sss, client.InNamespace(ns)); err == nil {
		for i := range sss.Items {
			add(sss.Items[i].UID, "StatefulSet", "apps", sss.Items[i].Name, sss.Items[i].OwnerReferences)
		}
	} else {
		logger.V(1).Info("rollup: list statefulsets failed", "err", err.Error())
	}
	var jobs batchv1.JobList
	if err := r.List(ctx, &jobs, client.InNamespace(ns)); err == nil {
		for i := range jobs.Items {
			add(jobs.Items[i].UID, "Job", "batch", jobs.Items[i].Name, jobs.Items[i].OwnerReferences)
		}
	} else {
		logger.V(1).Info("rollup: list jobs failed", "err", err.Error())
	}
	// LeaderWorkerSets can be intermediates too (NIMService → LWS →
	// StatefulSet, DGD → DCD → LWS); list them when the kind is tracked.
	lwsGVK := schema.GroupVersionKind{Group: "leaderworkerset.x-k8s.io", Version: "v1", Kind: "LeaderWorkerSet"}
	for _, gvk := range append([]schema.GroupVersionKind{lwsGVK}, intermediateKinds...) {
		if gvk == lwsGVK && !r.Tracked.Has(gvk.GroupKind()) {
			continue
		}
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(schema.GroupVersionKind{Group: gvk.Group, Version: gvk.Version, Kind: gvk.Kind + "List"})
		if err := r.List(ctx, list, client.InNamespace(ns)); err != nil {
			if !meta.IsNoMatchError(err) && !apierrors.IsForbidden(err) {
				logger.V(1).Info("rollup: list intermediate kind failed", "kind", gvk.Kind, "err", err.Error())
			}
			continue
		}
		for i := range list.Items {
			it := &list.Items[i]
			add(it.GetUID(), gvk.Kind, gvk.Group, it.GetName(), it.GetOwnerReferences())
		}
	}

	// Closure under controller ownership from the root.
	descendants := map[types.UID]struct{}{}
	changed := true
	for changed {
		changed = false
		for uid, n := range nodes {
			if _, seen := descendants[uid]; seen {
				continue
			}
			if n.owner == root.GetUID() {
				descendants[uid] = struct{}{}
				changed = true
				continue
			}
			if _, ok := descendants[n.owner]; ok {
				descendants[uid] = struct{}{}
				changed = true
			}
		}
	}

	var owned []OwnedWorkload
	for uid := range descendants {
		n := nodes[uid]
		if r.Tracked.Has(schema.GroupKind{Group: n.group, Kind: n.kind}) {
			owned = append(owned, OwnedWorkload{Kind: n.kind, Name: n.name, UID: uid})
		}
	}
	sort.Slice(owned, func(i, j int) bool {
		if owned[i].Kind != owned[j].Kind {
			return owned[i].Kind < owned[j].Kind
		}
		return owned[i].Name < owned[j].Name
	})

	// Pods whose controller owner is the root or any descendant.
	owners := map[types.UID]struct{}{root.GetUID(): {}}
	for uid := range descendants {
		owners[uid] = struct{}{}
	}
	var pods corev1.PodList
	if err := r.List(ctx, &pods, client.InNamespace(ns)); err != nil {
		logger.V(1).Info("rollup: list pods failed", "err", err.Error())
		return owned, nil
	}
	return owned, filterPodsOwnedBy(pods.Items, owners)
}

// ownedStrings renders OwnedWorkloads for BuildOptions.
func ownedStrings(owned []OwnedWorkload) []string {
	if len(owned) == 0 {
		return nil
	}
	out := make([]string, 0, len(owned))
	for _, o := range owned {
		out = append(out, o.String())
	}
	return out
}

// EnqueueRootForPod maps a Pod event to the root of kind gk that owns
// the pod through its controller chain (Pod → ReplicaSet → Deployment →
// DynamoComponentDeployment → DynamoGraphDeployment, for instance), so
// a root's document follows digest changes in pods it owns only
// transitively. Each hop is one live Get; the caller pairs it with
// PodImageIDChangedPredicate so only creation, deletion and digest
// changes pay it. Pods whose chain does not reach gk map to nothing.
func (r *WorkloadReconciler) EnqueueRootForPod(gk schema.GroupKind) handler.MapFunc {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		pod, ok := obj.(*corev1.Pod)
		if !ok {
			return nil
		}
		var cur client.Object = pod
		for depth := 0; depth < rollupMaxDepth; depth++ {
			ref := metav1.GetControllerOf(cur)
			if ref == nil {
				return nil
			}
			gv, err := schema.ParseGroupVersion(ref.APIVersion)
			if err != nil {
				return nil
			}
			if gv.Group == gk.Group && ref.Kind == gk.Kind {
				return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: pod.Namespace, Name: ref.Name}}}
			}
			next := &unstructured.Unstructured{}
			next.SetGroupVersionKind(gv.WithKind(ref.Kind))
			if err := r.Get(ctx, types.NamespacedName{Namespace: pod.Namespace, Name: ref.Name}, next); err != nil {
				return nil
			}
			cur = next
		}
		return nil
	}
}
