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
	"strings"
	"time"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/metadata"
	toolscache "k8s.io/client-go/tools/cache"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// CRD discovery and lifecycle (Design 004 amendment, #226: A2 rows 1-10).
//
// One metadata-only CustomResourceDefinition informer, in its own cache
// outside the manager's, tells the supervisor *when* to look at a
// candidate CRD. It never decides *what* the CRD's state is: metadata
// carries neither spec nor status, so each candidate has its own worker
// that GETs the full CRD by its compiled name through the uncached
// reader and checks the amendment's A1 Ready conditions. Workers retry
// failed reads with their own backoff, so one slow or failing candidate
// never holds up another, and they only publish observations; runners,
// health and cleanup change solely in the supervisor's serialized
// apply().
//
// Each observation names the CRD generation (its UID) it is about.
// Only a full read decides a state: an informer DELETE event, a missing
// event, a read error, a RESTMapper error or a failing watch is never
// evidence of removal. Removed is published only for a generation the
// worker had published as Ready or Unservable, on a confirmed NotFound
// or deletionTimestamp; a new UID is the old generation's removal
// followed by a new generation.

// +kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,verbs=get;list;watch

var (
	crdGVK = schema.GroupVersionKind{Group: "apiextensions.k8s.io", Version: "v1", Kind: "CustomResourceDefinition"}
	crdGVR = schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}
)

// crdReadTimeout bounds one full-CRD GET.
const crdReadTimeout = 30 * time.Second

// CRDDiscoveryKind is the aggregate, non-workload discovery pseudo-kind (A6).
const CRDDiscoveryKind = "CRDDiscovery"

// crdState is a candidate CRD's state per amendment A1.
type crdState int

const (
	// crdStateUnknown: never confirmed Ready by this supervisor.
	crdStateUnknown crdState = iota
	// crdStateReady: Established, serving the compiled GVK, namespaced, and
	// resolvable through the RESTMapper.
	crdStateReady
	// crdStateUnservable: was Ready, still exists, now fails a Ready
	// condition.
	crdStateUnservable
	// crdStateRemoved: was Ready or Unservable; a full read found it gone.
	crdStateRemoved
)

func (c crdState) String() string {
	return [...]string{"Unknown", "Ready", "Unservable", "Removed"}[c]
}

// crdObservation is what discovery last confirmed about one candidate.
type crdObservation struct {
	state crdState
	// uid is the CRD generation the state is about.
	uid types.UID
	// reason says why an Unservable CRD fails A1 (verbatim for the
	// Degraded message).
	reason string
	// late: a Ready CRD was installed or re-created after the
	// supervisor started, as far as this supervisor's own reads can tell
	// (see crdWorker.isLate). Its first sync announces WatchStarted
	// instead of WatchRecovered (amendment A4).
	late bool
	// lagErr/lagSeq: the CRD is Ready but the RESTMapper has not
	// resolved its GVK for longer than CacheSyncTimeout. lagSeq grows
	// on each such report so apply() acts on it once.
	lagErr error
	lagSeq int
}

// crdWorker reads one candidate's CRD whenever it is kicked.
type crdWorker struct {
	w    ThirdPartyWatch
	kick chan struct{}

	// Owned by the worker goroutine. state and uid are what this worker
	// last published as Ready, Unservable or Removed.
	state crdState
	uid   types.UID
	// sawAbsent: a successful read found no CRD (NotFound), or a
	// generation this worker tracked went away, before the CRD now
	// becoming Ready; that CRD was created after the supervisor started.
	sawAbsent bool
	// firstUID: the UID of the CRD object as first found by a successful
	// read, Ready or not. A CRD that becomes Ready under another UID was
	// re-created after that read.
	firstUID     types.UID
	loggedAbsent bool
	lagSince     time.Time
	lagSeq       int
	lagReported  bool
}

