# Design 003: Native scrapers for NIMService and LeaderWorkerSet

Status: Draft; amended 2026-09-17 after AICR-maintainer review on the
PR — priority re-ranked (Dynamo CRDs promoted above NIMService on
their recipe-footprint data; LWS re-scoped to the llm-d demand path
after a factual correction), open question 3 resolved (scraper code,
subordinate to declared sources), and the storage-source extraction
generalized beyond nimCache. Published for open review before any
code (per the release-cadence policy in VERSIONING.md); the review
window is stated on the PR. Targets the v1.6 train — deliberately not v1.5.0, which is
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
  roll-up rule. Correction from AICR review (2026-09-17): AICR does
  NOT ship LWS (their multi-node path is DynamoGraphDeployment →
  Grove); the demand path for LWS support is the llm-d ecosystem,
  whose wide expert-parallelism well-lit path deploys via LWS. LWS
  ranks last in this design accordingly and remains demand-gated on
  that path.
- The CronJob-coverage item on the same v1.6 train needs the same
  ownership machinery (CronJob → Job → Pod); this design's roll-up
  rule is written to serve both.

## Goal

One AIBOM per NIMService and per LeaderWorkerSet, carrying
declared-tier facts from the CR spec where the customer wrote them,
inferred-tier facts where derived, and no duplicate AIBOMs for
workloads those CRs own.

## Non-goals

1. **No NeMo CRDs** (NemoCustomizer, NemoGuardrails, …) in this
   design. **Dynamo CRDs (DynamoGraphDeployment) are promoted**: AICR
   review ranked them ABOVE NIMService (dynamo-platform in 15 of
   their recipes vs 4 for the NIM operator, and both stock recipes
   shipping k8s-aibom include a Dynamo lane, no NIM lane). A Dynamo
   extraction section will be added to this design within the review
   window once the DynamoGraphDeployment schema is scoped; note the
   Dynamo caveat from the same review — Dynamo images carry no model
   identity, so declared sources matter even more there.
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
| `spec.storage.*` (nimCache reference, or the storage shape: pvc / emptyDir / hostPath) | model-source fact (property on the model component) — `nimCache` is one shape, not the only one (AICR's own demo uses `emptyDir`) | `declared` |
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
3. **RESOLVED (AICR review, 2026-09-17): scraper code**, subordinate
   to declared env/args sources — the derivation is valid only
   because `nvcr.io/nim/` is one-model-per-image, which is a NIM
   property, not a general pattern (Dynamo runtime images carry no
   model). Declared NIM_MODEL_NAME/NIM_SERVED_MODEL_NAME always win;
   the image path fills in only when nothing is declared. (The env
   names themselves ship earlier, in v1.5.0 — a mechanical allowlist
   addition prompted by the same review.)
