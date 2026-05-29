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
	"sync"
	"time"

	"crypto/sha256"
	"encoding/hex"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	aibomv1alpha1 "github.com/GoogleCloudPlatform/k8s-aibom/api/v1alpha1"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/bom"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/config"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/metrics"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/scraper"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/sink"
)

// WorkloadReconciler holds the kind-neutral dependencies and methods
// shared by every per-kind reconciler (DeploymentReconciler,
// StatefulSetReconciler, DaemonSetReconciler, KServeInferenceServiceReconciler).
// Per-kind types embed it; the Reconcile method on each per-kind type
// performs the kind-specific Get + pod listing + Workload preparation,
// then delegates to reconcileWorkload.
//
// The type is exported so external packages (cmd/manager) can
// construct per-kind reconcilers via the embedded-struct literal:
//
//	&controller.DeploymentReconciler{
//	    WorkloadReconciler: controller.WorkloadReconciler{
//	        Client:        mgr.GetClient(),
//	        Scheme:        mgr.GetScheme(),
//	        Scraper:       scraper.NewInferenceSpecScraper(nil),
//	        BOMBuilder:    bom.NewBuilder(),
//	        StatusBuilder: controller.NewStatusBuilder(),
//	        ConfigStore:   configStore,
//	        ControllerName:    "k8s-aibom",
//	        ControllerVersion: "0.1.0",
//	    },
//	}
type WorkloadReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	Scraper       scraper.Scraper
	BOMBuilder    *bom.Builder
	StatusBuilder *StatusBuilder

	// ConfigStore holds the live AIBOMControllerConfig-derived Snapshot.
	// Read once at the top of reconcileWorkload; the loaded *Snapshot is
	// threaded through every downstream call site for the duration of
	// that reconcile. This is the structural enforcement of the load-once
	// invariant: a single reconcile cannot see hot-reload mid-flight,
	// because every helper that needs config receives it as a parameter
	// (Scrape, emitToExternalSinks, namespace-opt-in check, threshold for
	// status-builder inline-vs-external decision).
	//
	// MUST NOT be nil. cmd/manager seeds the Store with DefaultSnapshot
	// at boot; the AIBOMControllerConfigReconciler hot-reloads it.
	ConfigStore *config.Store

	// ControllerName and ControllerVersion are stamped into BOM metadata
	// (metadata.tools) and into per-workload property blocks.
	ControllerName    string
	ControllerVersion string
}

// WorkloadReconcileRequest carries everything the kind-neutral reconcile
// logic needs to process a workload. Per-kind reconcilers construct this
// after their typed Get + pod listing + scraper.Workload preparation,
// then call WorkloadReconciler.reconcileWorkload.
//
// The struct form (over positional arguments) is deliberate: it leaves
// room for additional fields (Phase 13 will add per-workload config
// overrides) without breaking existing per-kind reconcilers, and it
// makes the kind-neutral code's required inputs explicit.
type WorkloadReconcileRequest struct {
	// Workload is the scraper-input view of this workload.
	Workload scraper.Workload

	// AIBOMName is the metadata.name of the AIBOM CR for this
	// workload. Convention: lowercase(kind) + "-" + workload-name
	// (see AIBOMNameForWorkload).
	AIBOMName string

	// SetOwnerReference sets the workload as the controller-reference
	// owner of the given AIBOM. The closure pattern lets the
	// kind-neutral logic call SetControllerReference with the typed
	// kind-specific owner without leaking the typed object into this
	// package's helper logic.
	SetOwnerReference func(*aibomv1alpha1.AIBOM) error

	// BOMBuildOptions and SummaryOptions are workload-identity carriers
	// for the BOM builder and status summary builder respectively.
	BOMBuildOptions bom.BuildOptions
	SummaryOptions  SummaryOptions

	// Generation is the workload's metadata.generation, recorded as
	// Status.ObservedGeneration.
	Generation int64
}

