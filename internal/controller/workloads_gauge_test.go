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
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	aibomv1beta1 "github.com/GoogleCloudPlatform/k8s-aibom/api/v1beta1"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/metrics"
)

// aibom_workloads_total was registered but never set (#398), so
// /metrics had no series for it at all. These tests pin the gauge that
// publishes the current AIBOM count by category and runtime.

// workloadKey is one (category, runtime) label pair.
type workloadKey struct{ category, runtime string }

// resetWorkloadsTotal clears the process-global gauge now and after
// the test, so tests do not see each other's series.
func resetWorkloadsTotal(t *testing.T) {
	t.Helper()
	metrics.WorkloadsTotal.Reset()
	t.Cleanup(metrics.WorkloadsTotal.Reset)
}

// workloadsTotalMatches compares the whole aibom_workloads_total vec
// with want. It never calls WithLabelValues, which would create the
// series it reads.
func workloadsTotalMatches(want map[workloadKey]float64) error {
	if len(want) == 0 {
		if n := testutil.CollectAndCount(metrics.WorkloadsTotal); n != 0 {
			return fmt.Errorf("aibom_workloads_total has %d series, want 0", n)
		}
		return nil
	}
	keys := make([]workloadKey, 0, len(want))
	for k := range want {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].category != keys[j].category {
			return keys[i].category < keys[j].category
		}
		return keys[i].runtime < keys[j].runtime
	})
	var b strings.Builder
	b.WriteString("# HELP aibom_workloads_total Current number of AI workloads tracked by the controller\n")
	b.WriteString("# TYPE aibom_workloads_total gauge\n")
	for _, k := range keys {
		fmt.Fprintf(&b, "aibom_workloads_total{category=%q,runtime=%q} %v\n", k.category, k.runtime, want[k])
	}
	if err := testutil.CollectAndCompare(metrics.WorkloadsTotal, strings.NewReader(b.String())); err != nil {
		return fmt.Errorf("aibom_workloads_total mismatch: %w", err)
	}
	return nil
}

func expectWorkloadsTotal(t *testing.T, want map[workloadKey]float64) {
	t.Helper()
	if err := workloadsTotalMatches(want); err != nil {
		t.Fatal(err)
	}
}

// testAIBOM builds an AIBOM with the given summary category and
// runtime. An empty runtime means Summary.Runtime is nil.
func testAIBOM(ns, name, category, runtime string) *aibomv1beta1.AIBOM {
	a := &aibomv1beta1.AIBOM{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: aibomv1beta1.AIBOMSpec{
			WorkloadRef: aibomv1beta1.WorkloadRef{APIVersion: "apps/v1", Kind: "Deployment", Name: name},
		},
		Status: aibomv1beta1.AIBOMStatus{
			Summary: &aibomv1beta1.AIBOMSummary{
				Workload: aibomv1beta1.WorkloadSummary{
					Kind: "Deployment", APIVersion: "apps/v1", Name: name, Namespace: ns, Category: category,
				},
			},
		},
	}
	if runtime != "" {
		a.Status.Summary.Runtime = &aibomv1beta1.RuntimeSummary{Name: runtime}
	}
	return a
}

func newGaugeFakeClient(t *testing.T, objs ...client.Object) client.WithWatch {
	t.Helper()
	return fake.NewClientBuilder().WithScheme(newConfigFakeScheme(t)).WithObjects(objs...).Build()
}

func mustRefresh(t *testing.T, g *WorkloadsGauge) {
	t.Helper()
	if err := g.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
}

