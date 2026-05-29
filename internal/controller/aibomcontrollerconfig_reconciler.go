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
	"io"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/source"

	aibomv1alpha1 "github.com/GoogleCloudPlatform/k8s-aibom/api/v1alpha1"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/config"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/metrics"
)

// observedState is the AIBOMControllerConfigReconciler's view of the
// CR after the most recent reconcile. State transitions drive
// Kubernetes Event emission (one event per transition, NOT one per
// reconcile) — see emitTransitionEvent.
//
// Serialized access: the reconciler runs with MaxConcurrentReconciles=1
// (enforced explicitly in SetupWithManager), so reads/writes of
// lastObserved are sequenced by controller-runtime's per-controller
// worker pool. No mutex needed; the invariant is documented at the
// SetupWithManager call site.
type observedState int

const (
	stateUnknown observedState = iota // pre-first-reconcile
	stateMissing                      // CR does not exist
	stateValid                        // CR exists, parsed cleanly
	stateInvalid                      // CR exists, has LoadErrors
)

func (s observedState) String() string {
	switch s {
	case stateUnknown:
		return "unknown"
	case stateMissing:
		return "missing"
	case stateValid:
		return "valid"
	case stateInvalid:
		return "invalid"
	default:
		return fmt.Sprintf("observedState(%d)", int(s))
	}
}

// Event reasons emitted by AIBOMControllerConfigReconciler.
const (
	// EventReasonConfigMissing fires once when the controller first
	// observes that the singleton AIBOMControllerConfig CR is absent
	// from the cluster (fresh start, or deleted while running). The
	// involvedObject is the controller's own Pod (no CR exists to set
	// it on).
	EventReasonConfigMissing = "AIBOMControllerConfigMissing"

	// EventReasonConfigDeleted fires when the CR transitions from
	// existing-state (valid or invalid) to absent. Distinct from
	// EventReasonConfigMissing (which fires on startup-into-missing)
	// so operators can distinguish "fresh deploy without a CR" from
	// "someone deleted the CR." Targeted at the controller's Pod.
	EventReasonConfigDeleted = "AIBOMControllerConfigDeleted"

	// EventReasonConfigLoaded fires when the CR transitions to valid
	// from any non-valid state. Targeted at the CR.
	EventReasonConfigLoaded = "ConfigLoaded"

	// EventReasonConfigInvalid fires when the CR transitions to
	// invalid AND there is no prior valid snapshot to retain (fresh
	// start with an invalid CR). Targeted at the CR.
	EventReasonConfigInvalid = "ConfigInvalid"

	// EventReasonConfigInvalidUsingLKG fires when the CR transitions
	// from valid to invalid; the controller retains the last
	// successfully-loaded snapshot. Distinct event reason so
	// operators can tell at a glance that customer config is still
	// in effect (just not the latest version). Targeted at the CR.
	EventReasonConfigInvalidUsingLKG = "ConfigInvalidUsingLastKnownGood"

	// EventReasonConfigRecovered fires when the CR transitions from
	// invalid back to valid. Clears the Degraded condition.
	// Targeted at the CR.
	EventReasonConfigRecovered = "ConfigRecovered"
)

// snapshotLoader is the loader contract consumed by
// AIBOMControllerConfigReconciler. Production wires *config.Loader;
// tests substitute counting/failing wrappers. Defined here (not in
// internal/config) because the abstraction exists for THIS reconciler's
// testability — the loader package itself has only one concrete type.
type snapshotLoader interface {
	Load(ctx context.Context) (config.LoadResult, error)
}

