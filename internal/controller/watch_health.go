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
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/GoogleCloudPlatform/k8s-aibom/internal/metrics"
)

// WatchStatus is the health of one third-party watch (Design 004).
type WatchStatus struct {
	Kind string
	// Healthy is true once the kind's informer has synced and no
	// list/watch error has been recorded since.
	Healthy bool
	// LastError is the most recent list/watch/probe error, verbatim
	// from the API server (it carries the conversion-webhook message
	// an operator needs). Empty when Healthy, and before the first
	// failure.
	LastError string
	// Since is when the current Healthy value was entered.
	Since time.Time
	// Failures counts errors recorded since the last healthy
	// transition.
	Failures int
}

// Degraded reports whether this status should surface as a Degraded
// reason: a kind that has recorded an error and has not recovered. A
// kind that is still starting (no error yet) is not degraded.
func (s WatchStatus) Degraded() bool { return !s.Healthy && s.LastError != "" }

// WatchTransition is delivered to subscribers on every health
// transition: starting/unhealthy → healthy, or healthy/starting →
// unhealthy. Repeated failures while already unhealthy do not
// transition (one event per outage, not per retry).
type WatchTransition struct {
	Kind    string
	Healthy bool
	Message string
}

// WatchHealth is the concurrency-safe registry of third-party watch
// health. The supervisor writes it; the AIBOMControllerConfig
// reconciler reads it for the Degraded condition; metrics mirror it.
type WatchHealth struct {
	mu    sync.Mutex
	kinds map[string]*WatchStatus
	subs  []func(WatchTransition)
	now   func() time.Time
}

// NewWatchHealth returns an empty registry.
func NewWatchHealth() *WatchHealth {
	return &WatchHealth{kinds: map[string]*WatchStatus{}, now: time.Now}
}

// Register declares a kind the supervisor will run. Until its first
// sync or first error it is "starting": neither healthy nor degraded.
func (h *WatchHealth) Register(kind string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.kinds[kind]; !ok {
		h.kinds[kind] = &WatchStatus{Kind: kind, Since: h.now()}
		metrics.WatchHealthy.WithLabelValues(kind).Set(0)
	}
}

// Subscribe adds a transition listener. Listeners are called
// synchronously under no lock and must not block.
func (h *WatchHealth) Subscribe(fn func(WatchTransition)) {
	h.mu.Lock()
	h.subs = append(h.subs, fn)
	h.mu.Unlock()
}

// MarkHealthy records a successful sync or probe. Returns true on a
// transition into the healthy state.
func (h *WatchHealth) MarkHealthy(kind string) bool {
	h.mu.Lock()
	s := h.get(kind)
	transition := !s.Healthy
	s.Healthy = true
	s.LastError = ""
	s.Failures = 0
	if transition {
		s.Since = h.now()
	}
	subs := h.subs
	h.mu.Unlock()
	metrics.WatchHealthy.WithLabelValues(kind).Set(1)
	if transition {
		h.notify(subs, WatchTransition{Kind: kind, Healthy: true,
			Message: fmt.Sprintf("%s watch is healthy", kind)})
	}
	return transition
}

// MarkUnhealthy records a list/watch/probe/start error. Returns true
// on a transition into the unhealthy state (first error of an outage).
func (h *WatchHealth) MarkUnhealthy(kind string, err error) bool {
	if err == nil {
		return false
	}
	h.mu.Lock()
	s := h.get(kind)
	transition := s.Healthy || s.LastError == ""
	s.Healthy = false
	s.LastError = err.Error()
	s.Failures++
	if transition {
		s.Since = h.now()
	}
	subs := h.subs
	h.mu.Unlock()
	metrics.WatchHealthy.WithLabelValues(kind).Set(0)
	metrics.WatchErrors.WithLabelValues(kind).Inc()
	if transition {
		h.notify(subs, WatchTransition{Kind: kind, Healthy: false,
			Message: fmt.Sprintf("%s watch is unhealthy: %s", kind, err.Error())})
	}
	return transition
}

// IsHealthy reports the current healthy flag for kind.
func (h *WatchHealth) IsHealthy(kind string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.kinds[kind]
	return ok && s.Healthy
}

// Status returns a copy of one kind's status.
func (h *WatchHealth) Status(kind string) (WatchStatus, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.kinds[kind]
	if !ok {
		return WatchStatus{}, false
	}
	return *s, true
}

// Degraded returns the kinds that should surface on the Degraded
// condition, sorted by kind for a deterministic message.
func (h *WatchHealth) Degraded() []WatchStatus {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []WatchStatus
	for _, s := range h.kinds {
		if s.Degraded() {
			out = append(out, *s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Kind < out[j].Kind })
	return out
}

func (h *WatchHealth) get(kind string) *WatchStatus {
	s, ok := h.kinds[kind]
	if !ok {
		s = &WatchStatus{Kind: kind, Since: h.now()}
		h.kinds[kind] = s
	}
	return s
}

func (h *WatchHealth) notify(subs []func(WatchTransition), t WatchTransition) {
	for _, fn := range subs {
		fn(t)
	}
}

// watchHealthMessage renders the Degraded message for unhealthy
// watches: one clause per kind with the verbatim API error, so the
// conversion-webhook failure (or whatever it is) is readable from
// kubectl describe without the controller logs.
func watchHealthMessage(degraded []WatchStatus) string {
	if len(degraded) == 0 {
		return ""
	}
	parts := make([]string, 0, len(degraded))
	for _, s := range degraded {
		parts = append(parts, fmt.Sprintf("%s (since %s): %s",
			s.Kind, s.Since.UTC().Format(time.RFC3339), s.LastError))
	}
	return "Third-party watch unhealthy; existing AIBOMs for these kinds are kept as last known, " +
		"new or changed workloads of these kinds are not inventoried until the watch recovers, " +
		"all other kinds are unaffected: " + strings.Join(parts, "; ")
}
