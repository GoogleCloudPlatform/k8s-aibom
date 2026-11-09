# Threat model

This is the security analysis of the k8s-aibom controller: assets,
actors, trust boundaries, attack scenarios, and the mitigations that
actually exist in the shipped code. Findings discovered against this
model are recorded in §7 with their fixes — defects are part of the
record here, not a separate story.

Scope note: the controller is detection-only and unprivileged by
design (no DaemonSets, no privileged containers, no kernel access, no
admission webhooks on workloads, no pod mutation). Its entire write
surface inside the cluster is the AIBOM custom resource; its entire
read surface is the Kubernetes API.

## 1. Assets

- **A1 — The BOM as audit evidence.** The CycloneDX document and the
  AIBOM CR summary are consumed as compliance evidence. Their value is
  integrity: a wrong fact stamped with a confidence tier is worse than
  an absent fact. Everything else here defends A1.
- **A2 — Sink credentials.** The GCS service-account binding and any
  webhook bearer token / mTLS material referenced from
  AIBOMControllerConfig Secrets.
- **A3 — The archive.** Objects written to external sinks, relied on
  for retrospective questions after workloads are gone.
- **A4 — Trust roots.** The Sigstore trust material (public-good TUF,
  TUF mirror, or static bundle) that the `verified` tier depends on.
- **A5 — The controller's read position.** Cluster-wide informer
  caches (all workload specs) and its network egress position.

## 2. Actors

- **T1 — Namespace tenant** (untrusted): can create workloads and pods
  in namespaces they control, author annotations, and reference
  attacker-controlled URLs in signature claims. The primary adversary.
- **T2 — Cluster operator** (trusted): installs the chart, labels
  namespaces, authors AIBOMControllerConfig, owns trust roots.
- **T3 — Sink operator / receiver** (semi-trusted): controls the far
  end of BOM delivery; may be a third party (Dependency-Track, SIEM).
- **T4 — External signature host** (untrusted): serves the bundles
  that workload annotations reference.
- **T5 — Registry operators** (semi-trusted): control image content
  behind references; the controller never pulls images, only records
  references and kubelet-reported digests.

## 3. Trust boundaries and surfaces

- **B1 — Workload spec and annotations (T1 → controller).** All spec
  content is untrusted input: names, args, env values, annotation
  claims. Mitigations: conservative detection (registry-anchored
  allowlist; ambiguous stays `unresolved`); declared values recorded
  as *claims* with evidence locators, never as verified facts; length
  truncation on extracted names; the output sanitization pass
  (`k8s-aibom:redaction:applied` is recorded, never silent).
- **B2 — Pod status → digest attribution (T1 → A1).** Digests enter a
  BOM only from pods tied to the workload through the controller
  ownerReference chain, with an image-name match on the candidate's
  container status; pods with no controller owner never contribute
  (see F1, fixed v1.5.1).
