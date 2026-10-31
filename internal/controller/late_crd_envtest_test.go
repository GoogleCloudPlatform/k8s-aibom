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
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/yaml"

	"github.com/GoogleCloudPlatform/k8s-aibom/internal/metrics"
)

// Late CRD discovery (Design 004 amendment A7, #226, Phase A): CRDs
// installed after the manager is running are discovered and watched
// without a restart; the workloadKinds allowlist keeps precedence.

const (
	dgdCRDFile  = "nvidia.com_dynamographdeployments.yaml"
	dcdCRDFile  = "nvidia.com_dynamocomponentdeployments.yaml"
	lwsCRDFile  = "leaderworkerset.x-k8s.io_leaderworkersets.yaml"
	nimCRDFile  = "apps.nvidia.com_nimservices.yaml"
	isvcCRDFile = "serving.kserve.io_inferenceservices.yaml"
	dsetCRDFile = "disaggregatedset.x-k8s.io_disaggregatedsets.yaml"

	dgdCRDName = "dynamographdeployments.nvidia.com"
	lwsCRDName = "leaderworkersets.leaderworkerset.x-k8s.io"
	nimCRDName = "nimservices.apps.nvidia.com"
)

var (
	gkDGD  = schema.GroupKind{Group: "nvidia.com", Kind: "DynamoGraphDeployment"}
	gkDCD  = schema.GroupKind{Group: "nvidia.com", Kind: "DynamoComponentDeployment"}
	gkLWS  = schema.GroupKind{Group: "leaderworkerset.x-k8s.io", Kind: "LeaderWorkerSet"}
	gkNIM  = schema.GroupKind{Group: "apps.nvidia.com", Kind: "NIMService"}
	gkISVC = schema.GroupKind{Group: "serving.kserve.io", Kind: "InferenceService"}
	gkDSet = schema.GroupKind{Group: "disaggregatedset.x-k8s.io", Kind: "DisaggregatedSet"}

	allCandidateKinds = []string{"InferenceService", "DynamoGraphDeployment", "DynamoComponentDeployment",
		"NIMService", "LeaderWorkerSet", "DisaggregatedSet"}
)

// installExternalCRD installs one config/crd/external file into the
// running envtest API server; envtest waits until it is discoverable.
func installExternalCRD(t *testing.T, env *envTestEnv, file string) {
	t.Helper()
	if _, err := envtest.InstallCRDs(env.cfg, envtest.CRDInstallOptions{
		Paths:              []string{filepath.Join("..", "..", "config", "crd", "external", file)},
		ErrorIfPathMissing: true,
	}); err != nil {
		t.Fatalf("install %s: %v", file, err)
	}
}

// waitCRDServes waits until the CRD reports Established=True and serves
// version, read straight from the API server (independently of the
// production readiness check).
func waitCRDServes(t *testing.T, env *envTestEnv, name, version string) {
	t.Helper()
	eventually(t, 30*time.Second, 100*time.Millisecond, func() error {
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(crdGVK)
		if err := env.k8sClient.Get(context.Background(), types.NamespacedName{Name: name}, u); err != nil {
			return err
		}
		established := false
		conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
		for _, c := range conds {
			m := c.(map[string]interface{})
			if m["type"] == "Established" && m["status"] == "True" {
				established = true
			}
		}
		if !established {
			return fmt.Errorf("%s not Established yet", name)
		}
		versions, _, _ := unstructured.NestedSlice(u.Object, "spec", "versions")
		for _, v := range versions {
			m := v.(map[string]interface{})
			if m["name"] == version && m["served"] == true {
				return nil
			}
		}
		return fmt.Errorf("%s does not serve %s", name, version)
	})
}

// eventLog accumulates the supervisor's Pod events from a FakeRecorder
// ("<type> <reason> <message>").
type eventLog struct {
	mu   sync.Mutex
	rec  *record.FakeRecorder
	seen []string
}

func (l *eventLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	for {
		select {
		case e := <-l.rec.Events:
			l.seen = append(l.seen, e)
		default:
			return append([]string(nil), l.seen...)
		}
	}
}

// count returns how many events have this reason and name kind as the
// subject of their message.
func (l *eventLog) count(reason, kind string) int {
	n := 0
	for _, e := range l.all() {
		if strings.Contains(e, " "+reason+" "+kind+" ") {
			n++
		}
	}
	return n
}

