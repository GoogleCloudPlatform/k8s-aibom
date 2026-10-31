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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/GoogleCloudPlatform/k8s-aibom/internal/config"
)

// Design 004 amendment A2 rows 5-9 (#226, phase B), driven through
// apply() with a fake runner, so ordering and once-only effects are
// checked without an API server.

// lifecycleRig is a supervisor with a fake runner and a log of what
// happened, in order.
type lifecycleRig struct {
	s   *WatchSupervisor
	mu  sync.Mutex
	log []string
}

func (r *lifecycleRig) record(e string) {
	r.mu.Lock()
	r.log = append(r.log, e)
	r.mu.Unlock()
}

func (r *lifecycleRig) events() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.log...)
}

func newLifecycleRig(t *testing.T) *lifecycleRig {
	t.Helper()
	r := &lifecycleRig{s: newApplyTestSupervisor(t)}
	r.s.runFn = func(ctx context.Context, w ThirdPartyWatch) {
		r.record("start " + w.Name)
		<-ctx.Done()
		r.record("stop " + w.Name)
	}
	r.s.Health.Subscribe(func(tr WatchTransition) {
		switch {
		case tr.Disabled:
			st, _ := r.s.Health.Status(tr.Kind)
			r.record("disabled " + string(st.DisabledReason))
		case tr.Started:
			r.record("started-event")
		case tr.Healthy:
			r.record("recovered-event")
		default:
			r.record("unhealthy-event")
		}
	})
	t.Cleanup(func() {
		for name := range r.s.runners {
			r.s.runners[name].cancel()
			<-r.s.runners[name].done
		}
	})
	return r
}

const dgd = "DynamoGraphDeployment"

func (r *lifecycleRig) observe(obs crdObservation, allowed config.WorkloadKindSet) {
	r.s.crd.Lock()
	r.s.crd.obs[dgd] = obs
	r.s.crd.Unlock()
	r.s.apply(allowed)
}

func (r *lifecycleRig) running() bool {
	_, ok := r.s.runners[dgd]
	return ok
}

func countPrefix(events []string, prefix string) int {
	n := 0
	for _, e := range events {
		if strings.HasPrefix(e, prefix) {
			n++
		}
	}
	return n
}

var allKinds = config.AllWorkloadKinds()

// Rows 5 and 6: an unservable CRD stops its runner, is Degraded with
// the reason and keeps its membership; Ready again restarts it without
// arming WatchStarted.
func TestLifecycle_UnservableAndBack(t *testing.T) {
	r := newLifecycleRig(t)
	r.observe(crdObservation{state: crdStateReady, uid: "u1"}, allKinds)
	if !r.running() {
		t.Fatal("Ready and allowed must run")
	}
	r.observe(crdObservation{state: crdStateUnservable, uid: "u1", reason: "version v1beta1 is not served"}, allKinds)
	if r.running() {
		t.Fatal("row 5: the runner must be stopped")
	}
	st, _ := r.s.Health.Status(dgd)
	if !st.Degraded() || st.Disabled || !strings.Contains(st.LastError, "not served") {
		t.Errorf("row 5: want Degraded with the reason, not disabled; got %+v", st)
	}
	if !r.s.Tracked.Has(gkDGD) {
		t.Error("row 5: an unservable kind stays tracked")
	}
	// Repeated observations change nothing.
	r.observe(crdObservation{state: crdStateUnservable, uid: "u1", reason: "version v1beta1 is not served"}, allKinds)
	if n := countPrefix(r.events(), "unhealthy-event"); n != 1 {
		t.Errorf("row 5: one WatchUnhealthy per outage, got %d: %v", n, r.events())
	}

	r.observe(crdObservation{state: crdStateReady, uid: "u1"}, allKinds)
	if !r.running() {
		t.Fatal("row 6: the runner must restart")
	}
	if r.s.isPendingStart(dgd) {
		t.Error("row 6 recovers with WatchRecovered, never WatchStarted")
	}
	if st, _ := r.s.Health.Status(dgd); st.Degraded() || st.Disabled {
		t.Errorf("row 6: starting again, not degraded: %+v", st)
	}
	time.Sleep(50 * time.Millisecond)
	if n := countPrefix(r.events(), "start "); n != 2 {
		t.Errorf("two runner starts in total, got %d: %v", n, r.events())
	}
	if countPrefix(r.events(), "disabled") != 0 {
		t.Errorf("rows 5-6 never disable: %v", r.events())
	}
}

