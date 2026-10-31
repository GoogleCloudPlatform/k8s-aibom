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
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	"github.com/GoogleCloudPlatform/k8s-aibom/internal/metrics"
)

// CRD lifecycle after a watch started (Design 004 amendment A2 rows
// 5-9, #226 phase B). envtest runs no garbage collector, so every
// AIBOM deletion asserted here is the supervisor's own sweep.

func getCRD(t *testing.T, c client.Client, name string) *unstructured.Unstructured {
	t.Helper()
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(crdGVK)
	if err := c.Get(context.Background(), types.NamespacedName{Name: name}, u); err != nil {
		t.Fatal(err)
	}
	return u
}

// setServed flips spec.versions[version].served on a live CRD, keeping
// its UID.
func setServed(t *testing.T, c client.Client, name, version string, served bool) {
	t.Helper()
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		u := getCRD(t, c, name)
		versions, _, _ := unstructured.NestedSlice(u.Object, "spec", "versions")
		for i := range versions {
			m := versions[i].(map[string]interface{})
			if m["name"] == version {
				m["served"] = served
			}
		}
		_ = unstructured.SetNestedSlice(u.Object, versions, "spec", "versions")
		return c.Update(context.Background(), u)
	}); err != nil {
		t.Fatalf("set %s %s served=%v: %v", name, version, served, err)
	}
}

// deleteCRD deletes a CRD and waits until it is gone (the API server
// deletes its CRs first).
func deleteCRD(t *testing.T, c client.Client, name string) {
	t.Helper()
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(crdGVK)
	u.SetName(name)
	if err := c.Delete(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	eventually(t, 60*time.Second, 100*time.Millisecond, func() error {
		err := c.Get(context.Background(), types.NamespacedName{Name: name}, u)
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("CRD %s still present (err=%v)", name, err)
	})
}

func touchCRDLabel(t *testing.T, c client.Client, name, value string) {
	t.Helper()
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		u := getCRD(t, c, name)
		l := u.GetLabels()
		if l == nil {
			l = map[string]string{}
		}
		l["crd-lifecycle-test/touch"] = value
		u.SetLabels(l)
		return c.Update(context.Background(), u)
	}); err != nil {
		t.Fatal(err)
	}
}

func waitDegradedWith(t *testing.T, h *WatchHealth, kind, substr string) {
	t.Helper()
	eventually(t, 30*time.Second, 100*time.Millisecond, func() error {
		st, ok := h.Status(kind)
		if !ok || !st.Degraded() || !strings.Contains(st.LastError, substr) {
			return fmt.Errorf("%s not Degraded with %q yet: %+v", kind, substr, st)
		}
		return nil
	})
}

func waitDisabledFor(t *testing.T, h *WatchHealth, kind string, reason DisabledReason) {
	t.Helper()
	eventually(t, 30*time.Second, 100*time.Millisecond, func() error {
		st, ok := h.Status(kind)
		if !ok || !st.Disabled || st.DisabledReason != reason {
			return fmt.Errorf("%s not disabled(%s) yet: %+v", kind, reason, st)
		}
		return nil
	})
}

func dgdKeyFor(ns, name string) types.NamespacedName {
	return types.NamespacedName{Name: AIBOMNameForWorkload("nvidia.com", "DynamoGraphDeployment", name), Namespace: ns}
}

