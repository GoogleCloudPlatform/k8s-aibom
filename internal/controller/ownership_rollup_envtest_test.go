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
	"fmt"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	aibomv1beta1 "github.com/GoogleCloudPlatform/k8s-aibom/api/v1beta1"
)

// Design 005 regression suite, on the full harness (apps kinds on the
// manager cache, CRD kinds under the supervisor, CronJob reconciler,
// tracked-kind set wired exactly as cmd/manager does).

const rollupDigest = "3333333333333333333333333333333333333333333333333333333333333333"

func vllmPodSpec() corev1.PodSpec {
	return corev1.PodSpec{Containers: []corev1.Container{{
		Name: "main", Image: "vllm/vllm-openai:v0.6.3", Args: []string{"--model", "Qwen/Qwen3-0.6B"},
	}}}
}

func rollupStatefulSet(ns, name string, owners ...metav1.OwnerReference) *appsv1.StatefulSet {
	sel := map[string]string{"app": name}
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, OwnerReferences: owners},
		Spec: appsv1.StatefulSetSpec{
			Selector:    &metav1.LabelSelector{MatchLabels: sel},
			ServiceName: name,
			Template:    corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: sel}, Spec: vllmPodSpec()},
		},
	}
}

// createPodWithDigest creates a pod owned by owner and sets its
// container status ImageID, the only digest source for a workload.
func createPodWithDigest(t *testing.T, c client.Client, ctx context.Context, ns, name string, labels map[string]string, owner metav1.OwnerReference, digest string) {
	t.Helper()
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels, OwnerReferences: []metav1.OwnerReference{owner}},
		Spec:       vllmPodSpec(),
	}
	mustCreate(t, c, ctx, p)
	p.Status = corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
		Name: "main", Image: "vllm/vllm-openai:v0.6.3", ImageID: "vllm/vllm-openai@sha256:" + digest,
	}}}
	if err := c.Status().Update(ctx, p); err != nil {
		t.Fatalf("update pod status: %v", err)
	}
}

func inlineBOMOf(c client.Client, ctx context.Context, key types.NamespacedName) (string, error) {
	var a aibomv1beta1.AIBOM
	if err := c.Get(ctx, key, &a); err != nil {
		return "", err
	}
	if a.Status.BOMDocument == nil || a.Status.BOMDocument.Inline == nil {
		return "", errors.New("inline BOM not yet populated")
	}
	return string(a.Status.BOMDocument.Inline.Data), nil
}

func waitNoAIBOM(t *testing.T, c client.Client, ctx context.Context, key types.NamespacedName) {
	t.Helper()
	eventually(t, 30*time.Second, 200*time.Millisecond, func() error {
		var a aibomv1beta1.AIBOM
		err := c.Get(ctx, key, &a)
		if err == nil {
			return fmt.Errorf("AIBOM %s still present", key.Name)
		}
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	})
}

func waitInlineContains(t *testing.T, c client.Client, ctx context.Context, key types.NamespacedName, wants ...string) string {
	t.Helper()
	var bom string
	eventually(t, 60*time.Second, 250*time.Millisecond, func() error {
		var err error
		bom, err = inlineBOMOf(c, ctx, key)
		if err != nil {
			return err
		}
		for _, w := range wants {
			if !strings.Contains(bom, w) {
				return fmt.Errorf("BOM lacks %q yet", w)
			}
		}
		return nil
	})
	return bom
}