// Row 7 from Unservable: exclusion disables with reason config, once.
func TestLifecycle_UnservableThenExcluded(t *testing.T) {
	r := newLifecycleRig(t)
	excluded := config.NewWorkloadKindSet(gkDeployment)
	r.observe(crdObservation{state: crdStateReady, uid: "u1"}, allKinds)
	r.observe(crdObservation{state: crdStateUnservable, uid: "u1", reason: "Established is not True"}, allKinds)
	r.observe(crdObservation{state: crdStateUnservable, uid: "u1", reason: "Established is not True"}, excluded)
	r.observe(crdObservation{state: crdStateUnservable, uid: "u1", reason: "Established is not True"}, excluded)
	st, _ := r.s.Health.Status(dgd)
	if !st.Disabled || st.DisabledReason != DisabledByConfig {
		t.Fatalf("row 7: want disabled(config), got %+v", st)
	}
	if n := countPrefix(r.events(), "disabled config"); n != 1 {
		t.Errorf("row 7: one WatchDisabled, got %d: %v", n, r.events())
	}
	// Allowed again while still unservable: Degraded, not running.
	r.observe(crdObservation{state: crdStateUnservable, uid: "u1", reason: "Established is not True"}, allKinds)
	if st, _ := r.s.Health.Status(dgd); st.Disabled || !st.Degraded() || r.running() {
		t.Errorf("allowed while unservable: Degraded and stopped, got %+v running=%v", st, r.running())
	}
}

// Row 8: a confirmed removal stops the runner, disables with reason
// crd-removed once, drops tracked membership and asks for a fan-out.
func TestLifecycle_Removed(t *testing.T) {
	r := newLifecycleRig(t)
	r.observe(crdObservation{state: crdStateReady, uid: "u1"}, allKinds)
	<-r.s.fanoutReq // the activation's fan-out
	r.observe(crdObservation{state: crdStateRemoved, uid: "u1"}, allKinds)
	if r.running() {
		t.Fatal("row 8: runner must be stopped")
	}
	st, _ := r.s.Health.Status(dgd)
	if !st.Disabled || st.DisabledReason != DisabledCRDRemoved || st.Degraded() {
		t.Fatalf("row 8: want disabled(crd-removed), got %+v", st)
	}
	if r.s.Tracked.Has(gkDGD) {
		t.Error("row 8: a removed kind is no longer tracked")
	}
	select {
	case <-r.s.fanoutReq:
	default:
		t.Error("row 8: the removal requests a fan-out so children re-root")
	}
	r.observe(crdObservation{state: crdStateRemoved, uid: "u1"}, allKinds)
	r.observe(crdObservation{state: crdStateRemoved, uid: "u1"}, config.NewWorkloadKindSet(gkDeployment))
	ev := r.events()
	if n := countPrefix(ev, "disabled"); n != 1 {
		t.Errorf("row 8: WatchDisabled on entry only, got %d: %v", n, ev)
	}
	if countPrefix(ev, "unhealthy-event") != 0 {
		t.Errorf("row 8: a removal is not a failure: %v", ev)
	}
}

