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
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	toolscache "k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	aibomv1beta1 "github.com/GoogleCloudPlatform/k8s-aibom/api/v1beta1"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/config"
)

// Event reasons emitted by the watch supervisor on the controller Pod.
const (
	// EventReasonWatchUnhealthy fires (Warning) once per outage when a
	// third-party watch cannot list or sync.
	EventReasonWatchUnhealthy = "WatchUnhealthy"
	// EventReasonWatchRecovered fires (Normal) once when it recovers.
	EventReasonWatchRecovered = "WatchRecovered"
	// EventReasonWatchDisabled fires (Normal) once when a supervised
	// kind is switched off by spec.discovery.workloadKinds (Design 006
	// §3). Not a Warning: nothing failed.
	EventReasonWatchDisabled = "WatchDisabled"
	// EventReasonWatchStarted fires (Normal) once, in place of
	// WatchRecovered, on the first informer sync of a kind whose CRD was
	// installed after the supervisor started (Design 004 amendment A4,
	// #226).
	EventReasonWatchStarted = "WatchStarted"
)

// ThirdPartyWatch describes one third-party kind run under the
// WatchSupervisor (Design 004): its GVK, the reconciler that handles
// requests for it, and the WorkloadReconciler that provides the
// namespace opt-in plumbing shared with the apps/v1 kinds.
type ThirdPartyWatch struct {
	// Name is the stable kind label used in health, metrics, events and
	// log lines (e.g. "DynamoGraphDeployment").
	Name string
	GVK  schema.GroupVersionKind
	// CRD is the compiled-in name of the CustomResourceDefinition that
	// serves GVK (<plural>.<group>). Discovery (Design 004 amendment,
	// #226) reads exactly this CRD to decide whether the kind is Ready.
	CRD        string
	Reconciler reconcile.Reconciler
	Base       *WorkloadReconciler
}

// WatchSupervisor runs third-party kinds in isolation from the manager's
// shared cache (Design 004). Each kind gets its own cache and an
// unmanaged controller; a failure to probe, sync or run is recorded in
// WatchHealth and retried with capped backoff, and is never returned to
// the manager. A kind whose watch degrades after a healthy start keeps
// serving its last-known view and performs no deletions on its own
// initiative: its reconciler is only ever driven by events the (now
// stale) informer delivers.
//
// Why: controller-runtime's Kind source waits for the WHOLE cache it
// belongs to. With third-party kinds on the manager cache, one informer
// that can never list (a Dynamo CRD whose conversion webhook is down)
// times out every controller's start and takes the process down.
type WatchSupervisor struct {
	Config        *rest.Config
	Scheme        *runtime.Scheme
	Mapper        meta.RESTMapper
	SharedCache   cache.Cache   // the manager cache: AIBOM and Namespace sources
	APIReader     client.Reader // uncached reader for probes
	Client        client.Client // deletes a disabled kind's AIBOMs (Design 006); nil skips the sweep
	Health        *WatchHealth
	Recorder      record.EventRecorder
	ControllerPod *corev1.ObjectReference // nil-tolerant: events are skipped
	Watches       []ThirdPartyWatch

	// ConfigStore supplies spec.discovery.workloadKinds (Design 006 §3):
	// a supervised kind not in the set is not started, or is stopped
	// when removed, and started when added back. nil runs every kind.
	ConfigStore *config.Store
	// Fanout, when set, gives each kind's controller the channel source
	// through which an allowlist change re-enqueues its workloads.
	Fanout *AllowlistFanout
	// Tracked receives a kind once its CRD is confirmed Ready, so
	// workloads it owns are rolled up (Design 005; amendment A2). nil
	// disables roll-up bookkeeping.
	Tracked *TrackedKinds
	// CRDReader reads full CRDs by name for readiness (amendment A3).
	// Defaults to APIReader; tests substitute a failing reader.
	CRDReader client.Reader

	// Runtime state, owned by the Start goroutine.
	rootCtx context.Context
	runners map[string]*watchRunner
	wg      sync.WaitGroup
	desired struct {
		sync.Mutex
		set config.WorkloadKindSet
	}
	kick chan struct{}

	// Discovery state (amendment A1): what discovery last confirmed
	// about each candidate's CRD. Written by discovery workers, read by
	// apply(); only apply() acts on it.
	crd struct {
		sync.Mutex
		obs map[string]crdObservation
	}
	// Owned by the Start goroutine: kinds whose health entry and
	// TrackedKinds membership exist for the current CRD generation, the
	// generation apply() last acted on, and the last mapping-lag report
	// acted on.
	activated   map[string]bool
	life        map[string]kindLife
	lagReported map[string]int
	// crdWorkers by kind name, so a runner seeing an absence-shaped
	// error can ask discovery to re-read its CRD. Read-only after
	// startDiscovery.
	crdWorkers map[string]*crdWorker
	// absentSince: when a kind's runner first saw an absence-shaped
	// error (NotFound, NoMatch) in the current streak.
	absentSince struct {
		sync.Mutex
		at map[string]time.Time
	}
	// pendingStart marks a late-discovered kind whose first informer
	// sync must emit WatchStarted instead of WatchRecovered. Set by
	// apply(), consumed by the kind's runner goroutine.
	pendingStart struct {
		sync.Mutex
		kinds map[string]bool
	}
	fanoutReq chan struct{}
	// runFn runs one kind's watch until its context ends; s.run unless a
	// unit test substitutes a fake runner.
	runFn func(context.Context, ThirdPartyWatch)

	// Knobs with production defaults; tests shorten them.
	InitialBackoff   time.Duration // default 5s
	MaxBackoff       time.Duration // default 5m
	CacheSyncTimeout time.Duration // default 2m (controller-runtime's)
	ReprobeInterval  time.Duration // default 2m: one Limit=1 list per kind per interval
}