// AC1: each label pair reads its count; a deleted AIBOM's pair is
// removed and the other stays.
func TestWorkloadsGauge_AddAndDelete(t *testing.T) {
	resetWorkloadsTotal(t)
	ctx := context.Background()
	c := newGaugeFakeClient(t,
		testAIBOM("ns-a", "serve", "inference", "vllm"),
		testAIBOM("ns-a", "train", "training", ""),
	)
	g := NewWorkloadsGauge(c)

	mustRefresh(t, g)
	expectWorkloadsTotal(t, map[workloadKey]float64{
		{"inference", "vllm"}: 1,
		{"training", "none"}:  1,
	})

	if err := c.Delete(ctx, &aibomv1beta1.AIBOM{ObjectMeta: metav1.ObjectMeta{Namespace: "ns-a", Name: "train"}}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	mustRefresh(t, g)
	expectWorkloadsTotal(t, map[workloadKey]float64{
		{"inference", "vllm"}: 1,
	})
}

// AC6: no AIBOMs means no series and no error.
func TestWorkloadsGauge_Empty(t *testing.T) {
	resetWorkloadsTotal(t)
	g := NewWorkloadsGauge(newGaugeFakeClient(t))
	mustRefresh(t, g)
	expectWorkloadsTotal(t, nil)
}

// AC7: a missing summary is unknown/none; a nil runtime is none.
func TestWorkloadsGauge_MissingSummary(t *testing.T) {
	resetWorkloadsTotal(t)
	noSummary := testAIBOM("ns-a", "failed", "", "")
	noSummary.Status.Summary = nil
	c := newGaugeFakeClient(t,
		noSummary,
		testAIBOM("ns-a", "agent", "agent", ""),
	)
	g := NewWorkloadsGauge(c)
	mustRefresh(t, g)
	expectWorkloadsTotal(t, map[workloadKey]float64{
		{"unknown", "none"}: 1,
		{"agent", "none"}:   1,
	})
}

// AC8: AIBOMs with the same pair across namespaces add up.
func TestWorkloadsGauge_Aggregates(t *testing.T) {
	resetWorkloadsTotal(t)
	c := newGaugeFakeClient(t,
		testAIBOM("ns-a", "one", "inference", "vllm"),
		testAIBOM("ns-a", "two", "inference", "vllm"),
		testAIBOM("ns-b", "three", "inference", "vllm"),
	)
	g := NewWorkloadsGauge(c)
	mustRefresh(t, g)
	expectWorkloadsTotal(t, map[workloadKey]float64{
		{"inference", "vllm"}: 3,
	})
}

// AC9: a runtime change moves the count and drops the old series.
func TestWorkloadsGauge_LabelMove(t *testing.T) {
	resetWorkloadsTotal(t)
	ctx := context.Background()
	c := newGaugeFakeClient(t, testAIBOM("ns-a", "serve", "inference", "vllm"))
	g := NewWorkloadsGauge(c)
	mustRefresh(t, g)
	expectWorkloadsTotal(t, map[workloadKey]float64{
		{"inference", "vllm"}: 1,
	})

	var a aibomv1beta1.AIBOM
	if err := c.Get(ctx, types.NamespacedName{Namespace: "ns-a", Name: "serve"}, &a); err != nil {
		t.Fatalf("get: %v", err)
	}
	a.Status.Summary.Runtime.Name = "tgi"
	if err := c.Update(ctx, &a); err != nil {
		t.Fatalf("update: %v", err)
	}
	mustRefresh(t, g)
	expectWorkloadsTotal(t, map[workloadKey]float64{
		{"inference", "tgi"}: 1,
	})
}

// AC10: an AIBOM held by a finalizer after delete is not counted.
func TestWorkloadsGauge_SkipsDeleting(t *testing.T) {
	resetWorkloadsTotal(t)
	ctx := context.Background()
	held := testAIBOM("ns-a", "held", "training", "")
	held.Finalizers = []string{"example.com/hold"}
	c := newGaugeFakeClient(t, held, testAIBOM("ns-a", "live", "inference", "vllm"))
	if err := c.Delete(ctx, held); err != nil {
		t.Fatalf("delete: %v", err)
	}
	var got aibomv1beta1.AIBOM
	if err := c.Get(ctx, types.NamespacedName{Namespace: "ns-a", Name: "held"}, &got); err != nil {
		t.Fatalf("get held AIBOM: %v", err)
	}
	if got.DeletionTimestamp == nil {
		t.Fatal("test setup: held AIBOM has no deletion timestamp")
	}

	g := NewWorkloadsGauge(c)
	mustRefresh(t, g)
	expectWorkloadsTotal(t, map[workloadKey]float64{
		{"inference", "vllm"}: 1,
	})
}

// AC11: a failing List returns an error and keeps the last values.
func TestWorkloadsGauge_ListErrorKeepsValues(t *testing.T) {
	resetWorkloadsTotal(t)
	ctx := context.Background()
	var fail atomic.Bool
	c := fake.NewClientBuilder().
		WithScheme(newConfigFakeScheme(t)).
		WithObjects(
			testAIBOM("ns-a", "serve", "inference", "vllm"),
			testAIBOM("ns-a", "train", "training", ""),
		).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				if fail.Load() {
					return apierrors.NewServiceUnavailable("list failed")
				}
				return cl.List(ctx, list, opts...)
			},
		}).
		Build()
	g := NewWorkloadsGauge(c)
	mustRefresh(t, g)
	want := map[workloadKey]float64{
		{"inference", "vllm"}: 1,
		{"training", "none"}:  1,
	}
	expectWorkloadsTotal(t, want)

	// Change the data too, so a Refresh that ignored the error and
	// published something would be caught.
	if err := c.Delete(ctx, &aibomv1beta1.AIBOM{ObjectMeta: metav1.ObjectMeta{Namespace: "ns-a", Name: "train"}}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	fail.Store(true)
	if err := g.Refresh(ctx); err == nil {
		t.Fatal("Refresh returned nil while List fails; want an error")
	}
	expectWorkloadsTotal(t, want)
}

