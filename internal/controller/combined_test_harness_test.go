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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
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

// combinedEnv wraps envTestEnv with references to the live reconciler
// state under test. Tests use these references to assert intermediate
// state (e.g., "did the AIBOMControllerConfigReconciler observe the
// new CR and update the ConfigStore?") without having to peek at
// reconciler internals.
type combinedEnv struct {
	*envTestEnv

	configStore  *config.Store
	configRec    *AIBOMControllerConfigReconciler
	configRecOut *captureRecorder
}

// startCombinedEnvTest stands up an envtest with BOTH the
// AIBOMControllerConfigReconciler AND the WorkloadReconciler family
// (Deployment / StatefulSet / DaemonSet / KServe) wired through a
// shared ConfigStore. This mirrors the production wiring in
// cmd/manager/main.go and is the harness used by hot-reload and
// bootstrap-state integration tests.
//
// Distinct from startEnvTest (suite_test.go), which wires only the
// WorkloadReconciler family with no config reconciler — the existing
// tests in that harness pre-seed ConfigStore directly and the config
// reconciler would otherwise race them by overwriting on missing-CR.
func startCombinedEnvTest(t *testing.T) *combinedEnv {
	t.Helper()
	t.Setenv("AIBOM_DISABLE_SSRF_CHECKS", "true")

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
	t.Cleanup(func() { _ = te.Stop() })

	k8sClient, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme: scheme,
		Controller: ctrlcfg.Controller{
			SkipNameValidation: ptr.To(true),
		},
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	// Shared ConfigStore: seeded with compiled defaults; rotated by the
	// AIBOMControllerConfigReconciler when the CR is applied.
	configStore := config.NewStore(config.DefaultSnapshot())

	// Production-grade ClientSinkFactory so the end-to-end chain is
	// exercised. Secret lookups land in "k8s-aibom-system"; tests that
	// reference Secrets create them in that namespace.
	loader := &config.Loader{
		Client: mgr.GetClient(),
		SinkFactory: &config.ClientSinkFactory{
			Client:            mgr.GetClient(),
			Namespace:         "k8s-aibom-system",
			ControllerVersion: "0.1.0-test",
		},
	}

	configRecOut := &captureRecorder{}
	configRec := &AIBOMControllerConfigReconciler{
		Client:      mgr.GetClient(),
		Loader:      loader,
		ConfigStore: configStore,
		Recorder:    configRecOut,
		ControllerPod: &corev1.ObjectReference{
			APIVersion: "v1", Kind: "Pod",
			Name: "aibom-controller-test", Namespace: "k8s-aibom-system",
		},
	}
	if err := configRec.SetupWithManager(mgr); err != nil {
		t.Fatalf("SetupWithManager AIBOMControllerConfigReconciler: %v", err)
	}

	// Wire the workload reconciler family with the shared ConfigStore.
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

	// The controller's own namespace must exist for the
	// ClientSinkFactory to look up Secrets without surfacing
	// namespace-not-found errors. Created eagerly so individual tests
	// don't have to remember.
	mustCreateNamespace(t, k8sClient, context.Background(), "k8s-aibom-system")

	mgrCtx, mgrCancel := context.WithCancel(context.Background())
	mgrErrCh := make(chan error, 1)
	go func() { mgrErrCh <- mgr.Start(mgrCtx) }()
	t.Cleanup(func() {
		mgrCancel()
		<-mgrErrCh
	})
	if !mgr.GetCache().WaitForCacheSync(mgrCtx) {
		t.Fatal("manager cache failed to sync")
	}

	return &combinedEnv{
		envTestEnv: &envTestEnv{
			cfg: cfg, k8sClient: k8sClient, testEnv: te, scheme: scheme,
			mgrCtx: mgrCtx, mgrCancel: mgrCancel, mgrErrCh: mgrErrCh,
		},
		configStore:  configStore,
		configRec:    configRec,
		configRecOut: configRecOut,
	}
}

func mustCreateNamespace(t *testing.T, c client.Client, ctx context.Context, name string) {
	t.Helper()
	ns := &corev1.Namespace{}
	ns.Name = name
	if err := c.Create(ctx, ns); err != nil {
		t.Fatalf("create namespace %s: %v", name, err)
	}
}
