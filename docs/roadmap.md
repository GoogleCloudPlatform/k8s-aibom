# Roadmap

Direction, not commitment: items may move between releases as demand and
contributions dictate. Delivered work is recorded in the
[CHANGELOG](../CHANGELOG.md); versioning policy in
[VERSIONING.md](../VERSIONING.md).

## v1.x — stable API series

- **v1.1** — Native GUAC sink for OpenSSF GUAC ingestion; Sigstore / OMS
  signature verification for model identities (`verified` confidence),
  implemented as a nested module on upstream Sigstore libraries with
  configurable trust roots; admission webhook for `AIBOMControllerConfig`
  singleton enforcement; configurable workload-kind allowlist via the CR.
- **v1.2** — Additional CRD scrapers (llm-d native CRDs, KAITO, Seldon
  Core); deep KServe extraction following `ServingRuntime` references;
  expanded agent framework coverage (Semantic Kernel, Haystack, DSPy).
- **v1.3** — Active registry digest resolution for mutable image tags;
  image SBOM extraction from registries; hardware (GPU/TPU) extraction
  from resource requests and node selectors.
- **API graduation** — `v1beta1` for the AIBOM APIs with a documented
  conversion path (`v1alpha1` remains served and field-frozen through
  1.x; see VERSIONING.md).

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
