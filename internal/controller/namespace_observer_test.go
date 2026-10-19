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
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// testNamespaceObservationSettle is the settle period startEnvTest
// wires: short enough that envtests see the event quickly, long enough
// that a slow first reconcile on a loaded CI runner cannot outrun it.
const testNamespaceObservationSettle = 2 * time.Second

func testNamespace(name, uid string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID(uid)}}
}

func waitForEvents(t *testing.T, rec *captureRecorder, want int) []capturedEvent {
	t.Helper()
	var got []capturedEvent
	eventually(t, 2*time.Second, 10*time.Millisecond, func() error {
		got = rec.eventsWithReason(EventReasonNamespaceObserved)
		if len(got) < want {
			return fmt.Errorf("NamespaceObserved events = %d, want %d", len(got), want)
		}
		return nil
	})
	return got
}

// assertNoMoreEvents waits past the settle period and checks the event
// count did not grow.
func assertNoMoreEvents(t *testing.T, rec *captureRecorder, want int, wait time.Duration) {
	t.Helper()
	time.Sleep(wait)
	if got := rec.eventsWithReason(EventReasonNamespaceObserved); len(got) != want {
		t.Fatalf("NamespaceObserved events = %d, want %d: %+v", len(got), want, got)
	}
}

func TestNamespaceObserver_CountsAndTargetsNamespace(t *testing.T) {
	rec := &captureRecorder{}
	o := newNamespaceObserver(rec, 30*time.Millisecond, time.Second)
	ns := testNamespace("team-a", "uid-1")

	o.Considered(ns, "apps/Deployment/nginx", false)
	o.Considered(ns, "apps/Deployment/vllm", true)
	o.Considered(ns, "batch/Job/eval", false)
	// A repeat reconcile of the same workload is not another workload.
	o.Considered(ns, "apps/Deployment/nginx", false)

	events := waitForEvents(t, rec, 1)
	e := events[0]
	if e.EventType != corev1.EventTypeNormal {
		t.Errorf("event type = %q, want Normal", e.EventType)
	}
	if !strings.Contains(e.Message, "3 workload(s) considered, 1 matched") || !strings.Contains(e.Message, "2 matched none") {
		t.Errorf("message = %q, want 3 considered / 1 matched / 2 none", e.Message)
	}
	ref, ok := e.Object.(*corev1.ObjectReference)
	if !ok {
		t.Fatalf("involvedObject = %T, want *corev1.ObjectReference", e.Object)
	}
	if ref.Kind != "Namespace" || ref.Name != "team-a" || ref.Namespace != "team-a" || ref.UID != "uid-1" {
		t.Errorf("involvedObject = %+v, want Namespace team-a stored in team-a", ref)
	}

	// Announced once: later reconciles in the namespace say nothing.
	o.Considered(ns, "apps/Deployment/new", true)
	assertNoMoreEvents(t, rec, 1, 100*time.Millisecond)
}

func TestNamespaceObserver_LatestOutcomeWins(t *testing.T) {
	rec := &captureRecorder{}
	o := newNamespaceObserver(rec, 30*time.Millisecond, time.Second)
	ns := testNamespace("team-b", "uid-1")

	o.Considered(ns, "apps/Deployment/app", false)
	o.Considered(ns, "apps/Deployment/app", true) // image changed to a runtime

	e := waitForEvents(t, rec, 1)[0]
	if !strings.Contains(e.Message, "1 workload(s) considered, 1 matched") {
		t.Errorf("message = %q, want 1 considered / 1 matched", e.Message)
	}
}

func TestNamespaceObserver_ForgetAnnouncesAgain(t *testing.T) {
	rec := &captureRecorder{}
	o := newNamespaceObserver(rec, 30*time.Millisecond, time.Second)
	ns := testNamespace("team-c", "uid-1")

	o.Considered(ns, "apps/Deployment/app", false)
	waitForEvents(t, rec, 1)

	// Opted out, then in again.
	o.Forget("team-c")
	o.Considered(ns, "apps/Deployment/app", false)
	waitForEvents(t, rec, 2)
}

func TestNamespaceObserver_ForgetBeforeSettleCancels(t *testing.T) {
	rec := &captureRecorder{}
	o := newNamespaceObserver(rec, 50*time.Millisecond, time.Second)

	o.Considered(testNamespace("team-d", "uid-1"), "apps/Deployment/app", false)
	o.Forget("team-d")
	assertNoMoreEvents(t, rec, 0, 150*time.Millisecond)
}