func (l *eventLog) wait(t *testing.T, reason, kind string) {
	t.Helper()
	eventually(t, 30*time.Second, 100*time.Millisecond, func() error {
		if l.count(reason, kind) == 0 {
			return fmt.Errorf("no %s event for %s yet; events: %q", reason, kind, l.all())
		}
		return nil
	})
}

func requireManagerRunning(t *testing.T, env *envTestEnv) {
	t.Helper()
	select {
	case err := <-env.mgrErrCh:
		env.mgrErrCh <- err // keep it for cleanup
		t.Fatalf("manager exited: %v", err)
	default:
	}
}

func requireNoHealthEntry(t *testing.T, h *WatchHealth, kind string) {
	t.Helper()
	if st, ok := h.Status(kind); ok {
		t.Fatalf("row 1: %s has a health entry before its CRD was confirmed Ready: %+v", kind, st)
	}
}

// Primary acceptance test (amendment A7): the Dynamo graph CRD arrives
// after the controller started, while the DCD CRD already serves the
// same group-version. Its watch starts, is announced once as
// WatchStarted after its informer synced, and inventories a DGD created
// afterwards; nothing else is disturbed and nothing restarts.
func TestIntegration_LateCRD_DynamoGraphDeploymentAfterStartup(t *testing.T) {
	opts := envTestOptions{omitExternalCRDs: []string{dgdCRDFile}, events: true}
	reads := observeCRDReads(&opts)
	env := startEnvTestWith(t, opts)
	ctx := context.Background()
	events := &eventLog{rec: env.recorder}

	// nvidia.com/v1beta1 is already served and mapped through DCD.
	waitHealthy(t, env.watchHealth, "DynamoComponentDeployment")
	events.wait(t, EventReasonWatchRecovered, "DynamoComponentDeployment")
	if n := events.count(EventReasonWatchStarted, "DynamoComponentDeployment"); n != 0 {
		t.Errorf("a kind present at startup keeps WatchRecovered; got %d WatchStarted", n)
	}
	requireNoHealthEntry(t, env.watchHealth, "DynamoGraphDeployment")
	if env.tracked.Has(gkDGD) {
		t.Fatal("an absent CRD's kind must not be tracked")
	}

	ns := "late-dynamo"
	mustCreateOptedInNamespace(t, env.k8sClient, ctx, ns)
	mustCreate(t, env.k8sClient, ctx, expVllmDeployment(ns, "vllm-a"))
	depA := types.NamespacedName{Name: AIBOMNameForWorkload("apps", "Deployment", "vllm-a"), Namespace: ns}
	waitAIBOM(t, env, ctx, depA, "Deployment before the late CRD")

	waitAbsenceObserved(t, reads, dgdCRDName)
	installExternalCRD(t, env, dgdCRDFile)
	waitCRDServes(t, env, dgdCRDName, "v1beta1")

	waitHealthy(t, env.watchHealth, "DynamoGraphDeployment")
	events.wait(t, EventReasonWatchStarted, "DynamoGraphDeployment")
	if !env.tracked.Has(gkDGD) {
		t.Error("a Ready CRD's kind must be tracked")
	}

	mustCreate(t, env.k8sClient, ctx, dynamoGraphDeployment(ns, "qwen-late", "vllm",
		dynamoComponent("Frontend", "frontend", "", "nvcr.io/nvidia/ai-dynamo/dynamo-frontend:0.6.0"),
		dynamoComponent("VllmWorker", "worker", "Qwen/Qwen3-0.6B", "nvcr.io/nvidia/ai-dynamo/vllm-runtime:0.6.0"),
	))
	dgdKey := types.NamespacedName{Name: AIBOMNameForWorkload("nvidia.com", "DynamoGraphDeployment", "qwen-late"), Namespace: ns}
	waitInlineContains(t, env.k8sClient, ctx, dgdKey, `"Qwen/Qwen3-0.6B"`)

	if g := testutil.ToFloat64(metrics.WatchHealthy.WithLabelValues("DynamoGraphDeployment")); g != 1 {
		t.Errorf("aibom_watch_healthy{DynamoGraphDeployment} = %v, want 1", g)
	}
	if d := env.watchHealth.Degraded(); len(d) != 0 {
		t.Errorf("no kind may be Degraded: %+v", d)
	}
	if !aibomExists(env.k8sClient, ctx, depA) {
		t.Error("the existing Deployment AIBOM must be intact")
	}
	mustCreate(t, env.k8sClient, ctx, expVllmDeployment(ns, "vllm-b"))
	waitAIBOM(t, env, ctx, types.NamespacedName{Name: AIBOMNameForWorkload("apps", "Deployment", "vllm-b"), Namespace: ns},
		"a Deployment created after the late CRD")

	// Exactly one WatchStarted, no WatchRecovered or WatchUnhealthy for
	// that start, and none appear later.
	time.Sleep(2 * time.Second)
	if n := events.count(EventReasonWatchStarted, "DynamoGraphDeployment"); n != 1 {
		t.Errorf("WatchStarted for DynamoGraphDeployment: %d, want 1; events: %q", n, events.all())
	}
	for _, reason := range []string{EventReasonWatchRecovered, EventReasonWatchUnhealthy, EventReasonWatchDisabled} {
		if n := events.count(reason, "DynamoGraphDeployment"); n != 0 {
			t.Errorf("%s for DynamoGraphDeployment: %d, want 0; events: %q", reason, n, events.all())
		}
	}
	for _, e := range events.all() {
		if strings.Contains(e, " "+EventReasonWatchStarted+" ") && !strings.Contains(e, dgdCRDName) {
			t.Errorf("WatchStarted message must name the CRD: %q", e)
		}
	}
	requireManagerRunning(t, env)
}

