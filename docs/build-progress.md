# Build progress ledger

The authoritative phase-by-phase state of the k8s-aibom build. Read
this first when resuming work in a new session or onboarding a
contributor.

Three companion docs cover dimensions this ledger refers to:
[`open-decisions.md`](open-decisions.md) (unresolved choices blocking
public release), [`phase-deferrals.md`](phase-deferrals.md) (deliberate
punts with phase owners), and [`prd-deviations.md`](prd-deviations.md)
(implementation vs. PRD text deltas).

## TL;DR status

**Phases complete:** 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 13.

**Phase 12 (tooling hygiene)** is still deferred — see
[`phase-deferrals.md`](phase-deferrals.md). It does not gate Phase 14
because the controller-gen / Go-toolchain bumps are independent of
observability work.

**Verification at last completion (post-Phase 13):**
- `go vet ./...` clean
- `go build ./...` clean
- `KUBEBUILDER_ASSETS=… go test -race -count=1 ./...` passes
- Controller package: ~184s of envtest runtime (up from ~130s
  pre-Phase 13). The shared-envtest optimization in
  `phase-deferrals.md` is now a real lever for CI; revisit during
  Phase 14 scope-planning per the user's lean ("treat envtest
  parallelization as a checkpoint within Phase 14 with its own
  design proposal and verification").

**Next:** Phase 14 (Observability — Prometheus metrics for sink
emission failures and scraper attribute errors, `Stale` condition
trigger, optional reload-count metric).

## Ledger gap: Phases 10 and 11

Phases 10 and 11 shipped but did NOT receive detailed ledger entries
in this file as they completed. They are summarized below for
continuity; future sessions resuming the build can verify the
on-disk state via the linked files.

- **Phase 10 — StatefulSet + KServe extension.** Added
  `StatefulSetReconciler`, `DaemonSetReconciler`,
  `KServeInferenceServiceReconciler` (using
  `unstructured.Unstructured` to avoid a kserve Go dep). Extracted
  `WorkloadReconciler` base for kind-neutral logic; per-kind
  reconcilers embed it.
- **Phase 11 — Input-hash dedup + bootstrap-race fix.** Added
  `Status.InputHash` field, fast-path that skips BOM rebuild +
  external sink emission when the hash is unchanged, and the
  "AIBOM exists but Status.LastReconciled is nil → defer to next
  reconcile" guard for the Phase 9.5 smoke-test-discovered race
  where the Owns-watch delivers Create event before the prior
  reconcile's Status().Update lands.

If a future contributor needs the full diff for these phases,
`git log` is authoritative. Backfilling formal ledger entries is
low-priority cleanup, not a blocker.

## Phase order — actual vs. PRD sequencing

The user deliberately reordered the original PRD phase sequence to
make external contracts derive from proven internal types. Actual
order shipped:

```
1 → 2 → 4 → 5 → 6 → 7 → 3 → 8 → 9 → (10 next)
```

The PRD's original 3-before-everything-else sequence would have meant
designing CRDs against unverified type assumptions; the actual order
let Phase 7's integration milestone validate every API-facing type
shape before Phase 3 locked it down with validation markers.

## Per-phase summary

### Phase 1 — Scaffolding

Standard kubebuilder layout: `cmd/manager/`, `api/v1alpha1/`,
`internal/{controller,scraper,sink,bom}/`, `config/`, `hack/`,
`charts/k8s-aibom/`, `docs/`. Apache 2.0 license header on every Go
file from commit 1. Go module path: bare `k8s-aibom` (local-only;
search-and-replace at handoff per OD-001).

Key files: [`go.mod`](../go.mod), [`Makefile`](../Makefile),
[`hack/boilerplate.go.txt`](../hack/boilerplate.go.txt),
[`PROJECT`](../PROJECT).

### Phase 2 — Project metadata + LICENSE

[`LICENSE`](../LICENSE), [`README.md`](../README.md),
[`.gitignore`](../.gitignore), [`docs/threat-model.md`](threat-model.md)
skeleton, [`docs/schema-divergences.md`](schema-divergences.md)
skeleton.