// Rows 5 and 6 on the two-version Dynamo fixture: v1beta1 stops being
// served (same UID). The watch stops, the kind is Degraded with the
// reason and one WatchUnhealthy, its AIBOMs are kept and it stays
// tracked; repeated CRD updates add nothing. Serving v1beta1 again
// restarts the watch with WatchRecovered (never WatchStarted) and
// inventory resumes, including a DGD created meanwhile.
func TestIntegration_CRDLifecycle_UnservableThenRecovered(t *testing.T) {
	env := startEnvTestWith(t, envTestOptions{
		omitExternalCRDs: []string{dgdCRDFile},
		events:           true,
		beforeManager: func(t *testing.T, c client.Client) {
			crd := &unstructured.Unstructured{}
			if err := yaml.Unmarshal([]byte(dgdTwoVersionCRD), &crd.Object); err != nil {
				t.Fatal(err)
			}
			mustCreate(t, c, context.Background(), crd)
		},
	})
	ctx := context.Background()
	events := &eventLog{rec: env.recorder}
	waitCRDServes(t, env, dgdCRDName, "v1beta1")
	waitHealthy(t, env.watchHealth, dgd)
	uid := getCRD(t, env.k8sClient, dgdCRDName).GetUID()

	ns := "lifecycle-unservable"
	mustCreateOptedInNamespace(t, env.k8sClient, ctx, ns)
	mustCreate(t, env.k8sClient, ctx, dgdAt("v1beta1", ns, "before"))
	waitAIBOM(t, env, ctx, dgdKeyFor(ns, "before"), "DGD while served")

	setServed(t, env.k8sClient, dgdCRDName, "v1beta1", false)
	waitDegradedWith(t, env.watchHealth, dgd, "not served")
	events.wait(t, EventReasonWatchUnhealthy, dgd)
	if !env.tracked.Has(gkDGD) {
		t.Error("row 5: an unservable kind stays tracked")
	}
	for i := 0; i < 3; i++ {
		touchCRDLabel(t, env.k8sClient, dgdCRDName, fmt.Sprint(i))
	}
	// Written while v1beta1 is not served: not inventoried until it is.
	mustCreate(t, env.k8sClient, ctx, dgdAt("v1alpha1", ns, "during"))
	time.Sleep(2 * time.Second)
	if !aibomExists(env.k8sClient, ctx, dgdKeyFor(ns, "before")) {
		t.Fatal("row 5: existing AIBOMs are kept")
	}
	if aibomExists(env.k8sClient, ctx, dgdKeyFor(ns, "during")) {
		t.Fatal("row 5: the watch is stopped while unservable")
	}
	if n := events.count(EventReasonWatchUnhealthy, dgd); n != 1 {
		t.Errorf("row 5: one WatchUnhealthy, got %d: %q", n, events.all())
	}
	if n := events.count(EventReasonWatchDisabled, dgd); n != 0 {
		t.Errorf("row 5 is not a removal: %d WatchDisabled", n)
	}

	setServed(t, env.k8sClient, dgdCRDName, "v1beta1", true)
	waitCRDServes(t, env, dgdCRDName, "v1beta1")
	waitHealthy(t, env.watchHealth, dgd)
	waitAIBOM(t, env, ctx, dgdKeyFor(ns, "during"), "row 6: inventory resumes")
	if got := getCRD(t, env.k8sClient, dgdCRDName).GetUID(); got != uid {
		t.Fatalf("precondition: same CRD UID throughout, %s != %s", got, uid)
	}
	time.Sleep(time.Second)
	if n := events.count(EventReasonWatchRecovered, dgd); n != 2 {
		t.Errorf("row 6: WatchRecovered at startup and on recovery (2), got %d: %q", n, events.all())
	}
	if n := events.count(EventReasonWatchStarted, dgd); n != 0 {
		t.Errorf("row 6 never announces WatchStarted, got %d", n)
	}
	if !aibomExists(env.k8sClient, ctx, dgdKeyFor(ns, "before")) {
		t.Error("row 6: AIBOMs kept through the outage")
	}
	requireManagerRunning(t, env)
}

