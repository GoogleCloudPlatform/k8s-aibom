/*
Copyright 2026 Google LLC

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package metrics

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"

	aibomv1beta1 "github.com/GoogleCloudPlatform/k8s-aibom/api/v1beta1"
)

// Design 011: the inventory's model and runtime identity as info metrics,
// so series that are keyed by (namespace, pod) — cAdvisor, DCGM, TPU
// utilisation, OpenCost allocation — attribute to a model with one join:
//
//	sum by (model) (rate(container_cpu_usage_seconds_total[2m])
//	  * on (namespace, pod) group_left (model, runtime) aibom_pod_info)
//
// Both gauges are always 1 and exist exactly while the AIBOM exists. The
// source of truth is the AIBOM status summary; the pod set is the one
// the reconcilers already gather for digest resolution.
var (
	WorkloadInfo = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "aibom_workload_info",
			Help: "Model and runtime identity per AI workload, from the AIBOM status summary; one series per model, value always 1.",
		},
		[]string{"namespace", "workload_kind", "workload_name", "category", "runtime", "runtime_version", "model", "model_confidence", "served_model"},
	)
	PodInfo = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "aibom_pod_info",
			Help: "Model and runtime identity per pod owned by an AI workload; one series per pod per model, value always 1.",
		},
		[]string{"namespace", "pod", "workload_kind", "workload_name", "runtime", "model", "served_model"},
	)
)

// infoTracker remembers the label sets published per AIBOM key so an
// update replaces them and a deletion removes them; a GaugeVec on its
// own cannot forget a series whose labels it no longer sees.
type infoTracker struct {
	mu        sync.Mutex
	workloads map[string][]prometheus.Labels
	pods      map[string][]prometheus.Labels
}

var tracker = &infoTracker{
	workloads: map[string][]prometheus.Labels{},
	pods:      map[string][]prometheus.Labels{},
}

// SetWorkloadInfo publishes the series for one AIBOM (key is its
// namespace/name) from its status summary and the names of the pods the
// workload currently owns, replacing whatever was published before.
func SetWorkloadInfo(key string, summary *aibomv1beta1.AIBOMSummary, pods []string) {
	if summary == nil {
		ForgetWorkloadInfo(key)
		return
	}
	runtime, version := "", ""
	if summary.Runtime != nil {
		runtime, version = summary.Runtime.Name, summary.Runtime.Version
	}
	models := summary.Models
	if len(models) == 0 {
		models = []aibomv1beta1.ModelSummary{{}}
	}
	var w, p []prometheus.Labels
	for _, m := range models {
		w = append(w, prometheus.Labels{
			"namespace": summary.Workload.Namespace, "workload_kind": summary.Workload.Kind, "workload_name": summary.Workload.Name,
			"category": summary.Workload.Category, "runtime": runtime, "runtime_version": version,
			"model": m.Identity, "model_confidence": m.Confidence, "served_model": m.ServedName,
		})
		for _, pod := range pods {
			p = append(p, prometheus.Labels{
				"namespace": summary.Workload.Namespace, "pod": pod, "workload_kind": summary.Workload.Kind, "workload_name": summary.Workload.Name,
				"runtime": runtime, "model": m.Identity, "served_model": m.ServedName,
			})
		}
	}
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	tracker.dropLocked(key)
	for _, l := range w {
		WorkloadInfo.With(l).Set(1)
	}
	for _, l := range p {
		PodInfo.With(l).Set(1)
	}
	tracker.workloads[key] = w
	tracker.pods[key] = p
}

// ForgetWorkloadInfo removes every series published for the AIBOM key.
func ForgetWorkloadInfo(key string) {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	tracker.dropLocked(key)
}

func (t *infoTracker) dropLocked(key string) {
	for _, l := range t.workloads[key] {
		WorkloadInfo.Delete(l)
	}
	for _, l := range t.pods[key] {
		PodInfo.Delete(l)
	}
	delete(t.workloads, key)
	delete(t.pods, key)
}
