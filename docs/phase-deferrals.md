# Phase deferrals

This document records things deliberately punted to specific later
phases so they are not lost between summaries. Each entry names the
phase that picks the work up and what data or scaffolding is already
in place.

## Phase 8 — GCS sink

- Sink plumbing through `internal/controller/status.go`'s `SinkResult`
  is exercised in tests today (`TestStatusBuilder_ExternalWhenOverThresholdAndSinkSucceeded`).
- BOM truncation fallback path is already operational — Phase 8 layers
  on top without touching the truncation path.

## Phase 12 — Tooling hygiene

- **Bump `controller-gen`.** Currently pinned at v0.16.5. Still
  generates clean CRDs against controller-runtime v0.24 and K8s v0.36
  upstream types, but a generation lag is accumulating. Bump to a
  matching `v0.17.x` or `v0.18.x` and regenerate CRDs in one PR.
- **Bump `envtest`.** Already bumped to `release-0.24` matching
  controller-runtime v0.24.0; revisit when controller-runtime moves.
  **Constraint: any envtest version bump MUST stay at K8s 1.25+** —
  the CRD immutability rule on `AIBOMSpec.WorkloadRef` uses CEL
  (`XValidation: self == oldSelf`), which K8s only supports for
  CRDs from 1.25 onward. Tests will silently pass on older API
  servers but the immutability would not be enforced.
- **Pin Go toolchain in go.mod.** Currently `go 1.26.3`; consider a
  `toolchain go1.26.3` directive for reproducibility.
- **Parallel test execution opportunity.** After Phase 10's
  KServe-CRD-loading addition, `internal/controller` envtest startup
  takes ~5s per test (vs. ~3s pre-KServe). Total runtime for the
  package is ~113s. Each test currently starts its own envtest
  instance (`startEnvTest(t)` / `startEnvTestWithSinks(t, ...)`);
  sharing a single envtest across multiple `t.Run` sub-tests would
  cut startup overhead at the cost of test isolation. Worth doing
  if controller-package runtime ever becomes a CI bottleneck;
  current numbers are tolerable.

## Phase 14 — Observability

The scraper's `BOMInputs.Errors []error` field is populated by scrapers
for non-fatal per-attribute extraction failures but is not currently
surfaced anywhere. Phase 14 uses this data for three telemetry surfaces:

- **Prometheus counter:** `aibom_scrape_attribute_errors_total{scraper,
  source}` — bucketed by scraper name and `EvidenceSource`. Lets
  operators see which workload kinds or evidence sources are producing
  extraction errors in their fleet.
- **Structured log entries:** one log line per error in `BOMInputs.Errors`
  at the controller's configured verbosity (info by default), including
  workload ref and evidence source. Makes individual failures debuggable
  without enabling verbose logs across the entire reconciler.
