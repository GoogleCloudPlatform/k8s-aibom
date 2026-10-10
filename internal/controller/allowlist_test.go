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
	"errors"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/GoogleCloudPlatform/k8s-aibom/internal/config"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/metrics"
)

func TestWatchHealth_Disabled(t *testing.T) {
	h := NewWatchHealth()
	var got []WatchTransition
	h.Subscribe(func(tr WatchTransition) { got = append(got, tr) })
	h.Register("LeaderWorkerSet")

	// A degraded kind that the operator switches off leaves the
	// Degraded set: nothing is failing any more.
	h.MarkUnhealthy("LeaderWorkerSet", errors.New("conversion webhook: connection refused"))
	if len(h.Degraded()) != 1 {
		t.Fatal("precondition: kind is degraded")
	}
	if !h.MarkDisabled("LeaderWorkerSet") {
		t.Error("first MarkDisabled must transition")
	}
	if h.MarkDisabled("LeaderWorkerSet") {
		t.Error("repeated MarkDisabled must not transition again")
	}
	st, _ := h.Status("LeaderWorkerSet")
	if !st.Disabled || st.Healthy || st.Degraded() || st.LastError != "" || st.Failures != 0 {
		t.Errorf("disabled status: %+v", st)
	}
	if len(h.Degraded()) != 0 {
		t.Error("a disabled kind must not be Degraded")
	}
	if testutil.ToFloat64(metrics.WatchHealthy.WithLabelValues("LeaderWorkerSet")) != 0 {
		t.Error("gauge must be 0 while disabled")
	}
	last := got[len(got)-1]
	if !last.Disabled || last.Healthy || !strings.Contains(last.Message, "disabled by spec.discovery.workloadKinds") {
		t.Errorf("disabled transition: %+v", last)
	}

	// Re-enabling goes back through starting (no transition), then the
	// first sync is the usual healthy transition.
	n := len(got)
	h.MarkStarting("LeaderWorkerSet")
	if len(got) != n {
		t.Error("MarkStarting must not notify")
	}
	st, _ = h.Status("LeaderWorkerSet")
	if st.Disabled || st.Healthy || st.Degraded() {
		t.Errorf("starting status after re-enable: %+v", st)
	}
	if !h.MarkHealthy("LeaderWorkerSet") {
		t.Error("first sync after re-enable must transition to healthy")
	}
	st, _ = h.Status("LeaderWorkerSet")
	if st.Disabled || !st.Healthy {
		t.Errorf("healthy status must clear Disabled: %+v", st)
	}
	// And an error on a disabled kind clears Disabled too (it cannot
	// happen while stopped, but the state must stay consistent).
	h.MarkDisabled("LeaderWorkerSet")
	h.MarkUnhealthy("LeaderWorkerSet", errors.New("x"))
	if st, _ = h.Status("LeaderWorkerSet"); st.Disabled || !st.Degraded() {
		t.Errorf("unhealthy must clear Disabled: %+v", st)
	}
}

func TestTrackedKinds_OwnsFollowsAllowlist(t *testing.T) {
	lws := schema.GroupKind{Group: "leaderworkerset.x-k8s.io", Kind: "LeaderWorkerSet"}
	sts := schema.GroupKind{Group: "apps", Kind: "StatefulSet"}
	rs := schema.GroupKind{Group: "apps", Kind: "ReplicaSet"}

	tracked := NewTrackedKinds()
	tracked.Add(lws)
	tracked.Add(sts)
	// No store: Owns is Has.
	if !tracked.Owns(lws) || !tracked.Owns(sts) || tracked.Owns(rs) {
		t.Fatal("without a store every tracked kind owns, untracked kinds never do")
	}

	store := config.NewStore(config.DefaultSnapshot())
	tracked.Follow(store)
	if !tracked.Owns(lws) {
		t.Fatal("unrestricted allowlist: tracked kind owns")
	}
	snap := config.DefaultSnapshot()
	snap.WorkloadKinds = config.NewWorkloadKindSet(sts)
	store.Store(snap)
	if tracked.Owns(lws) {
		t.Error("a kind removed from the allowlist must stop owning (its children re-root)")
	}
	if !tracked.Has(lws) {
		t.Error("structural membership must not follow the allowlist (the descendant walk still lists it)")
	}
	if !tracked.Owns(sts) {
		t.Error("an allowed tracked kind owns")
	}
	if tracked.Owns(rs) {
		t.Error("an allowed-but-untracked kind never owns")
	}
}
