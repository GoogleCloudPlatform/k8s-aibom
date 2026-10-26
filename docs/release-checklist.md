# Release Checklist

Every release — including release candidates — walks this list top to
bottom. A release is one coherent artifact set: tag, binary, image, chart,
CRDs chart (`k8s-aibom-crds`, same version), install.yaml, SBOM,
provenance. If any step fails, fix and restart from
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
5. Helm version: every workflow pins Helm (`azure/setup-helm` `version:`),
   currently v3.22.1, and the weekly version matrix runs one kind leg on
   Helm 4 on purpose (#260). A release is never packaged or attested with
   an unpinned Helm; bump the pin in all six workflow files together.
6. README version strings: the Quickstart's chart `--version` and
   install.yaml release URL updated to the new version (they are
   hardcoded by design so installs are reproducible). Merge all
   README/docs updates BEFORE tagging so the tagged tree is
   self-consistent — release URLs briefly pointing at a not-yet-published
   release is acceptable; a tag whose README contradicts the release is
   not (v1.0.0 lesson: its tagged README predated the install rewrite).
7. Downstream consumers: distributions that qualify k8s-aibom (e.g.
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
   Helm chart and CRDs chart (OCI, same version, both attested),
   install.yaml (digest-pinned), SBOM, provenance attestations, GitHub
   release with notes.

## After publishing

1. Verify the image attestation: `gh attestation verify
   oci://<image>@<digest> --owner GoogleCloudPlatform`. Then verify a
   plugin archive offline from the release assets alone:
   `gh attestation verify kubectl-aibom_<tag>_linux_amd64.tar.gz --owner
   GoogleCloudPlatform --bundle kubectl-aibom_<tag>.provenance.intoto.jsonl`.
   The five `*.intoto.jsonl` assets (image provenance, image SBOM, chart
   provenance, CRDs-chart provenance, plugin provenance) must all be
   present; the Scorecard Signed-Releases check reads them.
2. `helm install` the published CRDs chart, then the main chart, from
   OCI on a clean kind cluster; confirm the digest-pinned image pulls
   and the CRDs carry `app.kubernetes.io/managed-by=Helm`.
3. `kubectl apply` the release install.yaml on a clean cluster; confirm
   readiness.
4. First release of a new package only: set ghcr package visibility to
   public and confirm repository linkage. (v1.6.0: the new
   `charts/k8s-aibom-crds` package.)
5. Update the adoption metrics log (image pull baseline for the new
   version).

## Plugin distribution (after publishing)

0. Bump every installable version reference to the new tag —
   `grep -rn 'version 1\.' README.md docs/install-eks-aks.md
   docs/deck/index.html terraform/variables.tf` — install commands,
   install.yaml URL, terraform chart_version default, deck install
   slide + title kicker. Historical "since vX.Y" feature attributions
   stay as-is.
0. The plugin is live on krew-index (`kubectl krew install aibom`,
   since 2026-09-25); each release updates the existing
   `plugins/aibom.yaml` there rather than adding a new file.
1. Fill `hack/krew/aibom.yaml` version + sha256 fields from
   `kubectl-aibom_checksums.txt` on the release.
2. Validate locally: `kubectl krew install --manifest=hack/krew/aibom.yaml`.
3. PR the updated manifest over kubernetes-sigs/krew-index `plugins/aibom.yaml` (version-bump PRs are lightly reviewed; consider krew-release-bot to automate this step).