### Phase 4 — Internal types and interfaces

Order swapped with Phase 3 (deliberate). Established the boundary
between internal Go types and API-facing types.

- [`internal/scraper/types.go`](../internal/scraper/types.go) —
  `WorkloadKind`, `WorkloadCategory`, `Workload`, `Evidence`,
  `EvidenceSource` (closed enum, 13 constants), `Confidence`,
  `Component`, `Service`, `Provenance`, `BOMInputs`.
- [`internal/scraper/scraper.go`](../internal/scraper/scraper.go) —
  `Scraper` interface.
- [`internal/scraper/signature.go`](../internal/scraper/signature.go) —
  `SignatureStatus`, `SignatureClaim`, `SignatureResult`,
  `SignatureVerifier` interface, `NoopVerifier` (v1 stub).
- [`internal/bom/document.go`](../internal/bom/document.go) — placeholder
  `Document` (extended in Phase 6).
- [`internal/sink/sink.go`](../internal/sink/sink.go) — `Sink`
  interface (signature evolved in Phase 8 to return `(url, error)`).
- [`internal/sink/types.go`](../internal/sink/types.go) — `SinkMeta`.

Tests: enum-stability tests, NoopVerifier "never returns Verified"
property test, JSON round-trip + tag-presence reflection tests for the
six types likely to mirror into external API surfaces.

### Phase 5 — InferenceSpecScraper for Deployment

The lighthouse v1 scraper, plus the embedded YAML allowlist that
drives extraction.

- [`internal/scraper/inference.go`](../internal/scraper/inference.go) —
  scraper struct, Scrape orchestration, deterministic
  `sortComponents`, `aggregateConfidence` (Unresolved excluded;
  lowest tier among the rest wins).
- [`internal/scraper/inference_extract.go`](../internal/scraper/inference_extract.go) —
  `parseImageRef`, `parseImageDigest` with strict sha256 format
  validation, `resolveContainerDigest` (A1-compliant: spec digest →
  pod-status digest → unresolved), per-extraction helpers for image,
  env vars, args, volume mounts, annotations, runtime detection.
  `looksSemverTag` plus runtime version's "semver-shaped tag or
  empty" rule. `validSHA256Digest` regex + `cleanSHA256Digest`
  defensive helper.
- [`internal/scraper/inference_config.go`](../internal/scraper/inference_config.go) —
  `InferenceConfig` loaded from embedded YAML.
- [`internal/scraper/v1-runtime-patterns.yaml`](../internal/scraper/v1-runtime-patterns.yaml) —
  the audit-reviewable allowlist of image patterns, env var names,
  arg flags, volume path prefixes.
- [`docs/scraper-heuristics.md`](scraper-heuristics.md) — full
  documentation of every extraction rule.

Includes a tested "init container args/env are NOT scraped" boundary
(per the "honest, not clever" preference).

### Phase 6 — BOM builder + schema validation

CycloneDX 1.6 ML-BOM output, with the schema-validation test that
caught a real fixture bug on its first run.

- [`internal/bom/builder.go`](../internal/bom/builder.go) — `Builder`,
  `Build(*BOMInputs, BuildOptions) (*Document, error)`, type mappings,
  deterministic component ordering, `cdxHashAlgFor` fail-fast on
  unknown algorithms.
- [`internal/bom/document.go`](../internal/bom/document.go) — extended
  with `CDX *cdx.BOM` field; `Format`/`Version`/`JSON`/`SHA256`/`Size()`.
- [`internal/bom/testdata/cyclonedx-1.6/`](../internal/bom/testdata/cyclonedx-1.6/) —
  vendored CycloneDX 1.6 schemas pinned at upstream tag `1.6.1`
  (sha256s in the testdata README).
- [`internal/bom/schema_validation_test.go`](../internal/bom/schema_validation_test.go) —
  loads vendored schemas; `assertValidCycloneDX` helper.
- [`internal/bom/builder_test.go`](../internal/bom/builder_test.go) —
  every-internal-component-type validates, byte-deterministic across
  invocations, evidence + confidence on every component.
