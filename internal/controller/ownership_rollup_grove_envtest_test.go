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
	"errors"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Grove ownership shapes under a DynamoGraphDeployment (Design 005, per
// the Dynamo/Grove maintainers' review): the PodCliqueSet is owned
// directly by the graph, and pods arrive either through a direct clique
// or through a scaling group. Objects are created parent-first, each
// child's controller reference carrying the real parent UID, exactly as
// the operators would. No Grove or Dynamo controller runs here.

const (
	groveDigest = "4444444444444444444444444444444444444444444444444444444444444444"
	decoyDigest = "5555555555555555555555555555555555555555555555555555555555555555"
)

func groveObject(kind, ns, name string, owner metav1.OwnerReference, spec map[string]interface{}) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("grove.io/v1alpha1")
	u.SetKind(kind)
	u.SetName(name)
	u.SetNamespace(ns)
	u.SetOwnerReferences([]metav1.OwnerReference{owner})
	_ = unstructured.SetNestedMap(u.Object, spec, "spec")
	return u
}

// podCliqueSpec mirrors the generated PodClique spec shape, with the
// same container name/image as the graph's component template so the
// digest matches the right container.
func podCliqueSpec(roleName string) map[string]interface{} {
	return map[string]interface{}{
		"roleName": roleName, "replicas": int64(1), "minAvailable": int64(1),
		"podSpec": map[string]interface{}{"containers": []interface{}{
			map[string]interface{}{"name": "main", "image": "vllm/vllm-openai:v0.6.3", "args": []interface{}{"--model", "Qwen/Qwen3-0.6B"}},
		}},
	}
}

func createGraph(t *testing.T, c client.Client, ctx context.Context, ns, name string) (*unstructured.Unstructured, types.NamespacedName) {
	t.Helper()
	dgd := dynamoGraphDeployment(ns, name, "vllm",
		dynamoComponent("Worker", "worker", "Qwen/Qwen3-0.6B", "vllm/vllm-openai:v0.6.3"))
	mustCreate(t, c, ctx, dgd)
	key := types.NamespacedName{Name: AIBOMNameForWorkload("nvidia.com", "DynamoGraphDeployment", name), Namespace: ns}
	waitInlineContains(t, c, ctx, key, `"Qwen/Qwen3-0.6B"`)
	return dgd, key
}

// Direct clique: DGD → PodCliqueSet → PodClique → Pod.
func TestIntegration_Rollup_Grove_DirectClique(t *testing.T) {
	env := startEnvTest(t)
	ctx := context.Background()
	ns := "rollup-grove-pclq"
	mustCreateOptedInNamespace(t, env.k8sClient, ctx, ns)
	dgd, dgdKey := createGraph(t, env.k8sClient, ctx, ns, "graph")

	pcs := groveObject("PodCliqueSet", ns, "graph",
		ownerRefTo("nvidia.com/v1beta1", "DynamoGraphDeployment", "graph", dgd.GetUID()),
		map[string]interface{}{"replicas": int64(1), "template": map[string]interface{}{"cliques": []interface{}{}}})
	mustCreate(t, env.k8sClient, ctx, pcs)
	pclq := groveObject("PodClique", ns, "graph-0-worker",
		ownerRefTo("grove.io/v1alpha1", "PodCliqueSet", "graph", pcs.GetUID()),
		podCliqueSpec("worker"))
	mustCreate(t, env.k8sClient, ctx, pclq)
	createPodWithDigest(t, env.k8sClient, ctx, ns, "graph-0-worker-0", map[string]string{"nvidia.com/dynamo-graph-deployment-name": "graph"},
		ownerRefTo("grove.io/v1alpha1", "PodClique", "graph-0-worker", pclq.GetUID()), groveDigest)

	bom := waitInlineContains(t, env.k8sClient, ctx, dgdKey, groveDigest)
	if strings.Contains(bom, "k8s-aibom:rollup:owned:") {
		t.Errorf("Grove kinds are walk-only intermediates and must not be listed as absorbed workloads: %s", bom)
	}
}

