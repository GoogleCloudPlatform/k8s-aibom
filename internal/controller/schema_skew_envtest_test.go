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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/yaml"

	aibomv1beta1 "github.com/GoogleCloudPlatform/k8s-aibom/api/v1beta1"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/config"
)

// olderCRDDir writes a copy of the CRDs with spec.verification removed
// from every served version of AIBOMControllerConfig — the schema a
// v1.3.0 chart installs. Built by structural YAML edit, not text
// surgery, so it tracks the real CRD.
func olderCRDDir(t *testing.T) string {
	t.Helper()
	return crdDirWithout(t, func(spec map[string]interface{}) int {
		if _, ok := spec["verification"]; ok {
			delete(spec, "verification")
			return 1
		}
		return 0
	})
}

// crdDirWithout writes a copy of the CRDs with edit applied to the
// served v1beta1/v1alpha1 spec properties of AIBOMControllerConfig; edit
// returns how many fields it removed so a stale premise fails loudly.
func crdDirWithout(t *testing.T, edit func(spec map[string]interface{}) int) string {
	t.Helper()
	src := filepath.Join("..", "..", "config", "crd", "bases")
	dst := t.TempDir()

	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatalf("read crd dir: %v", err)
	}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		if e.Name() == "aibom.k8saibom.dev_aibomcontrollerconfigs.yaml" {
			var crd map[string]interface{}
			if err := yaml.Unmarshal(b, &crd); err != nil {
				t.Fatalf("unmarshal crd: %v", err)
			}
			versions := crd["spec"].(map[string]interface{})["versions"].([]interface{})
			removed := 0
			for _, v := range versions {
				spec := v.(map[string]interface{})["schema"].(map[string]interface{})["openAPIV3Schema"].(map[string]interface{})["properties"].(map[string]interface{})["spec"].(map[string]interface{})["properties"].(map[string]interface{})
				removed += edit(spec)
			}
			if removed == 0 {
				t.Fatal("fixture build: the edit removed nothing; test premise broken")
			}
			if b, err = yaml.Marshal(crd); err != nil {
				t.Fatalf("marshal crd: %v", err)
			}
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), b, 0o600); err != nil {
			t.Fatalf("write %s: %v", e.Name(), err)
		}
	}
	return dst
}

// TestReconcile_SchemaPredatesController_IsNotGreen is the #104
// regression: an older served schema must surface as Degraded=True
// with reason SchemaPredatesController plus a Warning Event — never as
// a fully green status with verification silently off.
func TestReconcile_SchemaPredatesController_IsNotGreen(t *testing.T) {
	env, r, rec := startConfigEnvTestWithCRDs(t, []string{olderCRDDir(t)})
	ctx := context.Background()

	// OpenAPI v3 publication for a freshly installed CRD is
	// asynchronous; wait until the served schema is readable before
	// creating the CR, so the first reconcile sees the real answer.
	eventually(t, 30*time.Second, 250*time.Millisecond, func() error {
		missing, err := r.SchemaChecker.MissingSpecFields(ctx)
		if err != nil {
			return err
		}
		if len(missing) == 0 {
			return errors.New("older schema not yet served")
		}
		return nil
	})

	cr := &aibomv1beta1.AIBOMControllerConfig{
		ObjectMeta: metav1.ObjectMeta{Name: config.DefaultConfigName},
		Spec:       aibomv1beta1.AIBOMControllerConfigSpec{},
	}
	mustCreate(t, env.k8sClient, ctx, cr)

	var got aibomv1beta1.AIBOMControllerConfig
	eventually(t, 30*time.Second, 250*time.Millisecond, func() error {
		if err := env.k8sClient.Get(ctx, types.NamespacedName{Name: config.DefaultConfigName}, &got); err != nil {
			return err
		}
		d := meta.FindStatusCondition(got.Status.Conditions, aibomv1beta1.AIBOMControllerConfigConditionDegraded)
		if d == nil || d.Status != metav1.ConditionTrue {
			return errors.New("Degraded not yet True")
		}
		if d.Reason != aibomv1beta1.ReasonSchemaPredatesController {
			return errors.New("Degraded reason = " + d.Reason)
		}
		return nil
	})

	// The spec itself parsed: Ready stays True. Green-but-Degraded is
	// the intended shape.
	ready := meta.FindStatusCondition(got.Status.Conditions, aibomv1beta1.AIBOMControllerConfigConditionReady)
	if ready == nil || ready.Status != metav1.ConditionTrue {
		t.Fatalf("Ready should remain True when the spec loaded; got %+v", ready)
	}

	// Strict-readiness contract (--strict-config-readiness reads
	// ConfigStore.ConfigInvalid, which is set only from the state
	// machine's stateInvalid classification): schema skew is a
	// Degraded condition, NOT a config-invalid state. A distribution
	// running strict readiness (AICR does) must see the skew as a
	// signal, never as a readiness failure that blocks its health
	// check. Regression for the question raised on NVIDIA/aicr#2962.
	if r.ConfigStore.ConfigInvalid() {
		t.Fatalf("schema skew flipped ConfigInvalid=true; strict readiness would fail the pod for a condition that is informational by design")
	}

	// Warning Event, once, naming the reason.
	rec.mu.Lock()
	defer rec.mu.Unlock()
	warnings := 0
	for _, e := range rec.events {
		if e.Reason == EventReasonSchemaPredatesController {
			if e.EventType != corev1.EventTypeWarning {
				t.Errorf("schema event type = %s, want Warning", e.EventType)
			}
			warnings++
		}
	}
	if warnings != 1 {
		t.Fatalf("SchemaPredatesController events = %d, want exactly 1 (anti-spam)", warnings)
	}
}

