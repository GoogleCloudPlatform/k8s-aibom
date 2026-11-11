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
	"encoding/json"
	"maps"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	toolscache "k8s.io/client-go/tools/cache"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	aibomv1beta1 "github.com/GoogleCloudPlatform/k8s-aibom/api/v1beta1"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/bom"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/config"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/scraper"
)

// Gateway API Inference Extension InferencePool (Design 007 §2, Part
// 2a). Only the released v1 API is supported; the older
// inference.networking.x-k8s.io group is deliberately not read.
const (
	InferencePoolCRD  = "inferencepools.inference.networking.k8s.io"
	inferencePoolName = "InferencePool"
)

var (
	InferencePoolGVK = schema.GroupVersionKind{Group: "inference.networking.k8s.io", Version: "v1", Kind: inferencePoolName}
	inferencePoolGVR = schema.GroupVersionResource{Group: "inference.networking.k8s.io", Version: "v1", Resource: "inferencepools"}
)

// InferencePoolDeferRequeue is how soon a reconcile that deferred its
// pool facts (view unsynced, previously published facts unreadable)
// retries. The view's own recovery re-enqueues everything sooner.
const InferencePoolDeferRequeue = 30 * time.Second

// +kubebuilder:rbac:groups=inference.networking.k8s.io,resources=inferencepools,verbs=get;list;watch

// PoolViewState is how far the InferencePool view can be trusted.
type PoolViewState int

const (
	// PoolViewInactive: no pool watch runs (CRD absent, not Ready,
	// removed, or this process is not the leader).
	PoolViewInactive PoolViewState = iota
	// PoolViewUnsynced: the watch runs but has not completed a list, or
	// its last list/watch attempt failed.
	PoolViewUnsynced
	// PoolViewUsable: a list completed and no list/watch failure since.
	PoolViewUsable
)

func (s PoolViewState) String() string {
	switch s {
	case PoolViewUsable:
		return "usable"
	case PoolViewUnsynced:
		return "unsynced"
	default:
		return "inactive"
	}
}

// poolEntry is the part of one InferencePool the view keeps.
type poolEntry struct {
	selector map[string]string
	picker   string
}

func (e poolEntry) equal(o poolEntry) bool {
	return e.picker == o.picker && maps.Equal(e.selector, o.selector)
}

// InferencePoolView is the read-only, in-memory view of InferencePools
// that workload reconciles match against (Design 007 §2).
//
// It is not a workload kind: it has no AIBOM, never enters
// TrackedKinds or the workloadKinds allowlist, and takes no part in
// ownership roll-up. Its list/watch is started and stopped by the
// WatchSupervisor's CRD discovery (#226) as an auxiliary candidate (see
// Watch), so CRD installation after startup, removal, re-creation and
// recovery follow the same decisions as every third-party kind.
//
// The view is also a leader-elected Runnable: it re-enqueues affected
// workloads through the allowlist fan-out, per namespace for a pool
// change, and for every namespace when the view becomes usable or
// stops (Design 007 §2, Discovery and freshness).
type InferencePoolView struct {
	Config      *rest.Config
	Fanout      *AllowlistFanout
	ConfigStore *config.Store

	// newClient builds the dynamic client; tests substitute one.
	newClient func(*rest.Config) (dynamic.Interface, error)

	mu      sync.Mutex
	running bool
	listed  bool
	failing bool
	pools   map[string]map[string]poolEntry // namespace -> name -> entry

	pend struct {
		sync.Mutex
		all bool
		ns  map[string]bool
	}
	kickOnce sync.Once
	kick     chan struct{}
}

var _ manager.LeaderElectionRunnable = (*InferencePoolView)(nil)

// NewInferencePoolView returns a view wired to the given fan-out.
func NewInferencePoolView(cfg *rest.Config, fanout *AllowlistFanout, store *config.Store) *InferencePoolView {
	return &InferencePoolView{Config: cfg, Fanout: fanout, ConfigStore: store}
}

// NeedLeaderElection: re-enqueues go to controllers that only run on
// the leader.
func (v *InferencePoolView) NeedLeaderElection() bool { return true }

// Watch returns the auxiliary discovery candidate that runs the pool
// list/watch while the CRD is Ready.
func (v *InferencePoolView) Watch() ThirdPartyWatch {
	return ThirdPartyWatch{Name: inferencePoolName, GVK: InferencePoolGVK, CRD: InferencePoolCRD, Run: v.run}
}