// AIBOMControllerConfigReconciler watches the singleton
// AIBOMControllerConfig CR (the one named DefaultConfigName) and
// translates its spec into a runtime Snapshot stored in ConfigStore.
// The WorkloadReconciler family (Phase 12, Checkpoint 5) reads from
// the same ConfigStore on each reconcile to pick up changes without
// a process restart.
//
// State machine (full table in the Phase 12 proposal):
//
//	missing  → valid: emit ConfigLoaded; Store(spec snapshot)
//	missing  → invalid: emit ConfigInvalid; Store(defaults); Ready=False / RunningOnDefaults
//	valid    → invalid: emit ConfigInvalidUsingLastKnownGood; Store(MarkAsLastKnownGood(prev))
//	invalid  → valid: emit ConfigRecovered; Store(spec snapshot); clear Degraded
//	valid    → missing: emit AIBOMControllerConfigDeleted; Store(defaults)
//	invalid  → missing: emit AIBOMControllerConfigDeleted; Store(defaults)
//	any same → same: no event; no Store change beyond initial
//
// MaxConcurrentReconciles is locked at 1 in SetupWithManager — this is
// required for state-machine correctness, NOT a performance default.
// A future contributor copy-pasting a parallel-reconcile pattern from
// the WorkloadReconciler family would silently break the
// "emit-on-transition" invariant by interleaving observations.
type AIBOMControllerConfigReconciler struct {
	client.Client

	// Loader translates the CR into a Snapshot. Production wires
	// *config.Loader; the snapshotLoader interface exists so tests can
	// substitute counting/failing wrappers without touching the
	// internal/config package.
	Loader snapshotLoader

	// ConfigStore is the atomic pointer hosting the live Snapshot.
	// Read by every WorkloadReconciler on every reconcile.
	ConfigStore *config.Store

	// Recorder emits Kubernetes Events. The reconciler targets two
	// kinds of involvedObject: the CR itself (when it exists) and
	// the controller's own Pod (when it does not). Both use the
	// same recorder.
	Recorder record.EventRecorder

	// ControllerPod is an ObjectReference to the controller's own Pod,
	// used as the involvedObject for the no-CR events. Constructed in
	// cmd/manager from POD_NAME / POD_NAMESPACE downward-API env vars
	// (Phase 14 Helm-chart dependency; documented in
	// docs/phase-deferrals.md). May be nil in tests that exercise only
	// the CR-targeted events; the reconciler tolerates nil by
	// skipping no-CR event emission with a debug log line.
	ControllerPod *corev1.ObjectReference

	// ConfigName is the singleton CR's metadata.name. Defaults to
	// config.DefaultConfigName when empty. Exposed for test
	// determinism; production always uses the default.
	ConfigName string

	// lastObserved is the state machine's previous observation, used
	// to suppress duplicate events when the CR remains in the same
	// state across multiple reconciles. See the package-level note on
	// serialization (MaxConcurrentReconciles=1).
	lastObserved observedState
}

