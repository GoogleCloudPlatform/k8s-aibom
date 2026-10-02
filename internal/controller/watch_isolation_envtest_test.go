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
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlcfg "sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	aibomv1beta1 "github.com/GoogleCloudPlatform/k8s-aibom/api/v1beta1"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/bom"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/config"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/metrics"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/scraper"
)

// The Design 004 regression suite: the investigation scenarios from
// #127 (recorded in the Design 004 PR), now asserting the contract.
// Same CRD shape (v1alpha1 storage, v1beta1 served, conversion webhook
// pointed at a closed port when "down"); the Deployment reconciler runs
// on the manager cache and the Dynamo reconciler under the supervisor,
// exactly as cmd/manager wires them.

// dgdTwoVersionCRD mirrors the deployed Dynamo shape (#127): v1alpha1 is
// the storage version and v1beta1 is served. envtest strips conversion
// webhooks for kinds its scheme does not know, so the webhook is added
// to the live CRD by the test (see setDGDConversion).
const dgdTwoVersionCRD = `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: dynamographdeployments.nvidia.com
spec:
  group: nvidia.com
  scope: Namespaced
  names:
    plural: dynamographdeployments
    singular: dynamographdeployment
    kind: DynamoGraphDeployment
    listKind: DynamoGraphDeploymentList
  versions:
    - name: v1alpha1
      served: true
      storage: true
      schema:
        openAPIV3Schema:
          type: object
          x-kubernetes-preserve-unknown-fields: true
    - name: v1beta1
      served: true
      storage: false
      schema:
        openAPIV3Schema:
          type: object
          x-kubernetes-preserve-unknown-fields: true
`

// setDGDConversion points the live CRD's conversion at an unreachable
// webhook (down=true), which is what a stopped or uninstalled Dynamo
// operator looks like to the API server, or back to None (down=false).
func setDGDConversion(t *testing.T, c client.Client, ctx context.Context, down bool) {
	t.Helper()
	crd := &unstructured.Unstructured{}
	crd.SetAPIVersion("apiextensions.k8s.io/v1")
	crd.SetKind("CustomResourceDefinition")
	if err := c.Get(ctx, types.NamespacedName{Name: "dynamographdeployments.nvidia.com"}, crd); err != nil {
		t.Fatal(err)
	}
	conv := map[string]interface{}{"strategy": "None"}
	if down {
		conv = map[string]interface{}{
			"strategy": "Webhook",
			"webhook": map[string]interface{}{
				"conversionReviewVersions": []interface{}{"v1"},
				"clientConfig":             map[string]interface{}{"url": "https://127.0.0.1:1/convert"},
			},
		}
	}
	_ = unstructured.SetNestedMap(crd.Object, conv, "spec", "conversion")
	if err := c.Update(ctx, crd); err != nil {
		t.Fatalf("updating CRD conversion: %v", err)
	}
	time.Sleep(1 * time.Second)
}

func expVllmDeployment(ns, name string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{
					Name: "c", Image: "vllm/vllm-openai:v0.6.3", Args: []string{"--model", "facebook/opt-125m"},
				}}},
			},
		},
	}
}

func dgdAt(version, ns, name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("nvidia.com/" + version)
	u.SetKind("DynamoGraphDeployment")
	u.SetName(name)
	u.SetNamespace(ns)
	_ = unstructured.SetNestedField(u.Object, "vllm", "spec", "backendFramework")
	_ = unstructured.SetNestedSlice(u.Object, []interface{}{
		map[string]interface{}{"name": "W", "type": "worker",
			"modelRef": map[string]interface{}{"name": "Qwen/Qwen3-0.6B"}},
	}, "spec", "components")
	return u
}

func aibomExists(c client.Client, ctx context.Context, key types.NamespacedName) bool {
	var a aibomv1beta1.AIBOM
	return c.Get(ctx, key, &a) == nil && a.Status.Summary != nil
}

type isolationEnv struct {
	c        client.Client
	mgr      ctrl.Manager
	health   *WatchHealth
	recorder *record.FakeRecorder
	ctx      context.Context
}