// isLate reports whether a CRD now Ready with uid counts as installed
// after startup (amendment A4: WatchStarted). Only evidence from this
// supervisor's successful reads counts. A CRD that already existed at
// the first successful read — even if not yet Established — keeps
// WatchRecovered; so does one whose first reads all failed, since its
// installation time is then unknown.
func (k *crdWorker) isLate(uid types.UID) bool {
	return k.sawAbsent || (k.firstUID != "" && k.firstUID != uid)
}

func (k *crdWorker) poke() {
	select {
	case k.kick <- struct{}{}:
	default: // a read is already due; it will see the latest state
	}
}

// startDiscovery builds the shared CRD metadata informer and starts one
// worker per candidate, all under the supervisor's wait group. Each
// worker reads its CRD once straight away (the startup read), and again
// on every informer event for its name, and once more after the
// informer first syncs (to cover a change between the startup read and
// the informer's initial list).
func (s *WatchSupervisor) startDiscovery(ctx context.Context) error {
	if s.CRDReader == nil {
		s.CRDReader = s.APIReader
	}
	log := ctrl.Log.WithName("watch-supervisor").WithName("crd-discovery")

	workers := make([]*crdWorker, 0, len(s.Watches))
	byName := make(map[string]*crdWorker, len(s.Watches))
	s.crdWorkers = make(map[string]*crdWorker, len(s.Watches))
	for _, w := range s.Watches {
		k := &crdWorker{w: w, kick: make(chan struct{}, 1)}
		workers = append(workers, k)
		byName[w.CRD] = k
		s.crdWorkers[w.Name] = k
	}

	// client-go's metadata informer, not a controller-runtime metadata
	// cache: controller-runtime wraps metadata watches in watch.Filter
	// (its GVK fixup), whose goroutine blocks forever on an unread event
	// once the informer stops, leaking one goroutine per shutdown (found
	// by the envtest goleak check). Discovery needs only object names, so
	// the GVK fixup buys nothing here.
	mc, err := metadata.NewForConfig(s.Config)
	if err != nil {
		return fmt.Errorf("CRD discovery client: %w", err)
	}
	// A watch-error handler cannot observe recovery if the CRD list is
	// empty and no more events arrive. Successful watch establishment
	// must clear informer errors even without a candidate event (A6).
	list := func(ctx context.Context, opts metav1.ListOptions) (runtime.Object, error) {
		obj, err := mc.Resource(crdGVR).Namespace("").List(ctx, opts)
		if err != nil && ctx.Err() == nil {
			s.reportDiscoveryInformer(err)
		}
		return obj, err
	}
	watchCRDs := func(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
		stream, err := mc.Resource(crdGVR).Namespace("").Watch(ctx, opts)
		if ctx.Err() == nil {
			s.reportDiscoveryInformer(err)
		}
		return stream, err
	}
	lw := &toolscache.ListWatch{
		ListWithContextFunc: list,
		WatchFuncWithContext: watchCRDs,
	}
	inf := toolscache.NewSharedIndexInformer(
		toolscache.ToListWatcherWithWatchListSemantics(lw, mc),
		&metav1.PartialObjectMetadata{}, 0, toolscache.Indexers{})
	if err := inf.SetTransform(stripCRDMetadata); err != nil {
		return fmt.Errorf("CRD discovery transform: %w", err)
	}
	if err := inf.SetWatchErrorHandlerWithContext(func(_ context.Context, _ *toolscache.Reflector, err error) {
		// Discovery-wide and never evidence of absence: running
		// watches are untouched, the informer retries on its own.
		if err != nil {
			s.reportDiscoveryInformer(err)
			log.Info("CRD informer list/watch failed; discovery of newly installed CRDs is paused until it recovers", "err", err.Error())
		}
	}); err != nil {
		return fmt.Errorf("CRD discovery error handler: %w", err)
	}
	enqueue := func(o interface{}) {
		if name := crdNameOf(o); name != "" {
			if k, ok := byName[name]; ok {
				k.poke()
			}
		}
	}
	if _, err := inf.AddEventHandler(toolscache.ResourceEventHandlerFuncs{
		AddFunc:    enqueue,
		UpdateFunc: func(_, n interface{}) { enqueue(n) },
		DeleteFunc: enqueue, // only a reason to read; never a removal by itself
	}); err != nil {
		return fmt.Errorf("CRD discovery handler: %w", err)
	}

	for _, k := range workers {
		k.poke() // startup read
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.runCRDWorker(ctx, k)
		}()
	}
	s.wg.Add(2)
	go func() {
		defer s.wg.Done()
		inf.RunWithContext(ctx)
	}()
	go func() {
		defer s.wg.Done()
		// A stalled initial sync must become visible as Degraded (A6).
		// Keep waiting under the manager context so a later sync clears
		// the failure without restarting the supervisor.
		syncCtx, stop := context.WithTimeout(ctx, s.CacheSyncTimeout)
		synced := toolscache.WaitForCacheSync(syncCtx.Done(), inf.HasSynced)
		stop()
		if !synced && ctx.Err() == nil {
			s.reportDiscoverySyncFailure(fmt.Errorf("CRD metadata informer did not sync within %s", s.CacheSyncTimeout))
			synced = toolscache.WaitForCacheSync(ctx.Done(), inf.HasSynced)
		}
		if synced {
			s.reportDiscoverySynced()
			for _, k := range workers {
				k.poke()
			}
		}
	}()
	return nil
}