// TestReconcile_SchemaPredatesController_NestedField is the 1.5.1 → 1.6.x
// shape found by NVIDIA AICR: the served CRD declares every top-level
// spec field but lacks discovery.workloadKinds. The controller must name
// the nested path, not report a clean schema.
func TestReconcile_SchemaPredatesController_NestedField(t *testing.T) {
	dir := crdDirWithout(t, func(spec map[string]interface{}) int {
		disc, ok := spec["discovery"].(map[string]interface{})
		if !ok {
			return 0
		}
		props, ok := disc["properties"].(map[string]interface{})
		if !ok {
			return 0
		}
		if _, ok := props["workloadKinds"]; ok {
			delete(props, "workloadKinds")
			return 1
		}
		return 0
	})
	env, r, _ := startConfigEnvTestWithCRDs(t, []string{dir})
	ctx := context.Background()

	var missing []string
	eventually(t, 30*time.Second, 250*time.Millisecond, func() error {
		m, err := r.SchemaChecker.MissingSpecFields(ctx)
		if err != nil {
			return err
		}
		if len(m) == 0 {
			return errors.New("older schema not yet served")
		}
		missing = m
		return nil
	})
	if len(missing) != 1 || missing[0] != "discovery.workloadKinds" {
		t.Fatalf("missing = %v, want exactly [discovery.workloadKinds] against a 1.5.1-shaped CRD", missing)
	}

	mustCreate(t, env.k8sClient, ctx, &aibomv1beta1.AIBOMControllerConfig{
		ObjectMeta: metav1.ObjectMeta{Name: config.DefaultConfigName},
	})
	var got aibomv1beta1.AIBOMControllerConfig
	eventually(t, 30*time.Second, 250*time.Millisecond, func() error {
		if err := env.k8sClient.Get(ctx, types.NamespacedName{Name: config.DefaultConfigName}, &got); err != nil {
			return err
		}
		d := meta.FindStatusCondition(got.Status.Conditions, aibomv1beta1.AIBOMControllerConfigConditionDegraded)
		if d == nil || d.Status != metav1.ConditionTrue || d.Reason != aibomv1beta1.ReasonSchemaPredatesController {
			return errors.New("Degraded/SchemaPredatesController not yet set")
		}
		if !strings.Contains(d.Message, "discovery.workloadKinds") {
			return errors.New("message does not name the nested path: " + d.Message)
		}
		return nil
	})
}
