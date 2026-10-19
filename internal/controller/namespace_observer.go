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
	"fmt"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"

	"github.com/GoogleCloudPlatform/k8s-aibom/internal/scraper"
)

// EventReasonNamespaceObserved is the Normal event emitted once on an
// opted-in namespace after the controller's first pass over it, with
// the number of workloads considered and matched (#106). Conservative
// detection makes a quiet namespace the common first experience; the
// event is what tells "seen, nothing recognized" apart from a
// controller that never looked.
const EventReasonNamespaceObserved = "NamespaceObserved"

const (
	// DefaultNamespaceObservationSettle is how long a namespace must go
	// without a newly considered workload before its event is emitted.
	DefaultNamespaceObservationSettle = 10 * time.Second

	// DefaultNamespaceObservationMaxWait caps the wait from the first
	// considered workload, so a namespace with steady churn still gets
	// its event.
	DefaultNamespaceObservationMaxWait = 60 * time.Second
)

// NamespaceObserver counts, per opted-in namespace, the workloads the
// reconcilers considered (reached detection) and matched (produced an
// AIBOM), and emits EventReasonNamespaceObserved once the first pass
// has settled. Rolled-up workloads and kinds excluded by
// spec.discovery.workloadKinds are not counted: neither reached
// detection.
//
// State is in memory: a restarted controller announces each namespace
// again, like the ControllerStarting event. A namespace that stops
// matching the selector is forgotten, so opting it in again announces
// it again; so does a namespace deleted and recreated under the same
// name (new UID).
//
// One observer is shared by every WorkloadReconciler copy; methods are
// safe for concurrent use.
type NamespaceObserver struct {
	recorder record.EventRecorder
	settle   time.Duration
	maxWait  time.Duration

	mu         sync.Mutex
	namespaces map[string]*namespaceObservation
}

type namespaceObservation struct {
	uid       types.UID
	first     time.Time
	timer     *time.Timer
	announced bool
	// workloads maps "group/Kind/name" to whether it matched. Dropped
	// once announced; only the flag is kept after that.
	workloads map[string]bool
}

// NewNamespaceObserver returns an observer that emits through recorder
// with the default settle and max-wait durations.
func NewNamespaceObserver(recorder record.EventRecorder) *NamespaceObserver {
	return newNamespaceObserver(recorder, DefaultNamespaceObservationSettle, DefaultNamespaceObservationMaxWait)
}

func newNamespaceObserver(recorder record.EventRecorder, settle, maxWait time.Duration) *NamespaceObserver {
	return &NamespaceObserver{
		recorder:   recorder,
		settle:     settle,
		maxWait:    maxWait,
		namespaces: map[string]*namespaceObservation{},
	}
}

// Considered records that a workload in an opted-in namespace reached
// detection, and whether it matched. Nil-safe.
func (o *NamespaceObserver) Considered(ns *corev1.Namespace, workload string, matched bool) {
	if o == nil || o.recorder == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()

	obs := o.namespaces[ns.Name]
	if obs != nil && obs.uid != ns.UID {
		// Same name, new namespace: start over.
		if obs.timer != nil {
			obs.timer.Stop()
		}
		obs = nil
	}
	if obs == nil {
		obs = &namespaceObservation{uid: ns.UID, first: time.Now(), workloads: map[string]bool{}}
		o.namespaces[ns.Name] = obs
		name, uid := ns.Name, ns.UID
		obs.timer = time.AfterFunc(o.settle, func() { o.announce(name, uid) })
	}
	if obs.announced {
		return
	}
	_, seen := obs.workloads[workload]
	obs.workloads[workload] = matched
	if !seen {
		// A new workload pushes the event back by one settle period,
		// never past maxWait from the first one.
		wait := o.settle
		if remaining := time.Until(obs.first.Add(o.maxWait)); remaining < wait {
			wait = max(remaining, 0)
		}
		obs.timer.Reset(wait)
	}
}

// Forget drops what is known about a namespace, so it is announced
// again if it is opted in later. Called when a workload's namespace no
// longer matches the selector. Nil-safe.
func (o *NamespaceObserver) Forget(namespace string) {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if obs := o.namespaces[namespace]; obs != nil {
		if obs.timer != nil {
			obs.timer.Stop()
		}
		delete(o.namespaces, namespace)
	}
}

func (o *NamespaceObserver) announce(namespace string, uid types.UID) {
	o.mu.Lock()
	obs := o.namespaces[namespace]
	if obs == nil || obs.uid != uid || obs.announced {
		o.mu.Unlock()
		return
	}
	considered, matched := len(obs.workloads), 0
	for _, m := range obs.workloads {
		if m {
			matched++
		}
	}
	obs.announced = true
	obs.workloads = nil
	o.mu.Unlock()

	// Namespace is set to the namespace itself so the Event is stored
	// there and shows up in `kubectl get events -n <ns>`; with an empty
	// namespace a cluster-scoped reference lands in "default".
	ref := &corev1.ObjectReference{
		APIVersion: "v1",
		Kind:       "Namespace",
		Name:       namespace,
		Namespace:  namespace,
		UID:        uid,
	}
	o.recorder.Event(ref, corev1.EventTypeNormal, EventReasonNamespaceObserved, namespaceObservedMessage(considered, matched))
}

// observedWorkloadKey identifies a workload within its namespace.
func observedWorkloadKey(w scraper.Workload) string {
	return w.Kind.Group + "/" + w.Kind.Kind + "/" + w.Name
}

func namespaceObservedMessage(considered, matched int) string {
	return fmt.Sprintf("k8s-aibom is inventorying this namespace: %d workload(s) considered, %d matched an AI workload signal "+
		"and have an AIBOM, %d matched none. Detection is conservative: a workload whose images match no runtime pattern "+
		"gets no AIBOM (see docs/scraper-heuristics.md).",
		considered, matched, considered-matched)
}
