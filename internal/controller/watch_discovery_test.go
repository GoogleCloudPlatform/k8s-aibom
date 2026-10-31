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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"go.uber.org/goleak"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	toolscache "k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	"github.com/GoogleCloudPlatform/k8s-aibom/internal/config"
)

var testDGDGVK = schema.GroupVersionKind{Group: "nvidia.com", Version: "v1beta1", Kind: "DynamoGraphDeployment"}

func readyCRDObject() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "apiextensions.k8s.io/v1",
		"kind":       "CustomResourceDefinition",
		"metadata":   map[string]interface{}{"name": "dynamographdeployments.nvidia.com", "uid": "u1"},
		"spec": map[string]interface{}{
			"group": "nvidia.com",
			"scope": "Namespaced",
			"names": map[string]interface{}{"kind": "DynamoGraphDeployment", "plural": "dynamographdeployments"},
			"versions": []interface{}{
				map[string]interface{}{"name": "v1alpha1", "served": true, "storage": true},
				map[string]interface{}{"name": "v1beta1", "served": true, "storage": false},
			},
		},
		"status": map[string]interface{}{"conditions": []interface{}{
			map[string]interface{}{"type": "NamesAccepted", "status": "True"},
			map[string]interface{}{"type": "Established", "status": "True"},
		}},
	}}
}

// A1 Ready conditions, each on its own.
func TestCRDReady_Classification(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(u *unstructured.Unstructured)
		ready  bool
		reason string
	}{
		{"ready", func(*unstructured.Unstructured) {}, true, ""},
		{"not established", func(u *unstructured.Unstructured) {
			_ = unstructured.SetNestedSlice(u.Object, []interface{}{
				map[string]interface{}{"type": "Established", "status": "False"}}, "status", "conditions")
		}, false, "Established"},
		{"no status yet", func(u *unstructured.Unstructured) { delete(u.Object, "status") }, false, "Established"},
		{"version not served", func(u *unstructured.Unstructured) {
			_ = unstructured.SetNestedSlice(u.Object, []interface{}{
				map[string]interface{}{"name": "v1alpha1", "served": true},
				map[string]interface{}{"name": "v1beta1", "served": false}}, "spec", "versions")
		}, false, "not served"},
		{"version missing", func(u *unstructured.Unstructured) {
			_ = unstructured.SetNestedSlice(u.Object, []interface{}{
				map[string]interface{}{"name": "v1alpha1", "served": true}}, "spec", "versions")
		}, false, "not served"},
		{"kind mismatch", func(u *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(u.Object, "Other", "spec", "names", "kind")
		}, false, "spec.names.kind"},
		{"group mismatch", func(u *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(u.Object, "example.com", "spec", "group")
		}, false, "spec.group"},
		{"cluster scoped", func(u *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(u.Object, "Cluster", "spec", "scope")
		}, false, "Namespaced"},
		{"being deleted", func(u *unstructured.Unstructured) {
			now := metav1.Now()
			u.SetDeletionTimestamp(&now)
		}, false, "deleted"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := readyCRDObject()
			tc.mutate(u)
			ok, reason := crdReady(u, testDGDGVK)
			if ok != tc.ready || !strings.Contains(reason, tc.reason) {
				t.Errorf("crdReady = %v, %q; want %v, containing %q", ok, reason, tc.ready, tc.reason)
			}
		})
	}
}

func TestCRDNameOf_HandlesTombstones(t *testing.T) {
	m := &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{Name: "leaderworkersets.leaderworkerset.x-k8s.io"}}
	if got := crdNameOf(m); got != m.Name {
		t.Errorf("crdNameOf(object) = %q", got)
	}
	if got := crdNameOf(toolscache.DeletedFinalStateUnknown{Key: m.Name, Obj: m}); got != m.Name {
		t.Errorf("crdNameOf(tombstone) = %q", got)
	}
	if got := crdNameOf("not an object"); got != "" {
		t.Errorf("crdNameOf(garbage) = %q, want empty", got)
	}
}

func TestStripCRDMetadata(t *testing.T) {
	m := &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{
		Name:          "x",
		Annotations:   map[string]string{"kubectl.kubernetes.io/last-applied-configuration": "{…}"},
		ManagedFields: []metav1.ManagedFieldsEntry{{Manager: "kubectl"}},
		Labels:        map[string]string{"keep": "me"},
	}}
	if _, err := stripCRDMetadata(m); err != nil {
		t.Fatal(err)
	}
	if m.Annotations != nil || m.ManagedFields != nil || m.Labels["keep"] != "me" || m.Name != "x" {
		t.Errorf("unexpected metadata after strip: %+v", m.ObjectMeta)
	}
}

