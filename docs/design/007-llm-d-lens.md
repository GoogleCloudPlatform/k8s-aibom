# Design 007: llm-d lens — DisaggregatedSet coverage and InferencePool membership

Status: Accepted 2026-10-08 (review window on #139); Part 2 amendment
accepted 2026-11-10 (review window 5–10 November on #331). Part 1
implemented on main the following day; Part 2a targets v1.7 and Part
2b v1.8.
Tracks #137. Review from llm-d maintainers and anyone running the
wide-EP well-lit path is still invited on the implementation.

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

**Amendment accepted 2026-11-10** (proposed for #137 on 2026-11-05;
review window 5–10 November on PR #331, no objections). Per
`VERSIONING.md`, implementation begins only after the design amendment
is accepted; Part 2a implementation is open as #356. Part 2a (document
facts, CRD discovery and freshness) targets v1.7 (November 17 rc.1
scope freeze), or a later train if review or testing needs longer.
Part 2b (status summary and `find --pool`) targets v1.8 as a separate
PR. Part 1 is implemented and unchanged.

**Resolution.** For each tracked inference workload that produces its
own AIBOM, compare each same-namespace `InferencePool`'s
`spec.selector.matchLabels` with labels on the Pod templates already
read by that workload's scraper. All selector key/value pairs must
match one template (AND within a selector, OR across the workload's
templates); do not combine labels from different templates. This
includes the existing per-role/per-component templates of tracked
third-party workloads, not their live Pods. A workload with no
declared Pod-template labels cannot match. Do not infer template
labels from the top-level object's labels or its owned Pods.
DisaggregatedSet per-role templates and Dynamo per-component templates
count as the respective workload's templates; labels added only to
live Pods by a mutating webhook do not count. The pool and workload
must have the same namespace. Only workloads that already pass the
normal opt-in, kind-allowlist, classification, and
ownership roll-up rules receive a document; a pool match never makes
an unrecognized workload eligible for an AIBOM.

Deduplicate matching pools by `(namespace, name)`, sort by that pair,
and number the properties from zero. A workload with multiple matching
templates still records a pool only once. A workload that is rolled
up into an owner has no separate document; matching is performed on
the templates represented by the reported root rather than creating
a pool-driven ownership link.

**Document representation (accepted on PR #331).** Record the facts on
the workload's root `metadata.component`, not `metadata.properties`,
model components, or pod components:

| Property | Value |
|---|---|
| `k8s-aibom:routing:inferencePool:<i>` | `<namespace>/<name>` of the matching pool |
| `k8s-aibom:routing:inferencePool:<i>:endpointPicker` | `spec.endpointPickerRef.name`, only when present |

The `routing` sub-namespace and both property names are accepted.
Part 2a adds them to `docs/cyclonedx-property-taxonomy.md` in its
implementation PR, after or alongside the `k8s-aibom:` rename in #132.
An absent `endpointPickerRef` is not an error. No Endpoint Picker
resource is fetched or validated, and no traffic or model-identity
inference is made from it.

Why this does not violate the ownership-only rule: the rule exists
because a label match is not evidence that a pod *belongs to* a
workload, and a tenant can plant labels. An `InferencePool` is defined
by the API to route to whatever its selector matches. Recording "this
pool's selector matches this workload" is recording the pool's own
semantics, a *configured selector match*, and it attributes nothing —
no owned pod, digest, model, identity or observed network traffic — to
the workload. It does not prove that a Gateway references the pool,
that the Endpoint Picker is healthy, or that requests flow to it.

**API pin (confirmed on PR #331).** The upstream Gateway API Inference
Extension v1.6.2 CRD is namespaced
`inferencepools.inference.networking.k8s.io`, served and stored at
`inference.networking.k8s.io/v1`. Its selector is
`spec.selector.matchLabels` (nonempty map, all pairs required), and
`spec.endpointPickerRef` is optional. The issue's earlier text and
the November 4 maintainer comment instead named the older
`inference.networking.x-k8s.io` group. The released upstream v1 GVK
is the only version supported by Part 2a; do not silently implement
both API groups.

**Discovery and freshness.** Reuse #226's CRD discovery, readiness,
late-installation, removal, and recovery decisions to start/stop an
isolated read-only `InferencePool` list/watch. A pool is *not* a
tracked workload kind: it has no AIBOM of its own, must not enter
`TrackedKinds` or the workload-kind allowlist, and cannot participate
in ownership roll-up. Add one read-only `get;list;watch` ClusterRole
rule for `inferencepools.inference.networking.k8s.io` to
`config/rbac/role.yaml` and the Helm chart, with the same upgrade
note shape as #226. Existing CRD discovery permissions are reused.
No third-party resource writes.

On initial pool-watch sync (including a CRD installed after startup),
pool add/update/delete, confirmed CRD removal/re-creation, or recovery
from an unreadable watch, re-enqueue affected reported inference
workloads (namespace-scoped for a pool change, across opted-in
namespaces for CRD-wide transitions). Workload-template label changes
already trigger the normal workload reconcile. Recompute membership
from the current usable pool view on each reconcile; selector changes
must remove old matches as well as add new ones. Include only the
sorted membership facts in the input-hash decision before the existing
unchanged-input fast path, so pool-only fact changes rebuild the
document and reach sinks. Do not hash watch or lookup availability:
watch flaps alone must not cause new document emission.

If the CRD is absent, unready, or the pool view cannot be read
reliably, create no new positive pool facts and do not remove
previously published membership solely because the view is
unavailable. Defer membership updates until a usable pool view
returns; then reconcile against that view and remove stale facts.
Retry/recover through the same discovery/watch mechanisms.
Transient watch errors alone are not proof of CRD removal (the #226
rule). Avoid deleting unrelated AIBOMs or changing the underlying
workload's runtime/model/digest facts.

**Part 2b — summary and plugin (v1.8, separate PR).** Extend
`AIBOM.status.summary` additively with sorted pool names (namespace
is already in `summary.workload`) and an
explicit pool-membership lookup state: `known` after a healthy pool
list/watch has synchronized, `unavailable` when discovery or reads
cannot establish membership. In the `known` state an empty pool list
means no selector matched. `kubectl aibom find --pool <name>` filters
by exact pool name within the requested namespace scope, ANDed with
existing filters. For `unavailable` or pre-feature summaries, show
the unevaluable row with a warning rather than silently excluding it,
following the existing image/digest filter rule. These are CRD schema
changes and ship in their own v1.8 PR, including regenerated CRD
descriptions, to honor `VERSIONING.md`'s prohibition on bundling
API-contract changes with feature work. Until Part 2b ships, the
plugin has no `--pool` filter; Part 2a publishes only document facts.

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
| InferencePool CRD absent, unready or list/watch unavailable | no new positive pool properties; retain previously published membership until the view is readable again; unrelated facts unchanged; recovery retried |
| A role's template does not decode | recorded on the document's errors, other roles still extracted (the Dynamo precedent) |

## Testing

- Unit: per-role extraction with role-rooted locators; leader-only,
  worker-only, both; slices/size/replicas properties; malformed role
  degrades per role; determinism; the apps/v1 and LWS locators are
  unchanged.
- Envtest Part 1 (already delivered): a wide-EP-shaped DisaggregatedSet
  in an opted-in namespace produces one AIBOM; an LWS it owns produces
  none and appears in `aibom.rollup.owned`; a pod two hops down
  resolves its digest onto the DisaggregatedSet.
- Part 2a unit/envtest: matching and nonmatching Pod templates (including
  no live Pods), different namespace, multiple templates and pools,
  deterministic ordering, deduplication, and absence of model/digest
  or ownership changes. Include the optional Endpoint Picker fact.
- Part 2a lifecycle envtest: CRD absent at boot then installed, pool
  added/changed/deleted, selector and workload labels changed, CRD
  removed/re-created, watch/read failure and recovery; assert that
  an unavailable view preserves existing facts without re-emission,
  that stale facts clear once a readable view returns, and that
  changes to pool facts bypass input-hash deduplication. Use a
  minimal CRD fixture consistent with the pinned upstream version.
- Part 2b tests (in the separate v1.8 PR): summary lookup state and
  `find --pool`, including unavailable or older summaries that
  cannot safely be excluded.

## Rollout

Part 1: additive MINOR on the v1.6 train, with the llm-d guide (already
open as llm-d/llm-d#2573) updated to show the DisaggregatedSet case.
Part 2a: v1.7 if the stated review window, #132 property rename, and
tests permit; otherwise the next available train. It adds properties
to documents of workloads that match pools, recorded in the CHANGELOG
alongside v1.7's CycloneDX byte-shape transition if it lands on that
train. Part 2b: v1.8, in a separately reviewed PR, adds the status
summary fields and `find --pool` API surface.

## Open questions

1. For a DisaggregatedSet, should `aibom.rollup.owned` list the
   LeaderWorkerSets (one per role) or also the StatefulSets beneath
   them? Proposal: tracked kinds only, which is both, consistent with
   Design 005; reviewers who find the list noisy should say so.