// Reconcile implements the state machine. Called by controller-runtime
// when the watched CR changes (Create/Update/Delete) AND on initial
// cache sync. Other-named CRs are filtered out by the predicate in
// SetupWithManager — this method assumes the request is for the
// configured name.
//
// Return semantics:
//   - nil error: state has been reconciled; controller-runtime will
//     not requeue until the next watch event.
//   - non-nil error: controller-runtime retries with exponential
//     backoff. Used for transient API failures only (Loader.Load
//     returning a Go error). Invalid CR is NOT a retry condition —
//     the snapshot/event/condition machinery handles it inline.
//
// +kubebuilder:rbac:groups=aibom.k8saibom.dev,resources=aibomcontrollerconfigs,verbs=get;list;watch
// +kubebuilder:rbac:groups=aibom.k8saibom.dev,resources=aibomcontrollerconfigs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
func (r *AIBOMControllerConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("aibomcontrollerconfig", req.Name)

	// Name filtering is the predicate's responsibility (see
	// SetupWithManager). No defensive recheck here — predicate is
	// the sole entry, and a redundant guard would rot into false
	// security without a backing test.
	configName := r.configName()

	result, err := r.Loader.Load(ctx)
	if err != nil {
		// Transient API failure. Don't mutate state or emit events;
		// controller-runtime will retry. lastObserved stays as-is so
		// the eventual successful reconcile emits the correct
		// transition event.
		return ctrl.Result{}, fmt.Errorf("load AIBOMControllerConfig: %w", err)
	}

	// Distinguish missing-CR (Source=compiled-defaults, no errors,
	// no LoadedAt yet) from invalid-CR (Source=compiled-defaults,
	// errors present). The Loader does not surface a "CR exists or
	// not" boolean directly, but the error count + Source combination
	// is sufficient.
	newState := classify(result)

	// Read the CURRENT stored snapshot to decide whether an invalid
	// result should preserve a last-known-good. The Source field on
	// the stored snapshot is the authoritative signal:
	//   - SourceConfigCR: prior valid load; preserve as LKG.
	//   - SourceCompiledDefaults: no prior valid load; stay on defaults.
	//   - SourceLastKnownGood: already retaining; do not re-stamp
	//     (avoid clobbering the generation that produced the LKG).
	prev := r.ConfigStore.Load()

	// Decide which snapshot to store.
	var toStore *config.Snapshot
	switch newState {
	case stateValid:
		toStore = result.Snapshot
	case stateInvalid:
		if prev != nil && prev.Source == config.SourceConfigCR {
			// valid → invalid: retain the prior valid snapshot,
			// re-stamped as LKG so the WorkloadReconciler family can
			// see "we're on a fallback."
			toStore = config.MarkAsLastKnownGood(prev)
		} else if prev != nil && prev.Source == config.SourceLastKnownGood {
			// invalid → invalid (still on LKG): keep the existing
			// LKG snapshot, do not re-stamp.
			toStore = prev
		} else {
			// fresh-start-invalid or repeated-defaults-invalid:
			// compiled defaults are the safe answer.
			toStore = result.Snapshot
		}
	case stateMissing:
		// CR absent (fresh start OR deletion). The Loader already
		// returned a DefaultSnapshot; just store it. Per the
		// approved design, deletion does NOT retain LKG — explicit
		// customer action overrides retention.
		toStore = result.Snapshot
	}

	retainSinks := false
	if toStore != nil && toStore.Source == config.SourceLastKnownGood {
		retainSinks = true
	}
	
	if prev != nil && prev != toStore && !retainSinks {
		// Delay closing to ensure active WorkloadReconciler in-flight
		// requests finish before the clients are torn down.
		oldSinks := prev.ExternalSinks
		time.AfterFunc(35*time.Second, func() { // DefaultExternalSinkTimeout is 30s
			for _, s := range oldSinks {
				if closer, ok := s.(io.Closer); ok {
					_ = closer.Close()
				}
			}
		})
	}
	r.ConfigStore.Store(toStore)

	// Emit transition events BEFORE patching status: an event that
	// fires once is more valuable than a status update that may
	// conflict and retry.
	r.emitTransitionEvent(ctx, r.lastObserved, newState, toStore)

	// Update CR status — only when the CR exists.
	if newState != stateMissing {
		if err := r.updateConditions(ctx, configName, newState, result, toStore); err != nil {
			// Status conflict means another writer raced us; the
			// next reconcile will reapply. Don't fail the reconcile
			// for a status conflict — the snapshot is already stored
			// and the event already emitted. Log and move on.
			if apierrors.IsConflict(err) {
				logger.V(1).Info("status conflict; will reapply on next reconcile", "err", err.Error())
				return ctrl.Result{Requeue: true}, nil
			} else if !apierrors.IsNotFound(err) {
				// NotFound means the CR was deleted between our
				// Load and our status patch — handled on the next
				// reconcile's missing-state transition.
				return ctrl.Result{}, fmt.Errorf("update status: %w", err)
			}
		}
	}

	r.lastObserved = newState
	return ctrl.Result{}, nil
}

// classify decides which observedState the Loader's result represents.
// Centralizing the decision keeps the Reconcile body short and gives
// tests a single function to assert on for the "what does this
// LoadResult mean" question.
func classify(result config.LoadResult) observedState {
	if result.Snapshot == nil {
		// Shouldn't happen — Loader contract is "snapshot is never
		// nil." Treat as missing.
		return stateMissing
	}
	if result.HasErrors() {
		return stateInvalid
	}
	if result.Snapshot.Source == config.SourceCompiledDefaults {
		// Successful Load returning compiled-defaults with no errors
		// is the Loader's "CR not found" signal.
		return stateMissing
	}
	return stateValid
}

