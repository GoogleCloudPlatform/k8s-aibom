# Webhook sink protocol

This document specifies the HTTP request envelope the WebhookSink uses
to deliver generated BOMs to a customer-configured receiver. Customer
integrations depend on the stability of this envelope; changes to it
are treated as **API-surface changes** in semver terms.

## Request shape

```
POST <endpoint>
Content-Type: application/json
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
| `Content-Type` | always | Constant: `application/json`. The body is CycloneDX 1.6 ML-BOM JSON; the CycloneDX version is the body's `specVersion`, not a media-type parameter. The CycloneDX media type `application/vnd.cyclonedx+json` is not sent. |
| `X-Aibom-Document-Hash` | always | Content-addressed hash of the BOM body (`sha256:<hex>`). Receivers SHOULD use this for deduplication — identical hashes across requests represent the same BOM and the receiver MAY drop duplicates. |
| `X-Aibom-Workload-Kind` | always | Kubernetes kind of the workload (e.g., `Deployment`, `StatefulSet`, `InferenceService`). |
| `X-Aibom-Workload-Namespace` | always | Workload's Kubernetes namespace. |
| `X-Aibom-Workload-Name` | always | Workload's Kubernetes `metadata.name`. |
| `X-Aibom-Workload-Category` | always (v1) | High-level AI lifecycle category, the same value as the document's `k8s-aibom:workload:category` (`inference`, `agent`, `vector-database`, `training`, `evaluation`). |
| `X-Aibom-Controller-Version` | always | Version of the k8s-aibom controller that produced this BOM. |
| `Authorization` | when configured | `Bearer <token>`. Sent only when the sink sets `webhook.auth.bearerToken`. The token is the value of the Secret key named by `webhook.auth.bearerToken.secretRef` (a Secret in the controller's namespace), with surrounding whitespace trimmed; a whitespace-only value is a sink error. The chart needs `rbac.sinkSecretAccess=true` to let the controller read the Secret. Auth on an `http://` endpoint is rejected when the configuration loads, so the header is only sent over `https://`. |

## TLS trust for bearer authentication

For an HTTPS receiver signed by a private CA, set
`webhook.auth.bearerToken.caSecretRef` alongside the token's `secretRef`:

```yaml
auth:
  bearerToken:
    secretRef:
      name: webhook-token
      key: token
    caSecretRef:
      name: webhook-ca
      key: ca.crt
```

The CA reference has the same semantics as `webhook.auth.mtls.caSecretRef`:
its Secret must be in the controller's namespace, and the key must contain
a PEM-encoded CA bundle. The chart requires `rbac.sinkSecretAccess=true`.
When set, the bundle replaces the system trust roots for this sink; when
omitted, system roots are used. Certificate-chain and hostname verification
remain enabled. Missing Secrets or keys, empty values, and invalid CA PEM
cause configuration load errors. Bearer credentials over `http://` remain
invalid even when a CA reference is set.

After rotating the CA Secret, change the `AIBOMControllerConfig` spec or
touch an annotation to reload it; changing Secret data alone does not
trigger a reload.

## Body

The request body is the **canonical JSON serialization** of the CycloneDX
1.6 ML-BOM document, byte-identical to what would be written to the GCS
sink and inlined in the `AIBOM` CR status when small enough. No
additional framing, envelope, or transformation is applied.

## Response handling

| Status range | Controller interpretation |
|---|---|
| `2xx` | Success. The sink reports the endpoint URL on `BOMDocumentRef.External.URL` for this delivery. The receiver is assumed to have durably accepted the BOM; the controller does NOT re-deliver until the BOM content changes. |
| `3xx` | Redirects are **not followed**. Handled like `4xx`: no retry, and the status code and response body are surfaced on `SinkFailed`. |
| `4xx` | **Customer configuration problem.** No retry. The controller surfaces the response status code and the response body (see below) on the AIBOM CR's `SinkFailed` condition. The next reconcile cycle retries after the customer fixes the receiver. |
| `5xx` | **Transient receiver problem.** The controller retries with exponential backoff (250ms, 1s, 3s; 4 total attempts). After the final retry, the failure is recorded on `SinkFailed` and the next reconcile cycle tries again. |
| Network / TLS error | Treated as transient. Same retry behavior as `5xx`. |

The response body on `SinkFailed` is a bounded excerpt: the controller
reads at most the first 1024 bytes, drops invalid UTF-8, turns line
breaks into spaces and removes ANSI escape sequences, then keeps at
most 256 characters (runes, not bytes). `...(truncated)` is appended
when the read limit was passed or the text was cut to 256 characters.
An empty body is reported as `(empty body)`.

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
- A CycloneDX spec bump in the controller (e.g., from 1.6 to 1.7):
  changes the body's `specVersion`, not the `Content-Type` header, and
  is announced as a breaking change in release notes.

The corresponding tests in
[`internal/sink/webhook_test.go`](https://github.com/GoogleCloudPlatform/k8s-aibom/blob/main/internal/sink/webhook_test.go)
check the header names and the `Content-Type` value as written in this
document. If a test in that file fails on a header, the corresponding
row of the table above also needs to change in the same commit.
