# k8s-aibom

A Kubernetes controller that generates [CycloneDX 1.6 ML-BOM][cyclonedx-ml] documents for AI workloads at runtime — inference services, agent stacks, RAG pipelines, training jobs, evaluation harnesses — with auditor-traceable evidence for every attribute.

[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)
[![CycloneDX 1.6 ML-BOM](https://img.shields.io/badge/CycloneDX-1.6%20ML--BOM-success.svg)](https://cyclonedx.org/capabilities/mlbom/)
[![Static Analysis](https://github.com/GoogleCloudPlatform/k8s-aibom/actions/workflows/static-analysis.yml/badge.svg)](https://github.com/GoogleCloudPlatform/k8s-aibom/actions/workflows/static-analysis.yml)


> **Status:** v1.0 — alpha. Production-suitable for non-critical observation use cases. Stable APIs through v1.x. Feedback welcome.

---

## The problem

Organizations running AI workloads on Kubernetes — inference services, agent applications, RAG systems, training jobs — face a growing requirement to produce auditable inventories of *what is actually running in production*. Build-time AI bill-of-materials tools describe what was intended to be deployed. Runtime tools describe what is in fact serving inference, calling external APIs, holding embeddings, or training on which datasets.

This distinction matters in any environment subject to AI governance: [EU AI Act][eu-ai-act] Article 12 logging requirements and Article 50 transparency obligations; [NIST AI RMF][nist-ai-rmf] Govern/Map/Measure/Manage controls that require knowing what AI systems are deployed and how; [ISO/IEC 42001][iso-42001] inventory and lifecycle clauses. Each of these requires evidence about deployed systems, not just intentions.

k8s-aibom produces that evidence as a side effect of normal cluster operation. Install the controller, opt in a namespace, and CycloneDX 1.6 ML-BOM documents are produced for each AI workload, with explicit confidence flags and evidence locators on every attribute.

## What it observes

k8s-aibom recognizes workload kinds across the AI lifecycle and applies category-specific scrapers to extract relevant attributes.

**Inference services.** Deployments, StatefulSets, DaemonSets, and KServe `InferenceService` resources serving model inference. The controller detects the serving runtime (vLLM, Hugging Face TGI, NVIDIA Triton, Ollama, Ray Serve, llm-d, SGLang, LMDeploy, HuggingFace TEI), the container image and resolved digest, and the claimed model identity from container args, environment variables, mounted volume sources, or workload annotations.

**Agent stacks.** Workloads running agent frameworks (LangChain / LangGraph, AutoGen, CrewAI, Langflow, Flowise, Chainlit). The controller extracts the framework version, the external LLM API dependencies declared via environment variables (OpenAI, Anthropic, Google, Cohere), and telemetry signatures that indicate observability integration (LangSmith, LangChain tracing).

**Vector databases and RAG infrastructure.** Workloads running Milvus, Qdrant, Weaviate, Chroma, or pgvector. The controller extracts the vector store identity, configured collections where determinable, and the workload's relationship to RAG-related dependencies.

**Training and fine-tuning jobs.** Kubernetes Jobs and CronJobs running PyTorch, KubeRay, JAX, or Hugging Face Accelerate workloads. The controller extracts mounted training datasets via volume specifications, telemetry integrations (Weights & Biases, Hugging Face Hub), and runtime/framework version information.

**Evaluation harnesses.** Workloads running `lm-evaluation-harness`, Ragas, or Trulens. The controller extracts evaluation framework identity and configured benchmark suites where determinable.

## How it works

```
┌────────────────────────────────────────────────────────────────────┐
│ Kubernetes cluster                                                 │
│                                                                    │
│  ┌─────────────────────┐         ┌──────────────────────────────┐  │
│  │ k8s-aibom-system    │         │ Opted-in namespaces          │  │
│  │                     │ watches │                              │  │
│  │  Controller         ├────────►│  Inference / Agents / RAG /  │  │
│  │  (Deployment)       │         │  Training / Evaluation       │  │
│  │                     │         │                              │  │
│  └──────────┬──────────┘         └──────────────────────────────┘  │
│             │                                                      │
│             │ produces                                             │
│             ▼                                                      │
│  ┌─────────────────────┐                                           │
│  │ AIBOM CR            │ (one per workload, namespace-scoped)      │
│  │  status.bomDocument │                                           │
│  └─────────────────────┘                                           │
│                                                                    │
└─────────────┬──────────────────────────────────────────────────────┘
              │
              │ optional external sinks
              ▼
   ┌──────────────────────┬──────────────────────┐
   │ GCS bucket           │ Webhook endpoint     │
   │ (full BOM JSON)      │ (POST per BOM)       │
   └──────────────────────┴──────────────────────┘
```

The controller runs as a single Deployment in its own namespace. It requires no DaemonSet, no privileged container, no kernel access, no node-level agent. It observes via the standard Kubernetes API.

For each tracked workload, the controller scrapes the workload spec and current pod status, applies category-specific detection heuristics, builds a CycloneDX 1.6 ML-BOM, and writes the BOM to all configured sinks. Identical inputs produce byte-identical BOMs (modulo timestamps), so output is suitable for content-addressable storage and diff-based change tracking.

## The confidence model

Every attribute in a k8s-aibom BOM carries a confidence flag and an evidence locator. This is the difference between a useful BOM and a misleading one.

- **`declared`** — the customer wrote this into their workload spec. A `--model` container arg is declared. An `HF_MODEL_ID` set on the container is declared. A model name in a `model.k8saibom.dev/name` annotation is declared.
- **`inferred`** — the controller derived this from a heuristic. The runtime name `vllm` derived from matching the image against `^vllm/.*` is inferred. The agent framework identified from a `langchain` import pattern in container args is inferred.
- **`unresolved`** — the controller could not determine the value with confidence. The image digest of a pod that has not pulled yet is unresolved.

For compliance reviewers, this distinction is the entire point: a BOM that says "this workload runs vLLM and serves Phi-3-mini" is dramatically more useful when the reviewer can tell at a glance which parts of that claim are the customer's own declaration versus the controller's pattern-matching inference.

The v1.1 roadmap extends the confidence model with cryptographic verification — `verified` for model identities backed by a [Sigstore][sigstore] / OMS signature with a valid Rekor entry. The v1.0 codebase ships with the verification interface in place but uses a `NoopVerifier` that never marks anything verified, leaving signing for v1.1.

## Compliance framework mapping

k8s-aibom outputs are designed to serve as evidence for the following framework requirements:

- **EU AI Act Article 12** — Logging requirements for high-risk AI systems. The AIBOM CR's per-workload status and the immutable BOM archive in external sinks provide the per-system inventory and logging artifacts the article requires.
- **EU AI Act Article 50** — Transparency obligations. The model identity, runtime, and provenance attributes in each BOM support the disclosure obligations applicable to deployers of general-purpose AI systems.
- **NIST AI RMF (Govern, Map, Measure, Manage)** — Several measures across the framework require maintaining an inventory of AI systems and tracking changes. k8s-aibom produces and maintains that inventory automatically.
- **ISO/IEC 42001** — AI Management System inventory and lifecycle clauses. The BOM's confidence model and evidence locators provide the auditable lineage the standard's certification path requires.

k8s-aibom does not certify compliance with any framework; it produces evidence that organizations can use as inputs to their compliance processes.

## Quickstart (Bring Your Own Image)

> **Prerequisites:** A Kubernetes cluster (1.27+), `kubectl` configured to talk to it, Docker, and Helm 3.

Google does **not** host a pre-built container image or Helm repository for `k8s-aibom`. You must build the image and push it to your own container registry before deploying.

> [!WARNING]
> **API and Billing Requirements:** The Zero-Install paths below (Cloud Shell and Terraform) utilize Google Cloud Build and Artifact Registry to automatically build and host your image. This requires the `cloudbuild.googleapis.com` and `artifactregistry.googleapis.com` APIs to be enabled on your GCP project and will incur standard billing costs.

### 1. Zero-Install via Google Cloud Shell (Recommended for GCP)

The fastest way to evaluate `k8s-aibom` is to use our interactive Cloud Shell tutorial. This automatically provisions a workspace in your browser, builds the image using Cloud Build, and deploys the controller to your cluster.

[![Open in Cloud Shell](https://gstatic.com/cloudssh/images/open-btn.svg)](https://ssh.cloud.google.com/cloudshell/editor?cloudshell_git_repo=https://github.com/GoogleCloudPlatform/k8s-aibom&cloudshell_tutorial=.cloudshell/tutorial.md)

> [!NOTE]
> If the interactive tutorial panel does not open automatically on the right side of your screen, you can manually launch it by running this command in the Cloud Shell terminal:
> ```bash
> teachme .cloudshell/tutorial.md
> ```


### 2. Infrastructure-as-Code via Terraform

For enterprise GitOps deployments, we provide a fully automated Terraform module that provisions an Artifact Registry repository, runs `gcloud builds submit` to compile the image, and deploys the Helm chart.

See the [Terraform Automation Guide](terraform/README.md) to get started.

### 3. Manual Deployment (Docker + Helm)

If you are not using GCP or prefer to build locally:

```bash
git clone https://github.com/GoogleCloudPlatform/k8s-aibom.git
cd k8s-aibom

# Set your target registry
export IMG=my-registry.example.com/k8s-aibom:v1.0.0

# Build and push the image
make image
make docker-push
```

> [!WARNING]
> **Platform Architecture Mismatch:** `make image` builds the container for your host's native architecture. If you build on an Apple Silicon (M1/M2) Mac, you will produce a `linux/arm64` image. If you deploy this to a standard AMD64 Kubernetes cluster, the pod will CrashLoop with an `exec format error`. Use `make image-multiarch` to safely cross-compile. (Note: The Cloud Shell and Terraform paths bypass this issue by natively building on AMD64 Cloud Build runners).

Deploy the local chart to your cluster, injecting your registry path:

```bash
helm install k8s-aibom ./charts/k8s-aibom \
  --namespace k8s-aibom-system \
  --create-namespace \
  --set image.repository=my-registry.example.com/k8s-aibom \
  --set image.tag=v1.0.0
```

> [!NOTE]
> **Private Registries:** If your registry requires authentication, add `--set imagePullSecrets[0].name=my-secret` to the Helm command.

### Opt in a namespace

The controller is installed cluster-wide but inactive until you opt in at least one namespace:

```bash
kubectl label namespace my-ai-namespace aibom.k8saibom.dev/enabled=true
```

### See the BOMs

```bash
kubectl get aibom -A
```

You will see one `AIBOM` resource per tracked workload. To see the BOM itself:

```bash
kubectl describe aibom -n my-ai-namespace deployment-my-workload
```

The full CycloneDX BOM is inline in `status.bomDocument` for BOMs under 256 KB, or referenced via URL for larger BOMs.

## Configuring external sinks

By default, BOMs are stored only in the AIBOM CR's status — no data leaves the cluster. To configure external sinks, edit the `AIBOMControllerConfig` named `default`:

```yaml
apiVersion: aibom.k8saibom.dev/v1alpha1
kind: AIBOMControllerConfig
metadata:
  name: default
spec:
  sinks:
    - name: audit-archive
      type: GCS
      gcs:
        bucket: my-aibom-archive
        pathTemplate: "aibom/{namespace}/{kind}-{name}/{timestamp}.json"
        workloadIdentity: k8s-aibom-controller@my-project.iam.gserviceaccount.com
    - name: graph-ingest
      type: Webhook
      webhook:
        endpoint: https://guac.internal.example.com/ingest
        auth:
          bearerToken:
            secretRef:
              name: graph-ingest-creds
              key: token
```

Configuration changes take effect on the next reconcile, without restarting the controller. Invalid configuration is rejected; the previous good configuration remains in effect, and the failure is surfaced as a condition on the `AIBOMControllerConfig` CR. This is the *last-known-good* property: an operator mistake in the configuration does not break the pipeline.

## Security model

The controller is the *only* identity in the system that writes BOMs to external sinks. This is enforced structurally:

- The controller runs as a single ServiceAccount with minimum permissions: `roles/storage.objectCreator` on the target GCS bucket (no `objectViewer`, no `objectAdmin`, no bucket-level admin). On non-GCP environments, equivalent minimum permissions via Workload Identity Federation.
- Webhook sink credentials are loaded from Kubernetes Secrets in the controller's namespace. The controller does not read Secrets from other namespaces. Customer workload pods cannot read these Secrets.
- The GCS sink uses `DoesNotExist` preconditions on every write, making BOM objects immutable once written. A second write to the same path fails by design — the audit trail cannot be silently overwritten.

This bounds the blast radius of a compromised AI workload: it cannot tamper with audit BOMs. The single-principal write pattern also produces a clean signature in cloud audit logs.

See [docs/security-model.md](docs/security-model.md) for the full threat model and design justification.

## Engineering discipline

The project is built around a few load-bearing conventions documented in [CONTRIBUTING.md](CONTRIBUTING.md):

- **316 tests** across unit, envtest, and real-cluster smoke layers, with substring-asserted error messages, fallback-path-first ordering, and the customer-protection properties (last-known-good config retention) tested at all three layers.
- **Conservative-detection principle** — the controller prefers false negatives (an honest "unresolved") to false positives (a fabricated "declared"). Detection patterns are added only when there is clear signal, not speculatively.
- **Cloud-neutrality constraint** — the project runs on any conformant Kubernetes cluster. Google-specific dependencies are limited to the optional GCS sink; all other code paths work identically on EKS, AKS, on-prem, or local clusters.
- **Real-cluster smoke verification** — every release is verified end-to-end on a real GKE cluster against the documented properties before tagging, not solely via envtest.

## Performance footprint

Production measurements on a real GKE cluster with the v1.0 release:

- Controller CPU at rest: ~15m
- Controller memory at rest: ~85Mi
- Per-reconcile work for a single workload: low single-digit milliseconds
- 256 KB inline threshold; BOMs exceeding the threshold are offloaded to an external sink and referenced by URL in the CR status

The controller is comfortable on a cluster with a few hundred AI workloads. For clusters at meaningful scale (1,000+ workloads), please monitor the controller's memory usage and adjust the resources as necessary.

## Compatibility

**Kubernetes versions.** Tested against Kubernetes 1.27 through 1.35. The controller uses only stable APIs; older versions back to 1.23 should work but are not actively tested.

**Cloud platforms.** Runs on Google Kubernetes Engine (Standard and Autopilot), Amazon Elastic Kubernetes Service, Azure Kubernetes Service, on-premises clusters (kubeadm, kops, Rancher, OpenShift), and local development clusters (kind, minikube, k3s). The GCS sink requires Google Cloud authentication; the webhook sink works against any HTTPS endpoint; the CRD status sink works on any conformant cluster.

**Service meshes.** Compatible with Istio, Linkerd, and Cilium without mesh-specific code.

## Roadmap

### v1.x — stable API series

- **v1.1** — Native GUAC sink for OpenSSF GUAC ingestion; Sigstore / OMS signature verification for model identities (`verified` confidence); admission webhook for `AIBOMControllerConfig` singleton enforcement; configurable workload-kind allowlist via the CR.
- **v1.2** — Additional CRD scrapers (llm-d native CRDs, KAITO, Seldon Core); deep KServe extraction following `ServingRuntime` references; expanded agent framework coverage (Semantic Kernel, Haystack, DSPy).
- **v1.3** — Active registry digest resolution for mutable image tags; image SBOM extraction from Artifact Registry; hardware (GPU/TPU) extraction from resource requests and node selectors.

### v2 — Phase 2 capability tier

- eBPF-based scraper for higher-fidelity attribute extraction: in-container model load events, egress destination capture, runtime version verification against running processes.
- Native SPDX 3.0 AI profile emission alongside CycloneDX.
- Service mesh telemetry integration (Istio / Linkerd / Cilium) for network posture in the BOM.
- Upstream CycloneDX profile contribution — a "Kubernetes runtime ML-BOM profile" codifying the conventions developed in v1.x as a CycloneDX upstream specification.

## Relationship to other projects

k8s-aibom complements, rather than replaces, the broader AI supply-chain transparency ecosystem.

- **[OWASP CycloneDX][cyclonedx]** defines the BOM schema. k8s-aibom emits CycloneDX 1.6 ML-BOM documents that validate against the official schema and use the project's ML-BOM extensions for model identity and runtime metadata.
- **[OWASP AIBOM Project][owasp-aibom]** is standardizing AIBOM concepts at the framework level. k8s-aibom is a Kubernetes-runtime implementation of those concepts.
- **[OpenSSF GUAC][guac]** aggregates and graphs supply-chain metadata. The v1.1 native GUAC sink will publish BOMs directly to GUAC's ingestion path; the v1.0 webhook sink can be pointed at a GUAC blob-storage collector for the same result with one additional hop.
- **[Sigstore][sigstore]** provides the signing and verification infrastructure that the v1.1 `verified` confidence level depends on.
- **Build-time AIBOM tools** (AIBoMGen, OWASP AIBOM Generator, vendor tools) describe what was built. k8s-aibom describes what is running. Both are needed for full supply-chain visibility.

## Contributing

Issues, pull requests, and feedback welcome. See [CONTRIBUTING.md](CONTRIBUTING.md) for development workflow, testing discipline, and the conventions the project is built around.

Areas where contributions are particularly valuable:

- **Additional runtime, agent framework, and infrastructure patterns.** If you run a workload kind not currently detected, a PR adding the detection pattern with a regression test is the fastest path to coverage.
- **Customer-realistic edge cases.** Production workloads have shapes that synthetic tests do not cover. Issues describing "the controller does not detect X in our setup, here is the workload spec" are extremely useful.
- **Schema and confidence-model feedback.** The BOM property naming conventions and confidence flag definitions are open to refinement. See [docs/schema-divergences.md](docs/schema-divergences.md) for current conventions.

## Governance

k8s-aibom is published by Google under the Apache 2.0 license. The project welcomes contributions from any individual or organization under the standard Google Contributor License Agreement (CLA).

Kubernetes and K8s are registered trademarks of The Linux Foundation in the United States and other countries.

## License

Apache 2.0. See [LICENSE](LICENSE).

---

[cyclonedx]: https://cyclonedx.org
[cyclonedx-ml]: https://cyclonedx.org/capabilities/mlbom/
[owasp-aibom]: https://owaspaibom.org
[guac]: https://guac.sh
[sigstore]: https://sigstore.dev
[eu-ai-act]: https://artificialintelligenceact.eu/
[nist-ai-rmf]: https://www.nist.gov/itl/ai-risk-management-framework
[iso-42001]: https://www.iso.org/standard/81230.html

## Disclaimer
This is not an officially supported Google product. This project is not eligible for the [Google Open Source Software Vulnerability Rewards Program](https://bughunters.google.com/open-source-security).