- [`internal/bom/golden_test.go`](../internal/bom/golden_test.go) —
  vLLM fixture golden test with `-update-golden` flag and
  `make update-golden` target.
- [`internal/bom/testdata/golden/`](../internal/bom/testdata/golden/) —
  JSON input fixture + indented BOM golden output.

### Phase 7 — First integration milestone

End-to-end: Deployment in labeled namespace → AIBOM CR with populated
status.

- [`api/v1alpha1/aibom_types.go`](../api/v1alpha1/aibom_types.go) —
  full `AIBOMSpec`, `AIBOMStatus`, `WorkloadRef`, `AIBOMSummary`,
  `WorkloadSummary`, `RuntimeSummary`, `ModelSummary`,
  `BOMDocumentRef`, `InlineBOM`, `ExternalBOMRef`.
- [`api/v1alpha1/aibom_conditions.go`](../api/v1alpha1/aibom_conditions.go) —
  `Ready`/`Synced`/`SinkFailed`/`Stale` + 7 standard reasons.
- [`internal/controller/summary.go`](../internal/controller/summary.go) —
  BOM-to-summary translation.
- [`internal/controller/status.go`](../internal/controller/status.go) —
  `StatusBuilder` with inline / external / truncated logic.
- [`internal/controller/aibom_controller.go`](../internal/controller/aibom_controller.go) —
  `DeploymentReconciler` with namespace-opt-in check, pod listing,
  CreateOrUpdate + Status().Update flow.
- [`internal/controller/suite_test.go`](../internal/controller/suite_test.go) —
  envtest harness with `SkipNameValidation` for multi-test setups.
- [`internal/controller/aibom_controller_test.go`](../internal/controller/aibom_controller_test.go) —
  3 envtest integration tests.

Wired into [`cmd/manager/main.go`](../cmd/manager/main.go).

### Phase 3 — CRD validation markers (after Phase 7)

Done after Phase 7 so the API design was informed by working
integration code rather than theoretical types.

- `+kubebuilder:validation:Required` / `Enum=CycloneDX` /
  `Pattern=^\d+\.\d+$` / `MinLength` / `MaxLength` on Spec fields.
- `+kubebuilder:validation:XValidation: rule="self == oldSelf"` on
  `workloadRef` for immutability.
- 6 `+kubebuilder:printcolumn` markers on AIBOM for useful
  `kubectl get` output.
- `default` singleton convention documented for `AIBOMControllerConfig`.
- 5 envtest validation tests in
  [`internal/controller/validation_test.go`](../internal/controller/validation_test.go):
  rejection paths for missing-name, unknown-format, bad-pattern,
  workloadRef-mutation, plus accept-valid counter-test.

Deliberately NOT added: `MaxItems` on status arrays, defaults,
admission webhook enforcement (post-v1.0).

### Phase 8 — GCS sink

Sole-writer security model, immutability via `DoesNotExist`
precondition, ADC / Workload Identity Federation / key-file auth
paths, env-var-based config.

- [`internal/sink/gcs.go`](../internal/sink/gcs.go) — `GCSSink`,
  `DefaultGCSPathTemplate`, `renderGCSPath`.
- [`internal/sink/gcs_test.go`](../internal/sink/gcs_test.go) — unit
  tests (templating, validation, no-colons invariant, UTC pinning).
- [`internal/sink/gcs_integration_test.go`](../internal/sink/gcs_integration_test.go) —
  `//go:build integration` real-bucket tests (happy path,
  bucket-not-found with credential-redaction check, immutability
  precondition).
- [`docs/security-model.md`](security-model.md) — auditor-grade IAM
  + immutability documentation.
- [`internal/controller/aibom_controller_sinks_test.go`](../internal/controller/aibom_controller_sinks_test.go) —
  envtest test with `recordingSink`: failure path verifies CRD
  status still updated, BOM still inline, `SinkFailed=True`
  condition names the failing sink and underlying error.

`Sink.Emit` signature changed in this phase from `error` to
`(url, error)` so the URL flows cleanly into `BOMDocumentRef.External.URL`.

