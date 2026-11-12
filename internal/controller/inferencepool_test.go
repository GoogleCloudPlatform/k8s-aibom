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
	"reflect"
	"sync"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	toolscache "k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	aibomv1beta1 "github.com/GoogleCloudPlatform/k8s-aibom/api/v1beta1"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/bom"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/config"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/scraper"
)

// Design 007 §2 (Part 2a), unit level: matching, the view's state
// machine, reading back published facts, the unavailable-view decision,
// and the supervisor's auxiliary-candidate path.

func pool(ns, name string, sel map[string]string, picker string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(InferencePoolGVK)
	u.SetNamespace(ns)
	u.SetName(name)
	if sel != nil {
		m := map[string]interface{}{}
		for k, v := range sel {
			m[k] = v
		}
		_ = unstructured.SetNestedMap(u.Object, m, "spec", "selector", "matchLabels")
	}
	if picker != "" {
		_ = unstructured.SetNestedField(u.Object, picker, "spec", "endpointPickerRef", "name")
	}
	return u
}

// usableView returns a running view that has listed pools.
func usableView(t *testing.T, pools ...*unstructured.Unstructured) *InferencePoolView {
	t.Helper()
	v := &InferencePoolView{}
	v.begin()
	list := make([]interface{}, 0, len(pools))
	for _, p := range pools {
		list = append(list, p)
	}
	if err := (poolStore{v}).Replace(list, "1"); err != nil {
		t.Fatal(err)
	}
	if st := v.State(); st != PoolViewUsable {
		t.Fatalf("state %s, want usable", st)
	}
	v.take() // drop the sync's request
	return v
}