// emitTransitionEvent fires the appropriate event for a state change.
// Same-state transitions emit nothing (the explicit anti-spam rule).
//
// CR-targeted events use the CR's ObjectReference; Pod-targeted
// events use r.ControllerPod (nil-tolerant for test ergonomics).
func (r *AIBOMControllerConfigReconciler) emitTransitionEvent(
	ctx context.Context,
	from, to observedState,
	stored *config.Snapshot,
) {
	if from == to {
		return
	}
	logger := log.FromContext(ctx)

	// CR-existence events need the CR ObjectReference; build it lazily.
	crRef := func() *corev1.ObjectReference {
		return &corev1.ObjectReference{
			APIVersion: aibomv1alpha1.GroupVersion.String(),
			Kind:       "AIBOMControllerConfig",
			Name:       r.configName(),
		}
	}

	emitOnPod := func(reason, msg string) {
		if r.ControllerPod == nil {
			logger.V(1).Info("skipping pod-targeted event (ControllerPod nil)",
				"reason", reason, "message", msg)
			return
		}
		r.Recorder.Event(r.ControllerPod, corev1.EventTypeWarning, reason, msg)
	}

	switch {
	case to == stateMissing && from == stateUnknown:
		emitOnPod(EventReasonConfigMissing,
			fmt.Sprintf("AIBOMControllerConfig/%s not found; running on compiled-in defaults. "+
				"Create the CR to override defaults.", r.configName()))
	case to == stateMissing && (from == stateValid || from == stateInvalid):
		emitOnPod(EventReasonConfigDeleted,
			fmt.Sprintf("AIBOMControllerConfig/%s was deleted; reverting to compiled-in defaults.",
				r.configName()))
	case to == stateValid && from == stateInvalid:
		r.Recorder.Event(crRef(), corev1.EventTypeNormal, EventReasonConfigRecovered,
			"AIBOMControllerConfig parsed cleanly; runtime snapshot updated and Degraded condition cleared.")
		r.Recorder.Event(crRef(), corev1.EventTypeNormal, EventReasonConfigLoaded,
			"AIBOMControllerConfig loaded; runtime snapshot updated.")
		metrics.ConfigReloads.WithLabelValues("recovered").Inc()
	case to == stateValid:
		// from is unknown or missing
		r.Recorder.Event(crRef(), corev1.EventTypeNormal, EventReasonConfigLoaded,
			"AIBOMControllerConfig loaded; runtime snapshot updated.")
		metrics.ConfigReloads.WithLabelValues("loaded").Inc()
	case to == stateInvalid:
		if stored != nil && stored.Source == config.SourceLastKnownGood {
			r.Recorder.Event(crRef(), corev1.EventTypeWarning, EventReasonConfigInvalidUsingLKG,
				"AIBOMControllerConfig spec is invalid; retaining last-known-good snapshot. "+
					"See Ready condition Message for the failing fields.")
			metrics.ConfigReloads.WithLabelValues("invalid_using_lkg").Inc()
		} else {
			r.Recorder.Event(crRef(), corev1.EventTypeWarning, EventReasonConfigInvalid,
				"AIBOMControllerConfig spec is invalid; running on compiled-in defaults. "+
					"See Ready condition Message for the failing fields.")
			metrics.ConfigReloads.WithLabelValues("invalid_using_defaults").Inc()
		}
	}
}