// startGauge runs g.Start in a goroutine. The returned stop cancels it
// and returns Start's result, failing if it does not return within 5s.
func startGauge(t *testing.T, g *WorkloadsGauge) (stop func() error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- g.Start(ctx) }()
	stopped := false
	var result error
	stop = func() error {
		if stopped {
			return result
		}
		stopped = true
		cancel()
		select {
		case result = <-done:
			return result
		case <-time.After(5 * time.Second):
			t.Fatal("Start did not return within 5s of the context ending")
			return nil
		}
	}
	t.Cleanup(func() { _ = stop() })
	return stop
}

// AC12: Start refreshes at once, then on every tick, and returns nil
// when its context ends.
func TestWorkloadsGauge_StartRefreshesAndStops(t *testing.T) {
	resetWorkloadsTotal(t)
	ctx := context.Background()

	t.Run("refreshes at once", func(t *testing.T) {
		metrics.WorkloadsTotal.Reset()
		c := newGaugeFakeClient(t, testAIBOM("ns-a", "serve", "inference", "vllm"))
		g := NewWorkloadsGauge(c)
		g.Interval = time.Hour
		stop := startGauge(t, g)
		eventually(t, 5*time.Second, 20*time.Millisecond, func() error {
			return workloadsTotalMatches(map[workloadKey]float64{{"inference", "vllm"}: 1})
		})
		if err := stop(); err != nil {
			t.Fatalf("Start returned %v after cancel; want nil", err)
		}
	})

	t.Run("refreshes on tick", func(t *testing.T) {
		metrics.WorkloadsTotal.Reset()
		c := newGaugeFakeClient(t, testAIBOM("ns-a", "serve", "inference", "vllm"))
		g := NewWorkloadsGauge(c)
		g.Interval = 100 * time.Millisecond
		stop := startGauge(t, g)
		eventually(t, 5*time.Second, 20*time.Millisecond, func() error {
			return workloadsTotalMatches(map[workloadKey]float64{{"inference", "vllm"}: 1})
		})
		if err := c.Create(ctx, testAIBOM("ns-b", "serve", "inference", "vllm")); err != nil {
			t.Fatalf("create: %v", err)
		}
		eventually(t, 5*time.Second, 20*time.Millisecond, func() error {
			return workloadsTotalMatches(map[workloadKey]float64{{"inference", "vllm"}: 2})
		})
		if err := stop(); err != nil {
			t.Fatalf("Start returned %v after cancel; want nil", err)
		}
	})
}