func TestInferencePoolLookup_Matching(t *testing.T) {
	v := usableView(t,
		// AND within the selector.
		pool("ns", "both", map[string]string{"app": "vllm", "tier": "decode"}, "both-epp"),
		// Matches one template only: OR across templates.
		pool("ns", "prefill-only", map[string]string{"role": "prefill"}, ""),
		// Each pair is on a different template: never combined.
		pool("ns", "mixed", map[string]string{"role": "prefill", "tier": "decode"}, ""),
		// Same selector, other namespace: never matched.
		pool("other", "both", map[string]string{"app": "vllm"}, ""),
		// Empty and absent selectors match nothing.
		pool("ns", "empty", map[string]string{}, ""),
		pool("ns", "absent", nil, ""),
		// Value mismatch.
		pool("ns", "wrong-value", map[string]string{"app": "sglang"}, ""),
		// Name sorts before "both": ordering is by name.
		pool("ns", "a-first", map[string]string{"app": "vllm"}, ""),
	)
	templates := []map[string]string{
		{"app": "vllm", "tier": "decode"},
		{"app": "vllm", "role": "prefill"},
	}
	got, st := v.Lookup("ns", templates)
	if st != PoolViewUsable {
		t.Fatalf("state %s", st)
	}
	want := []scraper.InferencePoolMembership{
		{Pool: "ns/a-first"}, // matches both templates, recorded once
		{Pool: "ns/both", EndpointPicker: "both-epp"},
		{Pool: "ns/prefill-only"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Lookup = %v, want %v", got, want)
	}
	if got, _ := v.Lookup("ns", nil); got != nil {
		t.Errorf("a workload with no template labels matched %v", got)
	}
	if got, _ := v.Lookup("third", templates); got != nil {
		t.Errorf("a namespace with no pools matched %v", got)
	}
	var nilView *InferencePoolView
	if got, st := nilView.Lookup("ns", templates); got != nil || st != PoolViewInactive {
		t.Errorf("nil view: %v %s", got, st)
	}
}

func pending(v *InferencePoolView) (bool, []string) { return v.take() }

func TestInferencePoolView_StateAndRequests(t *testing.T) {
	v := &InferencePoolView{}
	if st := v.State(); st != PoolViewInactive {
		t.Fatalf("new view %s", st)
	}
	if got, _ := v.Lookup("ns", []map[string]string{{"a": "b"}}); got != nil {
		t.Fatal("inactive view returned facts")
	}
	v.begin()
	if st := v.State(); st != PoolViewUnsynced {
		t.Fatalf("running, unlisted: %s", st)
	}
	// Events before the first list neither count nor enqueue.
	s := poolStore{v}
	_ = s.Add(pool("ns", "p", map[string]string{"a": "b"}, ""))
	if all, nss := pending(v); all || nss != nil {
		t.Fatalf("pre-sync add enqueued %v %v", all, nss)
	}
	// A failed list before the first sync: still unsynced; a watch
	// alone cannot make an unlisted view usable.
	v.failed(apierrors.NewForbidden(schema.GroupResource{Group: InferencePoolGVK.Group, Resource: "inferencepools"}, "", nil), func() {})
	v.watchEstablished()
	if st := v.State(); st != PoolViewUnsynced {
		t.Fatalf("forbidden list: %s", st)
	}
	// First list: usable, every namespace re-enqueued.
	_ = s.Replace([]interface{}{pool("ns", "p", map[string]string{"a": "b"}, "")}, "1")
	if st := v.State(); st != PoolViewUsable {
		t.Fatalf("after list: %s", st)
	}
	if all, _ := pending(v); !all {
		t.Fatal("initial sync must re-enqueue every namespace")
	}
	// A no-op update enqueues nothing; a change enqueues its namespace.
	_ = s.Update(pool("ns", "p", map[string]string{"a": "b"}, ""))
	if all, nss := pending(v); all || nss != nil {
		t.Fatalf("no-op update enqueued %v %v", all, nss)
	}
	_ = s.Update(pool("ns", "p", map[string]string{"a": "c"}, ""))
	_ = s.Add(pool("ns2", "q", map[string]string{"a": "b"}, ""))
	if all, nss := pending(v); all || !reflect.DeepEqual(nss, []string{"ns", "ns2"}) {
		t.Fatalf("changes enqueued %v %v", all, nss)
	}
	_ = s.Delete(toolsTombstone(pool("ns2", "q", nil, "")))
	if _, nss := pending(v); !reflect.DeepEqual(nss, []string{"ns2"}) {
		t.Fatalf("delete enqueued %v", nss)
	}
	_ = s.Delete(pool("ns2", "never-seen", nil, ""))
	if all, nss := pending(v); all || nss != nil {
		t.Fatalf("delete of an unknown pool enqueued %v %v", all, nss)
	}
	// A relist of a usable view enqueues only changed namespaces.
	_ = s.Replace([]interface{}{pool("ns", "p", map[string]string{"a": "c"}, ""), pool("ns3", "r", nil, "")}, "2")
	if all, nss := pending(v); all || !reflect.DeepEqual(nss, []string{"ns3"}) {
		t.Fatalf("relist enqueued %v %v", all, nss)
	}
	// A watch failure makes the view unsynced; NotFound asks discovery
	// to re-read the CRD.
	rechecked := false
	v.failed(apierrors.NewNotFound(schema.GroupResource{Resource: "inferencepools"}, ""), func() { rechecked = true })
	if !rechecked {
		t.Error("an absence-shaped error must recheck the CRD")
	}
	if st := v.State(); st != PoolViewUnsynced {
		t.Fatalf("after failure: %s", st)
	}
	if got, _ := v.Lookup("ns", []map[string]string{{"a": "c"}}); got != nil {
		t.Fatal("an unsynced view returned facts")
	}
	// A watch resuming after a list recovers and re-enqueues all.
	v.watchEstablished()
	if st := v.State(); st != PoolViewUsable {
		t.Fatalf("after recovery: %s", st)
	}
	if all, _ := pending(v); !all {
		t.Fatal("recovery must re-enqueue every namespace")
	}
	// Stopping leaves the view inactive and re-enqueues all.
	v.end()
	if st := v.State(); st != PoolViewInactive {
		t.Fatalf("after end: %s", st)
	}
	if all, _ := pending(v); !all {
		t.Fatal("stopping must re-enqueue every namespace")
	}
	// Only a confirmed removal allows the view to discard published
	// matches. Ordinary stop/restart remains an unavailable view.
	v.confirmRemoved()
	if st := v.State(); st != PoolViewRemoved {
		t.Fatalf("after confirmed removal: %s", st)
	}
	if all, _ := pending(v); !all {
		t.Fatal("confirmed removal must re-enqueue every namespace")
	}
	v.begin()
	if st := v.State(); st != PoolViewUnsynced {
		t.Fatalf("new watch must clear removed state: %s", st)
	}
	v.end()
	if st := v.State(); st != PoolViewInactive {
		t.Fatalf("stop of new unsynced watch: %s", st)
	}
}

func toolsTombstone(obj interface{}) interface{} {
	return toolscache.DeletedFinalStateUnknown{Key: "ns2/q", Obj: obj}
}

// publishedInferencePools reads back exactly what the builder wrote.
func TestPublishedInferencePools_RoundTrip(t *testing.T) {
	facts := []scraper.InferencePoolMembership{{Pool: "ns/a", EndpointPicker: "a-epp"}, {Pool: "ns/b"}}
	a := aibomWithDoc(t, facts)
	got, ok := publishedInferencePools(a)
	if !ok || !reflect.DeepEqual(got, facts) {
		t.Errorf("read back %v %v, want %v", got, ok, facts)
	}
	none, ok := publishedInferencePools(aibomWithDoc(t, nil))
	if !ok || none != nil {
		t.Errorf("document without pools: %v %v", none, ok)
	}
	if _, ok := publishedInferencePools(&aibomv1beta1.AIBOM{}); ok {
		t.Error("no inline document must be unknown")
	}
	ext := &aibomv1beta1.AIBOM{Status: aibomv1beta1.AIBOMStatus{BOMDocument: &aibomv1beta1.BOMDocumentRef{}}}
	if _, ok := publishedInferencePools(ext); ok {
		t.Error("an external document must be unknown")
	}
}

func aibomWithDoc(t *testing.T, facts []scraper.InferencePoolMembership) *aibomv1beta1.AIBOM {
	t.Helper()
	doc, err := bom.NewBuilder().Build(&scraper.BOMInputs{ScraperName: "inference.spec", Confidence: scraper.ConfidenceInferred},
		bom.BuildOptions{WorkloadKind: "Deployment", WorkloadName: "w", WorkloadNamespace: "ns", InferencePools: facts})
	if err != nil {
		t.Fatal(err)
	}
	return &aibomv1beta1.AIBOM{Status: aibomv1beta1.AIBOMStatus{
		BOMDocument: &aibomv1beta1.BOMDocumentRef{Inline: &aibomv1beta1.InlineBOM{Data: doc.JSON}},
	}}
}

// The unavailable-view decision: no new facts, published ones kept,
// defer only when unknowable and the watch is starting or recovering.
func TestInferencePoolFacts_Decision(t *testing.T) {
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "w"},
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "vllm"}}}},
	}
	req := WorkloadReconcileRequest{Workload: scraper.Workload{Namespace: "ns", Name: "w", Object: dep}}
	inputs := func() *scraper.BOMInputs {
		return &scraper.BOMInputs{ScraperName: "inference.spec", Confidence: scraper.ConfidenceInferred, Category: scraper.CategoryInference}
	}
	published := []scraper.InferencePoolMembership{{Pool: "ns/old"}}
	inline := aibomWithDoc(t, published)
	external := &aibomv1beta1.AIBOM{}
	noPoolHash, _ := scraper.HashBOMInputs(inputs())
	externalNoPools := &aibomv1beta1.AIBOM{Status: aibomv1beta1.AIBOMStatus{InputHash: noPoolHash}}

	usable := usableView(t, pool("ns", "new", map[string]string{"app": "vllm"}, ""))
	unsynced := &InferencePoolView{}
	unsynced.begin()
	inactive := &InferencePoolView{}
	removed := &InferencePoolView{}
	removed.confirmRemoved()

	cases := []struct {
		name      string
		view      *InferencePoolView
		category  scraper.WorkloadCategory
		existing  *aibomv1beta1.AIBOM
		want      []scraper.InferencePoolMembership
		wantDefer bool
	}{
		{"feature off", nil, scraper.CategoryInference, inline, nil, false},
		{"not inference", usable, scraper.CategoryAgent, inline, nil, false},
		// Usable: recomputed, the stale "ns/old" fact is dropped.
		{"usable recomputes", usable, scraper.CategoryInference, inline, []scraper.InferencePoolMembership{{Pool: "ns/new"}}, false},
		{"unsynced keeps inline facts", unsynced, scraper.CategoryInference, inline, published, false},
		{"inactive keeps inline facts", inactive, scraper.CategoryInference, inline, published, false},
		{"confirmed removal clears inline facts", removed, scraper.CategoryInference, inline, nil, false},
		{"confirmed removal clears unknowable external facts", removed, scraper.CategoryInference, external, nil, false},
		{"unavailable, first document: none", unsynced, scraper.CategoryInference, nil, nil, false},
		{"unavailable, hash proves none", unsynced, scraper.CategoryInference, externalNoPools, nil, false},
		// Not inline and not recorded by this process (e.g. after a
		// restart): never dropped, the update waits for the view.
		{"unsynced, unknowable: defer", unsynced, scraper.CategoryInference, external, nil, true},
		{"inactive, unknowable: defer", inactive, scraper.CategoryInference, external, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &WorkloadReconciler{Pools: tc.view}
			in := inputs()
			in.Category = tc.category
			got, deferred, err := r.inferencePoolFacts(req, in, tc.existing)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) || deferred != tc.wantDefer {
				t.Errorf("got %v defer=%v, want %v defer=%v", got, deferred, tc.want, tc.wantDefer)
			}
		})
	}
}

