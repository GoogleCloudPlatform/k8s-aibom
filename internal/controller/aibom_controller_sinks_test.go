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
	"path/filepath"
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	aibomv1alpha1 "github.com/GoogleCloudPlatform/k8s-aibom/api/v1alpha1"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/bom"
	configpkg "github.com/GoogleCloudPlatform/k8s-aibom/internal/config"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/scraper"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/sink"
)

// recordingSink is a Sink that records every Emit call. Tests use it to
// verify the reconciler invokes external sinks with the right metadata
// and propagates results into the AIBOMStatus.
//
// delay, if non-zero, makes Emit sleep that long before returning. Used
// by parallel-fan-out tests to verify total elapsed time is bounded by
// max(sink_latency) rather than sum(sink_latency).
type recordingSink struct {
	name      string
	url       string
	err       error
	writeOnly bool
	delay     time.Duration
	mu        sync.Mutex
	emits     []sink.SinkMeta
}

func (r *recordingSink) Name() string { return r.name }

func (r *recordingSink) Emit(ctx context.Context, _ *bom.Document, meta sink.SinkMeta) (string, error) {
	if r.delay > 0 {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(r.delay):
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.emits = append(r.emits, meta)
	return r.url, r.err
}

func (r *recordingSink) HealthCheck(_ context.Context) error { return nil }
func (r *recordingSink) WriteOnly() bool                     { return r.writeOnly }

func (r *recordingSink) emitCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.emits)
}

// startEnvTestWithSinks is a variant of startEnvTest that wires
// additional external sinks into the reconciler. Factored out so the
// existing Phase 7 envtest tests don't change.
func startEnvTestWithSinks(t *testing.T, sinks []sink.Sink) *envTestEnv {
	t.Helper()
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(aibomv1alpha1.AddToScheme(scheme))

	te := &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join("..", "..", "config", "crd", "bases"),
			filepath.Join("..", "..", "config", "crd", "external"),
		},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := te.Start()
	if err != nil {
		t.Fatalf("envtest start: %v", err)
	}
	t.Cleanup(func() {
		if err := te.Stop(); err != nil {
			t.Logf("envtest stop: %v", err)
		}
	})

	k8sClient, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme: scheme,
		Controller: config.Controller{
			SkipNameValidation: ptr.To(true),
		},
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	// Seed a Snapshot containing the test sinks. Per Checkpoint 5,
	// WorkloadReconciler reads ExternalSinks from the snapshot rather
	// than a constructor field, so the harness has to materialize a
	// Store with the desired sinks before wiring reconcilers.
	testSnap := configpkg.DefaultSnapshot()
	testSnap.ExternalSinks = sinks
	configStore := configpkg.NewStore(testSnap)

	inferenceBase := WorkloadReconciler{
		Client:            mgr.GetClient(),
		Scheme:            mgr.GetScheme(),
		Scraper:           scraper.NewInferenceSpecScraper(nil),
		BOMBuilder:        bom.NewBuilder(),
		StatusBuilder:     NewStatusBuilder(),
		ConfigStore:       configStore,
		ControllerName:    "k8s-aibom",
		ControllerVersion: "0.1.0-test",
	}
	kserveBase := inferenceBase
	kserveBase.Scraper = scraper.NewKServeInferenceServiceScraper(nil)

	if err := (&DeploymentReconciler{WorkloadReconciler: inferenceBase}).SetupWithManager(mgr); err != nil {
		t.Fatalf("SetupWithManager DeploymentReconciler: %v", err)
	}
	if err := (&StatefulSetReconciler{WorkloadReconciler: inferenceBase}).SetupWithManager(mgr); err != nil {
		t.Fatalf("SetupWithManager StatefulSetReconciler: %v", err)
	}
	if err := (&DaemonSetReconciler{WorkloadReconciler: inferenceBase}).SetupWithManager(mgr); err != nil {
		t.Fatalf("SetupWithManager DaemonSetReconciler: %v", err)
	}
	if err := (&KServeInferenceServiceReconciler{WorkloadReconciler: kserveBase}).SetupWithManager(mgr); err != nil {
		t.Fatalf("SetupWithManager KServeInferenceServiceReconciler: %v", err)
	}
	mgrCtx, mgrCancel := context.WithCancel(context.Background())
	mgrErrCh := make(chan error, 1)
	go func() { mgrErrCh <- mgr.Start(mgrCtx) }()
	t.Cleanup(func() { mgrCancel(); <-mgrErrCh })
	if !mgr.GetCache().WaitForCacheSync(mgrCtx) {
		t.Fatal("manager cache failed to sync")
	}
	return &envTestEnv{
		cfg: cfg, k8sClient: k8sClient, testEnv: te, scheme: scheme,
		mgrCtx: mgrCtx, mgrCancel: mgrCancel, mgrErrCh: mgrErrCh,
	}
}

