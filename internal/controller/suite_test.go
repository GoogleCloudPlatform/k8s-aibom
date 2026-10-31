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
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/goleak"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlcfg "sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	aibomv1beta1 "github.com/GoogleCloudPlatform/k8s-aibom/api/v1beta1"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/bom"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/config"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/scraper"
)

// envTestEnv carries the shared state set up once for all integration
// tests in this package.
type envTestEnv struct {
	cfg       *rest.Config
	k8sClient client.Client
	testEnv   *envtest.Environment
	scheme    *runtime.Scheme

	mgrCtx    context.Context
	mgrCancel context.CancelFunc
	mgrErrCh  chan error

	// configStore and watchHealth are nil for the config-reconciler
	// harness; startEnvTest sets them so allowlist tests can rotate the
	// snapshot the way the config reconciler does.
	configStore *config.Store
	watchHealth *WatchHealth
	// observer is the NamespaceObserver wired into every workload
	// reconciler, so tests can read its counts and fire its announcement
	// without depending on the settle timer.
	observer *NamespaceObserver
	// tracked is the TrackedKinds shared by every reconciler and the
	// supervisor.
	tracked *TrackedKinds
	// recorder receives the supervisor's controller-Pod events when
	// envTestOptions.events is set; nil otherwise.
	recorder *record.FakeRecorder
}

// envTestOptions varies startEnvTestWith for the late-CRD-discovery
// tests (Design 004 amendment A7, #226).
type envTestOptions struct {
	// omitExternalCRDs names files under config/crd/external/ that are
	// NOT installed at start; a test installs them later with
	// installExternalCRD.
	omitExternalCRDs []string
	// events records the supervisor's Pod events in env.recorder.
	events bool
	// tune runs after testWatchKnobs on the WatchSupervisor.
	tune func(*WatchSupervisor)
	// beforeManager runs against the API server after the CRDs are
	// installed and before the manager (and so the supervisor) starts,
	// to set up cluster state the supervisor must find at startup.
	beforeManager func(t *testing.T, c client.Client)
}

// startEnvTest starts an envtest API server, registers schemes, applies
// the CRDs from config/crd/bases, and starts a controller-runtime
// manager with the DeploymentReconciler registered. Tests call this
// once per integration test (TestMain isn't used here because that
// would lock all package tests behind a single shared envtest, which
// makes the lighter-weight unit tests pay the envtest startup cost).
func startEnvTest(t *testing.T) *envTestEnv {
	t.Helper()
	return startEnvTestWith(t, envTestOptions{})
}

