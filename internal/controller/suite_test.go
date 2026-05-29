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
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/goleak"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlcfg "sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	aibomv1alpha1 "github.com/GoogleCloudPlatform/k8s-aibom/api/v1alpha1"
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
}

// startEnvTest starts an envtest API server, registers schemes, applies
// the CRDs from config/crd/bases, and starts a controller-runtime
// manager with the DeploymentReconciler registered. Tests call this
// once per integration test (TestMain isn't used here because that
// would lock all package tests behind a single shared envtest, which
// makes the lighter-weight unit tests pay the envtest startup cost).
func startEnvTest(t *testing.T) *envTestEnv {
	t.Helper()
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
	utilruntime.Must(aibomv1alpha1.AddToScheme(scheme))

	te := &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join("..", "..", "config", "crd", "bases"),
			// External (test-only) CRDs: minimal KServe InferenceService
			// CRD. See config/crd/external/serving.kserve.io_inferenceservices.yaml.
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
	// KServe needs its own scraper; everything else shared.
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
	go func() {
		mgrErrCh <- mgr.Start(mgrCtx)
	}()
	t.Cleanup(func() {
		mgrCancel()
		<-mgrErrCh
	})

	// Wait for the manager's cache to sync before returning.
	if !mgr.GetCache().WaitForCacheSync(mgrCtx) {
		t.Fatal("manager cache failed to sync")
	}

	return &envTestEnv{
		cfg:       cfg,
		k8sClient: k8sClient,
		testEnv:   te,
		scheme:    scheme,
		mgrCtx:    mgrCtx,
		mgrCancel: mgrCancel,
		mgrErrCh:  mgrErrCh,
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