// runCRDWorker reads its candidate's CRD on each kick, and retries a
// failed or inconclusive read with its own capped backoff. No timer runs
// otherwise: discovery is event-driven.
func (s *WatchSupervisor) runCRDWorker(ctx context.Context, k *crdWorker) {
	log := ctrl.Log.WithName("watch-supervisor").WithValues("kind", k.w.Name, "crd", k.w.CRD)
	backoff := s.InitialBackoff
	var timer *time.Timer
	var retry <-chan time.Time
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-k.kick:
		case <-retry:
		}
		if timer != nil {
			timer.Stop()
			timer, retry = nil, nil
		}
		if !s.readCRD(ctx, k, log) {
			backoff = s.InitialBackoff
			continue
		}
		if ctx.Err() != nil {
			return
		}
		timer = time.NewTimer(backoff)
		retry = timer.C
		backoff = nextBackoff(backoff, s.MaxBackoff)
	}
}

// readCRD performs one full read and classification; it returns true
// when the read should be retried with backoff.
func (s *WatchSupervisor) readCRD(ctx context.Context, k *crdWorker, log logr.Logger) bool {
	w := k.w
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(crdGVK)
	rctx, cancel := context.WithTimeout(ctx, crdReadTimeout)
	err := s.CRDReader.Get(rctx, client.ObjectKey{Name: w.CRD}, u)
	cancel()
	if ctx.Err() == nil {
		if apierrors.IsNotFound(err) {
			// Authoritative absence is a successful observation.
			s.reportCandidateCRDRead(w.Name, nil)
		} else {
			s.reportCandidateCRDRead(w.Name, err)
		}
	}
	switch {
	case apierrors.IsNotFound(err):
		k.sawAbsent = true
		if k.state == crdStateReady || k.state == crdStateUnservable {
			// Row 8: confirmed by a full read.
			s.removeObserved(k, log, "CRD not found")
			return false
		}
		if !k.loggedAbsent {
			k.loggedAbsent = true
			log.Info("CRD not installed; its watch starts when the CRD is installed and Established")
		}
		return false
	case err != nil:
		if ctx.Err() == nil {
			// Not evidence of anything (A1): state unchanged, retried.
			log.Info("CRD read failed; state unchanged, will retry", "err", err.Error())
		}
		return true
	}

	uid := u.GetUID()
	if k.firstUID == "" {
		k.firstUID = uid
	}
	tracked := k.state == crdStateReady || k.state == crdStateUnservable
	if tracked && uid != k.uid {
		// Row 9: a new generation under the same name. The tracked
		// generation is gone; the new one is classified below.
		s.removeObserved(k, log, "CRD re-created under a new UID")
		tracked = false
	}
	if u.GetDeletionTimestamp() != nil {
		// A1: deletion has begun and cannot be undone.
		if tracked {
			s.removeObserved(k, log, "CRD is being deleted")
		}
		return false
	}

	ok, reason := crdReady(u, w.GVK)
	if !ok {
		if k.state == crdStateReady {
			// Row 5: same generation, no longer Ready.
			k.state = crdStateUnservable
			log.Info("CRD no longer Ready; the watch stops and its AIBOMs are kept", "reason", reason)
			s.publish(w.Name, crdObservation{state: crdStateUnservable, uid: uid, reason: reason, lagSeq: k.lagSeq})
			return false
		}
		if k.state != crdStateUnservable {
			log.Info("CRD present but not Ready yet; waiting for its next change", "reason", reason)
		}
		return false
	}
	if k.state == crdStateReady {
		// Row 4: an update that stays Ready (labels, caBundle,
		// conversion settings) changes nothing.
		return false
	}

	// Becoming Ready (rows 2, 3, 6, 9). The existing dynamic RESTMapper
	// reloads the group version on NoMatch (amendment A4), so a
	// successful lookup here means every later client lookup resolves
	// too. NoMatch right after Established is discovery lag: retry, and
	// surface it only once it outlasts CacheSyncTimeout.
	gk := schema.GroupKind{Group: w.GVK.Group, Kind: w.GVK.Kind}
	if _, merr := s.Mapper.RESTMapping(gk, w.GVK.Version); merr != nil {
		if k.lagSince.IsZero() {
			k.lagSince = time.Now()
		}
		if time.Since(k.lagSince) >= s.CacheSyncTimeout && !k.lagReported {
			k.lagReported = true
			k.lagSeq++
			s.publishLag(w.Name, fmt.Errorf("CRD %s is Established and serves %s, but the API server's discovery still does not resolve %s: %w",
				w.CRD, w.GVK.Version, w.GVK.String(), merr), k.lagSeq)
		}
		log.Info("CRD Ready but not yet resolvable; will retry", "err", merr.Error())
		return true
	}
	k.lagSince, k.lagReported = time.Time{}, false
	// Row 6 (the same generation recovering) is never late; otherwise
	// isLate decides between WatchStarted and WatchRecovered.
	late := k.state != crdStateUnservable && k.isLate(uid)
	k.state, k.uid = crdStateReady, uid
	log.Info("CRD Ready; handing the kind to the supervisor", "lateInstall", late, "uid", string(uid))
	s.publish(w.Name, crdObservation{state: crdStateReady, uid: uid, late: late, lagSeq: k.lagSeq})
	return false
}

