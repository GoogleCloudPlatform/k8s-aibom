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
	"fmt"
	"strings"
	"testing"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	aibomv1beta1 "github.com/GoogleCloudPlatform/k8s-aibom/api/v1beta1"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/scraper"
)

// The scrapers' extraction errors are counted but not logged or kept
// anywhere else, so the Stale message is the only place an operator can
// see which field was skipped. It must name the errors, whatever their
// type: the Dynamo, NIMService, LeaderWorkerSet and DisaggregatedSet
// scrapers return plain errors, not *scraper.ExtractionError.
func TestRecordExtractionErrors_StaleMessageNamesErrors(t *testing.T) {
	staleStatus := func() *aibomv1beta1.AIBOMStatus {
		return &aibomv1beta1.AIBOMStatus{Conditions: []metav1.Condition{{
			Type:   aibomv1beta1.ConditionStale,
			Status: metav1.ConditionFalse,
			Reason: aibomv1beta1.ReasonBOMGenerated,
		}}}
	}

	cases := []struct {
		name    string
		errs    []error
		want    []string
		notWant []string
	}{
		{
			name: "plain errors from a CRD scraper",
			errs: []error{
				fmt.Errorf("spec.env[0]: not an object (string); skipped"),
				fmt.Errorf("spec.args[1]: not a string (int64); skipped"),
			},
			want: []string{
				"on 3 consecutive reconciles",
				"spec.env[0]: not an object (string); skipped",
				"spec.args[1]: not a string (int64); skipped",
			},
		},
		{
			name: "ExtractionError",
			errs: []error{&scraper.ExtractionError{EvidenceSource: scraper.SourceCRDField, Message: "spec.roles[0]: not an object; skipped"}},
			want: []string{"extraction from crd_field: spec.roles[0]: not an object; skipped"},
		},
		{
			name: "repeated errors are listed once",
			errs: []error{
				fmt.Errorf("spec.roles[0].name: missing; role identified by index"),
				fmt.Errorf("spec.roles[0].name: missing; role identified by index"),
			},
			want: []string{"errors: spec.roles[0].name: missing; role identified by index"},
		},
		{
			name: "long lists are cut",
			errs: []error{
				fmt.Errorf("e0"), fmt.Errorf("e1"), fmt.Errorf("e2"), fmt.Errorf("e3"), fmt.Errorf("e4"),
			},
			want:    []string{"e0", "e1", "e2", "and 2 more"},
			notWant: []string{"e3", "e4"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status := staleStatus()
			recordExtractionErrors(status, 2, tc.errs, 3)
			stale := apimeta.FindStatusCondition(status.Conditions, aibomv1beta1.ConditionStale)
			if stale.Status != metav1.ConditionTrue || stale.Reason != reasonExtractionErrorsPersistent {
				t.Fatalf("Stale = %s/%s, want True/%s", stale.Status, stale.Reason, reasonExtractionErrorsPersistent)
			}
			for _, w := range tc.want {
				if !strings.Contains(stale.Message, w) {
					t.Errorf("Stale message %q does not contain %q", stale.Message, w)
				}
			}
			for _, nw := range tc.notWant {
				if strings.Contains(stale.Message, nw) {
					t.Errorf("Stale message %q contains %q", stale.Message, nw)
				}
			}
			if n := strings.Count(stale.Message, "spec.roles[0].name"); n > 1 {
				t.Errorf("Stale message %q lists the same error %d times", stale.Message, n)
			}
		})
	}
}

// The message lands in status, so a spec that produces huge error texts
// must not bloat it: each listed error is cut, and the field locator at
// its start is kept.
func TestRecordExtractionErrors_StaleMessageBounded(t *testing.T) {
	status := &aibomv1beta1.AIBOMStatus{Conditions: []metav1.Condition{{
		Type:   aibomv1beta1.ConditionStale,
		Status: metav1.ConditionFalse,
		Reason: aibomv1beta1.ReasonBOMGenerated,
	}}}
	var errs []error
	for i := 0; i < 5; i++ {
		errs = append(errs, fmt.Errorf("spec.components[%d].podTemplate: not a PodTemplateSpec: %s; skipped", i, strings.Repeat("x", 10000)))
	}
	recordExtractionErrors(status, 2, errs, 3)
	stale := apimeta.FindStatusCondition(status.Conditions, aibomv1beta1.ConditionStale)
	if n := len([]rune(stale.Message)); n > 512 {
		t.Errorf("Stale message is %d characters, want at most 512", n)
	}
	for _, w := range []string{"spec.components[0].podTemplate", "spec.components[2].podTemplate", "and 2 more"} {
		if !strings.Contains(stale.Message, w) {
			t.Errorf("Stale message %q does not contain %q", stale.Message, w)
		}
	}
}
