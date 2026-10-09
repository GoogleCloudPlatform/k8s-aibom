# External CRD version pinning

This document records the explicit version pins between k8s-aibom and
the third-party Kubernetes CRDs its scrapers extract from.

The controller does NOT install these CRDs in customer clusters —
customers install them via the respective project's own packaging
(Helm chart, operator, etc.). What k8s-aibom commits to is: "given a
CRD at the pinned version, the scraper extracts the documented field
paths." A CRD upgrade that renames or removes those paths is an
upgrade-blocking event for that scraper.

The minimal CRD copies vendored under
[`config/crd/external/`](../config/crd/external/) are TEST-ONLY: they
exist so `envtest` can validate CR creation against a schema that
won't break when the upstream project ships patch versions of the
pinned API. They are NOT a substitute for the real CRDs in
production. Each file's header comment carries an explicit
"do-not-apply-to-real-clusters" warning.

## Invariant: read-only toward third-party CRDs

k8s-aibom never applies, patches or ships any CRD it does not own. The
minimal CRDs under `config/crd/external/` exist so envtest can create
real-shape CRs; each file carries a do-not-apply warning. In a real
cluster the controller only reads third-party CRs, and registers a
watch for a kind only when its CRD is already present.

## Watch isolation and health (Design 004)

Every third-party kind below runs under the `WatchSupervisor`
(`internal/controller/watch_supervisor.go`): its own informer cache,
never the manager's, and an unmanaged controller the supervisor
probes, starts, and rebuilds with capped backoff (5 s → 5 min). A kind
whose CRD is present but unservable — the canonical case is a Dynamo
conversion webhook whose operator is down — degrades **that kind
only**:

| Signal | Where |
|---|---|
| `Degraded=True`, reason `ThirdPartyWatchUnhealthy`, message naming each kind and the verbatim API error | `AIBOMControllerConfig` status (same place as the served-schema skew check) |
| `WatchUnhealthy` (Warning) once per outage; `WatchRecovered` (Normal) once on recovery | Events on the controller Pod |
| `aibom_watch_healthy{kind}` 1/0; `aibom_watch_errors_total{kind}` | Metrics endpoint |

While a kind is unhealthy: its existing AIBOMs are kept as last known,
new or changed workloads of that kind are not inventoried, no cleanup
runs for it, every other kind keeps working, and readiness is
unchanged (a down Dynamo operator must not make k8s-aibom unready).
Recovery is automatic: a clean probe clears the condition and the
informer picks up what it missed.

Steady-state detection is a `Limit: 1` list per present kind every two
minutes. It has to be: when an established watch stream hits a
conversion error, client-go's reflector does not call its error
handler — it logs at warning level and re-opens the watch from the
same resource version, so the object is never delivered and nothing
fails loudly. The probe takes the same conversion path and fails
honestly. Cost with AICR's two kinds: about one request per minute on
top of the measured sub-1-req/min steady state.

Watching a third-party CRD installed *after* the controller starts
still requires a restart; the presence check at startup decides which
kinds the supervisor runs.

## Ownership roll-up (Design 005)

One workload, one AIBOM. A tracked workload owned, directly or
transitively via controller `ownerReferences`, by another tracked kind
(the apps/v1 and batch kinds plus every third-party kind the supervisor
runs) is not separately reported. The owner's document records each
absorbed workload as `aibom.rollup.owned.<i>` (`Kind/name`, sorted) and
receives the descendants' pods, which is how the CRD kinds resolve
pod-status digests. Attribution is ownership only: Dynamo's pod labels
exist but are never consulted, because a tenant pod can carry a label
and cannot forge an `ownerReference` to an object it does not control.

The upward walk from a child is bounded (six hops) and reads each owner
once as unstructured. An owner the controller cannot read — missing
RBAC, CRD absent, deleted — ends the walk as *unresolved* and the child
is reported as before (`rollup_unresolved` outcome); coverage never
regresses because of a permission. Roots list their descendants from
cache-backed typed lists plus live lists of the intermediate CRD kinds
that are present: `DynamoComponentDeployment` and the Grove pod-owning
kinds `PodCliqueSet`, `PodCliqueScalingGroup`, `PodClique`
(`grove.io/v1alpha1`; read-only, never watched). Under Grove the
`PodCliqueSet` is owned directly by the graph, and both
`PodCliqueSet → PodClique` and `PodCliqueSet → PodCliqueScalingGroup →
PodClique` resolve; Grove kinds never appear in `aibom.rollup.owned`.
Every hop validates the fetched owner's UID against the reference, so
an owner re-created under the same name cannot adopt a stale child.
Minimal test-only Grove CRDs live under `config/crd/external/`.

