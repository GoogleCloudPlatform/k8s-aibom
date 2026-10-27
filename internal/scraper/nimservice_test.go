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

var fixedNIMTime = time.Date(2026, 10, 1, 13, 0, 0, 0, time.UTC)

var nimKind = WorkloadKind{Group: "apps.nvidia.com", Version: "v1alpha1", Kind: "NIMService"}

func newNIMScraper() *NIMServiceScraper {
	s := NewNIMServiceScraper(nil)
	s.now = func() time.Time { return fixedNIMTime }
	return s
}

// nimService returns an unstructured NIMService with the given image
// repository and tag (both required by the real CRD).
func nimService(namespace, name, repo, tag string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("apps.nvidia.com/v1alpha1")
	u.SetKind("NIMService")
	u.SetName(name)
	u.SetNamespace(namespace)
	_ = unstructured.SetNestedField(u.Object, repo, "spec", "image", "repository")
	_ = unstructured.SetNestedField(u.Object, tag, "spec", "image", "tag")
	return u
}

func nimWorkload(u *unstructured.Unstructured) Workload {
	return Workload{Kind: nimKind, Category: CategoryInference, Namespace: u.GetNamespace(), Name: u.GetName(), Object: u}
}

func TestNIMServiceScraper_Name_HandlesKind(t *testing.T) {
	s := newNIMScraper()
	if s.Name() != "inference.nimservice" {
		t.Errorf("Name() = %q", s.Name())
	}
	cases := map[WorkloadKind]bool{
		nimKind: true,
		{Group: "apps.nvidia.com", Version: "v1alpha2", Kind: "NIMService"}: false,
		{Group: "apps.nvidia.com", Version: "v1alpha1", Kind: "NIMCache"}:   false,
		{Group: "apps", Version: "v1", Kind: "Deployment"}:                  false,
	}
	for k, want := range cases {
		if got := s.HandlesKind(k); got != want {
			t.Errorf("HandlesKind(%+v) = %v, want %v", k, got, want)
		}
	}
}

func TestNIMServiceScraper_NilObject_NilCfg(t *testing.T) {
	s := newNIMScraper()
	if _, err := s.Scrape(context.Background(), Workload{Kind: nimKind}, testConfig()); err == nil {
		t.Error("expected error for nil Object")
	}
	if _, err := s.Scrape(context.Background(), nimWorkload(nimService("ns", "n", "nvcr.io/nim/meta/llama-3.1-8b-instruct", "1.3.0")), nil); err == nil {
		t.Error("expected error for nil cfg")
	}
}