var _ manager.LeaderElectionRunnable = (*WatchSupervisor)(nil)

// NeedLeaderElection makes the supervisor run only on the leader, like
// the controllers it replaces.
func (s *WatchSupervisor) NeedLeaderElection() bool { return true }

// Start runs every watch until ctx is cancelled. It never returns an
// error from a watch failure; the only error path is a programming
// mistake in the supervisor's own wiring.
func (s *WatchSupervisor) Start(ctx context.Context) error {
	s.defaults()
	// Every goroutine the supervisor starts runs under this context, and
	// every return from Start cancels it before waiting, so an early
	// return can never block on goroutines still waiting for the
	// manager's context.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.rootCtx = ctx
	s.runners = map[string]*watchRunner{}
	s.kick = make(chan struct{}, 1)
	// No health entry yet: a kind is registered only once discovery has
	// confirmed its CRD Ready (amendment A2 row 1), so a candidate whose
	// CRD is never installed has no entry, no gauge series and no event.
	s.crd.obs = map[string]crdObservation{}
	s.activated = map[string]bool{}
	s.life = map[string]kindLife{}
	s.lagReported = map[string]int{}
	s.absentSince.at = map[string]time.Time{}
	s.pendingStart.kinds = map[string]bool{}
	s.fanoutReq = make(chan struct{}, 1)
	// Discovery is built before any goroutine starts: everything in it
	// that can fail does so before it launches its own goroutines.
	if err := s.startDiscovery(ctx); err != nil {
		// Only a wiring mistake (the CRD informer cannot be built at all)
		// gets here; a list/watch failure is retried by the informer.
		cancel()
		s.wg.Wait()
		return err
	}
	s.Health.Subscribe(s.emitTransitionEvent)
	if s.Fanout != nil {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.runFanout(ctx)
		}()
	}

	// Desired set: the live allowlist, kept current by the store
	// subscription. The subscriber only records and kicks; applying
	// (which may wait for a controller to drain) happens here.
	s.setDesired(config.AllWorkloadKinds())
	if s.ConfigStore != nil {
		s.ConfigStore.Subscribe(func(prev, next *config.Snapshot) {
			if prev != nil && prev.WorkloadKinds.Equal(next.WorkloadKinds) {
				return
			}
			s.setDesired(next.WorkloadKinds)
		})
		s.setDesired(s.ConfigStore.Load().WorkloadKinds)
	}
	for {
		s.apply(s.getDesired())
		select {
		case <-ctx.Done():
			s.wg.Wait()
			return nil
		case <-s.kick:
		}
	}
}

