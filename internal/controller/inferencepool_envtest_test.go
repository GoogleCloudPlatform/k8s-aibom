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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/watchlist"
	"sigs.k8s.io/controller-runtime/pkg/client"

	aibomv1beta1 "github.com/GoogleCloudPlatform/k8s-aibom/api/v1beta1"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/bom"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/scraper"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/sink"
)

// Design 007 §2 (Part 2a) against a real API server: matching on Pod
// templates only, the document facts, pool lifecycle, CRD lifecycle
// through #226 discovery, and the unavailable view.

const poolCRDFile = "inference.networking.k8s.io_inferencepools.yaml"

type pm = scraper.InferencePoolMembership

func createPool(t *testing.T, c client.Client, ctx context.Context, ns, name string, sel map[string]string, picker string) {
	t.Helper()
	mustCreate(t, c, ctx, pool(ns, name, sel, picker))
}

func updatePoolSelector(t *testing.T, c client.Client, ctx context.Context, ns, name string, sel map[string]string) {
	t.Helper()
	eventually(t, 10*time.Second, 100*time.Millisecond, func() error {
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(InferencePoolGVK)
		if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, u); err != nil {
			return err
		}
		m := map[string]interface{}{}
		for k, v := range sel {
			m[k] = v
		}
		_ = unstructured.SetNestedMap(u.Object, m, "spec", "selector", "matchLabels")
		return c.Update(ctx, u)
	})
}

func deletePool(t *testing.T, c client.Client, ctx context.Context, ns, name string) {
	t.Helper()
	if err := c.Delete(ctx, pool(ns, name, nil, "")); err != nil && !apierrors.IsNotFound(err) {
		t.Fatalf("delete pool %s/%s: %v", ns, name, err)
	}
}

func getAIBOM(c client.Client, ctx context.Context, key types.NamespacedName) (*aibomv1beta1.AIBOM, error) {
	var a aibomv1beta1.AIBOM
	if err := c.Get(ctx, key, &a); err != nil {
		return nil, err
	}
	return &a, nil
}

// waitRouting waits until the AIBOM's inline document records exactly
// want (nil: no routing properties) and returns the AIBOM.
func waitRouting(t *testing.T, c client.Client, ctx context.Context, key types.NamespacedName, want []pm) *aibomv1beta1.AIBOM {
	t.Helper()
	var a *aibomv1beta1.AIBOM
	eventually(t, 60*time.Second, 200*time.Millisecond, func() error {
		var err error
		if a, err = getAIBOM(c, ctx, key); err != nil {
			return err
		}
		got, ok := publishedInferencePools(a)
		if !ok {
			return fmt.Errorf("no inline document yet")
		}
		if !reflect.DeepEqual(got, want) {
			return fmt.Errorf("routing %v, want %v", got, want)
		}
		return nil
	})
	return a
}