// External and truncated documents: the facts this process published
// are kept from its record while the view is unavailable, only for the
// document the AIBOM still carries; inline documents are not recorded.
func TestInferencePoolFacts_RecordedForExternalDocuments(t *testing.T) {
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "w"},
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "vllm"}}}},
	}
	req := WorkloadReconcileRequest{Workload: scraper.Workload{Namespace: "ns", Name: "w", Object: dep}}
	in := &scraper.BOMInputs{ScraperName: "inference.spec", Confidence: scraper.ConfidenceInferred, Category: scraper.CategoryInference,
		Components: []scraper.Component{{Name: "changed-since"}}} // inputs no longer match the stored hash
	published := []scraper.InferencePoolMembership{{Pool: "ns/p", EndpointPicker: "epp"}}
	external := func(sha string, truncated bool) *aibomv1beta1.AIBOM {
		a := &aibomv1beta1.AIBOM{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "w-aibom"},
			Status: aibomv1beta1.AIBOMStatus{InputHash: "stale", BOMHash: sha,
				BOMDocument: &aibomv1beta1.BOMDocumentRef{SHA256: sha, Truncated: truncated}}}
		if !truncated {
			a.Status.BOMDocument.External = &aibomv1beta1.ExternalBOMRef{Sink: "gcs", URL: "gs://b/o"}
		}
		return a
	}

	for _, truncated := range []bool{false, true} {
		v := &InferencePoolView{}
		v.begin()
		_ = (poolStore{v}).Replace([]interface{}{pool("ns", "p", map[string]string{"app": "vllm"}, "epp")}, "1")
		r := &WorkloadReconciler{Pools: v}
		a := external("sha-1", truncated)
		v.remember(a, published)

		for _, down := range []func(){
			func() {
				v.failed(apierrors.NewForbidden(schema.GroupResource{Resource: "inferencepools"}, "", nil), func() {})
			},
			func() { v.end() }, // view stopped; removal has not been confirmed
		} {
			down()
			got, deferred, err := r.inferencePoolFacts(req, in, a)
			if err != nil || deferred || !reflect.DeepEqual(got, published) {
				t.Fatalf("truncated=%v: got %v defer=%v err=%v, want the recorded facts", truncated, got, deferred, err)
			}
		}
		// A different document on the AIBOM (another writer, or a
		// rebuilt document this process did not record): unknown.
		if _, deferred, _ := r.inferencePoolFacts(req, in, external("sha-2", truncated)); !deferred {
			t.Errorf("truncated=%v: a document this process did not record must defer", truncated)
		}
		// Deleted AIBOM: record dropped.
		v.forget(client.ObjectKeyFromObject(a))
		if _, deferred, _ := r.inferencePoolFacts(req, in, a); !deferred {
			t.Errorf("truncated=%v: a forgotten record must not be used", truncated)
		}
	}

	// Inline documents are read back directly and never recorded.
	v := &InferencePoolView{}
	inline := aibomWithDoc(t, published)
	inline.Name, inline.Namespace = "inline", "ns"
	inline.Status.BOMDocument.SHA256 = "sha-inline"
	v.remember(inline, published)
	if _, ok := v.recall(client.ObjectKeyFromObject(inline), "sha-inline"); ok {
		t.Error("an inline document must not be recorded")
	}
}