// watchRunner is one running kind: cancel stops it, done closes when
// its goroutine has returned.
type watchRunner struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func (s *WatchSupervisor) setDesired(set config.WorkloadKindSet) {
	s.desired.Lock()
	s.desired.set = set
	s.desired.Unlock()
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

func (s *WatchSupervisor) getDesired() config.WorkloadKindSet {
	s.desired.Lock()
	defer s.desired.Unlock()
	return s.desired.set
}

// apply reconciles the running kinds with the allowlist and with what
// discovery has confirmed about each candidate's CRD (Design 004
// amendment A1/A2). It is the only place runners, health, TrackedKinds
// membership and sweeps change, one kind at a time, and it acts on CRD
// state and on the allowlist separately: the destructive path (disable
// and sweep) is reached only from a confirmed removal (rows 8-9) or
// from exclusion of a kind whose CRD is Ready or Unservable (rows 3,
// 7). A candidate whose CRD has never been confirmed Ready is left
// alone entirely, whatever the allowlist says: no runner, no health
// entry, no event, no sweep (row 1). Idempotent.
func (s *WatchSupervisor) apply(allowed config.WorkloadKindSet) {
	if s.rootCtx.Err() != nil {
		return
	}
	for i := range s.Watches {
		w := s.Watches[i]
		gk := schema.GroupKind{Group: w.GVK.Group, Kind: w.GVK.Kind}
		obs := s.observation(w.Name)
		if s.rootCtx.Err() != nil {
			return
		}
		if obs.lagSeq > s.lagReported[w.Name] && obs.lagErr != nil {
			// Ready CRD the RESTMapper still cannot resolve after
			// CacheSyncTimeout (amendment A4): visible, not started.
			s.lagReported[w.Name] = obs.lagSeq
			s.Health.Register(w.Name)
			s.Health.MarkUnhealthy(w.Name, obs.lagErr)
		}
		cur := s.life[w.Name]
		if obs.state == crdStateUnknown && cur.state == crdStateUnknown {
			// Row 1: nothing confirmed, nothing acted on.
			continue
		}
		present := cur.state == crdStateReady || cur.state == crdStateUnservable

		// The generation apply acts on changes first: a removal, or a
		// new UID, retires the current generation before anything else
		// (rows 8-9), so its sweep finishes before a new runner starts.
		if present && (obs.state == crdStateRemoved && obs.uid == cur.uid ||
			obs.state == crdStateReady && obs.uid != cur.uid) {
			s.retire(w, gk)
			cur = kindLife{state: crdStateRemoved}
			present = false
		}

		switch {
		case obs.state == crdStateReady && !present:
			// Rows 2-3: a generation newly Ready.
			cur = kindLife{state: crdStateReady, uid: obs.uid}
			s.life[w.Name] = cur
			first := s.activate(w, gk)
			if allowed.Allows(gk) && first && obs.late {
				// Row 2, late discovery: this runner's first sync
				// announces WatchStarted (amendment A4). Only when the
				// start comes with the transition to Ready: a kind
				// installed while excluded (row 3) and allowed later is
				// row 10 and keeps WatchRecovered.
				s.setPendingStart(w.Name, true)
			}
		case obs.state == crdStateReady && cur.state == crdStateUnservable:
			// Row 6: the same generation is Ready again; the runner is
			// started below and recovers with WatchRecovered.
			cur.state, cur.reason = crdStateReady, ""
			s.life[w.Name] = cur
		case obs.state == crdStateUnservable && cur.state == crdStateReady && obs.uid == cur.uid:
			// Row 5: stop the watch, keep AIBOMs and membership.
			cur.state, cur.reason = crdStateUnservable, obs.reason
			s.life[w.Name] = cur
			s.stopRunner(w)
			if allowed.Allows(gk) {
				s.markUnservable(w, cur.reason)
			}
		}

		switch cur.state {
		case crdStateReady:
			s.applyAllowlist(w, gk, allowed)
		case crdStateUnservable:
			// No runner while unservable. Exclusion still disables and
			// sweeps (row 7); an allowed kind is Degraded, including one
			// allowed again after being excluded.
			if !allowed.Allows(gk) {
				s.disable(w, DisabledByConfig)
			} else if st, _ := s.Health.Status(w.Name); st.Disabled {
				s.markUnservable(w, cur.reason)
			}
		}
	}
}

// kindLife is the CRD generation apply() last acted on for a kind.
// Owned by the Start goroutine.
type kindLife struct {
	state  crdState
	uid    types.UID
	reason string
}

// applyAllowlist is the Ready kind's allowlist step, unchanged from
// Design 006: start an allowed kind that is not running (rows 2, 6,
// 10), stop and disable one no longer allowed (row 7), and disable an
// excluded one (row 3).
func (s *WatchSupervisor) applyAllowlist(w ThirdPartyWatch, gk schema.GroupKind, allowed config.WorkloadKindSet) {
	_, running := s.runners[w.Name]
	switch {
	case allowed.Allows(gk) && !running:
		s.Health.MarkStarting(w.Name)
		kctx, cancel := context.WithCancel(s.rootCtx)
		r := &watchRunner{cancel: cancel, done: make(chan struct{})}
		s.runners[w.Name] = r
		s.setRunnerActive(w.Name, true)
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer close(r.done)
			s.runFn(kctx, w)
		}()
	case !allowed.Allows(gk) && running:
		s.stopRunner(w)
		s.disable(w, DisabledByConfig)
	case !allowed.Allows(gk):
		s.disable(w, DisabledByConfig)
	}
}

