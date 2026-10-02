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
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

var fixedDynamoTime = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

var dynamoKind = WorkloadKind{Group: "nvidia.com", Version: "v1beta1", Kind: "DynamoGraphDeployment"}

func newDynamoScraper() *DynamoGraphDeploymentScraper {
	s := NewDynamoGraphDeploymentScraper(nil)
	s.now = func() time.Time { return fixedDynamoTime }
	return s
}

// minimalDGD returns an unstructured DynamoGraphDeployment with an empty
// spec. Tests add fields with the helpers below.
func minimalDGD(namespace, name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("nvidia.com/v1beta1")
	u.SetKind("DynamoGraphDeployment")
	u.SetName(name)
	u.SetNamespace(namespace)
	_ = unstructured.SetNestedMap(u.Object, map[string]interface{}{}, "spec")
	return u
}

func withBackend(u *unstructured.Unstructured, backend string) *unstructured.Unstructured {
	_ = unstructured.SetNestedField(u.Object, backend, "spec", "backendFramework")
	return u
}

func withComponents(u *unstructured.Unstructured, comps ...interface{}) *unstructured.Unstructured {
	_ = unstructured.SetNestedSlice(u.Object, comps, "spec", "components")
	return u
}

// dgdComponent builds a spec.components[] entry whose single container
// runs image with the given args. Extra fields are merged in last.
func dgdComponent(name, ctype, image string, args []string, extra map[string]interface{}) map[string]interface{} {
	container := map[string]interface{}{"name": "main", "image": image}
	if len(args) > 0 {
		a := make([]interface{}, 0, len(args))
		for _, s := range args {
			a = append(a, s)
		}
		container["args"] = a
	}
	c := map[string]interface{}{
		"name": name,
		"podTemplate": map[string]interface{}{
			"spec": map[string]interface{}{
				"containers": []interface{}{container},
			},
		},
	}
	if ctype != "" {
		c["type"] = ctype
	}
	for k, v := range extra {
		c[k] = v
	}
	return c
}

func dgdWorkload(u *unstructured.Unstructured) Workload {
	return Workload{
		Kind:      dynamoKind,
		Category:  CategoryInference,
		Namespace: u.GetNamespace(),
		Name:      u.GetName(),
		Object:    u,
	}
}

func componentsOf(cs []Component, typ ComponentType) []Component {
	var out []Component
	for _, c := range cs {
		if c.Type == typ {
			out = append(out, c)
		}
	}
	return out
}

func TestDynamoScraper_Name(t *testing.T) {
	if got := newDynamoScraper().Name(); got != "inference.dynamo" {
		t.Errorf("Name() = %q, want inference.dynamo", got)
	}
}