// newApplyTestSupervisor builds just enough of a supervisor to drive
// apply() without starting anything: Client nil skips the sweep.
func newApplyTestSupervisor(t *testing.T) *WatchSupervisor {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s := &WatchSupervisor{
		Health:  NewWatchHealth(),
		Tracked: NewTrackedKinds(),
		// Never triggered here: only its request channel is observed.
		Fanout:  &AllowlistFanout{},
		Watches: []ThirdPartyWatch{{Name: "DynamoGraphDeployment", GVK: testDGDGVK, CRD: "dynamographdeployments.nvidia.com"}},
	}
	s.rootCtx = ctx
	s.runners = map[string]*watchRunner{}
	s.kick = make(chan struct{}, 1)
	s.crd.obs = map[string]crdObservation{}
	s.activated = map[string]bool{}
	s.lagReported = map[string]int{}
	s.pendingStart.kinds = map[string]bool{}
	s.fanoutReq = make(chan struct{}, 1)
	return s
}

// Row 1: an unconfirmed CRD gets nothing, whatever the allowlist says.
func TestApply_UnknownCRDGetsNothing(t *testing.T) {
	s := newApplyTestSupervisor(t)
	for _, allowed := range []config.WorkloadKindSet{config.AllWorkloadKinds(), config.NewWorkloadKindSet(gkDeployment)} {
		s.apply(allowed)
	}
	if st, ok := s.Health.Status("DynamoGraphDeployment"); ok {
		t.Fatalf("health entry for an Unknown CRD: %+v", st)
	}
	if s.Tracked.Has(gkDGD) || len(s.runners) != 0 {
		t.Fatal("an Unknown CRD must not be tracked or run")
	}
}

// Row 3: Ready and excluded is disabled once and tracked, never run.
func TestApply_ReadyAndExcludedIsDisabledOnce(t *testing.T) {
	s := newApplyTestSupervisor(t)
	var transitions []WatchTransition
	s.Health.Subscribe(func(tr WatchTransition) { transitions = append(transitions, tr) })
	s.crd.obs["DynamoGraphDeployment"] = crdObservation{ready: true, late: true}
	excluded := config.NewWorkloadKindSet(gkDeployment)
	s.apply(excluded)
	s.apply(excluded)
	st, ok := s.Health.Status("DynamoGraphDeployment")
	if !ok || !st.Disabled {
		t.Fatalf("want disabled, got %+v (present=%v)", st, ok)
	}
	if len(transitions) != 1 || !transitions[0].Disabled {
		t.Errorf("want exactly one Disabled transition, got %+v", transitions)
	}
	if !s.Tracked.Has(gkDGD) || len(s.runners) != 0 {
		t.Error("row 3: tracked, not running")
	}
	if s.isPendingStart("DynamoGraphDeployment") {
		t.Error("row 3 must not arm WatchStarted")
	}
	select {
	case <-s.fanoutReq:
	default:
		t.Error("a newly tracked kind requests one fan-out")
	}
}

// Mapping lag past CacheSyncTimeout surfaces once as Unhealthy without
// starting anything.
func TestApply_MappingLagReportedOnce(t *testing.T) {
	s := newApplyTestSupervisor(t)
	s.crd.obs["DynamoGraphDeployment"] = crdObservation{lagErr: errTest("not discoverable"), lagSeq: 1}
	s.apply(config.AllWorkloadKinds())
	s.apply(config.AllWorkloadKinds())
	st, ok := s.Health.Status("DynamoGraphDeployment")
	if !ok || !st.Degraded() || st.Failures != 1 {
		t.Fatalf("want one recorded failure, got %+v", st)
	}
	if len(s.runners) != 0 || s.Tracked.Has(gkDGD) {
		t.Error("lag must not start or track the kind")
	}
}

type errTest string

func (e errTest) Error() string { return string(e) }

func TestWatchHealth_MarkStartedTransition(t *testing.T) {
	h := NewWatchHealth()
	var got []WatchTransition
	h.Subscribe(func(tr WatchTransition) { got = append(got, tr) })
	h.Register("K")
	h.MarkStarting("K")
	if !h.MarkStarted("K") {
		t.Fatal("first MarkStarted must transition")
	}
	if h.MarkStarted("K") || h.MarkHealthy("K") {
		t.Fatal("already healthy: no second transition")
	}
	if len(got) != 1 || !got[0].Started || !got[0].Healthy {
		t.Errorf("want one Started healthy transition, got %+v", got)
	}
}