func TestIntegration_ExternalSink_SuccessPath(t *testing.T) {
	rs := &recordingSink{name: "recording", url: "gs://test-bucket/path.json"}
	env := startEnvTestWithSinks(t, []sink.Sink{rs})
	ctx := context.Background()

	nsName := "prod-sink-ok"
	depName := "vllm-sinkok"
	mustCreateOptedInNamespace(t, env.k8sClient, ctx, nsName)
	mustCreate(t, env.k8sClient, ctx, vllmDeployment(nsName, depName))

	aibomKey := types.NamespacedName{
		Name: "apps-deployment-" + depName, Namespace: nsName,
	}
	var got aibomv1alpha1.AIBOM
	eventually(t, 30*time.Second, 200*time.Millisecond, func() error {
		if err := env.k8sClient.Get(ctx, aibomKey, &got); err != nil {
			return err
		}
		if got.Status.Summary == nil {
			return errors.New("status.summary not yet populated")
		}
		// Don't accept the status until the sink has been called too.
		if rs.emitCount() == 0 {
			return errors.New("recording sink not yet invoked")
		}
		return nil
	})

	// Sink was invoked with correct metadata.
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if len(rs.emits) == 0 {
		t.Fatal("recording sink was never invoked")
	}
	last := rs.emits[len(rs.emits)-1]
	if last.WorkloadKind != "Deployment" {
		t.Errorf("SinkMeta.WorkloadKind = %q, want %q", last.WorkloadKind, "Deployment")
	}
	if last.WorkloadNamespace != nsName {
		t.Errorf("SinkMeta.WorkloadNamespace = %q, want %q", last.WorkloadNamespace, nsName)
	}
	if last.WorkloadName != depName {
		t.Errorf("SinkMeta.WorkloadName = %q, want %q", last.WorkloadName, depName)
	}
	if last.WorkloadCategory != "inference" {
		t.Errorf("SinkMeta.WorkloadCategory = %q", last.WorkloadCategory)
	}
	if last.BOMHash == "" {
		t.Error("SinkMeta.BOMHash is empty")
	}

	// Conditions: Synced=True, SinkFailed=False on success.
	conds := conditionsByType(got.Status.Conditions)
	if conds[aibomv1alpha1.ConditionSynced].Status != metav1.ConditionTrue {
		t.Errorf("Synced condition = %q, want True; reason=%q msg=%q",
			conds[aibomv1alpha1.ConditionSynced].Status,
			conds[aibomv1alpha1.ConditionSynced].Reason,
			conds[aibomv1alpha1.ConditionSynced].Message)
	}
	if conds[aibomv1alpha1.ConditionSinkFailed].Status != metav1.ConditionFalse {
		t.Errorf("SinkFailed condition = %q, want False", conds[aibomv1alpha1.ConditionSinkFailed].Status)
	}
}

