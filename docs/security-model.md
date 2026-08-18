# Security model

This document describes the security boundaries the k8s-aibom
controller upholds: who is allowed to write what, the minimum IAM the
controller requires, and the invariants customer security teams can
rely on. It is the authoritative reference for security review.

See [`threat-model.md`](threat-model.md) for the attack scenarios and
the formal trust-boundary analysis. This document is the operational
"how do I configure this safely" guide.

## 1. Sole-writer model (PRD FR4.4)

The controller is the **only identity** in the system that writes BOMs
to external sinks. Every other principal — inference workload pods,
data-plane pods, other operators, application service accounts —
**must not** have any write permission on the configured sink targets.

This bounds the blast radius of a compromised inference workload: the
attacker cannot tamper with audit BOMs for any workload, including the
one they compromised, because they have no IAM grant on the sink. It
also produces a single-principal write pattern in cloud audit logs
that is easy to monitor and alert on.

## 2. GCS sink IAM

### Minimum permissions

The GCS sink requires **only** `roles/storage.objectCreator` on the
target bucket. This grant allows the controller to create new objects
but not:

- Read existing objects (`storage.objects.get`)
- List existing objects (`storage.objects.list`)
- Delete objects (`storage.objects.delete`)
- Update or overwrite objects (`storage.objects.update`)
- Modify bucket-level configuration (`storage.buckets.*`)

A `storage.objectCreator` grant satisfies the FR4.4 sole-writer model
while preserving the immutability guarantee: the controller can write
new BOMs but cannot tamper with prior BOMs.

### Recommended bucket configuration

For production deployments, configure the target bucket with:

- **Uniform bucket-level access** (UBLA). Avoids the per-object ACL
  surface entirely.
- **Object retention policy** matching the customer's audit retention
  requirements (e.g., 7 years for EU AI Act records). The controller
  writes-only; the retention policy is the enforcement mechanism for
  "BOMs are not deleted before audit period ends".
- **Object lifecycle rule** for cold-storage migration after 90 days,
  if cost-relevant. BOMs are read rarely after the initial reconcile.
- **Customer-managed encryption keys (CMEK)** if your data-residency
  posture requires it. The GCS sink does not need to be aware of CMEK
  — GCS handles encryption transparently at the bucket level.
- **Audit logging enabled** for `storage.objects.create` so the
  single-principal write pattern is visible in Cloud Audit Logs. This
  is the basis for "alert if any non-controller principal writes to
  this bucket".

### Authentication

The GCS sink supports three authentication paths, in order of
preference:

1. **Application Default Credentials (ADC) with Workload Identity (GKE).**
   The Kubernetes ServiceAccount running the controller is bound to a
   GCP Service Account via GKE Workload Identity. No credentials are
   stored in the cluster or on disk. Annotate the KSA with
   `iam.gke.io/gcp-service-account=<gsa-email>` and grant the GSA
   `roles/storage.objectCreator` on the bucket.
2. **Application Default Credentials with Workload Identity Federation**
   (for non-GKE clusters: EKS, AKS, on-prem). Configure ADC via the
   federation JSON config; point `GOOGLE_APPLICATION_CREDENTIALS` at
   it. No long-lived credentials in the cluster.
3. **Service account key file (documented fallback).** Mount a JSON
   key file as a Kubernetes Secret in the controller's namespace; set
   `K8S_AIBOM_GCS_CREDENTIALS_FILE` to its path. Key rotation is the
   **customer's responsibility**; the controller has no rotation
   support. Avoid this path in production.

### Configuration via env vars (v1.0 interim)

The Phase 13 AIBOMControllerConfig CRD will replace this; for v1.0:

| Env var | Required | Description |
|---|---|---|
| `K8S_AIBOM_GCS_BUCKET` | yes (to enable sink) | Bucket name |
| `K8S_AIBOM_GCS_PATH_TEMPLATE` | no | Object path layout; default `mlbom/{namespace}/{kind}-{name}/{timestamp}.json` |
| `K8S_AIBOM_GCS_CREDENTIALS_FILE` | no | Path to service-account JSON key file. If unset, ADC is used. |

## 3. Immutability guarantee

The GCS sink never overwrites a prior BOM object.

- Object names include the reconcile timestamp at second granularity:
  `mlbom/{namespace}/{kind}-{name}/{timestamp}.json`.
- Every write is gated by a `DoesNotExist` precondition. On collision
  (rare, possible only on second-resolution timestamp overlap), the
  write fails. The next reconcile produces a new timestamp and
  succeeds. The collision is surfaced as a `SinkFailed` condition on
  the AIBOM CR for visibility; it is self-healing across reconcile
  cycles.
- The controller does NOT require nor request `storage.objects.delete`
  on the bucket. Even a compromised controller cannot remove prior
  BOMs.
