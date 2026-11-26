# Changelog

All notable changes to k8s-aibom are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[Semantic Versioning](https://semver.org/) (see `VERSIONING.md`).

## [Unreleased]

### One model component per name (v1.8.0)

- **A model named by several sources is one `machine-learning-model`
  component** (#362). A model found in both `--model` and
  `HF_MODEL_ID` gave two components that differed only in evidence;
  Dependency-Track kept one and dropped the other's properties. The
  component now carries every source: the first-found locator and source
  stay in `k8s-aibom:evidence:locator` / `:source`, each further one is
  `k8s-aibom:evidence:locator:<i>` / `:source:<i>`, and each source's
  confidence is `k8s-aibom:evidence:confidence[:<i>]`. The component's
  `k8s-aibom:confidence` is the highest tier among its sources, and the
  identity properties of every source are kept, including
  `k8s-aibom:identity:servedModelName` (#411), so the summary's
  `servedName` survives the merge; where sources disagree, the
  highest-tier source's value wins. Only top-level models
  are merged; other components and models named once are byte-identical.
  The document's aggregate confidence is unchanged.
- **API contract (behaviour, not schema): `status.summary.models` has
  one entry per model.** `Source` and `Confidence` come from the
  highest-tier source (the first-found one on a tie). The `Models`
  godoc changed in both served versions:

  ```diff
  -// Models is the set of declared/inferred model identities. Multiple
  -// entries are emitted when different evidence sources name the same
  -// model with different confidence (e.g., --model arg + HF_MODEL_ID
  -// env var) so auditors see both signals.
  +// Models is the set of declared/inferred model identities, one entry
  +// per model. A model named by several evidence sources (e.g., --model
  +// arg + HF_MODEL_ID env var) is one entry whose Source and Confidence
  +// come from the highest-tier source (verified > declared > inferred),
  +// the first-found one on a tie. Every source and its locator stay on
  +// the model's component in the BOM document.
  ```

  CRD descriptions regenerated in both charts. **Note for downstream
  readers:** a workload whose model was listed twice (arg + env var) now
  has one summary entry; read the document for every source.

### Added

- **Model identity as a metrics dimension** (#411, Design 011). Two info
  series on the existing `/metrics` endpoint: `aibom_workload_info` (one
  per workload per model: namespace, kind, name, category, runtime and
  version, model, model confidence, served model) and `aibom_pod_info`
  (one per owned pod per model), always `1`, refreshed on every
  reconcile including the unchanged-input fast path, and removed when
  the AIBOM is deleted, including by owner garbage collection. They let
  cAdvisor, DCGM, TPU and cost-allocation series, all keyed by pod, be
  read per model with one PromQL join. The inference scraper records
  vLLM's `--served-model-name` alias as `k8s-aibom:identity:servedModelName`
  on the container's model components and as `servedName` on the status
  summary (additive, both served API versions), so the identity exposed
  equals the `model_name` on vLLM's own metrics. Reference recording
  rules are in `grafana/model-attribution-rules.yaml`; the chart can
  install them as a PrometheusRule (`metrics.rules.enabled`) and a
  per-model Grafana dashboard as a ConfigMap
  (`metrics.dashboard.enabled`), both off by default. See
  `docs/observability/per-model-utilisation.md`. Documents without the
  flag are byte-identical.

- Add `kubectl aibom --version` to report the plugin's build version (#403).

### Removed

- The Cloud Shell tutorial (`.cloudshell/`), which was pinned to v1.0.0 and
  predated the Helm OCI install, and its "Cloud Shell tutorial" option in the
  bug report template (#404).

### Fixed

- **NIMService model locators keep their `spec.env` index past a
  malformed entry.** A `spec.env` entry that was not an object or did
  not decode was recorded and dropped from the list, so every later
  entry moved up one place and a model claimed from it was located at
  the wrong index (`spec.env[0](NIM_MODEL_NAME)` for the entry at
  `spec.env[1]`). The entry is now left in place with no stated value,
  as `spec.args` already does (#430).

- **The controller no longer risks a 30-second hang on shutdown.**
  controller-runtime v0.24's default priority queue can deadlock while a
  controller stops (kubernetes-sigs/controller-runtime#3540, fixed in
  v0.25.0), which delays process exit until the manager's grace period
  on a rollout or node drain. Controllers use the classic workqueue until
  the dependency is bumped (#417). Reconcile behaviour is unchanged; only
  the queue's start-up ordering of the initial list differs. The same
  deadlock made the envtest suite fail intermittently in CI.

- **Repeated pod events on an unchanged workload refresh AIBOM status at
  most once every five minutes** (#409). In a fake-client test with one
  vLLM Deployment reconciled every 30 seconds for eight simulated minutes,
  status writes fell from 17 to 2, including the initial write. Inventory,
  generation and extraction-error updates are still written immediately.

- Show namespace-specific guidance when `kubectl aibom summary` finds no AIBOMs (#403).

- **The release workflow fails where a chart push fails.** `helm push`
  was piped through `tee` without `pipefail`, so a registry error left
  the step green with an empty digest and the release stopped one step
  later with an unrelated-looking attestation error (the first v1.7.0
  attempt). Chart pushes now go through `hack/release/push-chart.sh`:
  up to four attempts, and a non-zero exit when the push fails or prints
  no digest. The release dry-run exercises the script's paths against a
  stub. No change to published artefacts.

- **`aibom_workloads_total` is now set (#398).** The gauge was registered
  but never written, so it had no series. The leader now recounts the
  AIBOMs in its cache every 15 seconds, by `category` and `runtime`
  (`unknown` when the status has no summary yet, `none` when no runtime
  was detected), so the value follows adds, deletes and restarts. The
  example Grafana dashboard gains a "Tracked Workloads (by Category)"
  panel.

## [1.7.0] - 2026-11-24

### Document shape (one transition, v1.7.0)

Six changes to the emitted CycloneDX document land together (#132,
#328, #363, #375), so downstream readers see one byte-shape transition.
Byte-identity with documents from v1.4.0 through v1.6.x ends here;
determinism within a release is unchanged (identical inputs still
produce identical bytes), and the `AIBOM` status summary fields and the
API schema do not change.

- **`metadata.lifecycles` is `[{"phase": "operations"}]` on every
  document.** Every k8s-aibom document is an as-deployed inventory
  observed from the Kubernetes API; the CycloneDX maintainers' guidance
  on spec#990 is that this context is a lifecycle on the metadata, not a
  perspective or a property. As-trained context is deliberately not
  emitted (`docs/schema-divergences.md`).

- **Container components carry a `pkg:oci` purl**:
  `pkg:oci/<name>@sha256:<digest>?repository_url=<repository>&tag=<tag>`
  in the purl specification's canonical form (name lowercased,
  qualifiers percent-encoded and sorted). The version is the resolved
  digest; a tag-only image gets no version rather than a fabricated one,
  and `repository_url` is the repository as the owner wrote it. The
  observed reference and tag stay as properties.

- **Properties are named under the `k8s-aibom` namespace registered in
  the CycloneDX Property Taxonomy**, with the taxonomy's colon grammar:
  `aibom.workload.kind` → `k8s-aibom:workload:kind`,
  `aibom.confidence` → `k8s-aibom:confidence`,
  `runtime.name` → `k8s-aibom:runtime:name`,
  `image.reference` → `k8s-aibom:image:reference`,
  `signature.status` → `k8s-aibom:security:signing:status`, and so on
  for every property; the full mapping is in
  `docs/cyclonedx-property-taxonomy.md`. Scrapers keep their internal
  keys; the builder maps every key through one function at emission.

  **Upgrade note for consumers that read property names** (AICR's
  health check, the production-stack and KubeAI integrations,
  Dependency-Track policies, custom relays): switch to the new names
  when you move to v1.7.0; documents written before the upgrade keep
  the old names until the workload is next reconciled. `kubectl aibom`
  v1.7.0 reads both spellings, so a newer plugin still reads documents
  from an older controller.

- **A verified model signature raises the model component's confidence
  to `verified`** (#328, Design 002 §4). Previously
  `k8s-aibom:security:signing:status` became `verified` while the
  component's `k8s-aibom:confidence` stayed `declared`. Only a signature
  verified against an operator trust root with a signer-identity
  constraint upgrades; an unconstrained valid signature still leaves
  confidence unchanged and records `signature-valid-unconstrained`.
  `status.summary.models[].confidence` follows the component, and the
  aggregate tier order is `verified` > `declared` > `inferred`, lowest
  tier winning. Bytes change only for workloads with a verified
  signature.

- **Every document has a `dependencies` array** (#363): one entry for
  `metadata.component` whose `dependsOn` lists the `bom-ref` of every
  top-level component in document order (runtimes, container images,
  models, data, remote APIs). Nested components and edges between
  components are not emitted, since nothing observed says that one
  component depends on another. A document with no components still
  carries the workload entry, without `dependsOn`. Dependency-Track
  now shows the workload's graph instead of logging `The dependency
  graph has 0 entries` on every upload, and GUAC gets the
  workload-to-component edges from the document. Bytes change for
  every document.

- **Every document has a `serialNumber`** (#375): `urn:uuid:` followed
  by the version 5 UUID of the workload's UID in the k8s-aibom namespace
  UUID `c8b031ec-958e-5f54-93dd-f9b3da80bfd0` (itself the version 5 UUID
  of `https://github.com/GoogleCloudPlatform/k8s-aibom` in the RFC 4122
  URL namespace). The same workload keeps the same serial across
  reconciles and controller restarts, so another document can BOM-Link
  to ours (`urn:cdx:<serial>/1#<bom-ref>`) and Dependency-Track or GUAC
  can key documents by serial. `version` stays `1`; the serial names the
  workload's document, `status.inputHash` says what changed in it.
  Determinism is unchanged: the serial is a pure function of the UID. A
  workload that is deleted and recreated gets a new UID and so a new
  serial. Bytes change for every document.

### API contract

- **`spec.discovery.workloadKinds` is declared in `v1alpha1` as well**
  (#343). The field was `v1beta1`-only in v1.6.0, so with conversion
  `None` an apply or patch through `v1alpha1` silently removed an
  allowlist the config already had. Both served versions are
  schema-identical again. Upgrade note: a `v1alpha1` apply no longer
  drops the allowlist, and a `v1alpha1` manifest may now set it.

- **Two field descriptions now say what the controller does** (no
  behaviour change, descriptions regenerated in both served versions).
  `spec.discovery.namespaceSelector`: an explicit empty selector (`{}`)
  matches every namespace, which the loader has always honoured on
  purpose; only an absent selector falls back to the opt-in label.
  `spec.discovery.inferenceRuntimeImagePatterns`: setting any pattern
  replaces the compiled-in list rather than adding to it, and
  `status.effectiveInferenceRuntimeImagePatterns` shows the list in
  force. The contributor guide and scraper-heuristics page carry the
  same warning.

### Added

- **The chart prints what to do next.** `helm install` and `helm upgrade`
  end with a NOTES section (#401, #406): the namespace opt-in label,
  `kubectl get aibom -A` and `kubectl aibom summary -A`, a pointer to
  the `AIBOMControllerConfig` named `default` and the chart's `config.*`
  values, and the reminder that `rbac.sinkSecretAccess` is off by
  default.

- **InferencePool membership as a routing fact** (#137, Design 007 §2,
  Part 2a). An inference workload whose declared Pod template labels
  match all of `spec.selector.matchLabels` of a Gateway API Inference
  Extension `InferencePool` (`inference.networking.k8s.io/v1` only) in
  the same namespace records the pool on its document's root
  `metadata.component`: `k8s-aibom:routing:inferencePool:<i>` =
  `<namespace>/<name>`, plus `…:<i>:endpointPicker` when the pool sets
  `spec.endpointPickerRef.name`. Pools are deduplicated, sorted and
  numbered from zero. Only Pod templates the scrapers already read
  count (including DisaggregatedSet roles and Dynamo components); live
  Pods and the workload's own labels never do, and a match attributes
  no pod, model, digest or traffic to the workload. The pool watch is
  started and stopped by the same CRD discovery as third-party kinds
  (#226), so a CRD installed later needs no restart. A pool change
  rebuilds the affected documents; while the pool view is unavailable
  (absent at startup, not Ready, or list/watch failing) no new facts are
  added and published ones are kept. A confirmed removal of a previously
  observed CRD clears stale pool facts; temporary availability changes
  alone re-emit nothing. Documents that match no pool are byte-identical
  to before. Known limitation: after a controller restart, an AIBOM
  whose document is external or truncated (over the inline threshold)
  and may carry pool facts is left unchanged while the pool view is
  unavailable, rather than risk dropping them; see
  docs/external-crd-versions.md. **Upgrade note:** the ClusterRole
  gains one read-only rule, `get`/`list`/`watch` on
  `inference.networking.k8s.io/inferencepools`. Helm installs get it;
  custom RBAC must add it, otherwise the pool list is forbidden, the
  controller logs the 403, and no pool membership is recorded (all
  other facts are unaffected).

- **Third-party CRDs are discovered, watched and retired without a
  controller restart** (#226, Design 004 amendment). The supervisor now
  runs on the leader whether or not any third-party CRD exists, watches
  `CustomResourceDefinition` metadata, and reads each of the six
  candidate CRDs (KServe `InferenceService`, Dynamo
  `DynamoGraphDeployment` and `DynamoComponentDeployment`, `NIMService`,
  `LeaderWorkerSet`, `DisaggregatedSet`) by name.

  *Late install.* A kind starts once its CRD is `Established` and serves
  the version k8s-aibom reads, so installing an operator after k8s-aibom
  (Dynamo after the controller, for example) no longer needs `kubectl
  rollout restart`. The first sync of such a kind emits a new Normal
  `WatchStarted` event naming the CRD; kinds whose CRD already existed
  at startup (even if it was still establishing) and allowlist
  re-enables keep `WatchRecovered`. `spec.discovery.workloadKinds` still
  wins: a CRD installed while its kind is excluded is disabled
  (`WatchDisabled`) and not watched, and a kind whose CRD is not
  installed gets no health entry, gauge series or event.

  *Lifecycle after a watch started.* Only a full read of the CRD by name
  decides; an informer event, a read error, a RESTMapper error or a
  failing watch never does. A CRD that stops serving the version
  k8s-aibom reads (or loses `Established`) stops its watch and reports
  the kind Degraded with the reason and one `WatchUnhealthy`; its AIBOMs
  are kept and its children stay rolled up, and when it serves again
  the watch restarts with `WatchRecovered`. A deleted CRD stops the
  watch, drops the kind from the roll-up owners and deletes its AIBOMs
  once, with one Normal `WatchDisabled` event (message names `reason:
  crd-removed`; an allowlist exclusion now names `reason: config`);
  workloads it had absorbed are reported on their own again. A CRD
  re-created under a new UID retires the old generation first (its
  sweep finishes before the new watch starts), then starts the new one
  with `WatchStarted`. `aibom_watch_healthy` stays 0 for a removed kind;
  nothing is Degraded.

  *Discovery health.* One `CRDDiscovery` pseudo-kind reports metadata
  informer LIST/WATCH/sync failures and candidate CRD read failures
  through Degraded status, the existing events and the
  `aibom_watch_healthy` gauge. It clears only when every outstanding
  discovery failure recovers, without stopping existing watches.

  **Upgrade note:** the ClusterRole gains one read-only rule,
  `get`/`list`/`watch` on `apiextensions.k8s.io/customresourcedefinitions`.
  Helm installs get it; custom RBAC must add it, otherwise CRD reads are
  forbidden and no third-party kind starts (the controller logs the 403
  and keeps retrying; apps/v1 and batch kinds are unaffected).

- **The controller says when nothing is opted in** (#261). `AIBOMControllerConfig`
  gains a `NamespacesOptedIn` condition: `False` with the exact opt-in command
  in its message while no namespace matches
  `spec.discovery.namespaceSelector`, `True` with the count once one does,
  re-evaluated on namespace create, delete and label changes. A Normal event
  fires on each transition, `aibom_namespaces_opted_in` exposes the count,
  and `kubectl aibom summary` with zero rows prints the same hint on stderr.
  Informational only: Ready, Degraded and readiness are unchanged, so a
  distribution that installs k8s-aibom by default keeps its health check
  while the cluster itself shows the one missing step.

- **Effective runtime patterns in `AIBOMControllerConfig.status`**
  (#106). `status.effectiveInferenceRuntimeImagePatterns` lists the
  runtime-detection patterns the controller is matching images
  against, in match order: the compiled-in defaults when
  `spec.discovery.inferenceRuntimeImagePatterns` is empty, the spec
  list once it has loaded, and the last-known-good list while the spec
  is invalid. An operator whose workload produced no AIBOM can now
  check its image against the list without reading the controller
  binary. Added to both `v1alpha1` and `v1beta1`; status only, so
  emitted documents are unchanged.

- **`NamespaceObserved` event on opted-in namespaces** (#106). After
  its first pass over an opted-in namespace the controller emits one
  Normal event on that namespace, stored in it so `kubectl get events
  -n <ns>` shows it, with how many workloads were considered, how many
  matched an AI workload signal and how many matched none. A quiet
  namespace no longer looks the same as a controller that never ran.
  The event waits until no new workload has been considered for 10s
  (at most 60s from the first). Rolled-up workloads and kinds excluded
  by `spec.discovery.workloadKinds` are not counted. The count is kept
  in memory: a restart announces each namespace again, and so does
  opting a namespace out and back in. No new RBAC (events
  create/patch is already granted); emitted documents are unchanged.

- **The vector-database scraper detects Vespa** (#286). Workloads running
  `vespaengine/vespa` (or `vespaengine/vespa-generic-intel-x86_64`, the
  same server built for older x86_64 hardware) get category
  `vector-database` and a `vespa` component. The pattern ends at the
  repository name, so the build, dev, system-test, pipeline, minimal and
  tutorial images in the `vespaengine` namespace are not matched.

- **TensorFlow Serving runtime pattern** (#189). `tensorflow/serving`
  (GA, rc, latest, nightly and `-gpu` tags, bare and digest forms)
  attributes as runtime `tensorflow-serving`. Publisher-anchored at the
  image-name boundary with the serving tags spelled out, so the `-devel`
  build images in the same repository, other `tensorflow/*` images and
  other registries stay unmatched.

- **TorchServe runtime pattern** (#191). `pytorch/torchserve` (CPU,
  GPU, latest and early CUDA runtime tags, bare and digest forms)
  attributes as runtime `torchserve`. Publisher-anchored at the
  image-name boundary with the serving tags spelled out, so the
  `-cpp-dev` build images in the same repository, the
  `pytorch/torchserve-kfs` and `pytorch/torchserve-nightly` repositories,
  other `pytorch/*` images and other registries stay unmatched.

- **KServe predictive server runtime patterns** (#196).
  `kserve/sklearnserver`, `kserve/xgbserver`, `kserve/lgbserver`,
  `kserve/paddleserver` and `kserve/pmmlserver` run as a plain
  Deployment attribute as runtimes `sklearn`, `xgboost`, `lightgbm`,
  `paddle` and `pmml`, the names their ClusterServingRuntimes declare,
  matching what the KServe scraper reports for an InferenceService.
  Publisher-anchored at the image-name boundary; the multi-framework
  `kserve/predictiveserver`, other `kserve/*` images and other
  registries stay unmatched.

- **Whisper ASR Webservice runtime pattern** (#197).
  `onerahmet/openai-whisper-asr-webservice` (release, `-gpu`, latest
  and debug tags, bare and digest forms) attributes as runtime
  `whisper-asr-webservice`. Publisher-anchored at the image-name
  boundary, so other users' whisper images and other registries stay
  unmatched.

- **Private-CA HTTPS for bearer-token webhook sinks** (#96): optional
  `webhook.auth.bearerToken.caSecretRef` in both API versions reuses the
  existing namespaced Secret lookup and mTLS CA trust handling. Omitting
  it keeps system roots; certificate and hostname verification and the
  rejection of bearer credentials over plain HTTP are unchanged. The
  e2e matrix's bearer-delivery leg now uses TLS with a private CA.

- **Dependency-Track and GUAC walkthroughs against the v1.7 document**
  (#313). `docs/integrations/dependency-track.md` now has a tested
  walkthrough (Dependency-Track 4.14.6) and a list of what
  Dependency-Track drops; the new `docs/integrations/guac.md` does the
  same for GUAC v1.1.0. Both use documents from a kind cluster; one of
  them is kept as `docs/integrations/fixtures/llama-cpp-chat.cdx.json`
  and a unit test validates it against the CycloneDX 1.6 schema.
  Corrections to the Dependency-Track page: the relay's API key also
  needs `PROJECT_CREATION_UPLOAD`; policies cannot match `k8s-aibom:`
  properties or the classifier in 4.x; an upload replaces the
  component list rather than showing a diff; the webhook sink refuses
  a relay behind a ClusterIP Service; and adding a sink does not send
  documents that already exist. The relay example now rejects anything
  but a POST of a document with a workload name, and passes
  Dependency-Track's answer back. Documentation and a test only.

- **`GOVERNANCE.md` and `MAINTAINERS.md`**: roles (contributor, reviewer,
  maintainer) with entry criteria, the decision process (lazy consensus,
  design documents, votes), security and conduct handling, and how the
  document changes. Reviewer nominations are open. Documentation only.

- **README names Project AIBOM as the training-side counterpart** (Red Hat
  Research / Mass Open Cloud): reproducibility records for training and
  inference runs, next to k8s-aibom's serving-side inventory; interop
  (CycloneDX export, distinct short name, cross-links) agreed on their
  tracker. Documentation only.

### Security

- **Go 1.26.11** (GO-2026-6859): the `net/http` HTTP/1 server could
  panic on a header race when `MaxBytesReader` or `MaxBytesHandler`
  limits were exceeded. The controller and plugin are rebuilt on the
  fixed toolchain; `govulncheck` is clean again. No change in behaviour.

### Fixed

- **A model env var that refers to another env var names the model, not
  the reference.** `HF_MODEL_ID` (or `MODEL_NAME`, `NIM_MODEL_NAME`,
  `NIM_SERVED_MODEL_NAME`) set to `$(MODEL_ID)` or `$(ORG)/gemma-2-2b`
  produced a model component named by the reference text, which also
  reached `status.summary.models` and `kubectl aibom find --model`. The
  kubelet expands such references from the entries before them, so the
  values in a container's `env` or a NIMService's `spec.env` are now
  read that way, as model args are since #391. A reference whose value
  the spec does not state (`valueFrom`, `envFrom`, a Service link, a
  later entry, undefined) names no model. A workload whose only signal
  was such a reference is now unresolved and gets no AIBOM, so an
  existing one is removed. `identity.envVarName` lists the referenced
  names after the variable's own name. Values without a `$` are read as
  before.

- **A vector database, agent framework, training or evaluation image
  written with `docker.io/` is recognized.** The `vectordb.spec`,
  `agent.spec`, `training.spec` and `eval.spec` image patterns are
  anchored at the short Docker Hub name (`^qdrant/qdrant`,
  `^langflowai/langflow`, `^pytorch/pytorch`, `^eleutherai/lm-eval`,
  ...) and were matched against the image as written, so
  `docker.io/qdrant/qdrant:v1.12.0` (or `index.docker.io/...`) matched
  nothing. With no other signal the workload got no category and no
  AIBOM. These patterns now also try the Docker Hub short form, as
  runtime detection does since #399. No other registry prefix is
  stripped, and the image reference is recorded as written.

- **A Docker Hub image written with `docker.io/` gets its runtime
  attributed (#399).** Runtime detection matched the image reference as
  written against patterns anchored at the short Docker Hub name, so
  `docker.io/vllm/vllm-openai:v0.6.3` (or `index.docker.io/...`) matched
  nothing while `vllm/vllm-openai:v0.6.3` matched `vllm`. Digest
  resolution already treated the two as the same image, so the BOM
  showed the digest but no runtime, and a workload with no other signal
  got no AIBOM at all. The patterns are now also tried against the short
  form, with `docker.io/`, `index.docker.io/` and `library/` removed.
  No other registry prefix is stripped, and the image reference is
  recorded as written.

- **A webhook endpoint on an IPv6 loopback or link-local address is
  rejected at config load.** The SSRF check in the webhook sink's
  config validation compared the endpoint host with `[::1]`, but the
  host it reads has no brackets (`::1`), so `https://[::1]:8443/` passed,
  as did `[::]`, `[fe80::1]` and `[::ffff:127.0.0.1]`. The sink was
  built and reported ready, and every emission then failed at dial time,
  where the private-IP guard does block these addresses. IPv6 literals
  are now matched by address (loopback, unspecified, link-local), the
  IPv6 forms of the `127.*`, `0.0.0.0` and `169.254.*` checks already
  there, so the config is rejected with `ConfigInvalid` instead.
  `AIBOM_DISABLE_SSRF_CHECKS=true` still turns the check off.

- **Documentation matches the 1.7.0 tree.** The Dependency-Track and
  GUAC fixture carries the v1.7.0 `serialNumber`; D-006 in
  `schema-divergences.md` says v1.7.0, not v1.8.0; `VERSIONING.md`
  records the one-time document-shape transition under the public
  contract it affects; the README no longer suggests
  `go install ...@latest` for the plugin (the root module's `replace`
  directive for the verifier breaks it; krew and the attested release
  binaries are the supported paths), no longer shows the webhook sink
  posting to GUAC (GUAC collects files, see the walkthrough), and notes
  that the sink refuses endpoints resolving to private addresses;
  `llms.txt` lists `find`; the overview deck's confidence examples and
  roadmap panel match the README; the Terraform example names the
  current chart version; Design 002 marks its verification metrics as
  deferred.

- **Two code comments no longer cite the retired D-001 rule.** #377
  retired schema-divergences entry D-001 ("v1 never emits `verified`")
  in the CRD descriptions and docs, but the comment on
  `ModelSummary.Signed` in `internal/controller/summary.go` still said
  the value was a NoopVerifier-derived
  `k8s-aibom:identity:confidence=claimed`, a property the builder never
  writes, and `NewKServeInferenceServiceScraper` still pointed at D-001
  for its nil-verifier default. The summary comment now names the
  `k8s-aibom:security:signing:status` property it reads and when it is
  `verified`. Comments only; no behaviour change.

- **No spurious `WatchUnhealthy` at startup on API servers without the
  WatchList feature gate.** client-go v0.36 enables `WatchListClient` by
  default and opens the first watch of an informer with
  `sendInitialEvents`; API servers without the server-side `WatchList`
  gate (the default through Kubernetes 1.33) reject it with 403 and
  client-go falls back to a plain list on its own, but the rejection
  reached the CRD discovery informer's error handler first and was
  reported as a discovery outage: one `WatchUnhealthy` event, a
  `Degraded` condition on `AIBOMControllerConfig`, and a `WatchRecovered`
  a second later, at every controller start on such clusters (and the
  same for the InferencePool view). Both reflectors now opt out of
  WatchList semantics and use list+watch on every server version; the
  lists involved are small. Affected: 1.7.0 pre-releases only (the
  discovery informer and the client-go bump both landed after 1.6.2);
  found by the weekly envtest matrix on 1.27 and 1.31.

- **A document is not emitted twice when two reconciles overlap the
  informer cache.** The dedup fast path compares the input hash with the
  one in the AIBOM status read through the cache; a reconcile that ran
  before the previous reconcile's status write had reached the cache saw
  the old hash and emitted the same document to the sinks a second time
  (a duplicate archive copy, never an overwrite). The reconciler now
  also keeps a per-process record of the last hash it emitted for each
  AIBOM, cleared on a sink failure so the retry contract holds and on
  deletion. The InferencePool view's fan-out on recovery made the race
  easy to hit; it has been possible since external sinks were
  introduced.

- **A model arg that refers to an env var names the model, not the
  reference** (#391). `--model=$(MODEL_ID)` with `MODEL_ID` set in the
  container's `env`, the form GKE's vLLM and TGI serving samples use,
  produced a `declared` model component named `$(MODEL_ID)`, which also
  reached `status.summary.models` and `kubectl aibom find --model`. The
  kubelet expands such references before the server starts, so args are
  now expanded from the container's `env` (or a NIMService's
  `spec.env`) the same way. A reference whose value the spec does not
  state (`valueFrom`, `envFrom`, a Service link, undefined) names no
  model. A workload whose only signal was such a reference, with no
  runtime image or other model claim, is now unresolved and gets no
  AIBOM, so an existing one is removed. Args without a `$` are read as
  before.

- **A failed `AIBOMControllerConfig` status write no longer repeats
  the config event.** The controller emits the transition event
  (`ConfigLoaded`, `ConfigInvalid`, `ConfigRecovered`, ...) before
  writing the CR status, but recorded the new state only after that
  write succeeded. When the write hit a conflict or failed, the retry
  emitted the same event again and counted the reload twice in
  `aibom_controller_config_reloads_total`. The state is now recorded
  as soon as the event is emitted.

- **AIBOM conditions keep their `lastTransitionTime` when their status
  does not change** (#231). A full rebuild stamped `Ready`, `Synced`,
  `SinkFailed` and `Stale` with the reconcile time every time, so while
  a sink failed and was retried every minute, `SinkFailed=True` always
  looked less than a minute old and "failing since" could not be read
  from the status. The full path now keeps the previous time for a
  condition whose status is the same, as the dedup fast path and the
  scrape/build failure path already did; a condition that flips still
  gets the reconcile time.

- **A container's digest no longer depends on the order pods are
  listed in** (#236). When a workload's replicas ran different digests
  (a moving tag such as `:latest`, or a rollout in progress), the
  digest came from whichever matching pod the informer cache listed
  first, which changes between calls. The document and its input hash
  could change on every reconcile and be re-sent to every sink. The
  digest the most pods report now wins, and a tie goes to the lowest
  digest. Workloads whose pods all run one digest, and digests pinned
  in the spec image, are unchanged.

- **Pod digests resolve for workloads whose selector uses
  `matchExpressions`.** Deployment, StatefulSet, DaemonSet and Job
  reconcilers listed the workload's pods by `selector.matchLabels`
  only, and listed nothing when `matchLabels` was empty, so a workload
  that selects its pods with `matchExpressions` alone never had its
  container digests resolved from pod status. Pods are now listed by
  the whole selector; controller ownership still decides which pods
  count.

- **Touching an annotation on `AIBOMControllerConfig` reloads it**
  (#304). `SecretKeyRef` documents an annotation touch as the way to make
  the controller re-read a rotated sink credential, but the config
  controller reacted only to `metadata.generation` changes, which
  annotations do not cause. A rotated token or key was picked up only by
  an unrelated reconcile (a namespace change, a restart). Annotation
  changes now reload the config; status-only updates are still ignored.
  The `SecretKeyRef` doc also says that re-applying an unchanged
  manifest is not a change.

- **`spec.logging.level` sets the controller's log level** (#279). The
  field has been a no-op since 1.0: the CRD accepted it and the chart
  rendered it (`config.logging.level`), but nothing read it, so the
  controller always logged at info. **On upgrade, a level that was set
  but ignored takes effect**; a cluster that set `debug` starts logging
  the controller's debug lines. The levels map as follows: `debug` enables
  the `V(1)` lines, `info` is the default, and `warn` and `error` quiet the
  info lines (`error` also quiets warnings). The logger runs on an atomic
  level that follows the active config snapshot, so a change to the CR
  takes effect without a restart, as the field's documentation says.
  Deleting the CR or running on compiled defaults returns to info. An
  explicit `--zap-log-level` flag keeps priority, and the controller logs
  once that the spec field is ignored. `spec.logging.format` is unchanged.

- **`spec.logging.format` selects the controller's log encoder** (#281).
  The field was accepted by the CRD and rendered by the chart
  (`config.logging.format`), but nothing applied it, so the controller
  always logged JSON. The chart now passes it to the controller as
  `--zap-encoder`, with `text` mapped to zap's `console` encoder. The
  encoder is fixed when the process starts, so the field is read at
  startup: a change takes effect when the pod restarts, for example on
  `helm upgrade`. Its documentation now says so, and no longer promises
  a notice when it changes, which nothing logged. With the default
  `json`, the controller logs as before.

- **`status.summary.models` no longer lists unresolved models** (#259).
  `ModelSummary` says its confidence is never `unresolved`, but
  `buildSummary` copied every `machine-learning-model` component, so an
  agent Deployment that passes `OPENAI_API_KEY` (or another API-key
  variable) through `valueFrom` got an `openai-api` entry with
  confidence `unresolved`, which `kubectl aibom summary`, `find` and
  `find --model` then showed. Unresolved models are now left out of the
  summary; the component stays in the document with its locator. An
  API key with a literal value is `inferred` and is still summarized.
  The document and input hash are unchanged, so an existing AIBOM keeps
  its stored summary until the workload's inputs next change (the dedup
  fast path does not rebuild the status).

- **`aibom_scraper_extraction_errors_total` keeps counting on the dedup
  fast path** (#325). The counter was raised only when a BOM was rebuilt.
  Extraction errors are not part of the input hash, so an unchanged
  workload that keeps producing the same errors takes the fast path from
  its second reconcile on: the counter rose once and then stayed flat,
  and the dashboard's `rate()` panel read zero while the same errors
  drove `Stale` to `True` (#223 fixed the `Stale` count, not the
  metric). The fast path now counts the errors too.

- **The `Stale` message for persistent extraction errors names the
  errors** (#327). `ExtractionErrorsPersistent` listed evidence sources
  only for `*scraper.ExtractionError`, which no scraper returns, so the
  message was always just `workload produced extraction errors on N
  consecutive reconciles`. The errors (for example `spec.env[0]: not an
  object (string); skipped`) are not logged or stored anywhere else.
  The message now lists the distinct error texts in scraper order, the
  first three (each cut to 120 characters) and then `and N more`. The
  reason, threshold and clearing are unchanged.

- **`SinkFailed` marks a webhook receiver's body `(truncated)` only
  when text was dropped** (#245). The marker was added to every
  response body over 256 bytes, counted before ANSI color codes are
  stripped, so a body shown in full (multi-byte UTF-8, or a short
  message wrapped in color codes) still read as cut off. A
  body of exactly 1024 bytes, the read limit, was marked too. The
  marker now means the read limit was passed or the text was capped
  at 256 characters.

- **The cleartext-credential config error names the real field**
  (#243). A webhook sink with `auth` and an `http://` endpoint is
  rejected at config load, and the `ConfigInvalid` message pointed at
  `spec.sinks[name=X].webhook.url`, a field that does not exist. It
  now names `spec.sinks[name=X].webhook.endpoint`. The check itself is
  unchanged.

- **`training.spec` no longer claims `PyTorchJob` and `RayJob`.**
  `HandlesKind` returned true for `kubeflow.org/PyTorchJob` and
  `ray.io/RayJob`, but no reconciler watches either kind, so the
  scraper never saw one; its fallback for them scraped the workload's
  pods with locators such as `spec.containers.image` that name no
  field of the object. `HandlesKind` now covers only `batch/Job` and
  `batch/CronJob`, the unreachable pod fallback is removed, and any
  other object type is an error, as in `eval.spec`, `agent.spec` and
  `vectordb.spec`. No output changes: Job and CronJob documents are
  scraped as before.

- **`MultiScraper` keeps extraction errors from scrapers that found
  nothing.** The scraper for Deployments, StatefulSets, DaemonSets,
  Jobs and CronJobs runs inference.spec, vectordb.spec, agent.spec,
  training.spec and eval.spec and merges the results it recognizes,
  but merged `Errors` only from scrapers with a resolved result. The
  `Scraper` contract says a field a scraper could not read goes on
  `Errors`; one recorded by a scraper whose result was unresolved was
  dropped with the rest of that result, so it never reached the
  `aibom_scraper_extraction_errors_total` metric or the
  `ExtractionErrorsPersistent` Stale condition. Errors from every
  scraper that ran are now kept, in scraper order. None of these five
  scrapers records extraction errors today, so no current output
  changes.

- **The per-document component cap now bounds the merged scraper
  result** (#273). `MaxComponentsPerDocument` was applied only inside
  `inference.spec` and the CRD scrapers. `agent.spec`, `vectordb.spec`,
  `training.spec` and `eval.spec` returned uncapped lists, and the
  `MultiScraper` appended them to a list `inference.spec` had already
  capped. So a Deployment whose containers each set `OPENAI_API_KEY`
  produced 512 components, or 356 with no runtime image, against a cap
  of 256. The merged result is now capped too: the first components in
  scraper order are kept, and the drop is counted in
  `aibom.truncation.applied` along with any drop a merged scraper
  recorded. Documents under the cap are unchanged.

- **Agent and vector-database AIBOMs list the workload's containers**
  (#290). `inference.spec` is the only scraper that builds container
  components, and `MultiScraper` dropped its whole result when it found
  no runtime or model, so a Qdrant or Langflow Deployment's BOM had no
  image reference and no digest. Its container components are now kept
  when another scraper recognizes the workload; nothing else from that
  result is, so the confidence and category are unchanged and a
  workload no scraper recognizes still gets no AIBOM. Existing agent and
  vector-database AIBOMs regenerate once. Jobs and CronJobs are not read
  by `inference.spec`, so training and evaluation BOMs are unchanged.

- **KServe InferenceService BOMs are capped at 256 components** (#288).
  The KServe scraper is called without `MultiScraper` and was the only
  CR scraper that did not apply `MaxComponentsPerDocument`, so each
  `model.k8saibom.dev/*` annotation added a component with no bound and
  no `aibom.truncation.applied` property. It now caps and records the
  drop the way the LWS, DisaggregatedSet, Dynamo and NIMService scrapers
  do.

- **NIMService `spec.args` with a non-string element is no longer
  dropped silently** (#269). One such element made the scraper ignore
  every arg, `--model` included, without recording anything, and the
  model then came from the image path as `inferred`. The element is now
  recorded as `spec.args[i]: not a string (<type>); skipped` and the
  other args are read with their original indices, the same contract as
  `spec.env`. A `spec.args` or `spec.env` that is not a list is recorded
  too instead of being ignored.

- **An API key set from a Secret is marked as a remote API dependency**
  (#320). `agent.spec` gave the `openai-api` / `anthropic-api` / …
  component `dependency.type: remote-api` only when the key was a
  literal value. A key set through `valueFrom`, which is how keys are
  normally loaded, got the component with no properties. Both forms now
  carry `dependency.type`; the `valueFrom` component stays `unresolved`.
  Documents for workloads that load an API key from a Secret or
  ConfigMap change once after upgrade.

- **A dataset mount is reported once when a Job matches both
  `training.spec` and `eval.spec`** (#232). Both scrapers report
  dataset mounts since #218, and the multi-scraper appended their
  results without deduplicating, so an lm-eval Job that logs to
  Weights & Biases (`WANDB_API_KEY`) listed each mount twice. Merged
  results are now deduplicated on type, name, version, evidence source
  and locator; a single scraper's result is unchanged.

- **A model mounted from an OCI image volume is named by its image
  reference** (#275). An `image` volume (KEP-4639) mounted at a model
  path produced a `data` component named after the pod-local volume
  name, with `volume.source: other`, so the image reference that
  identifies the model was missing from the BOM. The component is now
  named by `image.reference`, with `volume.source: image`. An image
  volume without a reference keeps the volume name.

- **`kubectl aibom` rejects an empty value for a filter or a scope flag**
  (#352). `--runtime`, `--model`, `--image`, `--digest` and `--signed`
  set to `""` (`--digest "$DIGEST"` with the variable unset) counted as
  no filter, so `find -A --digest ""` listed every AIBOM as running that
  digest. `-n`, `--namespace`, `--context` and `--kubeconfig` set to
  `""` counted as not given, so the command quietly used the
  kubeconfig's namespace, current context or default file, and
  `verify NAME -n ""` verified a same-named AIBOM in another namespace
  and exited 0. Both are now argument errors, reported before the
  kubeconfig is loaded, like `--digest sha256:` already was. Leaving a
  flag out still means no filter, or the kubeconfig default. Plugin
  only.

- **`kubectl aibom` reads `--A` as `-A` and treats `--` as the end of
  the flags** (#255, #365). The flag package accepts both spellings and
  the `--` convention, but the plugin's argument splitter knew only the
  one-dash `-A` was a boolean and read `--` as a flag taking a value, so
  each swallowed the next argument: `find --A --runtime vllm` failed with
  `flag needs an argument: -runtime`, `summary --A prod-jobs` listed
  every namespace instead of rejecting `prod-jobs`, `view -- NAME` and
  `verify -- NAME` failed with "requires exactly one AIBOM name", and
  `find -- vllm` listed every AIBOM unfiltered. Both spellings of `-A`
  and `--raw` now take no value, and every token after `--` is
  positional and goes through the usual checks.

- **`kubectl aibom find` and `summary` reject `-n` together with `-A`**
  (#239). With both, `-A` won and `-n` was dropped, so `find --runtime
  vllm -A -n prod-jobs` listed matches from every namespace and exited
  0. The combination is now an argument error, reported before the
  kubeconfig is loaded: `-n and -A cannot be used together (-n
  prod-jobs for one namespace, -A for all)`. Either flag alone behaves
  as before.

- **`kubectl aibom` rejects arguments a subcommand does not use**
  (#235). `kubectl aibom find vllm` listed every AIBOM unfiltered,
  `summary prod-jobs` summarized the current namespace instead of
  `prod-jobs`, `summary --runtime vllm` ignored the filter, and `view
  NAME -A` sent a cluster-scoped GET that came back 404. These are now
  argument errors, reported before the kubeconfig is loaded, with a
  hint (`filters are flags, e.g. --runtime vllm`). `-n`, `--kubeconfig`
  and `--context` still apply to every subcommand.

- **`kubectl aibom` accepts `--namespace` and `--all-namespaces`**
  (#267). Only `-n` and `-A` were defined, so kubectl's long spellings
  failed with `flag provided but not defined: -namespace`. They now set
  the same values and follow the same rules: `--namespace` with `-A` or
  `--all-namespaces` is an argument error, and `--all-namespaces` does
  not apply to `view` or `verify`.

- **`kubectl aibom find` no longer fails when one AIBOM's document
  cannot be read** (#265). Inline data that is not base64, not JSON, or
  not a string made the whole command exit 1 with that AIBOM's error and
  no table, even with no filter or only `--runtime`,
  `--model` or `--signed`, which never need the document. Such a row is
  now listed with IMAGES `unreadable` and handled like `no-document`:
  shown (not excluded) with the stderr warning when an image or digest
  filter cannot be evaluated for it. `view` and `verify` keep the error.

- **`kubectl aibom find --runtime` finds every runtime a workload runs**
  (#335). The filter compared only `status.summary.runtime.name`, which
  names the first runtime in the document. A pod running vLLM with a
  LiteLLM proxy beside it has summary runtime `litellm`, so
  `find --runtime vllm` printed `0 matches` for it. `--runtime` now also
  matches the runtime components of the inline document, and the
  RUNTIME column lists them, summary runtime first. When the document is
  not inline, only the summary runtime can be matched; a row whose
  summary names another runtime is left out and named in a stderr
  warning, printed after `0 matches` too. Plugin only; the controller and
  its documents are unchanged. `status.summary.runtime` itself stays a
  single runtime until #336.

- **`kubectl aibom find` lists each model and image once** (#241). The
  MODELS and IMAGES columns repeated a value once per place it was
  found: a LeaderWorkerSet whose leader and worker run the same image
  showed it twice, and the repeat pushed other images behind the `+N`
  suffix. A model named by both `--model` and an env var also showed
  twice, where `summary` shows it once. Both columns now list distinct
  values in first-seen order, and `+N` counts distinct values.
  Filtering is unchanged.

- **`kubectl aibom verify` states each model's signature once**
  (#249). A model named in two places (`--model` and an env var) has
  two `status.summary.models` entries, and `verify` printed
  `model X: signature unsigned` once per entry, so one model read as
  two. Identical lines are now printed once, in first-seen order; a
  model whose entries record different states still shows each. The
  digest check and the exit code are unchanged.

- **The chart's `config.discovery` comment describes `namespaceSelector`
  correctly** (#299). It said the selector applies in addition to the
  `aibom.k8saibom.dev/enabled=true` label opt-in; a selector replaces the
  label, so its `environment: production` example inventoried every
  production namespace, labelled or not, and dropped labelled
  namespaces outside production. The comment now says the selector
  replaces the opt-in, and the example keeps the label in
  `matchLabels`. The `metrics` comment's list of reconcile outcomes also
  gains `kind_not_allowed`. Comments only.

- **`make helm` installs Helm into `./bin`, at the CI version** (#308).
  The target ran `get-helm-3` without `HELM_INSTALL_DIR`, so it installed
  to `/usr/local/bin` (or failed there without root), then moved
  `./bin/helm` onto itself and exited 0 when either step failed, leaving
  no `./bin/helm` for `make install.yaml`. It now installs into `./bin`,
  fails when the install fails, and installs `HELM_VERSION` (v3.22.1,
  the version every workflow pins) instead of v3.13.0. Build tooling
  only.

- **Building from source shows a multi-platform build that pushes**
  (#317). The guide said `make image-multiarch` then `make docker-push`,
  but `image-multiarch` has no output flag. With the classic image store,
  the default builder refuses a multi-platform build, and a
  `docker-container` builder leaves the result in its build cache, so
  `make docker-push` had nothing to push or pushed an older image. Only
  the containerd image store keeps the image for `docker push`. The guide
  now runs `docker buildx build ... --push`, and says when the make
  targets work. Documentation only.

- **`hack/full-smoke-test.yaml` works on a fresh cluster again**
  (#316). It put every workload in `inference-test` without creating
  that namespace or opting it in, so `kubectl apply` failed, and a
  namespace created by hand without the opt-in label got no AIBOMs. Its
  ragas Job ran `ragas/ragas-eval`, which the anchored ragas pattern no
  longer matches. The file now creates the namespace with
  `aibom.k8saibom.dev/enabled: "true"` and runs
  `explodinggradients/ragas`, so all 15 workloads get an AIBOM. Test
  manifest only.

- **The Terraform README shows the `cluster_location` default** (#310).
  Its Variables table listed `cluster_name` and `cluster_location`
  together with no default; `cluster_location` defaults to
  `us-central1-c` in `variables.tf`, so leaving it out looks the cluster
  up there. The row is split and shows the default. Documentation only.

- **The AIBOM CRD no longer says models are never `verified`** (#377).
  The `status.summary.models[].signed` description, which `kubectl
  explain` shows, said "v1 never emits `verified`", and the `confidence`
  description listed only `declared` and `inferred`. With signature
  verification (Design 002, v1.5.0) a model's signature can be
  `verified`, and since #328 its confidence follows. Both descriptions
  now give `verified` and when it appears, and
  `docs/schema-divergences.md` marks D-001, the "never emits `verified`"
  entry, as retired by Design 002. Documentation only; no schema change.

- **Three CRD field descriptions match current behaviour** (#306).
  `status.summary.workload.category` said v1 always emits `inference`;
  the scrapers also emit `agent`, `training`, `evaluation` and
  `vector-database`. `spec.bomGeneration.inlineThresholdBytes` said an
  oversized BOM is truncated only when no external sink is configured;
  it is also truncated when every configured sink failed (as #301 fixed
  for `status.bomDocument`). `spec.discovery.workloadKinds` left
  DisaggregatedSet out of the third-party kinds whose watch stops when
  unlisted. Regenerated CRDs; no schema change.

- **`status.bomDocument` field docs match when a BOM is truncated**
  (#301). The `external` and `truncated` descriptions (API godoc and the
  CRD schema `kubectl explain` reads) said `truncated` means no external
  sink is configured. The controller also truncates when sinks are
  configured but none accepted the BOM this cycle, and says so in
  `truncationReason`. The descriptions now say `external` is set when a
  sink accepted the BOM and `truncated` when none did, naming both
  causes. Regenerated CRDs; no schema change.

- **The README and docs name properties as v1.7.0 documents emit them**
  (#360). Seven places still gave the name from before the taxonomy
  rename: `aibom.redaction.applied` in the README's sanitization
  guarantee and in the threat model, `dependency.type` in the scraper
  heuristics, and `dynamo.component.<name>.type`, `container.name` and
  `lws.role` (twice) in the external CRD tables. They now read
  `k8s-aibom:redaction:applied`, `k8s-aibom:dependency:type`,
  `k8s-aibom:dynamo:component:<name>:type`, `k8s-aibom:container:name`
  and `k8s-aibom:lws:role`. Documentation only.

- **The property taxonomy matches what the controller emits** (#321).
  `docs/cyclonedx-property-taxonomy.md` listed the workload category
  `vectordb` (it is `vector-database`) and the signing status `none`
  (no status is written without a signature reference, and absence reads
  as unsigned). It also gave no v1.7 name to `dependency.type`,
  `dataset.path`, `disaggregatedset.role` or `disaggregatedset.slices`,
  and its mapping table did not cover the kind-specific names. It now has
  `k8s-aibom:dependency`, `k8s-aibom:dataset` and
  `k8s-aibom:disaggregatedset`, and mapping rows for them and for
  `kserve.*`, `dynamo.*`, `nim.*` and `lws.*`. Documentation only.

- **The webhook sink protocol gives the `Content-Type` the sink sends**
  (#376). `docs/webhook-sink-protocol.md` said every delivery carries
  `Content-Type: application/vnd.cyclonedx+json; version=1.6`; the sink
  has always sent `application/json`, so a receiver built to the
  document and checking the media type refused every delivery. The
  request shape, the headers table and the stability notes now give
  `application/json`, with the CycloneDX version in the body's
  `specVersion`. The envelope test now checks the header names and the
  `Content-Type` value as the document writes them rather than against
  the constants, which is why the drift went unnoticed. No change to
  what the sink sends.

- **The webhook protocol doc names where the bearer token comes from**
  (#284). `docs/webhook-sink-protocol.md` said the `Authorization` token
  came from a `BearerTokenFile`, an internal setting that
  `AIBOMControllerConfig` cannot set. It now names
  `webhook.auth.bearerToken.secretRef` (a Secret in the controller's
  namespace, readable with `rbac.sinkSecretAccess=true`), says the value
  is whitespace-trimmed, and says the header is only sent over
  `https://`, since auth on an `http://` endpoint is a configuration
  error. Documentation only.

- **Workload categorization and the webhook `SinkFailed` body
  match the code** (#250). `docs/scraper-heuristics.md` said a
  workload takes the category of the first scraper that matches; every
  scraper runs and the highest-priority category wins (`agent`,
  `training`, `evaluation`, `vector-database`, `inference`). It also
  gave API-key components `declared` confidence: they are `inferred`,
  or `unresolved` for a `valueFrom` key, and an API key alone does not
  make a workload an `agent`. `docs/webhook-sink-protocol.md` now
  describes the `SinkFailed` body as up to 256 characters after ANSI
  stripping, not 256 bytes, and lists `3xx`: redirects are not
  followed or retried. Docs only.

- **The scraper heuristics no longer give a six-runtime allowlist**
  (#366). `docs/scraper-heuristics.md` said `inference.spec` matches
  against "vllm, tgi, triton, ollama, ray-serve, llm-d" and that seven
  named patterns match at the image-name boundary; the default
  `runtimeImagePatterns` attribute 29 runtimes, most of them
  boundary-matched. Both places now give examples and point at
  `internal/scraper/v1-runtime-patterns.yaml`, with NIM and Dynamo as
  examples of the prefix matches, and the README's runtime list says it is not the
  full set. Documentation only.

- **The scraper heuristics no longer say TensorRT-LLM has no pattern of
  its own** (#358). The deferred-cases table in
  `docs/scraper-heuristics.md` still said only Triton's `-trtllm-` images
  were covered and that a separate pattern would double-count, but
  `nvcr.io/nvidia/ai-dynamo/tensorrtllm-runtime` (v1.4.0) and
  `nvcr.io/nvidia/tensorrt-llm/release` (v1.6.0) attribute as
  `tensorrt-llm`. The row is marked resolved, and the runtime-patterns
  comment that listed two forms now lists all three. Documentation only.

- **The README's workload-type paragraphs describe what is recorded**
  (#337). Since the initial commit they said the controller extracts
  agent framework versions, vector-database collections and RAG
  relationships, a Hugging Face Hub signal and framework versions for
  training jobs, and benchmark suites for evaluation jobs. None of these
  is extracted. The paragraphs now list the frameworks each scraper
  recognizes and what it records, and name what it does not read.
  Documentation only.

- **The README's confidence examples match what the scrapers emit**
  (#296). "The confidence model" listed an `HF_MODEL_ID` env var as
  `declared`; env-var model claims are `inferred`, as
  `docs/scraper-heuristics.md` says and the scraper emits. It said the
  agent framework comes from "a `langchain` import pattern in container
  args"; `agent.spec` does not read args, and finds LangChain through
  env vars such as `LANGCHAIN_TRACING_V2`. And it gave `^vllm/.*` as the
  vLLM image pattern; the pattern is anchored at `vllm/vllm-openai` and
  its siblings, so `vllm/vllm-router`, for one, is not matched. The
  heuristics doc's deferred-cases table quoted the same old pattern and
  is corrected too. Documentation only.

- **The README's config example uses `v1beta1`** (#340). The external
  sinks example wrote the `AIBOMControllerConfig` through `v1alpha1`,
  whose schema has no `spec.discovery.workloadKinds`, so adding an
  allowlist to it failed strict validation or dropped the field, and
  applying it removed an allowlist already set. The example now uses
  `v1beta1`, and the README and the migration guide say the field is
  `v1beta1`-only. The migration guide and roadmap no longer say the
  chart renders the default config at `v1alpha1` (it renders `v1beta1`
  since v1.4.0). Documentation only.

- **The README's GCS sink example applies** (#292). It set
  `gcs.workloadIdentity`, a field no `AIBOMControllerConfig` version has,
  so `kubectl apply` rejected the example as an unknown field (and
  non-strict validation dropped it silently). The field is gone, and the
  README now says how the GCS sink authenticates: Application Default
  Credentials with Workload Identity on the controller's ServiceAccount
  (`serviceAccount.annotations` in the chart), or
  `gcs.credentialsSecretRef`. `docs/security-model.md` described the
  key-file fallback through `K8S_AIBOM_GCS_CREDENTIALS_FILE`, a v1.0
  environment variable nothing reads; it now names
  `gcs.credentialsSecretRef`. Documentation only.

- **The GCS sink docs describe the object paths it writes** (#257).
  The `{kind}` placeholder was documented as `Deployment`; the sink
  writes it lowercased (`deployment`), as the tests expect.
  `docs/security-model.md` gave the timestamp at second granularity;
  it is to the millisecond. The README now says how each `pathTemplate`
  placeholder renders, including `{category}` and the `_` that replaces
  `/`, `\`, `.` and line breaks in workload values. Paths are
  unchanged.

- **The README's AIBOM name examples include the API group**
  (#277). "See the BOMs" used `deployment-my-workload`, but the
  controller names an AIBOM `<group>-<kind>-<name>`, so the Deployment's
  AIBOM is `apps-deployment-my-workload` and both commands failed with
  `not found`. The examples use that name, and the README states the
  naming rule.

- **The model allowlists are not configurable through the CR**
  (#270). The header and the `modelArgFlags` comment in
  `internal/scraper/v1-runtime-patterns.yaml`, and the `InferenceConfig`
  godoc, said customers override `modelEnvVarNames`, `modelArgFlags` and
  the volume-path prefixes in `AIBOMControllerConfig`. The CR exposes
  only `spec.discovery.inferenceRuntimeImagePatterns`; the three model
  allowlists always come from the embedded file, so a value set in the
  CR is pruned with no effect. The comments and
  `docs/scraper-heuristics.md` now say so and point to
  `docs/contributing-a-pattern.md`. Docs only; a loader test now pins
  all three allowlists across a pattern override.

- **The Gatekeeper recipe in the policy cookbook flags namespaces that
  were never opted in** (#333). It checked
  `ns.metadata.labels["aibom.k8saibom.dev/enabled"] != "true"`, and in
  Rego a missing label is undefined, so the rule never fired for a
  namespace without the label (or without any labels), only for one
  labelled with another value. It now tests for opt-in in a helper rule
  and negates it. Documentation only.

- **Links from the documentation site to files outside `docs/` resolve**
  (#354). The GitHub Pages site is built from `docs/` only, so relative
  links such as `../README.md#quickstart` or
  `../internal/scraper/v1-runtime-patterns.yaml` led to 404s there. The
  19 such links outside `docs/design/` now point at the file on GitHub,
  which works on the site and in the repository. Documentation only.

## [1.6.2] - 2026-10-22

Patch release: eight defects found in the days after 1.6.0, two of them by
NVIDIA AICR's requalification run and six by community review. Two change
what the controller reports: the watch supervisor now sees a conversion-webhook
outage, and schema skew is detected for nested CRD fields. The rest correct
emitted documents (vector-database false positives, sink category, dataset
mounts, training CronJobs) and status (`Stale`, webhook error text). No new
patterns and no new capability; the CRDs, RBAC and the document shape are
those of 1.6.0.

### Fixed

- **The watch supervisor's steady-state probe now reads one object by
  name, not just a one-item list** (#216). While a conversion webhook is
  down the API server keeps serving LIST and WATCH for the kind from its
  per-version watch cache, so the list probe succeeded and a Dynamo
  operator outage never produced `Degraded=True`, `WatchUnhealthy` or
  `aibom_watch_healthy=0` (measured on GKE 1.35, 7 minutes). A
  single-object GET goes to storage and through the webhook, so the
  signal fires again within one probe interval; a kind with no objects
  falls back to the list. Correctness was never affected: the kind's
  API rejects every write during the outage and existing documents stay
  right.

- **Schema skew is detected for nested fields, and the chart guards
  `config.discovery.workloadKinds`** (#105 follow-up, found by NVIDIA AICR
  against 1.5.1 CRDs). 1.6.0's only CRD addition, `discovery.workloadKinds`,
  sits inside an existing object, so the controller's top-level skew check
  reported nothing while the API server pruned the field, and the chart's
  render-time guard covered only `verification`. The skew check now compares
  nested paths from the compiled-in types (`discovery.workloadKinds`,
  `verification.identities`, ...) and names them in the
  `SchemaPredatesController` condition; a parent whose children the served
  schema does not describe is not reported. The chart refuses to render an
  allowlist against CRDs that do not declare it, with the same remedy text as
  the verification guard.

- **The vector-database scraper no longer reports Milvus tool images as
  Milvus** (#210). The `milvus`, `qdrant` and `chroma` patterns ended
  in `.*`, so every image in the vendor's namespace that starts with
  the database name matched: a Deployment running
  `milvusdb/milvus-operator`, `milvusdb/milvus-backup`,
  `milvusdb/milvus-cdc` or `milvusdb/milvus-config-tool` got category
  `vector-database` and a `milvus` component. The three patterns now
  end at the image-name boundary, as `weaviate` and `pgvector` already
  did; `milvusdb/milvus-dev` (the server's development build) still
  matches.

--- e3c905d

- **External sinks receive the scraped workload category** (#212).
  Every reconciler seeds its workload as `inference`, and the
  scraper's category (`agent`, `vector-database`, `training`,
  `evaluation`) was copied into the document and the status but not
  into the sink metadata. A GCS path template's `{category}` token and
  the webhook `X-Aibom-Workload-Category` header therefore said
  `inference` for every workload. They now carry the same category as
  the document's `aibom.workload.category`.

- **Evaluation jobs list their dataset mounts** (#218). Only
  `training.spec` turned volume mounts named `dataset` or
  `training-data`, or mounted at `/data`, into `data` components, and
  it keeps its result only for training jobs, so an lm-eval, ragas or
  trulens Job or CronJob's AIBOM never named the data it was evaluated
  on, although `docs/scraper-heuristics.md` describes the rule for
  evaluation jobs too. `eval.spec` now applies the same rule, with
  locators on the kind's pod spec path. Training documents are
  unchanged; evaluation documents gain the `data` components.

- **Training CronJobs get an AIBOM** (#220). The Jobs a CronJob spawns
  roll up into the CronJob's document (Design 005), but `training.spec`
  did not handle CronJobs, so the CronJob's inputs stayed unresolved and
  a scheduled training run (for example `pytorch/pytorch`, or a job
  with `WANDB_API_KEY`) had no AIBOM at all, while the same template as
  a Job was reported as `training`. `training.spec` now reads a
  CronJob's job template, with evidence locators on
  `spec.jobTemplate.spec.template.spec`. Job documents are unchanged.

- **Webhook sink errors keep the receiver's answer when the sink
  deadline runs out mid-retry** (#222). When the 30s per-sink deadline
  expired during a backoff or a retry, the webhook sink returned only
  `context deadline exceeded`, so `SinkFailed` lost the status code and
  body the receiver had returned, contrary to
  `docs/webhook-sink-protocol.md`. The error is now the last receiver
  error, followed by `(retries stopped: context deadline exceeded)`.
  Retry counts and backoffs are unchanged.

- **`Stale` reaches `staleThresholdReconciles` for an unchanged
  workload** (#223). Extraction errors are not part of the input hash,
  so from its second reconcile on a workload whose spec kept producing
  the same errors took the dedup fast path, which skipped the error
  count: `status.consecutiveErrors` stayed at 1 and `Stale` never
  became `True`. The fast path now counts extraction errors like the
  full path, and clears the `ExtractionErrorsPersistent` `Stale` once
  the errors are gone. Documents and input hashes are unchanged.

## [1.6.1] - 2026-10-21

Patch release: four defects found in v1.6.0 by community review in the
week after the tag, three in `kubectl aibom` and one in emitted evidence,
plus a rebuild on Go 1.26.10 and corrected CRD-apply remedies. No new
patterns and no new capability; the CRDs, RBAC and the document shape are
those of 1.6.0.

### Fixed

- **`kubectl aibom find` no longer fails when one AIBOM has no
  document yet** (#200). The controller creates an AIBOM before it writes the
  status, and a failed status write leaves the status empty until the
  next reconcile. `find` read every row's inline document, so one such
  AIBOM made the whole command exit with `no BOM document in status
  (is the AIBOM Ready?)` whenever it ran with no filter, `--image` or
  `--digest`. That row is now treated like an external or truncated
  one: listed with IMAGES `no-document`, shown (not excluded) with the
  stderr warning when an image or digest filter cannot be evaluated
  for it, and excluded as before by `--runtime`, `--model` and
  `--signed`. `view` and `verify` keep the same error.

- **`kubectl aibom find --digest` rejects values that cannot name a
  digest** (#202). `--digest sha256:` (or whitespace) normalized to an
  empty prefix and matched every workload with a resolved digest, so
  "where is this digest running?" answered "everywhere". A value longer
  than 64 characters, non-hex text, a full image reference or another
  algorithm silently printed `0 matches`. `--digest` now takes 1 to 64
  hex characters, bare or `sha256:`-prefixed (the prefix in any case),
  and anything else is an error before the AIBOMs are listed.

- **`kubectl aibom help` and argument errors no longer need a
  kubeconfig** (#204). `run` loaded the kubeconfig before it looked at
  the subcommand, so with no usable kubeconfig `kubectl aibom help`, a
  mistyped subcommand, a bad `--signed` or `--digest` value and a
  missing `view`/`verify` name all failed with `resolving namespace:
  invalid configuration`. These are now settled first. `-h` and
  `--help` after a subcommand print the usage and exit 0 instead of
  failing with `flag: help requested`. A valid command loads the
  kubeconfig as before.

- **Evaluation CronJob evidence names the CronJob's pod spec path**
  (#206). `eval.spec` built every evidence locator as
  `spec.template.spec.containers[N].image`, which is the Job path; on
  a CronJob the pod spec is at `spec.jobTemplate.spec.template.spec`,
  so an lm-eval, ragas or trulens CronJob's AIBOM pointed at a field
  the object does not have. CronJob locators now name the real path.
  Job documents are unchanged; evaluation CronJob documents change in
  that locator only.

- **Built with Go 1.26.10** (GO-2026-6809, `crypto/x509`, found in
  go1.26.9). The Go directive moves in the root, `verifier` and
  `terraform/test` modules; no code change.

### Changed

- **The remaining operator-facing CRD-apply remedies carry
  `--force-conflicts` and the `k8s-aibom-crds` field manager** (#209,
  follow-up to #119). The `SchemaPredatesController` condition and event,
  the chart preflight, the migration guide and the README's manual
  upgrade step now print the same command, and CRDs managed by the
  `k8s-aibom-crds` chart are sent to a chart upgrade instead of a forced
  apply. The README's manual step applies the chart's CRDs rather than
  the release `install.yaml`, which also carries the Deployment, RBAC and
  `AIBOMControllerConfig/default`.

## [1.6.0] - 2026-10-19

The coverage release. One AIBOM per AI workload across the operator-managed
kinds the inference ecosystem actually deploys — NVIDIA Dynamo, NIM
Operator, LeaderWorkerSet, llm-d's DisaggregatedSet, CronJob — with owned
workloads rolled up into their owner's document, a workload-kind allowlist,
a companion CRDs chart, and the community's runtime-pattern additions.
Third-party CRD outages degrade one kind instead of the controller.

### Added

- **NVIDIA Dynamo `DynamoGraphDeployment` scraper** (Design 003 §4;
  v1.6 coverage release). One AIBOM per graph, keyed to the DGD.
  `spec.backendFramework` becomes a **declared** runtime (`trtllm`
  recorded as `tensorrt-llm` to match the image-pattern name);
  `spec.components[i].modelRef` becomes a **declared** model attributed
  to its component; component `type` is recorded as graph topology;
  every component and role `podTemplate` goes through the shared
  inference extraction with locators rooted at the component path.
  No model identity is ever derived from Dynamo image paths (Dynamo
  images carry no model). Registered only when the CRD is present;
  RBAC adds get/list/watch on `dynamographdeployments.nvidia.com`.
  Pod-status digests and the DGD → component → pod ownership roll-up
  follow in the §3 change.

- **Standalone `DynamoComponentDeployment` roots.** A component with no
  graph parent is reported as its own root with locators rooted at
  `spec`; components owned by a graph are absorbed. RBAC adds
  `dynamocomponentdeployments` get/list/watch, and read-only get/list
  on the Grove pod-owning kinds (`podcliquesets`, `podcliques`,
  `podcliquescalinggroups`) to complete multi-node chains; absent CRDs
  or denied permissions degrade to today's behavior.

- **NVIDIA NIM Operator `NIMService` scraper** (Design 003 §1; v1.6
  coverage release). One AIBOM per NIMService. The kind itself is a
  **declared** runtime (`nim`); `spec.image` becomes the container
  component (digest only when the repository is digest-pinned);
  `spec.env` / `spec.args` go through the existing model allowlists
  with `spec.env[i]` / `spec.args[i]` locators; the NIM image path
  `nvcr.io/nim/<org>/<name>` yields an **inferred** model identity
  **only when nothing is declared** — declared `NIM_MODEL_NAME` /
  `NIM_SERVED_MODEL_NAME`, args or annotations always win (the AICR
  case of a llama NIM image serving Qwen reports Qwen). Storage shape
  (`nimCache` / `pvc` / `hostPath` / `emptyDir`), multi-node topology
  and inference platform are recorded as properties. Registered only
  when the CRD is present; RBAC adds get/list/watch on
  `nimservices.apps.nvidia.com`.

- **`LeaderWorkerSet` scraper** (Design 003 §2; v1.6 coverage release).
  One AIBOM per LWS, keyed to the LWS UID. Both the optional leader
  template and the required worker template go through the shared
  inference extraction with locators rooted at
  `spec.leaderWorkerTemplate.{leader,worker}Template`, an `lws.role`
  property on every extracted component, and `lws.size` /
  `lws.replicas` on container components. Nothing is declared by an
  LWS, so runtime attribution is image-pattern inferred as for a
  StatefulSet. Registered only when the CRD is present; RBAC adds
  get/list/watch on `leaderworkersets.leaderworkerset.x-k8s.io`.
  The StatefulSets an LWS materializes roll up in the §3 change.

- **`DisaggregatedSet` scraper** (#137, Design 007 Part 1; v1.6
  coverage release). llm-d's prefill/decode primitive on NVIDIA
  hardware (`disaggregatedset.x-k8s.io/v1`, from the LeaderWorkerSet
  project, lws ≥ 0.11; the wide expert-parallelism and P/D guides) is a
  reported kind: one AIBOM per set, keyed to its UID. Each
  `spec.roles[i]` inlines a LeaderWorkerSet template, so the extraction
  is the LWS extraction once per role with locators rooted at
  `spec.roles[i].spec.leaderWorkerTemplate.<leader|worker>Template`;
  every component carries `disaggregatedset.role` and `lws.role`,
  containers also `lws.size`, `lws.replicas` and
  `disaggregatedset.slices`. The LeaderWorkerSets a set materializes,
  their StatefulSets and pods roll up into the set's document (Design
  005), which is how pod-status digests reach it. Runs under the watch
  supervisor like every third-party kind; RBAC: get/list/watch
  `disaggregatedsets.disaggregatedset.x-k8s.io`. `InferencePool`
  membership (Design 007 Part 2) is v1.7.

- **`CronJob` coverage.** One AIBOM per CronJob from `spec.jobTemplate`
  (eval patterns as for Jobs); spawned Jobs roll up. RBAC adds
  `cronjobs` get/list/watch.

- **Ownership roll-up: one workload, one AIBOM** (#126, Design 005). A
  tracked workload owned — directly or transitively via controller
  `ownerReferences` — by another tracked kind no longer gets its own
  AIBOM; the owner's document is the report. Covered chains:
  `DynamoGraphDeployment` → `DynamoComponentDeployment` → `Deployment` |
  `LeaderWorkerSet`, and `DynamoGraphDeployment` → Grove `PodCliqueSet`
  → [`PodCliqueScalingGroup` →] `PodClique` (the PodCliqueSet is owned
  directly by the graph); `NIMService` →
  `Deployment` | `LeaderWorkerSet`; `LeaderWorkerSet` → `StatefulSet`;
  `CronJob` → `Job`. The owner's document lists what it absorbed as
  `aibom.rollup.owned.<i>` (`Kind/name`, sorted) and receives the
  descendants' pods, so **the CRD kinds now resolve pod-status digests**
  like a Deployment does through its ReplicaSets. Ownership only, never
  labels. An intermediate kind the controller cannot read (RBAC, CRD
  absent) ends the walk as unresolved and the child is reported as
  before — coverage never regresses because of a missing permission
  (`rollup_unresolved` outcome). Workloads with no controller owner pay
  nothing. On upgrade, child AIBOMs under the chains above are deleted
  on their next reconcile. New reconcile outcomes: `rolled_up`,
  `rollup_unresolved`.

- **Configurable workload-kind allowlist** (#136, Design 006).
  `spec.discovery.workloadKinds` on `AIBOMControllerConfig` lists the
  kinds the controller inventories as `Group/Kind` strings
  (`apps/Deployment`, `nvidia.com/DynamoGraphDeployment`, ...); absent
  or empty means every known kind, exactly as before. An entry that is
  not a known kind is `ConfigInvalid` with the last-known-good retained
  and the entry named, never a silent no-op. A kind not in the list is
  treated like a namespace that is not opted in: no scrape, its existing
  AIBOMs deleted, outcome `kind_not_allowed`. For the third-party kinds
  (KServe, Dynamo, NIMService, LeaderWorkerSet) the list also decides
  whether the watch exists: a kind removed from it is stopped (`aibom_
  watch_healthy{kind}=0`, a Normal `WatchDisabled` event, not Degraded)
  and started again when added back. **The apps/v1 and batch watches
  are not narrowed in this release** — those controllers run on the
  manager's shared cache, so the list filters their reporting only;
  moving them under the supervisor is the v1.7 follow-up. A kind that is
  switched off also stops absorbing the workloads it owns (Design 005),
  which are reported on their own again rather than disappearing with
  it. Changes apply without a restart: a `workloadKinds` change
  re-reconciles every workload of every reporting kind.

- **`kubectl aibom find`** — the incident command: filter tracked
  workloads by `--runtime` (exact), `--model` (case-sensitive
  substring), `--image` (substring of the container reference),
  `--digest` (bare hex or `sha256:`-prefixed; a prefix matches) and
  `--signed` (unsigned | claimed | verified); filters AND together.
  Zero matches prints `0 matches` and exits 0 so scripts can tell
  "none found" from "plugin broke". Rows whose document is in an
  external sink or truncated are shown — never excluded — under
  image/digest filters, with a stderr warning. Same client and RBAC
  as `summary`; no new CRD, no controller flag.

- **`k8s-aibom-crds` companion chart** (#77, requested by NVIDIA AICR).
  The two CRDs are also published as their own chart,
  `oci://ghcr.io/googlecloudplatform/charts/k8s-aibom-crds`, at the same
  version and from the same tag-triggered, attested pipeline as the main
  chart. Helm never upgrades CRDs shipped in a chart's `crds/` directory,
  so a controller upgrade whose CRDs changed used to run against the
  previous schema unless the operator applied the CRDs by hand; with the
  companion chart the CRDs are an ordinary release, upgraded first, then
  the controller. Each CRD carries `helm.sh/resource-policy: keep`, so
  uninstalling the CRDs chart never deletes inventory. **Artifact-set
  change for downstream distributions:** a release now comprises image,
  main chart, CRDs chart, install.yaml and attestations; the main chart
  keeps its `crds/` directory, so single-chart installs and `install.yaml`
  are unchanged. CI installs the two charts in the documented order; the
  chart copies of the CRDs are generated from `config/crd/bases` and
  checked for drift.

- **Metrics are now scrapable, opt-in** (#106). The controller's
  Prometheus endpoint was registered but bound to loopback with no
  Service — unreachable by any scraper, which made the chart's
  "metrics" wording an overclaim and left the shipped
  `grafana/podmonitoring.yaml` targeting a pod port that did not
  exist. `metrics.enabled=true` now binds the endpoint on
  `metrics.port` (8080), names the container port `metrics`, and
  renders a ClusterIP Service; `metrics.serviceMonitor.enabled=true`
  additionally renders a Prometheus Operator ServiceMonitor. Off by
  default; nothing changes for existing installs.

- **`aibom_workload_reconcile_outcomes_total{kind,outcome}`** — the
  series that separates "opted-in namespace, nothing recognized" from
  a broken controller: `not_opted_in` (namespace selector did not
  match), `unmatched` (opted in, no inference signal — conservative
  detection declined), `matched` (an AIBOM is produced).

- **AMD ROCm vLLM runtime pattern** (#113). `rocm/vllm` images (tag,
  digest, and the `rocm/vllm/<build>` path form KubeAI ships as its
  AMD GPU default) attribute as runtime `vllm`. Anchored to AMD's
  `rocm/` publisher namespace at the image-name boundary, so
  `rocm/pytorch`, `rocm/vllm-dev` and other registries stay unmatched.

- **vLLM CPU release image runtime pattern** (#114).
  `public.ecr.aws/q9t5s3a7/vllm-cpu-release-repo` (the vLLM project's
  own public ECR alias; KubeAI's CPU default) attributes as runtime
  `vllm`. Anchored to that exact alias and repository, not a broad
  `public.ecr.aws/.*vllm` match.

- **Infinity embeddings server runtime pattern** (#111).
  `michaelf34/infinity` (tag and digest forms, including `0.0.77-cpu`
  and `0.0.77-rocm`) attributes as runtime `infinity`. The match is
  publisher-anchored at the image-name boundary, so
  `michaelf34/infinity-extra` and other registries stay unmatched.

- **faster-whisper-server speech-to-text runtime pattern** (#112).
  `fedirz/faster-whisper-server` (tag and digest forms, including
  KubeAI's `latest-cpu` and `latest-cuda`) attributes as runtime
  `faster-whisper`. The match is publisher-anchored at the image-name
  boundary, so `fedirz/faster-whisper-server-extra` and other
  registries stay unmatched.

- **LMCache production-stack vLLM runtime pattern** (#147).
  `lmcache/vllm-openai` (tag and digest forms; production-stack's
  default model server) attributes as runtime `vllm`. Anchored to
  LMCache's `lmcache/` publisher namespace at the image-name boundary,
  so `lmcache/lmstack-router`, `lmcache/lmstack-sidecar` and other
  orgs stay unmatched.

- **Vertex AI Model Garden vLLM runtime pattern** (#146).
  `us-docker.pkg.dev/vertex-ai/vertex-vision-model-garden-dockers/pytorch-vllm-serve`
  (tag and digest forms; GKE tutorials' Vertex vLLM image) attributes
  as runtime `vllm`. Anchored at the image-name boundary, so sibling
  images under the same path and the same name under another registry
  stay unmatched.

- **llama.cpp server runtime pattern** (#148).
  `ghcr.io/ggml-org/llama.cpp` and the older `ghcr.io/ggerganov/llama.cpp`
  (tag and digest forms, including `server-b10680`, `server-cuda-b10680`,
  and `server-rocm-b10680`) attribute as runtime `llama.cpp`. The match is
  publisher-anchored at the image-name boundary, so name-prefix near-misses
  and Docker Hub lookalikes stay unmatched. Tag-independent; `server-`
  build numbers are not treated as a runtime version.

- **ray-serve `rayproject/ray-ml` runtime pattern** (#151).
  `rayproject/ray-ml` (KubeRay serve samples) attributes as runtime
  `ray-serve`, same as `rayproject/ray`. Bounded at the image-name
  boundary, so other `rayproject/ray-*` names stay unmatched.

- **KServe HuggingFace server runtime pattern** (#152).
  `kserve/huggingfaceserver` (tag and digest forms) attributes as
  runtime `huggingface`, the same id a KServe `InferenceService`
  declares for this server in `modelFormat.name`, so one id covers both
  deployment shapes. Publisher-anchored at the image-name
  boundary, so KServe infrastructure images under the same org
  (`kserve/kserve-controller`, `kserve/agent`, `kserve/router`,
  `kserve/storage-initializer`), name-prefix near-misses, and other
  registries stay unmatched.

- **OpenVINO Model Server runtime pattern** (#164).
  `openvino/model_server` (tag and digest forms) attributes as runtime
  `openvino-model-server`. Publisher-anchored at the image-name
  boundary, so other `openvino/*` images (`openvino/ubuntu24_runtime`,
  `openvino/ubuntu24_dev`, `openvino/cvat_server`) and other registries
  stay unmatched.

- **AWS Deep Learning Containers vLLM runtime pattern** (#163).
  `public.ecr.aws/deep-learning-containers/vllm` (EC2 and SageMaker
  tags, bare, and digest forms) attributes as runtime `vllm`. Anchored
  to AWS's own public ECR namespace at the image-name boundary, so the
  entry does not match sibling repositories (`pytorch-inference`,
  `vllm-arm64`, `huggingface-vllm`), other public ECR aliases,
  private-account ECR copies, or the same path on another registry.
  No existing entry changes.

- **TensorRT-LLM release image runtime pattern** (#176).
  `nvcr.io/nvidia/tensorrt-llm/release` (GA, rc, dev and digest forms)
  attributes as runtime `tensorrt-llm`, the name the Dynamo
  `tensorrtllm-runtime` entry already uses. Anchored at the image-name
  boundary, so the `tensorrt-llm/devel` build image, name-prefix
  lookalikes and other registries stay unmatched. No existing entry
  changes.

- **Speaches speech server runtime pattern** (#177).
  `ghcr.io/speaches-ai/speaches` (CPU, CUDA, sha- and digest forms)
  attributes as runtime `speaches`. Speaches is the renamed
  continuation of `fedirz/faster-whisper-server`, whose entry is
  unchanged. Publisher-anchored at the image-name boundary, so
  `speaches-extra`, other GHCR orgs and other registries stay
  unmatched.

- **LocalAI runtime pattern** (#178). `localai/localai` and
  `quay.io/go-skynet/local-ai` (CPU, GPU, AIO, master and digest forms)
  attribute as runtime `localai`. LocalAI publishes every build to both
  repositories, so both are the publisher's own. Anchored at the
  image-name boundary, so name-prefix lookalikes, other users'
  repositories and other registries stay unmatched.

- **Xinference runtime pattern** (#182). `xprobe/xinference` (GPU,
  CPU, aarch64, nightly and digest forms) attributes as runtime
  `xinference`. Publisher-anchored at the image-name boundary, so
  `xprobe/xinference-pypiserver`, other `xprobe/*` images and other
  registries stay unmatched.

- **Aphrodite Engine runtime pattern** (#183).
  `alpindale/aphrodite-openai` (release, commit-hash and digest forms)
  attributes as runtime `aphrodite`. Publisher-anchored at the
  image-name boundary, so the build image, the older
  `alpindale/aphrodite-*` images and other registries stay unmatched.

- **llm-d model-server runtime pattern** (#149). `llm-d-cuda`,
  `llm-d-rocm`, `llm-d-cpu`, `llm-d-xpu`, `llm-d-xpu-sglang`,
  `llm-d-hpu`, `llm-d-gke`, `llm-d-aws` and `llm-d-cuda-gb200` (tag and
  digest forms, including stacked `-dev`/`-debug`/`-ubuntu` variants)
  attribute as runtime `llm-d`. Anchored to those model-server names
  rather than the whole `llm-d/` org, so the router endpoint picker and
  disaggregation sidecar, `llm-d-inference-sim`, the KV-cache manager
  and other infrastructure images stay unmatched. Since llm-d v0.8 the
  well-lit paths run upstream `vllm/vllm-openai` directly (v0.10.0
  deprecated `llm-d-cuda`/`llm-d-aws` in its favour); those already
  attribute as `vllm`.

- **vLLM Docker Hub pattern narrowed to model-server images** (#150).
  `^vllm/.*` matched every image in vLLM's org. It is now anchored to
  the server families — `vllm/vllm-openai` (and `-cpu`/`-rocm`/`-xpu`),
  `vllm/vllm-tpu`, `vllm/vllm-omni` (and `-rocm`) — so `vllm/agentic-api`
  (llm-d's OpenAI Responses API layer in front of an InferencePool),
  `vllm/vllm-router` and `vllm/vime` no longer attribute as a serving
  runtime. Tag and digest forms of the servers still match.

- **Runtime image-name boundary guards** (#165). TGI (Docker Hub and
  GHCR), Triton, Ollama, SGLang, LMDeploy, TEI and LiteLLM no longer
  attribute similarly named images as runtimes. Tag, digest and bare
  references still match. SGLang retains the CPU form and enumerates
  LMSYS's ROCm, JAX, Omni and daily server builds; LiteLLM
  retains its `-database` and `-non_root` variants. `sglang-router`,
  `sglang-staging`, `tritonserver-sidecar` and other unlisted suffixes
  stay unmatched.

### Changed

- **Attestation bundles are release assets.** Each release now attaches
  its Sigstore bundles as `*.intoto.jsonl` assets — SLSA build
  provenance for the image, the Helm chart and the `kubectl-aibom`
  archives, and the CycloneDX SBOM attestation for the image — in
  addition to the registry- and GitHub-stored attestations that
  already existed. A consumer can verify an artifact from the release
  page alone (`gh attestation verify <artifact> --owner
  GoogleCloudPlatform --bundle <bundle>`), and the OpenSSF Scorecard
  Signed-Releases check, which only looks at release assets, now sees
  provenance. No change to the artifacts themselves.

- **Build tooling: controller-gen v0.16.5 → v0.22.0.** Regenerated CRDs
  and RBAC are byte-identical apart from the generator version
  annotation. `make test` now passes `-timeout 30m` so the controller
  envtest package, which takes 8–10 minutes on CI-class machines, does
  not hit `go test`'s 10-minute default on slower ones (reported by a
  contributor as a spurious failure on main).

### Fixed

- **A third-party CRD that is present but unservable no longer takes
  the controller down or goes silent** (#127, Design 004). Dynamo's
  deployed CRDs (dynamo-platform ≤ 1.4) store `v1alpha1` behind a
  conversion webhook served by the Dynamo operator. Before: with that
  operator down at startup, controller-runtime's shared cache never
  synced, *every* reconciler failed its cache-sync wait and the process
  exited (a crash-loop of all of k8s-aibom, with a log blaming an
  unrelated kind); with the operator dying after startup, new Dynamo
  graphs were silently never inventoried. Now: third-party kinds
  (KServe, Dynamo, NIMService, LeaderWorkerSet) run under a supervisor
  on their own caches; a kind that cannot list, sync or convert is
  retried with capped backoff and reported — `Degraded=True` with
  reason `ThirdPartyWatchUnhealthy` on `AIBOMControllerConfig` naming
  the kind and the verbatim API error, one `WatchUnhealthy` Warning
  event per outage and one `WatchRecovered` on recovery,
  `aibom_watch_healthy{kind}` and `aibom_watch_errors_total{kind}`.
  Existing AIBOMs for the kind are kept; apps/v1 kinds and readiness
  are unaffected; recovery is automatic. Steady-state detection is a
  `Limit: 1` list per present kind every two minutes, because
  client-go's reflector swallows conversion errors on an established
  watch stream (measured). Found by the AICR maintainer's review of
  the v1.6 Dynamo scraper.

- **Served-schema skew is now detected and reported** (#104). If the
  cluster's `AIBOMControllerConfig` CRD is replaced by an older schema
  than the running controller (a GitOps sync pinned to an older
  revision, a `CreateReplace` rollback, a manual CRD re-apply), the API
  server silently prunes stored fields such as `spec.verification` —
  previously leaving signature verification OFF behind a fully green
  status. The controller now reads the served schema via the OpenAPI
  v3 endpoint (no new RBAC beyond what `system:discovery` already
  grants; stated explicitly in the ClusterRole) and compares it
  against every top-level spec field it was built with. Any missing
  field yields `Degraded=True` with reason `SchemaPredatesController`,
  a Warning Event naming the fields and the remedy, and a log line.
  The check is generic over the spec struct, so the next additive
  field cannot reintroduce the failure. Found by downstream
  adversarial upgrade testing (NVIDIA AICR qualification of v1.5.1).

- **An upgrade that sets `config.verification` against pre-1.5 CRDs now
  stops before anything is applied** (#105). Helm skips `crds/` on
  upgrade. On Helm 4 the config CR's server-side apply then failed with
  `.spec.verification: field not declared in schema` only after the
  Deployment had rolled, leaving the release `failed` and half-applied;
  on Helm 3 the upgrade succeeded and the API server pruned
  `spec.verification`, leaving verification off. When
  `config.verification` is set, the chart now reads the installed
  `AIBOMControllerConfig` CRD with `lookup` and, if its `v1beta1`
  schema lacks `spec.verification`, fails at render time with the
  CRD-apply command. Default values never perform the lookup, and
  `helm template` and client-side `--dry-run` are unaffected. With
  `config.verification` set, the Helm identity now needs `get` on that
  CRD; without it the render fails on `lookup` before anything is
  applied. Found by the same downstream qualification.

- **Tenant-controlled document growth is bounded.** Container
  component name/version are truncated on the same rule as every
  other authored string, and a per-document component cap (256)
  bounds pathological specs in memory and on the wire, not only in
  etcd. Truncation is recorded as `aibom.truncation.applied` —
  mirroring the redaction rule, never silent. Untruncated documents
  are byte-identical to before.

- **Transient sink failures now retry until the archive heals**
  (#91). Previously the BOM input hash was persisted even when a
  configured external sink failed, so the next reconcile took the
  dedup fast path and returned before re-emitting — a transient 403
  or network blip dropped that BOM from the archive until the
  workload spec changed. Now: the input hash is not persisted while
  any sink is failing (dedup unaffected on success), the reconcile
  requeues on a bounded cadence (1 minute) until delivery succeeds,
  and the four status/comment texts that promised a retry that never
  happened now describe the real behavior. After a partial failure,
  sinks that already succeeded are re-emitted on the retry; the
  default timestamped GCS path template makes that a duplicate
  archive object, never an overwrite.

- **The bootstrap-race deferral now requeues explicitly.** It
  previously waited for a status-update watch event that the
  Owns-watch's GenerationChangedPredicate filters out — an AIBOM
  whose first status write raced the cache could sit unpopulated
  until an unrelated event arrived, and the empty result could
  collapse a concurrently scheduled sink retry.

## [1.5.1] - 2026-09-28 (security PATCH)

### Fixed

- **Pod attribution is now ownership-based, not selector-based**
  (Deployment, StatefulSet, DaemonSet, Job). Previously, two
  same-namespace workloads with overlapping selectors and a shared
  container name could cross-contaminate image digests in each
  other's BOMs, and a principal with pod-create permission in an
  opted-in namespace could plant a chosen digest in another
  workload's record. Digests now enter a BOM only from pods tied to
  the workload through the controller ownerReference chain
  (Deployment → ReplicaSet → Pod walked explicitly), with a
  belt-and-braces image-name match on the candidate's container
  status. Pods with no controller owner never contribute. Found by
  internal review; regression-tested end to end.

- **Webhook sinks reject credentials over cleartext at config load.**
  An `http://` endpoint combined with any `auth` configuration is now
  a load-time validation error (all-or-nothing fallback, named
  LoadError). Plain http without auth remains legal for in-cluster
  receivers; https with auth is unchanged.

## [1.5.0] - 2026-09-22

The trust release: the `verified` confidence tier the README has
promised since v1.0 — cryptographic verification of model signature
claims against configurable Sigstore trust roots with Rekor
transparency-log inclusion — designed in public (Design 002),
substantively amended twice by external review from the model-signing
community, and hardened so a claim can never upgrade itself. Also:
the output sanitization guarantee, the kubectl-aibom plugin, the
non-default-configuration e2e matrix, and the NIM model env vars.
With no signature annotations present, output is byte-identical to
v1.4.0.

### Added

- Chart: `extraVolumes` / `extraVolumeMounts` values — required to
  mount a static trusted-root file for
  `verification.trustRootMode=staticBundle` (air-gapped clusters).
- e2e: non-default-configuration matrix (#59) — strict-readiness
  break/recover, webhook sink with a bearer-token Secret under real
  RBAC, and signature verification with verified and tampered
  outcomes against a static trust root. Closes the test-debt class
  behind the one code defect external qualification found.

- NIM model-declaration env vars `NIM_MODEL_NAME` and
  `NIM_SERVED_MODEL_NAME` join the default model-identity allowlist.
  Reported by an AICR maintainer during Design 003 review: a NIM
  container's served model can differ from its image default, and
  without these names such workloads carried no declared model signal.

- kubectl-aibom: `summary` gains a SIGNED column (per-model signature
  states, deduplicated) and `verify` appends the recorded per-model
  signature facts to its integrity verdict — the verified tier is
  demonstrable in one command.
- Sigstore/Rekor signature verification — the `verified` confidence
  tier (Design 002, #56): `spec.verification` on
  `AIBOMControllerConfig` enables cryptographic verification of model
  signature claims (`model.k8saibom.dev/oms-signature`, optional
  `model.k8saibom.dev/digest`) against configurable trust roots
  (Sigstore public-good via TUF, self-hosted TUF mirror, or a static
  trusted-root file). `verified` requires chain validity, Rekor
  inclusion, a satisfied signer-identity constraint (a non-public
  trust root counts), and no contradicted declared binding —
  digest-over-name precedence. All outcomes are recorded facts on the
  model component (`signature.*` properties; `ModelSummary.Signed` now
  populates); verification failures never fail a reconcile, and with
  verification absent, output is byte-identical to v1.4.0. Retires
  schema-divergences entry D-001. Fixed alongside: the reserved
  signature annotations are no longer mis-extracted as phantom model
  identities.

- `kubectl-aibom` plugin (#58): `summary` (per-namespace or `-A`
  table of workload, category, runtime, models, confidence, Ready),
  `view` (decoded, pretty-printed BOM; `--raw` for the canonical
  bytes the published digest covers), and `verify` (recomputes
  sha256 against `status.bomDocument.sha256`, non-zero exit on
  mismatch — script- and CI-composable). Built via
  `make kubectl-plugin` or `go install .../cmd/kubectl-aibom@latest`;
  verified live against a v1.4.0 install.
- Output sanitization guarantee (#57): every string emitted into a BOM
  passes a redaction filter at the build boundary — URI userinfo,
  known credential query parameters (pre-signed URL signatures, SAS
  tokens), and well-known secret token shapes are replaced before
  emission, with an `aibom.redaction.applied` property recording the
  redaction class on any affected component or service. The audit
  behind it (issue #57) confirmed no default extraction path emits
  credential material; the filter guarantees the residual vectors
  (URI-shaped identity fields such as KServe `storageUri` and model
  annotations, and operator-extended allowlists). Clean documents are
  byte-identical to v1.4.0 output.

## [1.4.0] - 2026-08-25

The downstream-coverage release, cut the day after k8s-aibom began
shipping in NVIDIA AICR v0.20.0: detection patterns NVIDIA's catalog
needs, the chart CR template graduation deferred out of v1.3.0, and
the re-baselined performance record — bundled so downstream
distributions requalify once.

### Added

- Runtime image patterns for NVIDIA NIM (`nvcr.io/nim/*` → `nim`) and
  NVIDIA Dynamo backend workers (`nvcr.io/nvidia/ai-dynamo/{vllm,sglang,
  tensorrtllm}-runtime` → `vllm`/`sglang`/`tensorrt-llm`, nightly
  variants included). Dynamo infrastructure images (frontend, planner,
  operator) deliberately do not match.
- TGI's GHCR namespace (`ghcr.io/huggingface/text-generation-inference`
  → `tgi`) — previously a documented deferred false negative; real
  deployment signal arrived.

### Changed

- Performance documentation re-baselined on live-GKE measurements of
  v1.2.0 and v1.3.0 (1,001 workloads, dual-sampled): steady state is
  1–2m CPU / ~61Mi, statistically identical across both versions and
  consistent with NVIDIA/aicr#2310's independent measurement. Every
  published figure now carries version + environment + sampling
  method; v1.1.0-era Kind steady-state figures are superseded, with
  the ~370m convergence burst retained as the upper bound.
- The chart now renders the default `AIBOMControllerConfig` at
  `aibom.k8saibom.dev/v1beta1`, matching the CRD storage version
  (#49). No behavioral change: the schema is identical under dual
  serving, and `v1alpha1` manifests remain valid through 1.x. Tools
  asserting on the rendered CR's `apiVersion` should follow the
  guidance in docs/migration-v1beta1.md (assert the CRD storage
  version, not blanket apiVersion replacement).

## [1.3.0] - 2026-08-19

The graduation release: the `aibom.k8saibom.dev` APIs reach `v1beta1`
(Design 001), satisfying the non-alpha storage requirement for stock
AICR adoption (NVIDIA/aicr ADR-019). **Upgrading requires applying the
new CRDs** — see docs/migration-v1beta1.md; skipping the step stalls
the rollout loudly and safely, with the previous pod still serving.

### Fixed

- Readiness gating had a start-ordering race (since v1.1.0): the
  cache-sync check called `WaitForCacheSync` before the manager started,
  trivially passing against an empty informer set — a pod could report
  Ready before (or without) its informers syncing. A first fix (a
  manager Runnable) was defeated by the same class of race: controllers
  create their informers after plain runnables start. Readiness is now
  asserted per probe against the load-bearing informers themselves —
  each readyz evaluation asks the cache for the v1beta1 `AIBOM` and
  `AIBOMControllerConfig` informers and their sync state, failing while
  the API server cannot serve those versions (e.g. stranded CRDs). With
  this fix, upgrading to the graduation release without the required
  CRD apply stalls the rollout with the previous pod still serving.
  Both defeated implementations were caught by the v1.3.0
  release-candidates' stranded-CRD boundary tests on a real cluster.
- Chart CRD files (and generated manifests) keep their YAML document
  separators: without them, `helm show crds` concatenates the two CRDs
  into one invalid stream and `kubectl apply` silently applies only the
  first — breaking the documented CRD-upgrade command exactly when it
  matters. Found by the rc.3 boundary test's recovery step.

### Added

- `v1beta1` API for `AIBOM` and `AIBOMControllerConfig` — schema-identical
  to `v1alpha1` (conversion strategy remains `None`), served alongside
  `v1alpha1`, and the storage version from this release onward.
  `v1alpha1` remains served and field-frozen through 1.x; its removal
  will be a separate, announced release with a documented migration step.
  Design: docs/design/001-api-graduation-v1beta1.md. The controller
  operates on the `v1beta1` types internally; both versions remain
  registered and served. An integration test proves the dual-serving
  round-trip (write v1alpha1 → read v1beta1 and vice versa, same UID,
  identical fields).

## [1.2.0] - 2026-08-18

### Added

- Opt-in strict configuration readiness: `--strict-config-readiness`
  (chart value `readiness.strictConfig`) fails the readiness probe while
  the active `AIBOMControllerConfig` is invalid. Off by default — the
  controller deliberately stays Ready on last-known-good config so an
  operator typo cannot take down inventory; distributions requiring
  configuration-aware readiness (e.g. AICR) enable it via values. An
  absent CR (defaults-by-choice) is not treated as invalid.

## [1.1.0] - 2026-08-18

The qualification release: every blocking finding from NVIDIA/AICR's
ADR-019 Phase 1 qualification of v1.0.0 (gates 3 and 4), fixed with
tests, plus readiness hardening. Details in the sections below and the
qualification record on issue #8.

### Added

- Chart `config.*` values render verbatim into the default
  `AIBOMControllerConfig`: `discovery` (incl. namespace selector),
  `bomGeneration`, `sinks`, and `logging` are now normal public values —
  no template patching or post-install CR mutation needed.
- Chart default resources are set from measured footprint (requests
  50m/128Mi, memory limit 256Mi; no CPU limit by design — see
  docs/quality-baseline.md).

### Changed

- Readiness now gates on informer cache sync: `/readyz` fails until the
  controller can observe the cluster, and the chart wires liveness and
  readiness probes against the health endpoints (previously no probe
  consulted them).

### Fixed

- Scrape and BOM-build failures now flip `Ready=False` (with reason and
  message) on the workload's existing AIBOM, so failures are observable
  in status rather than only in logs; prior document/summary fields are
  preserved and the failure path never creates AIBOMs.
- Non-conflict status-persistence failures now emit the
  `aibom_status_persist_failures_total` metric and an
  `AIBOMStatusPersistFailed` warning Event.
- Truncation reason now distinguishes "no external sink is configured"
  from "configured sinks all failed this cycle" — the latter previously
  reported the former's message.

- Every reconcile now runs under a finite 60s deadline, bounding all
  Kubernetes API operations (previously unbounded; a stalled API request
  could consume the reconcile forever). The 30s per-sink deadline nests
  inside it.
- GCS writes are capped at 4 attempts (matching the webhook sink's
  bounded attempt count) within the existing 30s elapsed bound.
- docs/webhook-sink-protocol.md backoff schedule corrected to match the
  code (250ms/1s/3s, 4 total attempts).

- Sink credential Secrets are now read via a direct (uncached) API
  reader. Previously the first Secret read started a cluster-wide Secret
  informer, which the namespace-scoped Role correctly forbids — with
  sinks configured and `rbac.sinkSecretAccess=true`, config reload stalled
  (`Ready=True` stale at the prior observedGeneration) with repeated
  `secrets is forbidden` list errors. Found by AICR gate-3 qualification.
  The Role also narrows to `get` only.

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
