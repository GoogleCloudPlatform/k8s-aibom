# Compatibility & Support

## Kubernetes versions

**Policy: the controller uses stable Kubernetes APIs only and has no
known version ceiling.** The tested floor is **1.27**; newer Kubernetes
releases are expected to work without changes, and the verification
matrix below tracks current releases as they ship. (Older versions back
to ~1.23 likely work but are untested.)

How the policy is verified:

| Layer | Coverage |
|---|---|
| envtest (API-server behavior) | k8s 1.34 on every PR; **1.27 / 1.31 / 1.34 weekly** (version-matrix workflow) |
| kind e2e (build, deploy via chart, readiness) | default node image every PR; **floor (1.27) and latest node image weekly** (version-matrix workflow) |
| Real-cluster verification | GKE, current stable channel, before every release tag |
| Independent third-party | NVIDIA/AICR's ADR-019 qualification of v1.2.0 passed on Kind Kubernetes **1.35.0 and 1.36.1** ([record](https://github.com/GoogleCloudPlatform/k8s-aibom/issues/8)) |

## Support policy

- **Latest release only.** Fixes land in a new PATCH release on the current
  MINOR; there are no long-term support branches.
- **Kubernetes window.** Each release supports at minimum the Kubernetes
  versions in upstream support (N-2) at the time of the release.
- **Best-effort project.** This is not an officially supported Google
  product. Security reports follow `SECURITY.md`; issues are triaged on a
  best-effort basis by the maintainers.

## Platform notes

- Cloud-neutral by design: the only cloud-specific code path is the
  optional GCS sink. Runs on any conformant cluster (GKE, EKS, AKS, kind,
  self-managed).
- Multi-arch images: linux/amd64 and linux/arm64.