// TestIntegration_ExternalSink_FailurePath_CRDStatusStillUpdated locks
// the auditor-facing SinkFailed condition message format. The message
// is part of the AIBOM external API surface: customer dashboards,
// alerting rules, and GRC pipelines may parse it. Changing the message
// format IS an API-surface change — update this test in the same
// commit and review the diff carefully in PR.
func TestIntegration_ExternalSink_FailurePath_CRDStatusStillUpdated(t *testing.T) {
	// Per FR4.2: a failing external sink does NOT prevent the CRD
	// status update. The AIBOM still appears with a populated Summary
	// and an inline BOM; the SinkFailed condition records the failure.
	failErr := errors.New("simulated bucket-not-found")
	rs := &recordingSink{name: "recording-failure", err: failErr}
	env := startEnvTestWithSinks(t, []sink.Sink{rs})
	ctx := context.Background()

	nsName := "prod-sink-fail"
	depName := "vllm-sinkfail"
	mustCreateOptedInNamespace(t, env.k8sClient, ctx, nsName)
	mustCreate(t, env.k8sClient, ctx, vllmDeployment(nsName, depName))

	aibomKey := types.NamespacedName{
		Name: "apps-deployment-" + depName, Namespace: nsName,
	}
	var got aibomv1alpha1.AIBOM
	eventually(t, 30*time.Second, 200*time.Millisecond, func() error {
		if err := env.k8sClient.Get(ctx, aibomKey, &got); err != nil {
			return err
		}
		if got.Status.Summary == nil {
			return errors.New("status.summary not yet populated despite sink failure")
		}
		// Conditions must reflect the failure.
		conds := conditionsByType(got.Status.Conditions)
		if conds[aibomv1alpha1.ConditionSinkFailed].Status != metav1.ConditionTrue {
			return fmt.Errorf("SinkFailed condition not yet True: %+v", conds[aibomv1alpha1.ConditionSinkFailed])
		}
		return nil
	})

	// CRD status was updated even though the sink failed:
	if got.Status.Summary == nil {
		t.Fatal("Summary is nil — sink failure prevented status update")
	}
	if got.Status.BOMDocument == nil || got.Status.BOMDocument.Inline == nil {
		t.Fatal("BOMDocument.Inline is nil — inline write blocked by sink failure")
	}
	// SinkFailed condition message must name the failing sink and reason.
	conds := conditionsByType(got.Status.Conditions)
	if !contains(conds[aibomv1alpha1.ConditionSinkFailed].Message, "recording-failure") {
		t.Errorf("SinkFailed message should name the failing sink; got: %q",
			conds[aibomv1alpha1.ConditionSinkFailed].Message)
	}
	if !contains(conds[aibomv1alpha1.ConditionSinkFailed].Message, "simulated bucket-not-found") {
		t.Errorf("SinkFailed message should include underlying error; got: %q",
			conds[aibomv1alpha1.ConditionSinkFailed].Message)
	}
	// Synced=False.
	if conds[aibomv1alpha1.ConditionSynced].Status != metav1.ConditionFalse {
		t.Errorf("Synced should be False on sink failure; got %q",
			conds[aibomv1alpha1.ConditionSynced].Status)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// TestIntegration_ExternalSinks_FailureIsolation_OneFailsOthersSucceed
// locks the per-sink failure isolation contract from PRD FR4.2: when
// one configured sink fails, OTHER configured sinks still receive the
// BOM. The reconciler's parallel-fanout via sync.WaitGroup must not
// cancel sibling sinks on a sink-level failure.
//
// Customers configure both GCS (archival) and a webhook (SIEM
// ingestion) precisely because they want defense in depth. A
// regression where GCS auth failure also blocks webhook delivery
// would defeat that, silently.
func TestIntegration_ExternalSinks_FailureIsolation_OneFailsOthersSucceed(t *testing.T) {
	failErr := errors.New("simulated archival-sink failure")
	failing := &recordingSink{name: "failing-archival", err: failErr}
	succeeding := &recordingSink{name: "succeeding-siem", url: "https://siem.example.com/ingest/abc"}

	env := startEnvTestWithSinks(t, []sink.Sink{failing, succeeding})
	ctx := context.Background()

	nsName := "prod-sink-isolation"
	depName := "vllm-isolation"
	mustCreateOptedInNamespace(t, env.k8sClient, ctx, nsName)
	mustCreate(t, env.k8sClient, ctx, vllmDeployment(nsName, depName))

	aibomKey := types.NamespacedName{Name: "apps-deployment-" + depName, Namespace: nsName}
	var got aibomv1alpha1.AIBOM
	eventually(t, 30*time.Second, 200*time.Millisecond, func() error {
		if err := env.k8sClient.Get(ctx, aibomKey, &got); err != nil {
			return err
		}
		// Wait for both sinks to have been called AND for the status
		// to reflect the outcome.
		if failing.emitCount() == 0 {
			return errors.New("failing sink not yet invoked")
		}
		if succeeding.emitCount() == 0 {
			return errors.New("succeeding sink not yet invoked — failure isolation broken")
		}
		if got.Status.Summary == nil {
			return errors.New("status.summary not yet populated")
		}
		conds := conditionsByType(got.Status.Conditions)
		if conds[aibomv1alpha1.ConditionSinkFailed].Status != metav1.ConditionTrue {
			return errors.New("SinkFailed condition not yet True")
		}
		return nil
	})

	// CRITICAL: succeeding sink was invoked despite the failing sink's
	// error. This is the property regression would silently break.
	if succeeding.emitCount() != 1 {
		t.Errorf("succeeding sink emitCount = %d, want 1 — one sink's failure must not block another's emission",
			succeeding.emitCount())
	}
	if failing.emitCount() != 1 {
		t.Errorf("failing sink emitCount = %d, want 1", failing.emitCount())
	}

	// SinkFailed condition mentions ONLY the failing sink, not the
	// succeeding one. A regression that attributed the failure to the
	// wrong sink would surface here.
	conds := conditionsByType(got.Status.Conditions)
	msg := conds[aibomv1alpha1.ConditionSinkFailed].Message
	if !contains(msg, "failing-archival") {
		t.Errorf("SinkFailed message should name the failing sink %q; got: %q",
			"failing-archival", msg)
	}
	if contains(msg, "succeeding-siem") {
		t.Errorf("SinkFailed message should NOT name the succeeding sink %q; got: %q",
			"succeeding-siem", msg)
	}
	if !contains(msg, "simulated archival-sink failure") {
		t.Errorf("SinkFailed message should include the underlying error; got: %q", msg)
	}

	// The BOM still landed in the CR status (inline). External delivery
	// failure does not block the always-on terminal CRD-status update
	// (FR4.2 again).
	if got.Status.BOMDocument == nil || got.Status.BOMDocument.Inline == nil {
		t.Error("BOMDocument.Inline is nil — sink failures should not block inline status writing")
	}

	// Synced=False because at least one sink failed.
	if conds[aibomv1alpha1.ConditionSynced].Status != metav1.ConditionFalse {
		t.Errorf("Synced = %q, want False when any sink failed", conds[aibomv1alpha1.ConditionSynced].Status)
	}
}
