# Contributing a detection pattern

The ideal first contribution to k8s-aibom is a runtime detection
pattern: one allowlist entry plus its tests. It takes one sitting, it
needs no cluster, and it expands what the controller can inventory for
everyone. This page walks through a real one — PR #115, which added the
Infinity embeddings server — so you can copy the shape exactly.

If you found a workload the controller should detect but doesn't, start
by filing it (issues labeled
[`good-first-pattern`](https://github.com/GoogleCloudPlatform/k8s-aibom/issues?q=is%3Aissue+is%3Aopen+label%3Agood-first-pattern)
are pre-scoped and waiting).

## The rule that shapes every pattern

k8s-aibom prefers a false negative to a false positive. A missed runtime
shows up as an honest `unresolved`; a wrong attribution puts a false
fact into someone's audit record. So patterns are:

- **anchored to the publisher's own image namespace** (`^michaelf34/infinity`),
  never to a substring (`.*infinity.*`) that would match unrelated images;
- **bounded at the image-name boundary** with `(?:[:@]|$)`, so tag and
  digest forms match but `…/infinity-extra` does not;
- **not mirror patterns**: a private-registry copy of a known image is
  deliberately out of scope for the default allowlist (operators add
  those via `AIBOMControllerConfig`).

If a pattern needs a broad match to work, it is not ready to be a
default.

## The three files

### 1. `internal/scraper/v1-runtime-patterns.yaml` — the entry

Add a commented entry under `runtimeImagePatterns:`. The comment states
the publisher, why the anchor is safe, and what it deliberately does not
match:

```yaml
  # Infinity — michaelf34/infinity embeddings and reranking server on
  # Docker Hub. Anchored at the image-name boundary so tag and digest
  # forms match (including 0.0.77-cpu and 0.0.77-rocm) while
  # michaelf34/infinity-extra and other registries do not. Not a
  # prefix and not a mirror pattern.
  - runtime: infinity
    pattern: '^michaelf34/infinity(?:[:@]|$)'
```

`runtime` is the name that appears in `kubectl aibom summary` and in
the BOM's runtime component; use the project's own short name,
lower-case.

### 2. `internal/scraper/inference_extract_test.go` — the table rows

Add rows to `TestInferenceConfig_DetectRuntime`. Three kinds are
required — fires, stays quiet on near-misses, and (where relevant) the
digest form:

```go
		// Infinity embeddings/reranking server — publisher-anchored Docker
		// Hub image. Tag and digest forms match; name-prefix and
		// other-registry near-misses do not.
		{"michaelf34/infinity:0.0.77", "infinity"},
		{"michaelf34/infinity:0.0.77-cpu", "infinity"},
		{"michaelf34/infinity@sha256:abc", "infinity"},
		{"michaelf34/infinity-extra:1", ""},
		{"otheruser/infinity:0.0.77", ""},
		{"ghcr.io/michaelf34/infinity:0.0.77", ""},
```

The near-miss rows are the important ones: they are what proves the
anchor is doing its job.

### 3. `CHANGELOG.md` — one bullet under `[Unreleased]` → `### Added`

```markdown
- **Infinity embeddings server runtime pattern** (#111).
  `michaelf34/infinity` (tag and digest forms) attributes as runtime
  `infinity`. Publisher-anchored at the image-name boundary, so
  `michaelf34/infinity-extra` and other registries stay unmatched.
```

## Run the checks

```bash
go test ./internal/scraper/ -run TestInferenceConfig_DetectRuntime -v   # your rows
make test                                                               # everything
```

`make test` downloads the envtest binaries on first run and takes a few
minutes; it is what CI runs. If your pattern changes the output for an
existing fixture, golden files will fail — regenerate them deliberately
with `make update-golden` and say so in the PR; never edit them by hand.
A pure allowlist addition normally touches no golden files.

## Open the PR

- Title: `scraper: detect <publisher>/<image> <what it is>`
- Reference the issue (`Fixes #NNN`) so it closes on merge.
- Mention if you could not run the full suite; a maintainer will run it.
- Sign the Google CLA when the bot asks (once, covers future PRs).

Pattern PRs get a review within **48 hours**. #115 went from opened to
merged in a morning.

## What is not a pattern PR

- **Model identity extraction** (new env vars, arg flags, annotations):
  same files, but the entry goes under `modelEnvVarNames` /
  `modelArgFlags`, and the evidence semantics differ — open an issue
  first so the confidence tier can be agreed.
- **A new workload kind** (a CRD the controller should watch): that is a
  scraper, and it gets a short design note before code (see
  `docs/design/`).
- **A mirror of a known image in your private registry**: configure
  `spec.discovery.inferenceRuntimeImagePatterns` in your
  `AIBOMControllerConfig` instead; the default allowlist stays
  publisher-anchored. Note that the field **replaces** the compiled-in
  list rather than adding to it: copy the defaults you still want from
  `internal/scraper/v1-runtime-patterns.yaml` (or from
  `status.effectiveInferenceRuntimeImagePatterns` on the config CR)
  before appending your own.