// Lookup returns the pools in ns whose selector matches at least one of
// templates, deduplicated and sorted by name, and the view state. Facts
// are returned only when the view is usable; otherwise nil.
//
// A pool matches a template when every key/value of its
// spec.selector.matchLabels is among that template's labels (AND within
// the selector); a pool matching any template matches (OR across
// templates); labels of different templates are never combined. A pool
// with an empty selector matches nothing.
func (v *InferencePoolView) Lookup(ns string, templates []map[string]string) ([]scraper.InferencePoolMembership, PoolViewState) {
	if v == nil {
		return nil, PoolViewInactive
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	st := v.stateLocked()
	if st != PoolViewUsable || len(templates) == 0 {
		return nil, st
	}
	byName := v.pools[ns]
	names := make([]string, 0, len(byName))
	for n := range byName {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []scraper.InferencePoolMembership
	for _, n := range names {
		e := byName[n]
		if selectorMatchesAny(e.selector, templates) {
			out = append(out, scraper.InferencePoolMembership{Pool: ns + "/" + n, EndpointPicker: e.picker})
		}
	}
	return out, st
}

// State reports the current view state.
func (v *InferencePoolView) State() PoolViewState {
	if v == nil {
		return PoolViewInactive
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.stateLocked()
}

func (v *InferencePoolView) stateLocked() PoolViewState {
	switch {
	case !v.running:
		return PoolViewInactive
	case !v.listed || v.failing:
		return PoolViewUnsynced
	default:
		return PoolViewUsable
	}
}

func selectorMatchesAny(sel map[string]string, templates []map[string]string) bool {
	if len(sel) == 0 {
		return false
	}
	for _, t := range templates {
		ok := true
		for k, want := range sel {
			if got, has := t[k]; !has || got != want {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// run is the supervisor-driven pool watch: it runs while the CRD is
// Ready and returns when ctx ends. recheck asks discovery to re-read
// the CRD (an absence-shaped list/watch error is never treated as
// removal on its own, #226 A1).
func (v *InferencePoolView) run(ctx context.Context, recheck func()) {
	log := ctrl.Log.WithName("inferencepool-view")
	newClient := v.newClient
	if newClient == nil {
		newClient = func(c *rest.Config) (dynamic.Interface, error) { return dynamic.NewForConfig(c) }
	}
	dc, err := newClient(v.Config)
	if err != nil {
		// Only a malformed rest.Config gets here; the view stays inactive.
		log.Error(err, "InferencePool client could not be built; pool membership is not recorded")
		<-ctx.Done()
		return
	}
	v.begin()
	defer v.end()
	log.Info("InferencePool watch started", "gvk", InferencePoolGVK.String())

	ri := dc.Resource(inferencePoolGVR).Namespace(metav1.NamespaceAll)
	lw := &toolscache.ListWatch{
		ListWithContextFunc: func(ctx context.Context, opts metav1.ListOptions) (runtime.Object, error) {
			obj, err := ri.List(ctx, opts)
			if err != nil && ctx.Err() == nil {
				v.failed(err, recheck)
			}
			return obj, err
		},
		WatchFuncWithContext: func(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
			w, err := ri.Watch(ctx, opts)
			if ctx.Err() == nil {
				if err != nil {
					v.failed(err, recheck)
				} else {
					v.watchEstablished()
				}
			}
			return w, err
		},
	}
	expected := &unstructured.Unstructured{}
	expected.SetGroupVersionKind(InferencePoolGVK)
	r := toolscache.NewReflectorWithOptions(toolscache.ToListWatcherWithWatchListSemantics(lw, dc), expected, poolStore{v},
		toolscache.ReflectorOptions{Name: "inferencepool-view", TypeDescription: InferencePoolGVK.String()})
	r.RunWithContext(ctx)
	log.Info("InferencePool watch stopped; previously published pool membership is kept until the view is readable again")
}

func (v *InferencePoolView) begin() {
	v.mu.Lock()
	v.running, v.listed, v.failing, v.pools = true, false, false, nil
	v.mu.Unlock()
}

// end leaves the view inactive. Nothing is removed from documents:
// reconciles keep the published facts while the view is unavailable.
func (v *InferencePoolView) end() {
	v.mu.Lock()
	v.running, v.listed, v.failing, v.pools = false, false, false, nil
	v.mu.Unlock()
	v.request("")
}

// failed records a list/watch failure: the view is unsynced until the
// next successful list (or watch, after one list succeeded).
func (v *InferencePoolView) failed(err error, recheck func()) {
	if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
		recheck()
	}
	v.mu.Lock()
	first := !v.failing
	v.failing = true
	v.mu.Unlock()
	if first {
		ctrl.Log.WithName("inferencepool-view").Info("InferencePool list/watch failed; pool membership is not updated until it recovers", "err", err.Error())
	}
}

// watchEstablished clears a failure once a watch resumes from a view
// that had completed a list: the watch continues from that list's
// resource version, so the view is consistent again.
func (v *InferencePoolView) watchEstablished() {
	v.mu.Lock()
	recovered := v.failing && v.listed
	if recovered {
		v.failing = false
	}
	v.mu.Unlock()
	if recovered {
		ctrl.Log.WithName("inferencepool-view").Info("InferencePool watch recovered")
		v.request("")
	}
}

// poolStore receives the reflector's events.
type poolStore struct{ v *InferencePoolView }

func poolOf(obj interface{}) (ns, name string, e poolEntry, ok bool) {
	if t, isTomb := obj.(toolscache.DeletedFinalStateUnknown); isTomb {
		obj = t.Obj
	}
	u, isU := obj.(*unstructured.Unstructured)
	if !isU {
		return "", "", poolEntry{}, false
	}
	sel, found, err := unstructured.NestedStringMap(u.Object, "spec", "selector", "matchLabels")
	if !found || err != nil {
		sel = nil // a malformed selector matches nothing
	}
	picker, _, _ := unstructured.NestedString(u.Object, "spec", "endpointPickerRef", "name")
	return u.GetNamespace(), u.GetName(), poolEntry{selector: sel, picker: picker}, true
}

func (s poolStore) Add(obj interface{}) error    { s.put(obj); return nil }
func (s poolStore) Update(obj interface{}) error { s.put(obj); return nil }

func (s poolStore) put(obj interface{}) {
	ns, name, e, ok := poolOf(obj)
	if !ok {
		return
	}
	v := s.v
	v.mu.Lock()
	if v.pools == nil {
		v.pools = map[string]map[string]poolEntry{}
	}
	old, had := v.pools[ns][name]
	if v.pools[ns] == nil {
		v.pools[ns] = map[string]poolEntry{}
	}
	v.pools[ns][name] = e
	notify := (!had || !old.equal(e)) && v.stateLocked() == PoolViewUsable
	v.mu.Unlock()
	if notify {
		v.request(ns)
	}
}

func (s poolStore) Delete(obj interface{}) error {
	ns, name, _, ok := poolOf(obj)
	if !ok {
		return nil
	}
	v := s.v
	v.mu.Lock()
	_, had := v.pools[ns][name]
	delete(v.pools[ns], name)
	if len(v.pools[ns]) == 0 {
		delete(v.pools, ns)
	}
	notify := had && v.stateLocked() == PoolViewUsable
	v.mu.Unlock()
	if notify {
		v.request(ns)
	}
	return nil
}

// Replace installs a complete list. The first one (and the first after
// a failure) makes the view usable and re-enqueues every namespace;
// a relist of an already usable view re-enqueues only namespaces whose
// pools changed.
func (s poolStore) Replace(list []interface{}, _ string) error {
	next := map[string]map[string]poolEntry{}
	for _, obj := range list {
		ns, name, e, ok := poolOf(obj)
		if !ok {
			continue
		}
		if next[ns] == nil {
			next[ns] = map[string]poolEntry{}
		}
		next[ns][name] = e
	}
	v := s.v
	v.mu.Lock()
	wasUsable := v.stateLocked() == PoolViewUsable
	var changed []string
	if wasUsable {
		changed = changedNamespaces(v.pools, next)
	}
	v.pools, v.listed, v.failing = next, true, false
	v.mu.Unlock()
	if !wasUsable {
		ctrl.Log.WithName("inferencepool-view").Info("InferencePool view synced", "pools", countPools(next))
		v.request("")
		return nil
	}
	for _, ns := range changed {
		v.request(ns)
	}
	return nil
}

func (s poolStore) Resync() error { return nil }

func changedNamespaces(prev, next map[string]map[string]poolEntry) []string {
	seen := map[string]bool{}
	var out []string
	check := func(ns string) {
		if seen[ns] {
			return
		}
		seen[ns] = true
		a, b := prev[ns], next[ns]
		if len(a) != len(b) {
			out = append(out, ns)
			return
		}
		for n, e := range a {
			if o, ok := b[n]; !ok || !o.equal(e) {
				out = append(out, ns)
				return
			}
		}
	}
	for ns := range prev {
		check(ns)
	}
	for ns := range next {
		check(ns)
	}
	sort.Strings(out)
	return out
}

func countPools(m map[string]map[string]poolEntry) int {
	n := 0
	for _, byName := range m {
		n += len(byName)
	}
	return n
}

// request queues a re-enqueue: of namespace ns, or of every namespace
// when ns is empty. Requests coalesce while a fan-out runs.
func (v *InferencePoolView) request(ns string) {
	v.pend.Lock()
	if ns == "" {
		v.pend.all = true
	} else {
		if v.pend.ns == nil {
			v.pend.ns = map[string]bool{}
		}
		v.pend.ns[ns] = true
	}
	v.pend.Unlock()
	select {
	case v.kickCh() <- struct{}{}:
	default:
	}
}

func (v *InferencePoolView) kickCh() chan struct{} {
	v.kickOnce.Do(func() { v.kick = make(chan struct{}, 1) })
	return v.kick
}

func (v *InferencePoolView) take() (all bool, nss []string) {
	v.pend.Lock()
	defer v.pend.Unlock()
	all = v.pend.all
	for ns := range v.pend.ns {
		nss = append(nss, ns)
	}
	v.pend.all, v.pend.ns = false, nil
	sort.Strings(nss)
	return all, nss
}

// Start runs the re-enqueue loop until ctx ends.
func (v *InferencePoolView) Start(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-v.kickCh():
		}
		all, nss := v.take()
		if v.Fanout == nil {
			continue
		}
		allowed := config.AllWorkloadKinds()
		if v.ConfigStore != nil {
			if snap := v.ConfigStore.Load(); snap != nil {
				allowed = snap.WorkloadKinds
			}
		}
		if all {
			v.Fanout.trigger(ctx, allowed, "", "re-enqueued workloads after an InferencePool view change")
			continue
		}
		for _, ns := range nss {
			if ctx.Err() != nil {
				return nil
			}
			v.Fanout.trigger(ctx, allowed, ns, "re-enqueued workloads after an InferencePool change")
		}
	}
}

// inferencePoolFacts returns the pool membership to publish for a
// workload, or deferred=true when it cannot be decided yet.
//
// Membership is computed only for inference workloads that reached
// this point (opted in, allowed, not rolled up, classified), from the
// Pod templates their scraper reads. With a usable view the facts are
// recomputed from it, which also removes stale ones. With an
// unavailable view no new facts are created and the previously
// published ones are kept: read back from the inline document, or known
// to be none when the stored input hash equals the hash without pool
// facts. When neither tells, a reconcile defers while the watch is
// starting or recovering; with no watch at all (CRD absent or not
// Ready) it proceeds without pool facts.
func (r *WorkloadReconciler) inferencePoolFacts(req WorkloadReconcileRequest, inputs *scraper.BOMInputs, existing *aibomv1beta1.AIBOM) (facts []scraper.InferencePoolMembership, deferred bool, err error) {
	if r.Pools == nil || inputs.Category != scraper.CategoryInference {
		return nil, false, nil
	}
	facts, st := r.Pools.Lookup(req.Workload.Namespace, scraper.PodTemplateLabels(req.Workload.Object))
	if st == PoolViewUsable {
		return facts, false, nil
	}
	if existing == nil {
		return nil, false, nil
	}
	if prev, ok := publishedInferencePools(existing); ok {
		return prev, false, nil
	}
	without := *inputs
	without.InferencePools = nil
	h, err := scraper.HashBOMInputs(&without)
	if err != nil {
		return nil, false, err
	}
	if existing.Status.InputHash != "" && existing.Status.InputHash == h {
		return nil, false, nil
	}
	return nil, st == PoolViewUnsynced, nil
}

// publishedInferencePools reads the routing facts of the AIBOM's
// inline document. ok is false when there is no inline document to
// read (external or truncated, or never written).
func publishedInferencePools(a *aibomv1beta1.AIBOM) ([]scraper.InferencePoolMembership, bool) {
	ref := a.Status.BOMDocument
	if ref == nil || ref.Inline == nil || len(ref.Inline.Data) == 0 {
		return nil, false
	}
	var doc struct {
		Metadata struct {
			Component struct {
				Properties []struct {
					Name  string `json:"name"`
					Value string `json:"value"`
				} `json:"properties"`
			} `json:"component"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(ref.Inline.Data, &doc); err != nil {
		return nil, false
	}
	byIndex := map[int]*scraper.InferencePoolMembership{}
	for _, p := range doc.Metadata.Component.Properties {
		rest, found := strings.CutPrefix(p.Name, bom.RoutingInferencePoolPrefix)
		if !found {
			continue
		}
		idx, picker := rest, false
		if i, isPicker := strings.CutSuffix(rest, ":endpointPicker"); isPicker {
			idx, picker = i, true
		}
		n, err := strconv.Atoi(idx)
		if err != nil || n < 0 {
			continue
		}
		m := byIndex[n]
		if m == nil {
			m = &scraper.InferencePoolMembership{}
			byIndex[n] = m
		}
		if picker {
			m.EndpointPicker = p.Value
		} else {
			m.Pool = p.Value
		}
	}
	idxs := make([]int, 0, len(byIndex))
	for i := range byIndex {
		idxs = append(idxs, i)
	}
	sort.Ints(idxs)
	var out []scraper.InferencePoolMembership
	for _, i := range idxs {
		if byIndex[i].Pool != "" {
			out = append(out, *byIndex[i])
		}
	}
	return out, true
}
