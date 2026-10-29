# Scraper heuristics

This document records the extraction heuristics the `MultiScraper` applies across the entire AI lifecycle (Inference, Agents, Vector DBs, Training, Evals). Each heuristic is a deliberate choice; auditors and contributors reading a generated BOM should be able to map every value back to the rule that produced it.

The over-arching rule is "scrapers are honest, not clever" — v1 reports
what it directly observes and never normalizes, infers beyond pattern
matching, or guesses.



## Workload Categorization (The MultiScraper Pipeline)

The controller runs a set of specialized scrapers over each workload. Every scraper that handles the workload's kind runs, and the results of all the scrapers that find a signal are merged into one BOM:

1. **Inference (`inference.spec`)**: Matches recognized LLM serving runtimes (e.g., vLLM, Triton).
2. **Vector Databases (`vectordb.spec`)**: Matches known RAG storage engines (e.g., Milvus, Qdrant).
3. **Agent Frameworks (`agent.spec`)**: Matches low-code agent UIs (e.g., Langflow) or environment telemetry signatures (e.g., `LANGCHAIN_TRACING_V2`).
4. **Training & Fine-Tuning (`training.spec`)**: Matches known training frameworks (e.g., PyTorch, KubeRay) or MLOps signatures (e.g., `WANDB_API_KEY`).
5. **Evaluations (`eval.spec`)**: Matches evaluation jobs (e.g., lm-eval, Ragas).

When more than one scraper reports a category, the workload takes the one with the highest priority, whatever the order above: `agent`, then `training`, then `evaluation`, then `vector-database`, then `inference`. A vLLM Deployment that also sets `LANGCHAIN_TRACING_V2` is categorized `agent`, and its BOM lists both the vLLM runtime and the LangChain framework. A workload that no scraper gives a category is categorized `inference`.

## Vector Database Heuristics
**Rule.** The `vector-database` category is for systems whose primary function is embedding storage and approximate nearest-neighbour search, so that the image alone is an AI signal. Milvus, Qdrant, Weaviate, Chroma and Vespa qualify on that reading, and so does a `pgvector` image, a PostgreSQL build made for vector search (a plain `postgres` image is not matched). Elasticsearch, OpenSearch and Redis do not qualify: vector search is one feature among many there, and the image usually means logs, search or caching.

Each pattern is anchored at the image name, so a project's build, development and tooling images (for example `vespaengine/vespa-build-almalinux-8` or `vespaengine/vespacli`) are not reported as a vector database.

## Agent & Remote API Heuristics
Agent workloads (unlike inference workloads) usually do not bundle local model weights; instead, they act as clients to remote Foundation Model APIs.
**Rule.** The `AgentSpecScraper` actively looks for injected API keys in the environment block (e.g., `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`). If found, it creates a `machine-learning-model` component representing the external API dependency (e.g., `openai-api`). Its confidence is `inferred` when the variable has a literal value, and `unresolved` when it is set through `valueFrom` (a Secret or ConfigMap reference). The actual value of the key is **never** extracted or logged.

An API key alone does not categorize a workload as `agent`: that takes a framework image (e.g., Langflow) or a framework environment signature (e.g., `LANGCHAIN_TRACING_V2`). A workload with only an API key keeps the category another scraper gives it, `inference` by default.

## Training Dataset Heuristics
**Rule.** For Training and Evaluation jobs, the `TrainingSpecScraper` inspects `volumeMounts` mapped to traditional data directories (like `/data` or named `dataset`). It exposes these as `data` components in the BOM, allowing compliance teams to trace exactly which storage volumes a model was trained on.

## Image digest resolution

**Rule.** The container image digest comes from one of two sources, in
order:

1. The spec image reference itself if it carries a `@sha256:` suffix
   (e.g., `vllm/vllm-openai:v0.6.3@sha256:abc...`). Evidence source:
   `image_reference`.
2. Otherwise, `pod.status.containerStatuses[].imageID` for the matching
   container in a running pod. Evidence source: `pod_status`.

If neither source resolves, the container Component's `Confidence` is
`unresolved` and `Hashes` is omitted. The controller **never fabricates a
digest**. v1 does NOT query the registry directly; that path is deferred to
v2.

When the pod is running an older image than the workload spec (rollout in
progress, paused, or failed), the BOM reports the **running** digest, not
anything derived from the spec. The BOM describes what is actually
running.

When the workload's pods report different digests for the same container
(a rollout in progress, or a moving tag such as `:latest` pulled at
different times), the BOM reports the digest the most pods run; a tie
goes to the lowest digest. The choice does not depend on the order the
pods are listed in, so the document and its input hash stay the same
from one reconcile to the next while the set of running digests does.

## KServe vs. inference-spec: confidence semantics

The project ships two inference-shaped scrapers with deliberately
different confidence semantics. Auditors reading workload-level
confidence should interpret the value in context of which scraper
produced it (see the `aibom.scrape.0.scraperName` property on every
BOM):