func TestDynamoScraper_HandlesKind(t *testing.T) {
	s := newDynamoScraper()
	cases := []struct {
		kind WorkloadKind
		want bool
	}{
		{dynamoKind, true},
		// v1alpha1 is served with conversion but the extraction map is
		// defined against v1beta1 only (Design 003 §4, open question 4).
		{WorkloadKind{Group: "nvidia.com", Version: "v1alpha1", Kind: "DynamoGraphDeployment"}, false},
		// Standalone DynamoComponentDeployment is its own root (Design 005 §4).
		{WorkloadKind{Group: "nvidia.com", Version: "v1beta1", Kind: "DynamoComponentDeployment"}, true},
		{WorkloadKind{Group: "apps", Version: "v1", Kind: "Deployment"}, false},
		{WorkloadKind{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.kind.Kind+"_"+tc.kind.Version, func(t *testing.T) {
			if got := s.HandlesKind(tc.kind); got != tc.want {
				t.Errorf("HandlesKind(%+v) = %v, want %v", tc.kind, got, tc.want)
			}
		})
	}
}

func TestDynamoScraper_NilObject_NilCfg_WrongType(t *testing.T) {
	s := newDynamoScraper()
	if _, err := s.Scrape(context.Background(), Workload{Kind: dynamoKind}, testConfig()); err == nil {
		t.Error("expected error for nil Object")
	}
	if _, err := s.Scrape(context.Background(), dgdWorkload(minimalDGD("ns", "dgd")), nil); err == nil {
		t.Error("expected error for nil cfg")
	}
}

// Declared backend: the API-validated enum becomes a Declared runtime
// component whose name matches what the pattern table emits for the
// same backend's runtime image, with graph topology as properties.
func TestDynamoScraper_BackendFramework_DeclaredRuntime(t *testing.T) {
	cases := []struct{ backend, wantRuntime string }{
		{"vllm", "vllm"},
		{"sglang", "sglang"},
		{"trtllm", "tensorrt-llm"},
		// Unknown (newer operator) value: recorded verbatim, never guessed.
		{"future-backend", "future-backend"},
	}
	for _, tc := range cases {
		t.Run(tc.backend, func(t *testing.T) {
			u := withComponents(withBackend(minimalDGD("ns", "dgd"), tc.backend),
				dgdComponent("Frontend", "frontend", "nvcr.io/nvidia/ai-dynamo/dynamo-frontend:0.6.0", nil, nil),
				dgdComponent("Worker", "worker", "example.com/custom/worker:1", nil, nil),
			)
			got, err := newDynamoScraper().Scrape(context.Background(), dgdWorkload(u), testConfig())
			if err != nil {
				t.Fatalf("Scrape: %v", err)
			}
			apps := componentsOf(got.Components, ComponentApplication)
			if len(apps) != 1 {
				t.Fatalf("application components = %d (%+v), want exactly 1 (no pattern matches these images)", len(apps), apps)
			}
			rt := apps[0]
			if rt.Name != tc.wantRuntime || rt.Confidence != ConfidenceDeclared {
				t.Errorf("runtime = %q/%s, want %q/declared", rt.Name, rt.Confidence, tc.wantRuntime)
			}
			if rt.Evidence.Source != SourceCRDField || rt.Evidence.Locator != "spec.backendFramework" {
				t.Errorf("runtime evidence = %+v, want crd_field @ spec.backendFramework", rt.Evidence)
			}
			if rt.Properties["runtime.name"] != tc.wantRuntime || rt.Properties["dynamo.backendFramework"] != tc.backend {
				t.Errorf("runtime properties = %v", rt.Properties)
			}
			if rt.Properties["dynamo.component.Frontend.type"] != "frontend" || rt.Properties["dynamo.component.Worker.type"] != "worker" {
				t.Errorf("topology properties missing: %v", rt.Properties)
			}
			if got.Confidence != ConfidenceDeclared {
				t.Errorf("workload confidence = %s, want declared", got.Confidence)
			}
		})
	}
}

// modelRef is attributed to the component that carries it: two workers
// with different modelRefs yield two Declared model components, each
// naming its component, with per-index locators.
func TestDynamoScraper_ModelRef_PerComponentAttribution(t *testing.T) {
	u := withComponents(withBackend(minimalDGD("ns", "disagg"), "vllm"),
		dgdComponent("Frontend", "frontend", "nvcr.io/nvidia/ai-dynamo/dynamo-frontend:0.6.0", nil, nil),
		dgdComponent("Prefill", "prefill", "nvcr.io/nvidia/ai-dynamo/vllm-runtime:0.6.0", nil,
			map[string]interface{}{"modelRef": map[string]interface{}{"name": "Qwen/Qwen3-0.6B", "revision": "abc123"}}),
		dgdComponent("Decode", "decode", "nvcr.io/nvidia/ai-dynamo/vllm-runtime:0.6.0", nil,
			map[string]interface{}{"modelRef": map[string]interface{}{"name": "meta-llama/Llama-3.1-8B-Instruct"}}),
	)
	got, err := newDynamoScraper().Scrape(context.Background(), dgdWorkload(u), testConfig())
	if err != nil {
		t.Fatalf("Scrape: %v", err)
	}
	models := componentsOf(got.Components, ComponentMLModel)
	if len(models) != 2 {
		t.Fatalf("model components = %d (%+v), want 2", len(models), models)
	}
	byName := map[string]Component{}
	for _, m := range models {
		byName[m.Name] = m
	}
	qwen, ok := byName["Qwen/Qwen3-0.6B"]
	if !ok {
		t.Fatalf("Qwen model missing: %+v", models)
	}
	if qwen.Confidence != ConfidenceDeclared || qwen.Evidence.Source != SourceCRDField {
		t.Errorf("qwen = %s/%s, want declared/crd_field", qwen.Confidence, qwen.Evidence.Source)
	}
	if qwen.Evidence.Locator != "spec.components[1].modelRef.name" {
		t.Errorf("qwen locator = %q", qwen.Evidence.Locator)
	}
	if qwen.Version != "abc123" || qwen.Properties["model.revision"] != "abc123" {
		t.Errorf("qwen revision not carried: version=%q props=%v", qwen.Version, qwen.Properties)
	}
	if qwen.Properties["dynamo.component.name"] != "Prefill" || qwen.Properties["dynamo.component.type"] != "prefill" {
		t.Errorf("qwen component attribution = %v", qwen.Properties)
	}
	llama := byName["meta-llama/Llama-3.1-8B-Instruct"]
	if llama.Evidence.Locator != "spec.components[2].modelRef.name" || llama.Properties["dynamo.component.name"] != "Decode" {
		t.Errorf("llama attribution = %q %v", llama.Evidence.Locator, llama.Properties)
	}
	if _, has := llama.Properties["model.revision"]; has {
		t.Errorf("llama has no revision; property must be absent, got %v", llama.Properties)
	}
}

// The binding caveat from the AICR review: a Dynamo runtime image with
// no modelRef and no declared env/args yields NO model component. The
// runtime is still attributed from the image (Inferred) — that is a
// runtime fact, not a model one — and the workload is Inferred overall.
func TestDynamoScraper_NoImagePathModelDerivation(t *testing.T) {
	u := withComponents(withBackend(minimalDGD("ns", "bare"), "vllm"),
		dgdComponent("Worker", "worker", "nvcr.io/nvidia/ai-dynamo/vllm-runtime:0.6.0", nil, nil),
	)
	got, err := newDynamoScraper().Scrape(context.Background(), dgdWorkload(u), testConfig())
	if err != nil {
		t.Fatalf("Scrape: %v", err)
	}
	if models := componentsOf(got.Components, ComponentMLModel); len(models) != 0 {
		t.Fatalf("model components = %+v, want none (Dynamo images carry no model identity)", models)
	}
	apps := componentsOf(got.Components, ComponentApplication)
	var declared, inferred int
	for _, a := range apps {
		switch a.Confidence {
		case ConfidenceDeclared:
			declared++
		case ConfidenceInferred:
			inferred++
		}
	}
	if declared != 1 || inferred != 1 {
		t.Errorf("runtime components: declared=%d inferred=%d (%+v), want 1 declared (backendFramework) + 1 inferred (image pattern)", declared, inferred, apps)
	}
	// Both attributions name the same runtime, so the summary shows one.
	for _, a := range apps {
		if a.Name != "vllm" {
			t.Errorf("runtime component named %q, want vllm", a.Name)
		}
	}
	if got.Confidence != ConfidenceInferred {
		t.Errorf("workload confidence = %s, want inferred (an inferred attribution is present)", got.Confidence)
	}
}

// Per-component pod templates go through the shared extraction with
// locators rooted at the component path; declared --model args and the
// template's container image are both read; roles get their own root.
func TestDynamoScraper_PodTemplateExtraction_Locators(t *testing.T) {
	u := withComponents(withBackend(minimalDGD("ns", "multi"), "sglang"),
		dgdComponent("Worker", "worker", "nvcr.io/nvidia/ai-dynamo/sglang-runtime@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			[]string{"--model-path", "Qwen/Qwen3-32B"},
			map[string]interface{}{
				"multinode": map[string]interface{}{"nodeCount": int64(2)},
				"roles": []interface{}{
					map[string]interface{}{
						"name": "leader",
						"podTemplate": map[string]interface{}{
							"spec": map[string]interface{}{
								"containers": []interface{}{
									map[string]interface{}{"name": "main", "image": "example.com/leader:1"},
								},
							},
						},
					},
				},
			}),
	)
	got, err := newDynamoScraper().Scrape(context.Background(), dgdWorkload(u), testConfig())
	if err != nil {
		t.Fatalf("Scrape: %v", err)
	}
	containers := componentsOf(got.Components, ComponentContainer)
	if len(containers) != 2 {
		t.Fatalf("container components = %d (%+v), want 2 (component template + role template)", len(containers), containers)
	}
	var sawComponent, sawRole bool
	for _, c := range containers {
		switch {
		case strings.HasPrefix(c.Evidence.Locator, "spec.components[0].podTemplate.spec.containers[0]"):
			sawComponent = true
			if c.Hashes["sha256"] != strings.Repeat("a", 64) {
				t.Errorf("digest-pinned image should resolve its digest from the reference: %+v", c)
			}
			if c.Properties["dynamo.component.name"] != "Worker" || c.Properties["dynamo.component.type"] != "worker" {
				t.Errorf("component container properties = %v", c.Properties)
			}
			if _, has := c.Properties["dynamo.role.name"]; has {
				t.Errorf("component-level container must not carry a role: %v", c.Properties)
			}
		case strings.HasPrefix(c.Evidence.Locator, "spec.components[0].roles[0].podTemplate.spec.containers[0]"):
			sawRole = true
			if c.Properties["dynamo.role.name"] != "leader" || c.Properties["dynamo.component.name"] != "Worker" {
				t.Errorf("role container properties = %v", c.Properties)
			}
		default:
			t.Errorf("unexpected container locator %q", c.Evidence.Locator)
		}
	}
	if !sawComponent || !sawRole {
		t.Errorf("component=%v role=%v, want both", sawComponent, sawRole)
	}
	models := componentsOf(got.Components, ComponentMLModel)
	if len(models) != 1 || models[0].Name != "Qwen/Qwen3-32B" {
		t.Fatalf("models = %+v, want the --model-path arg claim", models)
	}
	if want := "spec.components[0].podTemplate.spec.containers[0].args[0 1](--model-path)"; models[0].Evidence.Locator != want {
		t.Errorf("arg locator = %q, want %q", models[0].Evidence.Locator, want)
	}
	// No locator anywhere may claim the apps/v1 path: that would be a lie
	// about where the evidence lives in this CR.
	for _, c := range got.Components {
		if strings.HasPrefix(c.Evidence.Locator, "spec.template.") {
			t.Errorf("apps/v1-shaped locator on a Dynamo component: %q", c.Evidence.Locator)
		}
	}
}

// A component whose podTemplate does not decode is recorded as an error
// and skipped; the rest of the graph is still extracted and Scrape
// succeeds (degradation never fails another component or the reconcile).
func TestDynamoScraper_MalformedTemplate_DegradesPerComponent(t *testing.T) {
	bad := map[string]interface{}{
		"name":        "Broken",
		"type":        "worker",
		"podTemplate": "not-an-object",
	}
	u := withComponents(withBackend(minimalDGD("ns", "partial"), "vllm"),
		bad,
		dgdComponent("Worker", "worker", "nvcr.io/nvidia/ai-dynamo/vllm-runtime:0.6.0", nil,
			map[string]interface{}{"modelRef": map[string]interface{}{"name": "Qwen/Qwen3-0.6B"}}),
	)
	got, err := newDynamoScraper().Scrape(context.Background(), dgdWorkload(u), testConfig())
	if err != nil {
		t.Fatalf("Scrape must succeed with a malformed component: %v", err)
	}
	if len(got.Errors) != 1 || !strings.Contains(got.Errors[0].Error(), "spec.components[0].podTemplate") {
		t.Errorf("errors = %v, want exactly one naming spec.components[0].podTemplate", got.Errors)
	}
	if models := componentsOf(got.Components, ComponentMLModel); len(models) != 1 {
		t.Errorf("the healthy component must still be extracted: models = %+v", models)
	}
}

// Nothing declared, nothing inferable: unresolved, no components of
// interest — the reconciler declines to create an AIBOM.
func TestDynamoScraper_NoSignal_Unresolved(t *testing.T) {
	got, err := newDynamoScraper().Scrape(context.Background(), dgdWorkload(minimalDGD("ns", "empty")), testConfig())
	if err != nil {
		t.Fatalf("Scrape: %v", err)
	}
	if got.Confidence != ConfidenceUnresolved || len(got.Components) != 0 {
		t.Errorf("confidence=%s components=%+v, want unresolved and none", got.Confidence, got.Components)
	}
}

// Workload-level annotations are honored like every other kind.
func TestDynamoScraper_WorkloadAnnotations(t *testing.T) {
	u := withBackend(minimalDGD("ns", "annotated"), "vllm")
	u.SetAnnotations(map[string]string{"model.k8saibom.dev/name": "org/annotated-model"})
	got, err := newDynamoScraper().Scrape(context.Background(), dgdWorkload(u), testConfig())
	if err != nil {
		t.Fatalf("Scrape: %v", err)
	}
	models := componentsOf(got.Components, ComponentMLModel)
	if len(models) != 1 || models[0].Evidence.Source != SourceWorkloadAnnotation {
		t.Errorf("models = %+v, want one from metadata.annotations", models)
	}
}

func TestDynamoScraper_Deterministic(t *testing.T) {
	u := withComponents(withBackend(minimalDGD("ns", "det"), "vllm"),
		dgdComponent("Decode", "decode", "nvcr.io/nvidia/ai-dynamo/vllm-runtime:0.6.0", nil,
			map[string]interface{}{"modelRef": map[string]interface{}{"name": "b/model"}}),
		dgdComponent("Prefill", "prefill", "nvcr.io/nvidia/ai-dynamo/vllm-runtime:0.6.0", nil,
			map[string]interface{}{"modelRef": map[string]interface{}{"name": "a/model"}}),
		dgdComponent("Frontend", "frontend", "nvcr.io/nvidia/ai-dynamo/dynamo-frontend:0.6.0", nil, nil),
	)
	s := newDynamoScraper()
	a, err := s.Scrape(context.Background(), dgdWorkload(u), testConfig())
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Scrape(context.Background(), dgdWorkload(u.DeepCopy()), testConfig())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Errorf("two scrapes of identical input differ:\n%+v\n%+v", a, b)
	}
	if len(a.Provenance) != 1 || a.Provenance[0].ScraperName != "inference.dynamo" || a.Provenance[0].ScrapeMethod != "spec" {
		t.Errorf("provenance = %+v", a.Provenance)
	}
}

