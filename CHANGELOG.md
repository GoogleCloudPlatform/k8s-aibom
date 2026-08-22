# Changelog

All notable changes to k8s-aibom are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[Semantic Versioning](https://semver.org/) (see `VERSIONING.md`).

## [Unreleased]

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