- **`inference.spec`** (Deployment / StatefulSet / DaemonSet).
  Runtime is **inferred** via image-pattern match against an
  allowlist (vllm, tgi, triton, ollama, ray-serve, llm-d). The
  customer wrote an image string; the controller guessed the runtime
  from it. Confidence: `inferred`.

- **`inference.kserve`** (KServe InferenceService).
  Runtime is **declared** via the customer-authored
  `spec.predictor.model.modelFormat.name` field. The customer
  explicitly named the framework (pytorch, tensorflow, sklearn,
  etc.). Confidence: `declared`.

Same model identity extraction, two different confidence values.
"Declared" means customer-wrote-it-down; "inferred" means
controller-guessed-from-signal. Most projects flatten these; keeping
them distinct is part of what makes the BOM useful for audit.

## KServe image digest resolution (v1 limitation)

v1 KServe scraper extracts the declared InferenceService spec only.
**Image digests are not resolved** for KServe workloads because
KServe materializes pods indirectly via its own controller (the
InferenceService is not the direct parent of the running pod;
KServe-managed Deployment/ReplicaSet sit between). Walking that
chain to a `corev1.Pod.status.containerStatuses[].imageID` is
deferred to v2.

A v1 BOM for a KServe workload will therefore have no
`container`-type Component with a `sha256` hash. The runtime
application Component and the model identity Component are still
present and declared. v2 KServe extraction (when it lands) is
expected to be named `inference.kserve.deep` to preserve the v1
scraper identity in historical BOMs.

## Workload-level confidence aggregation

**Rule.** The workload-level `Confidence` on `BOMInputs` is computed as
follows:

- Attributes with `unresolved` confidence are **excluded** from the
  aggregation.
- The workload-level confidence is the **lowest tier** among the
  non-excluded attributes. Tiers: `declared` > `inferred`.
- If every attribute is `unresolved`, the workload-level confidence is
  `unresolved` and the discovery layer decides whether to suppress the
  workload entirely.

Concretely: any single `inferred` attribute drags the workload to
`inferred`. Only when every non-excluded attribute is `declared` does the
workload report `declared`.

## Runtime detection and runtime version

**Rule.** A container image that matches one of the configured runtime
image patterns produces an additional `application`-class Component
naming the runtime (e.g., `vllm`, `tgi`, `triton`).

The TGI, Triton, Ollama, SGLang, LMDeploy, TEI and LiteLLM patterns
match at the image-name boundary: a tag, a digest or the end of the
reference. Supported SGLang and LiteLLM image variants are enumerated
in the allowlist; names such as `lmsysorg/sglang-router` and
`nvcr.io/nvidia/tritonserver-sidecar` do not receive runtime attribution.

- **Confidence: `inferred`.** The runtime identity is inferred from a
  pattern match against the image string, not declared by the workload
  owner. Pattern-match-derived attributes are never `declared`.
- **Version: image tag only if semver-shaped.** The version field is
  populated with the image tag if the tag matches the pattern
  `^v?\d+\.\d+(\.\d+)?(-[\w.]+)?$` (i.e., `v0.6.3`, `0.6.3`, `v1.0`,
  `24.01-py3`, `v0.6.3-rc1`). Tags like `latest`, `nightly-20251025`,
  `main`, or git SHAs leave the version empty. The raw tag is preserved
  in the Component's `image.tag` property for traceability.

Putting `latest` in a version field would mislead auditors who reasonably
expect a version field to carry semantic version information. Empty is
more honest.

If a future scraper extracts runtime version from a more authoritative
source — an OCI image label, a runtime self-report endpoint, an attestation
— that's a different evidence source and may be `declared`.

## Init containers

**Rule.** v1 captures init containers' container images (Component
`container`) but does NOT scrape their args or env vars for model claims.

Init containers in inference workloads are typically used for model
downloading, sidecar setup, or readiness gating. Treating their args or
env vars as model claims would conflate "this is what's being deployed"
with "this is something that ran during deployment." The image is captured
because it's useful for fleet visibility (e.g., a vulnerable model-loader
init container still warrants inventory).

v2 may revisit this when eBPF-based scraping makes init-container activity
observable in a more authoritative way.

## Model claims via env vars

The three model allowlists used below (`modelEnvVarNames`,
`modelArgFlags`, `modelVolumePathPrefixes`) are compiled into the
controller from
[`internal/scraper/v1-runtime-patterns.yaml`](../internal/scraper/v1-runtime-patterns.yaml).
The `AIBOMControllerConfig` CR does not expose them: it overrides only
the runtime image patterns (`spec.discovery.inferenceRuntimeImagePatterns`).
Extending an allowlist is a PR to that file; see
[contributing a pattern](contributing-a-pattern.md).

**Rule.** Container env vars whose `name` matches the configured
`modelEnvVarNames` allowlist contribute `machine-learning-model`
Components. The env var VALUE becomes the model identity; the env var NAME
is recorded in the locator.

- Empty values are not emitted (an empty env var is not a model identity).
- Match is case-sensitive (`HF_MODEL_ID` is in the allowlist;
  `hf_model_id` is not).
- Confidence: `inferred`. The env var convention is a community pattern,
  not a Kubernetes-native declaration.

