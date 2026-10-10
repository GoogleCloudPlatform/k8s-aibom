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

package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

// disaggregatedSetCR returns an unstructured DisaggregatedSet in the
// shape of llm-d's wide-EP guide: prefill and decode roles, each a
// worker-only LeaderWorkerSet template with the guide's size.
func disaggregatedSetCR(namespace, name string, roles map[string]map[string]interface{}, size int64) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("disaggregatedset.x-k8s.io/v1")
	u.SetKind("DisaggregatedSet")
	u.SetName(name)
	u.SetNamespace(namespace)
	var list []interface{}
	for _, rn := range []string{"prefill", "decode"} {
		tmpl, ok := roles[rn]
		if !ok {
			continue
		}
		list = append(list, map[string]interface{}{
			"name": rn,
			"spec": map[string]interface{}{
				"replicas": int64(1),
				"leaderWorkerTemplate": map[string]interface{}{
					"size":           size,
					"workerTemplate": tmpl,
				},
			},
		})
	}
	_ = unstructured.SetNestedSlice(u.Object, list, "spec", "roles")
	return u
}

// A wide-EP-shaped DisaggregatedSet in an opted-in namespace produces
// one AIBOM with per-role evidence; the LeaderWorkerSet it owns produces
// none and appears in aibom.rollup.owned; a pod two hops down (LWS →
// StatefulSet → Pod) resolves its digest onto the set's document.
func TestIntegration_DisaggregatedSet_OneDocument_RollsUpLWS(t *testing.T) {
	env := startEnvTest(t)
	ctx := context.Background()
	ns := "dset-wide-ep"
	mustCreateOptedInNamespace(t, env.k8sClient, ctx, ns)

	vllm := lwsPodTemplate("vllm/vllm-openai:v0.11.0", "--model", "deepseek-ai/DeepSeek-R1-0528", "--tensor-parallel-size", "8")
	set := disaggregatedSetCR(ns, "wide-ep-nvidia-gpu-vllm", map[string]map[string]interface{}{"prefill": vllm, "decode": vllm}, 2)
	mustCreate(t, env.k8sClient, ctx, set)
	setKey := types.NamespacedName{Name: AIBOMNameForWorkload("disaggregatedset.x-k8s.io", "DisaggregatedSet", "wide-ep-nvidia-gpu-vllm"), Namespace: ns}
	bom := waitInlineContains(t, env.k8sClient, ctx, setKey,
		`"deepseek-ai/DeepSeek-R1-0528"`,
		`spec.roles[0].spec.leaderWorkerTemplate.workerTemplate.spec.containers[0]`,
		`spec.roles[1].spec.leaderWorkerTemplate.workerTemplate.spec.containers[0]`)
	for _, want := range []string{`"disaggregatedset.role"`, `"prefill"`, `"decode"`, `"lws.size"`} {
		if !strings.Contains(bom, want) {
			t.Errorf("document lacks %s", want)
		}
	}

	// The decode role's LWS, controller-owned by the set: no document of
	// its own, listed on the set's.
	lwsObj := leaderWorkerSetCR(ns, "wide-ep-nvidia-gpu-vllm-decode", nil, vllm, 2)
	lwsObj.SetOwnerReferences([]metav1.OwnerReference{ownerRefTo("disaggregatedset.x-k8s.io/v1", "DisaggregatedSet", set.GetName(), set.GetUID())})
	mustCreate(t, env.k8sClient, ctx, lwsObj)
	lwsKey := types.NamespacedName{Name: AIBOMNameForWorkload("leaderworkerset.x-k8s.io", "LeaderWorkerSet", lwsObj.GetName()), Namespace: ns}
	time.Sleep(2 * time.Second)
	if aibomExists(env.k8sClient, ctx, lwsKey) {
		t.Fatal("an LWS owned by a DisaggregatedSet must not get its own AIBOM")
	}

	// StatefulSet under the LWS, pod under the StatefulSet with a digest:
	// the pod event re-reconciles the set through the chain; its
	// document lists both absorbed workloads and carries the digest.
	sts := rollupStatefulSet(ns, "wide-ep-nvidia-gpu-vllm-decode-0", ownerRefTo("leaderworkerset.x-k8s.io/v1", "LeaderWorkerSet", lwsObj.GetName(), lwsObj.GetUID()))
	mustCreate(t, env.k8sClient, ctx, sts)
	createPodWithDigest(t, env.k8sClient, ctx, ns, "wide-ep-nvidia-gpu-vllm-decode-0-0", map[string]string{"app": sts.Name},
		ownerRefTo("apps/v1", "StatefulSet", sts.Name, sts.UID), rollupDigest)
	bom = waitInlineContains(t, env.k8sClient, ctx, setKey, rollupDigest,
		`"LeaderWorkerSet/wide-ep-nvidia-gpu-vllm-decode"`, `"StatefulSet/wide-ep-nvidia-gpu-vllm-decode-0"`)
	if strings.Contains(bom, "aibom.rollup.owned.2") {
		t.Errorf("exactly two absorbed workloads expected: %s", bom)
	}
	stsKey := types.NamespacedName{Name: AIBOMNameForWorkload("apps", "StatefulSet", sts.Name), Namespace: ns}
	if aibomExists(env.k8sClient, ctx, lwsKey) || aibomExists(env.k8sClient, ctx, stsKey) {
		t.Fatal("absorbed workloads must stay without documents")
	}
}

// A DisaggregatedSet in a namespace that is not opted in produces nothing.
func TestIntegration_DisaggregatedSet_NotOptedIn(t *testing.T) {
	env := startEnvTest(t)
	ctx := context.Background()
	ns := "dset-quiet"
	mustCreate(t, env.k8sClient, ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
	vllm := lwsPodTemplate("vllm/vllm-openai:v0.11.0", "--model", "org/m")
	set := disaggregatedSetCR(ns, "quiet", map[string]map[string]interface{}{"decode": vllm}, 1)
	mustCreate(t, env.k8sClient, ctx, set)
	time.Sleep(2 * time.Second)
	key := types.NamespacedName{Name: AIBOMNameForWorkload("disaggregatedset.x-k8s.io", "DisaggregatedSet", "quiet"), Namespace: ns}
	if aibomExists(env.k8sClient, ctx, key) {
		t.Fatal("AIBOM created in a namespace that is not opted in")
	}
}
