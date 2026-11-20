# Per-model GPU utilisation in ten minutes

Every metric a cluster already has for accelerator and CPU use is keyed
by pod: cAdvisor, the NVIDIA DCGM exporter, TPU utilisation, OpenCost
allocation. None of them knows which model the pod serves. k8s-aibom
does, for every workload it attributes, and exports that knowledge as
two info series:

```
aibom_workload_info{namespace, workload_kind, workload_name, category,
                    runtime, runtime_version, model, model_confidence,
                    served_model} 1
aibom_pod_info{namespace, pod, workload_kind, workload_name,
               runtime, model, served_model} 1
```

Both are always `1`, exist while the AIBOM exists, follow the pod set
on every reconcile, and are removed when the AIBOM is deleted. `model`
is the identity as the workload declared it (the `--model` argument,
`HF_MODEL_ID`, an annotation); `served_model` is vLLM's
`--served-model-name` alias when set, which is the value vLLM's own
metrics carry as `model_name`.

## The join

```promql
sum by (model) (
  rate(container_cpu_usage_seconds_total{container!="",pod!=""}[2m])
  * on (namespace, pod) group_left (model, runtime) aibom_pod_info
)
```

Replace the left-hand side and the same expression answers a different
question:

| Left-hand side | Result |
|---|---|
| `rate(container_cpu_usage_seconds_total[2m])` | CPU cores per model |
| `container_memory_working_set_bytes` | memory per model |
| `DCGM_FI_DEV_GPU_UTIL` (dcgm-exporter) | GPU utilisation per model |
| `count` of `DCGM_FI_DEV_GPU_UTIL` | GPUs allocated per model |
| an OpenCost or Kubecost allocation series | cost per model |

## Ten minutes

Assumes kube-prometheus-stack (or any Prometheus scraping cAdvisor and
pod annotations) and, for GPUs, dcgm-exporter with pod labels enabled,
which is its default under the GPU Operator.

1. Install or upgrade the chart with metrics on and the reference rules
   and dashboard enabled:

   ```bash
   helm upgrade --install k8s-aibom oci://ghcr.io/googlecloudplatform/charts/k8s-aibom \
     -n k8s-aibom-system --create-namespace \
     --set metrics.enabled=true --set metrics.serviceMonitor.enabled=true \
     --set metrics.rules.enabled=true --set metrics.dashboard.enabled=true
   ```

   Without the Prometheus Operator, leave `rules.enabled` off and load
   [recording-rules.yaml](recording-rules.yaml) into your Prometheus
   configuration instead; leave `dashboard.enabled` off and import
   [dashboard.json](dashboard.json) into Grafana by hand.

2. Opt a namespace in and let the controller attribute its workloads:

   ```bash
   kubectl label namespace <ns> aibom.k8saibom.dev/enabled=true
   kubectl aibom summary -n <ns>
   ```

3. Open the dashboard "AI workloads: resource use by model". It has CPU
   cores by model, share of AI CPU by model, memory by model, CPU by
   runtime, GPU utilisation by model (empty without dcgm-exporter),
   three stat tiles (models, AI workloads, AI pods) and an inventory
   table straight from `aibom_workload_info`.

## Reading the numbers honestly

- **Shared GPUs.** Time-sliced GPUs report the same utilisation to every
  pod on them; the driver cannot split it. The per-model number is then
  an upper bound for each tenant. MIG instances report cleanly.
- **Several models in one workload.** Each model gets its own series,
  so the workload's pods are counted under every model it serves. The
  dashboard does not divide; a workload like that shows the same
  utilisation on each of its models.
- **Pods that have not pulled yet.** A pending pod is in
  `aibom_pod_info` but has no cAdvisor series; it contributes nothing
  until it runs.
- **Confidence.** `model_confidence` is on the workload series. Filter
  on `declared` when a number has to be defensible; `inferred`
  identities came from an env var or an image pattern and the document
  records the evidence.

## OpenCost

OpenCost's inference cost module attributes GPU, CPU and memory cost per
model by joining its allocation data with vLLM token metrics on a pod
label whose value must equal vLLM's `model_name`. `aibom_pod_info`
carries that value as `served_model` (or `model` when no alias is set)
without a label on the pod, for every runtime k8s-aibom attributes. Until
the module can read it directly, the allocation join can be done in
PromQL against an allocation series the same way as above.