// Row 8: deleting the CRD stops the watch, disables the kind with
// reason crd-removed (one WatchDisabled naming it, no WatchUnhealthy,
// never Degraded), drops it from TrackedKinds and sweeps its AIBOMs.
// Other kinds' documents are untouched.
func TestIntegration_CRDLifecycle_RemovedIsSwept(t *testing.T) {
	env := startEnvTestWith(t, envTestOptions{events: true})
	ctx := context.Background()
	events := &eventLog{rec: env.recorder}
	waitHealthy(t, env.watchHealth, dgd)

	ns := "lifecycle-removed"
	mustCreateOptedInNamespace(t, env.k8sClient, ctx, ns)
	mustCreate(t, env.k8sClient, ctx, dynamoGraphDeployment(ns, "graph", "vllm",
		dynamoComponent("VllmWorker", "worker", "Qwen/Qwen3-0.6B", "nvcr.io/nvidia/ai-dynamo/vllm-runtime:0.6.0")))
	mustCreate(t, env.k8sClient, ctx, expVllmDeployment(ns, "vllm"))
	waitAIBOM(t, env, ctx, dgdKeyFor(ns, "graph"), "DGD before removal")
	depKey := types.NamespacedName{Name: AIBOMNameForWorkload("apps", "Deployment", "vllm"), Namespace: ns}
	waitAIBOM(t, env, ctx, depKey, "Deployment before removal")

	deleteCRD(t, env.k8sClient, dgdCRDName)
	waitDisabledFor(t, env.watchHealth, dgd, DisabledCRDRemoved)
	waitNoAIBOM(t, env.k8sClient, ctx, dgdKeyFor(ns, "graph"))
	events.wait(t, EventReasonWatchDisabled, dgd)
	if env.tracked.Has(gkDGD) {
		t.Error("row 8: a removed kind is not tracked")
	}
	if g := testutil.ToFloat64(metrics.WatchHealthy.WithLabelValues(dgd)); g != 0 {
		t.Errorf("gauge %v, want 0", g)
	}
	time.Sleep(2 * time.Second)
	if n := events.count(EventReasonWatchDisabled, dgd); n != 1 {
		t.Errorf("row 8: WatchDisabled once, got %d: %q", n, events.all())
	}
	for _, e := range events.all() {
		if strings.Contains(e, " "+EventReasonWatchDisabled+" "+dgd+" ") && !strings.Contains(e, "crd-removed") {
			t.Errorf("WatchDisabled must carry the reason: %q", e)
		}
	}
	if n := events.count(EventReasonWatchUnhealthy, dgd); n != 0 {
		t.Errorf("row 8: a removal is not a failure; %d WatchUnhealthy: %q", n, events.all())
	}
	if d := env.watchHealth.Degraded(); len(d) != 0 {
		t.Errorf("nothing Degraded after a removal: %+v", d)
	}
	if !aibomExists(env.k8sClient, ctx, depKey) {
		t.Error("other kinds' AIBOMs are untouched")
	}
	if !env.watchHealth.IsHealthy("DynamoComponentDeployment") {
		t.Error("other supervised kinds are unaffected")
	}
	requireManagerRunning(t, env)
}

// Row 9, quickly: delete the CRD and re-create it at once (new UID),
// then create a CR with the old name. The old generation is retired
// (WatchDisabled) before the new one starts (WatchStarted), and the
// new CR's AIBOM, which has the same name as the swept one, survives:
// no sweep of the old generation runs after the new one is active.
func TestIntegration_CRDLifecycle_RecreatedUnderNewUID(t *testing.T) {
	opts := envTestOptions{events: true}
	reads := observeCRDReads(&opts)
	env := startEnvTestWith(t, opts)
	ctx := context.Background()
	events := &eventLog{rec: env.recorder}
	waitHealthy(t, env.watchHealth, dgd)
	oldUID := getCRD(t, env.k8sClient, dgdCRDName).GetUID()

	ns := "lifecycle-recreated"
	mustCreateOptedInNamespace(t, env.k8sClient, ctx, ns)
	mk := func() *unstructured.Unstructured {
		return dynamoGraphDeployment(ns, "graph", "vllm",
			dynamoComponent("VllmWorker", "worker", "Qwen/Qwen3-0.6B", "nvcr.io/nvidia/ai-dynamo/vllm-runtime:0.6.0"))
	}
	mustCreate(t, env.k8sClient, ctx, mk())
	waitAIBOM(t, env, ctx, dgdKeyFor(ns, "graph"), "first generation")

	deleteCRD(t, env.k8sClient, dgdCRDName)
	installExternalCRD(t, env, dgdCRDFile)
	waitCRDServes(t, env, dgdCRDName, "v1beta1")
	if getCRD(t, env.k8sClient, dgdCRDName).GetUID() == oldUID {
		t.Fatal("precondition: re-created CRD has a new UID")
	}
	_ = reads
	eventually(t, 30*time.Second, 100*time.Millisecond, func() error {
		if events.count(EventReasonWatchStarted, dgd) == 0 {
			return fmt.Errorf("no WatchStarted yet: %q", events.all())
		}
		return nil
	})
	waitHealthy(t, env.watchHealth, dgd)
	var disabledAt, startedAt = -1, -1
	for i, e := range events.all() {
		if disabledAt < 0 && strings.Contains(e, " "+EventReasonWatchDisabled+" "+dgd+" ") {
			disabledAt = i
		}
		if startedAt < 0 && strings.Contains(e, " "+EventReasonWatchStarted+" "+dgd+" ") {
			startedAt = i
		}
	}
	if disabledAt < 0 || disabledAt >= startedAt {
		t.Fatalf("row 9: WatchDisabled(old) before WatchStarted(new); events %q", events.all())
	}
	waitNoAIBOM(t, env.k8sClient, ctx, dgdKeyFor(ns, "graph")) // swept (its CR went with the CRD)
	if !env.tracked.Has(gkDGD) {
		t.Error("row 9: the new generation is tracked")
	}

	mustCreate(t, env.k8sClient, ctx, mk())
	waitAIBOM(t, env, ctx, dgdKeyFor(ns, "graph"), "new generation's CR")
	time.Sleep(3 * time.Second)
	if !aibomExists(env.k8sClient, ctx, dgdKeyFor(ns, "graph")) {
		t.Fatal("row 9: a late sweep of the old generation deleted the new generation's AIBOM")
	}
	if n := events.count(EventReasonWatchDisabled, dgd); n != 1 {
		t.Errorf("one WatchDisabled, got %d: %q", n, events.all())
	}
	if n := events.count(EventReasonWatchStarted, dgd); n != 1 {
		t.Errorf("one WatchStarted, got %d: %q", n, events.all())
	}
	requireManagerRunning(t, env)
}