// requireDocUnchanged checks, over a few seconds, that the document and
// input hash stay byte-identical to a (no re-emission).
func requireDocUnchanged(t *testing.T, c client.Client, ctx context.Context, key types.NamespacedName, a *aibomv1beta1.AIBOM, why string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		b, err := getAIBOM(c, ctx, key)
		if err != nil {
			t.Fatalf("%s: %v", why, err)
		}
		if b.Status.InputHash != a.Status.InputHash || !bytes.Equal(b.Status.BOMDocument.Inline.Data, a.Status.BOMDocument.Inline.Data) {
			t.Fatalf("%s: the document was re-emitted (hash %s -> %s)", why, a.Status.InputHash, b.Status.InputHash)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// componentsOf returns the document's components array, the part of a
// document pool membership must never change.
func componentsOf(t *testing.T, a *aibomv1beta1.AIBOM) string {
	t.Helper()
	var doc struct {
		Components json.RawMessage `json:"components"`
	}
	if err := json.Unmarshal(a.Status.BOMDocument.Inline.Data, &doc); err != nil {
		t.Fatal(err)
	}
	return string(doc.Components)
}

func poolDeployment(ns, name string, templateLabels, topLabels map[string]string, image string) *appsv1.Deployment {
	d := expVllmDeployment(ns, name)
	for k, v := range templateLabels {
		d.Spec.Template.Labels[k] = v
	}
	d.Labels = topLabels
	d.Spec.Template.Spec.Containers[0].Image = image
	return d
}

func setDeploymentImage(t *testing.T, c client.Client, ctx context.Context, ns, name, image string, addTemplateLabels map[string]string) {
	t.Helper()
	eventually(t, 10*time.Second, 100*time.Millisecond, func() error {
		var d appsv1.Deployment
		if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &d); err != nil {
			return err
		}
		if image != "" {
			d.Spec.Template.Spec.Containers[0].Image = image
		}
		for k, v := range addTemplateLabels {
			d.Spec.Template.Labels[k] = v
		}
		return c.Update(ctx, &d)
	})
}

// Tests 1, 2, 6, 7, 8, 10 (pool changes), 12, 13: same-namespace
// matching on Pod-template labels only, AND within a selector, sorted
// and deduplicated facts with the optional Endpoint Picker, pool
// create/update/delete and template label changes, hash-driven
// rebuilds, and no change to eligibility or to any other fact.
func TestIntegration_InferencePool_MembershipFacts(t *testing.T) {
	env := startEnvTest(t)
	ctx := context.Background()
	c := env.k8sClient
	ns, other := "pool-a", "pool-b"
	mustCreateOptedInNamespace(t, c, ctx, ns)
	mustCreateOptedInNamespace(t, c, ctx, other)
	serving := map[string]string{"llm-d.ai/inferenceServing": "true"}

	// Pools that must never match: a same-selector pool in another
	// namespace, an AND selector half met by the template, a selector
	// on the Deployment's own labels, and one on labels only a live Pod
	// carries.
	createPool(t, c, ctx, other, "serving", serving, "")
	createPool(t, c, ctx, ns, "and-miss", map[string]string{"app": "vllm-a", "tier": "x"}, "")
	createPool(t, c, ctx, ns, "top-labels", map[string]string{"owner": "team-a"}, "")
	createPool(t, c, ctx, ns, "live-pod", map[string]string{"webhook-injected": "yes"}, "")
	createPool(t, c, ctx, ns, "serving", serving, "serving-epp")

	dep := poolDeployment(ns, "vllm-a", serving, map[string]string{"owner": "team-a"}, "vllm/vllm-openai:v0.6.3")
	mustCreate(t, c, ctx, dep)
	key := types.NamespacedName{Name: AIBOMNameForWorkload("apps", "Deployment", "vllm-a"), Namespace: ns}
	// A live Pod carrying the label a webhook would add: never matched.
	mustCreate(t, c, ctx, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "vllm-a-live", Labels: map[string]string{"app": "vllm-a", "webhook-injected": "yes"}},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "vllm/vllm-openai:v0.6.3"}}},
	})

	// A workload that is not inference, whose template matches a pool:
	// still no document (a match never makes a workload eligible).
	plain := poolDeployment(ns, "web", serving, nil, "nginx:1.27")
	plain.Spec.Template.Spec.Containers[0].Args = nil
	mustCreate(t, c, ctx, plain)

	first := waitRouting(t, c, ctx, key, []pm{{Pool: ns + "/serving", EndpointPicker: "serving-epp"}})
	baseComponents := componentsOf(t, first)

	// A second matching pool sorts first; the document is rebuilt.
	createPool(t, c, ctx, ns, "a-serving", serving, "")
	two := waitRouting(t, c, ctx, key, []pm{{Pool: ns + "/a-serving"}, {Pool: ns + "/serving", EndpointPicker: "serving-epp"}})
	if two.Status.InputHash == first.Status.InputHash {
		t.Error("a pool-only change must change the input hash")
	}
	if componentsOf(t, two) != baseComponents {
		t.Error("pool membership changed the document's components")
	}

	// A selector change removes the old match.
	updatePoolSelector(t, c, ctx, ns, "serving", map[string]string{"nope": "x"})
	waitRouting(t, c, ctx, key, []pm{{Pool: ns + "/a-serving"}})
	// A deleted pool is removed; no routing properties remain.
	deletePool(t, c, ctx, ns, "a-serving")
	none := waitRouting(t, c, ctx, key, nil)
	if bytes.Contains(none.Status.BOMDocument.Inline.Data, []byte("routing")) {
		t.Error("a document with no matching pool carries routing properties")
	}
	if componentsOf(t, none) != baseComponents {
		t.Error("removing pool membership changed the document's components")
	}

	// A template label change completes the AND selector.
	setDeploymentImage(t, c, ctx, ns, "vllm-a", "", map[string]string{"tier": "x"})
	waitRouting(t, c, ctx, key, []pm{{Pool: ns + "/and-miss"}})

	if aibomExists(c, ctx, types.NamespacedName{Name: AIBOMNameForWorkload("apps", "Deployment", "web"), Namespace: ns}) {
		t.Error("a non-inference workload must not get a document because a pool selects it")
	}
	// InferencePool is neither a reported nor a tracked kind.
	requireNoHealthEntry(t, env.watchHealth, inferencePoolName)
	if env.tracked.Has(schema.GroupKind{Group: InferencePoolGVK.Group, Kind: inferencePoolName}) {
		t.Error("InferencePool must not be tracked")
	}
	var list aibomv1beta1.AIBOMList
	if err := c.List(ctx, &list, client.InNamespace(ns)); err != nil {
		t.Fatal(err)
	}
	for _, a := range list.Items {
		if a.Spec.WorkloadRef.Kind == inferencePoolName {
			t.Errorf("an InferencePool got an AIBOM: %s", a.Name)
		}
	}
}

