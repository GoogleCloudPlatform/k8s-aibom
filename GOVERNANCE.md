# k8s-aibom Governance

This document describes how the k8s-aibom project makes decisions, who can
make them, and how those roles change. It is deliberately small; the
project is small. It will be revised as the maintainer group grows, by the
process at the end of this document.

## Principles

- **Open.** Design, review and decisions happen in public on GitHub:
  issues, pull requests and design documents under `docs/design/`.
- **Scoped.** The project boundaries in `CONTRIBUTING.md` (unprivileged,
  facts not judgments, conservative detection, cloud neutral,
  deterministic output) are governance constraints, not preferences.
  Changing them is a governance change, not a design decision.
- **Vendor neutral.** Maintainership is granted to people, not companies.
  No single employer may hold more than two-thirds of maintainer seats
  once there are three or more maintainers.

## Roles

### Contributors

Anyone who opens an issue, submits a pull request, improves documentation,
reports a workload shape the scrapers misread, or reviews someone else's
change. No formal step is needed. Contributions are accepted under the
project licence and the contribution terms in `CONTRIBUTING.md`.

### Reviewers

Contributors with a track record in an area who are trusted to review and
approve changes there. A reviewer's approval is the review signal a
maintainer relies on before merging in that area (see
`docs/merge-criteria.md` for which changes merge on review alone); it does
not by itself grant merge rights and needs no repository permissions.
Areas today: detection patterns and scrapers, controller and
configuration, charts and release tooling, documentation.

Becoming a reviewer: sustained, high-quality contributions in the area over
roughly three months (as a guide, ten or more merged changes with few
review rounds), nominated by any maintainer and approved by lazy consensus
of the maintainers. Nominations are open: a maintainer opens an issue
titled `Nomination: <name> as reviewer, <area>` with the evidence, the
nominee confirms, and after one week without objection the name is added
to `MAINTAINERS.md`. Contributors may ask a maintainer to be considered.

### Maintainers

Maintainers have merge rights and are jointly responsible for the project's
direction, quality, releases and security response. They are listed in
`MAINTAINERS.md`. Merge rights are repository write access, granted under
the GoogleCloudPlatform organisation's collaborator policy: the process
below decides who is nominated and approved, and the policy decides how
access is provisioned.

Becoming a maintainer: reviewers who have, over roughly six months, shown
judgement about the project's scope, reviewed others' work, and carried a
release or a design document through. Nominated by an existing maintainer
and approved by a two-thirds vote of the maintainers (a single maintainer
counts as unanimous).

Stepping down: a maintainer may step down at any time and is listed as
emeritus. A maintainer inactive for six months (no reviews, merges or
design participation) is moved to emeritus by a two-thirds vote of the
remaining maintainers, after being asked.

## Decision making

- **Default: lazy consensus.** Ordinary changes merge with one reviewer
  approval and no outstanding objection from a maintainer. Silence after a
  reasonable period (two working days for a fix, one week for a design
  document) is consent.
- **Design documents.** Anything that changes emitted document shape,
  CRD schema, a scraper's detection contract, or adds a workload kind or
  sink goes through a numbered design document under `docs/design/` with
  the review window described in `VERSIONING.md`. Design documents are
  approved by lazy consensus of the maintainers and recorded as accepted
  in the document itself.
- **Votes.** When consensus cannot be reached, or for the decisions below,
  maintainers vote on the issue or pull request with +1 / -1 and a
  sentence of reasoning. Votes stay open for at least one week.
  Two-thirds of current maintainers is required for: adding or removing
  maintainers, changing the project boundaries, changing this document,
  changing the licence or the project's home, and adopting or leaving a
  foundation.
- **Releases.** Any maintainer may cut a release following
  `docs/release-checklist.md` and the train in `VERSIONING.md`. Patch
  releases need no vote; a minor release's scope is agreed on its
  milestone.

## Security

Vulnerabilities are reported privately as described in `SECURITY.md`.
Maintainers handle embargoed fixes on a private fork and disclose through
GitHub Security Advisories. A maintainer who cannot keep an embargo for a
report recuses from it.

## Code of conduct

The project follows the `CODE_OF_CONDUCT.md` in this repository. Conduct
reports go to the contacts named there; maintainers named in a report
recuse from handling it.

## Changing this document

Changes to this document are proposed as pull requests and require a
two-thirds vote of the maintainers, open for at least two weeks.
