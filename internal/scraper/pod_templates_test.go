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
	"reflect"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func tmpl(labels map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{"metadata": map[string]interface{}{"labels": labels}}
}

func TestPodTemplateLabels(t *testing.T) {
	podTmpl := corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "vllm"}}}
	top := metav1.ObjectMeta{Name: "w", Labels: map[string]string{"top": "ignored"}}
	cases := []struct {
		name string
		obj  client.Object
		want []map[string]string
	}{
		{"Deployment", &appsv1.Deployment{ObjectMeta: top, Spec: appsv1.DeploymentSpec{Template: podTmpl}}, []map[string]string{{"app": "vllm"}}},
		{"StatefulSet", &appsv1.StatefulSet{ObjectMeta: top, Spec: appsv1.StatefulSetSpec{Template: podTmpl}}, []map[string]string{{"app": "vllm"}}},
		{"DaemonSet", &appsv1.DaemonSet{ObjectMeta: top, Spec: appsv1.DaemonSetSpec{Template: podTmpl}}, []map[string]string{{"app": "vllm"}}},
		{"Job", &batchv1.Job{ObjectMeta: top, Spec: batchv1.JobSpec{Template: podTmpl}}, []map[string]string{{"app": "vllm"}}},
		{"CronJob", &batchv1.CronJob{ObjectMeta: top, Spec: batchv1.CronJobSpec{JobTemplate: batchv1.JobTemplateSpec{Spec: batchv1.JobSpec{Template: podTmpl}}}}, []map[string]string{{"app": "vllm"}}},
		// The top-level object's labels never count; a template without
		// labels contributes nothing.
		{"Deployment without template labels", &appsv1.Deployment{ObjectMeta: top}, nil},
		{"LeaderWorkerSet", unstr("leaderworkerset.x-k8s.io/v1", "LeaderWorkerSet", map[string]interface{}{
			"leaderWorkerTemplate": map[string]interface{}{
				"leaderTemplate": tmpl(map[string]interface{}{"role": "leader"}),
				"workerTemplate": tmpl(map[string]interface{}{"role": "worker"}),
			}}), []map[string]string{{"role": "leader"}, {"role": "worker"}}},
		{"DisaggregatedSet roles", unstr("disaggregatedset.x-k8s.io/v1", "DisaggregatedSet", map[string]interface{}{
			"roles": []interface{}{
				map[string]interface{}{"name": "prefill", "spec": map[string]interface{}{"leaderWorkerTemplate": map[string]interface{}{
					"workerTemplate": tmpl(map[string]interface{}{"llm-d.ai/role": "prefill"})}}},
				"not-a-role",
				map[string]interface{}{"name": "decode", "spec": map[string]interface{}{"leaderWorkerTemplate": map[string]interface{}{
					"leaderTemplate": tmpl(map[string]interface{}{"llm-d.ai/role": "decode", "lead": "yes"}),
					"workerTemplate": tmpl(map[string]interface{}{"llm-d.ai/role": "decode"})}}},
			}}), []map[string]string{{"llm-d.ai/role": "prefill"}, {"llm-d.ai/role": "decode", "lead": "yes"}, {"llm-d.ai/role": "decode"}}},
		{"DynamoGraphDeployment components and roles", unstr("nvidia.com/v1beta1", "DynamoGraphDeployment", map[string]interface{}{
			"components": []interface{}{
				map[string]interface{}{"name": "frontend", "podTemplate": tmpl(map[string]interface{}{"c": "frontend"})},
				map[string]interface{}{"name": "worker", "roles": []interface{}{
					map[string]interface{}{"name": "prefill", "podTemplate": tmpl(map[string]interface{}{"c": "prefill"})},
				}},
			}}), []map[string]string{{"c": "frontend"}, {"c": "prefill"}}},
		{"DynamoComponentDeployment", unstr("nvidia.com/v1beta1", "DynamoComponentDeployment", map[string]interface{}{
			"podTemplate": tmpl(map[string]interface{}{"c": "decode", "n": int64(3)}),
		}), []map[string]string{{"c": "decode"}}},
		// No Pod template is read for these kinds.
		{"InferenceService", unstr("serving.kserve.io/v1beta1", "InferenceService", map[string]interface{}{}), nil},
		{"NIMService", unstr("apps.nvidia.com/v1alpha1", "NIMService", map[string]interface{}{}), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := PodTemplateLabels(tc.obj); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("PodTemplateLabels = %v, want %v", got, tc.want)
			}
		})
	}
}

func unstr(apiVersion, kind string, spec map[string]interface{}) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": apiVersion, "kind": kind,
		"metadata": map[string]interface{}{"name": "w", "namespace": "ns", "labels": map[string]interface{}{"top": "ignored"}},
		"spec":     spec,
	}}
	return u
}

// Pool facts are omitted from the hash when empty, so documents of
// workloads that match no pool keep their pre-feature input hash.
func TestHashBOMInputs_InferencePoolsOmittedWhenEmpty(t *testing.T) {
	in := &BOMInputs{ScraperName: "inference.spec", Confidence: ConfidenceInferred}
	before, err := HashBOMInputs(in)
	if err != nil {
		t.Fatal(err)
	}
	in.InferencePools = []InferencePoolMembership{}
	if h, _ := HashBOMInputs(in); h != before {
		t.Error("an empty pool list changed the hash")
	}
	in.InferencePools = []InferencePoolMembership{{Pool: "ns/a"}}
	withPool, _ := HashBOMInputs(in)
	if withPool == before {
		t.Error("a pool fact did not change the hash")
	}
	in.InferencePools[0].EndpointPicker = "epp"
	if h, _ := HashBOMInputs(in); h == withPool {
		t.Error("an endpoint picker fact did not change the hash")
	}
}