// removeObserved publishes the removal of the generation this worker
// tracked (A2 rows 8 and 9) and forgets it.
func (s *WatchSupervisor) removeObserved(k *crdWorker, log logr.Logger, why string) {
	log.Info("CRD generation removed; the watch stops and its AIBOMs are swept", "why", why, "uid", string(k.uid))
	s.publish(k.w.Name, crdObservation{state: crdStateRemoved, uid: k.uid, lagSeq: k.lagSeq})
	k.state, k.sawAbsent = crdStateRemoved, true
	k.lagSince, k.lagReported = time.Time{}, false
}

// crdStateReady checks the amendment's A1 Ready conditions for gvk on a full
// CRD object. The reason names the first unmet condition.
func crdReady(u *unstructured.Unstructured, gvk schema.GroupVersionKind) (bool, string) {
	if u.GetDeletionTimestamp() != nil {
		return false, "being deleted"
	}
	if g, _, _ := unstructured.NestedString(u.Object, "spec", "group"); g != gvk.Group {
		return false, fmt.Sprintf("spec.group is %q, want %q", g, gvk.Group)
	}
	if k, _, _ := unstructured.NestedString(u.Object, "spec", "names", "kind"); k != gvk.Kind {
		return false, fmt.Sprintf("spec.names.kind is %q, want %q", k, gvk.Kind)
	}
	if sc, _, _ := unstructured.NestedString(u.Object, "spec", "scope"); sc != "Namespaced" {
		return false, fmt.Sprintf("spec.scope is %q, want Namespaced", sc)
	}
	versions, _, _ := unstructured.NestedSlice(u.Object, "spec", "versions")
	served := false
	for _, v := range versions {
		m, ok := v.(map[string]interface{})
		if !ok {
			continue
		}
		if name, _ := m["name"].(string); name == gvk.Version {
			served, _ = m["served"].(bool)
			break
		}
	}
	if !served {
		return false, fmt.Sprintf("version %s is not served", gvk.Version)
	}
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, c := range conds {
		m, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		if t, _ := m["type"].(string); t == "Established" {
			if st, _ := m["status"].(string); st == "True" {
				return true, ""
			}
			break
		}
	}
	return false, "Established is not True"
}