// reconcileWorkload runs the kind-neutral reconcile logic:
//
//  1. Namespace opt-in check (OptInLabel == "true").
//  2. Scrape via the configured scraper.Scraper.
//  3. Skip if BOMInputs.Confidence is Unresolved (no inference signal).
//  4. Compute input hash; consult existing AIBOM CR for dedup.
//     - Match -> fast path: BuildFastPathStatus + Status().Update.
//     - Bootstrap race (AIBOM exists, Status.LastReconciled nil) -> defer.
//  5. Build the BOM.
//  6. Fan out to external sinks in parallel (see emitToExternalSinks).
//  7. Build the full AIBOMStatus.
//  8. CreateOrUpdate the AIBOM CR with the caller-supplied owner ref.
//  9. Status().Update the AIBOM with the full status.
//
// Returns standard ctrl.Result semantics. Conflict on Status().Update
// requeues; all other errors propagate to the caller.
func (r *WorkloadReconciler) reconcileWorkload(ctx context.Context, req WorkloadReconcileRequest) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Load-once: every config-driven decision in this reconcile uses
	// the same Snapshot pointer. Hot-reload via the
	// AIBOMControllerConfigReconciler swaps the Store atomically; the
	// next reconcile sees the new snapshot, this one finishes on the
	// snapshot it started with. See WorkloadReconciler.ConfigStore
	// godoc for the invariant.
	snap := r.ConfigStore.Load()

	// Namespace opt-in check, driven by the snapshot's pre-materialized
	// NamespaceSelector. Default selector requires
	// "aibom.k8saibom.dev/enabled=true"; customer-overridden selector can be
	// any valid LabelSelector. The selector is hot-reloadable through
	// the Snapshot rotation.
	var ns corev1.Namespace
	if err := r.Get(ctx, types.NamespacedName{Name: req.Workload.Namespace}, &ns); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get namespace %s: %w", req.Workload.Namespace, err)
	}
	if !snap.NamespaceSelector.Matches(labels.Set(ns.Labels)) {
		logger.V(1).Info("namespace does not match selector; skipping", "workload_namespace", req.Workload.Namespace, "workload_kind", req.Workload.Kind.Kind, "workload_name", req.Workload.Name)
		aibom := &aibomv1alpha1.AIBOM{}
		if err := r.Get(ctx, types.NamespacedName{Name: req.AIBOMName, Namespace: req.Workload.Namespace}, aibom); err == nil {
			if err := r.Delete(ctx, aibom); err != nil && !apierrors.IsNotFound(err) {
				return ctrl.Result{}, fmt.Errorf("delete orphaned AIBOM: %w", err)
			}
		} else if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("get orphaned AIBOM: %w", err)
		}
		return ctrl.Result{}, nil
	}

	// Scrape, passing the snapshot's patterns/allowlists. Scrapers are
	// stateless w.r.t. config; the load-once invariant is structurally
	// enforced because the *InferenceConfig flows through Scrape and
	// into every extraction helper as a parameter.
	inputs, err := r.Scraper.Scrape(ctx, req.Workload, snap.Patterns)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("scrape: %w", err)
	}
	if inputs.Confidence == scraper.ConfidenceUnresolved {
		// No inference signal. Per the discovery rule documented in
		// docs/scraper-heuristics.md, this workload is not classified
		// as inference; do not create an AIBOM.
		logger.V(1).Info("workload has no inference signal; skipping AIBOM creation", "workload_namespace", req.Workload.Namespace, "workload_kind", req.Workload.Kind.Kind, "workload_name", req.Workload.Name)
		aibom := &aibomv1alpha1.AIBOM{}
		if err := r.Get(ctx, types.NamespacedName{Name: req.AIBOMName, Namespace: req.Workload.Namespace}, aibom); err == nil {
			if err := r.Delete(ctx, aibom); err != nil && !apierrors.IsNotFound(err) {
				return ctrl.Result{}, fmt.Errorf("delete orphaned AIBOM: %w", err)
			}
		} else if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("get orphaned AIBOM: %w", err)
		}
		return ctrl.Result{}, nil
	}

	// Compute the input hash and consult any existing AIBOM CR to see
	// whether this reconcile would produce content-identical output.
	// If so, take the fast path: touch LastReconciled + ObservedGeneration
	// only, skipping BOM build and external sink emission entirely.
	if string(inputs.Category) != "" {
		req.SummaryOptions.WorkloadCategory = string(inputs.Category)
		req.BOMBuildOptions.WorkloadCategory = string(inputs.Category)
	}
	inputHash, err := scraper.HashBOMInputs(inputs)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("hash inputs: %w", err)
	}
	aibomKey := types.NamespacedName{
		Name:      req.AIBOMName,
		Namespace: req.Workload.Namespace,
	}
	var existing aibomv1alpha1.AIBOM
	existingErr := r.Get(ctx, aibomKey, &existing)
	if existingErr == nil {
		// Dedup fast path: input hash matches the previously-emitted BOM's
		// hash. Touch LastReconciled + ObservedGeneration only.
		if existing.Status.InputHash != "" && existing.Status.InputHash == inputHash {
			logger.V(1).Info("BOM inputs unchanged since last reconcile, skipping emit",
				"workload_namespace", req.Workload.Namespace,
				"workload_name", req.Workload.Name,
				"input_hash", inputHash[:12],
			)
			existing.Status = r.StatusBuilder.BuildFastPathStatus(&existing.Status, req.Generation)
			if err := r.Status().Update(ctx, &existing); err != nil {
				if apierrors.IsConflict(err) {
					return ctrl.Result{Requeue: true}, nil
				}
				return ctrl.Result{}, fmt.Errorf("update AIBOM status (fast path): %w", err)
			}
			return ctrl.Result{}, nil
		}
		// Bootstrap race: the AIBOM exists (a prior reconcile's
		// CreateOrUpdate landed) but its Status hasn't propagated
		// through the cache yet (the prior reconcile's
		// Status().Update is still in flight). This second reconcile
		// fired because the Owns-watch saw the AIBOM Create event
		// before the Status Update event. Treat this as "the prior
		// reconcile is doing the work" and exit — the cache will
		// eventually deliver the Status Update event and trigger a
		// reconcile that sees the populated InputHash and dedups
		// correctly.
		//
		// The signature we look for is "LastReconciled is nil" —
		// the AIBOM has never had its Status written. Distinct from
		// the upgrade case (LastReconciled populated, InputHash
		// empty because the prior controller version didn't emit it):
		// that case must proceed to the full build to populate
		// InputHash. See docs/open-decisions.md OD-002 for the
		// post-v1.0 customer-authored AIBOM constraint.
		if existing.Status.LastReconciled == nil {
			logger.V(1).Info("AIBOM Status not yet populated by prior reconcile, deferring to subsequent reconcile",
				"workload_namespace", req.Workload.Namespace,
				"workload_name", req.Workload.Name,
			)
			return ctrl.Result{}, nil
		}
	}
	if existingErr != nil && !apierrors.IsNotFound(existingErr) {
		// Real Get error (not "doesn't exist yet"). Surface it.
		return ctrl.Result{}, fmt.Errorf("get existing AIBOM: %w", existingErr)
	}

	// Build the BOM.
	doc, err := r.BOMBuilder.Build(inputs, req.BOMBuildOptions)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("build: %w", err)
	}

	// Fan out to external sinks. Each sink gets a bounded context so a
	// hung sink cannot stall reconciliation. Failures are recorded as
	// SinkResults and surfaced via conditions; they do NOT block the
	// CRD-status update below — that is the always-on terminal step.
	// Sinks come from the snapshot, not a constructor-time field, so a
	// CR edit + reconcile cycle on AIBOMControllerConfig hot-reloads
	// the fan-out targets.
	sinkResults := r.emitToExternalSinks(ctx, doc, req.Workload, snap.ExternalSinks)

	// Build the AIBOMStatus. The inline-vs-external threshold comes
	// from the snapshot per Checkpoint 5: customers can tune
	// inlineThresholdBytes in AIBOMControllerConfig and have it take
	// effect on the next reconcile without restart.
	status := r.StatusBuilder.BuildStatus(doc, req.SummaryOptions, sinkResults, req.Generation, inputHash, snap.InlineThreshold)

	// Phase 14: extraction errors and Stale wiring
	if len(inputs.Errors) > 0 {
		status.ConsecutiveErrors = existing.Status.ConsecutiveErrors + 1
	} else {
		status.ConsecutiveErrors = 0
	}

	staleThreshold := snap.StaleThresholdReconciles
	if staleThreshold <= 0 {
		staleThreshold = config.DefaultStaleThresholdReconciles
	}

	for _, e := range inputs.Errors {
		source := "unknown"
		if extErr, ok := e.(*scraper.ExtractionError); ok {
			source = string(extErr.EvidenceSource)
		}
		metrics.ScraperExtractionErrors.WithLabelValues(r.Scraper.Name(), source).Inc()
	}

	if status.ConsecutiveErrors >= staleThreshold {
		for i, c := range status.Conditions {
			if c.Type == aibomv1alpha1.ConditionStale {
				status.Conditions[i].Status = metav1.ConditionTrue
				status.Conditions[i].Reason = "ExtractionErrorsPersistent"

				var sources []string
				for _, e := range inputs.Errors {
					if extErr, ok := e.(*scraper.ExtractionError); ok {
						sources = append(sources, string(extErr.EvidenceSource))
					}
				}
				if len(sources) > 0 {
					status.Conditions[i].Message = fmt.Sprintf("workload produced extraction errors on %d consecutive reconciles; sources: %s", status.ConsecutiveErrors, strings.Join(sources, ", "))
				} else {
					status.Conditions[i].Message = fmt.Sprintf("workload produced extraction errors on %d consecutive reconciles", status.ConsecutiveErrors)
				}
				break
			}
		}
	}

	// CreateOrUpdate the AIBOM with owner reference, then update status.
	aibom := &aibomv1alpha1.AIBOM{
		ObjectMeta: metav1.ObjectMeta{
			Name:      req.AIBOMName,
			Namespace: req.Workload.Namespace,
		},
	}
	op, err := controllerutil.CreateOrUpdate(ctx, r.Client, aibom, func() error {
		// Spec: set on create AND update so a manual edit is rolled
		// back. The CR is fully controller-owned in v1.
		aibom.Spec = aibomv1alpha1.AIBOMSpec{
			WorkloadRef: aibomv1alpha1.WorkloadRef{
				APIVersion: formatAPIVersion(req.Workload.Kind),
				Kind:       req.Workload.Kind.Kind,
				Name:       req.Workload.Name,
			},
			BOMFormat:      string(doc.Format),
			BOMSpecVersion: doc.Version,
		}
		return req.SetOwnerReference(aibom)
	})
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("create-or-update AIBOM: %w", err)
	}
	logger.V(1).Info("CreateOrUpdate result", "op", op, "workload_namespace", req.Workload.Namespace, "workload_kind", req.Workload.Kind.Kind, "workload_name", req.Workload.Name)

	// Persist status separately (status subresource).
	aibom.Status = status
	if err := r.Status().Update(ctx, aibom); err != nil {
		if apierrors.IsConflict(err) {
			// Conflict means another reconcile cycle is racing us;
			// requeue and the next pass picks up the latest state.
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, fmt.Errorf("update AIBOM status: %w", err)
	}
	return ctrl.Result{}, nil
}