// stopRunner cancels a kind's runner and waits for it to return. A
// runner stopped before its first sync announces nothing (amendment
// A4).
func (s *WatchSupervisor) stopRunner(w ThirdPartyWatch) {
	r, ok := s.runners[w.Name]
	if !ok {
		return
	}
	r.cancel()
	<-r.done
	delete(s.runners, w.Name)
	s.setRunnerActive(w.Name, false)
	s.setPendingStart(w.Name, false)
}

// markUnservable reports a kind whose CRD is present but fails A1 as
// Degraded with the reason (row 5): one WatchUnhealthy per outage, and
// its AIBOMs are kept as last known (§4).
func (s *WatchSupervisor) markUnservable(w ThirdPartyWatch, reason string) {
	if s.Health.MarkUnhealthy(w.Name, fmt.Errorf("CRD %s no longer serves %s: %s; watch stopped, existing AIBOMs kept",
		w.CRD, w.GVK.String(), reason)) {
		ctrl.Log.WithName("watch-supervisor").WithValues("kind", w.Name).Info("watch stopped: CRD unservable", "reason", reason)
	}
}

// retire ends the current CRD generation of a kind (rows 8, and the
// first half of 9): stop and await its runner, drop it from
// TrackedKinds, disable it with reason crd-removed and, on entry into
// the disabled state only, sweep its AIBOMs. It then forgets the
// activation so a later generation is registered, tracked and
// announced afresh. Everything happens before apply() moves on, so a
// new generation's runner can only start after this sweep returned.
func (s *WatchSupervisor) retire(w ThirdPartyWatch, gk schema.GroupKind) {
	s.stopRunner(w)
	if s.Tracked != nil && s.Tracked.Remove(gk) {
		// Children it absorbed are re-rooted on their next reconcile
		// (their owner can no longer be read: ownerUnresolved).
		s.requestFanout()
	}
	s.disable(w, DisabledCRDRemoved)
	delete(s.activated, w.Name)
	s.life[w.Name] = kindLife{state: crdStateRemoved}
}

