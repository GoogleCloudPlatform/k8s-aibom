# Merge criteria

How pull requests to k8s-aibom get merged, so contributors can predict the
outcome before they open one. This complements `CONTRIBUTING.md` (scope)
and `VERSIONING.md` (release train and design-review windows).

## Every pull request

- CI green on the first run. A check that failed and passed on a rerun is
  read as a failure until a maintainer has looked at the log and said
  otherwise; flaky tests are fixed, not retried past.
- `make test` run locally by the reviewer for any change under `internal/`,
  `cmd/` or `api/`, with the race detector (that is what `make test` does).
- One CHANGELOG entry under `[Unreleased]`, in the author's words, naming the
  issue it fixes.
- A review comment on the pull request that states what was checked. Merges
  are not silent.

## Merged on review, without a maintainer discussion

Changes in these classes are merged by the reviewing maintainer as soon as
the checks above pass:

- Documentation: README, `docs/`, chart `values.yaml` comments, API godoc and
  the CRD descriptions regenerated from it, with no schema change.
- Tests only, including fixes for flaky tests.
- Detection patterns in `internal/scraper/v1-runtime-patterns.yaml` or the
  built-in vector-database and agent lists, when they follow the anchoring
  convention in `docs/contributing-a-pattern.md` and carry the match and
  non-match test matrix.
- Bug fixes confined to `internal/scraper`, `internal/controller` or
  `cmd/kubectl-aibom`, of roughly two hundred lines or fewer, with tests,
  from a contributor with ten or more merged pull requests.

## Held for a maintainer discussion

These are merged only after the maintainers have discussed them on the pull
request or the linked design document, whatever the size of the diff:

- Anything under `.github/`, the Makefile's release targets, `cmd/manager`,
  the charts' RBAC and templates, `go.mod` major-version bumps,
  `internal/sigverify`, or sink authentication.
- Schema changes under `api/`: a new or renamed field, a changed type or
  enum, a new CRD version.
- A contributor's first pull request.
- Design documents under `docs/design/`, which follow the review window in
  `VERSIONING.md`.
- Any change to behaviour a distribution has qualified, such as the
  `AIBOMControllerConfig` `Ready` condition that NVIDIA AI Cluster Runtime's
  health check reads (`docs/evidence/`).

## Issues

Bug reports and fix proposals are answered by whichever maintainer reads them
first. Scope, design direction and anything involving a partner project are
answered by the project lead.

## Branch protection

`main` requires the `test`, `golangci-lint`, `static-analysis`,
`release-dry-run`, `helm-lint`, `govulncheck` and `check-changes` checks to
pass, allows auto-merge once they do, and does not require the branch to be
current with `main` (chained pull requests that only touch the CHANGELOG at
the same spot are merged in order instead).