// No candidate CRD at startup: the supervisor is still registered and
// reports healthy aggregate discovery, but no candidate watch health
// entries or candidate-specific events exist. CRDs installed later,
// two in one group-version, start without a restart.
func TestIntegration_LateCRD_NoCandidateCRDsAtStartup(t *testing.T) {
	opts := envTestOptions{
		omitExternalCRDs: []string{dgdCRDFile, dcdCRDFile, lwsCRDFile, nimCRDFile, isvcCRDFile, dsetCRDFile},
		events:           true,
	}
	reads := observeCRDReads(&opts)
	env := startEnvTestWith(t, opts)
	ctx := context.Background()
	events := &eventLog{rec: env.recorder}

	ns := "late-none"
	mustCreateOptedInNamespace(t, env.k8sClient, ctx, ns)
	mustCreate(t, env.k8sClient, ctx, expVllmDeployment(ns, "vllm"))
	waitAIBOM(t, env, ctx, types.NamespacedName{Name: AIBOMNameForWorkload("apps", "Deployment", "vllm"), Namespace: ns},
		"apps/v1 kinds are unaffected by absent candidates")
	time.Sleep(time.Second)
	for _, k := range allCandidateKinds {
		requireNoHealthEntry(t, env.watchHealth, k)
	}
	// A6 adds one healthy discovery pseudo-kind even when no candidate
	// is installed. This must not announce any workload watch event.
	waitHealthy(t, env.watchHealth, CRDDiscoveryKind)
	events.wait(t, EventReasonWatchRecovered, CRDDiscoveryKind)
	for _, event := range events.all() {
		if !strings.Contains(event, " "+EventReasonWatchRecovered+" "+CRDDiscoveryKind+" ") {
			t.Fatalf("no candidate watch events while all CRDs are absent; got %q", events.all())
		}
	}

	for _, name := range []string{"dynamocomponentdeployments.nvidia.com", dgdCRDName, lwsCRDName} {
		waitAbsenceObserved(t, reads, name)
	}
	installExternalCRD(t, env, dcdCRDFile)
	installExternalCRD(t, env, dgdCRDFile)
	installExternalCRD(t, env, lwsCRDFile)
	for _, k := range []string{"DynamoComponentDeployment", "DynamoGraphDeployment", "LeaderWorkerSet"} {
		waitHealthy(t, env.watchHealth, k)
		events.wait(t, EventReasonWatchStarted, k)
	}
	lws := leaderWorkerSetCR(ns, "multi", nil, lwsPodTemplate("vllm/vllm-openai:v0.6.3", "--model", "Qwen/Qwen3-0.6B"), 1)
	mustCreate(t, env.k8sClient, ctx, lws)
	waitInlineContains(t, env.k8sClient, ctx,
		types.NamespacedName{Name: AIBOMNameForWorkload("leaderworkerset.x-k8s.io", "LeaderWorkerSet", "multi"), Namespace: ns},
		`"Qwen/Qwen3-0.6B"`)
	mustCreate(t, env.k8sClient, ctx, dynamoGraphDeployment(ns, "graph", "vllm",
		dynamoComponent("VllmWorker", "worker", "Qwen/Qwen3-0.6B", "nvcr.io/nvidia/ai-dynamo/vllm-runtime:0.6.0")))
	waitAIBOM(t, env, ctx, types.NamespacedName{Name: AIBOMNameForWorkload("nvidia.com", "DynamoGraphDeployment", "graph"), Namespace: ns},
		"DGD installed with DCD in one group-version")
	// Still-absent candidates stay untouched.
	for _, k := range []string{"InferenceService", "NIMService", "DisaggregatedSet"} {
		requireNoHealthEntry(t, env.watchHealth, k)
	}
	requireManagerRunning(t, env)
}