func TestNamespaceObserver_RecreatedNamespaceAnnouncesAgain(t *testing.T) {
	rec := &captureRecorder{}
	o := newNamespaceObserver(rec, 30*time.Millisecond, time.Second)

	o.Considered(testNamespace("team-e", "uid-1"), "apps/Deployment/app", true)
	waitForEvents(t, rec, 1)

	o.Considered(testNamespace("team-e", "uid-2"), "apps/Deployment/app", false)
	events := waitForEvents(t, rec, 2)
	if ref := events[1].Object.(*corev1.ObjectReference); ref.UID != "uid-2" {
		t.Errorf("second event UID = %q, want uid-2", ref.UID)
	}
}

// New workloads push the event back, but never past maxWait from the
// first one, so a namespace with constant churn is still announced.
func TestNamespaceObserver_MaxWaitCapsSettle(t *testing.T) {
	rec := &captureRecorder{}
	o := newNamespaceObserver(rec, 100*time.Millisecond, 300*time.Millisecond)
	ns := testNamespace("team-f", "uid-1")

	start := time.Now()
	deadline := start.Add(time.Second)
	for i := 0; time.Now().Before(deadline); i++ {
		if len(rec.eventsWithReason(EventReasonNamespaceObserved)) > 0 {
			break
		}
		o.Considered(ns, fmt.Sprintf("apps/Deployment/app-%d", i), false)
		time.Sleep(20 * time.Millisecond)
	}
	waitForEvents(t, rec, 1)
	if elapsed := time.Since(start); elapsed > 700*time.Millisecond {
		t.Errorf("event after %v, want about maxWait (300ms)", elapsed)
	}
}

func TestNamespaceObserver_NilSafe(t *testing.T) {
	var o *NamespaceObserver
	o.Considered(testNamespace("x", "uid"), "apps/Deployment/app", true)
	o.Forget("x")
}

// Against a real API server: the event must be accepted with the
// namespace as both its own namespace and the involved object, and be
// listable in that namespace (the `kubectl get events -n` check from
// #106).
func TestIntegration_NamespaceObservedEvent(t *testing.T) {
	env := startEnvTest(t)
	ctx := context.Background()

	dep := func(name, image string, args ...string) *appsv1.Deployment {
		return &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "observed-optin"},
			Spec: appsv1.DeploymentSpec{
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{Name: "c", Image: image, Args: args}},
					},
				},
			},
		}
	}
	// Create both workloads before opting the namespace in, so the opt-in
	// enqueues them together and the first pass counts both, however
	// slowly the first reconcile runs.
	optin := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "observed-optin"}}
	mustCreate(t, env.k8sClient, ctx, optin)
	mustCreate(t, env.k8sClient, ctx, dep("nginx", "nginx:1.27"))
	mustCreate(t, env.k8sClient, ctx, dep("vllm", "vllm/vllm-openai:v0.6.3", "--model", "facebook/opt-125m"))
	// Let the manager's cache see both Deployments before the opt-in
	// lists them.
	time.Sleep(500 * time.Millisecond)
	optin.Labels = map[string]string{OptInLabel: "true"}
	if err := env.k8sClient.Update(ctx, optin); err != nil {
		t.Fatalf("opt in namespace: %v", err)
	}

	// A namespace that is not opted in gets nothing.
	mustCreate(t, env.k8sClient, ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "observed-plain"}})
	plain := dep("nginx", "nginx:1.27")
	plain.Namespace = "observed-plain"
	mustCreate(t, env.k8sClient, ctx, plain)

	eventually(t, 30*time.Second, 200*time.Millisecond, func() error {
		var list corev1.EventList
		if err := env.k8sClient.List(ctx, &list, client.InNamespace("observed-optin")); err != nil {
			return err
		}
		for _, e := range list.Items {
			if e.Reason != EventReasonNamespaceObserved {
				continue
			}
			if e.InvolvedObject.Kind != "Namespace" || e.InvolvedObject.Name != "observed-optin" {
				return fmt.Errorf("involvedObject = %+v", e.InvolvedObject)
			}
			if !strings.Contains(e.Message, "2 workload(s) considered, 1 matched") {
				return fmt.Errorf("message = %q, want 2 considered / 1 matched", e.Message)
			}
			return nil
		}
		return fmt.Errorf("no %s event in observed-optin yet", EventReasonNamespaceObserved)
	})

	var plainEvents corev1.EventList
	if err := env.k8sClient.List(ctx, &plainEvents, client.InNamespace("observed-plain")); err != nil {
		t.Fatalf("list events: %v", err)
	}
	for _, e := range plainEvents.Items {
		if e.Reason == EventReasonNamespaceObserved {
			t.Errorf("not-opted-in namespace got %s: %q", e.Reason, e.Message)
		}
	}
}
