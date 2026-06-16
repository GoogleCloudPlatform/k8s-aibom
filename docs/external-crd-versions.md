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
