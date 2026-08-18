# Roadmap

Direction, not commitment: items may move between releases as demand and
contributions dictate. Delivered work is recorded in the
[CHANGELOG](../CHANGELOG.md); versioning policy in
[VERSIONING.md](../VERSIONING.md).

## v1.x — stable API series

- **v1.1 — the verification release.**
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
    jobs; grouped Dependabot updates.
  - **SKILL.md** — an agent-operable runbook for installing, inspecting,
    and troubleshooting the controller.
- **v1.2** — Native GUAC sink for OpenSSF GUAC ingestion; admission
  webhook for `AIBOMControllerConfig` singleton enforcement; additional
  CRD scrapers (llm-d native CRDs, KAITO, Seldon Core); deep KServe
  extraction following `ServingRuntime` references; expanded agent
  framework coverage (Semantic Kernel, Haystack, DSPy).
- **v1.3** — Active registry digest resolution for mutable image tags;
  image SBOM extraction from registries; hardware (GPU/TPU) extraction
  from resource requests and node selectors.
- **API graduation** — `v1beta1` for the AIBOM APIs with a documented
  conversion path (`v1alpha1` remains served and field-frozen through
  1.x; see VERSIONING.md). Lands as its own release so API-contract
  changes never share a release with feature work.

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
