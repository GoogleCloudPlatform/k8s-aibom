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

package config

import (
	"fmt"
	"strings"
)

// LoadError is one semantic-validation failure encountered during
// Loader.Load. Each error names a specific spec path AND a specific
// action the operator can take to fix it. Generic "config is invalid"
// errors are NEVER acceptable — they surface in
// AIBOMControllerConfig.status.conditions[Ready].Message where they
// are the only operator-facing diagnostic.
//
// Message-quality rule (auditor-facing precision applied to operators):
//
//	GOOD: "Sink 'audit-webhook' has Type=Webhook but spec.sinks[name=audit-webhook].webhook is nil. Set the webhook subfield with the sink configuration."
//	BAD:  "Invalid sink configuration."
//
// Tests in loader_test.go assert message shape on canonical failure
// modes to lock this property.
type LoadError struct {
	// Field is the JSON-path-style locator within the
	// AIBOMControllerConfig spec where the failure occurred.
	// Example: "spec.sinks[name=audit-webhook].webhook"
	Field string

	// Message is the actionable, customer-facing description. Must
	// include enough context that a human reading
	// AIBOMControllerConfig.status without access to controller logs
	// can identify and fix the problem.
	Message string
}

// Error implements the error interface so LoadError can be returned
// where a single error is expected. The format is "Field: Message"
// for log compatibility.
func (e LoadError) Error() string {
	if e.Field == "" {
		return e.Message
	}
	return fmt.Sprintf("%s: %s", e.Field, e.Message)
}

// LoadResult is what Loader.Load returns. The Snapshot is always
// non-nil (the fallback contract) — callers don't need to handle
// "no snapshot" cases. Errors is empty on success, populated with
// one or more LoadError entries on semantic failure.
//
// Per project memory "AIBOMControllerConfig v1 behavior", any
// non-empty Errors slice causes Snapshot to be a defaults snapshot
// (or the prior last-known-good, when the caller uses
// LoadWithFallback). The Loader itself returns defaults; the
// reconciler can swap to last-known-good after retrieval.
type LoadResult struct {
	Snapshot *Snapshot
	Errors   []LoadError
}

// HasErrors reports whether the load encountered any semantic
// failures. When true, Snapshot is the compiled-defaults fallback
// (or last-known-good if the caller upgraded the result).
func (r LoadResult) HasErrors() bool { return len(r.Errors) > 0 }

// AggregateMessage returns a single human-readable string summarizing
// all LoadErrors. Used as the Message on the Ready=False condition.
// Empty when HasErrors() is false.
func (r LoadResult) AggregateMessage() string {
	if !r.HasErrors() {
		return ""
	}
	parts := make([]string, 0, len(r.Errors))
	for _, e := range r.Errors {
		parts = append(parts, e.Error())
	}
	return strings.Join(parts, "; ")
}

// Error-message constructors. Centralized so all loader-side errors
// share the same auditor-facing precision standard and so tests can
// assert message shape against a known constructor.

// errSinkMissingTypeBody is emitted when a SinkConfig declares
// Type=GCS but the GCS field is nil (or Type=Webhook but Webhook is
// nil). The customer set the discriminator but forgot the body.
func errSinkMissingTypeBody(sinkName, sinkType string) LoadError {
	subfield := strings.ToLower(sinkType)
	return LoadError{
		Field: fmt.Sprintf("spec.sinks[name=%s].%s", sinkName, subfield),
		Message: fmt.Sprintf(
			"Sink %q has Type=%s but spec.sinks[name=%s].%s is nil. Set the %s subfield with the sink configuration, or change Type if a different sink was intended.",
			sinkName, sinkType, sinkName, subfield, subfield,
		),
	}
}

// errSinkExtraTypeBody is emitted when a SinkConfig has BOTH GCS and
// Webhook populated (or the body doesn't match Type). The customer's
// CR shape doesn't satisfy the "exactly one" rule the discriminator
// enforces.
func errSinkExtraTypeBody(sinkName, sinkType, extraField string) LoadError {
	return LoadError{
		Field: fmt.Sprintf("spec.sinks[name=%s].%s", sinkName, extraField),
		Message: fmt.Sprintf(
			"Sink %q has Type=%s but spec.sinks[name=%s].%s is also populated. Exactly one of {gcs, webhook} may be set per sink; the populated subfield must match Type.",
			sinkName, sinkType, sinkName, extraField,
		),
	}
}

// errSinkDuplicateName is emitted when two sinks in the list share
// the same Name. The +listMapKey=name marker on the CRD prevents this
// at apply time for server-side apply users; this loader-side check
// catches client-side apply / kubectl-create paths where the
// discriminator may not be enforced.
func errSinkDuplicateName(sinkName string) LoadError {
	return LoadError{
		Field: fmt.Sprintf("spec.sinks[name=%s]", sinkName),
		Message: fmt.Sprintf(
			"Sink name %q is used by multiple entries in spec.sinks. Sink names must be unique within the AIBOMControllerConfig.",
			sinkName,
		),
	}
}

// errPatternCompileFailed is emitted when an
// InferenceRuntimeImagePatterns entry has a regex that doesn't compile.
func errPatternCompileFailed(index int, runtime, pattern string, compileErr error) LoadError {
	return LoadError{
		Field: fmt.Sprintf("spec.discovery.inferenceRuntimeImagePatterns[%d].pattern", index),
		Message: fmt.Sprintf(
			"Runtime %q pattern %q failed to compile as a Go regexp: %v. Anchor the pattern (e.g., ^vllm/.*) and escape regex metacharacters in literal path segments.",
			runtime, pattern, compileErr,
		),
	}
}

// errNamespaceSelectorInvalid is emitted when
// spec.discovery.namespaceSelector cannot be materialized into a
// labels.Selector.
func errNamespaceSelectorInvalid(reason string) LoadError {
	return LoadError{
		Field: "spec.discovery.namespaceSelector",
		Message: fmt.Sprintf(
			"namespaceSelector cannot be materialized: %s. Common causes: invalid label-key syntax, malformed matchExpressions operator.",
			reason,
		),
	}
}