// Tests 3, 4 and 5: DisaggregatedSet per-role and Dynamo per-component
// templates count, each on its own: a selector whose pairs sit on
// different templates matches nothing.
func TestIntegration_InferencePool_RoleAndComponentTemplates(t *testing.T) {
	env := startEnvTest(t)
	ctx := context.Background()
	c := env.k8sClient
	ns := "pool-roles"
	mustCreateOptedInNamespace(t, c, ctx, ns)

	createPool(t, c, ctx, ns, "decode", map[string]string{"llm-d.ai/role": "decode"}, "decode-epp")
	createPool(t, c, ctx, ns, "prefill", map[string]string{"llm-d.ai/role": "prefill"}, "")
	createPool(t, c, ctx, ns, "mixed", map[string]string{"llm-d.ai/role": "prefill", "tier": "gpu"}, "")
	createPool(t, c, ctx, ns, "dynamo-worker", map[string]string{"nvidia.com/dynamo-component": "worker"}, "")

	labeled := func(labels map[string]interface{}) map[string]interface{} {
		tmpl := lwsPodTemplate("vllm/vllm-openai:v0.11.0", "--model", "deepseek-ai/DeepSeek-R1-0528")
		tmpl["metadata"] = map[string]interface{}{"labels": labels}
		return tmpl
	}
	set := disaggregatedSetCR(ns, "wide-ep", map[string]map[string]interface{}{
		"prefill": labeled(map[string]interface{}{"llm-d.ai/role": "prefill"}),
		"decode":  labeled(map[string]interface{}{"llm-d.ai/role": "decode", "tier": "gpu"}),
	}, 2)
	mustCreate(t, c, ctx, set)
	setKey := types.NamespacedName{Name: AIBOMNameForWorkload("disaggregatedset.x-k8s.io", "DisaggregatedSet", "wide-ep"), Namespace: ns}
	waitRouting(t, c, ctx, setKey, []pm{{Pool: ns + "/decode", EndpointPicker: "decode-epp"}, {Pool: ns + "/prefill"}})

	worker := dynamoComponent("VllmWorker", "worker", "Qwen/Qwen3-0.6B", "nvcr.io/nvidia/ai-dynamo/vllm-runtime:0.6.0")
	worker["podTemplate"].(map[string]interface{})["metadata"] = map[string]interface{}{
		"labels": map[string]interface{}{"nvidia.com/dynamo-component": "worker"}}
	mustCreate(t, c, ctx, dynamoGraphDeployment(ns, "qwen", "vllm",
		dynamoComponent("Frontend", "frontend", "", "nvcr.io/nvidia/ai-dynamo/dynamo-frontend:0.6.0"), worker))
	dgdKey := types.NamespacedName{Name: AIBOMNameForWorkload("nvidia.com", "DynamoGraphDeployment", "qwen"), Namespace: ns}
	waitRouting(t, c, ctx, dgdKey, []pm{{Pool: ns + "/dynamo-worker"}})
}

