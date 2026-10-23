# Changelog

All notable changes to k8s-aibom are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[Semantic Versioning](https://semver.org/) (see `VERSIONING.md`).

## [Unreleased]

### Added

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

### Fixed

- **`kubectl aibom` rejects arguments a subcommand does not use**
  (#235). `kubectl aibom find vllm` listed every AIBOM unfiltered,
  `summary prod-jobs` summarized the current namespace instead of
  `prod-jobs`, `summary --runtime vllm` ignored the filter, and `view
  NAME -A` sent a cluster-scoped GET that came back 404. These are now
  argument errors, reported before the kubeconfig is loaded, with a
  hint (`filters are flags, e.g. --runtime vllm`). `-n`, `--kubeconfig`
  and `--context` still apply to every subcommand.

- **AIBOM conditions keep their `lastTransitionTime` when their status
  does not change** (#231). A full rebuild stamped `Ready`, `Synced`,
  `SinkFailed` and `Stale` with the reconcile time every time, so while
  a sink failed and was retried every minute, `SinkFailed=True` always
  looked less than a minute old and "failing since" could not be read
  from the status. The full path now keeps the previous time for a
  condition whose status is the same, as the dedup fast path and the
  scrape/build failure path already did; a condition that flips still
  gets the reconcile time.

- **A dataset mount is reported once when a Job matches both
  `training.spec` and `eval.spec`** (#232). Both scrapers report
  dataset mounts since #218, and the multi-scraper appended their
  results without deduplicating, so an lm-eval Job that logs to
  Weights & Biases (`WANDB_API_KEY`) listed each mount twice. Merged
  results are now deduplicated on type, name, version, evidence source
  and locator; a single scraper's result is unchanged.

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