// Row 8 for a kind already excluded: the reason is updated, no second
// event (and so no second sweep).
func TestLifecycle_RemovedWhileExcluded(t *testing.T) {
	r := newLifecycleRig(t)
	excluded := config.NewWorkloadKindSet(gkDeployment)
	r.observe(crdObservation{state: crdStateReady, uid: "u1"}, excluded)
	r.observe(crdObservation{state: crdStateRemoved, uid: "u1"}, excluded)
	st, _ := r.s.Health.Status(dgd)
	if st.DisabledReason != DisabledCRDRemoved {
		t.Errorf("reason must follow the removal, got %q", st.DisabledReason)
	}
	if n := countPrefix(r.events(), "disabled"); n != 1 {
		t.Errorf("one WatchDisabled in total, got %d: %v", n, r.events())
	}
	if r.s.Tracked.Has(gkDGD) {
		t.Error("removed: not tracked")
	}
}

// Row 9: a new UID retires the old generation (runner stopped,
// disabled, untracked) before the new runner starts, which then owes
// WatchStarted.
func TestLifecycle_RecreatedUnderNewUID(t *testing.T) {
	r := newLifecycleRig(t)
	r.observe(crdObservation{state: crdStateReady, uid: "u1"}, allKinds)
	time.Sleep(20 * time.Millisecond)
	r.observe(crdObservation{state: crdStateReady, uid: "u2", late: true}, allKinds)
	time.Sleep(50 * time.Millisecond)
	ev := r.events()
	idx := func(e string, nth int) int {
		seen := 0
		for i, x := range ev {
			if x == e {
				if seen == nth {
					return i
				}
				seen++
			}
		}
		return -1
	}
	stop1, dis, start2 := idx("stop "+dgd, 0), idx("disabled crd-removed", 0), idx("start "+dgd, 1)
	if stop1 < 0 || dis < 0 || start2 < 0 || stop1 >= dis || dis >= start2 {
		t.Fatalf("row 9: want stop(old) < disabled(crd-removed) < start(new), got %v", ev)
	}
	if !r.s.isPendingStart(dgd) {
		t.Error("row 9: the new generation's first sync owes WatchStarted")
	}
	if !r.s.Tracked.Has(gkDGD) {
		t.Error("row 9: the new generation is tracked")
	}
	if got := r.s.life[dgd]; got.uid != "u2" || got.state != crdStateReady {
		t.Errorf("row 9: acting on the new generation, got %+v", got)
	}
}

// Observations about a generation apply never acted on, or about an
// older one, are no evidence for the current one.
func TestLifecycle_StaleAndUnactedObservationsIgnored(t *testing.T) {
	r := newLifecycleRig(t)
	// Created and removed between passes: nothing at all.
	r.observe(crdObservation{state: crdStateRemoved, uid: "u0"}, allKinds)
	if _, ok := r.s.Health.Status(dgd); ok || len(r.events()) != 0 {
		t.Fatalf("an unacted generation's removal is a no-op; health present=%v events=%v", ok, r.events())
	}
	r.observe(crdObservation{state: crdStateReady, uid: "u1"}, allKinds)
	// A late Removed/Unservable for an older UID must not touch u1.
	r.observe(crdObservation{state: crdStateRemoved, uid: "u0"}, allKinds)
	r.observe(crdObservation{state: crdStateUnservable, uid: "u0", reason: "x"}, allKinds)
	if !r.running() || countPrefix(r.events(), "disabled") != 0 || countPrefix(r.events(), "unhealthy-event") != 0 {
		t.Errorf("stale observations must not affect the current generation: %v", r.events())
	}
}

// Shutdown: once the root context is done, apply() acts on nothing,
// so no sweep or event follows cancellation.
func TestLifecycle_NothingAfterCancellation(t *testing.T) {
	r := newLifecycleRig(t)
	r.observe(crdObservation{state: crdStateReady, uid: "u1"}, allKinds)
	ctx, cancel := context.WithCancel(context.Background())
	r.s.rootCtx = ctx
	cancel()
	before := len(r.events())
	r.observe(crdObservation{state: crdStateRemoved, uid: "u1"}, allKinds)
	if len(r.events()) != before {
		t.Errorf("apply after cancellation must do nothing: %v", r.events()[before:])
	}
}