// workloadKinds precedence (A2 rows 1, 3, 10). A candidate excluded
// while its CRD is absent gets nothing at all. One whose CRD arrives
// while excluded is disabled (WatchDisabled, no informer, no AIBOM) and
// not announced as started; allowing it later starts it through the
// existing re-enable path, announced as WatchRecovered.
func TestIntegration_LateCRD_AllowlistPrecedence(t *testing.T) {
	env := startEnvTestWith(t, envTestOptions{omitExternalCRDs: []string{dgdCRDFile, lwsCRDFile}, events: true})
	ctx := context.Background()
	events := &eventLog{rec: env.recorder}
	waitHealthy(t, env.watchHealth, "DynamoComponentDeployment")

	// Exclude DGD and LWS, both absent.
	setWorkloadKinds(t, env, gkDeployment, gkStatefulSet, gkDaemonSet, gkJob, gkCronJob, gkDCD, gkNIM, gkISVC, gkDSet)
	time.Sleep(2 * time.Second)
	requireNoHealthEntry(t, env.watchHealth, "DynamoGraphDeployment")
	requireNoHealthEntry(t, env.watchHealth, "LeaderWorkerSet")
	for _, k := range []string{"DynamoGraphDeployment", "LeaderWorkerSet"} {
		if n := events.count(EventReasonWatchDisabled, k); n != 0 {
			t.Fatalf("row 1: an absent, excluded candidate must not be disabled: %d WatchDisabled for %s", n, k)
		}
	}
	// An allowed, running kind is unaffected.
	if !env.watchHealth.IsHealthy("DynamoComponentDeployment") {
		t.Fatal("DCD stays healthy")
	}

	// Row 3: the DGD CRD arrives while DGD is excluded.
	installExternalCRD(t, env, dgdCRDFile)
	waitCRDServes(t, env, dgdCRDName, "v1beta1")
	waitWatchDisabled(t, env.watchHealth, "DynamoGraphDeployment")
	events.wait(t, EventReasonWatchDisabled, "DynamoGraphDeployment")
	if !env.tracked.Has(gkDGD) || env.tracked.Owns(gkDGD) {
		t.Errorf("row 3: tracked (Has) but not owning while excluded; Has=%v Owns=%v", env.tracked.Has(gkDGD), env.tracked.Owns(gkDGD))
	}
	ns := "late-allowlist"
	mustCreateOptedInNamespace(t, env.k8sClient, ctx, ns)
	mustCreate(t, env.k8sClient, ctx, dynamoGraphDeployment(ns, "graph", "vllm",
		dynamoComponent("VllmWorker", "worker", "Qwen/Qwen3-0.6B", "nvcr.io/nvidia/ai-dynamo/vllm-runtime:0.6.0")))
	dgdKey := types.NamespacedName{Name: AIBOMNameForWorkload("nvidia.com", "DynamoGraphDeployment", "graph"), Namespace: ns}
	time.Sleep(2 * time.Second)
	if aibomExists(env.k8sClient, ctx, dgdKey) {
		t.Fatal("row 3: an excluded kind must not be inventoried")
	}

	// Row 10: allow everything again.
	setWorkloadKinds(t, env)
	waitHealthy(t, env.watchHealth, "DynamoGraphDeployment")
	waitAIBOM(t, env, ctx, dgdKey, "row 10: DGD inventoried once allowed")
	events.wait(t, EventReasonWatchRecovered, "DynamoGraphDeployment")
	if n := events.count(EventReasonWatchStarted, "DynamoGraphDeployment"); n != 0 {
		t.Errorf("row 10 keeps WatchRecovered; got %d WatchStarted", n)
	}
	if n := events.count(EventReasonWatchDisabled, "DynamoGraphDeployment"); n != 1 {
		t.Errorf("one WatchDisabled for row 3, got %d", n)
	}
	// LWS, still absent, has still nothing.
	requireNoHealthEntry(t, env.watchHealth, "LeaderWorkerSet")
	requireManagerRunning(t, env)
}