// crdNameOf extracts the object name from an informer event payload,
// including delete tombstones.
func crdNameOf(o interface{}) string {
	if t, ok := o.(toolscache.DeletedFinalStateUnknown); ok {
		o = t.Obj
	}
	m, err := meta.Accessor(o)
	if err != nil {
		return ""
	}
	return m.GetName()
}

// stripCRDMetadata drops the fields of CRD metadata discovery never
// reads (managedFields, and annotations, which can hold a large
// last-applied copy of the CRD) before they reach the informer's store.
func stripCRDMetadata(o interface{}) (interface{}, error) {
	if m, ok := o.(*metav1.PartialObjectMetadata); ok {
		m.ManagedFields = nil
		m.Annotations = nil
	}
	return o, nil
}

// publish records an observation and wakes apply().
func (s *WatchSupervisor) publish(kind string, obs crdObservation) {
	s.crd.Lock()
	s.crd.obs[kind] = obs
	s.crd.Unlock()
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// runnerError records an error from a kind's running watch (probe,
// list/watch, sync) and reports whether it was a transition into the
// unhealthy state. An absence-shaped error (NotFound for the resource,
// or NoMatch) is what a deleted or no-longer-served CRD looks like to
// a watch before discovery has read the CRD: it is never evidence of
// anything by itself (amendment A1). It wakes the kind's discovery
// worker, so a removal or unservable CRD is handled as such (rows 5,
// 8, 9: WatchDisabled or a reasoned Degraded, not a bare
// WatchUnhealthy), and it counts as a watch failure only once the
// streak outlasts CacheSyncTimeout with the CRD still Ready. Every
// other error is recorded at once, as before.
func (s *WatchSupervisor) runnerError(w ThirdPartyWatch, err error) bool {
	if err == nil {
		return false
	}
	if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
		if k, ok := s.crdWorkers[w.Name]; ok {
			k.poke()
		}
		s.absentSince.Lock()
		since, ok := s.absentSince.at[w.Name]
		if !ok {
			since = time.Now()
			s.absentSince.at[w.Name] = since
		}
		s.absentSince.Unlock()
		if time.Since(since) < s.CacheSyncTimeout {
			return false
		}
	} else {
		s.clearAbsent(w.Name)
	}
	return s.Health.MarkUnhealthy(w.Name, err)
}

// clearAbsent ends a kind's absence-shaped error streak.
func (s *WatchSupervisor) clearAbsent(kind string) {
	s.absentSince.Lock()
	delete(s.absentSince.at, kind)
	s.absentSince.Unlock()
}