// externalCRDDir returns the directory of external test CRDs to install
// at start: config/crd/external itself, or a temporary copy without
// the omitted files.
func externalCRDDir(t *testing.T, omit []string) string {
	t.Helper()
	src := filepath.Join("..", "..", "config", "crd", "external")
	if len(omit) == 0 {
		return src
	}
	skip := map[string]bool{}
	for _, f := range omit {
		skip[f] = true
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	for _, e := range entries {
		if e.IsDir() || skip[e.Name()] {
			delete(skip, e.Name())
			continue
		}
		b, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for f := range skip {
		t.Fatalf("omitExternalCRDs: %s is not in %s", f, src)
	}
	return dst
}

func startEnvTestWith(t *testing.T, opts envTestOptions) *envTestEnv {
	t.Helper()
	ctrl.SetLogger(zap.New(zap.WriteTo(os.Stderr), zap.UseDevMode(true)))
	t.Setenv("AIBOM_DISABLE_SSRF_CHECKS", "true")

	t.Cleanup(func() {
		goleak.VerifyNone(t,
			goleak.IgnoreTopFunction("sigs.k8s.io/controller-runtime/pkg/internal/testing/process.(*State).Start.func1"),
			goleak.IgnoreTopFunction("os/exec.(*Cmd).Wait"),
			goleak.IgnoreTopFunction("syscall.Syscall6"),
			goleak.IgnoreTopFunction("internal/poll.runtime_pollWait"),
			goleak.IgnoreTopFunction("golang.org/x/net/http2.(*clientConnReadLoop).run"),
		)
	})

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(aibomv1beta1.AddToScheme(scheme))

	te := &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join("..", "..", "config", "crd", "bases"),
			// External (test-only) CRDs: minimal KServe InferenceService
			// CRD. See config/crd/external/serving.kserve.io_inferenceservices.yaml.
			externalCRDDir(t, opts.omitExternalCRDs),
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
		// Multiple envtests within one Go test process spin up
		// successive managers; controller-runtime tracks controller
		// names globally in metrics, so the second registration would
		// fail without SkipNameValidation. In production there's only
		// one manager per process, so this is test-only safety.
		Controller: ctrlcfg.Controller{
			SkipNameValidation: ptr.To(true),
		},
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	configStore := config.NewStore(config.DefaultSnapshot())

	tracked := NewTrackedKinds()
	for _, gk := range []schema.GroupKind{
		{Group: "apps", Kind: "Deployment"}, {Group: "apps", Kind: "StatefulSet"}, {Group: "apps", Kind: "DaemonSet"},
		{Group: "batch", Kind: "Job"}, {Group: "batch", Kind: "CronJob"},
	} {
		tracked.Add(gk)
	}
	tracked.Follow(configStore)
	fanout := &AllowlistFanout{Reader: mgr.GetAPIReader()}
	fanout.Bind(configStore)
	observer := newNamespaceObserver(mgr.GetEventRecorderFor("k8s-aibom"), testNamespaceObservationSettle, 5*testNamespaceObservationSettle) //nolint:staticcheck
	inferenceBase := WorkloadReconciler{
		Client:            mgr.GetClient(),
		Scheme:            mgr.GetScheme(),
		Scraper:           scraper.NewMultiScraper(scraper.NewInferenceSpecScraper(nil), scraper.NewVectorDBSpecScraper(), scraper.NewAgentSpecScraper(), scraper.NewTrainingSpecScraper(), scraper.NewEvalSpecScraper()),
		BOMBuilder:        bom.NewBuilder(),
		StatusBuilder:     NewStatusBuilder(),
		ConfigStore:       configStore,
		ControllerName:    "k8s-aibom",
		ControllerVersion: "0.1.0-test",
		Tracked:           tracked,
		Fanout:            fanout,
		Observer:          observer,
	}
	// KServe needs its own scraper; everything else shared.
	kserveBase := inferenceBase
	kserveBase.Scraper = scraper.NewKServeInferenceServiceScraper(nil)
	dynamoBase := inferenceBase
	dynamoBase.Scraper = scraper.NewDynamoGraphDeploymentScraper(nil)
	nimBase := inferenceBase
	nimBase.Scraper = scraper.NewNIMServiceScraper(nil)
	lwsBase := inferenceBase
	lwsBase.Scraper = scraper.NewLeaderWorkerSetScraper(nil)
	dsetBase := inferenceBase
	dsetBase.Scraper = scraper.NewDisaggregatedSetScraper(nil)

	if err := (&DeploymentReconciler{WorkloadReconciler: inferenceBase}).SetupWithManager(mgr); err != nil {
		t.Fatalf("SetupWithManager DeploymentReconciler: %v", err)
	}
	if err := (&StatefulSetReconciler{WorkloadReconciler: inferenceBase}).SetupWithManager(mgr); err != nil {
		t.Fatalf("SetupWithManager StatefulSetReconciler: %v", err)
	}
	if err := (&DaemonSetReconciler{WorkloadReconciler: inferenceBase}).SetupWithManager(mgr); err != nil {
		t.Fatalf("SetupWithManager DaemonSetReconciler: %v", err)
	}
	if err := (&JobReconciler{WorkloadReconciler: inferenceBase}).SetupWithManager(mgr); err != nil {
		t.Fatalf("SetupWithManager JobReconciler: %v", err)
	}
	if err := (&CronJobReconciler{WorkloadReconciler: inferenceBase}).SetupWithManager(mgr); err != nil {
		t.Fatalf("SetupWithManager CronJobReconciler: %v", err)
	}
	// Third-party kinds through the supervisor, exactly as cmd/manager
	// wires them (Design 004), with test-speed knobs.
	watchHealth := NewWatchHealth()
	// An untyped nil recorder and pod unless events are asked for: a
	// typed nil *FakeRecorder in the interface would not read as nil.
	var recorder record.EventRecorder
	var pod *corev1.ObjectReference
	var fakeRecorder *record.FakeRecorder
	if opts.events {
		fakeRecorder = record.NewFakeRecorder(256)
		recorder = fakeRecorder
		pod = &corev1.ObjectReference{Kind: "Pod", Name: "k8s-aibom-0", Namespace: "k8s-aibom-system"}
	}
	tune := func(s *WatchSupervisor) {
		testWatchKnobs(s)
		if opts.tune != nil {
			opts.tune(s)
		}
	}
	if err := RegisterThirdPartyWatches(mgr, watchHealth, tracked, configStore, fanout, recorder, pod, []ThirdPartyWatch{
		(&KServeInferenceServiceReconciler{WorkloadReconciler: kserveBase}).Watch(),
		(&DynamoGraphDeploymentReconciler{WorkloadReconciler: dynamoBase}).Watch(),
		(&DynamoComponentDeploymentReconciler{WorkloadReconciler: dynamoBase}).Watch(),
		(&NIMServiceReconciler{WorkloadReconciler: nimBase}).Watch(),
		(&LeaderWorkerSetReconciler{WorkloadReconciler: lwsBase}).Watch(),
		(&DisaggregatedSetReconciler{WorkloadReconciler: dsetBase}).Watch(),
	}, tune); err != nil {
		t.Fatalf("RegisterThirdPartyWatches: %v", err)
	}

	if opts.beforeManager != nil {
		opts.beforeManager(t, k8sClient)
	}

	mgrCtx, mgrCancel := context.WithCancel(context.Background())
	mgrErrCh := make(chan error, 1)
	go func() {
		mgrErrCh <- mgr.Start(mgrCtx)
	}()
	t.Cleanup(func() {
		mgrCancel()
		if err := <-mgrErrCh; err != nil && !errors.Is(err, context.Canceled) {
			t.Logf("manager stopped with error: %v", err)
		}
	})

	// Wait for the manager's cache to sync before returning.
	if !mgr.GetCache().WaitForCacheSync(mgrCtx) {
		t.Fatal("manager cache failed to sync")
	}

	return &envTestEnv{
		cfg:         cfg,
		k8sClient:   k8sClient,
		testEnv:     te,
		scheme:      scheme,
		mgrCtx:      mgrCtx,
		mgrCancel:   mgrCancel,
		mgrErrCh:    mgrErrCh,
		configStore: configStore,
		watchHealth: watchHealth,
		observer:    observer,
		tracked:     tracked,
		recorder:    fakeRecorder,
	}
}

// eventually polls fn at small intervals until it returns nil or the
// timeout expires. Like gomega.Eventually but without the dependency.
func eventually(t *testing.T, timeout time.Duration, interval time.Duration, fn func() error) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		if err := fn(); err == nil {
			return
		} else {
			lastErr = err
		}
		time.Sleep(interval)
	}
	t.Fatalf("eventually timed out after %v: %v", timeout, lastErr)
}

// testWatchKnobs shortens the WatchSupervisor's production backoffs so
// envtests observe transitions in seconds.
func testWatchKnobs(s *WatchSupervisor) {
	s.InitialBackoff = 500 * time.Millisecond
	s.MaxBackoff = 2 * time.Second
	s.CacheSyncTimeout = 10 * time.Second
	s.ReprobeInterval = 500 * time.Millisecond
}
