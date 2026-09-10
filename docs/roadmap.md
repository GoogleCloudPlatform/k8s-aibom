# Roadmap

Direction, not commitment: items may move as demand and contributions
dictate. Releases ship on a monthly train (see the release-cadence
policy in [VERSIONING.md](../VERSIONING.md)); features catch the train
they are ready for, and new capability surface gets a published design
doc with a review window first. Delivered work is recorded in the
[CHANGELOG](../CHANGELOG.md).

## Shipped

v1.0.0 (first release: the signed, attested, digest-pinned artifact
set), v1.1.0 (the qualification release), v1.2.0 (opt-in strict
configuration readiness), v1.3.0 (API graduation to `v1beta1` with
dual serving, plus the readiness-probe fixes found in boundary
testing), and v1.4.0 (the downstream-coverage release: NVIDIA
NIM/Dynamo/TGI detection patterns, chart CR at `v1beta1`, performance
docs re-baselined on dual-sampled live-GKE runs) — see the
[CHANGELOG](../CHANGELOG.md) for details and the qualification record
on [issue #8](https://github.com/GoogleCloudPlatform/k8s-aibom/issues/8).
The weekly Kubernetes version matrix backing the compatibility range
also shipped (with v1.3.0).

## Next — v1.5.0, the trust release ([milestone 1](https://github.com/GoogleCloudPlatform/k8s-aibom/milestone/1))

- **Sigstore / OMS signature verification** for model identities (the
  `verified` confidence tier) — [Design 002](design/002-sigstore-rekor-verifier.md)
  is in open review (#55; two rounds of external review already
  incorporated). Key properties: model artifacts only; nested Go
  module on upstream sigstore-go; configurable trust roots
  (public / TUF mirror / static bundle); `verified` requires an
  operator-configured signer-identity constraint — under a public
  trust root with no identity constraints the tier is unattainable by
  design; verification outcomes are facts and never fail a reconcile
  (#56).
- **Output sanitization guarantee** — audit-backed invariant that no
  credential material appears in emitted documents, with a redaction
  pass at the BOM-build boundary (#57).
- **`kubectl aibom` plugin** — view / summary / verify, so reading and
  hash-checking a BOM is one command (#58).
- **e2e matrix for non-default configurations** — strict readiness,
  sinks under real RBAC, verification enabled (#59).

## v1.6 train (following)

- **Complete CronJob coverage** — wire the watcher and RBAC for the
  existing CronJob scraper path.
- **Configurable workload-kind allowlist** via the
  `AIBOMControllerConfig` CR — with a short design note first: if the
  allowlist narrows what the informers watch (not only what is
  reported), it directly reduces the controller's read surface.
- **Remaining CI hardening** — `govulncheck` and image scanning as
  required jobs; grouped Dependabot updates.

## Later

- Native GUAC sink for OpenSSF GUAC ingestion.
- **SKILL.md** (agent-operable runbook) — deliberately parked: the
  docs have proven sufficient so far; revisits on evidence of demand.
- Admission webhook for `AIBOMControllerConfig` singleton enforcement.
- Additional CRD scrapers (llm-d native CRDs, KAITO, Seldon Core); deep
  KServe extraction following `ServingRuntime` references; expanded
  agent framework coverage (Semantic Kernel, Haystack, DSPy).
- Active registry digest resolution for mutable image tags; image SBOM
  extraction from registries; hardware (GPU/TPU) extraction from
  resource requests and node selectors.

## API lifecycle

`v1beta1` shipped in v1.3.0 (dual-served, storage on `v1beta1`; see the
[migration guide](migration-v1beta1.md)). Remaining lifecycle work:
the chart's default `AIBOMControllerConfig` template moves to `v1beta1`
in the next MINOR release, and `v1alpha1` removal is a separate,
announced release outside 1.x (VERSIONING.md).

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