// AC13: only the leader reports, so sum() across replicas does not
// double count.
func TestWorkloadsGauge_NeedsLeaderElection(t *testing.T) {
	resetWorkloadsTotal(t)
	if !NewWorkloadsGauge(newGaugeFakeClient(t)).NeedLeaderElection() {
		t.Fatal("NeedLeaderElection() = false, want true")
	}
}

// AC2: end to end. Real reconcilers create the AIBOMs; the gauge runs
// in a manager exactly as cmd/manager adds it, reading the cache.
func TestIntegration_WorkloadsGauge(t *testing.T) {
	resetWorkloadsTotal(t)
	env := startEnvTest(t)
	ctx := context.Background()

	mgr2, err := ctrl.NewManager(env.cfg, ctrl.Options{
		Scheme:                 env.scheme,
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		LeaderElection:         false,
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	gauge := NewWorkloadsGauge(mgr2.GetClient())
	gauge.Interval = 200 * time.Millisecond
	if err := mgr2.Add(gauge); err != nil {
		t.Fatalf("mgr.Add(gauge): %v", err)
	}
	mgrCtx, mgrCancel := context.WithCancel(context.Background())
	mgrDone := make(chan error, 1)
	go func() { mgrDone <- mgr2.Start(mgrCtx) }()
	// Registered after startEnvTest, so it runs before envtest stops
	// and before goleak checks.
	t.Cleanup(func() {
		mgrCancel()
		select {
		case err := <-mgrDone:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Logf("gauge manager stopped with error: %v", err)
			}
		case <-time.After(30 * time.Second):
			t.Error("gauge manager did not stop within 30s")
		}
	})

	ns := "gauge-optin"
	mustCreateOptedInNamespace(t, env.k8sClient, ctx, ns)
	dep := func(name string) *appsv1.Deployment {
		return &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: appsv1.DeploymentSpec{
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{
							Name: "vllm", Image: "vllm/vllm-openai:v0.6.3",
							Args: []string{"--model", "facebook/opt-125m"},
						}},
					},
				},
			},
		}
	}
	names := []string{"vllm-one", "vllm-two"}
	for _, n := range names {
		mustCreate(t, env.k8sClient, ctx, dep(n))
	}

	// Setup check: both AIBOMs exist with the expected summary, so a
	// gauge failure below is about the gauge, not the reconciler.
	eventually(t, 30*time.Second, 200*time.Millisecond, func() error {
		for _, n := range names {
			var a aibomv1beta1.AIBOM
			key := types.NamespacedName{Namespace: ns, Name: AIBOMNameForWorkload("apps", "Deployment", n)}
			if err := env.k8sClient.Get(ctx, key, &a); err != nil {
				return fmt.Errorf("get AIBOM %s: %w", key, err)
			}
			s := a.Status.Summary
			if s == nil || s.Workload.Category != "inference" || s.Runtime == nil || s.Runtime.Name != "vllm" {
				return fmt.Errorf("AIBOM %s summary not yet inference/vllm: %+v", key, s)
			}
		}
		return nil
	})

	eventually(t, 10*time.Second, 100*time.Millisecond, func() error {
		return workloadsTotalMatches(map[workloadKey]float64{{"inference", "vllm"}: 2})
	})

	// envtest has no garbage collector: delete the Deployment, then
	// its AIBOM.
	gone := names[1]
	if err := env.k8sClient.Delete(ctx, &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: gone}}); err != nil {
		t.Fatalf("delete Deployment: %v", err)
	}
	goneAIBOM := &aibomv1beta1.AIBOM{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: AIBOMNameForWorkload("apps", "Deployment", gone)}}
	if err := env.k8sClient.Delete(ctx, goneAIBOM); err != nil && !apierrors.IsNotFound(err) {
		t.Fatalf("delete AIBOM: %v", err)
	}

	eventually(t, 10*time.Second, 100*time.Millisecond, func() error {
		return workloadsTotalMatches(map[workloadKey]float64{{"inference", "vllm"}: 1})
	})
}