// Tests 9, 10 (CRD) and 11: the CRD is absent at startup and installed
// later (no restart); removing it keeps the published facts without
// re-emitting the document; a workload change while it is gone keeps
// them too; re-creating it reconciles against the new (empty) view and
// clears the stale facts.
func TestIntegration_InferencePool_CRDLifecycle(t *testing.T) {
	opts := envTestOptions{omitExternalCRDs: []string{poolCRDFile}}
	reads := observeCRDReads(&opts)
	env := startEnvTestWith(t, opts)
	ctx := context.Background()
	c := env.k8sClient
	ns := "pool-crd"
	mustCreateOptedInNamespace(t, c, ctx, ns)
	serving := map[string]string{"llm-d.ai/inferenceServing": "true"}
	mustCreate(t, c, ctx, poolDeployment(ns, "vllm", serving, nil, "vllm/vllm-openai:v0.6.3"))
	key := types.NamespacedName{Name: AIBOMNameForWorkload("apps", "Deployment", "vllm"), Namespace: ns}
	waitRouting(t, c, ctx, key, nil)
	if st := env.pools.State(); st != PoolViewInactive {
		t.Fatalf("view %s with no CRD", st)
	}

	waitAbsenceObserved(t, reads, InferencePoolCRD)
	installExternalCRD(t, env, poolCRDFile)
	waitCRDServes(t, env, InferencePoolCRD, "v1")
	createPool(t, c, ctx, ns, "serving", serving, "epp")
	published := waitRouting(t, c, ctx, key, []pm{{Pool: ns + "/serving", EndpointPicker: "epp"}})

	// Removal: the view stops; the fan-out it requests reconciles the
	// workload, which keeps its facts and re-emits nothing.
	deleteCRD(t, c, InferencePoolCRD)
	eventually(t, 30*time.Second, 100*time.Millisecond, func() error {
		if st := env.pools.State(); st != PoolViewInactive {
			return fmt.Errorf("view %s after CRD removal", st)
		}
		return nil
	})
	requireDocUnchanged(t, c, ctx, key, published, "CRD removal")

	// A workload change while the CRD is gone rebuilds the document with
	// the published facts kept.
	setDeploymentImage(t, c, ctx, ns, "vllm", "vllm/vllm-openai:v0.7.0", nil)
	kept := waitRouting(t, c, ctx, key, []pm{{Pool: ns + "/serving", EndpointPicker: "epp"}})
	waitInlineContains(t, c, ctx, key, "v0.7.0")
	_ = kept

	// Re-created (new UID, no pools): the view lists nothing and the
	// stale fact is removed; the workload's facts stay current.
	installExternalCRD(t, env, poolCRDFile)
	waitCRDServes(t, env, InferencePoolCRD, "v1")
	cleared := waitRouting(t, c, ctx, key, nil)
	if !bytes.Contains(cleared.Status.BOMDocument.Inline.Data, []byte("v0.7.0")) {
		t.Error("the workload's own facts must be current")
	}
	requireNoHealthEntry(t, env.watchHealth, inferencePoolName)
	requireManagerRunning(t, env)
}

