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

package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	aibomv1beta1 "github.com/GoogleCloudPlatform/k8s-aibom/api/v1beta1"
)

func summaryFixture(models ...aibomv1beta1.ModelSummary) *aibomv1beta1.AIBOMSummary {
	return &aibomv1beta1.AIBOMSummary{
		Workload: aibomv1beta1.WorkloadSummary{Kind: "Deployment", APIVersion: "apps/v1", Name: "chat", Namespace: "team-a", Category: "inference"},
		Runtime:  &aibomv1beta1.RuntimeSummary{Name: "vllm", Version: "v0.6.3", Confidence: "inferred"},
		Models:   models,
	}
}

func TestWorkloadInfo_SetUpdateForget(t *testing.T) {
	WorkloadInfo.Reset()
	PodInfo.Reset()
	key := "team-a/apps-deployment-chat"

	SetWorkloadInfo(key, summaryFixture(aibomv1beta1.ModelSummary{Identity: "Qwen/Qwen3-0.6B", Confidence: "declared", ServedName: "qwen"}), []string{"chat-1", "chat-2"})
	if got := testutil.CollectAndCount(WorkloadInfo); got != 1 {
		t.Fatalf("workload series = %d, want 1", got)
	}
	if got := testutil.CollectAndCount(PodInfo); got != 2 {
		t.Fatalf("pod series = %d, want 2", got)
	}
	v := testutil.ToFloat64(WorkloadInfo.WithLabelValues("team-a", "Deployment", "chat", "inference", "vllm", "v0.6.3", "Qwen/Qwen3-0.6B", "declared", "qwen"))
	if v != 1 {
		t.Fatalf("workload series value = %v, want 1", v)
	}

	// A pod leaves and another arrives: the old pod's series is gone.
	SetWorkloadInfo(key, summaryFixture(aibomv1beta1.ModelSummary{Identity: "Qwen/Qwen3-0.6B", Confidence: "declared", ServedName: "qwen"}), []string{"chat-2", "chat-3"})
	if got := testutil.CollectAndCount(PodInfo); got != 2 {
		t.Fatalf("pod series after churn = %d, want 2", got)
	}
	stale := prometheus.Labels{"namespace": "team-a", "pod": "chat-1", "workload_kind": "Deployment", "workload_name": "chat", "runtime": "vllm", "model": "Qwen/Qwen3-0.6B", "served_model": "qwen"}
	if PodInfo.Delete(stale) {
		t.Fatal("stale pod series for chat-1 was still present")
	}

	// Two models: one workload series each, pods duplicated per model.
	SetWorkloadInfo(key, summaryFixture(
		aibomv1beta1.ModelSummary{Identity: "a/one", Confidence: "declared"},
		aibomv1beta1.ModelSummary{Identity: "b/two", Confidence: "inferred"},
	), []string{"chat-3"})
	if got := testutil.CollectAndCount(WorkloadInfo); got != 2 {
		t.Fatalf("two-model workload series = %d, want 2", got)
	}
	if got := testutil.CollectAndCount(PodInfo); got != 2 {
		t.Fatalf("two-model pod series = %d, want 2", got)
	}

	ForgetWorkloadInfo(key)
	if got := testutil.CollectAndCount(WorkloadInfo) + testutil.CollectAndCount(PodInfo); got != 0 {
		t.Fatalf("series after forget = %d, want 0", got)
	}
	// Forgetting twice, or an unknown key, is a no-op.
	ForgetWorkloadInfo(key)
	ForgetWorkloadInfo("nowhere/none")
}

func TestWorkloadInfo_NoModelsAndNilSummary(t *testing.T) {
	WorkloadInfo.Reset()
	PodInfo.Reset()
	s := summaryFixture()
	s.Runtime = nil
	SetWorkloadInfo("ns/x", s, []string{"x-1"})
	if got := testutil.CollectAndCount(WorkloadInfo); got != 1 {
		t.Fatalf("no-model workload series = %d, want 1 (model=\"\")", got)
	}
	if v := testutil.ToFloat64(PodInfo.WithLabelValues("team-a", "x-1", "Deployment", "chat", "", "", "")); v != 1 {
		t.Fatalf("no-model pod series value = %v, want 1", v)
	}
	// A nil summary (status not yet built) publishes nothing and clears.
	SetWorkloadInfo("ns/x", nil, []string{"x-1"})
	if got := testutil.CollectAndCount(WorkloadInfo) + testutil.CollectAndCount(PodInfo); got != 0 {
		t.Fatalf("series after nil summary = %d, want 0", got)
	}
}
