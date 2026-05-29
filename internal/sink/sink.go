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

package sink

import (
	"context"

	"github.com/GoogleCloudPlatform/k8s-aibom/internal/bom"
)

// Sink is the workload-type-neutral contract for emitting a finished BOM
// document to one EXTERNAL destination (GCS bucket, webhook endpoint,
// future GUAC). The CRD-status sink is NOT a Sink in this interface
// sense — it is part of the reconciler's terminal status-update step.
// External sinks are the ones that need parallel fan-out, retry policy,
// and per-sink success/failure tracking.
//
// Implementations MUST:
//
//   - Be safe for concurrent invocation. A single Sink instance may be
//     called with many different workloads simultaneously.
//   - Be idempotent within a workload. Re-emitting the same BOM for the
//     same workload MUST NOT produce duplicate, divergent, or corrupt
//     state at the destination.
//   - Return an error promptly on transient failure so the pipeline can
//     record the failure on the AIBOM CR's SinkFailed condition.
//     Implementations MUST NOT swallow errors. Error messages MUST be
//     safe to surface in CR conditions (bucket name OK, credentials
//     NEVER).
//   - Honor ctx cancellation. Long-running emissions (large BOMs to slow
//     sinks) MUST abort on ctx.Done(). The reconciler wraps each Emit
//     call with a bounded deadline.
//   - NOT mutate the passed *bom.Document or SinkMeta.
//
// The Sink contract intentionally does NOT receive Scraper.BOMInputs or
// any internal scraper state; only the finished, hashed *bom.Document and
// the SinkMeta. This keeps the audit-log story simple (one document, one
// hash, one identity writes it) and prevents internal scraper errors from
// leaking into sink-side state.
type Sink interface {
	// Name returns a stable identifier for this sink, used in the
	// AIBOM.status.bomDocument.externalRef.sink field, in logs, and in
	// metric labels. Convention: lowercase, dash-separated
	// (e.g., "gcs", "webhook", "guac").
	Name() string

	// Emit writes the document to the sink's destination and returns the
	// canonical retrieval URL for the written object (e.g., a gs:// URL
	// for GCS). On error, the returned URL MAY be empty; callers MUST
	// check err first.
	//
	// Emit MUST be idempotent: re-emitting the same Document+SinkMeta
	// MUST produce the same end state. For sinks with immutability
	// requirements (e.g., GCS write-only object policies), the URL
	// returned by Emit identifies the specific object written by this
	// invocation.
	Emit(ctx context.Context, doc *bom.Document, meta SinkMeta) (url string, err error)

	// HealthCheck performs a low-cost probe to confirm the sink is
	// reachable and authorized. The reconciler may call this on a
	// configurable interval to populate AIBOMControllerConfig status.
	// Implementations MAY return nil if the sink cannot be cheaply
	// probed without violating the least-privilege IAM (e.g., a sink
	// that only has write permission cannot read back to verify);
	// document such cases in the sink's godoc.
	HealthCheck(ctx context.Context) error

	// WriteOnly reports whether the URL returned by Emit is
	// informational rather than retrievable. True for sinks whose
	// receivers cannot serve the BOM back (e.g., webhook endpoints
	// posting to a SIEM). False for sinks where the URL is canonical
	// (e.g., a gs:// URL on a customer-readable GCS bucket).
	//
	// The reconciler's status builder uses this to prefer retrievable
	// sinks when populating AIBOM.status.bomDocument.externalRef:
	// if multiple sinks succeed, the first non-WriteOnly one wins.
	// When only WriteOnly sinks are configured, the first successful
	// one wins and ExternalBOMRef.WriteOnly is set to true so
	// auditors know the URL is not retrievable.
	WriteOnly() bool
}