// Row 4: CRD updates that leave it Ready (labels, annotations) neither
// restart the runner nor produce events.
func TestIntegration_LateCRD_NoopUpdatesStayQuiet(t *testing.T) {
	opts := envTestOptions{omitExternalCRDs: []string{dgdCRDFile}, events: true}
	reads := observeCRDReads(&opts)
	env := startEnvTestWith(t, opts)
	ctx := context.Background()
	events := &eventLog{rec: env.recorder}

	waitAbsenceObserved(t, reads, dgdCRDName)
	installExternalCRD(t, env, dgdCRDFile)
	waitHealthy(t, env.watchHealth, "DynamoGraphDeployment")
	events.wait(t, EventReasonWatchStarted, "DynamoGraphDeployment")
	before, _ := env.watchHealth.Status("DynamoGraphDeployment")
	nBefore := len(events.all())

	for i := 0; i < 3; i++ {
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(crdGVK)
		if err := env.k8sClient.Get(ctx, types.NamespacedName{Name: dgdCRDName}, u); err != nil {
			t.Fatal(err)
		}
		labels := u.GetLabels()
		if labels == nil {
			labels = map[string]string{}
		}
		labels["late-crd-test/touch"] = fmt.Sprint(i)
		u.SetLabels(labels)
		ann := u.GetAnnotations()
		if ann == nil {
			ann = map[string]string{}
		}
		ann["late-crd-test/touch"] = fmt.Sprint(i)
		u.SetAnnotations(ann)
		if err := env.k8sClient.Update(ctx, u); err != nil {
			t.Fatal(err)
		}
		time.Sleep(300 * time.Millisecond)
	}
	time.Sleep(2 * time.Second)
	after, _ := env.watchHealth.Status("DynamoGraphDeployment")
	if !after.Healthy || !after.Since.Equal(before.Since) {
		t.Errorf("row 4: the runner must not restart; before %+v, after %+v", before, after)
	}
	if got := events.all(); len(got) != nBefore {
		t.Errorf("row 4: no new events; got %q", got[nBefore:])
	}
	if n := events.count(EventReasonWatchStarted, "DynamoGraphDeployment"); n != 1 {
		t.Errorf("WatchStarted exactly once, got %d", n)
	}
}

// crdFaultReader fails or blocks full-CRD reads for chosen CRD names and
// passes everything else through.
type crdFaultReader struct {
	client.Reader
	forbid map[string]bool // Get returns 403
	block  map[string]bool // Get blocks until its context ends
	calls  sync.Map        // name -> *atomic.Int64 read count
	absent sync.Map        // name -> true once a read returned NotFound
	// failNow, when set for a name, makes reads fail with the stored
	// error from now on (toggled by a test while the manager runs).
	failNow sync.Map // name -> error
}

// observeCRDReads routes the supervisor's CRD reads through a
// pass-through crdFaultReader so a test can see what discovery saw.
func observeCRDReads(opts *envTestOptions) *crdFaultReader {
	r := &crdFaultReader{}
	prev := opts.tune
	opts.tune = func(s *WatchSupervisor) {
		if prev != nil {
			prev(s)
		}
		r.Reader = s.APIReader
		s.CRDReader = r
	}
	return r
}

// waitAbsenceObserved waits until discovery has read the named CRD and
// found it absent. A test that installs a CRD "after startup" must wait
// for this: installed before the supervisor's first read, the CRD is
// indistinguishable from one present at startup and correctly keeps
// WatchRecovered (amendment A4).
func waitAbsenceObserved(t *testing.T, r *crdFaultReader, name string) {
	t.Helper()
	eventually(t, 30*time.Second, 50*time.Millisecond, func() error {
		if _, ok := r.absent.Load(name); !ok {
			return fmt.Errorf("discovery has not yet seen %s absent", name)
		}
		return nil
	})
}

