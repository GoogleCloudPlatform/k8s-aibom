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

var fixedDSetTime = time.Date(2026, 10, 9, 16, 0, 0, 0, time.UTC)

var dsetKind = WorkloadKind{Group: "disaggregatedset.x-k8s.io", Version: "v1", Kind: "DisaggregatedSet"}

func newDSetScraper() *DisaggregatedSetScraper {
	s := NewDisaggregatedSetScraper(nil)
	s.now = func() time.Time { return fixedDSetTime }
	return s
}

// dsetRole builds one spec.roles[] entry in the real shape: the role
// inlines a LeaderWorkerSet template (metadata + spec), so the
// templates live under spec.leaderWorkerTemplate of the role.
func dsetRole(name string, leader, worker map[string]interface{}, size, replicas int64) map[string]interface{} {
	lwt := map[string]interface{}{}
	if worker != nil {
		lwt["workerTemplate"] = worker
	}
	if leader != nil {
		lwt["leaderTemplate"] = leader
	}
	if size > 0 {
		lwt["size"] = size
	}
	spec := map[string]interface{}{"leaderWorkerTemplate": lwt}
	if replicas > 0 {
		spec["replicas"] = replicas
	}
	return map[string]interface{}{"name": name, "spec": spec}
}

// dset returns an unstructured DisaggregatedSet with the given roles,
// shaped like llm-d's wide-EP guide (prefill + decode).
func dset(namespace, name string, roles ...map[string]interface{}) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("disaggregatedset.x-k8s.io/v1")
	u.SetKind("DisaggregatedSet")
	u.SetName(name)
	u.SetNamespace(namespace)
	rs := make([]interface{}, 0, len(roles))
	for _, r := range roles {
		rs = append(rs, r)
	}
	_ = unstructured.SetNestedSlice(u.Object, rs, "spec", "roles")
	return u
}

func dsetWorkload(u *unstructured.Unstructured) Workload {
	return Workload{Kind: dsetKind, Category: CategoryInference, Namespace: u.GetNamespace(), Name: u.GetName(), Object: u}
}

func TestDSetScraper_Name_HandlesKind(t *testing.T) {
	s := newDSetScraper()
	if s.Name() != "inference.disaggregatedset" {
		t.Errorf("Name = %q", s.Name())
	}
	if !s.HandlesKind(dsetKind) {
		t.Error("must handle disaggregatedset.x-k8s.io/v1 DisaggregatedSet")
	}
	for _, k := range []WorkloadKind{
		lwsKind,
		{Group: "disaggregatedset.x-k8s.io", Version: "v1", Kind: "DisaggregatedSetRoleScaler"},
		{Group: "disaggregatedset.x-k8s.io", Version: "v2", Kind: "DisaggregatedSet"},
	} {
		if s.HandlesKind(k) {
			t.Errorf("must not handle %v", k)
		}
	}
}

func TestDSetScraper_NilObject_NilCfg(t *testing.T) {
	s := newDSetScraper()
	if _, err := s.Scrape(context.Background(), Workload{Kind: dsetKind}, testConfig()); err == nil {
		t.Error("nil Object must error")
	}
	if _, err := s.Scrape(context.Background(), dsetWorkload(dset("ns", "x", dsetRole("decode", nil, podTemplate("a/b:1"), 0, 0))), nil); err == nil {
		t.Error("nil cfg must error")
	}
}

