# Roadmap

Direction, not commitment: items may move as demand and contributions
dictate. Version numbers are assigned at release time, not here — this
project's releases are event-driven (v1.1.0 and v1.2.0 were both created
by qualification findings within a week). Delivered work is recorded in
the [CHANGELOG](../CHANGELOG.md); versioning policy in
[VERSIONING.md](../VERSIONING.md).

## Shipped

v1.0.0 (first release: the signed, attested, digest-pinned artifact
set), v1.1.0 (the qualification release), and v1.2.0 (opt-in strict
configuration readiness) — see the [CHANGELOG](../CHANGELOG.md) for
details and the qualification record on
[issue #8](https://github.com/GoogleCloudPlatform/k8s-aibom/issues/8).

## Next — the verification release

- **Sigstore / OMS signature verification** for model identities (the
  `verified` confidence tier): a nested Go module on upstream Sigstore
  libraries. Scope constraints: model artifacts only (container-image
  verification belongs to admission tooling and is a non-goal); trust
  roots and log endpoints are configuration in the standard Sigstore
  TrustedRoot format (self-hosted Sigstore/Rekor deployments supported);
  verification degrades to `claimed` when the log is unreachable —
  never `verified`, never a failed reconcile; every `verified` claim
  carries the verifier identity and method. A public design sketch
  precedes implementation; contributions welcome against the module's
  conformance test suite.
- **Complete CronJob coverage** — wire the watcher and RBAC for the
  existing CronJob scraper path.
- **Configurable workload-kind allowlist** via the
  `AIBOMControllerConfig` CR.
- **CI hardening** — Kubernetes version matrix backing the documented
  compatibility range; `govulncheck` and image scanning as required
  jobs; grouped Dependabot updates; an e2e matrix leg for non-default
  configurations (strict readiness break/recover, sinks under real
  RBAC — the gap class behind the one code defect external
  qualification found).
- **SKILL.md** — an agent-operable runbook for installing, inspecting,
  and troubleshooting the controller.

## Later

- Native GUAC sink for OpenSSF GUAC ingestion.
- Admission webhook for `AIBOMControllerConfig` singleton enforcement.
- Additional CRD scrapers (llm-d native CRDs, KAITO, Seldon Core); deep
  KServe extraction following `ServingRuntime` references; expanded
  agent framework coverage (Semantic Kernel, Haystack, DSPy).
- Active registry digest resolution for mutable image tags; image SBOM
  extraction from registries; hardware (GPU/TPU) extraction from
  resource requests and node selectors.

## API graduation

`v1beta1` for the AIBOM APIs with a documented conversion path
(`v1alpha1` remains served and field-frozen through 1.x; see
VERSIONING.md). Lands as its own release so API-contract changes never
share a release with feature work.

## v2 — Phase 2 capability tier

- eBPF-based scraper for higher-fidelity attribute extraction:
  in-container model load events, egress destination capture, runtime
  version verification against running processes.
- Native SPDX 3.0 AI profile emission alongside CycloneDX.
- Service mesh telemetry integration (Istio / Linkerd / Cilium) for
  network posture in the BOM.
- Upstream CycloneDX profile contribution — a "Kubernetes runtime ML-BOM
  profile" codifying the conventions developed in v1.x as a CycloneDX
  upstream specification.