func (r *crdFaultReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if u, ok := obj.(*unstructured.Unstructured); ok && u.GroupVersionKind() == crdGVK {
		n, _ := r.calls.LoadOrStore(key.Name, new(atomic.Int64))
		n.(*atomic.Int64).Add(1)
		if r.forbid[key.Name] {
			return apierrors.NewForbidden(schema.GroupResource{Group: "apiextensions.k8s.io", Resource: "customresourcedefinitions"},
				key.Name, fmt.Errorf("test: forbidden"))
		}
		if r.block[key.Name] {
			<-ctx.Done()
			return ctx.Err()
		}
		if v, ok := r.failNow.Load(key.Name); ok {
			return v.(error)
		}
		err := r.Reader.Get(ctx, key, obj, opts...)
		if apierrors.IsNotFound(err) {
			r.absent.Store(key.Name, true)
		}
		return err
	}
	return r.Reader.Get(ctx, key, obj, opts...)
}

// One candidate's failing (403) or hanging CRD read neither delays
// another candidate's late install nor counts as absence or removal:
// the failing candidate gets no runner, no health entry, no
// WatchDisabled and no sweep, even when the allowlist excludes it.
func TestIntegration_LateCRD_CandidateReadFailuresAreIsolated(t *testing.T) {
	faults := &crdFaultReader{
		forbid: map[string]bool{nimCRDName: true},
		block:  map[string]bool{lwsCRDName: true},
	}
	env := startEnvTestWith(t, envTestOptions{
		omitExternalCRDs: []string{dgdCRDFile, lwsCRDFile},
		events:           true,
		tune: func(s *WatchSupervisor) {
			faults.Reader = s.APIReader
			s.CRDReader = faults
		},
	})
	ctx := context.Background()
	events := &eventLog{rec: env.recorder}

	// LWS reads hang; install it so its worker is busy for the whole test.
	installExternalCRD(t, env, lwsCRDFile)
	waitAbsenceObserved(t, faults, dgdCRDName)
	installExternalCRD(t, env, dgdCRDFile)
	waitHealthy(t, env.watchHealth, "DynamoGraphDeployment")
	events.wait(t, EventReasonWatchStarted, "DynamoGraphDeployment")

	ns := "late-faults"
	mustCreateOptedInNamespace(t, env.k8sClient, ctx, ns)
	mustCreate(t, env.k8sClient, ctx, dynamoGraphDeployment(ns, "graph", "vllm",
		dynamoComponent("VllmWorker", "worker", "Qwen/Qwen3-0.6B", "nvcr.io/nvidia/ai-dynamo/vllm-runtime:0.6.0")))
	waitAIBOM(t, env, ctx, types.NamespacedName{Name: AIBOMNameForWorkload("nvidia.com", "DynamoGraphDeployment", "graph"), Namespace: ns},
		"DGD despite a hanging LWS read and a forbidden NIM read")

	// The forbidden candidate keeps being retried, and is never treated
	// as anything: excluding it must not disable or sweep it.
	setWorkloadKinds(t, env, gkDeployment, gkStatefulSet, gkDaemonSet, gkJob, gkCronJob, gkDGD, gkDCD, gkLWS, gkISVC, gkDSet)
	eventually(t, 15*time.Second, 200*time.Millisecond, func() error {
		n, ok := faults.calls.Load(nimCRDName)
		if !ok || n.(*atomic.Int64).Load() < 3 {
			return fmt.Errorf("forbidden NIM CRD read not retried yet")
		}
		return nil
	})
	time.Sleep(time.Second)
	for _, k := range []string{"NIMService", "LeaderWorkerSet"} {
		requireNoHealthEntry(t, env.watchHealth, k)
		if n := events.count(EventReasonWatchDisabled, k); n != 0 {
			t.Errorf("a failing read is not evidence: %d WatchDisabled for %s", n, k)
		}
	}
	// A6 surfaces failed full-CRD GETs under one discovery pseudo-kind;
	// individual workload watches must still remain unaffected.
	d := env.watchHealth.Degraded()
	if len(d) != 1 || d[0].Kind != CRDDiscoveryKind ||
		!strings.Contains(d[0].LastError, "NIMService") {
		t.Errorf("only aggregate CRDDiscovery may be Degraded by the forbidden candidate read: %+v", d)
	}
	requireManagerRunning(t, env)
}

// dgdNameClaimCRD claims the DGD plural as a short name in nvidia.com,
// so a DGD CRD created after it has NamesAccepted=False and is never
// Established until this CRD is deleted.
const dgdNameClaimCRD = `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: dgdnameclaims.nvidia.com
spec:
  group: nvidia.com
  scope: Namespaced
  names:
    plural: dgdnameclaims
    singular: dgdnameclaim
    kind: DGDNameClaim
    listKind: DGDNameClaimList
    shortNames: ["dynamographdeployments"]
  versions:
    - name: v1beta1
      served: true
      storage: true
      schema:
        openAPIV3Schema:
          type: object
          x-kubernetes-preserve-unknown-fields: true
`