// setRunnerActive tells the allowlist fan-out whether a supervised kind
// has a running controller to deliver to (amendment A5: kinds without
// one are skipped).
func (s *WatchSupervisor) setRunnerActive(kind string, active bool) {
	if s.Fanout == nil {
		return
	}
	for _, w := range s.Watches {
		if w.Name == kind {
			s.Fanout.SetActive(schema.GroupKind{Group: w.GVK.Group, Kind: w.GVK.Kind}, active)
			return
		}
	}
}

// publishLag records a mapping-lag report on the candidate's current
// observation without changing its state, and wakes apply().
func (s *WatchSupervisor) publishLag(kind string, err error, seq int) {
	s.crd.Lock()
	obs := s.crd.obs[kind]
	obs.lagErr, obs.lagSeq = err, seq
	s.crd.obs[kind] = obs
	s.crd.Unlock()
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

func (s *WatchSupervisor) observation(kind string) crdObservation {
	s.crd.Lock()
	defer s.crd.Unlock()
	return s.crd.obs[kind]
}

// activate gives a kind whose CRD is confirmed Ready its health entry
// and TrackedKinds membership, once (amendment A2 rows 2-3: register,
// Add first), and reports whether this call did it. A new owner kind
// triggers one allowlist fan-out so workloads reconciled as roots
// before it was tracked are absorbed (Design 006 §4).
func (s *WatchSupervisor) activate(w ThirdPartyWatch, gk schema.GroupKind) bool {
	if s.activated[w.Name] {
		return false
	}
	s.activated[w.Name] = true
	s.Health.Register(w.Name)
	if s.Tracked != nil && s.Tracked.Add(gk) {
		s.requestFanout()
	}
	return true
}

func (s *WatchSupervisor) requestFanout() {
	if s.Fanout == nil {
		return
	}
	select {
	case s.fanoutReq <- struct{}{}:
	default: // one is already queued and will see the new membership
	}
}

// runFanout serializes and coalesces supervisor-requested fan-outs
// under the supervisor's root context (amendment A5).
func (s *WatchSupervisor) runFanout(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.fanoutReq:
			s.Fanout.TriggerContext(ctx, s.getDesired())
		}
	}
}

func (s *WatchSupervisor) setPendingStart(kind string, v bool) {
	s.pendingStart.Lock()
	defer s.pendingStart.Unlock()
	if v {
		s.pendingStart.kinds[kind] = true
	} else {
		delete(s.pendingStart.kinds, kind)
	}
}

func (s *WatchSupervisor) isPendingStart(kind string) bool {
	s.pendingStart.Lock()
	defer s.pendingStart.Unlock()
	return s.pendingStart.kinds[kind]
}

// takePendingStart reports and clears a kind's pending WatchStarted.
func (s *WatchSupervisor) takePendingStart(kind string) bool {
	s.pendingStart.Lock()
	defer s.pendingStart.Unlock()
	v := s.pendingStart.kinds[kind]
	delete(s.pendingStart.kinds, kind)
	return v
}

// initDiscoveryHealth registers one pseudo-kind; individual candidate
// errors never create additional health entries.
func (s *WatchSupervisor) initDiscoveryHealth() {
	s.discovery.Lock()
	defer s.discovery.Unlock()
	s.discovery.enabled = true
	s.discovery.synced = false
	s.discovery.informerOnline = false
	s.discovery.informerErr = ""
	s.discovery.syncErr = ""
	s.discovery.candidateErr = make(map[string]string)
	s.discovery.seen = make(map[string]bool)
	s.Health.Register(CRDDiscoveryKind)
}