## Model claims via container args

**Rule.** Container args whose flag name (the part before the value)
matches the configured `modelArgFlags` allowlist contribute
`machine-learning-model` Components. Both forms are recognized:

- Positional: `args: ["--model", "meta-llama/Llama-3.1-8B-Instruct"]` —
  the next arg is the value.
- Joined: `args: ["--model=meta-llama/Llama-3.1-8B-Instruct"]` — the part
  after `=` is the value.

A flag with no following value, or a value that itself starts with `--`,
is treated as "no value present" and emits nothing.

Confidence: `declared`. The workload owner explicitly invoked the runtime
with this model identity.

## Model claims via volume mounts

**Rule.** Container volume mounts whose `mountPath` matches one of the
configured `modelVolumePathPrefixes` (prefix-at-path-boundary match)
contribute `data`-class Components naming the volume's backing source
(PVC claim name, ConfigMap name, hostPath, the image reference of an
`image` volume, etc.).

Boundary match: `/models` matches `/models` and `/models/llama` but NOT
`/models-shared` (because `/models-` is not a path boundary).

Confidence: `inferred`. A volume mounted at `/models` is conventionally
model weights, but the controller cannot verify the volume contents.

## Model claims via annotations

**Rule.** Annotations whose keys begin with `model.k8saibom.dev/` contribute
`machine-learning-model` Components. The annotation VALUE becomes the
model identity claim.

Two distinct evidence sources are emitted:

- `workload_annotation`: annotations on the workload object
  (`metadata.annotations` on the Deployment/StatefulSet/etc.).
- `pod_template_annotation`: annotations on
  `spec.template.metadata.annotations`. These propagate to every pod
  replica.

A future v2 may add `pod_annotation` for annotations injected at pod
creation by mutating webhooks (i.e., annotations present on the live pod
but not in the workload spec). The constant exists today; the scraper does
not currently emit it.

Confidence: `declared`. The workload owner authored the annotation
explicitly.

## Known false negatives (deliberate, deferred)

These are detection cases v1 deliberately does NOT match, despite
appearances. Each is a deferred-by-design choice, not an
implementation gap. Future contributors arriving at one of these
expecting to "fix" it should read this section AND the
[conservative-detection principle](../internal/scraper/v1-runtime-patterns.yaml)
documented inline in the runtime-patterns YAML before opening a PR —
the asymmetry between false positives and false negatives in
auditor-facing output makes speculative pattern broadening dangerous.

| Case | Behavior | Why deferred |
|---|---|---|
| ~~**TGI in GHCR registry form.**~~ | **RESOLVED in v1.4.0** — real deployment signal arrived and `^ghcr\.io/huggingface/text-generation-inference.*` shipped (the publisher's own namespace, not a mirror pattern). Kept here as the worked example of the fix process below. | — |
| ~~**TGI / TEI registry asymmetry.**~~ | **RESOLVED in v1.4.0** by the row above — both now cover the publisher's GHCR namespace. | — |
| **Image mirrors with embedded vendor names.** `<some-private-mirror>/<some-namespace>/vllm-toolkit:tag` or similar | No match (current `^vllm/.*` requires the image to START with `vllm/`) | Anchored matching deliberately rejects mirrored / repackaged images. Broadening risks false positives (e.g., `customer-mirror/not-vllm/vllm-toolkit:tag` contains "vllm" but isn't vLLM). Deferred pending mirroring-pattern survey. |
| **TensorRT-LLM standalone images.** | Production-shipping form `nvcr.io/nvidia/tritonserver:*-trtllm-*` is covered by the Triton pattern. A separate TensorRT-LLM pattern is NOT added. | Adding TensorRT-LLM separately would double-count the same workload. If a future shipping form is genuinely distinct, add it then. |

**Fix process when a deferred case has real customer signal:**

1. Confirm the case is from a real customer install (not speculation).
2. Identify the specific registry prefix or image form involved.
3. Propose the narrowest pattern that covers the customer case without
   broadening to obvious confusables.
4. Add positive AND negative test cases — including a case asserting
   the confusable does NOT match.
5. File the change as a deliberate pattern-expansion phase, not as
   inline cleanup.

## What v1 does NOT scrape

To make the boundary of v1's spec-driven extraction explicit:

- **The container `command` field** (separate from `args`). v1 only
  inspects `args`.
- **Mutating-webhook-injected fields** on live pods that don't appear in
  the workload spec. v1 reads pod *status* (specifically `containerStatuses`
  for image digests), not arbitrary pod *spec* fields.
- **Anything fetched at runtime.** A workload that downloads a model from
  an arbitrary URL inside its container entrypoint is invisible to v1.
  This is the runtime-model-fetch-bypass limit documented in
  `docs/threat-model.md` §5.
- **OCI image labels.** The `SourceImageLabel` constant exists but v1 does
  not emit it. Reading image labels requires registry queries or image
  introspection, both deferred to v2.
- **Network egress destinations.** PRD FR2.10 mentions egress observation
  via service mesh or CNI flow logs as a hint; v1 does not emit it.
