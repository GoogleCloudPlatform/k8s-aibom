# Changelog

All notable changes to k8s-aibom are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[Semantic Versioning](https://semver.org/) (see `VERSIONING.md`).

## [Unreleased]

### Changed

- Readiness now gates on informer cache sync: `/readyz` fails until the
  controller can observe the cluster, and the chart wires liveness and
  readiness probes against the health endpoints (previously no probe
  consulted them).

## [1.0.0] - 2026-08-17

First tagged release. Every release publishes a coherent, verifiable
artifact set: a multi-arch image (linux/amd64, linux/arm64) on
ghcr.io/googlecloudplatform/k8s-aibom carrying Sigstore build-provenance
and CycloneDX SBOM attestations; a digest-pinned Helm chart on
oci://ghcr.io/googlecloudplatform/charts carrying a build-provenance
attestation (the chart has no separate SBOM attestation); and a
digest-pinned install.yaml release asset.

### Changed

- **AIBOMControllerConfig is now a regular Helm release resource** instead
  of a `pre-install` hook, so Helm owns install/upgrade/rollback/uninstall
  deterministically, and the invalid `namespace` on the cluster-scoped
  object is gone. **Migration for existing from-source installs:** the
  hook-created CR carries no Helm ownership metadata; before upgrading an
  existing release, delete it (`kubectl delete aibomcontrollerconfig
  default`) or annotate it for Helm adoption.
- **Secret access is now opt-in.** The namespace-scoped Role granting
  Secret reads (used only for sink credentials) is rendered only when
  `rbac.sinkSecretAccess=true`. With no sinks configured (the default), the
  controller holds no Secret permissions. Set the value if your
  AIBOMControllerConfig references credential Secrets.
- ClusterRole rules deduplicated; `aibomcontrollerconfigs` narrowed to
  read-only + status (matching the controller's kubebuilder markers).

### Added

- `image.digest` chart value: a digest takes precedence over the tag so
  releases can be pinned immutably without patching the chart.
- Controller version is stamped at build time via ldflags
  (`main.controllerVersion`); local builds report `dev`. The image carries
  OCI identity labels.
- Community health files: CODEOWNERS, issue forms, PR template.
- `VERSIONING.md`, this changelog, `docs/compatibility.md`,
  `docs/release-checklist.md`.
- Release pipeline: tag-triggered publishing of the signed multi-arch
  image (with provenance and CycloneDX SBOM attestations), the OCI chart
  (with a provenance attestation), and digest-pinned install.yaml; a
  dry-run job exercises the release path on every PR.

### Security

- Dependency updates cleared all critical/high Dependabot alerts
  (golang.org/x/net, golang.org/x/crypto, google.golang.org/grpc).
