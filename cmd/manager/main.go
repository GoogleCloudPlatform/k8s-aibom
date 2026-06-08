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

// Command manager is the k8s-aibom controller entrypoint.
//
// Wiring (Checkpoint 5):
//
//   - A *config.Store is constructed at startup with the compiled-in
//     defaults (no env-var-based sink construction; that path was
//     removed when AIBOMControllerConfig became the canonical config
//     surface).
//   - The AIBOMControllerConfigReconciler watches the singleton
//     AIBOMControllerConfig CR and updates the Store on change.
//   - Every WorkloadReconciler (Deployment/StatefulSet/DaemonSet/
//     KServe) reads the Store at the top of its reconcile loop; the
//     loaded *Snapshot is threaded through scrape, BOM build, sink
//     fan-out, and status assembly. Hot-reload is structural: the
//     load-once invariant is enforced by parameter-passing, not
//     contributor discipline.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	aibomv1alpha1 "github.com/GoogleCloudPlatform/k8s-aibom/api/v1alpha1"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/bom"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/config"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/controller"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/scraper"
)

// controllerVersion is the version label stamped into emitted BOMs.
// Pre-release while the project is in early development; will be
// ldflags-stamped at build time when releases start.
const controllerVersion = "0.1.0"

// Downward-API env var names. The Helm chart and install.yaml MUST
// populate these via `valueFrom.fieldRef` (Phase 15 chart dependency
// documented in docs/phase-deferrals.md). If unset, the controller
// degrades gracefully: it runs, but the startup K8s Event and the
// AIBOMControllerConfigMissing/Deleted events lose their involvedObject
// target and are not emitted.
const (
	envPodName      = "POD_NAME"
	envPodNamespace = "POD_NAMESPACE"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(aibomv1alpha1.AddToScheme(scheme))
}

func main() {
	var (
		metricsAddr          string
		probeAddr            string
		enableLeaderElection bool
	)

	flag.StringVar(&metricsAddr, "metrics-bind-address", "127.0.0.1:8080",
		"The address the metrics endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081",
		"The address the health probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election so only one manager replica is active at a time.")

	zapOpts := zap.Options{Development: false}
	zapOpts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOpts)))
	log := ctrl.Log.WithName("setup")

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "k8s-aibom.aibom.k8saibom.dev",
	})
	if err != nil {
		log.Error(err, "unable to construct manager")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		log.Error(err, "unable to add healthz check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		log.Error(err, "unable to add readyz check")
		os.Exit(1)
	}

	// Resolve the controller's own Pod from downward-API env vars. Used
	// as the involvedObject for the startup Event and the
	// AIBOMControllerConfigReconciler's no-CR events. Nil-tolerant
	// downstream: tests and `go run` scenarios without env vars skip
	// these events with a debug log.
	controllerPod := podObjectReference()
	if controllerPod == nil {
		log.Info("POD_NAME / POD_NAMESPACE not set; controller-self K8s Events disabled. " +
			"In production, the Helm chart sets these via downward API.")
	}

	// Controller-namespace resolution: the ClientSinkFactory looks up
	// Secrets referenced by AIBOMControllerConfig in the controller's
	// own namespace. Falls back to "k8s-aibom-system" if POD_NAMESPACE
	// is unset; production deployments always set it.
	controllerNamespace := os.Getenv(envPodNamespace)
	if controllerNamespace == "" {
		controllerNamespace = "k8s-aibom-system"
	}

	// Construct the ConfigStore with compiled-in defaults. The
	// AIBOMControllerConfigReconciler hot-reloads it when the CR is
	// applied / changed / deleted.
	configStore := config.NewStore(config.DefaultSnapshot())

	loader := &config.Loader{
		Client: mgr.GetClient(),
		SinkFactory: &config.ClientSinkFactory{
			Client:            mgr.GetClient(),
			Namespace:         controllerNamespace,
			ControllerVersion: controllerVersion,
		},
	}

	if err := (&controller.AIBOMControllerConfigReconciler{
		Client:        mgr.GetClient(),
		Loader:        loader,
		ConfigStore:   configStore,
		Recorder:      mgr.GetEventRecorderFor("k8s-aibom-config"), //nolint:staticcheck
		ControllerPod: controllerPod,
	}).SetupWithManager(mgr); err != nil {
		log.Error(err, "unable to set up AIBOMControllerConfigReconciler")
		os.Exit(1)
	}

	// Wire per-kind reconcilers. All share the same ConfigStore; each
	// loads a Snapshot at the top of its reconcile loop. Hot-reload of
	// patterns, sinks, namespace selector, and inline threshold is a
	// property of the rotating Snapshot — no field on the reconciler
	// holds config-derived state.
	inferenceBase := controller.WorkloadReconciler{
		Client:            mgr.GetClient(),
		Scheme:            mgr.GetScheme(),
		Scraper:           scraper.NewMultiScraper(scraper.NewInferenceSpecScraper(nil), scraper.NewVectorDBSpecScraper(), scraper.NewAgentSpecScraper(), scraper.NewTrainingSpecScraper(), scraper.NewEvalSpecScraper()),
		BOMBuilder:        bom.NewBuilder(),
		StatusBuilder:     controller.NewStatusBuilder(),
		ConfigStore:       configStore,
		ControllerName:    "k8s-aibom",
		ControllerVersion: controllerVersion,
	}
	// KServe needs its own scraper (declared-not-inferred semantics,
	// different field paths). Shallow-copy the inference base and
	// swap the Scraper; everything else (including the shared
	// ConfigStore reference) is preserved.
	kserveBase := inferenceBase
	kserveBase.Scraper = scraper.NewKServeInferenceServiceScraper(nil)

	if err := (&controller.DeploymentReconciler{WorkloadReconciler: inferenceBase}).SetupWithManager(mgr); err != nil {
		log.Error(err, "unable to set up DeploymentReconciler")
		os.Exit(1)
	}
	if err := (&controller.StatefulSetReconciler{WorkloadReconciler: inferenceBase}).SetupWithManager(mgr); err != nil {
		log.Error(err, "unable to set up StatefulSetReconciler")
		os.Exit(1)
	}
	if err := (&controller.DaemonSetReconciler{WorkloadReconciler: inferenceBase}).SetupWithManager(mgr); err != nil {
		log.Error(err, "unable to set up DaemonSetReconciler")
		os.Exit(1)
	}

	if err := (&controller.JobReconciler{WorkloadReconciler: inferenceBase}).SetupWithManager(mgr); err != nil {
		log.Error(err, "unable to set up JobReconciler")
		os.Exit(1)
	}
	if _, err := mgr.GetRESTMapper().RESTMapping(schema.GroupKind{Group: "serving.kserve.io", Kind: "InferenceService"}, "v1beta1"); err == nil {
		if err := (&controller.KServeInferenceServiceReconciler{WorkloadReconciler: kserveBase}).SetupWithManager(mgr); err != nil {
			log.Error(err, "unable to set up KServeInferenceServiceReconciler")
			os.Exit(1)
		}
	} else if meta.IsNoMatchError(err) {
		log.Info("serving.kserve.io/v1beta1 InferenceService CRD not found; skipping KServe controller registration")
	} else {
		log.Error(err, "failed to query RESTMapper for InferenceService")
		os.Exit(1)
	}

	// Emit the startup K8s Event. Fires once per Pod startup — not on
	// leader-election handoff (which doesn't restart the process) and
	// not on every reconcile (which would spam the namespace's events).
	// Targets the controller's own Pod so operators see it in
	// `kubectl describe pod` and `kubectl get events`.
	//
	// The event is emitted BEFORE mgr.Start: the EventBroadcaster
	// buffers events while the manager is starting and flushes once
	// the API client is wired. If the controller crashes before
	// mgr.Start completes, the event is lost — that's correct (a Pod
	// that never started doesn't deserve a "started" event).
	emitStartupEvent(mgr.GetEventRecorderFor("k8s-aibom"), controllerPod, log) //nolint:staticcheck

	log.Info("starting manager", "version", version())
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		log.Error(err, "manager exited with error")
		os.Exit(1)
	}
}