// Scaling group: DGD → PodCliqueSet → PodCliqueScalingGroup → PodClique
// → Pod. A decoy pod carries the graph-name label and the same image but
// belongs to an unrelated chain: its digest must never reach the graph.
func TestIntegration_Rollup_Grove_ScalingGroup_AndLabelDecoy(t *testing.T) {
	env := startEnvTest(t)
	ctx := context.Background()
	ns := "rollup-grove-pcsg"
	mustCreateOptedInNamespace(t, env.k8sClient, ctx, ns)
	dgd, dgdKey := createGraph(t, env.k8sClient, ctx, ns, "graph")

	pcs := groveObject("PodCliqueSet", ns, "graph",
		ownerRefTo("nvidia.com/v1beta1", "DynamoGraphDeployment", "graph", dgd.GetUID()),
		map[string]interface{}{"replicas": int64(1), "template": map[string]interface{}{
			"cliques": []interface{}{}, "podCliqueScalingGroups": []interface{}{map[string]interface{}{"name": "worker", "cliqueNames": []interface{}{"worker-wkr"}}},
		}})
	mustCreate(t, env.k8sClient, ctx, pcs)
	pcsg := groveObject("PodCliqueScalingGroup", ns, "graph-0-worker",
		ownerRefTo("grove.io/v1alpha1", "PodCliqueSet", "graph", pcs.GetUID()),
		map[string]interface{}{"replicas": int64(1), "minAvailable": int64(1), "cliqueNames": []interface{}{"worker-wkr"}})
	mustCreate(t, env.k8sClient, ctx, pcsg)
	pclq := groveObject("PodClique", ns, "graph-0-worker-0",
		ownerRefTo("grove.io/v1alpha1", "PodCliqueScalingGroup", "graph-0-worker", pcsg.GetUID()),
		podCliqueSpec("worker-wkr"))
	mustCreate(t, env.k8sClient, ctx, pclq)

	// The decoy first, so it is in the pod list before the real one.
	decoyRS := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{Name: "decoy-rs", Namespace: ns, Labels: map[string]string{"app": "decoy"}},
		Spec: appsv1.ReplicaSetSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "decoy"}},
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "decoy"}}, Spec: vllmPodSpec()}},
	}
	mustCreate(t, env.k8sClient, ctx, decoyRS)
	createPodWithDigest(t, env.k8sClient, ctx, ns, "aaa-decoy",
		map[string]string{"app": "decoy", "nvidia.com/dynamo-graph-deployment-name": "graph"},
		ownerRefTo("apps/v1", "ReplicaSet", "decoy-rs", decoyRS.UID), decoyDigest)
	createPodWithDigest(t, env.k8sClient, ctx, ns, "graph-0-worker-0-0",
		map[string]string{"nvidia.com/dynamo-graph-deployment-name": "graph"},
		ownerRefTo("grove.io/v1alpha1", "PodClique", "graph-0-worker-0", pclq.GetUID()), groveDigest)

	bom := waitInlineContains(t, env.k8sClient, ctx, dgdKey, groveDigest)
	if strings.Contains(bom, decoyDigest) {
		t.Fatalf("a pod with the graph label but a foreign ownership chain contaminated the graph's document: %s", bom)
	}
	if strings.Contains(bom, "k8s-aibom:rollup:owned:") {
		t.Errorf("Grove kinds must not be listed as absorbed workloads: %s", bom)
	}
}

// An owner re-created under the same name has a new UID: the child's
// stale reference must not roll it up into the new owner. The child is
// reported as a root again (unresolved owner), never silently dropped.
func TestIntegration_Rollup_StaleOwnerUIDDoesNotAdopt(t *testing.T) {
	env := startEnvTest(t)
	ctx := context.Background()
	ns := "rollup-stale"
	mustCreateOptedInNamespace(t, env.k8sClient, ctx, ns)
	dgd, _ := createGraph(t, env.k8sClient, ctx, ns, "graph")

	mkDCD := func() *unstructured.Unstructured {
		dcd := &unstructured.Unstructured{}
		dcd.SetAPIVersion("nvidia.com/v1beta1")
		dcd.SetKind("DynamoComponentDeployment")
		dcd.SetName("graph-worker")
		dcd.SetNamespace(ns)
		dcd.SetOwnerReferences([]metav1.OwnerReference{ownerRefTo("nvidia.com/v1beta1", "DynamoGraphDeployment", "graph", dgd.GetUID())})
		_ = unstructured.SetNestedMap(dcd.Object, map[string]interface{}{"backendFramework": "vllm", "name": "Worker", "type": "worker"}, "spec")
		return dcd
	}
	dcd := mkDCD()
	mustCreate(t, env.k8sClient, ctx, dcd)
	oldUID := dcd.GetUID()

	sel := map[string]string{"app": "graph-worker"}
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "graph-worker", Namespace: ns,
			OwnerReferences: []metav1.OwnerReference{ownerRefTo("nvidia.com/v1beta1", "DynamoComponentDeployment", "graph-worker", oldUID)}},
		Spec: appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: sel},
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: sel}, Spec: vllmPodSpec()}},
	}
	mustCreate(t, env.k8sClient, ctx, dep)
	depKey := types.NamespacedName{Name: AIBOMNameForWorkload("apps", "Deployment", "graph-worker"), Namespace: ns}
	time.Sleep(1500 * time.Millisecond)
	if aibomExists(env.k8sClient, ctx, depKey) {
		t.Fatal("Deployment owned by a live tracked component must be rolled up")
	}

	// Re-create the owner under the same name: new UID, stale reference.
	if err := env.k8sClient.Delete(ctx, dcd); err != nil {
		t.Fatal(err)
	}
	eventually(t, 15*time.Second, 200*time.Millisecond, func() error {
		probe := &unstructured.Unstructured{}
		probe.SetAPIVersion("nvidia.com/v1beta1")
		probe.SetKind("DynamoComponentDeployment")
		if err := env.k8sClient.Get(ctx, types.NamespacedName{Name: "graph-worker", Namespace: ns}, probe); err == nil {
			return errors.New("old component still present")
		}
		return nil
	})
	fresh := mkDCD()
	mustCreate(t, env.k8sClient, ctx, fresh)
	if fresh.GetUID() == oldUID {
		t.Fatal("test premise: re-created owner must have a new UID")
	}
	// Nudge the Deployment so it reconciles against the new owner.
	if err := env.k8sClient.Get(ctx, types.NamespacedName{Name: "graph-worker", Namespace: ns}, dep); err != nil {
		t.Fatal(err)
	}
	dep.Spec.Template.Annotations = map[string]string{"nudge": "1"}
	if err := env.k8sClient.Update(ctx, dep); err != nil {
		t.Fatal(err)
	}
	eventually(t, 30*time.Second, 200*time.Millisecond, func() error {
		if !aibomExists(env.k8sClient, ctx, depKey) {
			return errors.New("Deployment with a stale owner UID must be reported as a root, not adopted by the re-created owner")
		}
		return nil
	})
}