// The canonical NIM deployment: nvcr.io/nim image, nimCache storage,
// nothing declared. Runtime is declared from the kind; the model is
// derived from the image path (Inferred) and carries the storage facts.
func TestNIMServiceScraper_ImagePathModel_WhenNothingDeclared(t *testing.T) {
	u := nimService("ns", "llama", "nvcr.io/nim/meta/llama-3.1-8b-instruct", "1.3.0")
	_ = unstructured.SetNestedField(u.Object, "llama-cache", "spec", "storage", "nimCache", "name")
	_ = unstructured.SetNestedField(u.Object, "vllm-bf16-tp1", "spec", "storage", "nimCache", "profile")
	got, err := newNIMScraper().Scrape(context.Background(), nimWorkload(u), testConfig())
	if err != nil {
		t.Fatalf("Scrape: %v", err)
	}

	rt := findComponent(t, got.Components, func(c Component) bool { return c.Type == ComponentApplication })
	if rt.Name != "nim" || rt.Confidence != ConfidenceDeclared || rt.Evidence.Source != SourceCRDField || rt.Evidence.Locator != "kind" {
		t.Errorf("runtime = %+v, want nim/declared/crd_field@kind", rt)
	}
	if rt.Properties["runtime.name"] != "nim" || rt.Properties["nim.storage.kind"] != "nimCache" || rt.Properties["nim.storage.nimCache.name"] != "llama-cache" || rt.Properties["nim.storage.nimCache.profile"] != "vllm-bf16-tp1" {
		t.Errorf("runtime properties = %v", rt.Properties)
	}

	img := findComponent(t, got.Components, func(c Component) bool { return c.Type == ComponentContainer })
	if img.Name != "nvcr.io/nim/meta/llama-3.1-8b-instruct" || img.Version != "1.3.0" {
		t.Errorf("container = %q:%q", img.Name, img.Version)
	}
	if img.Confidence != ConfidenceUnresolved || len(img.Hashes) != 0 {
		t.Errorf("tag-only image must be unresolved with no digest: %+v", img)
	}
	if img.Properties["image.reference"] != "nvcr.io/nim/meta/llama-3.1-8b-instruct:1.3.0" || img.Evidence.Locator != "spec.image.repository" {
		t.Errorf("container evidence/properties = %+v %v", img.Evidence, img.Properties)
	}

	models := componentsOf(got.Components, ComponentMLModel)
	if len(models) != 1 {
		t.Fatalf("models = %+v, want exactly one from the image path", models)
	}
	m := models[0]
	if m.Name != "meta/llama-3.1-8b-instruct" || m.Confidence != ConfidenceInferred || m.Evidence.Source != SourceImageReference || m.Evidence.Locator != "spec.image.repository" {
		t.Errorf("model = %+v", m)
	}
	if m.Properties["identity.source"] != "nim.imagePath" || m.Properties["nim.storage.nimCache.name"] != "llama-cache" {
		t.Errorf("model properties = %v (storage facts must travel with the model)", m.Properties)
	}
	if got.Confidence != ConfidenceInferred {
		t.Errorf("workload confidence = %s, want inferred (image-derived model present)", got.Confidence)
	}
}

// Declared sources win: with NIM_MODEL_NAME set, the model is the env
// value and the image path is NOT used, even though it is a NIM image.
// Locators name spec.env, not a container path; no container.name
// property is fabricated.
func TestNIMServiceScraper_DeclaredEnvWins_NoImagePathModel(t *testing.T) {
	u := nimService("ns", "custom", "nvcr.io/nim/meta/llama-3.1-8b-instruct", "1.3.0")
	_ = unstructured.SetNestedSlice(u.Object, []interface{}{
		map[string]interface{}{"name": "NIM_LOG_LEVEL", "value": "INFO"},
		map[string]interface{}{"name": "NIM_MODEL_NAME", "value": "Qwen/Qwen3-0.6B"},
	}, "spec", "env")
	_ = unstructured.SetNestedMap(u.Object, map[string]interface{}{"sizeLimit": "16Gi"}, "spec", "storage", "emptyDir")
	got, err := newNIMScraper().Scrape(context.Background(), nimWorkload(u), testConfig())
	if err != nil {
		t.Fatalf("Scrape: %v", err)
	}
	models := componentsOf(got.Components, ComponentMLModel)
	if len(models) != 1 || models[0].Name != "Qwen/Qwen3-0.6B" {
		t.Fatalf("models = %+v, want only the declared env model", models)
	}
	m := models[0]
	if m.Evidence.Source != SourceEnvVar || m.Evidence.Locator != "spec.env[1](NIM_MODEL_NAME)" {
		t.Errorf("env model evidence = %+v", m.Evidence)
	}
	if _, has := m.Properties["container.name"]; has {
		t.Errorf("no container exists at spec.env; container.name must be absent: %v", m.Properties)
	}
	if m.Properties["nim.storage.kind"] != "emptyDir" {
		t.Errorf("storage shape not recorded on the declared model: %v", m.Properties)
	}
}