// Row 8 with the roll-up (Design 005): removing the LWS CRD sweeps the
// LWS document and re-roots the StatefulSet it had absorbed, which then
// gets its own AIBOM through ownerUnresolved.
func TestIntegration_CRDLifecycle_RemovedOwnerReRootsChildren(t *testing.T) {
	env := startEnvTestWith(t, envTestOptions{events: true})
	ctx := context.Background()
	ns := "lifecycle-reroot"
	mustCreateOptedInNamespace(t, env.k8sClient, ctx, ns)
	waitHealthy(t, env.watchHealth, "LeaderWorkerSet")

	lws := leaderWorkerSetCR(ns, "multi", nil, lwsPodTemplate("vllm/vllm-openai:v0.6.3", "--model", "Qwen/Qwen3-0.6B"), 2)
	mustCreate(t, env.k8sClient, ctx, lws)
	lwsKey := types.NamespacedName{Name: AIBOMNameForWorkload("leaderworkerset.x-k8s.io", "LeaderWorkerSet", "multi"), Namespace: ns}
	waitInlineContains(t, env.k8sClient, ctx, lwsKey, `"Qwen/Qwen3-0.6B"`)
	sts := rollupStatefulSet(ns, "multi-0", ownerRefTo("leaderworkerset.x-k8s.io/v1", "LeaderWorkerSet", "multi", lws.GetUID()))
	mustCreate(t, env.k8sClient, ctx, sts)
	stsKey := types.NamespacedName{Name: AIBOMNameForWorkload("apps", "StatefulSet", "multi-0"), Namespace: ns}
	createPodWithDigest(t, env.k8sClient, ctx, ns, "multi-0-0", map[string]string{"app": "multi-0"},
		ownerRefTo("apps/v1", "StatefulSet", "multi-0", sts.UID), rollupDigest)
	waitInlineContains(t, env.k8sClient, ctx, lwsKey, rollupDigest, `"StatefulSet/multi-0"`)
	if aibomExists(env.k8sClient, ctx, stsKey) {
		t.Fatal("precondition: the StatefulSet is absorbed by the LWS")
	}

	deleteCRD(t, env.k8sClient, lwsCRDName)
	waitDisabledFor(t, env.watchHealth, "LeaderWorkerSet", DisabledCRDRemoved)
	waitNoAIBOM(t, env.k8sClient, ctx, lwsKey)
	waitAIBOM(t, env, ctx, stsKey, "the StatefulSet re-roots once its owner kind is gone")
	gkLWSLocal := schema.GroupKind{Group: "leaderworkerset.x-k8s.io", Kind: "LeaderWorkerSet"}
	if env.tracked.Has(gkLWSLocal) {
		t.Error("removed owner kind is not tracked")
	}
	requireManagerRunning(t, env)
}