// Per-role, per-template extraction with locators rooted at the role's
// template; every component carries disaggregatedset.role and lws.role;
// containers carry the group shape and the set-wide slice count.
func TestDSetScraper_PerRoleExtraction(t *testing.T) {
	vllm := func(model string) map[string]interface{} {
		return podTemplate("vllm/vllm-openai:v0.11.0", "--model", model, "--tensor-parallel-size", "8")
	}
	cases := []struct {
		name  string
		roles []map[string]interface{}
		want  map[string][]string // role name → lws roles carrying the model claim
	}{
		{"worker-only prefill+decode (wide-EP shape)",
			[]map[string]interface{}{
				dsetRole("prefill", nil, vllm("deepseek-ai/DeepSeek-R1-0528"), 2, 1),
				dsetRole("decode", nil, vllm("deepseek-ai/DeepSeek-R1-0528"), 2, 1),
			},
			map[string][]string{"prefill": {"worker"}, "decode": {"worker"}}},
		{"leader-only role",
			[]map[string]interface{}{dsetRole("decode", vllm("org/m"), podTemplate("example.com/worker:1"), 4, 2)},
			map[string][]string{"decode": {"leader"}}},
		{"both templates in one role",
			[]map[string]interface{}{dsetRole("decode", vllm("org/m"), vllm("org/m"), 4, 2)},
			map[string][]string{"decode": {"leader", "worker"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := dset("ns", "wide-ep", tc.roles...)
			_ = unstructured.SetNestedField(u.Object, int64(3), "spec", "slices")
			got, err := newDSetScraper().Scrape(context.Background(), dsetWorkload(u), testConfig())
			if err != nil {
				t.Fatalf("Scrape: %v", err)
			}
			if len(got.Errors) != 0 {
				t.Errorf("unexpected errors: %v", got.Errors)
			}
			wantModels := 0
			for _, rs := range tc.want {
				wantModels += len(rs)
			}
			models := componentsOf(got.Components, ComponentMLModel)
			if len(models) != wantModels {
				t.Fatalf("models = %+v, want %d", models, wantModels)
			}
			seen := map[string]map[string]bool{}
			for _, m := range models {
				role, lwsRole := m.Properties["disaggregatedset.role"], m.Properties["lws.role"]
				if seen[role] == nil {
					seen[role] = map[string]bool{}
				}
				seen[role][lwsRole] = true
				// Locator is rooted at the role's template: find the
				// role index for the expected prefix.
				var idx int
				for i, r := range tc.roles {
					if r["name"] == role {
						idx = i
					}
				}
				wantLoc := "spec.roles[" + string(rune('0'+idx)) + "].spec.leaderWorkerTemplate." + lwsRole + "Template.spec.containers[0].args[0 1](--model)"
				if m.Evidence.Locator != wantLoc {
					t.Errorf("model locator = %q, want %q", m.Evidence.Locator, wantLoc)
				}
				if m.Confidence != ConfidenceDeclared {
					t.Errorf("model confidence = %s, want declared", m.Confidence)
				}
			}
			for role, lwsRoles := range tc.want {
				for _, lr := range lwsRoles {
					if !seen[role][lr] {
						t.Errorf("no model claim for role %s/%s; got %v", role, lr, seen)
					}
				}
			}
			for _, r := range componentsOf(got.Components, ComponentApplication) {
				if r.Name != "vllm" || r.Confidence != ConfidenceInferred {
					t.Errorf("runtime = %+v", r)
				}
				if r.Properties["disaggregatedset.role"] == "" || r.Properties["lws.role"] == "" {
					t.Errorf("runtime lacks role properties: %v", r.Properties)
				}
			}
			for _, c := range componentsOf(got.Components, ComponentContainer) {
				if c.Properties["disaggregatedset.slices"] != "3" {
					t.Errorf("container lacks disaggregatedset.slices: %v", c.Properties)
				}
				if c.Properties["lws.size"] == "" || c.Properties["lws.replicas"] == "" {
					t.Errorf("container lacks group shape: %v", c.Properties)
				}
				if c.Properties["disaggregatedset.role"] == "" || c.Properties["lws.role"] == "" {
					t.Errorf("container lacks role properties: %v", c.Properties)
				}
			}
			for _, c := range got.Components {
				if strings.HasPrefix(c.Evidence.Locator, "spec.template.") || strings.HasPrefix(c.Evidence.Locator, "spec.leaderWorkerTemplate.") {
					t.Errorf("foreign-shaped locator on a DisaggregatedSet component: %q", c.Evidence.Locator)
				}
			}
			if got.Confidence != ConfidenceInferred {
				t.Errorf("workload confidence = %s, want inferred", got.Confidence)
			}
		})
	}
}

// Group shape is per role: two roles with different sizes keep their
// own values; unset fields are omitted, never zeroed.
func TestDSetScraper_GroupShapePerRole_OmittedWhenUnset(t *testing.T) {
	u := dset("ns", "mixed",
		dsetRole("prefill", nil, podTemplate("vllm/vllm-openai:v0.11.0", "--model", "org/m"), 2, 1),
		dsetRole("decode", nil, podTemplate("vllm/vllm-openai:v0.11.0", "--model", "org/m"), 0, 0),
	)
	got, err := newDSetScraper().Scrape(context.Background(), dsetWorkload(u), testConfig())
	if err != nil {
		t.Fatalf("Scrape: %v", err)
	}
	for _, c := range componentsOf(got.Components, ComponentContainer) {
		switch c.Properties["disaggregatedset.role"] {
		case "prefill":
			if c.Properties["lws.size"] != "2" || c.Properties["lws.replicas"] != "1" {
				t.Errorf("prefill shape: %v", c.Properties)
			}
		case "decode":
			for _, k := range []string{"lws.size", "lws.replicas", "disaggregatedset.slices"} {
				if _, has := c.Properties[k]; has {
					t.Errorf("%s must be absent when unset on decode: %v", k, c.Properties)
				}
			}
		default:
			t.Errorf("container without a role: %v", c.Properties)
		}
	}
}

// Set-level annotations claim models with a set-rooted locator; a
// template's annotations keep the role-rooted locator.
func TestDSetScraper_Annotations(t *testing.T) {
	worker := podTemplate("example.com/custom:1")
	worker["metadata"] = map[string]interface{}{
		"annotations": map[string]interface{}{"model.k8saibom.dev/name": "org/template-annotated"},
	}
	u := dset("ns", "ann", dsetRole("decode", nil, worker, 0, 0))
	u.SetAnnotations(map[string]string{"model.k8saibom.dev/name": "org/set-annotated"})
	got, err := newDSetScraper().Scrape(context.Background(), dsetWorkload(u), testConfig())
	if err != nil {
		t.Fatalf("Scrape: %v", err)
	}
	models := componentsOf(got.Components, ComponentMLModel)
	if len(models) != 2 {
		t.Fatalf("models = %+v, want 2", models)
	}
	locs := map[string]string{}
	for _, m := range models {
		locs[m.Name] = m.Evidence.Locator
	}
	if !strings.HasPrefix(locs["org/set-annotated"], "metadata.annotations") {
		t.Errorf("set annotation locator = %q", locs["org/set-annotated"])
	}
	if !strings.HasPrefix(locs["org/template-annotated"], "spec.roles[0].spec.leaderWorkerTemplate.workerTemplate.metadata.annotations") {
		t.Errorf("template annotation locator = %q", locs["org/template-annotated"])
	}
}

// A malformed role, a nameless role and a malformed template are each
// recorded and skipped; the healthy role still extracts and Scrape
// succeeds (the Dynamo precedent).
func TestDSetScraper_MalformedRoles_Degrade(t *testing.T) {
	good := dsetRole("decode", nil, podTemplate("vllm/vllm-openai:v0.11.0", "--model", "org/m"), 2, 1)
	nameless := dsetRole("", nil, podTemplate("vllm/vllm-openai:v0.11.0", "--model", "org/n"), 0, 0)
	badTemplate := dsetRole("prefill", nil, nil, 0, 0)
	_ = unstructured.SetNestedField(badTemplate, "not-an-object", "spec", "leaderWorkerTemplate", "workerTemplate")
	u := dset("ns", "bad", good, nameless, badTemplate)
	roles, _, _ := unstructured.NestedSlice(u.Object, "spec", "roles")
	roles = append(roles, "not-a-role")
	_ = unstructured.SetNestedSlice(u.Object, roles, "spec", "roles")

	got, err := newDSetScraper().Scrape(context.Background(), dsetWorkload(u), testConfig())
	if err != nil {
		t.Fatalf("Scrape must succeed: %v", err)
	}
	var msgs []string
	for _, e := range got.Errors {
		msgs = append(msgs, e.Error())
	}
	joined := strings.Join(msgs, " | ")
	for _, want := range []string{"spec.roles[1].name: missing", "spec.roles[2].spec.leaderWorkerTemplate.workerTemplate: not an object", "spec.roles[3]: not an object"} {
		if !strings.Contains(joined, want) {
			t.Errorf("errors missing %q: %v", want, msgs)
		}
	}
	if len(got.Errors) != 3 {
		t.Errorf("errors = %v, want 3", msgs)
	}
	models := componentsOf(got.Components, ComponentMLModel)
	roles2 := map[string]bool{}
	for _, m := range models {
		roles2[m.Properties["disaggregatedset.role"]] = true
	}
	if !roles2["decode"] || !roles2["1"] || len(models) != 2 {
		t.Errorf("healthy role and index-named role must still extract: %+v", models)
	}
}

// No inference signal in any role → unresolved; containers are still
// listed for fleet visibility.
func TestDSetScraper_NoSignal_Unresolved(t *testing.T) {
	u := dset("ns", "plain",
		dsetRole("prefill", nil, podTemplate("example.com/a:1"), 0, 0),
		dsetRole("decode", nil, podTemplate("example.com/b:1"), 0, 0))
	got, err := newDSetScraper().Scrape(context.Background(), dsetWorkload(u), testConfig())
	if err != nil {
		t.Fatalf("Scrape: %v", err)
	}
	if got.Confidence != ConfidenceUnresolved {
		t.Errorf("confidence = %s, want unresolved", got.Confidence)
	}
	if n := len(componentsOf(got.Components, ComponentContainer)); n != 2 {
		t.Errorf("container components = %d, want 2", n)
	}
}

func TestDSetScraper_Deterministic(t *testing.T) {
	u := dset("ns", "det",
		dsetRole("prefill", podTemplate("vllm/vllm-openai:v0.11.0", "--model", "b/model"), podTemplate("vllm/vllm-openai:v0.11.0", "--model", "a/model"), 2, 1),
		dsetRole("decode", nil, podTemplate("vllm/vllm-openai:v0.11.0", "--model", "a/model"), 2, 1))
	s := newDSetScraper()
	a, err := s.Scrape(context.Background(), dsetWorkload(u), testConfig())
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Scrape(context.Background(), dsetWorkload(u.DeepCopy()), testConfig())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Errorf("two scrapes differ:\n%+v\n%+v", a, b)
	}
	if len(a.Provenance) != 1 || a.Provenance[0].ScraperName != "inference.disaggregatedset" {
		t.Errorf("provenance = %+v", a.Provenance)
	}
}

// The LWS scraper's own output is unchanged by sharing scrapeTemplate:
// lws.role only, no disaggregatedset.* properties, LWS-rooted locators.
func TestDSetScraper_LWSOutputUnchanged(t *testing.T) {
	u := lws("ns", "multi", nil, podTemplate("vllm/vllm-openai:v0.6.3", "--model", "org/m"))
	got, err := newLWSScraper().Scrape(context.Background(), lwsWorkload(u), testConfig())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range got.Components {
		for k := range c.Properties {
			if strings.HasPrefix(k, "disaggregatedset.") {
				t.Errorf("LWS component carries %s: %v", k, c.Properties)
			}
		}
		if c.Evidence.Locator != "" && !strings.HasPrefix(c.Evidence.Locator, "spec.leaderWorkerTemplate.") && !strings.HasPrefix(c.Evidence.Locator, "metadata.") {
			t.Errorf("LWS locator changed: %q", c.Evidence.Locator)
		}
	}
}