- Customers wishing to enforce immutability at the cloud-provider
  level should add an Object Retention Lock on the bucket. The
  controller's behavior is compatible: it only ever creates new
  objects.

## 4. Error message redaction

The controller's audit-relevant outputs — CR status conditions,
structured logs, Prometheus metrics — are scoped to be safe under
public inspection:

- **Bucket names** appear in conditions and logs (necessary for
  diagnosis).
- **Object paths** appear in conditions and logs (necessary for
  diagnosis).
- **Credentials** — service-account JSON contents, OAuth tokens, JWTs,
  Workload Identity tokens — MUST NEVER appear in conditions, logs,
  or metrics. The controller relies on the upstream `cloud.google.com/go/storage`
  library not leaking these in its returned errors; the
  `gcs_integration_test.go` test set includes an explicit assertion
  that simulated failure errors do not contain known credential
  markers.

If you ever see what appears to be a credential in a controller log
or condition, treat it as a P1 bug and rotate the affected credential
immediately.

## 5. RBAC inside the cluster

The controller's Kubernetes RBAC is the least-privilege set described
in PRD §NFR3.2 and enforced by the kubebuilder markers in
[`internal/controller/aibom_controller.go`](../internal/controller/aibom_controller.go).
Specifically:

- **Read** on `apps/v1.Deployment` (extended to StatefulSet, KServe
  InferenceService in Phase 10), `core/v1.Pod`, `core/v1.Namespace`.
- **Write** on `aibom.k8saibom.dev/v1alpha1.AIBOM` only (full CRUD for
  reconciler ownership; status subresource for status updates).
- **No** write permission on workload resources (Deployment, Pod,
  etc.). The controller observes; it never modifies workloads.
- **No** RBAC on Secrets in customer namespaces. Sink credentials
  live in the controller's namespace only.

## 6. What the sole-writer model does NOT defend against

For completeness, the threats outside scope:

- A compromised cluster admin. They have cluster scope by definition;
  they can edit the controller's RBAC, deployment, or `AIBOMControllerConfig`.
- A compromised cloud-provider admin. They can directly modify or
  delete objects in the GCS bucket regardless of in-cluster IAM.
- A compromised CI/CD pipeline that builds the controller container.
  Supply-chain protection is a separate concern (cosign-signed
  controller images, attested builds).
- The runtime-model-fetch bypass. A workload that downloads a model
  from an arbitrary URL at runtime is invisible to v1 spec-driven
  scraping. See [`threat-model.md`](threat-model.md) §5.

## 7. Data visibility, sensitivity, and retention

Adopter-facing disclosure of what the controller sees, what it emits, who
should be able to read the output, and how long data lives.

### What the controller sees (in memory)

The controller's informers watch workloads and pods **cluster-wide**. This
means the full spec of every workload and pod in the cluster — including
container args and inline environment variable *values* — passes through
the controller's informer cache in memory, **before** the namespace
opt-in check is applied. The `aibom.k8saibom.dev/enabled=true` label
governs what is *reported*, not what is *cached*: specs from non-opted-in
namespaces are held in the standard controller-runtime cache and are
never scraped, persisted, emitted to any sink, or included in any BOM.
Restricting the informers themselves to labeled namespaces would miss
newly labeled namespaces and is not currently implemented; the in-memory
scope is a deliberate, disclosed trade-off.

### What the controller emits

BOMs contain metadata **derived from workload specs**: image references
and digests, detected runtimes/frameworks, model identities from args,
annotations, and environment variable *names* and identity-bearing values
(e.g. `HF_MODEL_ID`). Secret *values* are never emitted: `secretKeyRef`
values do not appear in BOMs or controller logs (verified independently
during downstream qualification). Sink credential Secrets are read
on-demand, used to construct sink clients, and never logged or embedded
in output.

### Intended readers

Treat `AIBOM` resources as **sensitive operational metadata** — they
reveal which AI models, frameworks, and external AI dependencies run
where. Intended readers are platform, security, and compliance teams.
Recommended posture: do not grant `get`/`list` on `aiboms` in ClusterRole
grants for general developer roles; scope read access to the teams that
consume inventory. The same applies to any external sink destination
(bucket ACLs, webhook receivers).

### Retention

- **In-cluster:** each AIBOM lives exactly as long as its owning
  workload (owner-reference garbage collection). Uninstalling the
  controller does not delete AIBOMs (see README: Uninstall); deleting
  the CRDs purges all inventory data.
- **External sinks:** retention is governed by the destination. The GCS
  sink writes immutable objects (`DoesNotExist` preconditions) — the
  audit trail cannot be silently overwritten, and lifecycle/retention
  policy is the bucket owner's responsibility.