// LeaderWorkerSet → StatefulSet: a StatefulSet that already had its own
// AIBOM loses it the moment a tracked LWS becomes its controller owner;
// the LWS document lists it and resolves the digest from the
// StatefulSet's pod, two hops away.
func TestIntegration_Rollup_LWSAbsorbsStatefulSet(t *testing.T) {
	env := startEnvTest(t)
	ctx := context.Background()
	ns := "rollup-lws"
	mustCreateOptedInNamespace(t, env.k8sClient, ctx, ns)

	sts := rollupStatefulSet(ns, "group-0")
	mustCreate(t, env.k8sClient, ctx, sts)
	stsKey := types.NamespacedName{Name: AIBOMNameForWorkload("apps", "StatefulSet", "group-0"), Namespace: ns}
	eventually(t, 30*time.Second, 200*time.Millisecond, func() error {
		if !aibomExists(env.k8sClient, ctx, stsKey) {
			return errors.New("StatefulSet AIBOM not yet created (it is a root until the LWS owns it)")
		}
		return nil
	})

	lws := leaderWorkerSetCR(ns, "multi", nil, lwsPodTemplate("vllm/vllm-openai:v0.6.3", "--model", "Qwen/Qwen3-0.6B"), 2)
	mustCreate(t, env.k8sClient, ctx, lws)
	lwsKey := types.NamespacedName{Name: AIBOMNameForWorkload("leaderworkerset.x-k8s.io", "LeaderWorkerSet", "multi"), Namespace: ns}
	waitInlineContains(t, env.k8sClient, ctx, lwsKey, `"Qwen/Qwen3-0.6B"`)

	// Adopt: the LWS becomes the StatefulSet's controller owner.
	if err := env.k8sClient.Get(ctx, types.NamespacedName{Name: "group-0", Namespace: ns}, sts); err != nil {
		t.Fatal(err)
	}
	sts.OwnerReferences = []metav1.OwnerReference{ownerRefTo("leaderworkerset.x-k8s.io/v1", "LeaderWorkerSet", "multi", lws.GetUID())}
	if err := env.k8sClient.Update(ctx, sts); err != nil {
		t.Fatal(err)
	}
	waitNoAIBOM(t, env.k8sClient, ctx, stsKey)

	// A pod of the StatefulSet with a digest: the LWS document must pick
	// it up (pod event → root via the chain) and list the StatefulSet.
	createPodWithDigest(t, env.k8sClient, ctx, ns, "group-0-0", map[string]string{"app": "group-0"},
		ownerRefTo("apps/v1", "StatefulSet", "group-0", sts.UID), rollupDigest)
	bom := waitInlineContains(t, env.k8sClient, ctx, lwsKey, rollupDigest, `"aibom.rollup.owned.0"`, `"StatefulSet/group-0"`)
	if strings.Contains(bom, "aibom.rollup.owned.1") {
		t.Errorf("exactly one absorbed workload expected: %s", bom)
	}
	if aibomExists(env.k8sClient, ctx, stsKey) {
		t.Fatal("StatefulSet AIBOM must stay absent while the LWS owns it")
	}
}

