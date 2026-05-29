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

package v1alpha1

// Condition types emitted on AIBOMControllerConfig.status.conditions.
// Customer dashboards, alerting rules, and operators may grep on these
// exact strings. Adding a new condition type is fine; renaming an
// existing one is a backward-incompatible API change.
//
// Convention (same as AIBOM): condition types are positive-form nouns.
// status=True means the condition holds.
const (
	// AIBOMControllerConfigConditionReady is True when the controller
	// has successfully loaded this configuration into a runtime
	// snapshot, all referenced Secrets resolved, and all configured
	// external sinks constructed without error. False when the
	// controller fell back to compiled-in defaults due to a load or
	// construction failure; the condition's Message names the
	// failing field(s).
	AIBOMControllerConfigConditionReady = "Ready"

	// AIBOMControllerConfigConditionDegraded is True when the
	// controller is running with the LAST KNOWN GOOD snapshot or
	// with compiled-in defaults (not the snapshot derived from this
	// CR's current spec). Distinct from Ready=False: Ready=False
	// reports the parse/load failure; Degraded=True reports that
	// the controller is operating in a fallback mode because of it.
	// Both are typically True together on failure.
	AIBOMControllerConfigConditionDegraded = "Degraded"
)

// Standard condition reasons for AIBOMControllerConfig conditions.
// PascalCase, brief, machine-grepable. Reasons are part of the API
// but less strictly contractual than types — they may be extended
// over time. Customers writing alerts should match on type primarily
// and use reason as additional context.
const (
	// ReasonConfigLoaded is set on Ready=True when the config was
	// loaded into a runtime snapshot successfully.
	ReasonConfigLoaded = "ConfigLoaded"

	// ReasonConfigInvalid is set on Ready=False when the CR
	// passed OpenAPI/CEL validation at apply time but failed
	// semantic validation at load time (sink construction error,
	// referenced Secret not found, regexp doesn't compile, etc.).
	// The condition Message enumerates ALL failing fields so a
	// single fix-and-apply cycle can clear them.
	ReasonConfigInvalid = "ConfigInvalid"

	// ReasonSinkConstructionFailed is set on Ready=False when one
	// or more configured sinks failed to construct (typically: a
	// referenced Secret was missing, a credentials file path was
	// unreachable, or a webhook endpoint failed initial validation).
	ReasonSinkConstructionFailed = "SinkConstructionFailed"

	// ReasonSecretNotFound is set on Ready=False when a SecretKeyRef
	// in the spec points at a Secret that doesn't exist in the
	// controller's namespace, or at a key not present in the Secret.
	ReasonSecretNotFound = "SecretNotFound"

	// ReasonRunningOnDefaults is set on Degraded=True when the
	// controller fell back to compiled-in defaults (either because
	// the CR is missing or because the CR is invalid).
	ReasonRunningOnDefaults = "RunningOnDefaults"

	// ReasonRunningOnLastKnownGood is set on Degraded=True when the
	// controller fell back to the previous successfully-loaded
	// snapshot after a subsequent invalid update.
	ReasonRunningOnLastKnownGood = "RunningOnLastKnownGood"
)
