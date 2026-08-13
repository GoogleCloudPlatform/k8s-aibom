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
- **Graduation to `v1beta1` is planned**, with a documented conversion
  path, and will arrive in a MINOR release (both versions served) — the
  `v1alpha1` storage version is not removed within 1.x.

## Kubernetes version support

See `docs/compatibility.md` for the tested matrix and support policy.