// updateConditions writes the Ready and Degraded conditions on the CR
// based on the just-classified state. Uses Status().Update on a fresh
// Get so conflicts are visible to the caller; the caller treats
// IsConflict as a soft failure (next reconcile retries).
func (r *AIBOMControllerConfigReconciler) updateConditions(
	ctx context.Context,
	configName string,
	state observedState,
	result config.LoadResult,
	stored *config.Snapshot,
) error {
	var cr aibomv1alpha1.AIBOMControllerConfig
	if err := r.Client.Get(ctx, types.NamespacedName{Name: configName}, &cr); err != nil {
		return err
	}

	now := metav1.Now()
	switch state {
	case stateValid:
		meta.SetStatusCondition(&cr.Status.Conditions, metav1.Condition{
			Type:               aibomv1alpha1.AIBOMControllerConfigConditionReady,
			Status:             metav1.ConditionTrue,
			Reason:             aibomv1alpha1.ReasonConfigLoaded,
			ObservedGeneration: cr.Generation,
			Message:            "Configuration loaded successfully; runtime snapshot is in effect.",
			LastTransitionTime: now,
		})
		// Explicitly clear Degraded by setting it to False — this
		// makes the recovery path visible to customers reading
		// kubectl describe.
		meta.SetStatusCondition(&cr.Status.Conditions, metav1.Condition{
			Type:               aibomv1alpha1.AIBOMControllerConfigConditionDegraded,
			Status:             metav1.ConditionFalse,
			Reason:             aibomv1alpha1.ReasonConfigLoaded,
			ObservedGeneration: cr.Generation,
			Message:            "Controller is running on the current spec snapshot.",
			LastTransitionTime: now,
		})
		if stored != nil {
			loadedAt := metav1.NewTime(stored.LoadedAt)
			cr.Status.LastLoadedAt = &loadedAt
		}
	case stateInvalid:
		meta.SetStatusCondition(&cr.Status.Conditions, metav1.Condition{
			Type:               aibomv1alpha1.AIBOMControllerConfigConditionReady,
			Status:             metav1.ConditionFalse,
			Reason:             aibomv1alpha1.ReasonConfigInvalid,
			ObservedGeneration: cr.Generation,
			Message:            result.AggregateMessage(),
			LastTransitionTime: now,
		})
		degradedReason := aibomv1alpha1.ReasonRunningOnDefaults
		degradedMsg := "Spec is invalid; running on compiled-in defaults. Edit spec to clear Ready=False."
		if stored != nil && stored.Source == config.SourceLastKnownGood {
			degradedReason = aibomv1alpha1.ReasonRunningOnLastKnownGood
			degradedMsg = fmt.Sprintf(
				"Spec is invalid; retaining last-known-good snapshot from generation %d. "+
					"Edit spec to clear Ready=False.",
				stored.SourceGeneration,
			)
		}
		meta.SetStatusCondition(&cr.Status.Conditions, metav1.Condition{
			Type:               aibomv1alpha1.AIBOMControllerConfigConditionDegraded,
			Status:             metav1.ConditionTrue,
			Reason:             degradedReason,
			ObservedGeneration: cr.Generation,
			Message:            degradedMsg,
			LastTransitionTime: now,
		})
		// LastLoadedAt is NOT advanced — invalid loads do not count
		// as a successful load. Customer reading "last loaded 10
		// minutes ago" understands the controller is not on current spec.
	case stateMissing:
		// No CR to patch; caller skips updateConditions on missing.
		return nil
	}

	cr.Status.ObservedGeneration = cr.Generation
	return r.Client.Status().Update(ctx, &cr)
}

// configName returns the configured name, defaulting to
// config.DefaultConfigName.
func (r *AIBOMControllerConfigReconciler) configName() string {
	if r.ConfigName != "" {
		return r.ConfigName
	}
	return config.DefaultConfigName
}

// SetupWithManager registers the reconciler with the manager and wires
// the singleton-only predicate. MaxConcurrentReconciles is locked at 1
// — this is required for state-machine correctness, not a performance
// default. A future contributor adding > 1 anywhere in the controller
// options would silently interleave observations and break the
// emit-on-transition invariant.
func (r *AIBOMControllerConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	configName := r.configName()
	nameIsSingleton := func(obj client.Object) bool {
		return obj.GetName() == configName
	}
	singletonPredicate := predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return nameIsSingleton(e.Object)
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			if !nameIsSingleton(e.ObjectNew) {
				return false
			}
			// Ignore status-only updates (our own Status().Update
			// comes back as an event). spec changes bump
			// metadata.generation; status-only does not.
			return e.ObjectOld.GetGeneration() != e.ObjectNew.GetGeneration()
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return nameIsSingleton(e.Object)
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return nameIsSingleton(e.Object)
		},
	}

	startup := make(chan event.GenericEvent, 1)
	mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
		startup <- event.GenericEvent{
			Object: &aibomv1alpha1.AIBOMControllerConfig{
				ObjectMeta: metav1.ObjectMeta{Name: configName},
			},
		}
		<-ctx.Done()
		return nil
	}))

	return ctrl.NewControllerManagedBy(mgr).
		Named("aibomcontrollerconfig").
		For(
			&aibomv1alpha1.AIBOMControllerConfig{},
			builder.WithPredicates(singletonPredicate),
		).
		WatchesRawSource(source.Channel(startup, &handler.EnqueueRequestForObject{})).
		WithOptions(controller.Options{
			// REQUIRED for state-machine correctness. lastObserved
			// is unsynchronized; controller-runtime's per-controller
			// worker pool provides serialization at MaxConcurrent=1.
			// DO NOT raise this without also adding explicit
			// synchronization around lastObserved AND auditing the
			// transition-event emission for correctness under
			// interleaved observations.
			MaxConcurrentReconciles: 1,
		}).
		Complete(r)
}