### Phase 13 — `AIBOMControllerConfig` (CRD-driven runtime configuration)

The customer-facing configuration surface. Replaces the v1.0-interim
environment-variable sink wiring with a singleton cluster-scoped CR.
Hot-reloadable; structurally enforces the load-once invariant
("config seen mid-reconcile is the config seen at reconcile entry").

Delivered in 7 checkpoints (1 → 6 plus a Checkpoint 7 documentation
pass). The design decisions surfaced in the prose below are the ones
flagged as load-bearing during review.

**Checkpoint 1 — API surface.**
- [`api/v1alpha1/aibomcontrollerconfig_types.go`](../api/v1alpha1/aibomcontrollerconfig_types.go) —
  full `AIBOMControllerConfigSpec`: `DiscoveryConfig`,
  `BOMGenerationConfig`, `SinkConfig` (discriminated `Type=GCS|Webhook`),
  `WebhookAuth` (bearer or mTLS), `LoggingConfig`. Singleton-by-
  convention (name == `default`); other-named CRs silently ignored.
- [`api/v1alpha1/aibomcontrollerconfig_conditions.go`](../api/v1alpha1/aibomcontrollerconfig_conditions.go) —
  semantically distinct `Ready` and `Degraded` conditions; 6 reasons
  (`ConfigLoaded`, `ConfigInvalid`, `SinkConstructionFailed`,
  `SecretNotFound`, `RunningOnDefaults`, `RunningOnLastKnownGood`).
- Generated CRD: [`config/crd/bases/aibom.k8saibom.dev_aibomcontrollerconfigs.yaml`](../config/crd/bases/aibom.k8saibom.dev_aibomcontrollerconfigs.yaml).

**Checkpoint 2 — Loader + Snapshot + Store.**
- [`internal/config/snapshot.go`](../internal/config/snapshot.go) —
  immutable `Snapshot`; `Store` with `atomic.Pointer[Snapshot]`;
  `SourceConfigCR` / `SourceCompiledDefaults` / `SourceLastKnownGood`
  enum; `MarkAsLastKnownGood` shallow-copy helper.
- [`internal/config/loader.go`](../internal/config/loader.go) —
  three-way contract: missing CR → defaults+no errors;
  invalid CR → defaults+errors; valid CR → from-spec+no errors;
  API error → Go error (caller retries). All-or-nothing on invalid
  (no partial apply).
- [`internal/config/errors.go`](../internal/config/errors.go) —
  `LoadError`, `LoadResult`, `AggregateMessage`; centralized error
  constructors enforce auditor-precision message shape.
- [`internal/config/defaults.go`](../internal/config/defaults.go),
  [`internal/config/factory.go`](../internal/config/factory.go) —
  compiled defaults, `SinkFactory` interface, `NoopSinkFactory`
  test double.
- Loader tests assert **fallback-FIRST**: the discriminator-mismatch
  test (`TestLoad_InvalidCR_SinkTypeWebhookButBodyNil`) locks the
  message-specificity contract via substring assertions — generic
  "invalid config" messages fail the test.

**Checkpoint 3 — Production `ClientSinkFactory`.**
- [`internal/config/client_factory.go`](../internal/config/client_factory.go) —
  reads Secrets via the controller's namespace (sole-writer
  security model; cross-namespace Secret references are
  structurally impossible because `SecretKeyRef` carries only
  `name` + `key`).
- Auditor-precision error messages locked by 9 substring-assertion
  tests in
  [`internal/config/client_factory_test.go`](../internal/config/client_factory_test.go).
  Each failure mode (Secret not found, key not in Secret, key
  empty, both bearer+mTLS set, mTLS cert missing, GCS creds
  missing, cross-namespace invisibility) names the failing sink,
  the field path, the actionable fix; "Available keys: [...]" with
  sorted determinism guards typo diagnosis.

