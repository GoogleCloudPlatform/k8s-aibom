# Release Checklist

Every release — including release candidates — walks this list top to
bottom. A release is one coherent artifact set: tag, binary, image, chart,
install.yaml, SBOM, provenance. If any step fails, fix and restart from
the tag step with a new version.

## Before tagging

1. `main` is green: all required checks passing on the release commit.
2. `CHANGELOG.md`: move `[Unreleased]` into a new version section with
   today's date; entries reviewed for accuracy, migration notes included
   for any behavior change.
3. Version consistency: chart `version`/`appVersion` are derived at
   package time and the binary version at build time from the tag — no
   hand-edited version strings anywhere in the diff.
4. Dependabot alerts: zero open critical/high.
5. README version strings: the Quickstart's chart `--version` and
   install.yaml release URL updated to the new version (they are
   hardcoded by design so installs are reproducible). Merge all
   README/docs updates BEFORE tagging so the tagged tree is
   self-consistent — release URLs briefly pointing at a not-yet-published
   release is acceptable; a tag whose README contradicts the release is
   not (v1.0.0 lesson: its tagged README predated the install rewrite).
6. Downstream consumers: distributions that qualify k8s-aibom (e.g.
   NVIDIA AICR, per their ADR-019) pin the chart, image, CRDs, and status
   contract as one versioned set — any release changes their
   qualification target. Give downstream maintainers a heads-up,
   especially for unscheduled security patches landing mid-qualification.

## Real-cluster verification (GKE)

Run on a GKE cluster on the current stable channel, from the release
commit:

1. Build and push a candidate image; install via the chart with that
   image.
2. Controller Deployment becomes Available; `AIBOMControllerConfig/default`
   reports `Ready=True`.
3. Label a test namespace `aibom.k8saibom.dev/enabled=true`; deploy a
   known AI workload fixture; verify an AIBOM is created, carries the
   expected components/confidence, and its hashes validate.
4. Deploy a non-AI workload; verify no AIBOM is created for it.
5. Remove the fixture; verify the AIBOM is cleaned up (owner reference).
6. Uninstall the release; verify no **release-owned** resources remain
   (Deployment, RBAC, ServiceAccount, AIBOMControllerConfig). Three
   retentions are intentional, not orphans: the CRDs (Helm never removes
   `crds/`), the release namespace, and workload-owned AIBOMs whose
   owners still exist (inventory outlives the collector; GC'd with the
   workload).

## Tag & publish

1. Tag the verified commit: `git tag vX.Y.Z && git push origin vX.Y.Z`.
2. The release workflow builds and publishes: multi-arch image (ghcr),
   Helm chart (OCI), install.yaml (digest-pinned), SBOM, provenance
   attestations, GitHub release with notes.

## After publishing

1. Verify the image attestation: `gh attestation verify
   oci://<image>@<digest> --owner GoogleCloudPlatform`.
2. `helm install` from the published OCI chart on a clean kind cluster;
   confirm the digest-pinned image pulls.
3. `kubectl apply` the release install.yaml on a clean cluster; confirm
   readiness.
4. First release of a new package only: set ghcr package visibility to
   public and confirm repository linkage.
5. Update the adoption metrics log (image pull baseline for the new
   version).