// Args go through the same allowlist and are Declared with spec.args
// locators in both forms.
func TestNIMServiceScraper_DeclaredArgs(t *testing.T) {
	u := nimService("ns", "args", "nvcr.io/nim/meta/llama-3.1-8b-instruct", "1.3.0")
	_ = unstructured.SetNestedStringSlice(u.Object, []string{"--port", "8000", "--model", "org/args-model"}, "spec", "args")
	got, err := newNIMScraper().Scrape(context.Background(), nimWorkload(u), testConfig())
	if err != nil {
		t.Fatalf("Scrape: %v", err)
	}
	models := componentsOf(got.Components, ComponentMLModel)
	if len(models) != 1 || models[0].Name != "org/args-model" || models[0].Confidence != ConfidenceDeclared {
		t.Fatalf("models = %+v", models)
	}
	if want := "spec.args[2 3](--model)"; models[0].Evidence.Locator != want {
		t.Errorf("arg locator = %q, want %q", models[0].Evidence.Locator, want)
	}
	if got.Confidence != ConfidenceDeclared {
		t.Errorf("workload confidence = %s, want declared", got.Confidence)
	}
}

// A NIMService pointing at a non-NIM registry (mirror, custom build):
// the kind still declares the runtime, but no model is invented.
func TestNIMServiceScraper_NonNIMRegistry_NoModel(t *testing.T) {
	cases := []string{
		"registry.example.com/mirror/llama-3.1-8b-instruct",
		"nvcr.io/nvidia/tritonserver",
		"nvcr.io/nim/meta",                       // too shallow
		"nvcr.io/nim/meta/llama/extra",           // too deep
		"NVCR.IO/nim/meta/llama-3.1-8b-instruct", // case-sensitive
	}
	for _, repo := range cases {
		t.Run(repo, func(t *testing.T) {
			got, err := newNIMScraper().Scrape(context.Background(), nimWorkload(nimService("ns", "x", repo, "1")), testConfig())
			if err != nil {
				t.Fatalf("Scrape: %v", err)
			}
			if models := componentsOf(got.Components, ComponentMLModel); len(models) != 0 {
				t.Errorf("models = %+v, want none", models)
			}
			rt := findComponent(t, got.Components, func(c Component) bool { return c.Type == ComponentApplication })
			if rt.Name != "nim" || rt.Confidence != ConfidenceDeclared {
				t.Errorf("runtime = %+v", rt)
			}
			if got.Confidence != ConfidenceDeclared {
				t.Errorf("workload confidence = %s, want declared (runtime declared, no model)", got.Confidence)
			}
		})
	}
}

// Digest-pinned repository resolves the container digest from the
// reference; the model derivation ignores the digest suffix.
func TestNIMServiceScraper_DigestPinnedRepository(t *testing.T) {
	digest := strings.Repeat("b", 64)
	u := nimService("ns", "pinned", "nvcr.io/nim/meta/llama-3.1-8b-instruct@sha256:"+digest, "1.3.0")
	got, err := newNIMScraper().Scrape(context.Background(), nimWorkload(u), testConfig())
	if err != nil {
		t.Fatalf("Scrape: %v", err)
	}
	img := findComponent(t, got.Components, func(c Component) bool { return c.Type == ComponentContainer })
	if img.Hashes["sha256"] != digest || img.Confidence != ConfidenceDeclared {
		t.Errorf("container = %+v, want digest from the reference", img)
	}
	models := componentsOf(got.Components, ComponentMLModel)
	if len(models) != 1 || models[0].Name != "meta/llama-3.1-8b-instruct" {
		t.Errorf("models = %+v", models)
	}
}

