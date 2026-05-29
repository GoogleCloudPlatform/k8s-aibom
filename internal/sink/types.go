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

// Package sink contains the workload-type-neutral Sink interface and the
// SinkMeta type passed alongside each emission. Concrete sinks
// (CRDStatusSink, GCSSink, WebhookSink) land in Phases 7-9.
package sink

import "time"

// SinkMeta is the workload-identifying metadata that accompanies every
// Sink.Emit invocation. It is intentionally a flat, stringly-typed struct
// so individual sinks can use it as path-template input (GCS object path),
// HTTP header values (webhook), or audit-log fields without further
// parsing.
//
// SinkMeta is an internal type. The CRD-status sink builds a deliberately
// designed API-package summary from the *bom.Document plus this metadata;
// it does NOT serialize SinkMeta directly into the CR status.
type SinkMeta struct {
	WorkloadKind      string
	WorkloadNamespace string
	WorkloadName      string
	WorkloadCategory  string
	BOMHash           string
	Timestamp         time.Time
}