// DynamoGraphDeployment → DynamoComponentDeployment → Deployment →
// ReplicaSet → Pod: one document for the graph; the component and the
// Deployment produce nothing; the graph lists both and carries the
// worker pod's digest.
func TestIntegration_Rollup_DynamoGraphAbsorbsComponentAndDeployment(t *testing.T) {
	env := startEnvTest(t)
	ctx := context.Background()
	ns := "rollup-dgd"
	mustCreateOptedInNamespace(t, env.k8sClient, ctx, ns)

	dgd := dynamoGraphDeployment(ns, "graph", "vllm",
		dynamoComponent("Worker", "worker", "Qwen/Qwen3-0.6B", "vllm/vllm-openai:v0.6.3"))
	mustCreate(t, env.k8sClient, ctx, dgd)
	dgdKey := types.NamespacedName{Name: AIBOMNameForWorkload("nvidia.com", "DynamoGraphDeployment", "graph"), Namespace: ns}
	waitInlineContains(t, env.k8sClient, ctx, dgdKey, `"Qwen/Qwen3-0.6B"`)

	dcd := &unstructured.Unstructured{}
	dcd.SetAPIVersion("nvidia.com/v1beta1")
	dcd.SetKind("DynamoComponentDeployment")
	dcd.SetName("graph-worker")
	dcd.SetNamespace(ns)
	dcd.SetOwnerReferences([]metav1.OwnerReference{ownerRefTo("nvidia.com/v1beta1", "DynamoGraphDeployment", "graph", dgd.GetUID())})
	_ = unstructured.SetNestedMap(dcd.Object, map[string]interface{}{
		"backendFramework": "vllm", "name": "Worker", "type": "worker",
		"modelRef": map[string]interface{}{"name": "Qwen/Qwen3-0.6B"},
	}, "spec")
	mustCreate(t, env.k8sClient, ctx, dcd)

	sel := map[string]string{"app": "graph-worker"}
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "graph-worker", Namespace: ns,
			OwnerReferences: []metav1.OwnerReference{ownerRefTo("nvidia.com/v1beta1", "DynamoComponentDeployment", "graph-worker", dcd.GetUID())}},
		Spec: appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: sel},
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: sel}, Spec: vllmPodSpec()}},
	}
	mustCreate(t, env.k8sClient, ctx, dep)
	rs := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{Name: "graph-worker-rs", Namespace: ns, Labels: sel,
			OwnerReferences: []metav1.OwnerReference{ownerRefTo("apps/v1", "Deployment", "graph-worker", dep.UID)}},
		Spec: appsv1.ReplicaSetSpec{Selector: &metav1.LabelSelector{MatchLabels: sel},
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: sel}, Spec: vllmPodSpec()}},
	}
	mustCreate(t, env.k8sClient, ctx, rs)
	createPodWithDigest(t, env.k8sClient, ctx, ns, "graph-worker-rs-x", sel,
		ownerRefTo("apps/v1", "ReplicaSet", "graph-worker-rs", rs.UID), rollupDigest)

	bom := waitInlineContains(t, env.k8sClient, ctx, dgdKey, rollupDigest,
		`"Deployment/graph-worker"`, `"DynamoComponentDeployment/graph-worker"`)
	if !strings.Contains(bom, `"aibom.rollup.owned.0"`) || !strings.Contains(bom, `"aibom.rollup.owned.1"`) || strings.Contains(bom, `"aibom.rollup.owned.2"`) {
		t.Errorf("want exactly two absorbed workloads: %s", bom)
	}
	time.Sleep(1 * time.Second)
	for _, key := range []types.NamespacedName{
		{Name: AIBOMNameForWorkload("apps", "Deployment", "graph-worker"), Namespace: ns},
		{Name: AIBOMNameForWorkload("nvidia.com", "DynamoComponentDeployment", "graph-worker"), Namespace: ns},
	} {
		if aibomExists(env.k8sClient, ctx, key) {
			t.Errorf("%s must not have its own AIBOM under a tracked graph", key.Name)
		}
	}
}

// CronJob → Job: the CronJob is reported from its jobTemplate; the Job
// it spawned is absorbed.
func TestIntegration_Rollup_CronJobAbsorbsJob(t *testing.T) {
	env := startEnvTest(t)
	ctx := context.Background()
	ns := "rollup-cron"
	mustCreateOptedInNamespace(t, env.k8sClient, ctx, ns)

	evalSpec := corev1.PodSpec{RestartPolicy: corev1.RestartPolicyNever,
		Containers: []corev1.Container{{Name: "eval", Image: "eleutherai/lm-eval:0.4.3"}}}
	cj := &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Name: "nightly-eval", Namespace: ns},
		Spec: batchv1.CronJobSpec{Schedule: "0 2 * * *",
			JobTemplate: batchv1.JobTemplateSpec{Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: evalSpec}}}},
	}
	mustCreate(t, env.k8sClient, ctx, cj)
	cjKey := types.NamespacedName{Name: AIBOMNameForWorkload("batch", "CronJob", "nightly-eval"), Namespace: ns}
	waitInlineContains(t, env.k8sClient, ctx, cjKey, `"lm-eval"`)

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "nightly-eval-29", Namespace: ns,
			OwnerReferences: []metav1.OwnerReference{ownerRefTo("batch/v1", "CronJob", "nightly-eval", cj.UID)}},
		Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: evalSpec}},
	}
	mustCreate(t, env.k8sClient, ctx, job)
	// A pod of the Job drives the CronJob re-reconcile.
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "nightly-eval-29-p", Namespace: ns,
		OwnerReferences: []metav1.OwnerReference{ownerRefTo("batch/v1", "Job", "nightly-eval-29", job.UID)}}, Spec: evalSpec}
	mustCreate(t, env.k8sClient, ctx, p)

	waitInlineContains(t, env.k8sClient, ctx, cjKey, `"aibom.rollup.owned.0"`, `"Job/nightly-eval-29"`)
	time.Sleep(1 * time.Second)
	if aibomExists(env.k8sClient, ctx, types.NamespacedName{Name: AIBOMNameForWorkload("batch", "Job", "nightly-eval-29"), Namespace: ns}) {
		t.Fatal("a Job owned by a tracked CronJob must not have its own AIBOM")
	}
}

