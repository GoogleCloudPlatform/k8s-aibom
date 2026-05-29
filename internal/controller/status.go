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
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	aibomv1alpha1 "github.com/GoogleCloudPlatform/k8s-aibom/api/v1alpha1"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/bom"
)

// DefaultInlineThresholdBytes is the default size below which a BOM is
// inlined into the AIBOM CR's status; above which it is offloaded to an
// external sink (or truncated if none configured). Matches PRD FR5.4.
// Configurable via AIBOMControllerConfig once Phase 13 wires it.
const DefaultInlineThresholdBytes = 262144 // 256 KiB

// StatusBuilder produces a complete v1alpha1.AIBOMStatus from a finished
// BOM document plus the external-sink outcomes. The builder is the
// boundary between the controller's internal types and the deliberate
// external API.
//
// StatusBuilder is intentionally stateless and unaware of the kube client
// — it does not write the CR; the reconciler does. This keeps the
// builder unit-testable in isolation and the persistence concern
// separate.
//
// Per Checkpoint 5, the inline-vs-external threshold is NOT a field on
// the builder: it is passed per-call to BuildStatus by the
// WorkloadReconciler, which loads it from its Snapshot at the top of
// each reconcile. This makes the threshold hot-reloadable
// (AIBOMControllerConfig.spec.bomGeneration.inlineThresholdBytes) and
// keeps the load-once invariant structural rather than conventional.
type StatusBuilder struct {
	// Now returns the current time for LastReconciled and condition
	// transition times. Injectable for tests.
	Now func() time.Time
}

// NewStatusBuilder constructs a StatusBuilder with sensible defaults.
func NewStatusBuilder() *StatusBuilder {
	return &StatusBuilder{
		Now: time.Now,
	}
}

// SinkResult records the outcome of writing the BOM to one external sink.
// CRDStatus is NOT a sink for the purposes of SinkResult — the controller
// always updates the CR's status as the integral terminal step.
type SinkResult struct {
	// Sink is the sink name (matches Sink.Name()), e.g., "gcs", "webhook".
	Sink string
	// URL is the canonical retrieval URL on success.
	URL string
	// WriteOnly indicates that the URL is informational only (the sink
	// cannot serve the BOM back from it). Mirrors Sink.WriteOnly() at
	// the time of emission. The StatusBuilder uses this to prefer
	// retrievable sinks for ExternalBOMRef.
	WriteOnly bool
	// Err is non-nil on failure. The error is recorded into the
	// SinkFailed condition's Message; the builder doesn't propagate it
	// up beyond this struct.
	Err error
}

// BuildStatus produces the complete AIBOMStatus to write to the CR.
//
// Inline vs. external vs. truncated logic:
//
//   - doc.Size() <= InlineThresholdBytes → Inline (always populated;
//     external sinks may still be written to for archival).
//   - doc.Size() > InlineThresholdBytes AND a successful SinkResult is
//     present → External pointing at that sink.
//   - doc.Size() > InlineThresholdBytes AND no successful external sink →
//     Truncated with a clear TruncationReason. Summary remains populated.
//
// Condition logic:
//
//   - Ready=True when scrape + build succeeded and BOM was produced.
//   - Synced=True when every external sink that was attempted succeeded
//     (or zero external sinks were attempted — vacuously satisfied).
//   - SinkFailed=True when any external sink failed; Message names the
//     failing sinks.
//   - Stale=False (controller is reconciling; reconciler sets True
//     explicitly on persistent failure paths in later phases).
//
// inputHash is recorded on Status.InputHash so the next reconcile can
// dedup against it via BuildFastPathStatus.
//
// inlineThresholdBytes is the size at or below which the BOM is stored
// inline. Per Checkpoint 5, this is passed per-call rather than read
// from a StatusBuilder field: the WorkloadReconciler loads its snapshot
// once at the top of reconcileWorkload and passes snap.InlineThreshold
// here unchanged. A non-positive value falls back to
// DefaultInlineThresholdBytes (defensive: tests can pass 0 to get
// default behavior without restating the constant).
func (b *StatusBuilder) BuildStatus(doc *bom.Document, opts SummaryOptions, sinkResults []SinkResult, generation int64, inputHash string, inlineThresholdBytes int64) aibomv1alpha1.AIBOMStatus {
	if inlineThresholdBytes <= 0 {
		inlineThresholdBytes = DefaultInlineThresholdBytes
	}
	now := metav1.NewTime(b.Now())
	status := aibomv1alpha1.AIBOMStatus{
		Summary:            buildSummary(doc, opts),
		LastReconciled:     &now,
		ObservedGeneration: generation,
		InputHash:          inputHash,
	}
	if doc != nil {
		status.BOMHash = doc.SHA256
		status.BOMDocument = b.buildBOMDocumentRef(doc, sinkResults, inlineThresholdBytes)
	}
	status.Conditions = b.buildConditions(doc, sinkResults, now)
	return status
}