func TestMarkDisabled_Reasons(t *testing.T) {
	h := NewWatchHealth()
	var msgs []string
	h.Subscribe(func(tr WatchTransition) { msgs = append(msgs, tr.Message) })
	h.Register("K")
	if !h.MarkDisabled("K", DisabledByConfig) {
		t.Fatal("first MarkDisabled transitions")
	}
	if h.MarkDisabled("K", DisabledCRDRemoved) {
		t.Fatal("already disabled: no second transition")
	}
	if st, _ := h.Status("K"); st.DisabledReason != DisabledCRDRemoved {
		t.Errorf("reason updated without a transition, got %q", st.DisabledReason)
	}
	h.MarkStarting("K")
	if st, _ := h.Status("K"); st.Disabled || st.DisabledReason != "" {
		t.Errorf("MarkStarting clears the disabled state and reason: %+v", st)
	}
	h.MarkDisabled("K", DisabledCRDRemoved)
	if len(msgs) != 2 || !strings.Contains(msgs[0], "reason: config") || !strings.Contains(msgs[1], "CRD was removed (reason: crd-removed)") {
		t.Errorf("event messages carry the reason: %q", msgs)
	}
}

func TestTrackedKinds_Remove(t *testing.T) {
	tk := NewTrackedKinds()
	if tk.Remove(gkDGD) {
		t.Error("removing an untracked kind reports false")
	}
	tk.Add(gkDGD)
	if !tk.Remove(gkDGD) || tk.Has(gkDGD) || tk.Owns(gkDGD) {
		t.Error("Remove drops membership")
	}
	if !tk.Add(gkDGD) {
		t.Error("a removed kind can be added again")
	}
}

// Absence-shaped runner errors wake discovery and only count as a
// failure once they outlast CacheSyncTimeout; other errors count at
// once.
func TestRunnerError_AbsenceGrace(t *testing.T) {
	s := newApplyTestSupervisor(t)
	s.CacheSyncTimeout = 200 * time.Millisecond
	k := &crdWorker{w: s.Watches[0], kick: make(chan struct{}, 1)}
	s.crdWorkers = map[string]*crdWorker{dgd: k}
	s.Health.Register(dgd)
	notFound := apierrors.NewNotFound(schema.GroupResource{Group: "nvidia.com", Resource: "dynamographdeployments"}, "")
	noMatch := fmt.Errorf("get informer: %w", &meta.NoKindMatchError{GroupKind: gkDGD, SearchedVersions: []string{"v1beta1"}})
	for _, err := range []error{notFound, noMatch} {
		if s.runnerError(s.Watches[0], err) {
			t.Errorf("%v: no transition inside the grace window", err)
		}
	}
	select {
	case <-k.kick:
	default:
		t.Error("an absence-shaped error must wake the discovery worker")
	}
	if st, _ := s.Health.Status(dgd); st.Degraded() {
		t.Errorf("not degraded inside the grace window: %+v", st)
	}
	time.Sleep(250 * time.Millisecond)
	if !s.runnerError(s.Watches[0], notFound) {
		t.Error("past CacheSyncTimeout the streak counts as a failure")
	}
	s.clearAbsent(dgd)
	h2 := newApplyTestSupervisor(t)
	h2.crdWorkers = map[string]*crdWorker{}
	h2.Health.Register(dgd)
	if !h2.runnerError(h2.Watches[0], apierrors.NewServiceUnavailable("webhook down")) {
		t.Error("other errors count at once")
	}
}