// An owner the controller cannot read (no such CRD) ends the walk as
// unresolved: the child is reported exactly as before. Coverage never
// regresses because of a kind we do not know.
func TestIntegration_Rollup_UntrackedOwnerStillReported(t *testing.T) {
	env := startEnvTest(t)
	ctx := context.Background()
	ns := "rollup-unknown"
	mustCreateOptedInNamespace(t, env.k8sClient, ctx, ns)
	sts := rollupStatefulSet(ns, "owned-by-widget", ownerRefTo("widgets.example.com/v1", "Widget", "w", types.UID("11111111-1111-1111-1111-111111111111")))
	mustCreate(t, env.k8sClient, ctx, sts)
	key := types.NamespacedName{Name: AIBOMNameForWorkload("apps", "StatefulSet", "owned-by-widget"), Namespace: ns}
	eventually(t, 30*time.Second, 200*time.Millisecond, func() error {
		if !aibomExists(env.k8sClient, ctx, key) {
			return errors.New("StatefulSet under an unreadable owner must still be reported")
		}
		return nil
	})
}

// A DynamoComponentDeployment with no graph parent is its own root.
func TestIntegration_Rollup_StandaloneComponentIsARoot(t *testing.T) {
	env := startEnvTest(t)
	ctx := context.Background()
	ns := "rollup-dcd"
	mustCreateOptedInNamespace(t, env.k8sClient, ctx, ns)
	dcd := &unstructured.Unstructured{}
	dcd.SetAPIVersion("nvidia.com/v1beta1")
	dcd.SetKind("DynamoComponentDeployment")
	dcd.SetName("solo")
	dcd.SetNamespace(ns)
	_ = unstructured.SetNestedMap(dcd.Object, map[string]interface{}{
		"backendFramework": "sglang", "name": "Solo", "type": "worker",
		"modelRef": map[string]interface{}{"name": "Qwen/Qwen3-0.6B"},
		"podTemplate": map[string]interface{}{"spec": map[string]interface{}{"containers": []interface{}{
			map[string]interface{}{"name": "main", "image": "nvcr.io/nvidia/ai-dynamo/sglang-runtime:0.6.0"}}}},
	}, "spec")
	mustCreate(t, env.k8sClient, ctx, dcd)
	key := types.NamespacedName{Name: AIBOMNameForWorkload("nvidia.com", "DynamoComponentDeployment", "solo"), Namespace: ns}
	var got aibomv1beta1.AIBOM
	eventually(t, 30*time.Second, 200*time.Millisecond, func() error {
		if err := env.k8sClient.Get(ctx, key, &got); err != nil {
			return err
		}
		if got.Status.Summary == nil {
			return errors.New("summary not yet populated")
		}
		return nil
	})
	if got.Status.Summary.Runtime == nil || got.Status.Summary.Runtime.Name != "sglang" || got.Status.Summary.Runtime.Confidence != "declared" {
		t.Errorf("runtime = %+v, want sglang/declared from spec.backendFramework", got.Status.Summary.Runtime)
	}
	bom, err := inlineBOMOf(env.k8sClient, ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(bom, `"spec.modelRef.name"`) || strings.Contains(bom, "spec.components[") {
		t.Errorf("standalone component locators must be rooted at spec: %s", bom)
	}
}