## Pinned CRDs

### KServe `serving.kserve.io/v1beta1.InferenceService`

**Scraper:** `internal/scraper/kserve.go` (`KServeInferenceServiceScraper`,
`Name() = "inference.kserve"`)

**Reconciler:** `internal/controller/kserve_controller.go`

**Field paths the scraper reads** (from `spec`):

| Path | Used as |
|---|---|
| `spec.predictor.model.modelFormat.name` | Runtime application Component name |
| `spec.predictor.model.modelFormat.version` | Runtime Component version |
| `spec.predictor.model.runtime` | Recorded as `kserve.runtime.ref` property (not followed in v1) |
| `spec.predictor.model.storageUri` | ML-model Component identity |
| `spec.predictor.serviceAccountName` | Recorded as `kserve.serviceAccountName` property |
| `metadata.annotations` (`model.k8saibom.dev/*`) | Additional ML-model Components |

**Test-only minimal CRD:** [`config/crd/external/serving.kserve.io_inferenceservices.yaml`](../config/crd/external/serving.kserve.io_inferenceservices.yaml)

**Upgrade obligations.** A KServe upgrade that introduces v1beta2 or
v1 with breaking changes to any of the listed paths requires explicit
scraper work:

- The `KServeInferenceServiceScraper`'s `HandlesKind` is pinned to
  v1beta1 (see `kserveHandledKinds` in `kserve.go`). A new spec
  version's CRs will NOT be picked up by the v1 scraper. The
  controller will silently produce no AIBOMs for the new version
  until the scraper is extended.
- When extending: add the new GVK to `kserveHandledKinds`. If the
  field paths shifted, add per-version extraction logic OR fork to a
  new scraper named `inference.kserve.v1` (or similar) so historical
  BOMs continue to reference the original `inference.kserve` scraper
  identity correctly.
- When v2 (post-v1.0) adds deeper extraction (following the
  ServingRuntime reference, resolving managed-Deployment pod digests),
  name the new scraper `inference.kserve.deep` to preserve the v1
  scraper identity in historical BOMs.

**Why no kserve Go module dependency.** v1 deliberately uses
`*unstructured.Unstructured` rather than the typed
`kserve.io/api/v1beta1` Go module. The extraction surface is small
(4 nested field paths + workload annotations); the module adds
~20MB of transitive go.sum entries; the unstructured access is
contained in one file and easy to swap if the surface ever grows. See
the godoc on `KServeInferenceServiceScraper` for details on how
the transition to v2 (to resolve container image digests by traversing
down to managed Pods) is triggered.

### NVIDIA Dynamo `nvidia.com/v1beta1.DynamoGraphDeployment`

**Scraper:** `internal/scraper/dynamo.go` (`DynamoGraphDeploymentScraper`,
`Name() = "inference.dynamo"`) — Design 003 §4.

**Reconciler:** `internal/controller/dynamo_controller.go`

**Field paths the scraper reads** (from `spec`):

| Path | Used as |
|---|---|
| `spec.backendFramework` (enum `sglang` \| `vllm` \| `trtllm`) | Runtime application Component, **declared**; `trtllm` is recorded as runtime `tensorrt-llm` so it matches the pattern table's name for the same runtime image |
| `spec.components[i].name`, `spec.components[i].type` | `dynamo.component.name` / `dynamo.component.type` properties on every Component extracted from that graph component; graph topology recorded as `dynamo.component.<name>.type` on the runtime Component. Roles never imply models |
| `spec.components[i].modelRef.{name,revision}` | ML-model Component, **declared**, attributed to its component (`model.revision` when set) |
| `spec.components[i].podTemplate` | Full PodTemplateSpec through the shared inference extraction (images, env/arg allowlists, volume mounts, template annotations); locators rooted at `spec.components[i].podTemplate.spec` |
| `spec.components[i].roles[j].{name,podTemplate}` | Same, rooted at `spec.components[i].roles[j].podTemplate.spec`, plus `dynamo.role.name` |
| `metadata.annotations` (`model.k8saibom.dev/*`) | Additional ML-model Components; signature claims (Design 002) |

