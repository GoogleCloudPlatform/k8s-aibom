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

// Condition type constants for AIBOM.status.conditions. These are part
// of the external API contract: customer dashboards, GRC pipelines, and
// alerting can grep for these exact strings. Adding a new condition type
// is fine; renaming an existing one is a backward-incompatible API
// change.
//
// Convention: condition types are positive-form nouns. status=True
// means the condition holds. See FR5.5.
const (
	// ConditionReady is True when the controller has successfully
	// scraped the workload, built a BOM, and recorded a current
	// Summary in this CR's status. False during transient scrape or
	// build failures.
	ConditionReady = "Ready"

	// ConditionSynced is True when every configured sink (CRD status
	// always, plus any optional external sinks) has been written to
	// successfully with the current BOM. False when at least one sink
	// failed; see ConditionSinkFailed for the per-sink detail.
	ConditionSynced = "Synced"

	// ConditionSinkFailed is True when at least one external sink
	// failed to receive the current BOM. The condition's Message field
	// names the failing sink(s) and the underlying error.
	ConditionSinkFailed = "SinkFailed"

	// ConditionStale is True when the controller has been unable to
	// reconcile this AIBOM for longer than the staleness threshold
	// (e.g., API server unavailable, persistent scrape failure). The
	// Summary in status may still reflect a previous successful
	// reconciliation; LastReconciled is the time of that prior run.
	ConditionStale = "Stale"
)

// Standard condition reasons. PascalCase, brief, machine-grepable.
// Reasons are part of the API but less strictly contractual than types
// — they may be extended over time. Customers writing alerts should
// match on type primarily and use reason as additional context.
const (
	ReasonBOMGenerated       = "BOMGenerated"
	ReasonScrapeFailed       = "ScrapeFailed"
	ReasonBuildFailed        = "BuildFailed"
	ReasonAllSinksOK         = "AllSinksOK"
	ReasonOneOrMoreSinksDown = "OneOrMoreSinksDown"
	ReasonCRDStatusOnly      = "CRDStatusOnly"
	ReasonBOMTruncated       = "BOMTruncated"
)
