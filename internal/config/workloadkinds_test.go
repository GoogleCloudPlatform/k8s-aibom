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

package config

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	aibomv1beta1 "github.com/GoogleCloudPlatform/k8s-aibom/api/v1beta1"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/sink"
)

var (
	gkDeployment = schema.GroupKind{Group: "apps", Kind: "Deployment"}
	gkLWS        = schema.GroupKind{Group: "leaderworkerset.x-k8s.io", Kind: "LeaderWorkerSet"}
	gkJob        = schema.GroupKind{Group: "batch", Kind: "Job"}
)

func TestParseWorkloadKinds_EmptyMeansAll(t *testing.T) {
	for _, in := range [][]string{nil, {}} {
		set, errs := parseWorkloadKinds(in)
		if len(errs) != 0 {
			t.Fatalf("%v: unexpected errors %v", in, errs)
		}
		if !set.AllowsAll() || !set.Allows(gkDeployment) || !set.Allows(gkLWS) {
			t.Errorf("%v: want unrestricted set, got %s", in, set)
		}
		if set.Kinds() != nil || set.String() != "all" {
			t.Errorf("%v: unrestricted set must render as all/nil, got %q %v", in, set, set.Kinds())
		}
	}
	// Absent and empty are the same set: byte-identity of the documents
	// they produce follows from the same code path.
	a, _ := parseWorkloadKinds(nil)
	b, _ := parseWorkloadKinds([]string{})
	if !a.Equal(b) || !a.Equal(AllWorkloadKinds()) {
		t.Error("absent and empty lists must be equal to the unrestricted set")
	}
}

func TestParseWorkloadKinds_Known(t *testing.T) {
	set, errs := parseWorkloadKinds([]string{"leaderworkerset.x-k8s.io/LeaderWorkerSet", "apps/Deployment"})
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if set.AllowsAll() {
		t.Fatal("restricted set must not report AllowsAll")
	}
	if !set.Allows(gkDeployment) || !set.Allows(gkLWS) {
		t.Error("listed kinds must be allowed")
	}
	if set.Allows(gkJob) {
		t.Error("unlisted kind must not be allowed")
	}
	if got := set.String(); got != "apps/Deployment,leaderworkerset.x-k8s.io/LeaderWorkerSet" {
		t.Errorf("String() must be sorted Group/Kind, got %q", got)
	}
	if !set.Equal(NewWorkloadKindSet(gkLWS, gkDeployment)) {
		t.Error("Equal must ignore order")
	}
	if set.Equal(NewWorkloadKindSet(gkDeployment)) || set.Equal(AllWorkloadKinds()) {
		t.Error("Equal must distinguish different sets and the unrestricted set")
	}
}

func TestParseWorkloadKinds_EveryKnownKindParses(t *testing.T) {
	var entries []string
	for _, gk := range KnownWorkloadKinds {
		entries = append(entries, FormatWorkloadKind(gk))
	}
	set, errs := parseWorkloadKinds(entries)
	if len(errs) != 0 {
		t.Fatalf("the documented spelling of every known kind must parse: %v", errs)
	}
	if len(set.Kinds()) != len(KnownWorkloadKinds) {
		t.Errorf("got %d kinds, want %d", len(set.Kinds()), len(KnownWorkloadKinds))
	}
}

func TestParseWorkloadKinds_Errors(t *testing.T) {
	cases := []struct {
		name    string
		entries []string
		field   string
		wantMsg []string
	}{
		{"unknown kind", []string{"apps/Deployment", "apps/ReplicaSet"}, "spec.discovery.workloadKinds[1]",
			[]string{`"apps/ReplicaSet"`, "not a kind this controller inventories", "Known kinds: apps/Deployment"}},
		{"typo", []string{"apps/Deployments"}, "spec.discovery.workloadKinds[0]",
			[]string{`"apps/Deployments"`, "Known kinds"}},
		{"core spelling is unknown today", []string{"core/Pod"}, "spec.discovery.workloadKinds[0]",
			[]string{`"core/Pod"`, "not a kind this controller inventories"}},
		{"empty group spelling", []string{"/Pod"}, "spec.discovery.workloadKinds[0]",
			[]string{"malformed", "Group/Kind", "core/Kind"}},
		{"no slash", []string{"Deployment"}, "spec.discovery.workloadKinds[0]",
			[]string{"malformed", "Group/Kind"}},
		{"duplicate", []string{"apps/Deployment", "batch/Job", "apps/Deployment"}, "spec.discovery.workloadKinds[2]",
			[]string{"duplicates entry [0]"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			set, errs := parseWorkloadKinds(tc.entries)
			if len(errs) != 1 {
				t.Fatalf("want exactly one error, got %v", errs)
			}
			if errs[0].Field != tc.field {
				t.Errorf("Field = %q, want %q", errs[0].Field, tc.field)
			}
			for _, sub := range tc.wantMsg {
				if !strings.Contains(errs[0].Message, sub) {
					t.Errorf("message missing %q: %q", sub, errs[0].Message)
				}
			}
			if !set.AllowsAll() {
				t.Error("on error the returned set must be the safe unrestricted one")
			}
		})
	}
}

