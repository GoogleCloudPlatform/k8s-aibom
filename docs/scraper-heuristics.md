# Scraper heuristics

This document records the extraction heuristics the `MultiScraper` applies across the entire AI lifecycle (Inference, Agents, Vector DBs, Training, Evals). Each heuristic is a deliberate choice; auditors and contributors reading a generated BOM should be able to map every value back to the rule that produced it.

The over-arching rule is "scrapers are honest, not clever" — v1 reports
what it directly observes and never normalizes, infers beyond pattern
matching, or guesses.



## Workload Categorization (The MultiScraper Pipeline)

The controller uses a chained pipeline of specialized scrapers. Workloads are categorized by the first scraper that returns a definitive signal (in this order):

1. **Inference (`inference.spec`)**: Matches recognized LLM serving runtimes (e.g., vLLM, Triton).
2. **Vector Databases (`vectordb.spec`)**: Matches known RAG storage engines (e.g., Milvus, Qdrant).
3. **Agent Frameworks (`agent.spec`)**: Matches low-code agent UIs (e.g., Langflow) or environment telemetry signatures (e.g., `LANGCHAIN_TRACING_V2`).
4. **Training & Fine-Tuning (`training.spec`)**: Matches known training frameworks (e.g., PyTorch, KubeRay) or MLOps signatures (e.g., `WANDB_API_KEY`).
5. **Evaluations (`eval.spec`)**: Matches evaluation jobs (e.g., lm-eval, Ragas).

## Agent & Remote API Heuristics
Agent workloads (unlike inference workloads) usually do not bundle local model weights; instead, they act as clients to remote Foundation Model APIs.
**Rule.** The `AgentSpecScraper` actively looks for injected API keys in the environment block (e.g., `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`). If found, it creates a `machine-learning-model` component representing the external API dependency, marked with `declared` confidence. The actual value of the key is **never** extracted or logged.

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
(PVC claim name, ConfigMap name, hostPath, etc.).

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
| **TGI in GHCR registry form.** `ghcr.io/huggingface/text-generation-inference:*` | Runtime stays `ConfidenceUnresolved`. The existing TGI pattern is anchored to the Docker Hub form (`^huggingface/text-generation-inference.*`) | Fix is registry-prefix work; deferred per conservative-detection until real customer signal identifies the deployed registry pattern set. |
| **TGI / TEI registry asymmetry.** TGI pattern targets Docker Hub form; TEI pattern targets GHCR form. | Customer running both via the same registry will get one detected, not the other. | Same root cause as the row above. Single-decision fix when registry-prefix expansion is approached. |
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