// TriggerContext gives up waiting behind a running fan-out when its
// context ends (amendment A5), so supervisor shutdown never blocks.
func TestAllowlistFanout_TriggerContextCancelled(t *testing.T) {
	f := &AllowlistFanout{Reader: blockingListReader{}}
	f.Source(gkDeployment, false, func() client.ObjectList { return &unstructured.UnstructuredList{} }, unstructuredListItems)
	first, cancelFirst := context.WithCancel(context.Background())
	done1 := make(chan struct{})
	go func() { f.TriggerContext(first, config.AllWorkloadKinds()); close(done1) }()
	time.Sleep(100 * time.Millisecond) // first holds the slot, blocked in List

	second, cancelSecond := context.WithCancel(context.Background())
	done2 := make(chan struct{})
	go func() { f.TriggerContext(second, config.AllWorkloadKinds()); close(done2) }()
	cancelSecond()
	select {
	case <-done2:
	case <-time.After(5 * time.Second):
		t.Fatal("a waiting TriggerContext must return when its context is cancelled")
	}
	cancelFirst()
	select {
	case <-done1:
	case <-time.After(5 * time.Second):
		t.Fatal("a running TriggerContext must stop when its context is cancelled")
	}
}

type blockingListReader struct{ client.Reader }

func (blockingListReader) List(ctx context.Context, _ client.ObjectList, _ ...client.ListOption) error {
	<-ctx.Done()
	return ctx.Err()
}

// Each candidate's compiled CRD name matches its envtest fixture, and
// the read-only CRD rule is identical in the generated role and the
// Helm ClusterRole (amendment A6/A7).
func TestThirdPartyWatchCRDNames_MatchFixturesAndRBAC(t *testing.T) {
	watches := []ThirdPartyWatch{
		(&KServeInferenceServiceReconciler{}).Watch(),
		(&DynamoGraphDeploymentReconciler{}).Watch(),
		(&DynamoComponentDeploymentReconciler{}).Watch(),
		(&NIMServiceReconciler{}).Watch(),
		(&LeaderWorkerSetReconciler{}).Watch(),
		(&DisaggregatedSetReconciler{}).Watch(),
	}
	fixtures := map[string]map[string]interface{}{}
	dir := filepath.Join("..", "..", "config", "crd", "external")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var obj map[string]interface{}
		if err := yaml.Unmarshal(b, &obj); err != nil {
			t.Fatalf("%s: %v", e.Name(), err)
		}
		name, _, _ := unstructured.NestedString(obj, "metadata", "name")
		fixtures[name] = obj
	}
	for _, w := range watches {
		obj, ok := fixtures[w.CRD]
		if !ok {
			t.Errorf("%s: no fixture CRD named %q", w.Name, w.CRD)
			continue
		}
		u := &unstructured.Unstructured{Object: obj}
		// The fixture is served but has no status; mark it Established
		// to check the remaining A1 conditions against the compiled GVK.
		_ = unstructured.SetNestedSlice(u.Object, []interface{}{
			map[string]interface{}{"type": "Established", "status": "True"}}, "status", "conditions")
		if ok, reason := crdReady(u, w.GVK); !ok {
			t.Errorf("%s: fixture %s does not match the compiled GVK %s: %s", w.Name, w.CRD, w.GVK, reason)
		}
		plural, _, _ := unstructured.NestedString(obj, "spec", "names", "plural")
		if want := plural + "." + w.GVK.Group; w.CRD != want {
			t.Errorf("%s: CRD %q, want <plural>.<group> %q", w.Name, w.CRD, want)
		}
	}

	for _, path := range []string{
		filepath.Join("..", "..", "config", "rbac", "role.yaml"),
		filepath.Join("..", "..", "charts", "k8s-aibom", "templates", "clusterrole.yaml"),
	} {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !hasReadOnlyCRDRule(t, string(b)) {
			t.Errorf("%s: want exactly get/list/watch on apiextensions.k8s.io customresourcedefinitions", path)
		}
	}
}

// hasReadOnlyCRDRule finds the customresourcedefinitions rule in a
// ClusterRole (Helm template lines stripped) and checks its verbs are
// exactly get, list, watch.
func hasReadOnlyCRDRule(t *testing.T, doc string) bool {
	t.Helper()
	var lines []string
	for _, l := range strings.Split(doc, "\n") {
		if strings.Contains(l, "{{") {
			continue
		}
		lines = append(lines, l)
	}
	var role struct {
		Rules []struct {
			APIGroups []string `json:"apiGroups"`
			Resources []string `json:"resources"`
			Verbs     []string `json:"verbs"`
		} `json:"rules"`
	}
	if err := yaml.Unmarshal([]byte(strings.Join(lines, "\n")), &role); err != nil {
		t.Fatalf("parse ClusterRole: %v", err)
	}
	found := 0
	for _, r := range role.Rules {
		for _, res := range r.Resources {
			if res != "customresourcedefinitions" {
				continue
			}
			found++
			if len(r.APIGroups) != 1 || r.APIGroups[0] != "apiextensions.k8s.io" || len(r.Resources) != 1 {
				return false
			}
			verbs := map[string]bool{}
			for _, v := range r.Verbs {
				verbs[v] = true
			}
			if len(verbs) != 3 || !verbs["get"] || !verbs["list"] || !verbs["watch"] {
				return false
			}
		}
	}
	return found == 1
}