- **B3 — Signature-reference fetch (T1/T4 → A5).** Tenant-authored
  URLs fetched from the controller's network position. Bounds
  (verifier/fetch.go): https-only, 1 MiB cap, same-host redirects, no
  credentials attached, per-claim timeout, TTL'd cache. **Named
  residual:** no private-IP dial guard — a fetch can target internal
  HTTPS endpoints, and the recorded outcome is an existence/latency
  oracle readable by the tenant. Verification is off by default;
  the webhook sink's private-IP guard is in-tree precedent and the
  planned closure (#96-adjacent hardening, v1.6 candidate).
- **B4 — Sink egress (controller → T3).** Credentials never travel
  over cleartext: http + auth is rejected at config load (F2, fixed
  v1.5.1). The webhook sink refuses private-IP destinations by
  default (SSRF guard). GCS objects are written with a DoesNotExist
  precondition: write-once, never overwrite.
- **B5 — Configuration (T2 → controller).** AIBOMControllerConfig is
  validated all-or-nothing at load: any semantic error falls back to
  compiled defaults with Ready=False naming every error. Secrets are
  read only from the controller's own namespace. **CRD lifecycle is
  part of this boundary:** a served schema older than the controller
  causes the API server to prune newer stored fields without error;
  the controller detects this by comparing the served OpenAPI v3
  schema against its compiled-in spec and reports `Degraded=True`
  (F7), because a security control that is silently off must never
  present as green.
- **B6 — RBAC posture.** Read-only get/list/watch on workloads, pods,
  replicasets, namespaces; write only on AIBOM CRs; optional
  Secret access is chart-gated (`rbac.sinkSecretAccess`).

## 4. Attack scenarios

- **S1 — Digest planting (T1 → A1).** Create a pod matching another
  workload's selector with a chosen imageID. Pre-v1.5.1 this landed
  the attacker's digest in the victim's BOM as `pod_status` evidence.
  Closed by ownership-chain attribution (F1). Residual: a tenant who
  can modify the *workload itself* controls its BOM content — that is
  the design (the BOM records what the workload declares and runs),
  not a bypass.
- **S2 — Credential capture on the wire (T1 on-path / network).**
  Bearer token over http. Closed at config load (F2).
- **S3 — Identity laundering via signature claims (T1).** A valid
  signature from the wrong signer must not upgrade confidence:
  `verified` requires an operator-configured signer-identity
  constraint; under a public root with no constraint the outcome is
  recorded as a fact (`signature-valid-unconstrained`) and the tier
  stays `claimed`. Subject names are never sufficient and never
  override digests (Design 002 §5).
- **S4 — Oracle probing via the verifier fetch (T1 → A5).** See B3
  residual.
- **S5 — Archive gapping (availability of A3).** A transiently
  failing sink previously dropped the BOM from the archive until the
  workload spec changed (F3, fixed post-v1.5.1 on main). Remaining
  operational caveat: custom GCS path templates without a uniqueness
  token surface retry collisions as visible errors rather than
  silent gaps.
- **S6 — Resource exhaustion via authored specs (T1).** Extracted
  names are truncated; container-component name/version caps and a
  component-count cap are tracked as F4 (v1.6). Inline size is
  bounded by `inlineThresholdBytes` (etcd protection).

## 5. The runtime model-fetch bypass

The controller records what the API server shows. A workload that
downloads different weights at runtime than its spec declares defeats
spec-level inventory by construction. This is out of scope for the
controller and in scope for the `verified` tier: a cryptographically
verified model signature binds identity to content, which is the only
spec-level defense against this class. Fidelity beyond the API server
(in-container load events, egress capture) is permanently out of
scope for this controller (see roadmap "Out of scope — permanently").

## 6. Out of scope

- Kernel-level or in-container observation (permanent posture).
- Verdicts, scoring, admission control (facts-not-judgments).
- Defending against a malicious cluster operator (T2 is trusted; a
  hostile cluster admin owns the API server and everything above).
- Package-level image scanning (registry scanners own it; the BOM's
  digests are the join key).

## 7. Findings record

| ID | Finding | Status |
|---|---|---|
| F1 | Selector-based pod attribution allowed digest cross-contamination and planting (#97) | Fixed v1.5.1 |
| F2 | Bearer tokens accepted over cleartext http (#98) | Fixed v1.5.1 |
| F3 | Transient sink failures were never retried; archive gapped silently (#91) | Fixed on main; ships v1.6 |
| F4 | Container-component name/version and component count unbounded (tenant-controlled document growth) | Open; v1.6 |
| F5 | Verifier fetch lacks a private-IP dial guard (internal-endpoint oracle; off-by-default feature) | Open; doc'd here; guard is a v1.6 candidate |
| F6 | Bearer auth has no CA option, forcing public-CA https (#96) | Open; v1.6 candidate |
| F7 | Served CRD schema older than the controller silently prunes `spec.verification`; verification OFF behind `Ready=True` (#104). Found by downstream adversarial upgrade testing | Fixed on main; ships v1.6 — `Degraded=True` reason `SchemaPredatesController` + Warning Event, generic over all spec fields |