// disable records the disabled state with its reason and, on the
// transition into it only, deletes the kind's AIBOMs: with its
// controller stopped there is no reconcile left to do it (Design 006
// §2 reaches this kind through the supervisor, not the reconcile
// path). A kind already disabled only has its reason updated.
func (s *WatchSupervisor) disable(w ThirdPartyWatch, reason DisabledReason) {
	if !s.Health.MarkDisabled(w.Name, reason) {
		return
	}
	log := ctrl.Log.WithName("watch-supervisor").WithValues("kind", w.Name, "reason", string(reason))
	log.Info("watch disabled")
	if s.Client == nil || s.rootCtx.Err() != nil {
		return
	}
	ctx, cancel := context.WithTimeout(s.rootCtx, 2*time.Minute)
	defer cancel()
	deleted, err := s.sweepAIBOMs(ctx, w.GVK)
	if err != nil {
		log.Info("AIBOM sweep for disabled kind incomplete", "deleted", deleted, "err", err.Error())
		return
	}
	if deleted > 0 {
		log.Info("deleted AIBOMs of disabled kind", "deleted", deleted)
	}
}

// sweepAIBOMs deletes every AIBOM whose workloadRef is the given
// group/kind. The AIBOM list comes from the shared cache, which every
// workload controller already informs on.
func (s *WatchSupervisor) sweepAIBOMs(ctx context.Context, gvk schema.GroupVersionKind) (int, error) {
	var list aibomv1beta1.AIBOMList
	if err := s.Client.List(ctx, &list); err != nil {
		return 0, fmt.Errorf("list AIBOMs: %w", err)
	}
	deleted := 0
	for i := range list.Items {
		ref := list.Items[i].Spec.WorkloadRef
		if ref.Kind != gvk.Kind || !strings.HasPrefix(ref.APIVersion, gvk.Group+"/") {
			continue
		}
		if err := s.Client.Delete(ctx, &list.Items[i]); err != nil && !apierrors.IsNotFound(err) {
			return deleted, fmt.Errorf("delete %s/%s: %w", list.Items[i].Namespace, list.Items[i].Name, err)
		}
		deleted++
	}
	return deleted, nil
}

func (s *WatchSupervisor) defaults() {
	if s.runFn == nil {
		s.runFn = s.run
	}
	if s.InitialBackoff == 0 {
		s.InitialBackoff = 5 * time.Second
	}
	if s.MaxBackoff == 0 {
		s.MaxBackoff = 5 * time.Minute
	}
	if s.CacheSyncTimeout == 0 {
		s.CacheSyncTimeout = 2 * time.Minute
	}
	if s.ReprobeInterval == 0 {
		// One Limit=1 list per present kind every two minutes: with
		// the two kinds AICR ships (Dynamo, NIM) that is one request
		// per minute on top of the measured sub-1-req/min steady state.
		s.ReprobeInterval = 2 * time.Minute
	}
}

