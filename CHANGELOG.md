# Changelog

All notable changes to k8s-aibom are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[Semantic Versioning](https://semver.org/) (see `VERSIONING.md`).

## [Unreleased]

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

### Security

- Dependency updates cleared all critical/high Dependabot alerts
  (golang.org/x/net, golang.org/x/crypto, google.golang.org/grpc).