**Checkpoint 4 — `AIBOMControllerConfigReconciler` + state machine.**
- [`internal/controller/aibomcontrollerconfig_reconciler.go`](../internal/controller/aibomcontrollerconfig_reconciler.go) —
  six-state machine: `Unknown` → `Missing` / `Valid` / `Invalid`
  with full transition matrix; events fire only on state
  transitions (anti-spam structurally, not via
  EventBroadcaster aggregation).
- Pod-targeted events for the no-CR states
  (`AIBOMControllerConfigMissing`, `AIBOMControllerConfigDeleted`)
  use `ControllerPod` `ObjectReference` from downward API.
- **`MaxConcurrentReconciles=1` locked explicitly** in
  `SetupWithManager` with a `DO NOT raise this` comment — the
  `lastObserved` field's correctness depends on serialized
  observations.
- Predicate filtering: name == `DefaultConfigName` AND generation
  changed (status-only updates do not trigger reconciles).
- `TestReconcile_ValidToInvalid_PreservesLastKnownGood` is the
  senior-quality test: asserts at the snapshot-content level
  (not just the `Source` label) that a typo does not silently
  swap customer sinks.

**Checkpoint 5 (combined with formerly-6 main.go wiring) — Scraper
hot-reload via stateless-per-call + WorkloadReconciler refactor.**
- Scraper interface signature: `Scrape(ctx, w, *InferenceConfig)`.
  The load-once invariant is now a property of the type system,
  not contributor discipline — there is no scraper-side config
  pointer to swap mid-call.
- [`internal/scraper/inference.go`](../internal/scraper/inference.go),
  [`internal/scraper/inference_extract.go`](../internal/scraper/inference_extract.go) —
  `cfg` field dropped; threaded as parameter through every
  extraction helper. `NewInferenceSpecScraper(verifier)` is now
  one-arg.
- [`internal/scraper/kserve.go`](../internal/scraper/kserve.go) —
  accepts cfg, ignores it (premise inversion: KServe extracts
  declared values, doesn't pattern-match; uniform interface for
  forward-compat).
- [`internal/controller/workload_reconciler.go`](../internal/controller/workload_reconciler.go) —
  `snap := r.ConfigStore.Load()` at top of `reconcileWorkload`;
  `snap.Patterns` → Scrape; `snap.ExternalSinks` →
  `emitToExternalSinks`; `snap.InlineThreshold` → BuildStatus;
  `snap.NamespaceSelector` → namespace opt-in check (replacing
  hardcoded `OptInLabel == "true"`).
- [`internal/controller/status.go`](../internal/controller/status.go) —
  `BuildStatus` gains `inlineThresholdBytes int64` parameter; the
  field on `StatusBuilder` is removed. Same load-once-per-reconcile
  principle.
- [`cmd/manager/main.go`](../cmd/manager/main.go) — env-var sink
  construction removed; `ConfigStore` + `Loader` +
  `AIBOMControllerConfigReconciler` wired; downward-API Pod
  resolution; once-per-Pod-startup `ControllerStarting` Event
  including controller version.

**Checkpoint 6 — Integration tests for the customer-facing chain.**
- [`internal/controller/combined_test_harness_test.go`](../internal/controller/combined_test_harness_test.go) —
  `startCombinedEnvTest` wires both the config reconciler AND the
  workload-reconciler family through a shared ConfigStore.
- [`internal/controller/config_hotreload_test.go`](../internal/controller/config_hotreload_test.go) —
  end-to-end hot-reload test: CR edit swaps webhook A → webhook B;
  workload touch (via `model.k8saibom.dev/touch` annotation that's
  scraped, defeating input-hash dedup); BOM lands at B, A receives
  zero additional POSTs.
- [`internal/controller/config_bootstrap_test.go`](../internal/controller/config_bootstrap_test.go) —
  5 tests covering the Checkpoint-4 state matrix at integration
  level. `TestIntegration_Bootstrap_ValidToInvalid_PreservesLKG`
  is the customer-protection property end-to-end: BOMs continue
  reaching the LKG sink after a CR typo.
- [`internal/controller/aibom_controller_sinks_test.go`](../internal/controller/aibom_controller_sinks_test.go) —
  appended `TestIntegration_ExternalSinks_FailureIsolation_OneFailsOthersSucceed`:
  one sink fails, the other still receives the BOM; SinkFailed
  message names ONLY the failing sink.