// run is the per-kind supervision loop: probe, run until failure,
// record, back off, repeat.
func (s *WatchSupervisor) run(ctx context.Context, w ThirdPartyWatch) {
	log := ctrl.Log.WithName("watch-supervisor").WithValues("kind", w.Name)
	backoff := s.InitialBackoff
	for {
		if err := s.probe(ctx, w); err != nil {
			s.runnerError(w, err)
			log.Info("probe failed; will retry", "err", err.Error(), "retryIn", backoff.String())
			if !sleepCtx(ctx, backoff) {
				return
			}
			backoff = nextBackoff(backoff, s.MaxBackoff)
			continue
		}
		backoff = s.InitialBackoff
		err := s.runOnce(ctx, w, log)
		if ctx.Err() != nil {
			return
		}
		// The watch error handler usually recorded the real cause
		// already (e.g. the conversion-webhook message); do not
		// overwrite it with the generic sync-timeout error.
		if st, ok := s.Health.Status(w.Name); !ok || !st.Degraded() {
			s.runnerError(w, err)
		}
		log.Info("watch stopped; will rebuild", "err", err.Error(), "retryIn", backoff.String())
		if !sleepCtx(ctx, backoff) {
			return
		}
		backoff = nextBackoff(backoff, s.MaxBackoff)
	}
}

