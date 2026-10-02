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
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/GoogleCloudPlatform/k8s-aibom/internal/metrics"
)

// One transition per outage: repeated failures while unhealthy do not
// re-notify; recovery notifies once; a kind that is starting (no error
// yet) is neither healthy nor degraded.
func TestWatchHealth_Transitions(t *testing.T) {
	h := NewWatchHealth()
	var got []WatchTransition
	h.Subscribe(func(tr WatchTransition) { got = append(got, tr) })

	h.Register("Dynamo")
	if st, _ := h.Status("Dynamo"); st.Healthy || st.Degraded() {
		t.Fatalf("starting kind must be neither healthy nor degraded: %+v", st)
	}
	if len(h.Degraded()) != 0 {
		t.Fatalf("starting kind must not be Degraded")
	}

	if !h.MarkUnhealthy("Dynamo", errors.New("conversion webhook: connection refused")) {
		t.Error("first failure must transition")
	}
	if h.MarkUnhealthy("Dynamo", errors.New("timed out waiting for cache")) {
		t.Error("second failure must not transition again")
	}
	st, _ := h.Status("Dynamo")
	if st.Healthy || !st.Degraded() || st.Failures != 2 || !strings.Contains(st.LastError, "timed out") {
		t.Errorf("after two failures: %+v", st)
	}
	if testutil.ToFloat64(metrics.WatchHealthy.WithLabelValues("Dynamo")) != 0 {
		t.Error("gauge must be 0 while unhealthy")
	}

	if !h.MarkHealthy("Dynamo") {
		t.Error("recovery must transition")
	}
	if h.MarkHealthy("Dynamo") {
		t.Error("staying healthy must not transition")
	}
	st, _ = h.Status("Dynamo")
	if !st.Healthy || st.LastError != "" || st.Failures != 0 {
		t.Errorf("after recovery: %+v", st)
	}
	if testutil.ToFloat64(metrics.WatchHealthy.WithLabelValues("Dynamo")) != 1 {
		t.Error("gauge must be 1 while healthy")
	}

	if len(got) != 2 || got[0].Healthy || !got[1].Healthy || !strings.Contains(got[0].Message, "connection refused") {
		t.Errorf("transitions = %+v, want exactly [unhealthy(connection refused), healthy]", got)
	}
	if h.MarkUnhealthy("Dynamo", nil) {
		t.Error("nil error must be ignored")
	}
}

// The Degraded message is deterministic (sorted by kind) and carries the
// verbatim API error so kubectl describe is enough to diagnose.
func TestWatchHealthMessage(t *testing.T) {
	h := NewWatchHealth()
	h.now = func() time.Time { return time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC) }
	h.MarkUnhealthy("NIMService", errors.New("the server could not find the requested resource"))
	h.MarkUnhealthy("DynamoGraphDeployment", errors.New(`conversion webhook for nvidia.com/v1alpha1, Kind=DynamoGraphDeployment failed: Post "https://dynamo-operator.dynamo.svc:443/convert": connection refused`))
	h.Register("LeaderWorkerSet") // starting: must not appear
	msg := watchHealthMessage(h.Degraded())
	for _, want := range []string{
		"DynamoGraphDeployment (since 2026-10-02T12:00:00Z): conversion webhook",
		"NIMService (since 2026-10-02T12:00:00Z): the server could not find",
		"existing AIBOMs for these kinds are kept",
		"all other kinds are unaffected",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q:\n%s", want, msg)
		}
	}
	if strings.Index(msg, "DynamoGraphDeployment") > strings.Index(msg, "NIMService") {
		t.Errorf("kinds must be sorted: %s", msg)
	}
	if strings.Contains(msg, "LeaderWorkerSet") {
		t.Errorf("a starting kind must not be reported: %s", msg)
	}
	if watchHealthMessage(nil) != "" {
		t.Error("empty input must render empty")
	}
}

func TestNextBackoff(t *testing.T) {
	cases := []struct{ cur, max, want time.Duration }{
		{5 * time.Second, 5 * time.Minute, 10 * time.Second},
		{4 * time.Minute, 5 * time.Minute, 5 * time.Minute},
		{5 * time.Minute, 5 * time.Minute, 5 * time.Minute},
	}
	for _, tc := range cases {
		if got := nextBackoff(tc.cur, tc.max); got != tc.want {
			t.Errorf("nextBackoff(%s,%s)=%s want %s", tc.cur, tc.max, got, tc.want)
		}
	}
}
