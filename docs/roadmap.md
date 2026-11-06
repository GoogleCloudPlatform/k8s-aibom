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

## v1.6 — the coverage release (shipped 2026-10-19; patches 10-21, 10-22)

NVIDIA Dynamo, NIM Operator, LeaderWorkerSet, llm-d `DisaggregatedSet`
and CronJob as reported kinds ([Design 003](design/003-nimservice-lws-scrapers.md),
[Design 007](design/007-llm-d-lens.md) Part 1); ownership roll-up, one
workload one AIBOM ([Design 005](design/005-ownership-rollup.md)); the
workload-kind allowlist ([Design 006](design/006-workload-kind-allowlist.md));
the companion `k8s-aibom-crds` chart; third-party CRD outages that
degrade one kind instead of the controller ([Design 004](design/004-watch-isolation.md)).
NVIDIA AICR requalified each of the three tags within a day of release.

## v1.7 — the interoperability release (target 2026-11-24)

**Focus.** The document becomes the one other tools read, not only the
one we write. Three changes to the document shape land together so
downstream consumers see a single transition, and the two main
consumers prove the result by ingesting it. Tracking: [#315](https://github.com/GoogleCloudPlatform/k8s-aibom/issues/315)
(go/no-go Fri 2026-11-14, rc.1 Mon 11-17, tag Mon 11-24).

**In scope (decides the theme).**

- **CycloneDX alignment, one byte-shape transition** (#132):
  `metadata.lifecycles` with phase `operations`, the honest statement
  that every document is an as-deployed inventory (the CycloneDX
  maintainers' guidance on spec#990); `pkg:oci` purls on container
  components; the `k8s-aibom:` property namespace registered in the
  CycloneDX Property Taxonomy replacing the dotted names (#144). Ends
  byte-identity to v1.4.0; determinism within a version, the summary
  CRD fields and the API schema are unchanged. CHANGELOG-led, with a
  heads-up to AICR, production-stack and KubeAI before merge.
- **Proof by ingestion** (#313): Dependency-Track and GUAC consume the
  v1.7 document, documented and tested. This is the acceptance test
  for the shape change.
- **`verified` is a confidence tier in the code, not only the docs**
  (#328): a verified model signature raises the model component's
  confidence, as Design 002 §4 says.
- **Both API versions carry the allowlist** (#343): a `v1alpha1` write
  can no longer drop `spec.discovery.workloadKinds`.
- **llm-d lens, Part 2a** (#137, Design 007 amendment): `InferencePool`
  membership as a routing fact, discovery and updates. Part 2b
  (endpoint-picker evidence) follows in 1.8.

**Small, any time:** webhook sink CA bundle for bearer auth over a
private CA (#96).

**Stretch, moves to 1.8 if not on main by rc.1:** the detection-coverage
corpus scored per release (#314); a CycloneDX 2.0 sample document now
that the 2.0 AI/ML module is merged (#145); narrowing the apps/v1 and
batch watches under the Design 004 supervisor (#136); the KubeAI lens
(#172, needs its Design 008 note first).

**Not in this release:** anything that changes the API schema beyond
#343 (that is 1.8), new workload kinds, new platforms.

## v1.8 — the agent-platform release (target 2027-01-26)

**Focus.** The inventory follows AI workloads onto the platforms that
run agents, and the project becomes installable where those platforms
live. Each new lens is a published design with a review window before
code.

- **kagent lens** ([#329](https://github.com/GoogleCloudPlatform/k8s-aibom/issues/329),
  Design 010): the declared agent graph (Agent, ModelConfig,
  RemoteMCPServer, Harness) as declared-tier evidence.
- **agent-sandbox lens** ([#330](https://github.com/GoogleCloudPlatform/k8s-aibom/issues/330),
  Design 009): Agent Sandbox on GKE, NVIDIA OpenShell and Red Hat's
  OpenShell distribution, one lens.
- **KubeAI lens** (#172, Design 008) if it did not catch 1.7, and
  llm-d Part 2b.
- **`status.summary.runtimes`** (#336): the summary names every
  runtime, not one; the API change that waits for a minor.
- **OperatorHub** (#345): an OLM bundle and submission to operatorhub.io
  and the OpenShift catalog, with an OpenShift install page.
- **Carry-overs from the 1.7 stretch list** in the order above.

**Not in this release:** `v1alpha1` removal (a separate, announced
release outside 1.x), SPDX emission (v2 tier below).

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
[migration guide](migration-v1beta1.md)); the chart's default
`AIBOMControllerConfig` template moved to `v1beta1` in v1.4.0.
Remaining lifecycle work: `v1alpha1` removal is a separate, announced
release outside 1.x (VERSIONING.md).

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
