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
	"fmt"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/GoogleCloudPlatform/k8s-aibom/internal/config"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/metrics"
)

// setWorkloadKinds rotates the snapshot the way the config reconciler
// does on a spec change: a fresh Snapshot with the new allowlist,
// stored atomically, which notifies the supervisor and the fan-out.
func setWorkloadKinds(t *testing.T, env *envTestEnv, kinds ...schema.GroupKind) {
	t.Helper()
	prev := env.configStore.Load()
	next := *prev
	if len(kinds) == 0 {
		next.WorkloadKinds = config.AllWorkloadKinds()
	} else {
		next.WorkloadKinds = config.NewWorkloadKindSet(kinds...)
	}
	next.Source = config.SourceConfigCR
	next.SourceGeneration = prev.SourceGeneration + 1
	next.LoadedAt = time.Now()
	env.configStore.Store(&next)
}

func waitAIBOM(t *testing.T, env *envTestEnv, ctx context.Context, key types.NamespacedName, why string) {
	t.Helper()
	eventually(t, 30*time.Second, 200*time.Millisecond, func() error {
		if !aibomExists(env.k8sClient, ctx, key) {
			return fmt.Errorf("%s: AIBOM %s not present yet", why, key)
		}
		return nil
	})
}

func waitWatchDisabled(t *testing.T, h *WatchHealth, kind string) {
	t.Helper()
	eventually(t, 30*time.Second, 200*time.Millisecond, func() error {
		st, ok := h.Status(kind)
		if !ok || !st.Disabled {
			return fmt.Errorf("%s not yet disabled: %+v", kind, st)
		}
		if st.Degraded() {
			return fmt.Errorf("%s must not be degraded while disabled: %+v", kind, st)
		}
		return nil
	})
}

var (
	gkDeployment  = schema.GroupKind{Group: "apps", Kind: "Deployment"}
	gkStatefulSet = schema.GroupKind{Group: "apps", Kind: "StatefulSet"}
	gkDaemonSet   = schema.GroupKind{Group: "apps", Kind: "DaemonSet"}
	gkJob         = schema.GroupKind{Group: "batch", Kind: "Job"}
	gkCronJob     = schema.GroupKind{Group: "batch", Kind: "CronJob"}
)

// A Deployment in an opted-in namespace with workloadKinds that exclude
// Deployments: no AIBOM, and the one it had is deleted. Adding the kind
// back restores the document without a restart — the allowlist change
// alone must drive both, with no edit to the Deployment (Design 006 §2).
func TestIntegration_Allowlist_DeploymentFilteredAndRestored(t *testing.T) {
	env := startEnvTest(t)
	ctx := context.Background()
	ns := "allowlist-deploy"
	mustCreateOptedInNamespace(t, env.k8sClient, ctx, ns)

	dep := expVllmDeployment(ns, "serve")
	mustCreate(t, env.k8sClient, ctx, dep)
	key := types.NamespacedName{Name: AIBOMNameForWorkload("apps", "Deployment", "serve"), Namespace: ns}
	waitAIBOM(t, env, ctx, key, "unrestricted allowlist")

	setWorkloadKinds(t, env, gkStatefulSet)
	waitNoAIBOM(t, env.k8sClient, ctx, key)
	if got := testutil.ToFloat64(metrics.WorkloadReconcileOutcomes.WithLabelValues("Deployment", "kind_not_allowed")); got < 1 {
		t.Errorf("kind_not_allowed outcome must be counted, got %v", got)
	}

	// Still excluded: a change to the Deployment itself must not bring
	// the document back.
	eventually(t, 10*time.Second, 200*time.Millisecond, func() error {
		return env.k8sClient.Get(ctx, types.NamespacedName{Name: "serve", Namespace: ns}, dep)
	})
	if dep.Annotations == nil {
		dep.Annotations = map[string]string{}
	}
	dep.Annotations["touch"] = "1"
	if err := env.k8sClient.Update(ctx, dep); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Second)
	if aibomExists(env.k8sClient, ctx, key) {
		t.Fatal("an excluded kind must stay excluded on its own events")
	}

	setWorkloadKinds(t, env, gkStatefulSet, gkDeployment)
	waitAIBOM(t, env, ctx, key, "Deployment added back to workloadKinds")

	// And back to absent-or-empty: still reported.
	setWorkloadKinds(t, env)
	time.Sleep(time.Second)
	if !aibomExists(env.k8sClient, ctx, key) {
		t.Fatal("unrestricted allowlist must keep the document")
	}
}

