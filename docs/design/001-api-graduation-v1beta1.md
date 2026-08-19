# Design 001: API graduation to `v1beta1`

Status: Proposed (2026-08-19). Implementation follows this note; ships as
its own release per [VERSIONING.md](../../VERSIONING.md) (API-contract
changes never share a release with feature work).

## Context

- NVIDIA/AICR's [ADR-019](https://github.com/NVIDIA/aicr/blob/main/docs/design/019-k8s-aibom-runtime-inventory.md)
  requires a **non-alpha storage API** for stock-recipe adoption
  (NVIDIA/aicr#2271). The `aibom.k8saibom.dev` group is currently
  `v1alpha1`-only.
- VERSIONING.md has promised since v1.0.0: `v1alpha1` is field-frozen
  through 1.x, and graduation arrives as a dual-served MINOR release
  without removing `v1alpha1` storage support mid-1.x.
- That freeze is what makes this graduation cheap: the `v1beta1` schema
  is **identical** to `v1alpha1`, so no conversion logic is required.

## Decision

Add `v1beta1` for both CRDs (`AIBOM`, `AIBOMControllerConfig`):

1. **Schema-identical** to `v1alpha1` — same fields, same semantics,
   same printer columns. No field additions ride along (out of scope by
   rule).
2. **Conversion strategy stays `None`** — permitted because the served
   versions are byte-identical. No conversion webhook, no cert
   machinery, no new failure modes.
3. **Both versions served**; **storage moves to `v1beta1`**;
   `v1alpha1` remains served for all of 1.x (existing clients,
   manifests, and GitOps pipelines keep working unchanged).
4. **The controller moves to the `v1beta1` types internally**
   (mechanical import migration; the scheme registers both versions).

## Migration of stored objects

Storage-version changes affect writes only; existing `v1alpha1`-stored
objects remain readable indefinitely under strategy `None`. Migration to
`v1beta1` storage is therefore about *rewrites*, and this system mostly
rewrites itself:

- **AIBOMs** receive status updates on every reconcile — the entire
  inventory rewrites to `v1beta1` storage organically within normal
  reconcile cycles after upgrade.
- **AIBOMControllerConfig** (a singleton) rewrites on its next update;
  the documented fallback is a no-op touch.
- **`status.storedVersions` cleanup**: after rewrites complete, the
  administrator (or a documented one-liner) removes `v1alpha1` from
  each CRD's `status.storedVersions`. This is the final state ADR-019's
  "non-alpha storage" plausibly requires; we document and support the
  full procedure regardless of where AICR's gate draws the line
  (**open question for AICR below**).

## Upgrade path (the part that needs coordination)

This is the project's first CRD-changing release, so the documented
CRD-upgrade step (README → Upgrades) becomes load-bearing for every
existing install:

```bash
helm show crds oci://ghcr.io/googlecloudplatform/charts/k8s-aibom --version <new> \
  | kubectl apply --server-side --field-manager=k8s-aibom-crds -f -
```

(Server-side apply is required — the CRDs exceed the client-side
annotation size limit.)

Deployer caveats for downstream distributions: Helm and Helmfile never
upgrade `crds/` CRDs; Flux defaults to `Skip` (see NVIDIA/aicr#2264);
Argo CD applies CRDs each sync. AICR's component upgrade documentation
already records this matrix; the graduation release is where it first
matters in practice.

## Open questions (for AICR)

1. Does ADR-019's "non-alpha storage API" gate require (a) storage
   version = `v1beta1` at release, or (b) additionally, migrated stored
   objects and `storedVersions` cleanup verified? We implement and
   document both either way; the answer determines what the
   qualification checks.
2. Should NVIDIA/aicr#2271's demo pin v1.2.0 under the explicit-preview
   label and re-pin after this release, or wait for it? (Upstream
   recommendation: pin now; this release follows within days, and
   requalification is automated.)

## Testing

- envtest suites run against **both served versions**; golden BOM output
  is unaffected (document shape has no API-version dependence).
- Release-candidate verification on a real GKE cluster includes the
  **upgrade path**: install v1.2.0 → apply new CRDs per the documented
  step → upgrade the release → verify both versions serve, stored
  objects rewrite to `v1beta1`, and existing AIBOMs/config survive with
  UIDs intact.

## Non-goals

- No field or semantic changes to either CRD.
- No `v1` — beta is the deliberate stop: `v1` graduation is a future
  decision with real-world soak behind it.
- No removal of `v1alpha1` anything within 1.x (serving removal is a
  2.0 decision).
- The signature-verifier work (roadmap "Next") does not ride along.

## Consequences

- AICR's stock-adoption API requirement is satisfied; requalification
  is triggered (CRDs and status contract are in the qualified set) and
  is automated on their side.
- Upstream maintains two API packages through 1.x; the field freeze
  makes the sync trivial and mechanical.
- Every downstream consumer gets the graduation without action
  (`v1alpha1` clients unaffected); consumers who want `v1beta1` adopt
  it at their own pace.