// Row 7: excluding a kind disables and sweeps once, however many
// times apply() runs afterwards (repeated CRD events, allowlist
// rotations that keep it excluded); its CRD is still present, so it is
// not a removal.
func TestIntegration_CRDLifecycle_ExclusionDisablesOnce(t *testing.T) {
	env := startEnvTestWith(t, envTestOptions{events: true})
	ctx := context.Background()
	events := &eventLog{rec: env.recorder}
	waitHealthy(t, env.watchHealth, dgd)
	ns := "lifecycle-excluded"
	mustCreateOptedInNamespace(t, env.k8sClient, ctx, ns)
	mustCreate(t, env.k8sClient, ctx, dynamoGraphDeployment(ns, "graph", "vllm",
		dynamoComponent("VllmWorker", "worker", "Qwen/Qwen3-0.6B", "nvcr.io/nvidia/ai-dynamo/vllm-runtime:0.6.0")))
	waitAIBOM(t, env, ctx, dgdKeyFor(ns, "graph"), "DGD before exclusion")

	setWorkloadKinds(t, env, gkDeployment, gkDCD, gkLWS)
	waitDisabledFor(t, env.watchHealth, dgd, DisabledByConfig)
	waitNoAIBOM(t, env.k8sClient, ctx, dgdKeyFor(ns, "graph"))
	for i := 0; i < 3; i++ {
		touchCRDLabel(t, env.k8sClient, dgdCRDName, fmt.Sprint(i))
	}
	setWorkloadKinds(t, env, gkDeployment, gkDCD, gkLWS, gkNIM)
	time.Sleep(2 * time.Second)
	if n := events.count(EventReasonWatchDisabled, dgd); n != 1 {
		t.Errorf("row 7: one WatchDisabled, got %d: %q", n, events.all())
	}
	if !env.tracked.Has(gkDGD) {
		t.Error("an excluded kind whose CRD is present stays tracked")
	}
	if st, _ := env.watchHealth.Status(dgd); st.DisabledReason != DisabledByConfig {
		t.Errorf("exclusion is not removal: reason %q", st.DisabledReason)
	}
	requireManagerRunning(t, env)
}

// Transient CRD read failures on a Ready, running kind (403, timeout)
// are no evidence: the watch keeps running, nothing is disabled or
// swept, and inventory continues. Lifting the fault changes nothing.
func TestIntegration_CRDLifecycle_TransientReadFailuresAreNotRemoval(t *testing.T) {
	opts := envTestOptions{events: true}
	reads := observeCRDReads(&opts)
	env := startEnvTestWith(t, opts)
	ctx := context.Background()
	events := &eventLog{rec: env.recorder}
	waitHealthy(t, env.watchHealth, dgd)
	ns := "lifecycle-transient"
	mustCreateOptedInNamespace(t, env.k8sClient, ctx, ns)
	mustCreate(t, env.k8sClient, ctx, dynamoGraphDeployment(ns, "graph", "vllm",
		dynamoComponent("VllmWorker", "worker", "Qwen/Qwen3-0.6B", "nvcr.io/nvidia/ai-dynamo/vllm-runtime:0.6.0")))
	waitAIBOM(t, env, ctx, dgdKeyFor(ns, "graph"), "DGD before the faults")

	for _, fault := range []error{
		apierrors.NewForbidden(schema.GroupResource{Group: "apiextensions.k8s.io", Resource: "customresourcedefinitions"}, dgdCRDName, fmt.Errorf("test")),
		apierrors.NewTimeoutError("test", 1),
		fmt.Errorf("dial tcp 127.0.0.1:6443: connect: connection refused"),
	} {
		reads.failNow.Store(dgdCRDName, fault)
		touchCRDLabel(t, env.k8sClient, dgdCRDName, fmt.Sprint(time.Now().UnixNano())) // an event, so discovery reads
		time.Sleep(2 * time.Second)
		if st, _ := env.watchHealth.Status(dgd); !st.Healthy || st.Disabled {
			t.Fatalf("%v: a failing CRD read changes nothing; got %+v", fault, st)
		}
	}
	mustCreate(t, env.k8sClient, ctx, dynamoGraphDeployment(ns, "graph2", "vllm",
		dynamoComponent("VllmWorker", "worker", "Qwen/Qwen3-0.6B", "nvcr.io/nvidia/ai-dynamo/vllm-runtime:0.6.0")))
	waitAIBOM(t, env, ctx, dgdKeyFor(ns, "graph2"), "inventory continues while CRD reads fail")
	reads.failNow.Delete(dgdCRDName)
	touchCRDLabel(t, env.k8sClient, dgdCRDName, "healed")
	time.Sleep(time.Second)
	if !aibomExists(env.k8sClient, ctx, dgdKeyFor(ns, "graph")) {
		t.Error("no AIBOM may be swept on read failures")
	}
	for _, reason := range []string{EventReasonWatchDisabled, EventReasonWatchUnhealthy, EventReasonWatchStarted} {
		if n := events.count(reason, dgd); n != 0 {
			t.Errorf("%s for DGD: %d, want 0; %q", reason, n, events.all())
		}
	}
	requireManagerRunning(t, env)
}
