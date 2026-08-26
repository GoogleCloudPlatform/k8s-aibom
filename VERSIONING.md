# Versioning

k8s-aibom follows [Semantic Versioning 2.0.0](https://semver.org/) with a
single version number flowing through every artifact of a release: the git
tag, the GitHub release, the controller binary (stamped at build time), the
container image tag and OCI labels, and the Helm chart `version` /
`appVersion`. A release is one coherent set; artifacts never carry
mismatched versions.

## What the numbers mean

- **MAJOR** — a breaking change to a public contract (see below).
- **MINOR** — new capability, new scrapers, new configuration surface;
  existing contracts unchanged.
- **PATCH** — bug and security fixes only.

Pre-release identifiers (`v1.1.0-rc.1`) are used to exercise the release
pipeline and for downstream qualification ahead of a final tag.

## Public contracts

The following are the project's public contracts, stable within a MAJOR
version:

1. **The AIBOM and AIBOMControllerConfig APIs** (`aibom.k8saibom.dev`),
   including status condition semantics.
2. **The emitted CycloneDX 1.6 ML-BOM document shape**, including the
   confidence model (declared / inferred / unresolved), evidence locators,
   and signature status semantics (unsigned / claimed / verified).
3. **The Helm chart values interface.**
4. **The namespace opt-in contract** (`aibom.k8saibom.dev/enabled=true`).

Anything under `internal/` is not a contract. Consumers must not import
internal packages; downstream integrations consume the CRDs, the BOM
documents, and the chart.

## The `v1alpha1` API group and the v1.x promise

The CRD API group is `v1alpha1`; the project version is 1.x. These
statements reconcile as follows:

- **Within 1.x, `v1alpha1` is field-frozen:** existing fields are not
  removed or repurposed; changes are additive only. Treat it as stable in
  practice despite the alpha marker.
- **Graduation to `v1beta1`** ships as a MINOR release: both versions
  served, schema-identical, storage on `v1beta1` (see
  docs/design/001-api-graduation-v1beta1.md).
- **Removal of `v1alpha1` is a separate, announced release with a
  documented migration step** — it does not happen within 1.x, and it
  gates on stored objects being rewritten to `v1beta1` and
  `storedVersions` cleanup (see docs/migration-v1beta1.md).

## Release cadence

Adopted 2026-08-26, after the v1.0.0→v1.4.0 qualification burst (five
releases in nine days, each pulled by downstream findings) made the
cost of a release visible: downstream distributions requalify every
tag as a coherent artifact set. Steady-state rules:

1. **MINOR releases ship on a monthly train, at most.** Features catch
   whichever train they are ready for; a missed train waits for the
   next one. Merging to `main` is not rate-limited — the train
   disciplines tags, not development.
2. **PATCH releases are exempt** for security fixes and defects that
   block a downstream qualification, with the downstream heads-up the
   release checklist already requires.
3. **New capability surface requires a published design doc with a
   stated review window before implementation begins**
   (docs/design/; Designs 001 and 002 are the precedent). Mechanical
   additions inside existing capability — new detection patterns, new
   fields on existing evidence — do not.
4. **Demand-gated backlog:** capabilities without a concrete consumer
   asking wait for a pull signal, however good the idea. The roadmap
   lists them; the train does not carry them.
5. API-contract changes never share a release with feature work
   (restating the Design 001 rule).

## Kubernetes version support

See `docs/compatibility.md` for the tested matrix and support policy.