- **`Stale` condition trigger:** if the same workload produces extraction
  errors on N consecutive reconciles (N configurable in
  `AIBOMControllerConfig`), the AIBOM CR's `Stale` condition flips to
  True with a message naming the persistent failure. Makes "this BOM
  hasn't successfully scraped in a while" visible via `kubectl get
  aibom` without external monitoring.

No structural change to `BOMInputs.Errors` is needed; the current
`[]error` shape is the right input for all three surfaces.

Cross-cutting Prometheus metric for sink failures, layered by Phase 8/9
sink emission paths:

- **`aibom_external_sink_emit_failures_total{sink,namespace,kind}`** —
  label set is a CURATED triple: sink name, workload namespace, workload
  kind. **Workload name is intentionally excluded** from labels: a fleet
  with high workload churn (e.g., spot instances cycling deployments)
  would explode Prometheus cardinality. Operators wanting per-workload
  failure visibility should query the AIBOM CR's SinkFailed condition,
  which is keyed by workload identity in etcd, not in Prometheus.

The structured log path already includes the right fields
(`internal/controller/aibom_controller.go` `emitToExternalSinks`); only
the metric registration is missing.

Additional metric tied to `AIBOMControllerConfigReconciler` (Phase 12):

- **`aibom_controller_config_reloads_total{result}`** — counts successful
  reloads of the `AIBOMControllerConfig` CR. The `valid → valid` transition
  in the reconciler's state machine deliberately emits NO Kubernetes Event
  (avoiding helm-upgrade spam), so this metric is the surface for
  "controller successfully picked up a config change." `result` label
  values: `loaded` (valid), `invalid_using_defaults`, `invalid_using_lkg`,
  `recovered`. Operators wanting per-reload visibility scrape this; they
  do not get Events for it.

## Phase 9 — Parallel external sink fan-out

Phase 8 ships with sequential external-sink emission inside
`DeploymentReconciler.emitToExternalSinks`. With only the GCS sink in
v1.0, ordering is moot. Phase 9 introduces a second sink (webhook) and
the fan-out becomes a tail-latency concern.

Use a plain `sync.WaitGroup` (not `errgroup.Group.WithContext`) so
that one sink's failure does not cancel the others' contexts — sinks
fail independently per FR4.2. The per-sink bounded
`DefaultExternalSinkTimeout` already prevents any single sink from
holding up reconciliation; parallelism just means total latency is the
slowest sink, not the sum of all sinks.

The refactor is small and is done as part of Phase 9 itself.

## v1.1 polish — GCS sink startup-write probe

v1.0 lazily authenticates against GCS: `storage.NewClient` succeeds at
startup even if the credentials cannot reach the bucket, and the
failure surfaces only on first Emit (as a `SinkFailed` condition).
This is correct but degrades the first-run admin experience: the
admin sees their first AIBOM CR appear with a `SinkFailed` condition
rather than an obvious startup error.

v1.1 adds an optional startup-write probe that writes a tiny
`.startup-probe/{timestamp}.txt` object to the configured bucket
during sink construction. The probe uses only `storage.objectCreator`
(same IAM as normal writes; no admin permission needed). Customers
manage probe cleanup with a documented bucket lifecycle rule (delete
objects under `.startup-probe/` after 1 day). Misconfigured sinks
then fail loudly at startup rather than at first reconcile.

v1.0 behavior is documented in `docs/security-model.md` §2 and is
correct as-is; the startup probe is a UX polish improvement, not a
fix.

## Phase 15 — Launch announcement honesty about runtime coverage

When the public launch announcement's "supported runtimes" / "tested
runtimes" table is written, it MUST reflect registry-form specifics
honestly rather than generic "X supported" entries. Specifically:

- **TGI** is detected only in Docker Hub registry form
  (`^huggingface/text-generation-inference.*`). GHCR-form TGI is NOT
  detected in v1.0 — see [`scraper-heuristics.md`](scraper-heuristics.md)
  "Known false negatives" section.
- **TEI** is detected only in GHCR registry form
  (`^ghcr\.io/huggingface/text-embeddings-inference.*`).
- **vLLM** is detected via `^vllm/.*` and `.*ghcr\.io/.*/vllm.*`.
  Registry-mirrored vLLM (e.g., Artifact Registry, ECR private
  mirrors) is NOT detected. Workloads remain BOM-able with
  `Runtime: ConfidenceUnresolved`.
- Other runtimes (Triton, Ollama, llm-d, Ray Serve, SGLang, LMDeploy):
  each anchored to a specific upstream registry path; see
  [`internal/scraper/v1-runtime-patterns.yaml`](../internal/scraper/v1-runtime-patterns.yaml).

Customers reading a generic "X supported" announcement will spot the
asymmetries against their actual deployments and may feel misled.
Better to lead honest: "v1.0 detects these runtimes in these
registry forms; mirrored / repackaged forms produce honest
unresolved-confidence BOMs and are tracked as deferred coverage
expansion." Same framing as the existing
[`docs/scraper-heuristics.md`](scraper-heuristics.md) §"Known false
negatives."

## Phase 15 — Helm chart + install.yaml + Dockerfile UX

The Helm chart's controller Deployment MUST set `POD_NAME` and
`POD_NAMESPACE` via the downward API. The
`AIBOMControllerConfigReconciler` (Phase 12) emits Kubernetes Events for
the missing-CR and deleted-CR states with the controller's own Pod as
the involvedObject; without these env vars, those Events lose their
target and the customer-visible signal that "config CR is missing" is
silently broken. Standard pod-spec snippet:

```yaml
env:
  - name: POD_NAME
    valueFrom:
      fieldRef:
        fieldPath: metadata.name
  - name: POD_NAMESPACE
    valueFrom:
      fieldRef:
        fieldPath: metadata.namespace
```

The Phase 15 chart reuses the existing Dockerfile. Two UX issues
surfaced during the Phase 9.5 smoke test should land alongside the
chart:

- **Multi-arch builds as opt-in, not default.** The Dockerfile uses
  `ARG TARGETOS` / `ARG TARGETARCH` so `go build` cross-compiles when
  buildx supplies them. With plain `docker build`, both args default
  to empty strings and `go build` falls back to the host platform —
  correct, but the `FROM --platform=$BUILDPLATFORM ...` syntax we
  initially shipped did NOT degrade gracefully (it errored on plain
  `docker build`). The `--platform` was stripped from the FROM line
  during the smoke test. For Phase 15, settle on one of:
  - One Dockerfile (current state), plus a documented `make image`
    target for single-arch and `make image-multiarch` for buildx-
    driven multi-arch.
  - Two Dockerfiles: `Dockerfile` (single-arch default) and
    `Dockerfile.multiarch` (buildx-required).
  The first option is simpler and matches the current state.

- **Builder image vs. go.mod toolchain.** The Dockerfile is pinned at
  `golang:1.26-bookworm` (matching `go 1.26.3` in go.mod) after the
  smoke test discovered that `golang:1.24-bookworm` + GOTOOLCHAIN=auto
  works in most environments but breaks when CI sets
  GOTOOLCHAIN=local. Document the lockstep relationship in
  CONTRIBUTING.md when it exists; remind future bumps to update both
  in the same commit.

## Phase 2c.2 — Real signature verification (post-v1)

The `SignatureVerifier` interface and `NoopVerifier` implementation
exist in `internal/scraper/signature.go`. A future `RekorVerifier`
plugs in here without touching the scraper or builder. See
`docs/schema-divergences.md` D-001.
