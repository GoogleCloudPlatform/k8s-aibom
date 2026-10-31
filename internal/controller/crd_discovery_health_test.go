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
	"strings"
	"testing"
)

// A6: one aggregate entry per discovery outage. A second failure
// changes the Degraded message, never emits another unhealthy event.
func TestCRDDiscovery_AggregateFailureAndRecovery(t *testing.T) {
	s := newApplyTestSupervisor(t)
	s.initDiscoveryHealth()
	var transitions []WatchTransition
	s.Health.Subscribe(func(tr WatchTransition) {
		if tr.Kind == CRDDiscoveryKind {
			transitions = append(transitions, tr)
		}
	})
	s.reportDiscoverySynced()
	s.reportDiscoveryInformer(nil)
	s.reportCandidateCRDRead("DynamoGraphDeployment", nil)
	s.applyDiscoveryHealth()
	if !s.Health.IsHealthy(CRDDiscoveryKind) {
		t.Fatal("successful informer and candidate read must report healthy")
	}

	s.reportCandidateCRDRead("DynamoGraphDeployment", errTest("403 forbidden"))
	s.applyDiscoveryHealth()
	st, _ := s.Health.Status(CRDDiscoveryKind)
	if !st.Degraded() || !strings.Contains(st.LastError, "403 forbidden") {
		t.Fatalf("failed candidate GET must Degrade discovery: %+v", st)
	}
	s.reportDiscoveryInformer(errTest("informer WATCH failed"))
	s.applyDiscoveryHealth()
	s.reportCandidateCRDRead("DynamoGraphDeployment", nil)
	s.applyDiscoveryHealth()
	st, _ = s.Health.Status(CRDDiscoveryKind)
	if !st.Degraded() || !strings.Contains(st.LastError, "informer WATCH failed") {
		t.Fatalf("informer failure must remain after GET recovery: %+v", st)
	}
	s.reportDiscoveryInformer(nil)
	s.applyDiscoveryHealth()
	if !s.Health.IsHealthy(CRDDiscoveryKind) {
		t.Fatal("aggregate not healthy after every failure cleared")
	}
	unhealthy, healthy := 0, 0
	for _, tr := range transitions {
		if tr.Healthy {
			healthy++
		} else {
			unhealthy++
		}
	}
	if unhealthy != 1 || healthy != 2 {
		t.Fatalf("expected initial healthy, one outage and one recovery, got %+v", transitions)
	}
}

func TestCRDDiscovery_InitialSyncAndIndependentCandidateReads(t *testing.T) {
	s := newApplyTestSupervisor(t)
	s.Watches = append(s.Watches, ThirdPartyWatch{Name: "InferenceService"})
	s.initDiscoveryHealth()
	s.reportCandidateCRDRead("DynamoGraphDeployment", nil)
	s.reportDiscoveryInformer(nil)
	s.reportDiscoverySynced()
	s.applyDiscoveryHealth()
	if s.Health.IsHealthy(CRDDiscoveryKind) {
		t.Fatal("no healthy transition until every candidate has been read")
	}
	s.reportCandidateCRDRead("InferenceService", errTest("timeout"))
	s.applyDiscoveryHealth()
	st, _ := s.Health.Status(CRDDiscoveryKind)
	if !st.Degraded() || !strings.Contains(st.LastError, "InferenceService") {
		t.Fatalf("independent candidate failure not reported: %+v", st)
	}
	s.reportCandidateCRDRead("InferenceService", nil)
	s.applyDiscoveryHealth()
	if !s.Health.IsHealthy(CRDDiscoveryKind) {
		t.Fatal("healthy after last outstanding failure clears")
	}
}

func TestCRDDiscovery_InformerRecoveryNeedsSync(t *testing.T) {
	s := newApplyTestSupervisor(t)
	s.initDiscoveryHealth()
	s.reportCandidateCRDRead("DynamoGraphDeployment", nil)
	s.reportDiscoveryInformer(errTest("LIST forbidden"))
	s.applyDiscoveryHealth()
	st, _ := s.Health.Status(CRDDiscoveryKind)
	if !st.Degraded() {
		t.Fatalf("informer failure must Degrade before initial sync: %+v", st)
	}
	s.reportDiscoveryInformer(nil)
	s.applyDiscoveryHealth()
	if s.Health.IsHealthy(CRDDiscoveryKind) {
		t.Fatal("a new watch alone cannot report healthy before initial sync")
	}
	s.reportDiscoverySynced()
	s.applyDiscoveryHealth()
	if !s.Health.IsHealthy(CRDDiscoveryKind) {
		t.Fatal("empty metadata informer recovers on successful watch and sync")
	}
}
