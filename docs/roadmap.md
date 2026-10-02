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
also shipped (with v1.3.0). **v1.5.1 — security PATCH — shipped
2026-09-28** (cut from `release/1.5`; source delta over v1.5.0 is
exactly the two fixes: ownership-based pod attribution across all
four workload kinds, and rejection of webhook credentials over
cleartext — retrospectives in #97/#98). **v1.5.0 — the trust release
— shipped 2026-09-22**: Sigstore/OMS signature verification (the `verified`
tier, [Design 002](design/002-sigstore-rekor-verifier.md), two rounds
of external review), the output sanitization guarantee, the
`kubectl aibom` plugin, and the non-default-configuration e2e matrix.

## Next — v1.6, the coverage release

- **CRD workload scrapers: Dynamo, NIMService, LeaderWorkerSet** —
  [Design 003](design/003-nimservice-lws-scrapers.md) merged
  2026-10-01 after the open review window. Implementation order
  follows the AICR-review ranking: the `DynamoGraphDeployment`,
  `NIMService` and `LeaderWorkerSet` scrapers are on main, and the §3
  ownership roll-up (Design 005) with them.
- **Complete CronJob coverage** — landed with the ownership roll-up
  (Design 005): one AIBOM per CronJob, spawned Jobs absorbed.
- **Configurable workload-kind allowlist** via the
  `AIBOMControllerConfig` CR — with a short design note first: if the
  allowlist narrows what the informers watch (not only what is
  reported), it directly reduces the controller's read surface.
- **Separate `k8s-aibom-crds` chart** (#77) — the CRD-lifecycle
  packaging requested by an AICR operator; externally contributed.
- **Sink reliability** (landed on main post-v1.5.0, ships in v1.6):
  transiently failed sinks now retry until the archive heals, and the
  bootstrap-race deferral requeues explicitly (#91, #95).
- **Document-size caps** — bound container-component name/version and
  component count, with recorded (never silent) truncation.
- **Remaining CI hardening** — `golangci-lint` (with `gosec`) and
  `govulncheck` landed as required jobs after v1.5.0; remaining: image
  scanning as a required job and grouped Dependabot updates.

## October–November — from artifact to an answer

Direction (per the preamble: not commitment): the BOM is evidence; the
product's job is answering "where is this model, runtime, or digest
serving — now, last week, across clusters." In flight:

- **`kubectl aibom find`** — the summary table, filtered: runtime,
  model, image, digest, signature state. Zero matches exits 0 and is
  documented as "not attributed", never "confirmed absent".
- **`kubectl aibom discover`** — the pre-opt-in candidate pass:
  which workloads *would* be inventoried if their namespace were
  labelled. Client-side, the caller's own RBAC, nothing recorded —
  the opt-in contract for what the controller *records* is unchanged.
- **Coverage-gap instrumentation** — an `unresolved` attribution
  becomes an invitation to report (issue template, a gap count in
  `summary`), because conservative detection should surface its own
  false negatives rather than silently absorb them.
- **v1.7 candidates:** a queryable per-workload answer record beside
  each archived BOM plus a `{cluster}` path token (the fleet/history
  dimension); `pkg:oci` purls for container images (ends the
  byte-identity-to-v1.4.0 property — will be a documented,
  changelog-led change); a CA option for bearer-auth webhook sinks
  (#96).

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