// version is a placeholder until ldflags-stamped build metadata lands.
func version() string {
	return fmt.Sprintf("k8s-aibom %s", controllerVersion)
}

// podObjectReference materializes an ObjectReference to the
// controller's own Pod from the downward-API env vars. Returns nil if
// either is unset — callers degrade gracefully (Events that would have
// targeted the Pod are skipped with a debug log).
func podObjectReference() *corev1.ObjectReference {
	name := os.Getenv(envPodName)
	namespace := os.Getenv(envPodNamespace)
	if name == "" || namespace == "" {
		return nil
	}
	return &corev1.ObjectReference{
		APIVersion: "v1",
		Kind:       "Pod",
		Name:       name,
		Namespace:  namespace,
	}
}

// emitStartupEvent fires the once-per-Pod-startup signal that the
// k8s-aibom controller is alive. Complements AIBOMControllerConfigMissing
// (which fires only when the CR is absent on first reconcile); this
// fires unconditionally on every Pod boot.
//
// The event Message includes the controller version so operators can
// correlate the event with rollout revisions in
// `kubectl get events -n k8s-aibom-system`.
//
// Skipped (with a debug log) when the Pod reference is unavailable —
// see podObjectReference. Production deployments via the Phase 15 Helm
// chart MUST set POD_NAME / POD_NAMESPACE; the chart's templates
// enforce this so the startup signal isn't silently lost.
func emitStartupEvent(recorder record.EventRecorder, pod *corev1.ObjectReference, log logr.Logger) {
	if pod == nil {
		log.V(1).Info("startup K8s Event skipped: controller Pod reference unavailable")
		return
	}
	recorder.Event(
		pod,
		corev1.EventTypeNormal,
		"ControllerStarting",
		fmt.Sprintf("k8s-aibom controller starting; version=%s", controllerVersion),
	)
}