// An informer error is cleared only by a successfully established watch,
// not merely by an empty LIST. This works even with zero CRD events.
func (s *WatchSupervisor) reportDiscoveryInformer(err error) {
	s.discovery.Lock()
	if !s.discovery.enabled {
		s.discovery.Unlock()
		return
	}
	oldErr, oldOnline := s.discovery.informerErr, s.discovery.informerOnline
	if err == nil {
		s.discovery.informerErr = ""
		s.discovery.informerOnline = true
	} else {
		s.discovery.informerErr = err.Error()
		s.discovery.informerOnline = false
	}
	changed := oldErr != s.discovery.informerErr || oldOnline != s.discovery.informerOnline
	s.discovery.Unlock()
	if changed {
		s.wakeDiscoveryHealth()
	}
}

// reportDiscoverySyncFailure flags a stalled initial informer sync.
// It is cleared only once the informer actually synchronizes.
func (s *WatchSupervisor) reportDiscoverySyncFailure(err error) {
	if err == nil {
		return
	}
	s.discovery.Lock()
	if !s.discovery.enabled || s.discovery.synced {
		s.discovery.Unlock()
		return
	}
	changed := s.discovery.syncErr != err.Error()
	s.discovery.syncErr = err.Error()
	s.discovery.Unlock()
	if changed {
		s.wakeDiscoveryHealth()
	}
}

func (s *WatchSupervisor) reportDiscoverySynced() {
	s.discovery.Lock()
	if !s.discovery.enabled {
		s.discovery.Unlock()
		return
	}
	changed := !s.discovery.synced || s.discovery.syncErr != ""
	s.discovery.synced = true
	s.discovery.syncErr = ""
	s.discovery.Unlock()
	if changed {
		s.wakeDiscoveryHealth()
	}
}

// A successful GET or confirmed NotFound clears one candidate's error.
// Failed reads are retried independently by the existing workers.
func (s *WatchSupervisor) reportCandidateCRDRead(kind string, err error) {
	s.discovery.Lock()
	if !s.discovery.enabled {
		s.discovery.Unlock()
		return
	}
	old, seen := s.discovery.candidateErr[kind], s.discovery.seen[kind]
	if err == nil {
		delete(s.discovery.candidateErr, kind)
	} else {
		s.discovery.candidateErr[kind] = err.Error()
	}
	s.discovery.seen[kind] = true
	changed := !seen || old != s.discovery.candidateErr[kind]
	s.discovery.Unlock()
	if changed {
		s.wakeDiscoveryHealth()
	}
}

func (s *WatchSupervisor) wakeDiscoveryHealth() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// Called only by the serialized supervisor apply loop (A5). Report one
// aggregate failure for any informer or candidate error; recover only
// after all failures clear and the informer is synced and connected.
func (s *WatchSupervisor) applyDiscoveryHealth() {
	s.discovery.Lock()
	if !s.discovery.enabled {
		s.discovery.Unlock()
		return
	}
	var failures []string
	if s.discovery.informerErr != "" {
		failures = append(failures, "informer: "+s.discovery.informerErr)
	}
	if s.discovery.syncErr != "" {
		failures = append(failures, "sync: "+s.discovery.syncErr)
	}
	kinds := make([]string, 0, len(s.discovery.candidateErr))
	for kind := range s.discovery.candidateErr {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	for _, kind := range kinds {
		failures = append(failures, kind+": "+s.discovery.candidateErr[kind])
	}
	ready := s.discovery.synced && s.discovery.informerOnline
	for _, w := range s.Watches {
		if !s.discovery.seen[w.Name] {
			ready = false
		}
	}
	s.discovery.Unlock()
	if len(failures) != 0 {
		msg := "CRD discovery failed: " + strings.Join(failures, "; ")
		if st, ok := s.Health.Status(CRDDiscoveryKind); !ok || !st.Degraded() || st.LastError != msg {
			s.Health.MarkUnhealthy(CRDDiscoveryKind, fmt.Errorf("%s", msg))
		}
	} else if ready && !s.Health.IsHealthy(CRDDiscoveryKind) {
		s.Health.MarkHealthy(CRDDiscoveryKind)
	}
}
