# Design 008: model identity as a metrics dimension

Status: Proposed 2026-11-19. Targets v1.8.0. Review on the tracking issue.

## Context

k8s-aibom is the only component in a cluster that knows, for every AI
workload and without privilege, which model is being served on which
runtime. Today that knowledge leaves the controller in one form: the
CycloneDX document and its status summary. A second form is cheap and
answers a question platform teams are asked every month and cannot
answer from existing metrics: what does each model cost us, and how much
of our accelerator capacity does each one use?

The join everyone performs today is `utilisation per pod` (cAdvisor,
DCGM, TPU metrics, all keyed by namespace and pod) multiplied onto `pod
to model`, and the second factor has no source. Teams reconstruct it
with a hand-applied pod label or from the serving engine's own
`model_name` metric label. Evidence gathered in November 2026 (see the
notes under the tracking issue):

- OpenCost shipped per-model inference cost in July 2026 (PR #3845,
  IBM; follow-ups from Kubecost and others through November; llm-d
  ships an install recipe). Model identity comes from a pod label whose
  value must equal vLLM's `model_name` label, label values cannot
  contain `/`, and the module is vLLM-only. Its tracker shows the join
  failing in real use when labels disagree across namespaces.
- Cast.ai's guidance for GPU cost per model is "apply a `model-name`
  label to every inference deployment and join to node cost". Datadog,
  Kubecost, Vantage, Grafana Cloud, Run:ai, GKE and EKS cost allocation
  break GPU cost down by pod, workload, namespace or tag, never by model.
- Serving-runtime dashboards on grafana.com key on the engine's
  `model_name` where the engine emits one (vLLM, Triton); TGI, NIM,
  llama.cpp and Ollama deployments fall back to pod names.
- No project derives model identity independently of the serving
  runtime.

The gap is the key, not the dashboard. This design makes the key a
Prometheus series.

## Goals

1. Expose the inventory's model and runtime identity as two info
   metrics that any Prometheus join can use, keyed the way cAdvisor,
   DCGM and cost tools key their series (namespace and pod).
2. Make the identity we expose equal to the value vLLM puts on its own
   metrics, so joins against engine metrics and against OpenCost's
   module work without a relabel.
3. Ship the join itself as reference material: recording rules and a
   dashboard, off by default, that produce utilisation per model from
   cAdvisor and DCGM.

## Non-goals

- No cost model of our own. Prices belong to OpenCost, Kubecost and the
  cloud bill; we provide the dimension they lack.
- No pod mutation, no labels written, no new RBAC. The controller
  already reads workloads and their pods.