// Start must return a discovery initialization error promptly, with no
// goroutine left behind, even though the manager's context stays live:
// it used to start the fan-out goroutine first and then wait for it,
// which only ended with the manager's context.
func TestStart_DiscoveryInitErrorReturnsPromptly(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	s := &WatchSupervisor{
		// An unparsable host makes the metadata client, and so
		// startDiscovery, fail.
		Config:  &rest.Config{Host: "http://bad host"},
		Health:  NewWatchHealth(),
		Fanout:  &AllowlistFanout{},
		Watches: []ThirdPartyWatch{{Name: "DynamoGraphDeployment", GVK: testDGDGVK, CRD: "dynamographdeployments.nvidia.com"}},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- s.Start(ctx) }()
	select {
	case err := <-errCh:
		if err == nil || !strings.Contains(err.Error(), "CRD discovery") {
			t.Fatalf("Start = %v, want a CRD discovery error", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Start did not return its discovery error while the manager context was live")
	}
}

// scriptedCRDReader answers successive full-CRD Gets from a script.
type scriptedCRDReader struct {
	client.Reader
	steps []func(*unstructured.Unstructured) error
	n     int
}

func (r *scriptedCRDReader) Get(_ context.Context, _ client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
	step := r.steps[r.n]
	r.n++
	return step(obj.(*unstructured.Unstructured))
}

func crdStep(uid string, ready bool) func(*unstructured.Unstructured) error {
	return func(u *unstructured.Unstructured) error {
		src := readyCRDObject()
		src.SetUID(types.UID(uid))
		if !ready {
			_ = unstructured.SetNestedSlice(src.Object, []interface{}{
				map[string]interface{}{"type": "NamesAccepted", "status": "False"},
				map[string]interface{}{"type": "Established", "status": "False"}}, "status", "conditions")
		}
		u.Object = src.Object
		return nil
	}
}

func notFoundStep(*unstructured.Unstructured) error {
	return apierrors.NewNotFound(schema.GroupResource{Group: "apiextensions.k8s.io", Resource: "customresourcedefinitions"}, "x")
}

func timeoutStep(*unstructured.Unstructured) error {
	return apierrors.NewTimeoutError("test", 1)
}

// WatchStarted vs WatchRecovered classification (amendment A4) across
// read orders, including the startup/discovery timing cases: only a
// confirmed absence, or a new UID, makes an install late.
func TestReadCRD_LateClassification(t *testing.T) {
	cases := []struct {
		name  string
		steps []func(*unstructured.Unstructured) error
		late  bool
	}{
		{"Ready at the first read (present at startup)", []func(*unstructured.Unstructured) error{crdStep("A", true)}, false},
		{"present but not Established at startup, then Established", []func(*unstructured.Unstructured) error{crdStep("A", false), crdStep("A", true)}, false},
		{"absent at startup, installed later", []func(*unstructured.Unstructured) error{notFoundStep, crdStep("A", true)}, true},
		{"absent, then installed but still establishing, then Established", []func(*unstructured.Unstructured) error{notFoundStep, crdStep("A", false), crdStep("A", true)}, true},
		{"present not Established, deleted, re-created", []func(*unstructured.Unstructured) error{crdStep("A", false), notFoundStep, crdStep("B", true)}, true},
		{"present not Established, re-created unseen (new UID)", []func(*unstructured.Unstructured) error{crdStep("A", false), crdStep("B", true)}, true},
		{"first reads fail, then Ready: installation time unknown", []func(*unstructured.Unstructured) error{timeoutStep, timeoutStep, crdStep("A", true)}, false},
		{"first read fails, then absent, then installed", []func(*unstructured.Unstructured) error{timeoutStep, notFoundStep, crdStep("A", true)}, true},
	}
	mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{testDGDGVK.GroupVersion()})
	mapper.Add(testDGDGVK, meta.RESTScopeNamespace)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &WatchSupervisor{
				CRDReader:        &scriptedCRDReader{steps: tc.steps},
				Mapper:           mapper,
				CacheSyncTimeout: time.Minute,
			}
			s.crd.obs = map[string]crdObservation{}
			s.kick = make(chan struct{}, 1)
			k := &crdWorker{w: ThirdPartyWatch{Name: "DynamoGraphDeployment", GVK: testDGDGVK, CRD: "dynamographdeployments.nvidia.com"}}
			for range tc.steps {
				s.readCRD(context.Background(), k, logr.Discard())
			}
			obs := s.observation("DynamoGraphDeployment")
			if !obs.ready {
				t.Fatalf("want ready after the script, got %+v", obs)
			}
			if obs.late != tc.late {
				t.Errorf("late = %v, want %v", obs.late, tc.late)
			}
		})
	}
}