// poolFault fails InferencePool list/watch on demand and can end live
// watches, so the reflector has to come back through a failing call.
type poolFault struct {
	mu        sync.Mutex
	on        bool
	forbidden bool // permission denied instead of a transient error
	watches   []watch.Interface
}

func (f *poolFault) set(on bool) {
	f.mu.Lock()
	f.on = on
	f.mu.Unlock()
}

func (f *poolFault) failing() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.on
}

func (f *poolFault) breakWatches() {
	f.mu.Lock()
	ws := f.watches
	f.watches = nil
	f.mu.Unlock()
	for _, w := range ws {
		w.Stop()
	}
}

func (f *poolFault) err() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.forbidden {
		return apierrors.NewForbidden(schema.GroupResource{Group: InferencePoolGVK.Group, Resource: "inferencepools"}, "",
			fmt.Errorf("injected: RBAC does not grant list on inferencepools"))
	}
	return apierrors.NewServiceUnavailable("injected InferencePool read failure")
}

type faultyDynamic struct {
	dynamic.Interface
	f *poolFault
}

// IsWatchListSemanticsUnSupported keeps the reflector on the same
// list/watch path as the wrapped client.
func (d faultyDynamic) IsWatchListSemanticsUnSupported() bool {
	return watchlist.DoesClientNotSupportWatchListSemantics(d.Interface)
}

func (d faultyDynamic) Resource(gvr schema.GroupVersionResource) dynamic.NamespaceableResourceInterface {
	return faultyResource{d.Interface.Resource(gvr), d.f}
}

type faultyResource struct {
	dynamic.NamespaceableResourceInterface
	f *poolFault
}

func (r faultyResource) Namespace(ns string) dynamic.ResourceInterface {
	return faultyNamespaced{r.NamespaceableResourceInterface.Namespace(ns), r.f}
}

type faultyNamespaced struct {
	dynamic.ResourceInterface
	f *poolFault
}

func (r faultyNamespaced) List(ctx context.Context, opts metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	if r.f.failing() {
		return nil, r.f.err()
	}
	return r.ResourceInterface.List(ctx, opts)
}

func (r faultyNamespaced) Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
	if r.f.failing() {
		return nil, r.f.err()
	}
	w, err := r.ResourceInterface.Watch(ctx, opts)
	if err == nil {
		r.f.mu.Lock()
		r.f.watches = append(r.f.watches, w)
		r.f.mu.Unlock()
	}
	return w, err
}

// Test 11 (and 12): with the CRD Ready, a failing pool list/watch makes
// the view unavailable: no document is re-emitted, a workload change
// keeps the published facts, and pool changes made meanwhile are not
// applied; once a list succeeds the stale facts are replaced. A
// recovery that changes nothing re-emits nothing.
func TestIntegration_InferencePool_ReadFailurePreservesThenRecovers(t *testing.T) {
	fault := &poolFault{}
	env := startEnvTestWith(t, envTestOptions{pools: faultyPools(fault)})
	ctx := context.Background()
	c := env.k8sClient
	ns := "pool-faults"
	mustCreateOptedInNamespace(t, c, ctx, ns)
	serving := map[string]string{"llm-d.ai/inferenceServing": "true"}
	createPool(t, c, ctx, ns, "p", serving, "")
	mustCreate(t, c, ctx, poolDeployment(ns, "vllm", serving, nil, "vllm/vllm-openai:v0.6.3"))
	key := types.NamespacedName{Name: AIBOMNameForWorkload("apps", "Deployment", "vllm"), Namespace: ns}
	published := waitRouting(t, c, ctx, key, []pm{{Pool: ns + "/p"}})

	breakView := func() {
		t.Helper()
		fault.set(true)
		fault.breakWatches()
		eventually(t, 30*time.Second, 50*time.Millisecond, func() error {
			if st := env.pools.State(); st != PoolViewUnsynced {
				return fmt.Errorf("view %s", st)
			}
			return nil
		})
	}
	breakView()
	requireDocUnchanged(t, c, ctx, key, published, "watch failure")

	// While unavailable: the pool is replaced by another; neither change
	// is applied, and a workload change keeps the published fact.
	deletePool(t, c, ctx, ns, "p")
	createPool(t, c, ctx, ns, "q", serving, "q-epp")
	setDeploymentImage(t, c, ctx, ns, "vllm", "vllm/vllm-openai:v0.7.0", nil)
	waitInlineContains(t, c, ctx, key, "v0.7.0")
	waitRouting(t, c, ctx, key, []pm{{Pool: ns + "/p"}})

	// Recovery: relisted, the stale fact is replaced.
	fault.set(false)
	recovered := waitRouting(t, c, ctx, key, []pm{{Pool: ns + "/q", EndpointPicker: "q-epp"}})

	// A flap that changes nothing re-emits nothing.
	breakView()
	fault.set(false)
	eventually(t, 60*time.Second, 100*time.Millisecond, func() error {
		if st := env.pools.State(); st != PoolViewUsable {
			return fmt.Errorf("view %s", st)
		}
		return nil
	})
	requireDocUnchanged(t, c, ctx, key, recovered, "recovery without changes")
	requireManagerRunning(t, env)
}