// The InferencePool candidate is driven by discovery like a workload
// kind but is not one: no health entry, no TrackedKinds membership, no
// allowlist effect, no sweep.
func TestApplyAux_InferencePoolLifecycle(t *testing.T) {
	s := newApplyTestSupervisor(t)
	var mu sync.Mutex
	var log []string
	rec := func(e string) { mu.Lock(); log = append(log, e); mu.Unlock() }
	events := func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), log...) }
	started := make(chan struct{}, 8)
	aux := ThirdPartyWatch{Name: inferencePoolName, GVK: InferencePoolGVK, CRD: InferencePoolCRD,
		Run: func(ctx context.Context, recheck func()) {
			rec("start")
			started <- struct{}{}
			<-ctx.Done()
			rec("stop")
		}}
	removals := 0
	aux.OnRemoved = func() { removals++ }
	s.Watches = []ThirdPartyWatch{aux}
	s.crdWorkers = map[string]*crdWorker{}
	observe := func(obs crdObservation) {
		s.crd.Lock()
		s.crd.obs[inferencePoolName] = obs
		s.crd.Unlock()
		// An allowlist that excludes everything has no effect on it.
		s.apply(config.NewWorkloadKindSet(gkDeployment))
	}
	waitStart := func() {
		t.Helper()
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatalf("runner did not start: %v", events())
		}
	}
	t.Cleanup(func() {
		for _, r := range s.runners {
			r.cancel()
			<-r.done
		}
	})

	observe(crdObservation{}) // unknown: nothing
	if len(s.runners) != 0 {
		t.Fatal("unknown CRD started the view")
	}
	observe(crdObservation{state: crdStateReady, uid: "u1", late: true})
	waitStart()
	observe(crdObservation{state: crdStateReady, uid: "u1"}) // no-op update
	observe(crdObservation{state: crdStateUnservable, uid: "u1", reason: "v1 not served"})
	if removals != 0 {
		t.Fatal("unservable CRD must not clear published pool facts")
	}
	observe(crdObservation{state: crdStateReady, uid: "u1"})
	waitStart()
	observe(crdObservation{state: crdStateReady, uid: "u2"}) // re-created
	waitStart()
	observe(crdObservation{state: crdStateRemoved, uid: "u2"})
	observe(crdObservation{state: crdStateRemoved, uid: "u2"})
	if removals != 1 {
		t.Errorf("confirmed removal notified %d times, want once", removals)
	}

	want := []string{"start", "stop", "start", "stop", "start", "stop"}
	if got := events(); !reflect.DeepEqual(got, want) {
		t.Errorf("lifecycle %v, want %v", got, want)
	}
	if _, ok := s.Health.Status(inferencePoolName); ok {
		t.Error("the InferencePool view must have no watch health entry")
	}
	if s.Tracked.Has(schema.GroupKind{Group: InferencePoolGVK.Group, Kind: inferencePoolName}) {
		t.Error("InferencePool must never be a tracked kind")
	}
	select {
	case <-s.fanoutReq:
		t.Error("the InferencePool view must not request an allowlist fan-out")
	default:
	}
}