// A standalone DynamoComponentDeployment is read as a one-component
// graph: the same extraction, with locators rooted at spec rather than
// spec.components[i], and the component's own name/type on every fact.
func TestDynamoScraper_StandaloneComponentDeployment(t *testing.T) {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("nvidia.com/v1beta1")
	u.SetKind("DynamoComponentDeployment")
	u.SetName("solo")
	u.SetNamespace("ns")
	_ = unstructured.SetNestedMap(u.Object, map[string]interface{}{
		"backendFramework": "trtllm", "name": "Solo", "type": "decode",
		"modelRef":    map[string]interface{}{"name": "Qwen/Qwen3-0.6B", "revision": "r1"},
		"podTemplate": podTemplate("nvcr.io/nvidia/ai-dynamo/tensorrtllm-runtime:0.6.0"),
	}, "spec")
	w := Workload{Kind: WorkloadKind{Group: "nvidia.com", Version: "v1beta1", Kind: "DynamoComponentDeployment"}, Category: CategoryInference, Namespace: "ns", Name: "solo", Object: u}
	got, err := newDynamoScraper().Scrape(context.Background(), w, testConfig())
	if err != nil {
		t.Fatalf("Scrape: %v", err)
	}
	models := componentsOf(got.Components, ComponentMLModel)
	if len(models) != 1 || models[0].Evidence.Locator != "spec.modelRef.name" || models[0].Properties["dynamo.component.name"] != "Solo" || models[0].Properties["dynamo.component.type"] != "decode" {
		t.Errorf("models = %+v, want one at spec.modelRef.name attributed to Solo/decode", models)
	}
	containers := componentsOf(got.Components, ComponentContainer)
	if len(containers) != 1 || !strings.HasPrefix(containers[0].Evidence.Locator, "spec.podTemplate.spec.containers[0]") {
		t.Errorf("containers = %+v, want one rooted at spec.podTemplate", containers)
	}
	rt := findComponent(t, got.Components, func(c Component) bool { return c.Type == ComponentApplication && c.Confidence == ConfidenceDeclared })
	if rt.Name != "tensorrt-llm" || rt.Properties["dynamo.component.Solo.type"] != "decode" {
		t.Errorf("declared runtime = %+v", rt)
	}
	for _, c := range got.Components {
		if strings.Contains(c.Evidence.Locator, "spec.components[") {
			t.Errorf("standalone component must not claim a spec.components[] locator: %q", c.Evidence.Locator)
		}
	}
}