// docSink records every emitted document by workload name: the only
// place an external or truncated document's content can be checked.
type docSink struct {
	mu   sync.Mutex
	docs map[string][][]byte
}

func (s *docSink) Name() string { return "test-docs" }

func (s *docSink) Emit(_ context.Context, doc *bom.Document, meta sink.SinkMeta) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.docs == nil {
		s.docs = map[string][][]byte{}
	}
	s.docs[meta.WorkloadName] = append(s.docs[meta.WorkloadName], append([]byte(nil), doc.JSON...))
	return "test://" + meta.WorkloadNamespace + "/" + meta.WorkloadName, nil
}

func (s *docSink) HealthCheck(context.Context) error { return nil }
func (s *docSink) WriteOnly() bool                   { return false }

func (s *docSink) emitted(name string) [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]byte(nil), s.docs[name]...)
}

// routingOfJSON reads the routing facts of one emitted document.
func routingOfJSON(t *testing.T, data []byte) []pm {
	t.Helper()
	facts, ok := publishedInferencePools(&aibomv1beta1.AIBOM{Status: aibomv1beta1.AIBOMStatus{
		BOMDocument: &aibomv1beta1.BOMDocumentRef{Inline: &aibomv1beta1.InlineBOM{Data: data}}}})
	if !ok {
		t.Fatal("emitted document does not parse")
	}
	return facts
}

// externalDocuments makes every document external: an inline threshold
// below any document's size, and one capturing sink.
func externalDocuments(env *envTestEnv) *docSink {
	ds := &docSink{}
	prev := env.configStore.Load()
	next := *prev
	next.InlineThreshold = 1
	next.ExternalSinks = []sink.Sink{ds}
	next.SourceGeneration = prev.SourceGeneration + 1
	next.LoadedAt = time.Now()
	env.configStore.Store(&next)
	return ds
}

// waitEmitted waits for an emitted document of name that contains
// every want and records exactly facts; it returns the emit count.
func waitEmitted(t *testing.T, ds *docSink, name string, facts []pm, wants ...string) int {
	t.Helper()
	n := 0
	eventually(t, 60*time.Second, 200*time.Millisecond, func() error {
		docs := ds.emitted(name)
		n = len(docs)
		if n == 0 {
			return fmt.Errorf("nothing emitted for %s", name)
		}
		last := docs[n-1]
		for _, w := range wants {
			if !bytes.Contains(last, []byte(w)) {
				return fmt.Errorf("last document lacks %q", w)
			}
		}
		if got := routingOfJSON(t, last); !reflect.DeepEqual(got, facts) {
			return fmt.Errorf("last document routing %v, want %v", got, facts)
		}
		return nil
	})
	return n
}

