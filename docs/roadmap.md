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
also shipped (with v1.3.0). **v1.5.0 — the trust release — shipped
2026-09-22**: Sigstore/OMS signature verification (the `verified`
tier, [Design 002](design/002-sigstore-rekor-verifier.md), two rounds
of external review), the output sanitization guarantee, the
`kubectl aibom` plugin, and the non-default-configuration e2e matrix.

## Next — v1.6, the coverage release

- **CRD workload scrapers: NIMService, LeaderWorkerSet, Dynamo** —
  [Design 003](design/003-nimservice-lws-scrapers.md) is in open
  review (window closes 2026-10-01).
- **Complete CronJob coverage** — wire the watcher and RBAC for the
  existing CronJob scraper path.
- **Configurable workload-kind allowlist** via the
  `AIBOMControllerConfig` CR — with a short design note first: if the
  allowlist narrows what the informers watch (not only what is
  reported), it directly reduces the controller's read surface.
- **Remaining CI hardening** — `golangci-lint` (with `gosec`) and
  `govulncheck` landed as required jobs after v1.5.0; remaining: image
  scanning as a required job and grouped Dependabot updates.

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

- Native SPDX 3.0 AI profile emission alongside CycloneDX.
- Service mesh telemetry integration (Istio / Linkerd / Cilium) for
  network posture in the BOM.
- Upstream CycloneDX profile contribution — a "Kubernetes runtime ML-BOM
  profile" codifying the conventions developed in v1.x as a CycloneDX
  upstream specification.

## Out of scope — permanently

The controller's unprivileged posture is identity, not a phase: no
DaemonSets, no privileged containers, no kernel-level access
(including eBPF), no sidecars, no pod-spec mutation. Extraction ideas
that would require kernel access do not belong on this roadmap. If
such fidelity is ever warranted, it would be a separate, explicitly
opt-in component with its own threat model — not an evolution of this
controller.
