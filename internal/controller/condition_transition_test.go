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
	"errors"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	aibomv1beta1 "github.com/GoogleCloudPlatform/k8s-aibom/api/v1beta1"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/bom"
	configpkg "github.com/GoogleCloudPlatform/k8s-aibom/internal/config"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/sink"
)

// A full rebuild must not move a condition's lastTransitionTime unless
// its status changed. A failing sink blanks the input hash, so every
// retry takes the full path: SinkFailed stays True across them and must
// keep the time it first became True. When the sink recovers, the
// conditions that flip get the new time and the rest keep theirs.
func TestReconcile_FullPathKeepsTransitionTimeWhenStatusUnchanged(t *testing.T) {
	ctx := context.Background()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:   "ai-team",
		Labels: map[string]string{configpkg.DefaultNamespaceOptInLabel: "true"},
	}}
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "ai-team", UID: "uid-app", Generation: 1},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "vllm/vllm-openai:v0.6.3"}}},
			},
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(newConfigFakeScheme(t)).
		WithObjects(ns, dep).
		WithStatusSubresource(&aibomv1beta1.AIBOM{}).
		Build()

	rs := &recordingSink{name: "recording", url: "gs://test-bucket/path.json", err: errors.New("bucket unavailable")}
	snap := configpkg.DefaultSnapshot()
	snap.ExternalSinks = []sink.Sink{rs}
	// Whole seconds: metav1.Time round-trips through the fake client
	// at RFC 3339 second precision.
	t0 := time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)
	now := t0
	sb := NewStatusBuilder()
	sb.Now = func() time.Time { return now }
	r := &DeploymentReconciler{WorkloadReconciler: WorkloadReconciler{
		Client:            c,
		Scheme:            c.Scheme(),
		Scraper:           &erroringScraper{},
		BOMBuilder:        bom.NewBuilder(),
		StatusBuilder:     sb,
		ConfigStore:       configpkg.NewStore(snap),
		ControllerName:    "k8s-aibom",
		ControllerVersion: "0.1.0-test",
	}}
	reconcile := func() aibomv1beta1.AIBOMStatus {
		t.Helper()
		if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ai-team", Name: "app"}}); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		var got aibomv1beta1.AIBOM
		if err := c.Get(ctx, types.NamespacedName{Namespace: "ai-team", Name: AIBOMNameForDeployment(dep)}, &got); err != nil {
			t.Fatalf("get AIBOM: %v", err)
		}
		return got.Status
	}
	check := func(st aibomv1beta1.AIBOMStatus, condType string, wantStatus metav1.ConditionStatus, wantTransition time.Time) {
		t.Helper()
		cond := apimeta.FindStatusCondition(st.Conditions, condType)
		if cond == nil {
			t.Fatalf("%s condition missing", condType)
		}
		if cond.Status != wantStatus {
			t.Errorf("%s = %s, want %s", condType, cond.Status, wantStatus)
		}
		if !cond.LastTransitionTime.Time.Equal(wantTransition) {
			t.Errorf("%s.lastTransitionTime = %v, want %v", condType, cond.LastTransitionTime.UTC(), wantTransition)
		}
	}

	st := reconcile()
	check(st, aibomv1beta1.ConditionSinkFailed, metav1.ConditionTrue, t0)

	// Sink retry a minute later: still failing, so the full path runs
	// again and no condition changes status.
	now = t0.Add(time.Minute)
	st = reconcile()
	if rs.emitCount() != 2 {
		t.Fatalf("sink emits = %d, want 2 (a failing sink is retried on the full path)", rs.emitCount())
	}
	if st.LastReconciled == nil || !st.LastReconciled.Time.Equal(now) {
		t.Fatalf("lastReconciled = %v, want %v", st.LastReconciled, now)
	}
	check(st, aibomv1beta1.ConditionReady, metav1.ConditionTrue, t0)
	check(st, aibomv1beta1.ConditionSynced, metav1.ConditionFalse, t0)
	check(st, aibomv1beta1.ConditionSinkFailed, metav1.ConditionTrue, t0)
	check(st, aibomv1beta1.ConditionStale, metav1.ConditionFalse, t0)

	// The sink recovers: Synced and SinkFailed transition, Ready and
	// Stale do not.
	t2 := t0.Add(2 * time.Minute)
	now = t2
	rs.mu.Lock()
	rs.err = nil
	rs.mu.Unlock()
	st = reconcile()
	check(st, aibomv1beta1.ConditionReady, metav1.ConditionTrue, t0)
	check(st, aibomv1beta1.ConditionSynced, metav1.ConditionTrue, t2)
	check(st, aibomv1beta1.ConditionSinkFailed, metav1.ConditionFalse, t2)
	check(st, aibomv1beta1.ConditionStale, metav1.ConditionFalse, t0)
}