// BuildFastPathStatus returns a copy of prev with ONLY LastReconciled
// and ObservedGeneration updated. Used by the reconciler when the
// BOM input hash matches the previous reconcile, meaning no BOM
// regeneration or sink emission is warranted but the controller's
// "I've seen this generation" signal needs to update.
//
// Specifically preserved verbatim from prev:
//   - Summary (content-stable)
//   - BOMDocument (content-stable)
//   - BOMHash (content-stable; reflects the previously-emitted BOM)
//   - InputHash (still matches)
//   - Conditions including their LastTransitionTime values (per the
//     K8s condition convention, LastTransitionTime updates only on
//     status transitions, not on every reconcile)
//
// The caller must pass the existing AIBOMStatus (from a Get on the
// AIBOM CR) as prev; nil is treated as zero-value (which would
// effectively reset status — only meaningful for a "previous status
// missing" edge case, which the reconciler should treat as not-yet-
// reconciled and use the full BuildStatus path instead).
func (b *StatusBuilder) BuildFastPathStatus(prev *aibomv1alpha1.AIBOMStatus, generation int64) aibomv1alpha1.AIBOMStatus {
	now := metav1.NewTime(b.Now())
	if prev == nil {
		return aibomv1alpha1.AIBOMStatus{
			LastReconciled:     &now,
			ObservedGeneration: generation,
		}
	}
	// Deep copy via the generated DeepCopy method so we don't share
	// nested pointers with the input. The reconciler typically passes
	// a status from an in-memory client-cache object; mutating the
	// cache would be unsafe.
	out := prev.DeepCopy()
	out.LastReconciled = &now
	out.ObservedGeneration = generation
	return *out
}

func (b *StatusBuilder) buildBOMDocumentRef(doc *bom.Document, sinkResults []SinkResult, inlineThresholdBytes int64) *aibomv1alpha1.BOMDocumentRef {
	size := int64(doc.Size())
	ref := &aibomv1alpha1.BOMDocumentRef{
		Format:      string(doc.Format),
		SpecVersion: doc.Version,
		SHA256:      doc.SHA256,
		Size:        size,
	}
	if size <= inlineThresholdBytes {
		// Inline is always populated when small enough. The reconciler
		// may ALSO have called external sinks (for archival), but
		// the source-of-truth for `kubectl get aibom -o yaml` is the
		// inline copy here.
		data := append([]byte(nil), doc.JSON...) // defensive copy
		ref.Inline = &aibomv1alpha1.InlineBOM{Data: data}
		return ref
	}
	// Too large for inline. Selection algorithm: prefer retrievable
	// sinks (WriteOnly=false) over write-only sinks. Within each tier,
	// the first-configured successful sink wins. This means a customer
	// with both GCS (non-WriteOnly) and webhook (WriteOnly) configured
	// always gets the gs:// URL in ExternalBOMRef.URL — the webhook
	// delivery still happens, it just doesn't claim the External slot.
	if sr := firstSuccessful(sinkResults, false); sr != nil {
		ref.External = &aibomv1alpha1.ExternalBOMRef{
			Sink:      sr.Sink,
			URL:       sr.URL,
			WriteOnly: sr.WriteOnly,
		}
		return ref
	}
	if sr := firstSuccessful(sinkResults, true); sr != nil {
		ref.External = &aibomv1alpha1.ExternalBOMRef{
			Sink:      sr.Sink,
			URL:       sr.URL,
			WriteOnly: sr.WriteOnly,
		}
		return ref
	}
	// No external sink delivered it: honest degradation.
	ref.Truncated = true
	ref.TruncationReason = fmt.Sprintf(
		"BOM size %d bytes exceeds inline threshold %d bytes and no external sink is configured. Configure a GCS or webhook sink in AIBOMControllerConfig to retrieve the full BOM.",
		size, inlineThresholdBytes,
	)
	return ref
}

