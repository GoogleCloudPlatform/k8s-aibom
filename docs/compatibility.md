# Compatibility & Support

## Kubernetes versions

k8s-aibom targets Kubernetes **1.27 through 1.35**.

How that claim is verified today:

| Layer | Coverage |
|---|---|
| envtest (API-server behavior) | k8s 1.34 control-plane binaries |
| kind e2e (build, deploy via chart, readiness) | kind default node image, every PR |
| Real-cluster verification | GKE, current stable channel, before every release tag |

A full per-version CI matrix across the supported range is planned; until
it lands, versions inside the range but outside the table above are
supported on a best-effort basis. The controller uses no APIs newer than
the minimum supported version.

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
