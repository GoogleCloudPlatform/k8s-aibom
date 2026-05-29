# Open decisions

This document tracks decisions that are deliberately deferred but
**must** be resolved before public release. Each entry has a status, a
deadline trigger (which phase or milestone blocks on resolution), and
the options under consideration.

Distinct from [`prd-deviations.md`](prd-deviations.md) (which records
implementation choices that differ from the PRD) and
[`schema-divergences.md`](schema-divergences.md) (which records
CycloneDX schema gaps). This file is for unresolved choices, not
recorded divergences.

## OD-001: API group / domain

**Status:** OPEN — BLOCKING for public release
**Resolve before:** Phase 3 finalization (CRD validation markers)
**Current placeholder:** `aibom.k8saibom.dev` with annotation
`api-approved.kubernetes.io: "unapproved, experimental-only"`

**Why this is blocking**

The `.k8s.io` suffix is reserved for upstream Kubernetes-approved API
groups. Using it in a non-KEP-blessed project requires the placeholder
annotation, which signals "experimental" to every kubectl user
inspecting the CRD. For a project positioning as the canonical runtime
ML-BOM generator, this is a poor first impression. The annotation
placeholder is acceptable during local development; it is not
acceptable in the first public release.

**Options**

| Option | Pros | Cons | Status |
|---|---|---|---|
| `aibom.owasp.org` | Strongest ecosystem positioning under the OWASP AIBOM Project umbrella (PRD §12 OQ1). No KEP required. | Requires coordination with OWASP AIBOM leadership. May involve a brief alignment call. | Preferred. Coordination email sent to the OWASP AIBOM Project lead. |
| `aibom.openssf.org` | Strong ecosystem positioning under the OpenSSF umbrella. No KEP required. | Requires coordination with OpenSSF. | Acceptable redirect if OWASP coordination is declined or delayed. |
| `aibom.dev` | Clean, project-owned. No external coordination. Immediately usable. | Lacks explicit ecosystem alignment. Requires registering / holding the domain. | Fallback if neither ecosystem coordination resolves. |
| `aibom.k8saibom.dev` via upstream KEP | Best legitimacy if approved. | Heavy: requires SIG sponsorship, multi-week review, ongoing maintenance contract with K8s SIGs. Not worth it for v1. | Not pursued. |

**Impact of resolution**

When this resolves, the following surfaces change (mechanical search-
and-replace, but touches many files):

- `api/v1alpha1/groupversion_info.go` (`GroupVersion.Group`)
- `api/v1alpha1/*_types.go` (`+kubebuilder:resource` paths,
  `api-approved.kubernetes.io` annotation can be removed if no
  longer `.k8s.io`)
- All references in `internal/scraper/v1-runtime-patterns.yaml`,
  `docs/*.md`, `README.md`, the opt-in label `aibom.k8saibom.dev/enabled`,
  the model annotation prefix `model.k8saibom.dev/*` (separately decide
  whether the model annotation prefix tracks the API group or remains
  `model.k8saibom.dev/*` for compatibility with other AI-BOM ecosystems).
- All generated CRDs in `config/crd/bases/`.
- Helm chart values, install.yaml manifest.
- All test fixtures and golden files.

A one-shot script is appropriate; resolution is mechanical once the
target is known.

## OD-002: Customer-authored AIBOM path (post-v1.0)

**Status:** OPEN — design constraint for post-v1.0 admission webhook
**Resolve before:** Customer-authored AIBOM CRs are permitted (post-v1.0)
**Background**

v1 treats the AIBOM CR as fully controller-owned: only the controller
creates them, and the bootstrap-race guard in
[`internal/controller/aibom_controller.go`](../internal/controller/aibom_controller.go)
distinguishes the "AIBOM exists but Status.LastReconciled is nil"
state as "the prior reconcile's Status().Update is still propagating
through the cache; defer to the next reconcile." That signal is
robust because the controller is the only writer.

**Constraint for post-v1.0**

If a future admission webhook enables customer-authored AIBOM CRs
(per the deferred work in PRD §FR5.x and `aibomcontrollerconfig_types.go`
godoc), the webhook MUST initialize `Status.LastReconciled` to a
sentinel time (or any non-nil value) on any AIBOM it admits. Without
this, every customer-created AIBOM would match the bootstrap-race
guard's "exists but LastReconciled nil" condition and would never be
reconciled — the controller would defer indefinitely.

Mitigation alternatives if the admission-webhook initialization is
not viable: distinguish the bootstrap-race case by a more specific
signal (e.g., AIBOM creation time within the last N seconds, or a
controller-set annotation), accepting the additional code complexity.

## OD-NNN (placeholder)

Add subsequent open decisions here as they arise. Keep the list short;
the goal is "I cannot ship publicly until these are resolved", not "I
have a long-term wishlist".
