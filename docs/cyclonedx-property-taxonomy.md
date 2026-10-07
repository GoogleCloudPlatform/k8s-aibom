# `k8s-aibom` CycloneDX Property Taxonomy

This is the official `k8s-aibom` property namespace and name taxonomy for
CycloneDX, registered in the
[CycloneDX Property Taxonomy](https://github.com/CycloneDX/cyclonedx-property-taxonomy)
and administered by the k8s-aibom maintainers. It follows that
repository's grammar: a top-level namespace, colon-separated
sub-namespaces, and a name.

The key words "MUST", "SHOULD" and "MAY" are to be interpreted as
described in [RFC 2119](https://datatracker.ietf.org/doc/html/rfc2119).

## Status and transition

k8s-aibom documents through v1.6 emit these facts under dotted names
(`aibom.workload.kind`, `runtime.name`, `signature.status`, …) that
predate the namespace registration. **From v1.7 the controller emits
the colon-delimited names in this document.** The rename changes
document bytes for identical inputs and ships in the same release as
the other CycloneDX-alignment changes (`pkg:oci` purls,
`metadata.lifecycles`), so downstream consumers see one byte-shape
transition. The mapping is given in the last section; within a release,
documents remain byte-deterministic.

Design principle, inherited from the project: every property is a
**fact observed from the Kubernetes API or declared by the workload
owner**, never a judgment. Confidence and evidence travel with each
fact so an auditor can trace it back without cluster access.

## Sub-namespaces

| Namespace | Description | Property of |
|-----------|-------------|-------------|
| `k8s-aibom:workload` | Identity of the Kubernetes workload the document describes | `metadata.component` |
| `k8s-aibom:controller` | The emitting controller | `metadata.properties` |
| `k8s-aibom:scrape` | Provenance of each scrape that contributed | `metadata.properties` |
| `k8s-aibom:evidence` | Where in the Kubernetes object a fact was read | components, services |
| `k8s-aibom:runtime` | Attributed serving/agent/training runtime | `application` components |
| `k8s-aibom:identity` | How a model identity claim was obtained | `machine-learning-model` components |
| `k8s-aibom:image` / `k8s-aibom:container` | Container image reference and container identity | `container` components |
| `k8s-aibom:volume` | Model storage mounted into a container | `data` components |
| `k8s-aibom:security:signing` | Model signature claim and verification outcome, relative to the project's published verification process | `machine-learning-model` components |
| `k8s-aibom:rollup` | Workloads absorbed into this document under the ownership roll-up | `metadata.properties` |
| `k8s-aibom:kserve`, `k8s-aibom:dynamo`, `k8s-aibom:nim`, `k8s-aibom:lws` | Facts specific to a watched third-party kind | components |

## `k8s-aibom:workload`

| Property | Description |
|----------|-------------|
| `k8s-aibom:workload:kind` | Kubernetes kind of the subject workload (e.g. `Deployment`, `DynamoGraphDeployment`). MUST occur once. |
| `k8s-aibom:workload:group` | API group of the subject workload (`apps`, `nvidia.com`, …). |
| `k8s-aibom:workload:apiVersion` | API version within the group. |
| `k8s-aibom:workload:namespace` | Kubernetes namespace. |
| `k8s-aibom:workload:name` | Object name. |
| `k8s-aibom:workload:uid` | Object UID; the stable identity across renames. |
| `k8s-aibom:workload:category` | Workload category: `inference`, `agent`, `training`, `evaluation`, `vectordb`, `pipeline`, `notebook`. |

## `k8s-aibom:controller` and `k8s-aibom:scrape`

| Property | Description |
|----------|-------------|
| `k8s-aibom:controller:name` | Emitting controller name (`k8s-aibom`). |
| `k8s-aibom:controller:version` | Emitting controller version. |
| `k8s-aibom:scrape:<i>:scraperName` | Name of the i-th scraper that contributed (`inference.spec`, `inference.dynamo`, …). Zero-based index; MAY occur once per scraper. |
| `k8s-aibom:scrape:<i>:scraperVersion` | That scraper's version. |
| `k8s-aibom:scrape:<i>:method` | How it read the object (`spec`). |
| `k8s-aibom:scrape:<i>:timestamp` | RFC 3339 scrape time. |

## `k8s-aibom:confidence` and `k8s-aibom:evidence`

| Property | Description |
|----------|-------------|
| `k8s-aibom:confidence` | On `metadata.properties`: aggregate confidence of the document. On a component: confidence of that fact. Values: `declared` (written by the workload owner), `inferred` (derived by heuristic), `verified` (declared and cryptographically verified), `unresolved`. |
| `k8s-aibom:evidence:source` | Source kind the fact was read from: `crd_field`, `container_arg`, `env_var`, `env_var_name_present`, `image_reference`, `image_pattern`, `image_label`, `pod_status`, `pod_annotation`, `pod_template_annotation`, `workload_annotation`, `volume_source`, `resource_request`, `node_selector`. |
| `k8s-aibom:evidence:locator` | Path within the Kubernetes object the fact was read from (e.g. `spec.components[1].modelRef.name`). |
| `k8s-aibom:redaction:applied` | Present when a field of this component was redacted at emission; value names the redaction class. Redaction is recorded, never silent. |
| `k8s-aibom:truncation:applied` | On `metadata.properties`, present when the per-document component cap dropped components; value `components-dropped:<n>`. |

## `k8s-aibom:runtime`, `k8s-aibom:identity`, `k8s-aibom:image`, `k8s-aibom:container`, `k8s-aibom:volume`, `k8s-aibom:model`

| Property | Description |
|----------|-------------|
| `k8s-aibom:runtime:name` | Attributed runtime (`vllm`, `triton`, `nim`, `langchain`, …). |
| `k8s-aibom:runtime:source` | How it was attributed (`kserve.predictor.model.modelFormat`, `dynamo.backendFramework`, `nimservice.kind`, …) when declared. |
| `k8s-aibom:runtime:pattern` | The image pattern that matched, when inferred. |
| `k8s-aibom:identity:confidence` | Always `claimed` for a model identity read from the workload; verification is expressed under `security:signing`. |
| `k8s-aibom:identity:source` | Where the identity came from when not a container field (`kserve.storageUri`, `dynamo.modelRef`, `nim.imagePath`). |
| `k8s-aibom:identity:envVarName` / `k8s-aibom:identity:argFlag` / `k8s-aibom:identity:annotation` | The env var, arg flag or annotation key that carried the identity. |
| `k8s-aibom:image:reference` | Image reference as written in the workload. |
| `k8s-aibom:image:repository` / `k8s-aibom:image:tag` | Repository and tag when the source API splits them (NIMService). |
| `k8s-aibom:container:name` | Container name within the pod template. |
| `k8s-aibom:container:init` | `true` for an init container. |
| `k8s-aibom:volume:name` / `k8s-aibom:volume:mountPath` / `k8s-aibom:volume:source` | Volume name, mount path and backing source kind for a model-storage mount. |
| `k8s-aibom:model:revision` | Model revision when the source API declares one. |

## `k8s-aibom:security:signing`

These properties are meaningful relative to the verification process
documented in
[docs/security-model.md](security-model.md) and Design 002; documents
link that process via an `externalReference`. They are never properties
of a model in isolation.

| Property | Description |
|----------|-------------|
| `k8s-aibom:security:signing:status` | `none`, `claimed` (a signature reference exists), `verified`. |
| `k8s-aibom:security:signing:outcome` | Verification outcome, distinguishing a signature valid against an operator trust root with a signer-identity constraint from one that is merely well-formed. |
| `k8s-aibom:security:signing:identity` | Verified signer identity. |
| `k8s-aibom:security:signing:rekorEntry` | Transparency-log entry reference. |
| `k8s-aibom:security:signing:reason` | Why verification did not succeed, when it did not. |
| `k8s-aibom:security:signing:subjectNameMismatch` | `true` when the signed subject name differs from the declared identity. |

## `k8s-aibom:rollup`

| Property | Description |
|----------|-------------|
| `k8s-aibom:rollup:owned:<i>` | `Kind/name` of a tracked workload absorbed into this document because the subject owns it (Design 005). Sorted; MAY occur once per absorbed workload. |

## Kind-specific sub-namespaces

| Property | Description |
|----------|-------------|
| `k8s-aibom:kserve:runtime:ref`, `k8s-aibom:kserve:serviceAccountName`, `k8s-aibom:kserve:storageUri` | KServe `InferenceService` declared facts. |
| `k8s-aibom:dynamo:backendFramework`, `k8s-aibom:dynamo:component:name`, `k8s-aibom:dynamo:component:type`, `k8s-aibom:dynamo:component:<name>:type`, `k8s-aibom:dynamo:role:name` | Dynamo graph topology and attribution. `<name>` is the graph component name as written by the owner. |
| `k8s-aibom:nim:storage:kind`, `k8s-aibom:nim:storage:nimCache:name`, `k8s-aibom:nim:storage:nimCache:profile`, `k8s-aibom:nim:storage:pvc:name`, `k8s-aibom:nim:storage:hostPath`, `k8s-aibom:nim:multiNode`, `k8s-aibom:nim:multiNode:backendType`, `k8s-aibom:nim:multiNode:parallelism:tensor`, `k8s-aibom:nim:multiNode:parallelism:pipeline`, `k8s-aibom:nim:inferencePlatform` | NIMService declared facts. |
| `k8s-aibom:lws:role`, `k8s-aibom:lws:size`, `k8s-aibom:lws:replicas` | LeaderWorkerSet role and group shape. |

## Mapping from pre-v1.7 names

| Through v1.6 | From v1.7 |
|---|---|
| `aibom.workload.*` | `k8s-aibom:workload:*` |
| `aibom.controller.*` | `k8s-aibom:controller:*` |
| `aibom.scrape.<i>.*` | `k8s-aibom:scrape:<i>:*` |
| `aibom.confidence` | `k8s-aibom:confidence` |
| `aibom.evidence.source` / `.locator` | `k8s-aibom:evidence:source` / `:locator` |
| `aibom.redaction.applied`, `aibom.truncation.applied` | `k8s-aibom:redaction:applied`, `k8s-aibom:truncation:applied` |
| `aibom.rollup.owned.<i>` | `k8s-aibom:rollup:owned:<i>` |
| `runtime.*`, `identity.*`, `image.*`, `container.*`, `volume.*`, `model.revision` | `k8s-aibom:runtime:*`, `k8s-aibom:identity:*`, `k8s-aibom:image:*`, `k8s-aibom:container:*`, `k8s-aibom:volume:*`, `k8s-aibom:model:revision` |
| `signature.*` | `k8s-aibom:security:signing:*` |
| `kserve.*`, `dynamo.*`, `nim.*`, `lws.*` | `k8s-aibom:kserve:*`, `k8s-aibom:dynamo:*`, `k8s-aibom:nim:*`, `k8s-aibom:lws:*` |

Facts that the CycloneDX core model expresses natively are not and
will not be properties: as-deployed context is `metadata.lifecycles`
(`phase: operations`), image identity is the component purl, and
hashes are `component.hashes`.