func newIsolationEnv(t *testing.T) *isolationEnv {
	t.Helper()
	t.Setenv("AIBOM_DISABLE_SSRF_CHECKS", "true")
	crdDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(crdDir, "dgd.yaml"), []byte(dgdTwoVersionCRD), 0o600); err != nil {
		t.Fatal(err)
	}
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(aibomv1beta1.AddToScheme(scheme))
	te := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases"), crdDir},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := te.Start()
	if err != nil {
		t.Fatalf("envtest start: %v", err)
	}
	t.Cleanup(func() { _ = te.Stop() })
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:     scheme,
		Controller: ctrlcfg.Controller{SkipNameValidation: ptr.To(true)},
	})
	if err != nil {
		t.Fatal(err)
	}
	base := WorkloadReconciler{
		Client:            mgr.GetClient(),
		Scheme:            mgr.GetScheme(),
		Scraper:           scraper.NewInferenceSpecScraper(nil),
		BOMBuilder:        bom.NewBuilder(),
		StatusBuilder:     NewStatusBuilder(),
		ConfigStore:       config.NewStore(config.DefaultSnapshot()),
		ControllerName:    "k8s-aibom",
		ControllerVersion: "0.1.0-test",
	}
	dynamoBase := base
	dynamoBase.Scraper = scraper.NewDynamoGraphDeploymentScraper(nil)
	if err := (&DeploymentReconciler{WorkloadReconciler: base}).SetupWithManager(mgr); err != nil {
		t.Fatal(err)
	}
	health := NewWatchHealth()
	recorder := record.NewFakeRecorder(64)
	pod := &corev1.ObjectReference{Kind: "Pod", Name: "k8s-aibom-0", Namespace: "k8s-aibom-system"}
	if err := RegisterThirdPartyWatches(mgr, health, nil, recorder, pod, []ThirdPartyWatch{
		(&DynamoGraphDeploymentReconciler{WorkloadReconciler: dynamoBase}).Watch(),
	}, testWatchKnobs); err != nil {
		t.Fatal(err)
	}
	return &isolationEnv{c: c, mgr: mgr, health: health, recorder: recorder, ctx: context.Background()}
}

// startManager starts the manager and fails the test if it ever exits
// with an error while the test runs (the old failure mode).
func (e *isolationEnv) startManager(t *testing.T) {
	t.Helper()
	mgrCtx, cancel := context.WithCancel(e.ctx)
	errCh := make(chan error, 1)
	go func() { errCh <- e.mgr.Start(mgrCtx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-errCh; err != nil {
			t.Errorf("manager exited with error: %v", err)
		}
	})
	go func() {
		// Surface an early exit immediately rather than at cleanup.
		select {
		case err := <-errCh:
			errCh <- err
			if err != nil && mgrCtx.Err() == nil {
				t.Errorf("manager exited early: %v", err)
			}
		case <-mgrCtx.Done():
		}
	}()
}

func (e *isolationEnv) drainEvents() []string {
	var out []string
	for {
		select {
		case ev := <-e.recorder.Events:
			out = append(out, ev)
		default:
			return out
		}
	}
}

func dgdKey(ns, name string) types.NamespacedName {
	return types.NamespacedName{Name: AIBOMNameForWorkload("nvidia.com", "DynamoGraphDeployment", name), Namespace: ns}
}

func deployKey(ns, name string) types.NamespacedName {
	return types.NamespacedName{Name: AIBOMNameForWorkload("apps", "Deployment", name), Namespace: ns}
}

func waitDegraded(t *testing.T, h *WatchHealth, kind, wantSubstr string) {
	t.Helper()
	eventually(t, 30*time.Second, 200*time.Millisecond, func() error {
		st, ok := h.Status(kind)
		if !ok || !st.Degraded() {
			return fmt.Errorf("%s not degraded yet: %+v", kind, st)
		}
		if !strings.Contains(st.LastError, wantSubstr) {
			return fmt.Errorf("%s degraded but LastError %q lacks %q", kind, st.LastError, wantSubstr)
		}
		return nil
	})
}

func waitHealthy(t *testing.T, h *WatchHealth, kind string) {
	t.Helper()
	eventually(t, 30*time.Second, 200*time.Millisecond, func() error {
		if !h.IsHealthy(kind) {
			st, _ := h.Status(kind)
			return fmt.Errorf("%s not healthy yet: %+v", kind, st)
		}
		return nil
	})
}