// The discovery worker's per-generation classification (A1).
func TestReadCRD_Lifecycle(t *testing.T) {
	mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{testDGDGVK.GroupVersion()})
	mapper.Add(testDGDGVK, meta.RESTScopeNamespace)
	deleting := func(uid string) func(*unstructured.Unstructured) error {
		return func(u *unstructured.Unstructured) error {
			_ = crdStep(uid, true)(u)
			now := metav1.Now()
			u.SetDeletionTimestamp(&now)
			return nil
		}
	}
	type want struct {
		state crdState
		uid   types.UID
		late  bool
	}
	cases := []struct {
		name  string
		steps []func(*unstructured.Unstructured) error
		want  want
	}{
		{"Ready then not Ready: Unservable, same UID", []func(*unstructured.Unstructured) error{crdStep("A", true), crdStep("A", false)}, want{crdStateUnservable, "A", false}},
		{"Unservable then Ready: Ready, not late", []func(*unstructured.Unstructured) error{notFoundStep, crdStep("A", true), crdStep("A", false), crdStep("A", true)}, want{crdStateReady, "A", false}},
		{"Ready then NotFound: Removed", []func(*unstructured.Unstructured) error{crdStep("A", true), notFoundStep}, want{crdStateRemoved, "A", false}},
		{"Ready then deletionTimestamp: Removed", []func(*unstructured.Unstructured) error{crdStep("A", true), deleting("A")}, want{crdStateRemoved, "A", false}},
		{"Unservable then NotFound: Removed", []func(*unstructured.Unstructured) error{crdStep("A", true), crdStep("A", false), notFoundStep}, want{crdStateRemoved, "A", false}},
		{"Ready then new UID Ready: new generation, late", []func(*unstructured.Unstructured) error{crdStep("A", true), crdStep("B", true)}, want{crdStateReady, "B", true}},
		{"Ready then new UID not Ready: old Removed", []func(*unstructured.Unstructured) error{crdStep("A", true), crdStep("B", false)}, want{crdStateRemoved, "A", false}},
		{"Ready then read errors: still Ready", []func(*unstructured.Unstructured) error{crdStep("A", true), timeoutStep, forbiddenStep, timeoutStep}, want{crdStateReady, "A", false}},
		{"Ready, Removed, re-installed: late", []func(*unstructured.Unstructured) error{crdStep("A", true), notFoundStep, crdStep("B", true)}, want{crdStateReady, "B", true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &WatchSupervisor{CRDReader: &scriptedCRDReader{steps: tc.steps}, Mapper: mapper, CacheSyncTimeout: time.Minute}
			s.crd.obs = map[string]crdObservation{}
			s.kick = make(chan struct{}, 1)
			k := &crdWorker{w: ThirdPartyWatch{Name: dgd, GVK: testDGDGVK, CRD: "dynamographdeployments.nvidia.com"}}
			for range tc.steps {
				s.readCRD(context.Background(), k, logr.Discard())
			}
			obs := s.observation(dgd)
			if obs.state != tc.want.state || obs.uid != tc.want.uid || (tc.want.state == crdStateReady && obs.late != tc.want.late) {
				t.Errorf("got %v uid=%s late=%v; want %v uid=%s late=%v", obs.state, obs.uid, obs.late, tc.want.state, tc.want.uid, tc.want.late)
			}
		})
	}
}

func forbiddenStep(*unstructured.Unstructured) error {
	return apierrors.NewForbidden(schema.GroupResource{Group: "apiextensions.k8s.io", Resource: "customresourcedefinitions"}, "x", fmt.Errorf("test"))
}

// Supervised kinds without a running controller are skipped by the
// fan-out (A5).
func TestAllowlistFanout_SkipsInactiveSupervisedKinds(t *testing.T) {
	r := &countingListReader{}
	f := &AllowlistFanout{Reader: r}
	f.Source(gkDGD, true, unstructuredListFactory(testDGDGVK), unstructuredListItems)
	f.SetActive(gkDGD, false)
	f.Trigger(config.AllWorkloadKinds())
	if r.n != 0 {
		t.Errorf("an inactive supervised kind must not be listed, got %d lists", r.n)
	}
	f.SetActive(gkDGD, true)
	f.Trigger(config.AllWorkloadKinds())
	if r.n != 1 {
		t.Errorf("an active one is listed once, got %d", r.n)
	}
}

type countingListReader struct {
	blockingListReader
	n int
}

func (r *countingListReader) List(context.Context, client.ObjectList, ...client.ListOption) error {
	r.n++
	return nil
}