// firstSuccessful returns the first SinkResult with Err==nil, URL!="",
// and WriteOnly matching the wantWriteOnly argument. Returns nil if no
// such result is in the slice.
func firstSuccessful(sinkResults []SinkResult, wantWriteOnly bool) *SinkResult {
	for i := range sinkResults {
		sr := &sinkResults[i]
		if sr.Err != nil || sr.URL == "" {
			continue
		}
		if sr.WriteOnly != wantWriteOnly {
			continue
		}
		return sr
	}
	return nil
}

func (b *StatusBuilder) buildConditions(doc *bom.Document, sinkResults []SinkResult, now metav1.Time) []metav1.Condition {
	conds := []metav1.Condition{}

	// Ready: we have a BOM to record.
	readyStatus := metav1.ConditionTrue
	readyReason := aibomv1alpha1.ReasonBOMGenerated
	readyMessage := "BOM generated successfully"
	if doc == nil {
		readyStatus = metav1.ConditionFalse
		readyReason = aibomv1alpha1.ReasonBuildFailed
		readyMessage = "no BOM available"
	}
	conds = append(conds, metav1.Condition{
		Type:               aibomv1alpha1.ConditionReady,
		Status:             readyStatus,
		Reason:             readyReason,
		Message:            readyMessage,
		LastTransitionTime: now,
	})

	// Synced: every attempted external sink succeeded.
	var failedSinks []string
	for _, sr := range sinkResults {
		if sr.Err != nil {
			failedSinks = append(failedSinks, fmt.Sprintf("%s (%v)", sr.Sink, sr.Err))
		}
	}
	syncedStatus := metav1.ConditionTrue
	syncedReason := aibomv1alpha1.ReasonAllSinksOK
	syncedMessage := "all sinks updated"
	if len(sinkResults) == 0 {
		syncedReason = aibomv1alpha1.ReasonCRDStatusOnly
		syncedMessage = "no external sinks configured; CR status is the sole sink"
	}
	if len(failedSinks) > 0 {
		syncedStatus = metav1.ConditionFalse
		syncedReason = aibomv1alpha1.ReasonOneOrMoreSinksDown
		syncedMessage = "external sink failures: " + joinStrings(failedSinks, ", ")
	}
	conds = append(conds, metav1.Condition{
		Type:               aibomv1alpha1.ConditionSynced,
		Status:             syncedStatus,
		Reason:             syncedReason,
		Message:            syncedMessage,
		LastTransitionTime: now,
	})

	// SinkFailed: positive form of the failure signal, for alert grep.
	sinkFailedStatus := metav1.ConditionFalse
	sinkFailedReason := aibomv1alpha1.ReasonAllSinksOK
	sinkFailedMessage := "no sink failures"
	if len(failedSinks) > 0 {
		sinkFailedStatus = metav1.ConditionTrue
		sinkFailedReason = aibomv1alpha1.ReasonOneOrMoreSinksDown
		sinkFailedMessage = "external sink failures: " + joinStrings(failedSinks, ", ")
	}
	conds = append(conds, metav1.Condition{
		Type:               aibomv1alpha1.ConditionSinkFailed,
		Status:             sinkFailedStatus,
		Reason:             sinkFailedReason,
		Message:            sinkFailedMessage,
		LastTransitionTime: now,
	})

	// Stale: explicit False; reconciler sets True from elsewhere on
	// persistent reconcile failures (Phase 11+ retry/backoff plumbing).
	conds = append(conds, metav1.Condition{
		Type:               aibomv1alpha1.ConditionStale,
		Status:             metav1.ConditionFalse,
		Reason:             aibomv1alpha1.ReasonBOMGenerated,
		Message:            "controller reconciled this AIBOM during the current cycle",
		LastTransitionTime: now,
	})

	return conds
}

// joinStrings is strings.Join, factored out only so this file does not
// import "strings" alongside its other dependencies. (Keeping each
// helper file's import list tight is cosmetic; happy to remove.)
func joinStrings(parts []string, sep string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += sep
		}
		out += p
	}
	return out
}
