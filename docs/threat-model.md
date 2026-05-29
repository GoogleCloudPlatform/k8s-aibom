# Threat model

This document is the security analysis of the k8s-aibom controller. It
identifies the assets, actors, attack surfaces, and mitigations relevant to
operating the controller in a regulated production environment. It is a
living document; it will be filled in fully as the implementation lands and
in coordination with PRD §16 (Risks and Mitigations).

## Status

Phase 1 scaffold placeholder. Skeleton headings below indicate the structure
the finished document will follow.

## 1. Assets

The things this controller is responsible for protecting or producing.

- **Generated AI-BOM documents.** These are audit evidence used for EU AI Act
  Article 11 documentation, NIST AI RMF profile evidence, ISO 42001 audits,
  and internal incident response. Integrity and completeness matter more than
  confidentiality (the BOM describes the workload, not its data).
- **Sink credentials.** GCS Workload Identity binding, webhook bearer tokens,
  future GUAC auth material. Loaded from Kubernetes Secrets in
  `k8s-aibom-system`.
- **External sink targets.** GCS buckets, webhook endpoints, GUAC ingestion
  endpoints. The controller is the sole writer to these (PRD FR4.4).

## 2. Actors

- **Cluster administrator (trusted).** Installs the controller, configures
  sinks, opts in namespaces. Trusted with full cluster scope.
- **Namespace owner (trusted within own namespace).** Owns workloads tracked
  by the controller. Cannot tamper with the controller's BOMs (by design).
- **Compromised inference workload (untrusted).** A pod that has been taken
  over by an attacker (e.g., supply-chain compromise of the model server
  image). The threat model must bound the blast radius of such a workload.
- **External attacker (untrusted).** Pre-authentication adversary attempting
  to reach the controller from the network, the registry, or via crafted
  CRDs.

## 3. Trust boundaries

The boundaries across which authority changes, and what crosses each one.

- **Cluster API server <-> Controller.** The controller authenticates as a
  ServiceAccount with the RBAC defined in PRD §FR7. RBAC is least-privilege:
  read on workload kinds + Namespaces, write on AIBOM only.
- **Controller <-> External sinks.** Controller authenticates as a single
  identity (KSA bound to a GSA for GCS, bearer tokens from Secrets for
  webhook / GUAC). No other principal in the cluster has write access to the
  sink targets.
- **Controller <-> Workload pods.** No direct connection. The controller
  reads pod *spec* and *status* via the API server. The controller never
  executes into pods.

## 4. Attack scenarios

Placeholder list. Each scenario needs a full STRIDE-style write-up before
v1.0 ships, covering vector, impact, current mitigation, and residual risk.

- **AS-1: Compromised inference workload tampers with its own BOM.** The
  workload is the *subject* of the BOM, not the *writer*. RBAC denies pod
  ServiceAccounts any write permission on `AIBOM` resources or on the GCS
  bucket. Mitigation lives in PRD FR4.4 (sole-writer model).
- **AS-2: Compromised inference workload tampers with another workload's
  BOM.** Same mitigation as AS-1.
- **AS-3: CRD-injection attack via malicious AIBOMControllerConfig.** A
  cluster admin with cluster-config write access is already trusted; mitigation
  is to restrict who can write `AIBOMControllerConfig` via RBAC, treat the
  resource as security-sensitive.
- **AS-4: Sink credential exfiltration via reading the Secret.** The bearer
  token Secret for webhook and GUAC sinks lives in `k8s-aibom-system`. RBAC
  on that namespace must restrict read access to the controller's KSA and
  cluster admins only. Document this in the install guide.
- **AS-5: Denial-of-BOM-service via spec-spam.** A workload owner rapidly
  toggles labels or env vars to force regeneration. Mitigation: input-hash
  short-circuit (PRD FR3.5) and rate-limited reconciliation queues.
- **AS-6: BOM-poisoning via crafted env vars.** A workload owner sets
  `HF_MODEL_ID` to a long or malformed value to inflate BOM size or trigger
  parser issues. Mitigation: bounded-size string handling in the scraper,
  strict input validation, BOM size threshold.
- **AS-7: Sink endpoint hijack.** The configured webhook or GUAC endpoint is
  changed by an attacker who has compromised the `AIBOMControllerConfig`.
  Mitigation: treat `AIBOMControllerConfig` writes as audit-worthy events;
  GCS Workload Identity binding can't be redirected without IAM changes.

## 5. The runtime model-fetch bypass

A workload owner can write user code that fetches a model from an arbitrary
URL at runtime (e.g., `wget` from inside the container at startup). The BOM
will not capture this fetch, because the BOM is spec-driven and the URL is
not in the spec. This is an acknowledged limit (PRD NG8). It is not a defect
of the controller — it is the boundary of spec-driven scraping. v2 eBPF
scraping closes this gap. The threat model documents it explicitly so that
auditors are not misled.

## 6. Out of scope

- Vulnerability scanning of model artifacts (separate concern; existing tools
  cover container side).
- Admission control / enforcement (PRD NG1).
- Runtime threat detection (PRD NG6).
- Defending against malicious cluster admins (they have cluster scope by
  definition).
