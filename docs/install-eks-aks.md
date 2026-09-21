# Installing on EKS, AKS, and other conformant clusters

k8s-aibom is cloud-neutral by constraint, not by accident: the
controller observes via the standard Kubernetes API only, and the sole
Google-specific dependency is the *optional* GCS sink. Everything else
— detection, the AIBOM CRs, the inline documents, the webhook sink,
signature verification — behaves identically on Amazon EKS, Azure AKS,
OpenShift, on-prem, and local clusters. Release verification runs on
GKE; the CI e2e suite runs on kind, which is where the
cloud-neutrality claim is continuously exercised.

## Install (identical everywhere)

```bash
helm install k8s-aibom oci://ghcr.io/googlecloudplatform/charts/k8s-aibom \
  --version 1.5.0 \
  --namespace k8s-aibom-system --create-namespace

kubectl label namespace <your-ai-namespace> aibom.k8saibom.dev/enabled=true
```

The published chart pins the controller image by digest and every
release ships provenance and SBOM attestations — verify them with
`gh attestation verify` or cosign against the release identity,
identically from any cloud.

## Cloud-specific notes

**Sinks.** The CRD-status document (always on) and the webhook sink
are cloud-neutral; use the webhook sink to feed any HTTPS consumer —
[Dependency-Track](integrations/dependency-track.md), GUAC's
blob-storage collector, an S3/Blob-writing relay of your own, or a
SIEM. The GCS sink is the one Google-specific component; on EKS/AKS
either skip it or reach GCS cross-cloud via Workload Identity
Federation if GCS is genuinely your audit archive.

**Identity.** The controller needs no cloud identity at all unless
you enable the GCS sink. RBAC is namespace-scoped K8s RBAC, identical
on every distribution.

**Signature verification (v1.5.0+).** Trust-root modes are
cloud-neutral: the Sigstore public-good root via TUF, a self-hosted
TUF mirror, or a static trusted-root file for air-gapped clusters.
Nothing in verification touches cloud APIs.

**OpenShift.** The chart's default securityContext (non-root, no
privilege, no host access) is compatible with `restricted-v2`; no SCC
changes required.

## Verified combinations

The [compatibility policy](compatibility.md) documents the tested
Kubernetes range with a weekly version matrix; the controller uses
stable APIs only, with no known version ceiling. Runtime detection
(vLLM, TGI, Triton, NIM, Dynamo, Ollama, and the rest) is
image-pattern based and cares nothing for the cloud underneath.
If you run it somewhere interesting, an issue reporting the
combination — working or not — feeds the tested matrix.
