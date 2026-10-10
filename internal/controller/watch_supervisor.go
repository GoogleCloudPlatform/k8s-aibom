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
)

// ThirdPartyWatch describes one third-party kind run under the
// WatchSupervisor (Design 004): its GVK, the reconciler that handles
// requests for it, and the WorkloadReconciler that provides the
// namespace opt-in plumbing shared with the apps/v1 kinds.
type ThirdPartyWatch struct {
	// Name is the stable kind label used in health, metrics, events and
	// log lines (e.g. "DynamoGraphDeployment").
	Name       string
	GVK        schema.GroupVersionKind
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

	// Runtime state, owned by the Start goroutine.
	rootCtx context.Context
	runners map[string]*watchRunner
	wg      sync.WaitGroup
	desired struct {
		sync.Mutex
		set config.WorkloadKindSet
	}
	kick chan struct{}

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
	s.Health.Subscribe(s.emitTransitionEvent)
	s.rootCtx = ctx
	s.runners = map[string]*watchRunner{}
	s.kick = make(chan struct{}, 1)
	for _, w := range s.Watches {
		s.Health.Register(w.Name)
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

// apply reconciles the running kinds with the allowlist: starts allowed
// kinds that are not running, stops running kinds that are no longer
// allowed (then marks them disabled and sweeps their AIBOMs), and marks
// excluded kinds disabled at startup. Idempotent.
func (s *WatchSupervisor) apply(allowed config.WorkloadKindSet) {
	if s.rootCtx.Err() != nil {
		return
	}
	for i := range s.Watches {
		w := s.Watches[i]
		gk := schema.GroupKind{Group: w.GVK.Group, Kind: w.GVK.Kind}
		r, running := s.runners[w.Name]
		switch {
		case allowed.Allows(gk) && !running:
			s.Health.MarkStarting(w.Name)
			kctx, cancel := context.WithCancel(s.rootCtx)
			r = &watchRunner{cancel: cancel, done: make(chan struct{})}
			s.runners[w.Name] = r
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				defer close(r.done)
				s.run(kctx, w)
			}()
		case !allowed.Allows(gk) && running:
			r.cancel()
			<-r.done
			delete(s.runners, w.Name)
			s.disable(w)
		case !allowed.Allows(gk):
			s.disable(w)
		}
	}
}

// disable records the configured-off state and, on the transition into
// it, deletes the kind's AIBOMs: with its controller stopped there is
// no reconcile left to do it (Design 006 §2 reaches this kind through
// the supervisor, not the reconcile path).
func (s *WatchSupervisor) disable(w ThirdPartyWatch) {
	if !s.Health.MarkDisabled(w.Name) {
		return
	}
	log := ctrl.Log.WithName("watch-supervisor").WithValues("kind", w.Name)
	log.Info("watch disabled by spec.discovery.workloadKinds")
	if s.Client == nil {
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
			s.Health.MarkUnhealthy(w.Name, err)
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
			s.Health.MarkUnhealthy(w.Name, err)
		}
		log.Info("watch stopped; will rebuild", "err", err.Error(), "retryIn", backoff.String())
		if !sleepCtx(ctx, backoff) {
			return
		}
		backoff = nextBackoff(backoff, s.MaxBackoff)
	}
}

// probe is a one-item list through the uncached reader: the cheapest
// request that exercises the same path the informer's initial list
// will take (including any conversion webhook).
func (s *WatchSupervisor) probe(ctx context.Context, w ThirdPartyWatch) error {
	pctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(listGVK(w.GVK))
	return s.APIReader.List(pctx, list, client.Limit(1))
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
				s.Health.MarkUnhealthy(w.Name, err)
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
			if s.Health.MarkHealthy(w.Name) {
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
			// of #127). A one-item list through the uncached reader takes
			// the same conversion path and fails honestly. A clean probe
			// also clears a degraded kind once the operator is back.
			if perr := s.probe(ctx, w); perr != nil {
				if s.Health.MarkUnhealthy(w.Name, perr) {
					log.Info("watch degraded", "err", perr.Error())
				}
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

// RegisterThirdPartyWatches wires every third-party kind whose CRD is
// present into one WatchSupervisor and adds it to the manager. Kinds
// whose CRD is absent are skipped with a log line, as before. store and
// fanout carry the workload-kind allowlist (Design 006); both may be
// nil. Shared by cmd/manager and the envtest harness so both exercise
// the same path.
func RegisterThirdPartyWatches(mgr ctrl.Manager, health *WatchHealth, tracked *TrackedKinds, store *config.Store, fanout *AllowlistFanout, recorder record.EventRecorder, controllerPod *corev1.ObjectReference, candidates []ThirdPartyWatch, tune func(*WatchSupervisor)) error {
	log := ctrl.Log.WithName("setup")
	var present []ThirdPartyWatch
	for _, w := range candidates {
		_, err := mgr.GetRESTMapper().RESTMapping(schema.GroupKind{Group: w.GVK.Group, Kind: w.GVK.Kind}, w.GVK.Version)
		switch {
		case err == nil:
			present = append(present, w)
			if tracked != nil {
				tracked.Add(schema.GroupKind{Group: w.GVK.Group, Kind: w.GVK.Kind})
			}
		case meta.IsNoMatchError(err):
			log.Info("CRD not found; skipping watch", "kind", w.Name, "gvk", w.GVK.String())
		default:
			return fmt.Errorf("RESTMapper lookup for %s: %w", w.Name, err)
		}
	}
	if len(present) == 0 {
		return nil
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
		Watches:       present,
		ConfigStore:   store,
		Fanout:        fanout,
	}
	if tune != nil {
		tune(s)
	}
	return mgr.Add(s)
}