func crdCondition(t *testing.T, c client.Client, name, condType string) string {
	t.Helper()
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(crdGVK)
	if err := c.Get(context.Background(), types.NamespacedName{Name: name}, u); err != nil {
		t.Fatal(err)
	}
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, cnd := range conds {
		m := cnd.(map[string]interface{})
		if m["type"] == condType {
			s, _ := m["status"].(string)
			return s
		}
	}
	return ""
}

// A CRD that already exists when the supervisor starts, but is not yet
// Established, is not a late install (amendment A4): when it becomes
// Established its kind starts and is announced with WatchRecovered,
// never WatchStarted. Until then it is Unknown (row 1): no health entry.
func TestIntegration_LateCRD_PresentButNotEstablishedAtStartupKeepsWatchRecovered(t *testing.T) {
	env := startEnvTestWith(t, envTestOptions{
		omitExternalCRDs: []string{dgdCRDFile},
		events:           true,
		beforeManager: func(t *testing.T, c client.Client) {
			ctx := context.Background()
			claim := &unstructured.Unstructured{}
			if err := yaml.Unmarshal([]byte(dgdNameClaimCRD), &claim.Object); err != nil {
				t.Fatal(err)
			}
			mustCreate(t, c, ctx, claim)
			eventually(t, 30*time.Second, 100*time.Millisecond, func() error {
				if crdCondition(t, c, "dgdnameclaims.nvidia.com", "Established") != "True" {
					return fmt.Errorf("name-claim CRD not Established yet")
				}
				return nil
			})
			b, err := os.ReadFile(filepath.Join("..", "..", "config", "crd", "external", dgdCRDFile))
			if err != nil {
				t.Fatal(err)
			}
			dgd := &unstructured.Unstructured{}
			if err := yaml.Unmarshal(b, &dgd.Object); err != nil {
				t.Fatal(err)
			}
			mustCreate(t, c, ctx, dgd)
			eventually(t, 30*time.Second, 100*time.Millisecond, func() error {
				if crdCondition(t, c, dgdCRDName, "NamesAccepted") != "False" {
					return fmt.Errorf("DGD CRD names not yet rejected")
				}
				return nil
			})
			if crdCondition(t, c, dgdCRDName, "Established") == "True" {
				t.Fatal("precondition: the DGD CRD must exist but not be Established when the manager starts")
			}
		},
	})
	ctx := context.Background()
	events := &eventLog{rec: env.recorder}

	waitHealthy(t, env.watchHealth, "DynamoComponentDeployment")
	time.Sleep(time.Second)
	requireNoHealthEntry(t, env.watchHealth, "DynamoGraphDeployment")

	// Lift the name conflict: the existing DGD CRD becomes Established.
	claim := &unstructured.Unstructured{}
	claim.SetGroupVersionKind(crdGVK)
	claim.SetName("dgdnameclaims.nvidia.com")
	if err := env.k8sClient.Delete(ctx, claim); err != nil {
		t.Fatal(err)
	}
	eventually(t, 30*time.Second, 100*time.Millisecond, func() error {
		if err := env.k8sClient.Get(ctx, types.NamespacedName{Name: "dgdnameclaims.nvidia.com"}, claim); !apierrors.IsNotFound(err) {
			return fmt.Errorf("name-claim CRD not gone yet (err=%v)", err)
		}
		return nil
	})
	// The API server's naming controller re-checks the group's other
	// CRDs when one is deleted, but can do so while its cache still holds
	// the terminating claim, and then nothing re-checks the DGD CRD. A
	// label change on it is an update event the naming controller acts
	// on; it keeps the CRD's UID, so the supervisor must still treat it
	// as present at startup.
	eventually(t, 60*time.Second, 2*time.Second, func() error {
		if crdCondition(t, env.k8sClient, dgdCRDName, "Established") == "True" {
			return nil
		}
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(crdGVK)
		if err := env.k8sClient.Get(ctx, types.NamespacedName{Name: dgdCRDName}, u); err != nil {
			return err
		}
		labels := u.GetLabels()
		if labels == nil {
			labels = map[string]string{}
		}
		labels["late-crd-test/renaming-nudge"] = fmt.Sprint(time.Now().UnixNano())
		u.SetLabels(labels)
		if err := env.k8sClient.Update(ctx, u); err != nil && !apierrors.IsConflict(err) {
			return err
		}
		return fmt.Errorf("DGD CRD not Established yet; nudged")
	})
	waitCRDServes(t, env, dgdCRDName, "v1beta1")
	waitHealthy(t, env.watchHealth, "DynamoGraphDeployment")
	events.wait(t, EventReasonWatchRecovered, "DynamoGraphDeployment")

	ns := "late-preexisting"
	mustCreateOptedInNamespace(t, env.k8sClient, ctx, ns)
	mustCreate(t, env.k8sClient, ctx, dynamoGraphDeployment(ns, "graph", "vllm",
		dynamoComponent("VllmWorker", "worker", "Qwen/Qwen3-0.6B", "nvcr.io/nvidia/ai-dynamo/vllm-runtime:0.6.0")))
	waitAIBOM(t, env, ctx, types.NamespacedName{Name: AIBOMNameForWorkload("nvidia.com", "DynamoGraphDeployment", "graph"), Namespace: ns},
		"a CRD Established after startup inventories its CRs")
	time.Sleep(time.Second)
	if n := events.count(EventReasonWatchStarted, "DynamoGraphDeployment"); n != 0 {
		t.Errorf("a CRD present at startup must keep WatchRecovered; got %d WatchStarted; events %q", n, events.all())
	}
	if n := events.count(EventReasonWatchRecovered, "DynamoGraphDeployment"); n != 1 {
		t.Errorf("exactly one WatchRecovered, got %d; events %q", n, events.all())
	}
	requireManagerRunning(t, env)
}

