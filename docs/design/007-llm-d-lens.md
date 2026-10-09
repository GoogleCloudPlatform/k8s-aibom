# Design 007: llm-d lens — DisaggregatedSet coverage and InferencePool membership

Status: Accepted 2026-10-08 (review window on #139). Part 1 implemented
on main the following day; Part 2 targets v1.7. Tracks #137. Review
from llm-d maintainers and anyone running the wide-EP well-lit path is
still invited on the implementation.

Implementation note (2026-10-09): the Part 1 table below places the
LeaderWorkerSet fields directly under `spec.roles[i]`. The served
schema (lws v0.11.1 types, its CRD, llm-d's wide-EP manifest) inlines a
complete LeaderWorkerSet *template* per role — `metadata` plus `spec` —
so the real paths are `spec.roles[i].spec.replicas` and
`spec.roles[i].spec.leaderWorkerTemplate.{size,leaderTemplate,workerTemplate}`;
`spec.slices` is as written. The implementation and
docs/external-crd-versions.md use the served paths; the table is kept
as the record of what was reviewed. Also noted while implementing: since
llm-d v0.8 the well-lit paths run upstream `vllm/vllm-openai` as the
model-server image and v0.10.0 deprecated `llm-d-cuda`/`llm-d-aws`, so
llm-d membership increasingly cannot be read from image names — which
is the case for Part 2.

## Context

llm-d is the Kubernetes-native distributed inference project GKE
helped found and contributed to the CNCF. Its well-lit paths are the
deployments k8s-aibom should be most correct on, and today it is
correct on the smaller half:

- The **wide expert-parallelism** guide, the path that matters on GKE
  NVIDIA hardware, deploys a `DisaggregatedSet`
  (`disaggregatedset.x-k8s.io/v1`, from the LeaderWorkerSet project,
  lws ≥ 0.11, released 2026-09-23) that manages the prefill and decode
  roles together. Plain `LeaderWorkerSet` is now the Intel XPU variant
  of that guide. Our LWS scraper (Design 003 §2) covers the XPU path
  and nothing on the NVIDIA path.
- Every llm-d deployment is fronted by an **`InferencePool`**
  (Gateway API Inference Extension), which is selector-based and maps
  one-to-one to a logical model deployment: the Endpoint Picker chooses
  among the pods the pool's selector matches, and the Gateway routes to
  the pool. Nothing in a k8s-aibom document today says which pool, and
  therefore which route, serves a model.

Both gaps sit on the path to the ask the project actually wants to
make: k8s-aibom as an optional component of the llm-d modelservice
chart, default off, then default on for the GKE provider path. The ask
is credible only once the output is right on their stack.

## Goal

A DisaggregatedSet is reported as one AIBOM with per-role,
per-template evidence locators, the LeaderWorkerSets it materializes
rolled up into it; and every inference workload a pool fronts carries
the pool's identity as a routing fact.

## Non-goals

- Using `InferencePool` selectors for attribution of pods, digests or
  models. The ownership-only rule (pod_ownership.go, Design 005) is not
  relaxed by this design; see §2 for why the pool is different.
- Modelling llm-d's routing policy, scheduler plugins or KV-cache
  topology. Those are behaviours, not inventory.
- A `ModelService` kind. llm-d's modelservice is a Helm chart, not a CR.

## Decision

### Part 1 — `DisaggregatedSet` as a reported kind

`DisaggregatedSet.spec.roles[i]` embeds a LeaderWorkerSet template
spec inline, so the extraction is the LWS scraper applied once per
role:

| Source | Becomes |
|---|---|
| `spec.roles[i].leaderWorkerTemplate.leaderTemplate` (optional) | shared inference extraction; locators rooted at `spec.roles[i].leaderWorkerTemplate.leaderTemplate.spec`; properties `disaggregatedset.role: <roles[i].name>`, `lws.role: leader` |
| `spec.roles[i].leaderWorkerTemplate.workerTemplate` | same, `…workerTemplate.spec`, `lws.role: worker` |
| `spec.roles[i].leaderWorkerTemplate.size`, `spec.roles[i].replicas`, `spec.slices` | `lws.size` / `lws.replicas` / `disaggregatedset.slices` on container components |
| `metadata.annotations` | model claims; signature references, with each template's annotations as fallback |

- Runs under the Design 004 supervisor, pinned to
  `disaggregatedset.x-k8s.io/v1`, registered only when the CRD is
  present. RBAC: get/list/watch `disaggregatedsets.disaggregatedset.x-k8s.io`.
- The `LeaderWorkerSet`s a DisaggregatedSet materializes are
  controller-owned by it and roll up under Design 005 once the kind is
  tracked; their StatefulSets and pods follow through the existing
  closure, so pod-status digests reach the DisaggregatedSet's document.
- Implementation reuses `LeaderWorkerSetScraper.scrapeTemplate` with a
  locator root parameter; no new extraction logic. Confidence
  semantics are unchanged: a DisaggregatedSet declares nothing about
  what it runs, so runtime attribution is image-pattern inferred.
- `DisaggregatedSetRoleScaler` (same group) is a scaling adapter, not
  a workload, and is not read.

### Part 2 — `InferencePool` membership as a routing fact

For each inference workload in a namespace, record the `InferencePool`
objects whose `spec.selector` matches the workload's pod template
labels, as properties on the workload's root component:

| Property | Value |
|---|---|
| `inference.pool.<i>` | `<name>` of a matching `InferencePool` in the namespace (sorted) |
| `inference.pool.<i>.endpointPicker` | the pool's `endpointPickerRef`/`extensionRef` service name, when set |

Why this does not violate the ownership-only rule: the rule exists
because a label match is not evidence that a pod *belongs to* a
workload, and a tenant can plant labels. An `InferencePool` is defined
by the API to route to whatever its selector matches. Recording "this
pool's selector matches this workload" is recording the pool's own
semantics, a routing fact, and it attributes nothing — no digest, no
model, no identity — to the workload. It answers "which route serves
this model" without becoming a source of truth for what the model is.

- Read via the dynamic client when the `InferencePool` CRD is present;
  group `inference.networking.x-k8s.io`, version pinned to what GAIE
  serves as storage at implementation time (v1 in current releases).
  RBAC: get/list `inferencepools.inference.networking.x-k8s.io`.
  Absent CRD: no properties, no error.
- Namespace-scoped list per reconcile, cache-backed through the
  supervisor's shared mechanism or a dedicated small cache; the number
  of pools per namespace is small by construction (one per logical
  model deployment).
- `kubectl aibom find` gains `--pool <name>` in the same change if Part
  2 lands in v1.6; otherwise with v1.7.

### Fixture

The envtest fixtures are built from the llm-d wide-EP guide's
manifests (the `DisaggregatedSet` with prefill and decode roles, the
`InferencePool` and its Endpoint Picker), trimmed to the fields read
and with the guide's images, so the tests exercise what llm-d actually
deploys rather than a shape invented here. Minimal test-only CRDs for
`DisaggregatedSet` and `InferencePool` under `config/crd/external/`,
per the existing rule against vendoring upstream CRDs verbatim.

## Degradation

| Situation | Result |
|---|---|
| DisaggregatedSet CRD absent | kind not registered (log), as every third-party kind |
| DisaggregatedSet watch unhealthy | Design 004: Degraded for that kind only |
| InferencePool CRD absent or list forbidden | no `inference.pool.*` properties; logged once; nothing else changes |
| A role's template does not decode | recorded on the document's errors, other roles still extracted (the Dynamo precedent) |

## Testing

- Unit: per-role extraction with role-rooted locators; leader-only,
  worker-only, both; slices/size/replicas properties; malformed role
  degrades per role; determinism; the apps/v1 and LWS locators are
  unchanged.
- Envtest: a wide-EP-shaped DisaggregatedSet in an opted-in namespace
  produces one AIBOM; an LWS it owns produces none and appears in
  `aibom.rollup.owned`; a pod two hops down resolves its digest onto
  the DisaggregatedSet. With the `InferencePool` CRD installed, a
  Deployment matching a pool's selector carries `inference.pool.0`; a
  Deployment that matches no pool carries nothing; the pool's
  selector never changes which pods contribute digests.

## Rollout

Part 1: additive MINOR on the v1.6 train, with the llm-d guide (already
open as llm-d/llm-d#2573) updated to show the DisaggregatedSet case.
Part 2: v1.7 unless review agrees it is small enough to ride with Part
1; it adds properties to documents of workloads that have pools, which
is a byte-shape change for those documents only, noted in the
CHANGELOG with the purl and lifecycle changes already planned for v1.7.

## Open questions

1. Should the InferencePool fact also be summarized on
   `AIBOM.status.summary` (a `pools: []` list) so `kubectl aibom find
   --pool` can work without decoding documents? Proposal: yes, it is
   the incident path; the summary already carries runtime and models.
2. For a DisaggregatedSet, should `aibom.rollup.owned` list the
   LeaderWorkerSets (one per role) or also the StatefulSets beneath
   them? Proposal: tracked kinds only, which is both, consistent with
   Design 005; reviewers who find the list noisy should say so.
