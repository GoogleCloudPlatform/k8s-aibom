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
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// Design 011 §3: --served-model-name is the alias vLLM's metrics carry.
func TestServedModelName(t *testing.T) {
	env := []corev1.EnvVar{{Name: "ALIAS", Value: "qwen-small"}}
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"absent", []string{"--model", "Qwen/Qwen3-0.6B"}, ""},
		{"positional", []string{"--model", "Qwen/Qwen3-0.6B", "--served-model-name", "qwen"}, "qwen"},
		{"joined", []string{"--model=Qwen/Qwen3-0.6B", "--served-model-name=qwen"}, "qwen"},
		{"several names, first wins", []string{"--served-model-name", "qwen", "qwen-alias", "--model", "x"}, "qwen"},
		{"joined with several names, first wins", []string{"--served-model-name=qwen qwen-alias"}, "qwen"},
		{"flag without value", []string{"--served-model-name"}, ""},
		{"flag followed by another flag", []string{"--served-model-name", "--model", "x"}, ""},
		{"empty joined value", []string{"--served-model-name="}, ""},
		{"env reference expanded", []string{"--served-model-name", "$(ALIAS)"}, "qwen-small"},
		{"unresolved env reference names nothing", []string{"--served-model-name", "$(MISSING)"}, ""},
		{"escaped reference is literal", []string{"--served-model-name", "$$(ALIAS)"}, "$(ALIAS)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := servedModelName(tc.args, env); got != tc.want {
				t.Fatalf("servedModelName(%v) = %q, want %q", tc.args, got, tc.want)
			}
		})
	}
}

func TestApplyServedModelName(t *testing.T) {
	comps := []Component{
		{Type: ComponentMLModel, Name: "Qwen/Qwen3-0.6B", Properties: map[string]string{"identity.argFlag": "--model"}},
		{Type: ComponentMLModel, Name: "from-env"}, // nil properties
		{Type: ComponentApplication, Name: "vllm"},
	}
	applyServedModelName(comps, "")
	for _, c := range comps {
		if _, ok := c.Properties["identity.servedModelName"]; ok {
			t.Fatalf("empty alias must not write a property (component %s)", c.Name)
		}
	}
	applyServedModelName(comps, "qwen")
	if comps[0].Properties["identity.servedModelName"] != "qwen" || comps[0].Properties["identity.argFlag"] != "--model" {
		t.Fatalf("arg model: properties = %v", comps[0].Properties)
	}
	if comps[1].Properties["identity.servedModelName"] != "qwen" {
		t.Fatalf("env model with nil properties: %v", comps[1].Properties)
	}
	if _, ok := comps[2].Properties["identity.servedModelName"]; ok {
		t.Fatal("non-model component must not carry the alias")
	}
}

// Through the scraper: env- and arg-sourced identities from the same
// container share the alias; a second container without the flag does
// not, and the document gains exactly one property per model.
func TestInferenceSpecScraper_ServedModelNameOnContainerModels(t *testing.T) {
	s := NewInferenceSpecScraper(nil)
	cfg := DefaultV1Config()
	spec := &corev1.PodSpec{Containers: []corev1.Container{
		{
			Name:  "vllm",
			Image: "vllm/vllm-openai:v0.6.3",
			Args:  []string{"--model", "Qwen/Qwen3-0.6B", "--served-model-name", "qwen"},
			Env:   []corev1.EnvVar{{Name: "HF_MODEL_ID", Value: "Qwen/Qwen3-0.6B"}},
		},
		{
			Name:  "sidecar",
			Image: "ghcr.io/ggml-org/llama.cpp:server",
			Env:   []corev1.EnvVar{{Name: "HF_MODEL_ID", Value: "ggml-org/gemma-3-1b-it-GGUF"}},
		},
	}}
	inputs := &BOMInputs{}
	s.scrapePodSpecAt(inputs, spec, nil, nil, cfg, defaultPodSpecLocator, defaultPodTemplateAnnotationsLocator)
	var withAlias, without int
	for _, c := range inputs.Components {
		if c.Type != ComponentMLModel {
			continue
		}
		switch c.Properties["container.name"] {
		case "vllm":
			if c.Properties["identity.servedModelName"] != "qwen" {
				t.Errorf("model %s in vllm container: alias = %q, want qwen", c.Name, c.Properties["identity.servedModelName"])
			}
			withAlias++
		case "sidecar":
			if _, ok := c.Properties["identity.servedModelName"]; ok {
				t.Errorf("model %s in sidecar must not inherit the alias", c.Name)
			}
			without++
		}
	}
	if withAlias != 2 || without != 1 {
		t.Fatalf("models with alias = %d (want 2: env and arg), without = %d (want 1)", withAlias, without)
	}
}
