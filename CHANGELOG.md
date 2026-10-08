# Changelog

All notable changes to k8s-aibom are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[Semantic Versioning](https://semver.org/) (see `VERSIONING.md`).

## [Unreleased]

### Fixed

- **A third-party CRD that is present but unservable no longer takes
  the controller down or goes silent** (#127, Design 004). Dynamo's
  deployed CRDs (dynamo-platform ≤ 1.4) store `v1alpha1` behind a
  conversion webhook served by the Dynamo operator. Before: with that
  operator down at startup, controller-runtime's shared cache never
  synced, *every* reconciler failed its cache-sync wait and the process
  exited (a crash-loop of all of k8s-aibom, with a log blaming an
  unrelated kind); with the operator dying after startup, new Dynamo
  graphs were silently never inventoried. Now: third-party kinds
  (KServe, Dynamo, NIMService, LeaderWorkerSet) run under a supervisor
  on their own caches; a kind that cannot list, sync or convert is
  retried with capped backoff and reported — `Degraded=True` with
  reason `ThirdPartyWatchUnhealthy` on `AIBOMControllerConfig` naming
  the kind and the verbatim API error, one `WatchUnhealthy` Warning
  event per outage and one `WatchRecovered` on recovery,
  `aibom_watch_healthy{kind}` and `aibom_watch_errors_total{kind}`.
  Existing AIBOMs for the kind are kept; apps/v1 kinds and readiness
  are unaffected; recovery is automatic. Steady-state detection is a
  `Limit: 1` list per present kind every two minutes, because
  client-go's reflector swallows conversion errors on an established
  watch stream (measured). Found by the AICR maintainer's review of
  the v1.6 Dynamo scraper.

### Added

- **Ownership roll-up: one workload, one AIBOM** (#126, Design 005). A
  tracked workload owned — directly or transitively via controller
  `ownerReferences` — by another tracked kind no longer gets its own
  AIBOM; the owner's document is the report. Covered chains:
  `DynamoGraphDeployment` → `DynamoComponentDeployment` → `Deployment` |
  `LeaderWorkerSet`, and `DynamoGraphDeployment` → Grove `PodCliqueSet`
  → [`PodCliqueScalingGroup` →] `PodClique` (the PodCliqueSet is owned
  directly by the graph); `NIMService` →
  `Deployment` | `LeaderWorkerSet`; `LeaderWorkerSet` → `StatefulSet`;
  `CronJob` → `Job`. The owner's document lists what it absorbed as
  `aibom.rollup.owned.<i>` (`Kind/name`, sorted) and receives the
  descendants' pods, so **the CRD kinds now resolve pod-status digests**
  like a Deployment does through its ReplicaSets. Ownership only, never
  labels. An intermediate kind the controller cannot read (RBAC, CRD
  absent) ends the walk as unresolved and the child is reported as
  before — coverage never regresses because of a missing permission
  (`rollup_unresolved` outcome). Workloads with no controller owner pay
  nothing. On upgrade, child AIBOMs under the chains above are deleted
  on their next reconcile. New reconcile outcomes: `rolled_up`,
  `rollup_unresolved`.
- **`CronJob` coverage.** One AIBOM per CronJob from `spec.jobTemplate`
  (eval patterns as for Jobs); spawned Jobs roll up. RBAC adds
  `cronjobs` get/list/watch.
- **Standalone `DynamoComponentDeployment` roots.** A component with no
  graph parent is reported as its own root with locators rooted at
  `spec`; components owned by a graph are absorbed. RBAC adds
  `dynamocomponentdeployments` get/list/watch, and read-only get/list
  on the Grove pod-owning kinds (`podcliquesets`, `podcliques`,
  `podcliquescalinggroups`) to complete multi-node chains; absent CRDs
  or denied permissions degrade to today's behavior.
- **AMD ROCm vLLM runtime pattern** (#113). `rocm/vllm` images (tag,
  digest, and the `rocm/vllm/<build>` path form KubeAI ships as its
  AMD GPU default) attribute as runtime `vllm`. Anchored to AMD's
  `rocm/` publisher namespace at the image-name boundary, so
  `rocm/pytorch`, `rocm/vllm-dev` and other registries stay unmatched.

### Added

- **`LeaderWorkerSet` scraper** (Design 003 §2; v1.6 coverage release).
  One AIBOM per LWS, keyed to the LWS UID. Both the optional leader
  template and the required worker template go through the shared
  inference extraction with locators rooted at
  `spec.leaderWorkerTemplate.{leader,worker}Template`, an `lws.role`
  property on every extracted component, and `lws.size` /
  `lws.replicas` on container components. Nothing is declared by an
  LWS, so runtime attribution is image-pattern inferred as for a
  StatefulSet. Registered only when the CRD is present; RBAC adds
  get/list/watch on `leaderworkersets.leaderworkerset.x-k8s.io`.
  The StatefulSets an LWS materializes roll up in the §3 change.

- **NVIDIA NIM Operator `NIMService` scraper** (Design 003 §1; v1.6
  coverage release). One AIBOM per NIMService. The kind itself is a
  **declared** runtime (`nim`); `spec.image` becomes the container
  component (digest only when the repository is digest-pinned);
  `spec.env` / `spec.args` go through the existing model allowlists
  with `spec.env[i]` / `spec.args[i]` locators; the NIM image path
  `nvcr.io/nim/<org>/<name>` yields an **inferred** model identity
  **only when nothing is declared** — declared `NIM_MODEL_NAME` /
  `NIM_SERVED_MODEL_NAME`, args or annotations always win (the AICR
  case of a llama NIM image serving Qwen reports Qwen). Storage shape
  (`nimCache` / `pvc` / `hostPath` / `emptyDir`), multi-node topology
  and inference platform are recorded as properties. Registered only
  when the CRD is present; RBAC adds get/list/watch on
  `nimservices.apps.nvidia.com`.

- **NVIDIA Dynamo `DynamoGraphDeployment` scraper** (Design 003 §4;
  v1.6 coverage release). One AIBOM per graph, keyed to the DGD.
  `spec.backendFramework` becomes a **declared** runtime (`trtllm`
  recorded as `tensorrt-llm` to match the image-pattern name);
  `spec.components[i].modelRef` becomes a **declared** model attributed
  to its component; component `type` is recorded as graph topology;
  every component and role `podTemplate` goes through the shared
  inference extraction with locators rooted at the component path.
  No model identity is ever derived from Dynamo image paths (Dynamo
  images carry no model). Registered only when the CRD is present;
  RBAC adds get/list/watch on `dynamographdeployments.nvidia.com`.
  Pod-status digests and the DGD → component → pod ownership roll-up
  follow in the §3 change.

- **`kubectl aibom find`** — the incident command: filter tracked
  workloads by `--runtime` (exact), `--model` (case-sensitive
  substring), `--image` (substring of the container reference),
  `--digest` (bare hex or `sha256:`-prefixed; a prefix matches) and
  `--signed` (unsigned | claimed | verified); filters AND together.
  Zero matches prints `0 matches` and exits 0 so scripts can tell
  "none found" from "plugin broke". Rows whose document is in an
  external sink or truncated are shown — never excluded — under
  image/digest filters, with a stderr warning. Same client and RBAC
  as `summary`; no new CRD, no controller flag.

- **vLLM CPU release image runtime pattern** (#114).
  `public.ecr.aws/q9t5s3a7/vllm-cpu-release-repo` (the vLLM project's
  own public ECR alias; KubeAI's CPU default) attributes as runtime
  `vllm`. Anchored to that exact alias and repository, not a broad
  `public.ecr.aws/.*vllm` match.
- **Infinity embeddings server runtime pattern** (#111).
  `michaelf34/infinity` (tag and digest forms, including `0.0.77-cpu`
  and `0.0.77-rocm`) attributes as runtime `infinity`. The match is
  publisher-anchored at the image-name boundary, so
  `michaelf34/infinity-extra` and other registries stay unmatched.
- **faster-whisper-server speech-to-text runtime pattern** (#112).
  `fedirz/faster-whisper-server` (tag and digest forms, including
  KubeAI's `latest-cpu` and `latest-cuda`) attributes as runtime
  `faster-whisper`. The match is publisher-anchored at the image-name
  boundary, so `fedirz/faster-whisper-server-extra` and other
  registries stay unmatched.
- **Metrics are now scrapable, opt-in** (#106). The controller's
  Prometheus endpoint was registered but bound to loopback with no
  Service — unreachable by any scraper, which made the chart's
  "metrics" wording an overclaim and left the shipped
  `grafana/podmonitoring.yaml` targeting a pod port that did not
  exist. `metrics.enabled=true` now binds the endpoint on
  `metrics.port` (8080), names the container port `metrics`, and
  renders a ClusterIP Service; `metrics.serviceMonitor.enabled=true`
  additionally renders a Prometheus Operator ServiceMonitor. Off by
  default; nothing changes for existing installs.
- **`aibom_workload_reconcile_outcomes_total{kind,outcome}`** — the
  series that separates "opted-in namespace, nothing recognized" from
  a broken controller: `not_opted_in` (namespace selector did not
  match), `unmatched` (opted in, no inference signal — conservative
  detection declined), `matched` (an AIBOM is produced).

### Fixed

- **Served-schema skew is now detected and reported** (#104). If the
  cluster's `AIBOMControllerConfig` CRD is replaced by an older schema
  than the running controller (a GitOps sync pinned to an older
  revision, a `CreateReplace` rollback, a manual CRD re-apply), the API
  server silently prunes stored fields such as `spec.verification` —
  previously leaving signature verification OFF behind a fully green
  status. The controller now reads the served schema via the OpenAPI
  v3 endpoint (no new RBAC beyond what `system:discovery` already
  grants; stated explicitly in the ClusterRole) and compares it
  against every top-level spec field it was built with. Any missing
  field yields `Degraded=True` with reason `SchemaPredatesController`,
  a Warning Event naming the fields and the remedy, and a log line.
  The check is generic over the spec struct, so the next additive
  field cannot reintroduce the failure. Found by downstream
  adversarial upgrade testing (NVIDIA AICR qualification of v1.5.1).

- **An upgrade that sets `config.verification` against pre-1.5 CRDs now
  stops before anything is applied** (#105). Helm skips `crds/` on
  upgrade. On Helm 4 the config CR's server-side apply then failed with
  `.spec.verification: field not declared in schema` only after the
  Deployment had rolled, leaving the release `failed` and half-applied;
  on Helm 3 the upgrade succeeded and the API server pruned
  `spec.verification`, leaving verification off. When
  `config.verification` is set, the chart now reads the installed
  `AIBOMControllerConfig` CRD with `lookup` and, if its `v1beta1`
  schema lacks `spec.verification`, fails at render time with the
  CRD-apply command. Default values never perform the lookup, and
  `helm template` and client-side `--dry-run` are unaffected. With
  `config.verification` set, the Helm identity now needs `get` on that
  CRD; without it the render fails on `lookup` before anything is
  applied. Found by the same downstream qualification.

- **Tenant-controlled document growth is bounded.** Container
  component name/version are truncated on the same rule as every
  other authored string, and a per-document component cap (256)
  bounds pathological specs in memory and on the wire, not only in
  etcd. Truncation is recorded as `aibom.truncation.applied` —
  mirroring the redaction rule, never silent. Untruncated documents
  are byte-identical to before.

- **Transient sink failures now retry until the archive heals**
  (#91). Previously the BOM input hash was persisted even when a
  configured external sink failed, so the next reconcile took the
  dedup fast path and returned before re-emitting — a transient 403
  or network blip dropped that BOM from the archive until the
  workload spec changed. Now: the input hash is not persisted while
  any sink is failing (dedup unaffected on success), the reconcile
  requeues on a bounded cadence (1 minute) until delivery succeeds,
  and the four status/comment texts that promised a retry that never
  happened now describe the real behavior. After a partial failure,
  sinks that already succeeded are re-emitted on the retry; the
  default timestamped GCS path template makes that a duplicate
  archive object, never an overwrite.
- **The bootstrap-race deferral now requeues explicitly.** It
  previously waited for a status-update watch event that the
  Owns-watch's GenerationChangedPredicate filters out — an AIBOM
  whose first status write raced the cache could sit unpopulated
  until an unrelated event arrived, and the empty result could
  collapse a concurrently scheduled sink retry.

## [1.5.1] - UNRELEASED (security PATCH)

### Fixed

- **Pod attribution is now ownership-based, not selector-based**
  (Deployment, StatefulSet, DaemonSet, Job). Previously, two
  same-namespace workloads with overlapping selectors and a shared
  container name could cross-contaminate image digests in each
  other's BOMs, and a principal with pod-create permission in an
  opted-in namespace could plant a chosen digest in another
  workload's record. Digests now enter a BOM only from pods tied to
  the workload through the controller ownerReference chain
  (Deployment → ReplicaSet → Pod walked explicitly), with a
  belt-and-braces image-name match on the candidate's container
  status. Pods with no controller owner never contribute. Found by
  internal review; regression-tested end to end.
- **Webhook sinks reject credentials over cleartext at config load.**
  An `http://` endpoint combined with any `auth` configuration is now
  a load-time validation error (all-or-nothing fallback, named
  LoadError). Plain http without auth remains legal for in-cluster
  receivers; https with auth is unchanged.

## [1.5.0] - 2026-09-22

The trust release: the `verified` confidence tier the README has
promised since v1.0 — cryptographic verification of model signature
claims against configurable Sigstore trust roots with Rekor
transparency-log inclusion — designed in public (Design 002),
substantively amended twice by external review from the model-signing
community, and hardened so a claim can never upgrade itself. Also:
the output sanitization guarantee, the kubectl-aibom plugin, the
non-default-configuration e2e matrix, and the NIM model env vars.
With no signature annotations present, output is byte-identical to
v1.4.0.

### Added

- Chart: `extraVolumes` / `extraVolumeMounts` values — required to
  mount a static trusted-root file for
  `verification.trustRootMode=staticBundle` (air-gapped clusters).
- e2e: non-default-configuration matrix (#59) — strict-readiness
  break/recover, webhook sink with a bearer-token Secret under real
  RBAC, and signature verification with verified and tampered
  outcomes against a static trust root. Closes the test-debt class
  behind the one code defect external qualification found.

- NIM model-declaration env vars `NIM_MODEL_NAME` and
  `NIM_SERVED_MODEL_NAME` join the default model-identity allowlist.
  Reported by an AICR maintainer during Design 003 review: a NIM
  container's served model can differ from its image default, and
  without these names such workloads carried no declared model signal.

- kubectl-aibom: `summary` gains a SIGNED column (per-model signature
  states, deduplicated) and `verify` appends the recorded per-model
  signature facts to its integrity verdict — the verified tier is
  demonstrable in one command.
- Sigstore/Rekor signature verification — the `verified` confidence
  tier (Design 002, #56): `spec.verification` on
  `AIBOMControllerConfig` enables cryptographic verification of model
  signature claims (`model.k8saibom.dev/oms-signature`, optional
  `model.k8saibom.dev/digest`) against configurable trust roots
  (Sigstore public-good via TUF, self-hosted TUF mirror, or a static
  trusted-root file). `verified` requires chain validity, Rekor
  inclusion, a satisfied signer-identity constraint (a non-public
  trust root counts), and no contradicted declared binding —
  digest-over-name precedence. All outcomes are recorded facts on the
  model component (`signature.*` properties; `ModelSummary.Signed` now
  populates); verification failures never fail a reconcile, and with
  verification absent, output is byte-identical to v1.4.0. Retires
  schema-divergences entry D-001. Fixed alongside: the reserved
  signature annotations are no longer mis-extracted as phantom model
  identities.

- `kubectl-aibom` plugin (#58): `summary` (per-namespace or `-A`
  table of workload, category, runtime, models, confidence, Ready),
  `view` (decoded, pretty-printed BOM; `--raw` for the canonical
  bytes the published digest covers), and `verify` (recomputes
  sha256 against `status.bomDocument.sha256`, non-zero exit on
  mismatch — script- and CI-composable). Built via
  `make kubectl-plugin` or `go install .../cmd/kubectl-aibom@latest`;
  verified live against a v1.4.0 install.
- Output sanitization guarantee (#57): every string emitted into a BOM
  passes a redaction filter at the build boundary — URI userinfo,
  known credential query parameters (pre-signed URL signatures, SAS
  tokens), and well-known secret token shapes are replaced before
  emission, with an `aibom.redaction.applied` property recording the
  redaction class on any affected component or service. The audit
  behind it (issue #57) confirmed no default extraction path emits
  credential material; the filter guarantees the residual vectors
  (URI-shaped identity fields such as KServe `storageUri` and model
  annotations, and operator-extended allowlists). Clean documents are
  byte-identical to v1.4.0 output.

## [1.4.0] - 2026-08-25

The downstream-coverage release, cut the day after k8s-aibom began
shipping in NVIDIA AICR v0.20.0: detection patterns NVIDIA's catalog
needs, the chart CR template graduation deferred out of v1.3.0, and
the re-baselined performance record — bundled so downstream
distributions requalify once.

### Added

- Runtime image patterns for NVIDIA NIM (`nvcr.io/nim/*` → `nim`) and
  NVIDIA Dynamo backend workers (`nvcr.io/nvidia/ai-dynamo/{vllm,sglang,
  tensorrtllm}-runtime` → `vllm`/`sglang`/`tensorrt-llm`, nightly
  variants included). Dynamo infrastructure images (frontend, planner,
  operator) deliberately do not match.
- TGI's GHCR namespace (`ghcr.io/huggingface/text-generation-inference`
  → `tgi`) — previously a documented deferred false negative; real
  deployment signal arrived.

### Changed

- Performance documentation re-baselined on live-GKE measurements of
  v1.2.0 and v1.3.0 (1,001 workloads, dual-sampled): steady state is
  1–2m CPU / ~61Mi, statistically identical across both versions and
  consistent with NVIDIA/aicr#2310's independent measurement. Every
  published figure now carries version + environment + sampling
  method; v1.1.0-era Kind steady-state figures are superseded, with
  the ~370m convergence burst retained as the upper bound.
- The chart now renders the default `AIBOMControllerConfig` at
  `aibom.k8saibom.dev/v1beta1`, matching the CRD storage version
  (#49). No behavioral change: the schema is identical under dual
  serving, and `v1alpha1` manifests remain valid through 1.x. Tools
  asserting on the rendered CR's `apiVersion` should follow the
  guidance in docs/migration-v1beta1.md (assert the CRD storage
  version, not blanket apiVersion replacement).

## [1.3.0] - 2026-08-19

The graduation release: the `aibom.k8saibom.dev` APIs reach `v1beta1`
(Design 001), satisfying the non-alpha storage requirement for stock
AICR adoption (NVIDIA/aicr ADR-019). **Upgrading requires applying the
new CRDs** — see docs/migration-v1beta1.md; skipping the step stalls
the rollout loudly and safely, with the previous pod still serving.

### Fixed

- Readiness gating had a start-ordering race (since v1.1.0): the
  cache-sync check called `WaitForCacheSync` before the manager started,
  trivially passing against an empty informer set — a pod could report
  Ready before (or without) its informers syncing. A first fix (a
  manager Runnable) was defeated by the same class of race: controllers
  create their informers after plain runnables start. Readiness is now
  asserted per probe against the load-bearing informers themselves —
  each readyz evaluation asks the cache for the v1beta1 `AIBOM` and
  `AIBOMControllerConfig` informers and their sync state, failing while
  the API server cannot serve those versions (e.g. stranded CRDs). With
  this fix, upgrading to the graduation release without the required
  CRD apply stalls the rollout with the previous pod still serving.
  Both defeated implementations were caught by the v1.3.0
  release-candidates' stranded-CRD boundary tests on a real cluster.
- Chart CRD files (and generated manifests) keep their YAML document
  separators: without them, `helm show crds` concatenates the two CRDs
  into one invalid stream and `kubectl apply` silently applies only the
  first — breaking the documented CRD-upgrade command exactly when it
  matters. Found by the rc.3 boundary test's recovery step.


### Added

- `v1beta1` API for `AIBOM` and `AIBOMControllerConfig` — schema-identical
  to `v1alpha1` (conversion strategy remains `None`), served alongside
  `v1alpha1`, and the storage version from this release onward.
  `v1alpha1` remains served and field-frozen through 1.x; its removal
  will be a separate, announced release with a documented migration step.
  Design: docs/design/001-api-graduation-v1beta1.md. The controller
  operates on the `v1beta1` types internally; both versions remain
  registered and served. An integration test proves the dual-serving
  round-trip (write v1alpha1 → read v1beta1 and vice versa, same UID,
  identical fields).

## [1.2.0] - 2026-08-18

### Added

- Opt-in strict configuration readiness: `--strict-config-readiness`
  (chart value `readiness.strictConfig`) fails the readiness probe while
  the active `AIBOMControllerConfig` is invalid. Off by default — the
  controller deliberately stays Ready on last-known-good config so an
  operator typo cannot take down inventory; distributions requiring
  configuration-aware readiness (e.g. AICR) enable it via values. An
  absent CR (defaults-by-choice) is not treated as invalid.

## [1.1.0] - 2026-08-18

The qualification release: every blocking finding from NVIDIA/AICR's
ADR-019 Phase 1 qualification of v1.0.0 (gates 3 and 4), fixed with
tests, plus readiness hardening. Details in the sections below and the
qualification record on issue #8.

### Added

- Chart `config.*` values render verbatim into the default
  `AIBOMControllerConfig`: `discovery` (incl. namespace selector),
  `bomGeneration`, `sinks`, and `logging` are now normal public values —
  no template patching or post-install CR mutation needed.
- Chart default resources are set from measured footprint (requests
  50m/128Mi, memory limit 256Mi; no CPU limit by design — see
  docs/quality-baseline.md).

### Changed

- Readiness now gates on informer cache sync: `/readyz` fails until the
  controller can observe the cluster, and the chart wires liveness and
  readiness probes against the health endpoints (previously no probe
  consulted them).

### Fixed

- Scrape and BOM-build failures now flip `Ready=False` (with reason and
  message) on the workload's existing AIBOM, so failures are observable
  in status rather than only in logs; prior document/summary fields are
  preserved and the failure path never creates AIBOMs.
- Non-conflict status-persistence failures now emit the
  `aibom_status_persist_failures_total` metric and an
  `AIBOMStatusPersistFailed` warning Event.
- Truncation reason now distinguishes "no external sink is configured"
  from "configured sinks all failed this cycle" — the latter previously
  reported the former's message.

- Every reconcile now runs under a finite 60s deadline, bounding all
  Kubernetes API operations (previously unbounded; a stalled API request
  could consume the reconcile forever). The 30s per-sink deadline nests
  inside it.
- GCS writes are capped at 4 attempts (matching the webhook sink's
  bounded attempt count) within the existing 30s elapsed bound.
- docs/webhook-sink-protocol.md backoff schedule corrected to match the
  code (250ms/1s/3s, 4 total attempts).

- Sink credential Secrets are now read via a direct (uncached) API
  reader. Previously the first Secret read started a cluster-wide Secret
  informer, which the namespace-scoped Role correctly forbids — with
  sinks configured and `rbac.sinkSecretAccess=true`, config reload stalled
  (`Ready=True` stale at the prior observedGeneration) with repeated
  `secrets is forbidden` list errors. Found by AICR gate-3 qualification.
  The Role also narrows to `get` only.

## [1.0.0] - 2026-08-17

First tagged release. Every release publishes a coherent, verifiable
artifact set: a multi-arch image (linux/amd64, linux/arm64) on
ghcr.io/googlecloudplatform/k8s-aibom carrying Sigstore build-provenance
and CycloneDX SBOM attestations; a digest-pinned Helm chart on
oci://ghcr.io/googlecloudplatform/charts carrying a build-provenance
attestation (the chart has no separate SBOM attestation); and a
digest-pinned install.yaml release asset.

### Changed

- **AIBOMControllerConfig is now a regular Helm release resource** instead
  of a `pre-install` hook, so Helm owns install/upgrade/rollback/uninstall
  deterministically, and the invalid `namespace` on the cluster-scoped
  object is gone. **Migration for existing from-source installs:** the
  hook-created CR carries no Helm ownership metadata; before upgrading an
  existing release, delete it (`kubectl delete aibomcontrollerconfig
  default`) or annotate it for Helm adoption.
- **Secret access is now opt-in.** The namespace-scoped Role granting
  Secret reads (used only for sink credentials) is rendered only when
  `rbac.sinkSecretAccess=true`. With no sinks configured (the default), the
  controller holds no Secret permissions. Set the value if your
  AIBOMControllerConfig references credential Secrets.
- ClusterRole rules deduplicated; `aibomcontrollerconfigs` narrowed to
  read-only + status (matching the controller's kubebuilder markers).

### Added

- `image.digest` chart value: a digest takes precedence over the tag so
  releases can be pinned immutably without patching the chart.
- Controller version is stamped at build time via ldflags
  (`main.controllerVersion`); local builds report `dev`. The image carries
  OCI identity labels.
- Community health files: CODEOWNERS, issue forms, PR template.
- `VERSIONING.md`, this changelog, `docs/compatibility.md`,
  `docs/release-checklist.md`.
- Release pipeline: tag-triggered publishing of the signed multi-arch
  image (with provenance and CycloneDX SBOM attestations), the OCI chart
  (with a provenance attestation), and digest-pinned install.yaml; a
  dry-run job exercises the release path on every PR.

### Security

- Dependency updates cleared all critical/high Dependabot alerts
  (golang.org/x/net, golang.org/x/crypto, google.golang.org/grpc).