// A6: two candidate errors share one Degraded health entry. Clearing
// one failure must not hide the other, and running watches continue.
func TestIntegration_CRDDiscovery_CandidateReadFailureRecovers(t *testing.T) {
	opts := envTestOptions{events: true}
	reads := observeCRDReads(&opts)
	env := startEnvTestWith(t, opts)
	events := &eventLog{rec: env.recorder}
	waitHealthy(t, env.watchHealth, CRDDiscoveryKind)
	waitHealthy(t, env.watchHealth, dgd)

	reads.failNow.Store(dgdCRDName, apierrors.NewForbidden(
		schema.GroupResource{Group: "apiextensions.k8s.io", Resource: "customresourcedefinitions"},
		dgdCRDName, fmt.Errorf("test access denied")))
	touchCRDLabel(t, env.k8sClient, dgdCRDName, "a6-forbidden")
	waitDegradedWith(t, env.watchHealth, CRDDiscoveryKind, "DynamoGraphDeployment")
	if !env.watchHealth.IsHealthy(dgd) {
		t.Fatal("failed CRD read must not stop an already running watch")
	}
	const nimName = "nimservices.apps.nvidia.com"
	reads.failNow.Store(nimName, apierrors.NewTimeoutError("A6 injected timeout", 1))
	touchCRDLabel(t, env.k8sClient, nimName, "a6-timeout")
	waitDegradedWith(t, env.watchHealth, CRDDiscoveryKind, "NIMService")
	reads.failNow.Delete(dgdCRDName)
	touchCRDLabel(t, env.k8sClient, dgdCRDName, "a6-dgd-recovers")
	eventually(t, 30*time.Second, 100*time.Millisecond, func() error {
		st, _ := env.watchHealth.Status(CRDDiscoveryKind)
		if !st.Degraded() || strings.Contains(st.LastError, "DynamoGraphDeployment") ||
			!strings.Contains(st.LastError, "NIMService") {
			return fmt.Errorf("first candidate not cleared while second remains: %+v", st)
		}
		return nil
	})
	reads.failNow.Delete(nimName)
	touchCRDLabel(t, env.k8sClient, nimName, "a6-nim-recovers")
	waitHealthy(t, env.watchHealth, CRDDiscoveryKind)
	if n := events.count(EventReasonWatchUnhealthy, CRDDiscoveryKind); n != 1 {
		t.Fatalf("one aggregate WatchUnhealthy expected, got %d: %q", n, events.all())
	}
	if n := events.count(EventReasonWatchRecovered, CRDDiscoveryKind); n != 2 {
		t.Fatalf("initial sync and one aggregate recovery expected, got %d: %q", n, events.all())
	}
	requireManagerRunning(t, env)
}