func TestParseWorkloadKinds_AllErrorsReported(t *testing.T) {
	_, errs := parseWorkloadKinds([]string{"nope", "apps/Nope", "apps/Deployment", "apps/Deployment"})
	if len(errs) != 3 {
		t.Fatalf("every failing entry must be reported in one pass; got %d: %v", len(errs), errs)
	}
}

func TestFormatWorkloadKind_CoreGroup(t *testing.T) {
	if got := FormatWorkloadKind(schema.GroupKind{Group: "", Kind: "Pod"}); got != "core/Pod" {
		t.Errorf("core group must render as core/Kind, got %q", got)
	}
	gk, err := ParseWorkloadKind("core/Pod")
	if err != nil || gk.Group != "" || gk.Kind != "Pod" {
		t.Errorf("core/Pod must parse to the empty group: %v %v", gk, err)
	}
}

func TestLoad_WorkloadKinds_UnknownEntryIsConfigError(t *testing.T) {
	cr := &aibomv1beta1.AIBOMControllerConfig{
		ObjectMeta: metav1.ObjectMeta{Name: DefaultConfigName, Generation: 3},
		Spec: aibomv1beta1.AIBOMControllerConfigSpec{
			Discovery: aibomv1beta1.DiscoveryConfig{
				WorkloadKinds: []string{"apps/Deployment", "nvidia.com/DynamoGraph"},
			},
		},
	}
	l := newLoader(t, &stubSinkFactory{sinks: []sink.Sink{}}, cr)
	result, err := l.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !result.HasErrors() {
		t.Fatal("an unknown workloadKinds entry must be a config error, never a silent no-op")
	}
	agg := result.AggregateMessage()
	for _, sub := range []string{"spec.discovery.workloadKinds[1]", `"nvidia.com/DynamoGraph"`, "Known kinds"} {
		if !strings.Contains(agg, sub) {
			t.Errorf("aggregate message missing %q: %q", sub, agg)
		}
	}
	if result.Snapshot.Source != SourceCompiledDefaults || !result.Snapshot.WorkloadKinds.AllowsAll() {
		t.Error("all-or-nothing: the returned snapshot must be the compiled defaults (all kinds)")
	}
}

func TestLoad_WorkloadKinds_ValidListOnSnapshot(t *testing.T) {
	cr := &aibomv1beta1.AIBOMControllerConfig{
		ObjectMeta: metav1.ObjectMeta{Name: DefaultConfigName, Generation: 4},
		Spec: aibomv1beta1.AIBOMControllerConfigSpec{
			Discovery: aibomv1beta1.DiscoveryConfig{
				WorkloadKinds: []string{"apps/StatefulSet", "leaderworkerset.x-k8s.io/LeaderWorkerSet"},
			},
		},
	}
	l := newLoader(t, &stubSinkFactory{sinks: []sink.Sink{}}, cr)
	result, err := l.Load(context.Background())
	if err != nil || result.HasErrors() {
		t.Fatalf("Load: %v / %s", err, result.AggregateMessage())
	}
	snap := result.Snapshot
	if snap.Source != SourceConfigCR {
		t.Errorf("Source = %q, want %q", snap.Source, SourceConfigCR)
	}
	if snap.WorkloadKinds.AllowsAll() || snap.WorkloadKinds.Allows(gkDeployment) || !snap.WorkloadKinds.Allows(gkLWS) {
		t.Errorf("WorkloadKinds = %s, want apps/StatefulSet + LeaderWorkerSet only", snap.WorkloadKinds)
	}
}

func TestDefaultSnapshot_WorkloadKindsAllowAll(t *testing.T) {
	if !DefaultSnapshot().WorkloadKinds.AllowsAll() {
		t.Fatal("the compiled default must allow every known kind")
	}
	var zero Snapshot
	if !zero.WorkloadKinds.Allows(gkDeployment) {
		t.Fatal("a zero WorkloadKindSet must allow (snapshots built without the field keep today's behavior)")
	}
}

func TestStore_SubscribeSeesPrevAndNext(t *testing.T) {
	first := DefaultSnapshot()
	s := NewStore(first)
	var gotPrev, gotNext *Snapshot
	calls := 0
	s.Subscribe(func(prev, next *Snapshot) { calls++; gotPrev, gotNext = prev, next })
	s.Subscribe(nil) // ignored

	second := DefaultSnapshot()
	second.WorkloadKinds = NewWorkloadKindSet(gkDeployment)
	s.Store(second)
	if calls != 1 || gotPrev != first || gotNext != second {
		t.Fatalf("subscriber must run once with (prev, next); calls=%d prev==first:%v next==second:%v", calls, gotPrev == first, gotNext == second)
	}
	s.Store(nil) // rejected: no swap, no notification
	if calls != 1 || s.Load() != second {
		t.Fatal("a nil Store must neither swap nor notify")
	}
}