// emitToExternalSinks calls each configured external sink in parallel
// with a bounded per-sink context and collects results. Kind-neutral:
// the workload identity is carried via scraper.Workload, so the same
// helper serves Deployment / StatefulSet / DaemonSet / KServe paths.
//
// Concurrency model:
//   - Plain sync.WaitGroup. Not errgroup.Group.WithContext: per FR4.2,
//     sinks fail independently — one sink failing MUST NOT cancel the
//     other sinks' contexts.
//   - Each sink gets its own derived context with
//     DefaultExternalSinkTimeout. Parent ctx cancellation (reconcile
//     deadline exceeded) propagates to every sink simultaneously.
//
// Result ordering:
//   - The returned []SinkResult is in the same order as the passed-in
//     sinks slice (snapshot's configured order), regardless of
//     completion order. This makes "first successful sink wins" in
//     StatusBuilder.buildBOMDocumentRef deterministic across reconciles.
//
// The sinks parameter (rather than reading from r.ExternalSinks) is
// part of the Checkpoint 5 load-once invariant: the sink list is
// captured from the snapshot at the top of reconcileWorkload and
// passed in unchanged for the duration of this reconcile. Hot-reload
// affects the NEXT reconcile, not this one.
//
// Per the "errors are safe to surface" contract: sinks' returned
// errors are logged at info level and recorded in the SinkResult; the
// reconciler does NOT propagate them as a Reconcile error (which would
// trigger controller-runtime backoff and a re-reconcile). The next
// natural reconcile cycle retries via the standard event-driven path.
func (r *WorkloadReconciler) emitToExternalSinks(ctx context.Context, doc *bom.Document, w scraper.Workload, sinks []sink.Sink) []SinkResult {
	if len(sinks) == 0 || doc == nil {
		return nil
	}
	logger := log.FromContext(ctx)
	now := time.Now().UTC()
	meta := sink.SinkMeta{
		WorkloadKind:      w.Kind.Kind,
		WorkloadNamespace: w.Namespace,
		WorkloadName:      w.Name,
		WorkloadCategory:  string(w.Category),
		BOMHash:           doc.SHA256,
		Timestamp:         now,
	}

	results := make([]SinkResult, len(sinks))
	var wg sync.WaitGroup
	for i, s := range sinks {
		wg.Add(1)
		go func(i int, s sink.Sink) {
			defer wg.Done()
			sinkName := s.Name()
			writeOnly := s.WriteOnly()
			defer func() {
				if err := recover(); err != nil {
					results[i] = SinkResult{
						Sink:      sinkName,
						WriteOnly: writeOnly,
						Err:       fmt.Errorf("panic in sink: %v", err),
					}
					metrics.SinkEmitFailures.WithLabelValues(sinkName, w.Namespace, w.Kind.Kind).Inc()
					logger.Error(fmt.Errorf("panic in sink: %v", err), "external sink panicked",
						"sink", sinkName,
						"workload_namespace", w.Namespace,
						"workload_kind", w.Kind.Kind,
						"workload_name", w.Name,
					)
				}
			}()
			emitCtx, cancel := context.WithTimeout(ctx, DefaultExternalSinkTimeout)
			defer cancel()
			url, err := s.Emit(emitCtx, doc, meta)
			if err != nil {
				// Structured info-level log so Phase 14 can convert to
				// a Prometheus counter without code changes. The
				// curated label set is (sink, namespace, kind) —
				// workload name is intentionally excluded to bound
				// Prometheus cardinality (see docs/phase-deferrals.md
				// Phase 14 entry).
				metrics.SinkEmitFailures.WithLabelValues(s.Name(), w.Namespace, w.Kind.Kind).Inc()
				logger.Info("external sink failed",
					"sink", s.Name(),
					"workload_namespace", w.Namespace,
					"workload_kind", w.Kind.Kind,
					"workload_name", w.Name,
					"err", err,
				)
			}
			results[i] = SinkResult{
				Sink:      s.Name(),
				URL:       url,
				WriteOnly: s.WriteOnly(),
				Err:       err,
			}
		}(i, s)
	}
	wg.Wait()
	return results
}

// AIBOMNameForWorkload returns the canonical AIBOM resource name for a
// workload identified by kind + name. The pattern (lowercase kind +
// "-" + name) is documented in PRD §9.1. Names are DNS-1123-safe when
// both inputs are themselves DNS-1123-safe, which is guaranteed for
// Kubernetes object names and standard kind names.
func AIBOMNameForWorkload(group, kind, name string) string {
	base := strings.ToLower(kind) + "-" + name
	if group != "" && group != "core" {
		base = strings.ReplaceAll(strings.ToLower(group), ".", "-") + "-" + base
	}
	if len(base) > 253-13 {
		hash := sha256.Sum256([]byte(group + "/" + kind + "/" + name))
		hashHex := hex.EncodeToString(hash[:])[:12]
		base = strings.TrimRight(base[:253-13], "-.") + "-" + hashHex
	}
	return base
}

// formatAPIVersion returns the standard "group/version" string for a
// WorkloadKind, handling core kinds (empty Group) as just "version".
func formatAPIVersion(k scraper.WorkloadKind) string {
	if k.Group == "" {
		return k.Version
	}
	return k.Group + "/" + k.Version
}
