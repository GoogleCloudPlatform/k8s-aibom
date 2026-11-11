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

package scraper

import (
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// InferencePoolMembership is one Gateway API Inference Extension
// InferencePool whose spec.selector.matchLabels matches one of a
// workload's declared Pod templates (Design 007 §2, Part 2a). It is a
// configured selector match only: it attributes no pod, model, digest
// or traffic to the workload.
type InferencePoolMembership struct {
	// Pool is "<namespace>/<name>" of the matching InferencePool.
	Pool string `json:"pool"`
	// EndpointPicker is spec.endpointPickerRef.name, when the pool
	// declares one. Never fetched or validated.
	EndpointPicker string `json:"endpointPicker,omitempty"`
}

// PodTemplateLabels returns the metadata.labels of every Pod template
// the scrapers read for obj, in the order they read them: the apps/v1
// and batch template, an LWS's leader and worker templates, each
// DisaggregatedSet role's leader and worker templates, and each Dynamo
// component's (and component role's) podTemplate. One map per template;
// labels of different templates are never merged. Live Pods and the
// top-level object's own labels are never consulted. A kind whose
// scraper reads no Pod template (KServe InferenceService, NIMService)
// returns nil, as does a template with no labels.
func PodTemplateLabels(obj client.Object) []map[string]string {
	var out []map[string]string
	add := func(l map[string]string) {
		if len(l) > 0 {
			out = append(out, l)
		}
	}
	switch o := obj.(type) {
	case *appsv1.Deployment:
		add(o.Spec.Template.Labels)
	case *appsv1.StatefulSet:
		add(o.Spec.Template.Labels)
	case *appsv1.DaemonSet:
		add(o.Spec.Template.Labels)
	case *batchv1.Job:
		add(o.Spec.Template.Labels)
	case *batchv1.CronJob:
		add(o.Spec.JobTemplate.Spec.Template.Labels)
	case *unstructured.Unstructured:
		for _, t := range unstructuredTemplates(o) {
			add(templateLabels(t))
		}
	}
	return out
}

// unstructuredTemplates lists the PodTemplateSpec maps of the
// third-party kinds whose scrapers read Pod templates, at the same paths
// those scrapers read (lws.go, disaggregatedset.go, dynamo.go).
func unstructuredTemplates(u *unstructured.Unstructured) []map[string]interface{} {
	var out []map[string]interface{}
	lwsTemplates := func(lwt map[string]interface{}) {
		for _, key := range []string{"leaderTemplate", "workerTemplate"} {
			if t, ok := lwt[key].(map[string]interface{}); ok {
				out = append(out, t)
			}
		}
	}
	switch u.GroupVersionKind().GroupKind().String() {
	case "LeaderWorkerSet.leaderworkerset.x-k8s.io":
		if lwt, found, _ := unstructured.NestedMap(u.Object, "spec", "leaderWorkerTemplate"); found {
			lwsTemplates(lwt)
		}
	case "DisaggregatedSet.disaggregatedset.x-k8s.io":
		roles, _, _ := unstructured.NestedSlice(u.Object, "spec", "roles")
		for _, r := range roles {
			role, ok := r.(map[string]interface{})
			if !ok {
				continue
			}
			if lwt, found, _ := unstructured.NestedMap(role, "spec", "leaderWorkerTemplate"); found {
				lwsTemplates(lwt)
			}
		}
	case "DynamoGraphDeployment.nvidia.com", "DynamoComponentDeployment.nvidia.com":
		var comps []map[string]interface{}
		if u.GetKind() == "DynamoComponentDeployment" {
			if spec, found, _ := unstructured.NestedMap(u.Object, "spec"); found {
				comps = append(comps, spec)
			}
		} else {
			raw, _, _ := unstructured.NestedSlice(u.Object, "spec", "components")
			for _, c := range raw {
				if m, ok := c.(map[string]interface{}); ok {
					comps = append(comps, m)
				}
			}
		}
		for _, c := range comps {
			if t, ok := c["podTemplate"].(map[string]interface{}); ok {
				out = append(out, t)
			}
			roles, _, _ := unstructured.NestedSlice(c, "roles")
			for _, r := range roles {
				if rm, ok := r.(map[string]interface{}); ok {
					if t, ok := rm["podTemplate"].(map[string]interface{}); ok {
						out = append(out, t)
					}
				}
			}
		}
	}
	return out
}

// templateLabels reads metadata.labels of an unstructured
// PodTemplateSpec; non-string values are skipped.
func templateLabels(t map[string]interface{}) map[string]string {
	raw, found, _ := unstructured.NestedMap(t, "metadata", "labels")
	if !found {
		return nil
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}