// A supervised kind removed from the list: its watch is stopped and
// recorded as disabled (not Degraded, gauge 0), its AIBOMs are swept,
// and the StatefulSet it absorbed is reported as a root again. Adding
// it back restarts the watch, restores its document and re-absorbs the
// StatefulSet (Design 006 §3, §4 on the LWS → StatefulSet fixture).
func TestIntegration_Allowlist_SupervisedKindDisabledAndReenabled(t *testing.T) {
	env := startEnvTest(t)
	ctx := context.Background()
	ns := "allowlist-lws"
	mustCreateOptedInNamespace(t, env.k8sClient, ctx, ns)
	waitHealthy(t, env.watchHealth, "LeaderWorkerSet")

	lws := leaderWorkerSetCR(ns, "multi", nil, lwsPodTemplate("vllm/vllm-openai:v0.6.3", "--model", "Qwen/Qwen3-0.6B"), 2)
	mustCreate(t, env.k8sClient, ctx, lws)
	lwsKey := types.NamespacedName{Name: AIBOMNameForWorkload("leaderworkerset.x-k8s.io", "LeaderWorkerSet", "multi"), Namespace: ns}
	waitInlineContains(t, env.k8sClient, ctx, lwsKey, `"Qwen/Qwen3-0.6B"`)

	sts := rollupStatefulSet(ns, "multi-0", ownerRefTo("leaderworkerset.x-k8s.io/v1", "LeaderWorkerSet", "multi", lws.GetUID()))
	mustCreate(t, env.k8sClient, ctx, sts)
	stsKey := types.NamespacedName{Name: AIBOMNameForWorkload("apps", "StatefulSet", "multi-0"), Namespace: ns}
	// A pod of the StatefulSet re-reconciles the LWS through the chain
	// (as in the roll-up fixture); the LWS document then lists the
	// StatefulSet and carries the pod's digest.
	createPodWithDigest(t, env.k8sClient, ctx, ns, "multi-0-0", map[string]string{"app": "multi-0"},
		ownerRefTo("apps/v1", "StatefulSet", "multi-0", sts.UID), rollupDigest)
	waitInlineContains(t, env.k8sClient, ctx, lwsKey, rollupDigest, `"StatefulSet/multi-0"`)
	if aibomExists(env.k8sClient, ctx, stsKey) {
		t.Fatal("precondition: the StatefulSet is absorbed by the LWS")
	}

	// Remove LeaderWorkerSet from the allowlist.
	setWorkloadKinds(t, env, gkDeployment, gkStatefulSet, gkDaemonSet, gkJob, gkCronJob)
	waitWatchDisabled(t, env.watchHealth, "LeaderWorkerSet")
	if g := testutil.ToFloat64(metrics.WatchHealthy.WithLabelValues("LeaderWorkerSet")); g != 0 {
		t.Errorf("aibom_watch_healthy{LeaderWorkerSet} = %v while disabled, want 0", g)
	}
	if len(env.watchHealth.Degraded()) != 0 {
		t.Errorf("a disabled kind must not surface as Degraded: %+v", env.watchHealth.Degraded())
	}
	waitNoAIBOM(t, env.k8sClient, ctx, lwsKey)
	waitAIBOM(t, env, ctx, stsKey, "StatefulSet re-roots once its owner kind is disabled")

	// While disabled, a new LWS is not inventoried: its informer does
	// not exist.
	other := leaderWorkerSetCR(ns, "later", nil, lwsPodTemplate("vllm/vllm-openai:v0.6.3", "--model", "Qwen/Qwen3-0.6B"), 1)
	mustCreate(t, env.k8sClient, ctx, other)
	otherKey := types.NamespacedName{Name: AIBOMNameForWorkload("leaderworkerset.x-k8s.io", "LeaderWorkerSet", "later"), Namespace: ns}
	time.Sleep(2 * time.Second)
	if aibomExists(env.k8sClient, ctx, otherKey) {
		t.Fatal("a disabled kind must not be inventoried")
	}

	// Add it back: watch restarts, both LWS documents appear, the
	// StatefulSet is absorbed again.
	setWorkloadKinds(t, env)
	waitHealthy(t, env.watchHealth, "LeaderWorkerSet")
	if st, _ := env.watchHealth.Status("LeaderWorkerSet"); st.Disabled {
		t.Errorf("re-enabled kind must not read as disabled: %+v", st)
	}
	waitInlineContains(t, env.k8sClient, ctx, lwsKey, `"StatefulSet/multi-0"`)
	waitInlineContains(t, env.k8sClient, ctx, otherKey, `"Qwen/Qwen3-0.6B"`)
	waitNoAIBOM(t, env.k8sClient, ctx, stsKey)
}

// A kind excluded at startup (the allowlist was set before the
// controller came up) is never started and any AIBOMs it left behind
// are swept.
func TestIntegration_Allowlist_SupervisedKindExcludedAtStartup(t *testing.T) {
	env := startEnvTest(t)
	ctx := context.Background()
	// The harness starts unrestricted; narrow before any LWS exists so
	// the supervisor's steady state is "disabled", then seed a stale
	// AIBOM by hand to stand in for one left by a previous process.
	setWorkloadKinds(t, env, gkDeployment)
	waitWatchDisabled(t, env.watchHealth, "LeaderWorkerSet")

	ns := "allowlist-startup"
	mustCreateOptedInNamespace(t, env.k8sClient, ctx, ns)
	lws := leaderWorkerSetCR(ns, "multi", nil, lwsPodTemplate("vllm/vllm-openai:v0.6.3", "--model", "Qwen/Qwen3-0.6B"), 1)
	mustCreate(t, env.k8sClient, ctx, lws)
	lwsKey := types.NamespacedName{Name: AIBOMNameForWorkload("leaderworkerset.x-k8s.io", "LeaderWorkerSet", "multi"), Namespace: ns}
	time.Sleep(2 * time.Second)
	if aibomExists(env.k8sClient, ctx, lwsKey) {
		t.Fatal("an LWS created while the kind is disabled must not get a document")
	}

	// Re-enable, let the document appear, disable again: the sweep
	// must remove it even though no reconcile of the LWS runs.
	setWorkloadKinds(t, env)
	waitInlineContains(t, env.k8sClient, ctx, lwsKey, `"Qwen/Qwen3-0.6B"`)
	setWorkloadKinds(t, env, gkDeployment)
	waitWatchDisabled(t, env.watchHealth, "LeaderWorkerSet")
	waitNoAIBOM(t, env.k8sClient, ctx, lwsKey)
}
