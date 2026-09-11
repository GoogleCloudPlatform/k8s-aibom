# Design 003: Native scrapers for NIMService and LeaderWorkerSet

Status: Draft. Published for open review before any code (per the
release-cadence policy in VERSIONING.md); the review window is stated
on the PR. Targets the v1.6 train — deliberately not v1.5.0, which is
scoped to verification. Review is particularly invited from downstream
distributions shipping the NIM Operator or lws (NVIDIA AICR ships
both); open question 1 is a demand check, and a "not useful" answer
parks the corresponding half of this design per the demand-gated
backlog rule.

## Context

- v1.4.0 added image-pattern coverage for NIM (`nvcr.io/nim/*`) and
  Dynamo backend workers. Patterns attribute the runtime on generic
  workloads; they cannot read the *declared* facts that the NIM
  Operator's own API carries.
- `NIMService` (`apps.nvidia.com/v1alpha1`, NVIDIA/k8s-nim-operator)
  declares the serving image, model storage source, environment, and
  multi-node topology as structured fields. The KServe
  `InferenceService` scraper is the in-repo precedent for CRD-declared
  extraction: unstructured access, no Go dependency on the external
  project, declared-tier confidence for fields the customer wrote.
- `LeaderWorkerSet` (`leaderworkerset.x-k8s.io/v1`, kubernetes-sigs/lws)
  is the emerging primitive for multi-node inference (vLLM/SGLang
  multi-host serving). Its `leaderTemplate` and `workerTemplate` are
  full PodTemplateSpecs — the existing inference extraction applies
  unchanged; what is missing is the watch, RBAC, and an ownership
  roll-up rule.
- The CronJob-coverage item on the same v1.6 train needs the same
  ownership machinery (CronJob → Job → Pod); this design's roll-up
  rule is written to serve both.

## Goal

One AIBOM per NIMService and per LeaderWorkerSet, carrying
declared-tier facts from the CR spec where the customer wrote them,
inferred-tier facts where derived, and no duplicate AIBOMs for
workloads those CRs own.

## Non-goals

1. **No NeMo CRDs** (NemoCustomizer, NemoGuardrails, …) and **no
   Dynamo CRDs** in this design. Both are follow-on candidates gated
   on the same demand check; adding them later reuses this design's
   pattern without amendment.
2. **No operator-infrastructure images as runtimes.** The v1.4.0
   guard cases stand: k8s-nim-operator's own controller images are
   infrastructure, not serving runtimes.
3. **No reconciliation with NIMCache contents.** The scraper records
   the `nimCache` reference as declared fact; resolving what the cache
   holds requires reading another CR's status and is deferred until a
   consumer asks.

## Decision

### 1. NIMService scraper (KServe pattern)

Watch `NIMService` when the CRD is present (absent CRD = kind not
watched, no error — the existing KServe behavior). Extraction map,
all via unstructured access:

| Source field | Component / fact | Confidence |
|---|---|---|
| CR kind itself | `application` component, runtime `nim` | `declared` (the customer chose NIM serving; no inference involved) |
| `spec.image.repository` + `.tag` | `container` component | `declared` |
| NIM image path (`nvcr.io/nim/<org>/<name>`) | `machine-learning-model` identity `<org>/<name>` | `inferred` (derived from the image path; NIM images encode the model, but the path is not a declaration) |
| `spec.storage.nimCache` | model-source fact (property on the model component) | `declared` |
| `spec.env` / `spec.args` | through the existing model-identity allowlists, unchanged | as today |
| `spec.multiNode` (presence) | topology property on the runtime component | `declared` |

Everything flows through the existing BOM-build redaction boundary
(#57); no new emission paths.

### 2. LeaderWorkerSet support

- Watch `LeaderWorkerSet` (`v1`); RBAC adds get/list/watch on
  `leaderworkersets.leaderworkerset.x-k8s.io`.
- Scrape **both** pod templates with the existing inference
  extraction; evidence locators are prefixed
  `spec.leaderWorkerTemplate.leaderTemplate…` /
  `…workerTemplate…` so auditors see which role carried the signal.
- One AIBOM per LWS, keyed to the LWS UID (the controlling template
  is the LWS, not the workloads it materializes).

### 3. Ownership roll-up (shared with CronJob coverage)

A tracked workload that is owned — directly or transitively via
`ownerReferences` — by another *tracked* workload kind is not
separately reported; the owner's AIBOM is the report. Concretely: the
StatefulSets an LWS materializes, and the Jobs a CronJob spawns,
produce no AIBOMs of their own while their owner is tracked. If the
owner kind is not tracked (CRD absent, kind disabled), the owned
workload is reported as today — coverage never regresses by adding
this rule. Suppressed-owned-workload identities are recorded as
properties on the owner's AIBOM so nothing silently disappears.

## Degradation

Unchanged philosophy: absent CRDs mean the kind is not watched;
malformed CRs produce `Ready=False` with reason on that AIBOM only;
nothing here can fail another workload's reconcile.

## Testing

- Unit: one fixture per extraction-map row for NIMService; LWS
  fixtures with runtime signal in leader-only, worker-only, and both
  templates; roll-up fixtures (LWS→StatefulSet, CronJob→Job, and the
  untracked-owner fallback).
- e2e (kind): both CRDs installed as test-only fixtures (the KServe
  suite pattern), one live CR each. No GPU required — extraction is
  spec-level.

## Rollout

Additive MINOR, v1.6 train. Implementation begins after this
document's review window closes and after the v1.5.0 tag, whichever
is later.

## Open questions for reviewers

1. **Demand check (distributions shipping these operators):** is
   NIMService extraction useful to your users, and where does Dynamo
   CRD extraction rank against it? A clear "not useful" parks that
   half of this design.
2. **Roll-up representation:** are suppressed-owned-workload
   properties on the owner's AIBOM sufficient, or do consumers need a
   stub AIBOM per owned workload pointing at the owner?
3. **NIM model-from-image-path:** scraper logic (as designed) or a
   generalized "model identity from image path" pattern in the
   runtime-patterns config? The latter is more reusable; the former
   keeps the pattern file's scope honest (runtime attribution only).