// Multi-node and storage shapes are recorded as facts on the runtime.
func TestNIMServiceScraper_MultiNodeAndStorageShapes(t *testing.T) {
	u := nimService("ns", "mn", "nvcr.io/nim/meta/llama-3.1-405b-instruct", "1.3.0")
	_ = unstructured.SetNestedMap(u.Object, map[string]interface{}{
		"backendType": "lws",
		"parallelism": map[string]interface{}{"tensor": int64(8), "pipeline": int64(2)},
	}, "spec", "multiNode")
	_ = unstructured.SetNestedField(u.Object, "weights", "spec", "storage", "pvc", "name")
	_ = unstructured.SetNestedField(u.Object, "/mnt/models", "spec", "storage", "hostPath")
	_ = unstructured.SetNestedField(u.Object, "standalone", "spec", "inferencePlatform")
	got, err := newNIMScraper().Scrape(context.Background(), nimWorkload(u), testConfig())
	if err != nil {
		t.Fatalf("Scrape: %v", err)
	}
	rt := findComponent(t, got.Components, func(c Component) bool { return c.Type == ComponentApplication })
	want := map[string]string{
		"nim.multiNode":                      "true",
		"nim.multiNode.backendType":          "lws",
		"nim.multiNode.parallelism.tensor":   "8",
		"nim.multiNode.parallelism.pipeline": "2",
		"nim.storage.kind":                   "hostPath,pvc",
		"nim.storage.pvc.name":               "weights",
		"nim.storage.hostPath":               "/mnt/models",
		"nim.inferencePlatform":              "standalone",
	}
	for k, v := range want {
		if rt.Properties[k] != v {
			t.Errorf("runtime property %s = %q, want %q", k, rt.Properties[k], v)
		}
	}
}

// A malformed env entry is recorded and skipped; the rest still scrapes.
func TestNIMServiceScraper_MalformedEnv_Degrades(t *testing.T) {
	u := nimService("ns", "bad", "nvcr.io/nim/meta/llama-3.1-8b-instruct", "1.3.0")
	_ = unstructured.SetNestedSlice(u.Object, []interface{}{
		"not-an-object",
		map[string]interface{}{"name": "NIM_MODEL_NAME", "value": "Qwen/Qwen3-0.6B"},
	}, "spec", "env")
	got, err := newNIMScraper().Scrape(context.Background(), nimWorkload(u), testConfig())
	if err != nil {
		t.Fatalf("Scrape must succeed: %v", err)
	}
	if len(got.Errors) != 1 || !strings.Contains(got.Errors[0].Error(), "spec.env[0]") {
		t.Errorf("errors = %v", got.Errors)
	}
	if models := componentsOf(got.Components, ComponentMLModel); len(models) != 1 || models[0].Name != "Qwen/Qwen3-0.6B" {
		t.Errorf("models = %+v", models)
	}
}

// A non-string arg is recorded and skipped like a malformed env entry;
// the other args still scrape, with their original indices.
func TestNIMServiceScraper_MalformedArgs_Degrades(t *testing.T) {
	u := nimService("ns", "bad-args", "nvcr.io/nim/meta/llama-3.1-8b-instruct", "1.3.0")
	_ = unstructured.SetNestedSlice(u.Object, []interface{}{
		int64(8), "--model", "org/args-model", "--max-batch", int64(8),
	}, "spec", "args")
	got, err := newNIMScraper().Scrape(context.Background(), nimWorkload(u), testConfig())
	if err != nil {
		t.Fatalf("Scrape must succeed: %v", err)
	}
	if len(got.Errors) != 2 ||
		!strings.Contains(got.Errors[0].Error(), "spec.args[0]: not a string (int64)") ||
		!strings.Contains(got.Errors[1].Error(), "spec.args[4]: not a string (int64)") {
		t.Errorf("errors = %v", got.Errors)
	}
	models := componentsOf(got.Components, ComponentMLModel)
	if len(models) != 1 || models[0].Name != "org/args-model" || models[0].Confidence != ConfidenceDeclared {
		t.Fatalf("models = %+v", models)
	}
	if want := "spec.args[1 2](--model)"; models[0].Evidence.Locator != want {
		t.Errorf("arg locator = %q, want %q", models[0].Evidence.Locator, want)
	}
}

