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
	"time"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/metadata"
	"k8s.io/client-go/metadata/metadatainformer"
	toolscache "k8s.io/client-go/tools/cache"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Late CRD discovery (Design 004 amendment, #226; Phase A: A2 rows 1-4
// and 10).
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
// Phase A acts only on transitions into Ready. A CRD that later stops
// being Ready, is deleted or is re-created under a new UID is logged and
// otherwise left as it is (the running watch degrades through the
// existing probe); stopping and cleanup for those cases are Phase B.

// +kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,verbs=get;list;watch

var (
	crdGVK = schema.GroupVersionKind{Group: "apiextensions.k8s.io", Version: "v1", Kind: "CustomResourceDefinition"}
	crdGVR = schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}
)

// crdReadTimeout bounds one full-CRD GET.
const crdReadTimeout = 30 * time.Second

// crdObservation is what discovery last confirmed about one candidate.
type crdObservation struct {
	// ready: the CRD met every A1 Ready condition and the RESTMapper
	// resolves the compiled GVK. Phase A never clears it.
	ready bool
	// late: the CRD was installed or re-created after the supervisor
	// started, as far as this supervisor's own reads can tell (see
	// crdWorker.isLate). Its first sync announces WatchStarted instead of
	// WatchRecovered (amendment A4).
	late bool
	uid  types.UID
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

	// Owned by the worker goroutine.
	ready bool
	// sawAbsent: a successful read found no CRD (NotFound) before it was
	// first Ready, so the CRD that later becomes Ready was created after
	// the supervisor started.
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
	for _, w := range s.Watches {
		k := &crdWorker{w: w, kick: make(chan struct{}, 1)}
		workers = append(workers, k)
		byName[w.CRD] = k
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
	inf := metadatainformer.NewFilteredMetadataInformer(mc, crdGVR, "", 0, toolscache.Indexers{}, nil).Informer()
	if err := inf.SetTransform(stripCRDMetadata); err != nil {
		return fmt.Errorf("CRD discovery transform: %w", err)
	}
	if err := inf.SetWatchErrorHandlerWithContext(func(_ context.Context, _ *toolscache.Reflector, err error) {
		// Discovery-wide and never evidence of absence: running
		// watches are untouched, the informer retries on its own.
		if err != nil {
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
		if toolscache.WaitForCacheSync(ctx.Done(), inf.HasSynced) {
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
	switch {
	case apierrors.IsNotFound(err):
		if k.ready {
			// Phase B (A2 row 8) owns removal; nothing is stopped or
			// swept here.
			log.Info("CRD no longer found; removal handling is not implemented in this release, the watch is left as is")
			return false
		}
		k.sawAbsent = true
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

	if k.firstUID == "" {
		k.firstUID = u.GetUID()
	}
	ok, reason := crdReady(u, w.GVK)
	if !ok {
		if k.ready {
			log.Info("CRD no longer Ready; unservable handling is not implemented in this release, the watch is left as is", "reason", reason)
			return false
		}
		log.Info("CRD present but not Ready yet; waiting for its next change", "reason", reason)
		return false
	}
	if k.ready {
		// Row 4: an update that stays Ready (labels, caBundle,
		// conversion settings) changes nothing.
		return false
	}

	// Newly Ready. The existing dynamic RESTMapper reloads the group
	// version on NoMatch (amendment A4), so a successful lookup here
	// means every later client lookup resolves too. NoMatch right after
	// Established is discovery lag: retry, and surface it only once it
	// outlasts CacheSyncTimeout.
	gk := schema.GroupKind{Group: w.GVK.Group, Kind: w.GVK.Kind}
	if _, merr := s.Mapper.RESTMapping(gk, w.GVK.Version); merr != nil {
		if k.lagSince.IsZero() {
			k.lagSince = time.Now()
		}
		if time.Since(k.lagSince) >= s.CacheSyncTimeout && !k.lagReported {
			k.lagReported = true
			k.lagSeq++
			s.publish(w.Name, crdObservation{
				lagErr: fmt.Errorf("CRD %s is Established and serves %s, but the API server's discovery still does not resolve %s: %w",
					w.CRD, w.GVK.Version, w.GVK.String(), merr),
				lagSeq: k.lagSeq,
			})
		}
		log.Info("CRD Ready but not yet resolvable; will retry", "err", merr.Error())
		return true
	}
	k.ready = true
	late := k.isLate(u.GetUID())
	log.Info("CRD Ready; handing the kind to the supervisor", "lateInstall", late, "uid", string(u.GetUID()))
	s.publish(w.Name, crdObservation{ready: true, late: late, uid: u.GetUID(), lagSeq: k.lagSeq})
	return false
}

// crdReady checks the amendment's A1 Ready conditions for gvk on a full
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
