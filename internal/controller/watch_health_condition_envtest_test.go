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
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	aibomv1beta1 "github.com/GoogleCloudPlatform/k8s-aibom/api/v1beta1"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/config"
)

// A third-party watch outage must surface on AIBOMControllerConfig as
// Degraded=True / ThirdPartyWatchUnhealthy with the kind and the
// verbatim error, without waiting for a spec change (the health
// channel triggers the reconcile), while Ready stays True and strict
// readiness is untouched. Recovery clears it (Design 004).
func TestReconcile_ThirdPartyWatchUnhealthy_IsDegradedNotUnready(t *testing.T) {
	health := NewWatchHealth()
	env, r, _ := startConfigEnvTestFull(t, []string{filepath.Join("..", "..", "config", "crd", "bases")}, health)
	ctx := context.Background()

	mustCreate(t, env.k8sClient, ctx, &aibomv1beta1.AIBOMControllerConfig{
		ObjectMeta: metav1.ObjectMeta{Name: config.DefaultConfigName},
	})
	key := types.NamespacedName{Name: config.DefaultConfigName}
	var got aibomv1beta1.AIBOMControllerConfig
	eventually(t, 30*time.Second, 250*time.Millisecond, func() error {
		if err := env.k8sClient.Get(ctx, key, &got); err != nil {
			return err
		}
		if d := meta.FindStatusCondition(got.Status.Conditions, aibomv1beta1.AIBOMControllerConfigConditionDegraded); d == nil || d.Status != metav1.ConditionFalse {
			return errors.New("Degraded not yet False on a clean start")
		}
		return nil
	})

	// Outage: no spec change, just the registry transition.
	health.Register("DynamoGraphDeployment")
	health.MarkUnhealthy("DynamoGraphDeployment", errors.New(`conversion webhook for nvidia.com/v1alpha1, Kind=DynamoGraphDeployment failed: Post "https://dynamo-operator.dynamo.svc:443/convert": connection refused`))

	eventually(t, 30*time.Second, 250*time.Millisecond, func() error {
		if err := env.k8sClient.Get(ctx, key, &got); err != nil {
			return err
		}
		d := meta.FindStatusCondition(got.Status.Conditions, aibomv1beta1.AIBOMControllerConfigConditionDegraded)
		if d == nil || d.Status != metav1.ConditionTrue {
			return errors.New("Degraded not yet True")
		}
		if d.Reason != aibomv1beta1.ReasonThirdPartyWatchUnhealthy {
			return errors.New("Degraded reason = " + d.Reason)
		}
		if !strings.Contains(d.Message, "DynamoGraphDeployment") || !strings.Contains(d.Message, "connection refused") {
			return errors.New("Degraded message lacks kind or cause: " + d.Message)
		}
		return nil
	})
	if ready := meta.FindStatusCondition(got.Status.Conditions, aibomv1beta1.AIBOMControllerConfigConditionReady); ready == nil || ready.Status != metav1.ConditionTrue {
		t.Fatalf("Ready must stay True during a third-party watch outage; got %+v", ready)
	}
	if r.ConfigStore.ConfigInvalid() {
		t.Fatal("a watch outage flipped ConfigInvalid; strict readiness would fail the pod for a per-kind degradation")
	}

	// Recovery.
	health.MarkHealthy("DynamoGraphDeployment")
	eventually(t, 30*time.Second, 250*time.Millisecond, func() error {
		if err := env.k8sClient.Get(ctx, key, &got); err != nil {
			return err
		}
		d := meta.FindStatusCondition(got.Status.Conditions, aibomv1beta1.AIBOMControllerConfigConditionDegraded)
		if d == nil || d.Status != metav1.ConditionFalse {
			return errors.New("Degraded not yet cleared after recovery")
		}
		return nil
	})
}
