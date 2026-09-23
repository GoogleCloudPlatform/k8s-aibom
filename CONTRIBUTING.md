# Contributing to k8s-aibom

Contributions are welcome. The most valuable ones today: detection
patterns for runtimes we don't recognize yet, reports of workload
shapes the scrapers misread, scraper support for new workload kinds,
and corrections to documentation. If you run the controller somewhere
interesting, a bug report with the workload spec that confused it is a
genuinely useful contribution.

## Scope

The project has firm boundaries, documented in the README and the
design docs. Contributions that cross them won't be merged, so check
before investing time:

- **Unprivileged posture.** No DaemonSets, no privileged containers,
  no kernel-level access, no sidecar injection, no pod mutation. The
  controller reads the Kubernetes API and nothing else.
- **Facts, not judgments.** The controller records what is running and
  the evidence for it. It does not score, grade, or block anything.
  Policy belongs in policy engines (see `docs/policy-cookbook.md`).
- **Conservative detection.** False negatives over false positives.
  New detection patterns need an unambiguous signal (an env var, a
  container arg, an image identity); ambiguous workloads stay
  `unresolved` by design.
- **Cloud neutrality.** The core controller must run unmodified on any
  conformant Kubernetes. Vendor-specific code is limited to optional
  sinks behind generic interfaces (the GCS sink is the pattern).
- **Deterministic output.** Emitted documents are byte-deterministic
  for a given input. Changes that make output ordering or content
  nondeterministic will be rejected.

For anything larger than a focused fix — a new scraper, a schema
change, a new sink — open an issue first, or a design doc under
`docs/design/` for substantial work. `VERSIONING.md` describes the
release train and the design-doc review window. This costs a little
time up front and saves a lot of it on review.

## Development setup

You need a Go toolchain (version per `go.mod`), `make`, and Docker if
you build images. Common targets:

```
make build          # compile the controller (runs fmt + vet)
make test           # full test suite; downloads envtest binaries on first run
make cover          # test with an HTML coverage report
make update-golden  # regenerate golden files after intentional output changes
make kubectl-plugin # build the kubectl-aibom binary into bin/
```

The test suite uses `envtest` (a local API server, no cluster
required). The end-to-end matrix in `hack/e2e-matrix.sh` runs against
a [kind](https://kind.sigs.k8s.io/) cluster and is exercised in CI;
you rarely need it locally unless you're changing readiness, sink, or
verification behavior. The verifier is a nested Go module under
`verifier/` with its own tests (`go test ./...` from that directory).

## Testing expectations

- Behavior changes come with tests. For detection patterns that means:
  fires on the intended shape, stays quiet on clean input, and the
  golden output is updated deliberately (`make update-golden`), never
  by hand.
- Degradation paths are tested, not assumed: missing permissions,
  unreachable sinks, and malformed input should produce recorded
  facts or clean skips, never a crashed reconcile loop.
- The byte-determinism contract is load-bearing. If your change alters
  emitted bytes for existing fixtures, that's either a bug or a
  documented, changelog-worthy change — the golden files make it
  visible either way.

## Pull requests

Fork, branch, open a PR against `main`. CI runs the test suite, the
e2e matrix, static analysis, workflow linting, and a release dry-run;
all of it must pass. Every merge requires review from a maintainer.
Small, focused PRs review faster than large ones.

## Where to file what

- Bugs and feature requests: GitHub issues (templates provided).
- Security issues: do **not** open a public issue — see
  [`SECURITY.md`](SECURITY.md) for the disclosure process.

## Contributor License Agreement

Contributions to this project must be accompanied by a Contributor
License Agreement (CLA). You (or your employer) retain the copyright
to your contribution; this simply gives us permission to use and
redistribute your contributions as part of the project. Head over to
<https://cla.developers.google.com/> to see your current agreements on
file or to sign a new one.

You generally only need to submit a CLA once, so if you've already
submitted one (even if it was for a different project), you probably
don't need to do it again.