// requireNoEmission checks, over a few seconds, that nothing more is
// emitted for name and the stored input hash does not change.
func requireNoEmission(t *testing.T, env *envTestEnv, ds *docSink, key types.NamespacedName, name string, n int, why string) {
	t.Helper()
	before, err := getAIBOM(env.k8sClient, context.Background(), key)
	if err != nil {
		t.Fatalf("%s: %v", why, err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if got := len(ds.emitted(name)); got != n {
			t.Fatalf("%s: document re-emitted (%d emits, want %d)", why, got, n)
		}
		a, err := getAIBOM(env.k8sClient, context.Background(), key)
		if err != nil {
			t.Fatalf("%s: %v", why, err)
		}
		if a.Status.InputHash != before.Status.InputHash {
			t.Fatalf("%s: input hash changed on availability alone", why)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func faultyPools(fault *poolFault) func(*InferencePoolView) {
	return func(v *InferencePoolView) {
		v.newClient = func(cfg *rest.Config) (dynamic.Interface, error) {
			dc, err := dynamic.NewForConfig(cfg)
			if err != nil {
				return nil, err
			}
			return faultyDynamic{dc, fault}, nil
		}
	}
}

func waitViewState(t *testing.T, env *envTestEnv, want PoolViewState) {
	t.Helper()
	eventually(t, 60*time.Second, 50*time.Millisecond, func() error {
		if st := env.pools.State(); st != want {
			return fmt.Errorf("view %s, want %s", st, want)
		}
		return nil
	})
}

// External documents (no inline copy in status): published membership
// survives a temporary list/watch failure and CRD removal, through
// workload changes, with no re-emission on availability changes alone;
// recovery applies changed and deleted membership and re-emits nothing
// when membership is unchanged.
func TestIntegration_InferencePool_ExternalDocumentPreserved(t *testing.T) {
	fault := &poolFault{}
	env := startEnvTestWith(t, envTestOptions{pools: faultyPools(fault)})
	ds := externalDocuments(env)
	ctx := context.Background()
	c := env.k8sClient
	ns := "pool-external"
	mustCreateOptedInNamespace(t, c, ctx, ns)
	serving := map[string]string{"llm-d.ai/inferenceServing": "true"}
	createPool(t, c, ctx, ns, "p", serving, "")
	mustCreate(t, c, ctx, poolDeployment(ns, "vllm", serving, nil, "vllm/vllm-openai:v0.6.3"))
	key := types.NamespacedName{Name: AIBOMNameForWorkload("apps", "Deployment", "vllm"), Namespace: ns}
	n := waitEmitted(t, ds, "vllm", []pm{{Pool: ns + "/p"}})
	a, err := getAIBOM(c, ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if ref := a.Status.BOMDocument; ref == nil || ref.Inline != nil || ref.External == nil {
		t.Fatalf("the document must be external, got %+v", ref)
	}

	// Temporary failure: nothing re-emitted, hash unchanged.
	fault.set(true)
	fault.breakWatches()
	waitViewState(t, env, PoolViewUnsynced)
	requireNoEmission(t, env, ds, key, "vllm", n, "temporary failure")
	// A workload change while unavailable keeps the published fact;
	// pool changes meanwhile are not applied.
	deletePool(t, c, ctx, ns, "p")
	createPool(t, c, ctx, ns, "q", serving, "q-epp")
	setDeploymentImage(t, c, ctx, ns, "vllm", "vllm/vllm-openai:v0.7.0", nil)
	waitEmitted(t, ds, "vllm", []pm{{Pool: ns + "/p"}}, "v0.7.0")
	// Recovery with changed membership.
	fault.set(false)
	n = waitEmitted(t, ds, "vllm", []pm{{Pool: ns + "/q", EndpointPicker: "q-epp"}}, "v0.7.0")
	waitViewState(t, env, PoolViewUsable)
	requireNoEmission(t, env, ds, key, "vllm", n, "settled after recovery")

	// Recovery with unchanged membership: nothing re-emitted.
	fault.set(true)
	fault.breakWatches()
	waitViewState(t, env, PoolViewUnsynced)
	fault.set(false)
	waitViewState(t, env, PoolViewUsable)
	requireNoEmission(t, env, ds, key, "vllm", n, "recovery without changes")

	// CRD removal: preserved, nothing re-emitted; a workload change
	// keeps the fact; re-creation (no pools) removes it.
	deleteCRD(t, c, InferencePoolCRD)
	waitViewState(t, env, PoolViewInactive)
	requireNoEmission(t, env, ds, key, "vllm", n, "CRD removal")
	setDeploymentImage(t, c, ctx, ns, "vllm", "vllm/vllm-openai:v0.8.0", nil)
	waitEmitted(t, ds, "vllm", []pm{{Pool: ns + "/q", EndpointPicker: "q-epp"}}, "v0.8.0")
	installExternalCRD(t, env, poolCRDFile)
	waitCRDServes(t, env, InferencePoolCRD, "v1")
	waitEmitted(t, ds, "vllm", nil, "v0.8.0")
	requireManagerRunning(t, env)
}

// A persistent permission-denied list: the view never recovers, yet
// workload changes still go through with the published facts kept, a
// new workload gets a document with no invented facts, and nothing is
// re-emitted on availability alone. Once permission is granted the
// view syncs and membership is applied.
func TestIntegration_InferencePool_PersistentPermissionDenied(t *testing.T) {
	fault := &poolFault{}
	env := startEnvTestWith(t, envTestOptions{pools: faultyPools(fault)})
	ds := externalDocuments(env)
	ctx := context.Background()
	c := env.k8sClient
	ns := "pool-forbidden"
	mustCreateOptedInNamespace(t, c, ctx, ns)
	serving := map[string]string{"llm-d.ai/inferenceServing": "true"}
	createPool(t, c, ctx, ns, "p", serving, "")
	mustCreate(t, c, ctx, poolDeployment(ns, "vllm", serving, nil, "vllm/vllm-openai:v0.6.3"))
	key := types.NamespacedName{Name: AIBOMNameForWorkload("apps", "Deployment", "vllm"), Namespace: ns}
	n := waitEmitted(t, ds, "vllm", []pm{{Pool: ns + "/p"}})

	fault.mu.Lock()
	fault.on, fault.forbidden = true, true
	fault.mu.Unlock()
	fault.breakWatches()
	waitViewState(t, env, PoolViewUnsynced)
	requireNoEmission(t, env, ds, key, "vllm", n, "permission denied")

	// Not blocked: the change is emitted promptly, fact kept.
	start := time.Now()
	setDeploymentImage(t, c, ctx, ns, "vllm", "vllm/vllm-openai:v0.7.0", nil)
	n = waitEmitted(t, ds, "vllm", []pm{{Pool: ns + "/p"}}, "v0.7.0")
	if d := time.Since(start); d > InferencePoolDeferRequeue {
		t.Errorf("a workload change took %s under a persistent 403; it must not wait for the view", d)
	}
	// A workload first seen while denied gets no invented fact.
	mustCreate(t, c, ctx, poolDeployment(ns, "vllm-new", serving, nil, "vllm/vllm-openai:v0.6.3"))
	waitEmitted(t, ds, "vllm-new", nil)
	if st := env.pools.State(); st != PoolViewUnsynced {
		t.Fatalf("view %s under a persistent 403", st)
	}

	// Permission granted: the view syncs; the new workload gains its
	// fact; the old one's membership is unchanged and not re-emitted.
	fault.set(false)
	waitEmitted(t, ds, "vllm-new", []pm{{Pool: ns + "/p"}})
	requireNoEmission(t, env, ds, key, "vllm", n, "recovery with unchanged membership")
	requireManagerRunning(t, env)
}