// probe asks the API server for the kind through the uncached reader:
// a one-item list, then a GET of that item by name.
//
// The list alone is not enough. The API server keeps a watch cache per
// served version, and while a conversion webhook is down it keeps
// serving LIST and WATCH for the kind from that cache; only reads that
// go to storage (a single-object GET) and writes reach the webhook and
// fail (#216, measured on GKE 1.35 with Dynamo 1.4.2: a 7-minute
// operator outage never failed a Limit=1 list, while every GET did).
// The GET therefore exercises the conversion path the list no longer
// does. A kind with no objects falls back to the list, which is the
// most the API server will tell us; NotFound between the list and the
// GET is not an outage.
func (s *WatchSupervisor) probe(ctx context.Context, w ThirdPartyWatch) error {
	pctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(listGVK(w.GVK))
	if err := s.APIReader.List(pctx, list, client.Limit(1)); err != nil {
		return err
	}
	if len(list.Items) == 0 {
		return nil
	}
	item := list.Items[0]
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(w.GVK)
	err := s.APIReader.Get(pctx, client.ObjectKey{Namespace: item.GetNamespace(), Name: item.GetName()}, obj)
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

// runOnce builds a fresh cache and controller for the kind and runs them
// until the controller returns (sync timeout, start error) or ctx ends.
func (s *WatchSupervisor) runOnce(ctx context.Context, w ThirdPartyWatch, log logr.Logger) error {
	kctx, cancel := context.WithCancel(ctx)
	defer cancel()

	kindCache, err := cache.New(s.Config, cache.Options{
		Scheme: s.Scheme,
		Mapper: s.Mapper,
		DefaultWatchErrorHandler: func(_ context.Context, _ *toolscache.Reflector, err error) {
			if err != nil {
				s.runnerError(w, err)
			}
		},
	})
	if err != nil {
		return fmt.Errorf("new cache: %w", err)
	}

	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(w.GVK)

	// Create the informer before the cache starts so that
	// WaitForCacheSync below waits on it (an empty cache reports
	// synced vacuously).
	if _, err := kindCache.GetInformer(kctx, obj); err != nil {
		return fmt.Errorf("get informer: %w", err)
	}

	c, err := controller.NewUnmanaged(strings.ToLower(w.Name), controller.Options{
		Reconciler:         w.Reconciler,
		SkipNameValidation: ptr.To(true),
		CacheSyncTimeout:   s.CacheSyncTimeout,
		NeedLeaderElection: ptr.To(false), // the supervisor itself is leader-gated
	})
	if err != nil {
		return fmt.Errorf("new controller: %w", err)
	}
	if err := c.Watch(source.Kind(kindCache, client.Object(obj), &handler.EnqueueRequestForObject{})); err != nil {
		return fmt.Errorf("watch %s: %w", w.Name, err)
	}
	if err := c.Watch(source.Kind(s.SharedCache, client.Object(&aibomv1beta1.AIBOM{}),
		handler.EnqueueRequestForOwner(s.Scheme, s.Mapper, obj, handler.OnlyControllerOwner()),
		predicate.GenerationChangedPredicate{})); err != nil {
		return fmt.Errorf("watch AIBOM for %s: %w", w.Name, err)
	}
	// Pods the root owns transitively (Design 005): digest changes in
	// a Dynamo worker pod re-reconcile the graph, not just the pod's
	// Deployment (which is rolled up and produces nothing itself).
	if err := c.Watch(source.Kind(s.SharedCache, client.Object(&corev1.Pod{}),
		handler.EnqueueRequestsFromMapFunc(w.Base.EnqueueRootForPod(schema.GroupKind{Group: w.GVK.Group, Kind: w.GVK.Kind})),
		PodImageIDChangedPredicate())); err != nil {
		return fmt.Errorf("watch Pod for %s: %w", w.Name, err)
	}
	if err := c.Watch(source.Kind(s.SharedCache, client.Object(&corev1.Namespace{}),
		handler.EnqueueRequestsFromMapFunc(w.Base.EnqueueWorkloadsForNamespace(
			unstructuredListFactory(w.GVK), unstructuredListItems)),
		w.Base.NamespaceWatchPredicate())); err != nil {
		return fmt.Errorf("watch Namespace for %s: %w", w.Name, err)
	}
	// Allowlist hot-reload (Design 006): re-reconcile every object of
	// this kind when spec.discovery.workloadKinds changes. The channel
	// is stable across rebuilds; only this source object is new.
	if s.Fanout != nil {
		if err := c.Watch(s.Fanout.Source(schema.GroupKind{Group: w.GVK.Group, Kind: w.GVK.Kind}, true,
			unstructuredListFactory(w.GVK), unstructuredListItems)); err != nil {
			return fmt.Errorf("watch allowlist fan-out for %s: %w", w.Name, err)
		}
	}

	go func() { _ = kindCache.Start(kctx) }()

	errCh := make(chan error, 1)
	go func() { errCh <- c.Start(kctx) }()

	synced := make(chan struct{})
	go func() {
		if kindCache.WaitForCacheSync(kctx) {
			close(synced)
		}
	}()

	ticker := time.NewTicker(s.ReprobeInterval)
	defer ticker.Stop()
	for {
		select {
		case <-synced:
			synced = nil
			s.clearAbsent(w.Name)
			if s.takePendingStart(w.Name) {
				// A kind discovered after startup: its first sync is
				// announced as WatchStarted, in place of WatchRecovered.
				if s.Health.MarkStarted(w.Name) {
					log.Info("watch started")
				}
			} else if s.Health.MarkHealthy(w.Name) {
				log.Info("watch healthy")
			}
		case err := <-errCh:
			if err == nil {
				err = fmt.Errorf("controller stopped")
			}
			return err
		case <-ticker.C:
			// Steady state needs an independent signal. When an
			// established watch stream hits a conversion error, client-go's
			// reflector does NOT call the watch error handler: it logs at
			// warning level and re-opens the watch from the same resource
			// version, forever, so the object is never delivered and
			// nothing fails loudly (measured; this is the silent-zero case
			// of #127). The probe reads one object by name through the
			// uncached reader, which takes the conversion path and fails
			// honestly even when the API server still serves lists from
			// its per-version cache (#216). A clean probe also clears a
			// degraded kind once the operator is back.
			perr := s.probe(ctx, w)
			if perr == nil {
				s.clearAbsent(w.Name)
			}
			if perr != nil {
				if s.runnerError(w, perr) {
					log.Info("watch degraded", "err", perr.Error())
				}
			} else if synced != nil && s.isPendingStart(w.Name) {
				// Not synced yet and WatchStarted still owed: only the
				// sync may mark this kind healthy (amendment A4).
			} else if s.Health.MarkHealthy(w.Name) {
				log.Info("watch recovered")
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (s *WatchSupervisor) emitTransitionEvent(t WatchTransition) {
	if s.Recorder == nil || s.ControllerPod == nil {
		return
	}
	switch {
	case t.Disabled:
		s.Recorder.Event(s.ControllerPod, corev1.EventTypeNormal, EventReasonWatchDisabled, t.Message)
	case t.Started:
		msg := t.Message
		for _, w := range s.Watches {
			if w.Name == t.Kind {
				msg = fmt.Sprintf("%s watch started: CRD %s, installed after the controller started, serves %s",
					w.Name, w.CRD, w.GVK.GroupVersion().String())
				break
			}
		}
		s.Recorder.Event(s.ControllerPod, corev1.EventTypeNormal, EventReasonWatchStarted, msg)
	case t.Healthy:
		s.Recorder.Event(s.ControllerPod, corev1.EventTypeNormal, EventReasonWatchRecovered, t.Message)
	default:
		s.Recorder.Event(s.ControllerPod, corev1.EventTypeWarning, EventReasonWatchUnhealthy, t.Message)
	}
}

func listGVK(gvk schema.GroupVersionKind) schema.GroupVersionKind {
	return schema.GroupVersionKind{Group: gvk.Group, Version: gvk.Version, Kind: gvk.Kind + "List"}
}

func unstructuredListFactory(gvk schema.GroupVersionKind) func() client.ObjectList {
	return func() client.ObjectList {
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(listGVK(gvk))
		return list
	}
}

func unstructuredListItems(objList client.ObjectList) []client.Object {
	uList, ok := objList.(*unstructured.UnstructuredList)
	if !ok {
		return nil
	}
	objs := make([]client.Object, 0, len(uList.Items))
	for i := range uList.Items {
		objs = append(objs, &uList.Items[i])
	}
	return objs
}

func nextBackoff(cur, max time.Duration) time.Duration {
	next := cur * 2
	if next > max {
		return max
	}
	return next
}

// sleepCtx sleeps for d or until ctx is done; returns false if ctx ended.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// RegisterThirdPartyWatches hands every third-party candidate to one
// WatchSupervisor and adds it to the manager, whether or not any
// candidate's CRD is installed yet (Design 004 amendment A3, #226): the
// supervisor discovers each CRD on the leader and starts its watch once
// the CRD is confirmed Ready, at startup or later, without a restart.
// No RESTMapping check happens here any more; it is not evidence of
// readiness, and TrackedKinds is filled by the supervisor only after a
// CRD is confirmed Ready. store and fanout carry the workload-kind
// allowlist (Design 006); tracked, store and fanout may be nil. Shared
// by cmd/manager and the envtest harness so both exercise the same
// path.
func RegisterThirdPartyWatches(mgr ctrl.Manager, health *WatchHealth, tracked *TrackedKinds, store *config.Store, fanout *AllowlistFanout, recorder record.EventRecorder, controllerPod *corev1.ObjectReference, candidates []ThirdPartyWatch, tune func(*WatchSupervisor)) error {
	if len(candidates) == 0 {
		return nil
	}
	seen := map[string]bool{}
	for _, w := range candidates {
		if w.CRD == "" {
			return fmt.Errorf("third-party watch %s: CRD name not set", w.Name)
		}
		if seen[w.CRD] {
			return fmt.Errorf("third-party watch %s: CRD %s listed twice", w.Name, w.CRD)
		}
		seen[w.CRD] = true
	}
	s := &WatchSupervisor{
		Config:        mgr.GetConfig(),
		Scheme:        mgr.GetScheme(),
		Mapper:        mgr.GetRESTMapper(),
		SharedCache:   mgr.GetCache(),
		APIReader:     mgr.GetAPIReader(),
		Client:        mgr.GetClient(),
		Health:        health,
		Recorder:      recorder,
		ControllerPod: controllerPod,
		Watches:       candidates,
		ConfigStore:   store,
		Fanout:        fanout,
		Tracked:       tracked,
	}
	if tune != nil {
		tune(s)
	}
	return mgr.Add(s)
}