**Binding rule (AICR review of Design 003):** no model identity is
derived from Dynamo image paths. Dynamo runtime images carry no model;
absent `modelRef` and declared env/args the model stays `unresolved`.

**Not read:** `status.*`; `spec.env` (graph-wide env — a follow-up if a
consumer shows a model declared there). Pods are reached through the
ownership roll-up (DGD → DynamoComponentDeployment → Deployment / LWS /
Grove → Pod), so pod-status digests resolve as for a Deployment.

**`DynamoComponentDeployment` (same group/version)** is handled by the
same scraper: a component with no graph parent is its own root, read as
a one-component graph with locators rooted at `spec`; a component owned
by a graph is absorbed into the graph's document and never scraped on
its own.

**Test-only minimal CRD:** [`config/crd/external/nvidia.com_dynamographdeployments.yaml`](../config/crd/external/nvidia.com_dynamographdeployments.yaml) — serves `v1beta1` only.

**Deployed-version facts (from the AICR maintainer, 2026-10-01).**
AICR pins dynamo-platform **1.4.2** (operator image
`nvcr.io/nvidia/ai-dynamo/kubernetes-operator:1.4.2`) since AICR
v0.21.0; v0.20.0 shipped 1.2.1. In both, `DynamoGraphDeployment` and
`DynamoComponentDeployment` serve `v1alpha1` **and** `v1beta1`;
`v1alpha1` is deprecated **but still `storage: true`**, and conversion
is `Webhook`, served by the Dynamo operator. The shapes differ
(`v1alpha1`: `spec.services` map keyed by service name; `v1beta1`:
`spec.components` list; `modelRef` on each entry and top-level
`backendFramework` in both). Everything AICR creates itself uses
`v1beta1`.