// Webhook down at startup: the manager stays up, the unrelated
// Deployment is inventoried, the Dynamo kind is Degraded with the
// conversion error, one Warning event fires, the gauge reads 0. When
// the webhook comes back the kind recovers, the stored graph gets its
// AIBOM, and one Normal event fires.
func TestIntegration_WatchIsolation_WebhookDownAtStartup(t *testing.T) {
	e := newIsolationEnv(t)
	ns := "iso-down"
	mustCreateOptedInNamespace(t, e.c, e.ctx, ns)
	mustCreate(t, e.c, e.ctx, dgdAt("v1alpha1", ns, "stored-alpha"))
	mustCreate(t, e.c, e.ctx, expVllmDeployment(ns, "vllm"))
	setDGDConversion(t, e.c, e.ctx, true)

	e.startManager(t)

	// The apps path is unaffected.
	eventually(t, 30*time.Second, 200*time.Millisecond, func() error {
		if !aibomExists(e.c, e.ctx, deployKey(ns, "vllm")) {
			return errors.New("Deployment AIBOM not yet created")
		}
		return nil
	})
	// The Dynamo kind is degraded with the real cause.
	waitDegraded(t, e.health, "DynamoGraphDeployment", "conversion webhook")
	if aibomExists(e.c, e.ctx, dgdKey(ns, "stored-alpha")) {
		t.Fatal("no Dynamo AIBOM can exist while the kind cannot be listed")
	}
	if g := testutil.ToFloat64(metrics.WatchHealthy.WithLabelValues("DynamoGraphDeployment")); g != 0 {
		t.Errorf("aibom_watch_healthy{DynamoGraphDeployment} = %v, want 0", g)
	}
	warns := 0
	for _, ev := range e.drainEvents() {
		if strings.Contains(ev, EventReasonWatchUnhealthy) {
			warns++
			if !strings.Contains(ev, "conversion webhook") {
				t.Errorf("Warning event lacks the cause: %s", ev)
			}
		}
	}
	if warns != 1 {
		t.Errorf("WatchUnhealthy events = %d, want exactly 1 (one per outage, not per retry)", warns)
	}

	// Recovery.
	setDGDConversion(t, e.c, e.ctx, false)
	waitHealthy(t, e.health, "DynamoGraphDeployment")
	eventually(t, 30*time.Second, 200*time.Millisecond, func() error {
		if !aibomExists(e.c, e.ctx, dgdKey(ns, "stored-alpha")) {
			return errors.New("Dynamo AIBOM not yet created after recovery")
		}
		return nil
	})
	if g := testutil.ToFloat64(metrics.WatchHealthy.WithLabelValues("DynamoGraphDeployment")); g != 1 {
		t.Errorf("aibom_watch_healthy{DynamoGraphDeployment} = %v after recovery, want 1", g)
	}
	recovered := 0
	for _, ev := range e.drainEvents() {
		if strings.Contains(ev, EventReasonWatchRecovered) {
			recovered++
		}
	}
	if recovered != 1 {
		t.Errorf("WatchRecovered events = %d, want exactly 1", recovered)
	}
}

// Webhook dies after a healthy start: the kind is Degraded with the
// relist error, existing Dynamo AIBOMs survive, a new Deployment is
// still inventoried, and the graph created during the outage gets its
// AIBOM once the webhook returns.
func TestIntegration_WatchIsolation_WebhookDiesAfterStartup(t *testing.T) {
	e := newIsolationEnv(t)
	ns := "iso-late"
	mustCreateOptedInNamespace(t, e.c, e.ctx, ns)
	mustCreate(t, e.c, e.ctx, dgdAt("v1alpha1", ns, "early"))
	mustCreate(t, e.c, e.ctx, expVllmDeployment(ns, "vllm"))

	e.startManager(t)
	waitHealthy(t, e.health, "DynamoGraphDeployment")
	eventually(t, 30*time.Second, 200*time.Millisecond, func() error {
		if !aibomExists(e.c, e.ctx, dgdKey(ns, "early")) || !aibomExists(e.c, e.ctx, deployKey(ns, "vllm")) {
			return errors.New("healthy-phase AIBOMs not yet created")
		}
		return nil
	})
	e.drainEvents()

	// Outage. The informer only notices on its next relist/watch
	// failure; creating an object at the storage version forces a
	// watch event that needs conversion.
	setDGDConversion(t, e.c, e.ctx, true)
	mustCreate(t, e.c, e.ctx, dgdAt("v1alpha1", ns, "late"))
	mustCreate(t, e.c, e.ctx, expVllmDeployment(ns, "vllm2"))

	waitDegraded(t, e.health, "DynamoGraphDeployment", "conversion webhook")
	if !aibomExists(e.c, e.ctx, dgdKey(ns, "early")) {
		t.Fatal("existing Dynamo AIBOM must survive an outage")
	}
	eventually(t, 30*time.Second, 200*time.Millisecond, func() error {
		if !aibomExists(e.c, e.ctx, deployKey(ns, "vllm2")) {
			return errors.New("new Deployment AIBOM not yet created during the Dynamo outage")
		}
		return nil
	})
	if aibomExists(e.c, e.ctx, dgdKey(ns, "late")) {
		t.Fatal("a graph stored during the outage cannot have been inventoried yet")
	}

	// Recovery: the late graph is picked up.
	setDGDConversion(t, e.c, e.ctx, false)
	waitHealthy(t, e.health, "DynamoGraphDeployment")
	eventually(t, 60*time.Second, 250*time.Millisecond, func() error {
		if !aibomExists(e.c, e.ctx, dgdKey(ns, "late")) {
			return errors.New("late Dynamo AIBOM not yet created after recovery")
		}
		return nil
	})
}
