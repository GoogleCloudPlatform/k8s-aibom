# Design 003: Native CRD scrapers — Dynamo, NIMService, LeaderWorkerSet

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
- `DynamoGraphDeployment` (`nvidia.com/v1beta1`, ai-dynamo/dynamo
  operator; `v1alpha1` also served, with conversion) declares an
  inference graph as typed components. Schema facts (scoped
  2026-09-24 against the operator's `v1beta1` types): a validated
  `spec.backendFramework` enum (`vllm|sglang|trtllm`); per-component
  `modelRef {name, revision}`; a component `type` enum
  (`frontend|worker|prefill|decode|planner|epp`); and a full
  `PodTemplateSpec` per component. Components materialize as child
  `DynamoComponentDeployment` CRs, which in turn create Deployments,
  LeaderWorkerSets, or Grove resources depending on topology.
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

One AIBOM per DynamoGraphDeployment, per NIMService, and per
LeaderWorkerSet, carrying
declared-tier facts from the CR spec where the customer wrote them,
inferred-tier facts where derived, and no duplicate AIBOMs for
workloads those CRs own.

## Non-goals

1. **No NeMo CRDs** (NemoCustomizer, NemoGuardrails, …) in this
   design. **Dynamo CRDs are in** (Decision §4, added in-window
   2026-09-24 as committed): AICR review ranked them ABOVE NIMService
   (dynamo-platform in 15 of their recipes vs 4 for the NIM operator,
   and both stock recipes shipping k8s-aibom include a Dynamo lane,
   no NIM lane) — implementation order follows that ranking, not the
   section numbering. The Dynamo caveat from the same review (Dynamo
   images carry no model identity, so declared sources matter even
   more) is binding on §4's extraction rules.
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

### 4. DynamoGraphDeployment scraper (added in-window, 2026-09-24)

Scoped against `ai-dynamo/dynamo` `deploy/operator/api/v1beta1`.

- Watch `DynamoGraphDeployment` (`nvidia.com/v1beta1`); RBAC adds
  get/list/watch on `dynamographdeployments.nvidia.com` and
  `dynamocomponentdeployments.nvidia.com`.
- One AIBOM per DGD, keyed to the DGD UID. Extraction map:
  - `spec.backendFramework` (validated enum `vllm|sglang|trtllm`) →
    serving runtime, **declared** — the customer wrote it and the API
    server enforced the vocabulary. Per-component pod-template
    extraction remains the digest source and the inferred fallback.
  - `spec.components[i].modelRef.{name,revision}` → model identity,
    **declared**, attributed to the component that carries it.
    Honoring the AICR-review caveat: Dynamo runtime images carry no
    model identity, so there is NO image-path model derivation for
    Dynamo — absent `modelRef` and declared env, the model stays
    `unresolved`. Conservative-detection rule, applied strictly.
  - `spec.components[i].type`
    (`frontend|worker|prefill|decode|planner|epp`) → recorded as
    component-role properties (graph topology is an auditable fact);
    model attribution follows wherever `modelRef`/env actually sit
    (typically worker/prefill/decode). Roles never imply models.
  - `spec.components[i].podTemplate` is a full `PodTemplateSpec` —
    the existing inference extraction applies unchanged (args, env
    allowlist including the v1.5.0 `NIM_*` names, images); evidence
    locators are prefixed `spec.components[i].podTemplate…`.
- The §3 roll-up applies unchanged and is materialization-agnostic:
  DGD → `DynamoComponentDeployment` → (Deployment | LeaderWorkerSet |
  Grove resources) → Pods all roll up to the DGD's AIBOM via
  `ownerReferences`, whatever the operator chose to create. AICR's
  multi-node path (DGD → Grove) and the LWS path are therefore the
  same case. A `DynamoComponentDeployment` created standalone (no DGD
  parent) is tracked as its own root with the same extraction map —
  the fields above live on the shared component spec.
- Version skew: the extraction map is defined against `v1beta1`.
  Clusters serving only `v1alpha1` are read through the dynamic
  client as today; fields absent at runtime degrade per the standard
  rules (that fact `unresolved` or omitted, never a failed
  reconcile). Whether `v1alpha1` warrants its own fixtures is Open
  Question 4.

## Degradation

Unchanged philosophy: absent CRDs mean the kind is not watched;
malformed CRs produce `Ready=False` with reason on that AIBOM only;
nothing here can fail another workload's reconcile.

## Testing

- Unit: one fixture per extraction-map row for NIMService; LWS
  fixtures with runtime signal in leader-only, worker-only, and both
  templates; roll-up fixtures (LWS→StatefulSet, CronJob→Job, and the
  untracked-owner fallback).
- Unit (Dynamo): one fixture per §4 extraction row — per-component
  `modelRef` attribution, the `backendFramework` enum, role
  recording, the no-image-derivation rule (Dynamo image + no
  declarations → model `unresolved`), and roll-up
  (DGD→DynamoComponentDeployment→Deployment, plus standalone DCD).
- e2e (kind): all three CRD sets installed as test-only fixtures (the
  KServe suite pattern), one live CR each. No GPU required —
  extraction is spec-level.

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
4. **Dynamo `v1alpha1`:** is a `v1alpha1` fixture set worth carrying,
   or is `v1beta1`-only acceptable for the v1.6 train given the
   operator versions AICR actually ships? (The scraper degrades
   gracefully either way; this only decides test surface.)