**Checkpoint 7 — Documentation pass (this entry, README, close-gate
verification).** No code changes beyond doc updates.

### Phase 9 — Webhook sink + parallel fan-out

Customer-facing wire protocol with retry, idempotency header,
mTLS / bearer-token / no-auth modes.

- [`internal/sink/webhook.go`](../internal/sink/webhook.go) —
  `WebhookSink`, retry on 5xx (1s/2s/4s, 3 retries), no retry on 4xx,
  `X-Aibom-*` headers, `Authorization` redaction in error messages,
  256-byte body truncation in error messages, TLS config supporting
  bearer / mTLS / InsecureSkipVerify (loud warning).
- [`internal/sink/webhook_test.go`](../internal/sink/webhook_test.go) —
  12 httptest-driven tests: all-headers happy path, bearer token
  present, bearer token absent, empty-token-file fails construction,
  5xx retry, 4xx no-retry, redaction (no leaked token, body
  truncated), context-cancel honors cancellation, concurrent emits
  race-free.
- [`docs/webhook-sink-protocol.md`](webhook-sink-protocol.md) —
  customer-facing wire-protocol spec with stability commitments.
- Reconciler's `emitToExternalSinks` refactored to parallel
  `sync.WaitGroup` (not `errgroup.Group.WithContext` — sinks fail
  independently per FR4.2). Result ordering preserved.

## What sits at the edges

These items are tracked but **not** complete in the build proper:

### Open decisions (blocking public release)

See [`open-decisions.md`](open-decisions.md):

- **OD-001 — API group / domain.** The `aibom.k8saibom.dev` placeholder
  requires the `api-approved.kubernetes.io: "unapproved, experimental-only"`
  annotation. Options: `aibom.openssf.org`, `aibom.dev`, KEP-blessed
  `aibom.k8saibom.dev`. **Resolve before Phase 3-style work would rev the
  CRDs again** (i.e., before any v1.0 cut).

### Deferred (cross-phase)

See [`phase-deferrals.md`](phase-deferrals.md). Highlights:

- **Phase 12** — Bump `controller-gen` (currently v0.16.5) and Go
  toolchain pin. Constraint: any future envtest bump MUST stay at
  K8s 1.25+ for CEL rule support.
- **Phase 14** — Wire `BOMInputs.Errors` into Prometheus +
  structured logs + `Stale` condition. Cardinality guidance for
  sink-failure metrics: curated labels `(sink, namespace, kind)` —
  NOT workload name.
- **Phase 2c.2** — Replace `NoopVerifier` with a real Rekor-based
  signature verifier.
- **v1.1 polish** — GCS startup-write probe so misconfigured sinks
  fail at startup rather than first reconcile.

### PRD deviations

See [`prd-deviations.md`](prd-deviations.md): currently P-001 only
(`Workload.Spec map[string]any` → `Workload.Object client.Object`).

### Schema divergences

See [`schema-divergences.md`](schema-divergences.md): D-001 (signature
confidence tiering), D-002 (per-attribute evidence sourcing), D-003
(image-digest unresolved state), D-004 (properties-based Evidence
encoding with additive v2 migration plan).

## How to resume

If you're picking up this repository, in order:

1. Read this file end-to-end.
2. Skim [`open-decisions.md`](open-decisions.md) — anything blocking?
3. Skim [`phase-deferrals.md`](phase-deferrals.md) — anything assigned
   to your current phase?
4. Skim [`scraper-heuristics.md`](scraper-heuristics.md) and
   [`security-model.md`](security-model.md) — the design preferences
   that must carry through every new phase.
5. Run `KUBEBUILDER_ASSETS="$(./bin/setup-envtest use 1.34.0 -p path)" go test -race -cover -count=1 ./...`
   to confirm the baseline is green before changing anything.

The in-repo docs listed above are the authoritative source of truth
for design context, deferred work, and unresolved decisions. New
contributors should not need any state external to this repository to
become productive.