// A non-string value after a model flag is not read as an empty model,
// and the next string is not taken as the value.
func TestNIMServiceScraper_NonStringArgValue_NoModelFromNeighbour(t *testing.T) {
	u := nimService("ns", "bad-value", "nvcr.io/nim/meta/llama-3.1-8b-instruct", "1.3.0")
	_ = unstructured.SetNestedSlice(u.Object, []interface{}{
		"--model", int64(8), "org/not-a-model",
	}, "spec", "args")
	got, err := newNIMScraper().Scrape(context.Background(), nimWorkload(u), testConfig())
	if err != nil {
		t.Fatalf("Scrape must succeed: %v", err)
	}
	if len(got.Errors) != 1 || !strings.Contains(got.Errors[0].Error(), "spec.args[1]") {
		t.Errorf("errors = %v", got.Errors)
	}
	for _, m := range componentsOf(got.Components, ComponentMLModel) {
		if m.Name == "org/not-a-model" || m.Name == "" {
			t.Errorf("unexpected model %+v", m)
		}
	}
}

// spec.args or spec.env that is not a list is recorded, not ignored.
func TestNIMServiceScraper_ArgsEnvNotList_Recorded(t *testing.T) {
	u := nimService("ns", "not-list", "nvcr.io/nim/meta/llama-3.1-8b-instruct", "1.3.0")
	if err := unstructured.SetNestedField(u.Object, "--model org/args-model", "spec", "args"); err != nil {
		t.Fatal(err)
	}
	if err := unstructured.SetNestedField(u.Object, "NIM_MODEL_NAME=x", "spec", "env"); err != nil {
		t.Fatal(err)
	}
	got, err := newNIMScraper().Scrape(context.Background(), nimWorkload(u), testConfig())
	if err != nil {
		t.Fatalf("Scrape must succeed: %v", err)
	}
	if len(got.Errors) != 2 ||
		!strings.HasPrefix(got.Errors[0].Error(), "spec.env: ") ||
		!strings.HasPrefix(got.Errors[1].Error(), "spec.args: ") {
		t.Errorf("errors = %v", got.Errors)
	}
}

func TestNIMServiceScraper_Deterministic(t *testing.T) {
	u := nimService("ns", "det", "nvcr.io/nim/meta/llama-3.1-8b-instruct", "1.3.0")
	_ = unstructured.SetNestedField(u.Object, "c", "spec", "storage", "nimCache", "name")
	_ = unstructured.SetNestedField(u.Object, "/p", "spec", "storage", "hostPath")
	s := newNIMScraper()
	a, err := s.Scrape(context.Background(), nimWorkload(u), testConfig())
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Scrape(context.Background(), nimWorkload(u.DeepCopy()), testConfig())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Errorf("two scrapes differ:\n%+v\n%+v", a, b)
	}
	if len(a.Provenance) != 1 || a.Provenance[0].ScraperName != "inference.nimservice" {
		t.Errorf("provenance = %+v", a.Provenance)
	}
}

func TestNIMModelFromImagePath(t *testing.T) {
	cases := map[string]string{
		"nvcr.io/nim/meta/llama-3.1-8b-instruct":                                   "meta/llama-3.1-8b-instruct",
		"nvcr.io/nim/nvidia/nv-embedqa-e5-v5":                                      "nvidia/nv-embedqa-e5-v5",
		"nvcr.io/nim/meta/llama-3.1-8b-instruct@sha256:" + strings.Repeat("a", 64): "meta/llama-3.1-8b-instruct",
		"nvcr.io/nim/meta":              "",
		"nvcr.io/nim/meta/a/b":          "",
		"nvcr.io/nim//x":                "",
		"nvcr.io/nvidia/ai-dynamo/vllm": "",
		"":                              "",
	}
	for in, want := range cases {
		if got := nimModelFromImagePath(in); got != want {
			t.Errorf("nimModelFromImagePath(%q) = %q, want %q", in, got, want)
		}
	}
}