**Consequences.** The controller reads `v1beta1` only and never decodes
the `v1alpha1` shape; the API server converts stored objects on read
(Design 003 open question 4: closed, no `v1alpha1` fixtures). That
makes every Dynamo list/get depend on the Dynamo operator's conversion
webhook being reachable. A down or uninstalled operator with DGDs left
behind must surface as a condition, never as zero AIBOMs — tracked in
[#127](https://github.com/GoogleCloudPlatform/k8s-aibom/issues/127),
which also carries the failed-conversion fixture.

**Dynamo 1.5 (per the Dynamo maintainers, 2026-10-02):** storage
switches to `v1beta1`; it was left at `v1alpha1` in earlier releases
intentionally, to allow operator downgrades. Nothing changes here when
that happens — `v1beta1` reads simply stop crossing the conversion
webhook, which narrows the #127 exposure to clusters running operators
≤ 1.4 (AICR's current pin).

**k8s-aibom never applies, patches or ships third-party CRDs.** The
files under `config/crd/external/` are envtest fixtures only (each
carries a do-not-apply warning); the controller reads third-party CRs
through the dynamic client and applies nothing but its own CRDs.
Operator upgrade/downgrade expectations around a project's CRDs belong
to that project.

**Upgrade obligations.** A `v1` or breaking `v1beta2` requires the same
explicit work as KServe:
extend `dynamoHandledKinds`, keep `inference.dynamo` as the historical
scraper identity, fork to `inference.dynamo.<suffix>` if field paths
move. A new `backendFramework` enum value is recorded verbatim until
`dynamoBackendRuntimes` maps it. A new `type` enum value needs no code
change (recorded verbatim).

**Why no dynamo Go module dependency.** The operator module pulls in
gateway-api-inference-extension and the Grove APIs; the read surface
here is six field paths plus PodTemplateSpecs, which decode through
`runtime.DefaultUnstructuredConverter` into `corev1` types already in
the dependency graph.

### NVIDIA NIM Operator `apps.nvidia.com/v1alpha1.NIMService`

**Scraper:** `internal/scraper/nimservice.go` (`NIMServiceScraper`,
`Name() = "inference.nimservice"`) — Design 003 §1.

**Reconciler:** `internal/controller/nimservice_controller.go`

**Field paths the scraper reads** (from `spec`):

| Path | Used as |
|---|---|
| the CR kind itself | Runtime application Component `nim`, **declared** (locator `kind`): the customer chose NIM serving |
| `spec.image.repository`, `spec.image.tag` | Container Component (`image.reference` = `repository:tag`). Digest, and therefore container confidence, only when the repository is digest-pinned — container confidence means digest provenance everywhere in this codebase, so a tag-only reference is `unresolved` exactly as on a Deployment whose pods are not listed |
| `spec.env[]`, `spec.args[]` | ML-model Components through the existing env-name / arg-flag allowlists (`NIM_MODEL_NAME`, `NIM_SERVED_MODEL_NAME`, `--model`, …); locators `spec.env[i](NAME)`, `spec.args[i](--flag)`; no `container.name` property (there is no container at that path) |
| `nvcr.io/nim/<org>/<name>` image path | ML-model Component `<org>/<name>`, **inferred**, **only when nothing is declared** via env, args or annotations. Valid solely because NIM publishes one model per image (Design 003 open question 3, resolved on AICR review). Exact, case-sensitive prefix; exactly two path segments |
| `spec.storage.{nimCache{name,profile}, pvc.name, hostPath, emptyDir}` | `nim.storage.*` properties on the runtime Component and on every model Component; `nim.storage.kind` lists the shapes present, sorted. The NIMCache's contents are never resolved (non-goal 3) |
| `spec.multiNode.{backendType, parallelism.tensor, parallelism.pipeline}`, `spec.inferencePlatform` | `nim.multiNode*` / `nim.inferencePlatform` properties on the runtime Component |
| `metadata.annotations` (`model.k8saibom.dev/*`) | Additional ML-model Components (count as declared for the image-path rule); signature claims (Design 002) |

**Not read:** `status.*` (incl. `status.model`, which is the operator's
own conclusion, not customer input); `spec.initContainers` /
`spec.sidecarContainers` (operator-injected helpers); pods. Pod-status
digests arrive with the Design 003 §3 ownership roll-up (NIMService →
Deployment / LeaderWorkerSet → Pod).

**Test-only minimal CRD:** [`config/crd/external/apps.nvidia.com_nimservices.yaml`](../config/crd/external/apps.nvidia.com_nimservices.yaml)

**Upgrade obligations.** `v1alpha1` is the operator's only served
version today. A `v1alpha2`/`v1beta1` requires the same explicit work
as KServe: extend `nimServiceHandledKinds`, keep `inference.nimservice`
as the historical scraper identity, fork to `inference.nimservice.<suffix>`
if field paths move. If NVIDIA ever publishes NIM images outside
`nvcr.io/nim/`, or more than one model per image, the image-path
derivation must be revisited — it is correct only under both facts.

**Why no NIM Operator Go module dependency.** Seven field paths plus
`[]corev1.EnvVar`, decoded through `runtime.DefaultUnstructuredConverter`
into types already in the dependency graph.

### kubernetes-sigs/lws `leaderworkerset.x-k8s.io/v1.LeaderWorkerSet`

**Scraper:** `internal/scraper/lws.go` (`LeaderWorkerSetScraper`,
`Name() = "inference.lws"`) — Design 003 §2.

**Reconciler:** `internal/controller/lws_controller.go`

**Field paths the scraper reads** (from `spec`):

| Path | Used as |
|---|---|
| `spec.leaderWorkerTemplate.leaderTemplate` (optional) | Full PodTemplateSpec through the shared inference extraction; locators rooted at `spec.leaderWorkerTemplate.leaderTemplate.spec`; every resulting Component carries `lws.role: leader` |
| `spec.leaderWorkerTemplate.workerTemplate` (required upstream) | Same, rooted at `…workerTemplate.spec`, `lws.role: worker` |
| `spec.leaderWorkerTemplate.size`, `spec.replicas` | `lws.size` / `lws.replicas` properties on container Components (omitted when unset) |
| `metadata.annotations` (`model.k8saibom.dev/*`) | Additional ML-model Components; signature claims (Design 002), with each template's annotations as fallback |

There is no declared-runtime row: an LWS declares nothing about what it
runs, so runtime attribution is image-pattern **inferred** exactly as
for a StatefulSet. One AIBOM per LWS, keyed to the LWS UID; the
StatefulSets it materializes roll up under Design 003 §3.

**Not read:** `status.*`, `subGroupPolicy`, `networkConfig`,
`rolloutStrategy`, `volumeClaimTemplates`; pods (digests resolve only
from digest-pinned references until §3).

**Test-only minimal CRD:** [`config/crd/external/leaderworkerset.x-k8s.io_leaderworkersets.yaml`](../config/crd/external/leaderworkerset.x-k8s.io_leaderworkersets.yaml)

**Upgrade obligations.** `v1` is the only served version. A `v2`
requires extending `lwsHandledKinds`; keep `inference.lws` as the
historical identity. If a future version moves the templates out of
`spec.leaderWorkerTemplate`, the locator roots must move with them.

**Why no lws Go module dependency.** Two PodTemplateSpecs and two
integers, decoded into `corev1` types already in the dependency graph.

### kubernetes-sigs/lws `disaggregatedset.x-k8s.io/v1.DisaggregatedSet`

**Scraper:** `internal/scraper/disaggregatedset.go`
(`DisaggregatedSetScraper`, `Name() = "inference.disaggregatedset"`) —
Design 007 §1. llm-d's prefill/decode primitive on NVIDIA hardware (the
wide expert-parallelism and P/D-disaggregation guides); shipped by the
LeaderWorkerSet project since lws 0.11.

**Reconciler:** `internal/controller/disaggregatedset_controller.go`

**Shape.** `spec.roles[i]` inlines a complete LeaderWorkerSet template
(`metadata` + `spec`), so the LWS fields sit one level down from the
role, under `spec.roles[i].spec`. The design note's table wrote them one
level higher; the paths below are the served schema (verified against
lws v0.11.1 types, its CRD and llm-d's wide-EP manifest).

**Field paths the scraper reads:**

| Path | Used as |
|---|---|
| `spec.roles[i].name` | `disaggregatedset.role` property on every Component the role produces (index when missing, recorded as an error) |
| `spec.roles[i].spec.leaderWorkerTemplate.leaderTemplate` (optional) | Full PodTemplateSpec through the shared LWS extraction; locators rooted at `spec.roles[i].spec.leaderWorkerTemplate.leaderTemplate.spec`; `lws.role: leader` |
| `spec.roles[i].spec.leaderWorkerTemplate.workerTemplate` | Same, rooted at `…workerTemplate.spec`, `lws.role: worker` |
| `spec.roles[i].spec.leaderWorkerTemplate.size`, `spec.roles[i].spec.replicas`, `spec.slices` | `lws.size` / `lws.replicas` / `disaggregatedset.slices` on container Components (omitted when unset) |
| `metadata.annotations` (`model.k8saibom.dev/*`) | Additional ML-model Components; signature claims (Design 002), then each role's `metadata.annotations`, then each template's |

No declared-runtime row: a DisaggregatedSet declares nothing about what
it runs, so runtime attribution is image-pattern **inferred**. One
AIBOM per set, keyed to its UID; the LeaderWorkerSets it materializes
(one per role), their StatefulSets and pods roll up under Design 005,
which is how pod-status digests reach the set's document. A role that
is not an object or a template that does not decode is recorded on the
document's errors and skipped; the other roles still extract.

**Not read:** `status.*`, `spec.placementPolicy`, `spec.roles[i].scaling`,
`rolloutStrategy`, `networkConfig`, `subGroupPolicy`,
`volumeClaimTemplates`; `DisaggregatedSetRoleScaler` (a scaling
adapter, not a workload).

**Test-only minimal CRD:** [`config/crd/external/disaggregatedset.x-k8s.io_disaggregatedsets.yaml`](../config/crd/external/disaggregatedset.x-k8s.io_disaggregatedsets.yaml)

**Upgrade obligations.** `v1` is the only served version. A `v2`
requires extending `disaggregatedSetHandledKinds`; keep
`inference.disaggregatedset` as the historical identity. If a future
version stops inlining the LWS template under `spec.roles[i].spec`, the
locator roots must move with it.

**Why no lws Go module dependency.** Same as LeaderWorkerSet: pod
templates and integers, decoded into `corev1` types already in the
dependency graph.

## Process for adding a new external CRD

When a future phase adds a scraper for another project's CRD (llm-d,
KAITO, Seldon Core, etc.):

1. Pin the exact spec version in the scraper's `handledKinds` and in
   this document.
2. Vendor a minimal test-only CRD under `config/crd/external/` with
   the same do-not-apply-to-real-clusters warning at the top.
3. Document the field paths the scraper reads in this file, with the
   "upgrade obligations" template above.
4. Add envtest coverage that creates a real-shape CR via that minimal
   CRD and verifies extraction.

Do NOT vendor upstream CRDs verbatim. The minimal-subset approach
keeps test setup independent of the upstream project's CRD evolution.