// flakyListReader fails the first `fail` List calls, then lists one
// Deployment in the requested namespace.
type flakyListReader struct {
	client.Reader
	mu    sync.Mutex
	fail  int
	calls int
}

func (r *flakyListReader) List(_ context.Context, list client.ObjectList, opts ...client.ListOption) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if r.calls <= r.fail {
		return apierrors.NewServiceUnavailable("injected list failure")
	}
	o := &client.ListOptions{}
	o.ApplyOptions(opts)
	list.(*appsv1.DeploymentList).Items = []appsv1.Deployment{{ObjectMeta: metav1.ObjectMeta{Namespace: o.Namespace, Name: "vllm"}}}
	return nil
}

// A pool change whose re-enqueue fan-out fails (a list error or a
// timeout) is retried: the affected workloads are still re-enqueued,
// otherwise their documents would keep stale membership until an
// unrelated workload event.
func TestInferencePoolView_RetriesIncompleteFanout(t *testing.T) {
	reader := &flakyListReader{fail: 2}
	f := &AllowlistFanout{Reader: reader}
	f.Source(schema.GroupKind{Group: "apps", Kind: "Deployment"}, false,
		func() client.ObjectList { return &appsv1.DeploymentList{} },
		func(l client.ObjectList) []client.Object {
			var out []client.Object
			for i := range l.(*appsv1.DeploymentList).Items {
				out = append(out, &l.(*appsv1.DeploymentList).Items[i])
			}
			return out
		})
	ch := f.kinds[0].ch
	v := &InferencePoolView{Fanout: f, fanoutRetry: 10 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = v.Start(ctx) }()
	defer func() { cancel(); <-done }()

	v.request("ns")
	select {
	case ev := <-ch:
		if ev.Object.GetNamespace() != "ns" || ev.Object.GetName() != "vllm" {
			t.Fatalf("re-enqueued %s/%s", ev.Object.GetNamespace(), ev.Object.GetName())
		}
	case <-time.After(5 * time.Second):
		reader.mu.Lock()
		defer reader.mu.Unlock()
		t.Fatalf("the namespace was never re-enqueued after %d list calls", reader.calls)
	}
}
