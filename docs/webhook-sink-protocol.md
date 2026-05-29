# Webhook sink protocol

This document specifies the HTTP request envelope the WebhookSink uses
to deliver generated BOMs to a customer-configured receiver. Customer
integrations depend on the stability of this envelope; changes to it
are treated as **API-surface changes** in semver terms.

## Request shape

```
POST <endpoint>
Content-Type: application/vnd.cyclonedx+json; version=1.6
X-Aibom-Document-Hash: sha256:<hex>
X-Aibom-Workload-Kind: <kind>
X-Aibom-Workload-Namespace: <namespace>
X-Aibom-Workload-Name: <name>
X-Aibom-Workload-Category: <category>
X-Aibom-Controller-Version: <semver>
Authorization: Bearer <token>      (when configured)

<CycloneDX 1.6 ML-BOM as JSON body>
```

The receiver MUST respond with an HTTP status code; see "Response
handling" below for the controller's interpretation.

## Headers

| Header | Required | Purpose |
|---|---|---|
| `Content-Type` | always | Identifies the body as CycloneDX 1.6 ML-BOM JSON. Constant: `application/vnd.cyclonedx+json; version=1.6`. |
| `X-Aibom-Document-Hash` | always | Content-addressed hash of the BOM body (`sha256:<hex>`). Receivers SHOULD use this for deduplication — identical hashes across requests represent the same BOM and the receiver MAY drop duplicates. |
| `X-Aibom-Workload-Kind` | always | Kubernetes kind of the workload (e.g., `Deployment`, `StatefulSet`, `InferenceService`). |
| `X-Aibom-Workload-Namespace` | always | Workload's Kubernetes namespace. |
| `X-Aibom-Workload-Name` | always | Workload's Kubernetes `metadata.name`. |
| `X-Aibom-Workload-Category` | always (v1) | High-level AI lifecycle category (`inference` in v1; future values: `training`, `agent`, etc.). |
| `X-Aibom-Controller-Version` | always | Version of the k8s-aibom controller that produced this BOM. |
| `Authorization` | when configured | Bearer token from the controller's configured `BearerTokenFile`. Sent only when the controller is configured with bearer-token auth. |

## Body

The request body is the **canonical JSON serialization** of the CycloneDX
1.6 ML-BOM document, byte-identical to what would be written to the GCS
sink and inlined in the `AIBOM` CR status when small enough. No
additional framing, envelope, or transformation is applied.

## Response handling

| Status range | Controller interpretation |
|---|---|
| `2xx` | Success. The sink reports the endpoint URL on `BOMDocumentRef.External.URL` for this delivery. The receiver is assumed to have durably accepted the BOM; the controller does NOT re-deliver until the BOM content changes. |
| `4xx` | **Customer configuration problem.** No retry. The controller surfaces the response status code and a truncated response body (max 256 bytes) on the AIBOM CR's `SinkFailed` condition. The next reconcile cycle retries after the customer fixes the receiver. |
| `5xx` | **Transient receiver problem.** The controller retries with exponential backoff (1s, 2s, 4s; 3 retries total). After the final retry, the failure is recorded on `SinkFailed` and the next reconcile cycle tries again. |
| Network / TLS error | Treated as transient. Same retry behavior as `5xx`. |

The entire retry sequence runs inside the reconciler's per-sink
deadline (`DefaultExternalSinkTimeout`, 30s). If the deadline expires
mid-retry, the controller returns the last seen error and the next
reconcile cycle retries fresh.

## Idempotency contract for receivers

Receivers SHOULD treat two requests with the same
`X-Aibom-Document-Hash` value as redundant deliveries of the same BOM
and MAY drop the duplicate. The controller may re-deliver the same
document under any of these conditions:

- Transient delivery failure (5xx, network) leading to a retry.
- Controller restart followed by a re-reconcile of every tracked workload.
- Reconciler input-hash dedup miss (very rare; would indicate a bug).

A receiver that does not deduplicate will store multiple copies of the
same BOM. This is functionally correct but wastes storage.

## Stability

This document is the source of truth for the protocol. Changes are
**API-surface changes**:

- Adding a new optional `X-Aibom-*` header: minor / non-breaking.
- Adding a new value to `X-Aibom-Workload-Category`: minor / non-breaking.
- Changing an existing header name, removing a header, or changing the
  `Content-Type` value: MAJOR / breaking. Coordinate with at least one
  major external integrator before making.
- Changing the `Content-Type` minor version (e.g., from
  `version=1.6` to `version=1.7`): tied to a CycloneDX spec bump in
  the controller and announced as a breaking change in release notes.

The corresponding tests in
[`internal/sink/webhook_test.go`](../internal/sink/webhook_test.go)
lock the constants. If a test in that file fails on a header
constant, the corresponding row of the table above also needs to
change in the same commit.
