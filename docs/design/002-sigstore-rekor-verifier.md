# Design 002: Sigstore/Rekor verifier — the `verified` signature tier

Status: Draft (2026-08-25); **amended 2026-09-08 and 2026-09-09 after
external review** (credit: @Santoshkumarpuppala, review on #55 and
model-transparency#659) — first, §3's binding rules were rewritten
when review showed the original subject-*name* gate was
self-upgradable under default configuration; second, mismatch
precedence was corrected so a format-permitted rename cannot block a
satisfied digest binding (digest over name). The review window is
**extended through 2026-09-16** for the amended sections (§2
identities, §3, §4, §6, Goal). Published for open review
before any code (a standing commitment on
[issue #8](https://github.com/GoogleCloudPlatform/k8s-aibom/issues/8)).
Review is invited from anyone consuming or producing model signatures —
the sigstore / OpenSSF model-signing community especially, since this
would be among the first runtime consumers of that format — and from
downstream distributions (NVIDIA AICR is the first known consumer).
Ships as v1.5.0 per VERSIONING.md (additive MINOR).

## Context

- The verification seam has existed since v1.0.0:
  `internal/scraper/signature.go` defines `SignatureVerifier`,
  `SignatureClaim`, `SignatureResult`, and the three-state
  `SignatureStatus` (`unsigned` / `claimed` / `verified`), with a
  `NoopVerifier` and a standing rule that no v1 verifier may emit
  `verified` (schema-divergences entry D-001). This design lifts that
  rule by implementing the verifier the seam was reserved for.
- Constraints already on the public record (issue #8): design sketch
  before code; a nested Go module on upstream `sigstore-go`;
  configurable trust roots; **model artifacts only**.
- The consumer condition has been met: NVIDIA scoped the verifier out
  of registry-only adoption and said "revisit when we move to stock
  recipes." AICR v0.20.0 shipped k8s-aibom in a stock recipe on
  2026-08-24. AICR bundles already carry transparency-logged
  attestations, and their build pipeline inherits a self-hosted
  Sigstore — the configurable-trust-root requirement is theirs.

## Goal

When a workload's declared model identity carries a signature
reference, cryptographically verify it: validate the signing chain
against configured trust roots, confirm inclusion in a Rekor
transparency log, confirm the signer satisfies the operator's
identity constraints, and confirm no declared binding is
contradicted. On success, the signature record's status becomes
`verified` and the model identity's confidence may be reported as
`verified` — the tier the README has promised since v1.0. `verified`
answers *"did someone the operator trusts sign a statement consistent
with this claim?"* — the signer constraint is what makes the tier
mean something (§3).

## Non-goals (stated so reviewers can hold us to them)

1. **No container-image verification.** Image signature policy belongs
   to admission controllers (sigstore policy-controller, Kyverno);
   duplicating it here adds a worse copy of an existing control.
   Model artifacts only, as committed.
2. **No content verification on disk.** The controller observes the
   Kubernetes API only — no node agent, no privileged container (a
   property downstream qualification depends on). `verified` means
   *"the declared identity is backed by a valid, transparency-logged
   signed statement whose subject matches."* It does not mean "the
   bytes on the GPU were hashed." The same honesty boundary as
   "an AIBOM does not prove execution."
3. **No policy verdicts.** Verification outcomes are emitted as facts
   (`verified`, or `claimed` with a recorded failure reason).
   Whether an unverified model is acceptable is the consumer's
   decision (AICR health contract, GUAC, admission policy) — this
   project emits facts, not judgments.

## Decision

### 1. Module layout

A nested Go module, `verifier/`, owning the `sigstore-go` dependency
tree (and its own Dependabot surface). The root module gains one
dependency: the nested module itself. The `SignatureVerifier`
interface stays where it is (`internal/scraper`); `verifier/` provides
`RekorVerifier` implementing it; `cmd/manager` wires it in only when
verification is enabled in config, otherwise the `NoopVerifier` path
is unchanged. Disabled-mode behavior is byte-identical to v1.4.0.

### 2. Trust configuration (`AIBOMControllerConfig.spec.verification`)

```yaml
verification:
  enabled: false            # default off; enabling is a deliberate act
  trustRoot:
    mode: public            # public | tufMirror | staticBundle
    tufMirrorURL: ""        # mode=tufMirror: self-hosted Sigstore (AICR's case)
    staticBundlePath: ""    # mode=staticBundle: air-gapped trust bundle
  rekorURL: ""              # empty = the trust root's Rekor
  identities:               # who may sign. Empty list = identities are
    - issuer: ""            #   recorded but unconstrained — and under
      subjectPattern: ""    #   mode=public the `verified` tier is then
                            #   UNATTAINABLE by design (§3).
                            #   subjectPattern is RE2 against cert SAN
  perClaimTimeout: 10s      # hard deadline per verification attempt
  cacheTTL: 24h
```

`public` embeds the Sigstore public-good TUF root at build time (no
network dependency to *start* verifying); `tufMirror` serves the
self-hosted case; `staticBundle` serves air-gap. Identity constraints
are facts recorded into the result (`SignatureResult.Identity`), and
optionally enforced: a chain that validates but fails the identity
pattern records outcome `identity-mismatch` and stays `claimed`.

### 3. What is verified, and against what subject

The verifier consumes **Sigstore bundles over model-signing
statements** (OMS / sigstore `model-signing` manifest format). That
format's subject is a manifest of per-file — and for SafeTensors,
per-tensor — content digests, which is how "SafeTensors Merkle/root
binding" enters this design: as a **supported subject format**, not as
controller-side hashing (non-goal 2).

Binding chain, all steps recorded as evidence:

1. The scraper found a declared model identity and a signature
   reference (today: the `model.k8saibom.dev/oms-signature`
   annotation; the `SignatureClaim` carries both plus evidence).
2. The verifier fetches the referenced bundle (see fetch constraints,
   §6), validates chain → trust root, verifies the Rekor inclusion
   proof, and parses the statement.
3. **Binding (amended 2026-09-08).** The statement's subject *name*
   is recorded as fact but is never load-bearing: model-signing
   documents `model_name` as informative — signer-chosen, excluded
   from manifest equality, changeable without invalidating signatures
   (`manifest.py:435-460` at `089602d`) — so a name comparison binds
   nothing. What binds:
   - **Who signed.** The certificate identity must satisfy an
     identity constraint. A non-public trust root (`tufMirror` /
     `staticBundle`) is an implicit constraint — only that PKI's
     identities can produce a validating chain. Under `mode: public`
     with `identities: []`, nothing constrains the signer, and
     `verified` is unattainable by design; the outcome
     `signature-valid-unconstrained` records that a real, logged
     signature exists without pretending it binds.
   - **What was signed.** When the workload declares a content digest
     (`model.k8saibom.dev/digest`), it must equal the manifest root
     digest — the format's content-bound, equality-significant field.
4. On full success — chain valid, Rekor inclusion proven, identity
   constraint satisfied, no declared binding contradicted:
   `SignatureResult{Status: verified, Identity, RekorEntry,
   Timestamp}`; the model component's confidence is emitted as
   `verified`. Contradiction precedence (amended 2026-09-09, same
   review thread): **digest over name**. A declared-digest mismatch
   always blocks — it contradicts the strong binding. A subject-name
   mismatch blocks only when no digest binding is available: the
   format permits renaming without re-signing, so against a satisfied
   digest match the name disagreement is a permitted publisher action,
   recorded as fact and not treated as a failure. Symmetric to the
   original amendment: a name match is never sufficient, and a name
   mismatch is never authoritative against stronger evidence.

**Why the name gate was removed.** The original step 3 gated
`verified` on the subject name matching the declared identity, and §6
claimed a hostile annotation "can never upgrade its own confidence
(subject match is required)." External review (#55) showed that claim
false under default configuration: sign any directory *named* to
match, keylessly, with any identity the public Fulcio will issue;
host the bundle at any HTTPS URL; declare both annotations. Chain
validates, Rekor holds, name matches — `verified`, from inputs
entirely under the claimant's control. The generalization goes
further than the reviewed flaw: with no node access (non-goal 2),
*every* input — reference, bundle, subject name, even a declared
digest — originates in the workload spec. The signer's identity and
the transparency log are the only elements outside the claimant's
control, so the identity constraint is the only thing that can make
`verified` mean something, and the design now requires it.

Bundle sources are pluggable behind one interface. v1.5.0 ships one
source: the annotation reference (HTTPS URL or inline base64). OCI
referrers on model-as-OCI-artifact is the expected second source,
deliberately deferred until a consumer needs it — reviewers should say
if AICR's model distribution wants it sooner.

### 4. Outcome taxonomy (facts, never failures)

| Situation | SignatureStatus | Recorded outcome fact |
|---|---|---|
| No signature reference found | `unsigned` | — (unchanged from v1.4.0) |
| Reference found, verification disabled | `claimed` | — (unchanged) |
| Chain + Rekor + identity constraint satisfied + no binding contradicted | `verified` | `verified` + identity, Rekor entry, timestamp |
| Chain + Rekor valid, but no identity constraint configured (public root, empty `identities`) | `claimed` | `signature-valid-unconstrained` |
| Chain/signature invalid | `claimed` | `failed: <reason>` |
| Valid chain, identity pattern mismatch | `claimed` | `identity-mismatch` |
| Subject name ≠ declared identity, no digest binding declared | `claimed` | `subject-name-mismatch` |
| Subject name ≠ declared identity, declared digest matches (+ identity constraint satisfied) | `verified` | `verified` + `subject-name-mismatch` recorded as fact (rename is format-permitted) |
| Declared digest ≠ manifest root digest | `claimed` | `digest-mismatch` (always blocks) |
| Fetch/Rekor/TUF unreachable, deadline hit | `claimed` | `error: <class>` (retry next resync) |

Consistent with the project's degradation rule: **no verification
outcome ever fails a reconcile, and enabling verification never makes
inventory worse than v1.4.0** — the floor is `claimed`, exactly what
v1 emits today. Failed and mismatch outcomes additionally emit a
warning Event and a metric increment; they are observable, not
judged.

### 5. Performance budget

The measured envelope (1–2 mCPU / ~61 Mi steady at 1,001 workloads,
dual-sampled, README "Performance footprint") is a qualified property
downstream; verification must not move it in the steady state.

- Results cached by `(signatureRef digest, trust-root epoch)` with
  `cacheTTL`; identical claims across workloads share one entry;
  in-flight deduplication (singleflight) bounds thundering herds.
- All network I/O under `perClaimTimeout`, nested inside the existing
  60s reconcile deadline (the sink-deadline pattern).
- Steady state approaches zero added cost: a claim re-verifies only on
  reference change, TTL expiry, or trust-root rotation.
- Metrics: `aibom_verification_attempts_total{outcome}`,
  `aibom_verification_duration_seconds`,
  `aibom_verification_cache_hits_total`.
- The measurement methodology from the perf re-baseline reruns with
  verification enabled before release; numbers go in the README with
  version + environment + sampling labels, per current policy.

### 6. Security considerations

- The verifier fetches remote content on a path influenced by
  workload annotations (untrusted input). Constraints: HTTPS only,
  response size cap (1 MiB), no redirects across hosts, no
  credentials in v1.5.0 (public references only; private-registry
  auth is future work with its own review), per-claim timeout.
- A hostile annotation can therefore cause at most: one bounded fetch
  per TTL per unique reference, and a recorded outcome fact. It can
  never upgrade its own confidence, because `verified` requires the
  signer to satisfy an operator-configured identity constraint (or a
  non-public trust root) — precisely the input a workload author does
  not control. Every other element of the chain originates in the
  workload spec and is treated as claim, not proof. *(Amended
  2026-09-08: this bullet previously relied on subject-name matching;
  external review showed the name is informative in the format and
  the original claim was false under default configuration.)*
- Trust-root updates: TUF refresh failures fall back to the last
  cached root (logged); the embedded public root bounds cold-start.

### 7. Testing

- Unit: sigstore-go test fixtures for every row of the outcome table.
- CI e2e: sign a fixture model manifest keylessly in the workflow
  (staging Sigstore), verify end-to-end in kind — the release
  pipeline already has the identity to do this.
- The logged e2e debt (non-default-config matrix leg: strict
  readiness + sinks under real RBAC) is folded into this release's
  matrix, since verification adds a third non-default configuration.
- GKE release verification gains: enable verification, one signed
  fixture → `verified`; one tampered bundle → `claimed` +
  `failed` fact; disabled-mode regression against v1.4.0 behavior.

### 8. Rollout and compatibility

- Additive only: new optional config section, new status/BOM fields,
  no CRD storage change, `v1alpha1`/`v1beta1` both unaffected in
  shape. MINOR (v1.5.0).
- Default off. AICR opts in via values when their qualification is
  ready (the `readiness.strictConfig` precedent), pointing
  `trustRoot.mode=tufMirror` at their self-hosted Sigstore — the
  first real exercise of configurable trust roots.
- Retires schema-divergences entry D-001 and the "v1 MUST NOT emit
  verified" rule (the comment block in signature.go updates to point
  here).

## Open questions for reviewers

1. **Bundle source priority:** is the annotation-URL source the right
   first source, or do real model-distribution pipelines (OCI
   registries, hub-hosted signatures) need OCI referrers in v1.5.0?
   Consumers of model-signing in production, please say how your
   signatures actually travel.
2. **Identity defaults:** now sharper post-amendment — since
   `verified` is unattainable without an identity constraint (or
   non-public root), should the chart ship suggested patterns for
   common signers to make the tier reachable out of the box, or is
   forcing the operator to state whom they trust the safer default
   for a facts-only project? (Current position: force the
   statement.) Related: model-transparency#659 asks the format's
   maintainers whether any in-format identity binding is intended;
   the answer may refine this.
3. **Downstream surfacing:** should verification outcomes be consumable
   by distribution health checks (AICR's is the first known case), or
   only from the BOM document itself?

## Sequencing

1. This document reviewed publicly (PR; AICR maintainers tagged).
2. Nested module scaffold + `RekorVerifier` against the frozen
   interface; outcome table under unit test.
3. Config plumbing + status/BOM emission; e2e legs; perf rerun.
4. v1.5.0 rc → GKE verification → release. Companion `kubectl aibom`
   plugin (view / summary / verify) ships alongside so the verified
   tier is demonstrable in one command; its design is a short
   appendix-level note, not part of this document's review scope.
