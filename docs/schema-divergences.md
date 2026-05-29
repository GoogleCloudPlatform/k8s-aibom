# Schema divergences

This document tracks every place where the k8s-aibom BOM output deviates from,
or extends, the CycloneDX 1.6 ML-BOM specification or established conventions
(notably the Trusera/ai-bom project's choices). Each divergence has a
rationale, an example, and a link to the upstream issue or discussion where
applicable. Per PRD §9.4, divergences must be documented so design partners
and CycloneDX maintainers can audit them.

## Status

Phase 1 scaffold placeholder. Concrete divergence entries land as the scraper
and BOM-builder implementations progress.

## Format for divergence entries

Each divergence entry should follow this template:

```
### D-NNN: <short title>

**Status:** active | rescinded | upstreamed
**Introduced in:** <version or commit>
**Spec section:** <CycloneDX section reference>
**Trusera comparison:** <same / different / not covered>

**What we do**

<concrete description of the encoding the controller emits>

**What the spec says (or doesn't)**

<quote or paraphrase the spec; note where it's silent>

**Why we diverge**

<the rationale — usually customer/auditor requirement or spec ambiguity>

**Upstream tracking**

<link to CycloneDX issue, RFC, or maintainer thread, if any>

**Example BOM fragment**

\`\`\`json
{ ...minimal illustrative excerpt... }
\`\`\`
```

## Planned divergence entries (placeholders)

The following divergences are already decided in design and will be filled in
as the corresponding code lands.

- **D-001: Signature confidence tiering.** v1 reports OMS signatures as one of
  `unsigned` / `claimed` / `verified`, encoded via CycloneDX
  `evidence.identity.methods[].confidence`. v1 will never emit `verified`
  because v1 does not perform Rekor lookup or artifact verification. Auditors
  reading a v1 BOM can trust `claimed` to mean "we saw an annotation or
  attestation reference but did not validate it cryptographically." See PRD
  FR2.5 and the `internal/scraper` SignatureVerifier interface (with
  `NoopVerifier` v1 implementation; `RekorVerifier` is the v2 plug-in point).

- **D-002: Per-attribute evidence sourcing.** Every BOM attribute carries
  metadata identifying the specific source it was extracted from
  (`pod_annotation`, `container_arg`, `env_var`, `image_pattern`, `crd_field`,
  `volume_source`, `node_selector`, etc.). This is in addition to (not a
  replacement for) workload-level `declared` / `inferred` confidence. The
  mapping into CycloneDX uses `evidence.identity.methods[]` for identity-bearing
  attributes and `properties[]` with name `aibom.evidence.source` for others.
  See PRD §9.4 and FR1.5.

- **D-003: Image digest unresolved state.** When no running pod has yet reported
  an `imageID` (workload just created, pods in ImagePullBackOff), the BOM emits
  the image component with `confidence: unresolved` rather than fabricating a
  digest or omitting the field. This is an explicit "not yet known" state
  distinct from "known."

### D-004: Evidence and confidence encoded via `properties[]` (v1) — native CycloneDX evidence in v2

**Status:** active
**Introduced in:** Phase 6 (BOM builder)
**Spec section:** §5.6 (Evidence), §5.5 (Component.Evidence), §6.3 (Properties)
**Trusera comparison:** similar — Trusera also uses properties extensively, though it does not differentiate evidence-source granularity to the degree we do

**What we do**

Every Component the builder emits carries three properties identifying
its evidence and confidence:

- `aibom.confidence` — one of `declared`, `inferred`, `unresolved`
- `aibom.evidence.source` — one of the closed `EvidenceSource` constants
  (`pod_annotation`, `env_var`, `image_reference`, etc.)
- `aibom.evidence.locator` — a human-readable JSON-path-style pointer to
  the manifest field the value was extracted from

The CycloneDX `Component.Evidence` field (a structured
`Evidence.Identity.Methods[]` block) is NOT populated in v1. Workload-level
aggregate confidence is exposed as `aibom.confidence` in
`metadata.properties[]`.

**What the spec says**

CycloneDX 1.6 §5.5 defines `Component.Evidence` as a structured object
with `identity`, `occurrences`, `callstack`, `licenses`, and `copyright`
sub-fields. `Evidence.Identity` is a list of identification methods, each
with a `confidence` value from 0.0 to 1.0 and a `technique` enum
(`source-code-analysis`, `binary-analysis`, `attestation`, etc.).

The spec is silent on AI-specific evidence semantics; `attestation` is the
closest fit for our claimed-vs-verified signature work (D-001) but does
not encode "this value came from an env var named HF_MODEL_ID" cleanly.

**Why we diverge**

v1 ships a flat, auditor-readable property-based encoding because:

1. **Time-to-correct-output.** Properties are unambiguously schema-valid
   for any name/value pair. The structured `Evidence.Identity` mapping
   requires careful per-attribute decisions about `technique` and
   `confidence` mapping that would block v1 schedule on debate.
2. **Auditor accessibility.** A flat `aibom.evidence.source = env_var`
   property is grep-able in a BOM JSON. A nested
   `evidence.identity[0].methods[0].confidence = 0.7` is harder to read
   and the float-confidence interpretation is unclear.
3. **Library agnosticism.** Property encoding doesn't depend on
   cyclonedx-go correctly modeling all of `Evidence`. Properties survive
   any future library swap.

**Planned v2 refinement (additive, non-breaking)**

v2 will ADD native CycloneDX structured evidence to every Component that
carries a property-based one today. Specifically:

- Populate `Component.Evidence.Identity.Methods[]` with `technique` mapped
  from `EvidenceSource` and `confidence` mapped from our string tier
  (`declared` → 0.9, `inferred` → 0.5).
- Map signature claims (D-001) into `technique=attestation`.
- Keep the existing `aibom.evidence.*` and `aibom.confidence` properties
  for one v2 release so consumers depending on the property-based shape
  have time to migrate. Dual-emit during the transition.
- Document the property-form deprecation in the release notes; remove in
  v3 if no consumers are still using it.

This migration is intentionally additive: v1 BOMs remain valid v2 BOMs.
No customer or auditor breakage at the v2 cut.

**Upstream tracking**

No upstream issue yet. Open one when v2 implementation begins; the
property-naming conventions we're adopting are candidates for the
proposed "Kubernetes runtime ML-BOM profile" the project is moving toward
(see PRD §9.4).

**Example BOM fragment (v1)**

```json
{
  "type": "machine-learning-model",
  "name": "meta-llama/Llama-3.1-8B-Instruct",
  "properties": [
    {"name": "aibom.confidence", "value": "declared"},
    {"name": "aibom.evidence.source", "value": "container_arg"},
    {"name": "aibom.evidence.locator", "value": "spec.template.spec.containers[0].args[0 1](--model)"}
  ]
}
```

**Example BOM fragment (v2, additive)**

```json
{
  "type": "machine-learning-model",
  "name": "meta-llama/Llama-3.1-8B-Instruct",
  "properties": [
    {"name": "aibom.confidence", "value": "declared"},
    {"name": "aibom.evidence.source", "value": "container_arg"},
    {"name": "aibom.evidence.locator", "value": "spec.template.spec.containers[0].args[0 1](--model)"}
  ],
  "evidence": {
    "identity": [{
      "field": "name",
      "concludedValue": "meta-llama/Llama-3.1-8B-Instruct",
      "methods": [{
        "technique": "manifest-analysis",
        "confidence": 0.9,
        "value": "spec.template.spec.containers[0].args[0 1](--model)"
      }]
    }]
  }
}
```

Additional entries will be added as scraper and BOM-builder code lands.
