# Incident response: "where is X running?"

When a supply-chain incident hits an AI component — a poisoned
package release, a compromised image, a model with a revoked
signature — the first question is never "is this bad?" (the advisory
already told you). It is:

> **Where is this running in our fleet, right now, at which
> versions?**

This page documents answering that question with k8s-aibom. It uses a
real incident as the worked example: the **litellm 1.82.8 PyPI
compromise** ([BerriAI/litellm#24512](https://github.com/BerriAI/litellm/issues/24512)),
in which a malicious `.pth` file in the published wheel ran a
credential stealer at every Python interpreter start.

## Division of labor (read this first)

k8s-aibom **locates**; it does not **condemn**. It records what is
running — runtimes, models, image digests — as evidence-backed facts.
Determining that a specific version or digest is malicious is the job
of advisories and scanners (Trivy, Artifact Analysis,
Dependency-Track, your registry's scanning). The BOM's image digests
are the join key between the two: the scanner tells you *which
artifact* is bad; the BOM tells you *where that artifact is serving*.

## The workflow

### 1. Fleet view: which workloads run the affected runtime?

```
kubectl aibom summary -A
```

The RUNTIME column attributes every tracked workload. During the
litellm incident, every row with runtime `litellm` is your candidate
set — one command, no ssh, no spreadsheet census.

Runtime attribution is `inferred` (matched from the image against the
[audit-reviewable allowlist](../internal/scraper/v1-runtime-patterns.yaml)),
and only images on that allowlist are attributed — a mirrored or
custom-built image may show no runtime. **An empty RUNTIME column is
"not attributed," never "confirmed absent."** For incident scoping,
treat unattributed AI workloads as unknowns to check by digest, not
as cleared.

### 2. Evidence: exact images and digests per workload

```
kubectl aibom view <name> -n <namespace>
```

The decoded CycloneDX document carries each container's image
reference and resolved digest, with an evidence locator naming the
exact spec field each fact came from. The digests are what you feed
to — or match against — your scanner's list of affected artifacts.

### 3. Retrospective: what was running when?

If you ship BOMs to an external sink (GCS, webhook →
[Dependency-Track](integrations/dependency-track.md)), you have a
timestamped archive of what was serving on any past date — the
"were we exposed during the compromise window?" question, answerable
from retained documents rather than memory. Documents are
byte-deterministic, so archived copies diff cleanly.

### 4. Verify the recovery

After remediation, the same summary shows the replacement digests.
For model artifacts specifically, the `verified` confidence tier
(signature verification against your trust roots, v1.5.0+) turns
"we redeployed the good version" into a cryptographically checked
fact rather than an assertion.

## Scope honesty

- k8s-aibom does not enumerate packages inside images (no pip/npm
  inventory) — that is scanner territory, deliberately.
- Namespace opt-in bounds visibility: workloads in namespaces without
  the `aibom.k8saibom.dev/enabled=true` label are not tracked. Your
  coverage during an incident is exactly your opt-in footprint —
  a reason to opt in serving namespaces before you need this page.
- Detection is conservative by design: false negatives (unattributed
  runtime) over false positives (wrong attribution in an audit).