- No enforcement, no scheduler input, no attribution on shared GPUs
  beyond what the pod-level metric offers. Time-sliced GPUs report the
  same utilisation to every tenant; that is a driver limitation
  (dcgm-exporter #307) and we do not pretend otherwise.
- No change to the CycloneDX document shape beyond one additive
  property (served model name, below).

## Design

### 1. Two info metrics

Emitted by the controller on the existing `/metrics` endpoint, value
always `1`, present while the AIBOM exists.

```
aibom_workload_info{namespace, workload_kind, workload_name, category,
                    runtime, runtime_version, model, model_confidence,
                    served_model} 1

aibom_pod_info{namespace, pod, workload_kind, workload_name,
               runtime, model, served_model} 1
```

- One `aibom_workload_info` series per workload per model in the status
  summary; a workload with no model identity has one series with
  `model=""`. `runtime` and `runtime_version` are the summary's runtime
  (first detected, as the summary already does); empty when none.
- One `aibom_pod_info` series per owned pod per model. The pod set is
  the one the per-kind reconcilers already gather for digest
  resolution (`scraper.Workload.Pods`), so it is exactly the pods the
  document's evidence came from. Rolled-up workloads (Design 005)
  contribute their pods to the root's series.
- Source of truth is the status summary, so the series say what the
  AIBOM says. They are refreshed on every reconcile, including the
  unchanged-input fast path, because pod membership changes without the
  document changing.
- Removal: the workload reconcilers forget a workload's series when
  they delete its AIBOM (namespace no longer opted in, kind no longer
  allowed, roll-up, no inference signal). A small reconciler on AIBOM
  delete events forgets the series when the AIBOM is garbage-collected
  with its workload, which the workload reconcilers never see. After a
  controller restart the series repopulate on the startup resync.

The reference join, which is the whole feature:

```promql
sum by (model) (
  rate(container_cpu_usage_seconds_total{container!="",pod!=""}[2m])
  * on (namespace, pod) group_left (model, runtime) aibom_pod_info
)
```

Replace the left-hand side with `DCGM_FI_DEV_GPU_UTIL` for GPU
utilisation per model, with a TPU utilisation series for TPUs, or with
an OpenCost allocation series for cost per model. The right-hand side
never changes.

### 2. Cardinality

Series count is workloads × models + pods × models, bounded by the
inventory the controller already holds and the pods it already lists.
At the perf baseline (1,001 workloads, one model each, one pod each)
that is about 2,000 series, comparable to one `kube_pod_info` per pod.
Label values are the identities as written, truncated the way the
document truncates component names. Workloads with many pods
(DaemonSets, large replica counts) produce one pod series per pod; a
`metrics.workloadInfo.maxPodsPerWorkload` cap is not introduced in v1
but the values.yaml documents the count so operators can decide.

### 3. Served model name

vLLM labels its metrics with `--served-model-name` when set, otherwise
with `--model`. OpenCost's inference cost module joins on that value.
k8s-aibom records the model identity from `--model` (what the model is)
and today ignores the alias.

The inference scraper gains one rule: when a container's args carry
`--served-model-name <name>` (or `=<name>`; vLLM accepts several names,
the first is recorded), every model component extracted from that
container gets the property `k8s-aibom:identity:servedModelName`. The
status summary gains `servedName` on `ModelSummary` (additive, both
served API versions), and both metrics carry it as `served_model`,
empty when absent. The CycloneDX document changes only by this one
additive property, within the v1.7.0 shape; documents without the flag
are byte-identical.

Argument references are expanded the way #392 expands them, so
`--served-model-name $(MODEL_ALIAS)` resolves through the container's
env.

### 4. Reference rules and dashboard

Shipped under `docs/observability/`, not installed by the chart by
default:

- `recording-rules.yaml`: CPU cores, working-set memory, GPU
  utilisation, GPUs allocated, workloads and pods, each by model and
  runtime.
- `dashboard.json`: utilisation by model, share by model, memory by
  model, by runtime, GPU by model, three stat tiles, and an inventory
  table from `aibom_workload_info`.
- A page, "per-model GPU utilisation in ten minutes", that assumes
  kube-prometheus-stack and dcgm-exporter and nothing else.

The chart gains `metrics.rules.enabled` (PrometheusRule, default false)
and `metrics.dashboard.enabled` (ConfigMap with the Grafana sidecar
label, default false). Both are plain templates with no new RBAC.

### 5. OpenCost

Proposed to the OpenCost maintainers separately: let the inference cost
module take pod-to-model identity from `aibom_pod_info` when the pod
label is absent, selecting `served_model` when set and `model`
otherwise. That extends their module to every runtime k8s-aibom
attributes with no labelling requirement, and gives it the workload
dimension their pending list asks for. Nothing in this design depends
on that proposal being accepted; the series are useful on their own.

## Failure modes

| Condition | Behaviour |
|---|---|
| AIBOM deleted with its workload | AIBOM delete reconciler forgets the series within one event |
| Controller restart | series absent until the startup resync reconciles each workload; no stale series survive a restart |
| Pod list unavailable (API error) | reconcile returns the error as today; series keep the last pod set until the next successful reconcile |
| Workload with many models | one series per model; the dashboard divides nothing, so a two-model workload is counted under both, and the page says so |
| `--served-model-name` with several names | first name recorded; the others are in the args and visible to auditors through the document's locator |

## Testing

- `internal/metrics`: set, update, forget; series counts via
  `testutil.CollectAndCount`; a pod leaving the set removes its series.
- `internal/scraper`: table cases for `--served-model-name` in both
  forms, multiple names, `$(VAR)` expansion, absence; property present
  on arg-sourced and env-sourced model components from the same
  container; goldens unchanged for fixtures without the flag.
- `internal/controller`: summary carries `servedName`; envtest case
  that a workload's series appear after reconcile, follow a pod
  recreation, and disappear when the AIBOM is deleted.
- Chart: helm lint with both toggles on; `verify-chart-crds` after the
  additive summary field.

## Rollout

v1.8.0. CHANGELOG under Added, with the two metric names and the new
summary field. The evidence note for the 1.8.0 release candidate
repeats the join on the GKE test cluster with real inference, as the
November proof of concept did on CPU.
