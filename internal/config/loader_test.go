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
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	aibomv1alpha1 "github.com/GoogleCloudPlatform/k8s-aibom/api/v1alpha1"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/sink"
)

// Tests in this file follow the user-directed order: the
// fresh-start-invalid-CR fallback semantics are exercised FIRST,
// as the most behaviorally consequential decision in the package.
// Happy-path tests come after the fallback story is locked.

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(s))
	utilruntime.Must(aibomv1alpha1.AddToScheme(s))
	return s
}

func newLoader(t *testing.T, factory SinkFactory, objs ...client.Object) *Loader {
	t.Helper()
	if factory == nil {
		factory = NoopSinkFactory{}
	}
	c := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(objs...).
		Build()
	return &Loader{
		Client:      c,
		SinkFactory: factory,
		ConfigName:  DefaultConfigName,
	}
}

// ---------- Fresh-start / fallback behavior (FIRST per user direction) ----------

// TestLoad_MissingCR_FallsBackToDefaults locks the missing-CR
// contract: the loader returns a compiled-defaults Snapshot, no
// errors. The K8s Event surfacing happens at the reconciler level
// (Checkpoint 4); the loader's contract is "give me a usable
// snapshot, never fail just because the CR isn't there."
func TestLoad_MissingCR_FallsBackToDefaults(t *testing.T) {
	l := newLoader(t, nil) // no AIBOMControllerConfig objects in cluster

	result, err := l.Load(context.Background())
	if err != nil {
		t.Fatalf("Load returned Go error on missing CR (should be nil): %v", err)
	}
	if result.Snapshot == nil {
		t.Fatal("Snapshot is nil")
	}
	if result.Snapshot.Source != SourceCompiledDefaults {
		t.Errorf("Source = %q, want %q (missing-CR must fall back to compiled defaults)",
			result.Snapshot.Source, SourceCompiledDefaults)
	}
	if result.HasErrors() {
		t.Errorf("missing CR should NOT produce LoadErrors; got: %s", result.AggregateMessage())
	}
	if result.Snapshot.InlineThreshold != DefaultInlineThresholdBytes {
		t.Errorf("InlineThreshold = %d, want default %d",
			result.Snapshot.InlineThreshold, DefaultInlineThresholdBytes)
	}
	if len(result.Snapshot.ExternalSinks) != 0 {
		t.Errorf("missing CR should produce no external sinks; got %d",
			len(result.Snapshot.ExternalSinks))
	}
}

// TestLoad_InvalidCR_SinkTypeWebhookButBodyNil locks the
// discriminator-mismatch error path AND the message-specificity
// contract. The Message MUST name the sink, the type, the missing
// field path, and an actionable suggestion. Generic "invalid config"
// messages fail this test.
func TestLoad_InvalidCR_SinkTypeWebhookButBodyNil(t *testing.T) {
	cr := &aibomv1alpha1.AIBOMControllerConfig{
		ObjectMeta: metav1.ObjectMeta{Name: DefaultConfigName},
		Spec: aibomv1alpha1.AIBOMControllerConfigSpec{
			Sinks: []aibomv1alpha1.SinkConfig{
				{
					Name: "audit-webhook",
					Type: aibomv1alpha1.SinkTypeWebhook,
					// Webhook field intentionally nil — this is the bug
					// we're surfacing.
				},
			},
		},
	}
	l := newLoader(t, nil, cr)
	result, err := l.Load(context.Background())
	if err != nil {
		t.Fatalf("Load returned Go error: %v", err)
	}

	// All-or-nothing fallback: snapshot is defaults, NOT a partial
	// from-spec.
	if result.Snapshot.Source != SourceCompiledDefaults {
		t.Errorf("Source = %q, want %q (invalid CR must fall back to compiled defaults)",
			result.Snapshot.Source, SourceCompiledDefaults)
	}

	// Exactly one error.
	if len(result.Errors) != 1 {
		t.Fatalf("Errors = %d, want 1: %+v", len(result.Errors), result.Errors)
	}
	got := result.Errors[0]

	// Field path specificity — auditor-facing precision rule.
	if got.Field != "spec.sinks[name=audit-webhook].webhook" {
		t.Errorf("Field = %q, want %q", got.Field, "spec.sinks[name=audit-webhook].webhook")
	}

	// Message specificity. Required substrings: sink name, type,
	// missing field path, suggested action.
	required := []string{
		`"audit-webhook"`, // sink name in quotes
		"Type=Webhook",    // type explicit
		"webhook is nil",  // the failing condition
		"Set the webhook", // actionable suggestion
		"or change Type",  // alternative suggestion
	}
	for _, sub := range required {
		if !strings.Contains(got.Message, sub) {
			t.Errorf("Message missing required substring %q.\nGot: %q", sub, got.Message)
		}
	}
}

// TestLoad_InvalidCR_SinkExtraTypeBody verifies the inverse error:
// Type=GCS but Webhook is also populated. Customer's CR doesn't
// satisfy "exactly one" body.
func TestLoad_InvalidCR_SinkExtraTypeBody(t *testing.T) {
	cr := &aibomv1alpha1.AIBOMControllerConfig{
		ObjectMeta: metav1.ObjectMeta{Name: DefaultConfigName},
		Spec: aibomv1alpha1.AIBOMControllerConfigSpec{
			Sinks: []aibomv1alpha1.SinkConfig{
				{
					Name: "confused-sink",
					Type: aibomv1alpha1.SinkTypeGCS,
					GCS:  &aibomv1alpha1.GCSSinkSpec{Bucket: "ok"},
					Webhook: &aibomv1alpha1.WebhookSinkSpec{
						Endpoint: "https://example.com/sink",
					},
				},
			},
		},
	}
	l := newLoader(t, nil, cr)
	result, _ := l.Load(context.Background())

	if result.Snapshot.Source != SourceCompiledDefaults {
		t.Errorf("Source = %q, want compiled-defaults", result.Snapshot.Source)
	}
	if len(result.Errors) != 1 {
		t.Fatalf("Errors = %d, want 1: %+v", len(result.Errors), result.Errors)
	}
	got := result.Errors[0]
	if got.Field != "spec.sinks[name=confused-sink].webhook" {
		t.Errorf("Field = %q", got.Field)
	}
	for _, sub := range []string{`"confused-sink"`, "Type=GCS", "webhook is also populated", "Exactly one"} {
		if !strings.Contains(got.Message, sub) {
			t.Errorf("Message missing %q; got: %q", sub, got.Message)
		}
	}
}

// TestLoad_InvalidCR_MultipleErrorsAggregated verifies the all-or-
// nothing rule with multiple distinct failures. Customer sees ALL
// failures in one pass so a single fix-and-apply cycle clears them.
func TestLoad_InvalidCR_MultipleErrorsAggregated(t *testing.T) {
	cr := &aibomv1alpha1.AIBOMControllerConfig{
		ObjectMeta: metav1.ObjectMeta{Name: DefaultConfigName},
		Spec: aibomv1alpha1.AIBOMControllerConfigSpec{
			Discovery: aibomv1alpha1.DiscoveryConfig{
				InferenceRuntimeImagePatterns: []aibomv1alpha1.RuntimeImagePattern{
					{Runtime: "broken", Pattern: "[invalid-regex"}, // doesn't compile
				},
			},
			Sinks: []aibomv1alpha1.SinkConfig{
				{
					Name: "sink-one",
					Type: aibomv1alpha1.SinkTypeGCS,
					// GCS body nil → error
				},
				{
					Name: "sink-two",
					Type: aibomv1alpha1.SinkTypeWebhook,
					// Webhook body nil → error
				},
			},
		},
	}
	l := newLoader(t, nil, cr)
	result, _ := l.Load(context.Background())

	if result.Snapshot.Source != SourceCompiledDefaults {
		t.Errorf("Source = %q, want compiled-defaults (all-or-nothing)", result.Snapshot.Source)
	}
	if len(result.Errors) != 3 {
		t.Errorf("Errors = %d, want 3 (1 pattern + 2 sink shape): %+v",
			len(result.Errors), result.Errors)
	}

	// AggregateMessage joins all errors. Customer reading
	// AIBOMControllerConfig.status.conditions[Ready].Message sees the
	// full list.
	agg := result.AggregateMessage()
	for _, sub := range []string{"broken", "sink-one", "sink-two"} {
		if !strings.Contains(agg, sub) {
			t.Errorf("AggregateMessage missing %q; got: %q", sub, agg)
		}
	}
}

// TestLoad_InvalidCR_DuplicateSinkNames locks the unique-name rule.
// +listMapKey=name on the CRD enforces this for server-side apply but
// not all paths use SSA; loader-side enforcement is the safety net.
func TestLoad_InvalidCR_DuplicateSinkNames(t *testing.T) {
	cr := &aibomv1alpha1.AIBOMControllerConfig{
		ObjectMeta: metav1.ObjectMeta{Name: DefaultConfigName},
		Spec: aibomv1alpha1.AIBOMControllerConfigSpec{
			Sinks: []aibomv1alpha1.SinkConfig{
				{Name: "duplicate", Type: aibomv1alpha1.SinkTypeGCS, GCS: &aibomv1alpha1.GCSSinkSpec{Bucket: "a"}},
				{Name: "duplicate", Type: aibomv1alpha1.SinkTypeGCS, GCS: &aibomv1alpha1.GCSSinkSpec{Bucket: "b"}},
			},
		},
	}
	l := newLoader(t, nil, cr)
	result, _ := l.Load(context.Background())

	if result.Snapshot.Source != SourceCompiledDefaults {
		t.Errorf("Source = %q, want compiled-defaults", result.Snapshot.Source)
	}
	found := false
	for _, e := range result.Errors {
		if strings.Contains(e.Message, "duplicate") && strings.Contains(e.Message, "unique") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a duplicate-name error mentioning 'unique'; got: %+v", result.Errors)
	}
}

// TestLoad_InvalidCR_FactoryErrorTriggersAllOrNothing verifies that
// a SinkFactory-reported failure (Secret not found, etc.) causes the
// same all-or-nothing fallback as shape errors.
func TestLoad_InvalidCR_FactoryErrorTriggersAllOrNothing(t *testing.T) {
	cr := &aibomv1alpha1.AIBOMControllerConfig{
		ObjectMeta: metav1.ObjectMeta{Name: DefaultConfigName},
		Spec: aibomv1alpha1.AIBOMControllerConfigSpec{
			Sinks: []aibomv1alpha1.SinkConfig{
				{Name: "shape-valid", Type: aibomv1alpha1.SinkTypeGCS, GCS: &aibomv1alpha1.GCSSinkSpec{Bucket: "ok"}},
			},
		},
	}
	// Stub factory simulates "Secret not found" at construction time.
	stub := &stubSinkFactory{
		errs: []LoadError{{
			Field:   "spec.sinks[name=shape-valid].gcs.credentialsSecretRef",
			Message: `Sink "shape-valid" references Secret "missing-secret" but the Secret was not found in namespace "k8s-aibom-system".`,
		}},
	}
	l := newLoader(t, stub, cr)
	result, _ := l.Load(context.Background())

	if result.Snapshot.Source != SourceCompiledDefaults {
		t.Errorf("Source = %q, want compiled-defaults (factory error → all-or-nothing)",
			result.Snapshot.Source)
	}
	if len(result.Errors) != 1 {
		t.Fatalf("Errors = %d, want 1: %+v", len(result.Errors), result.Errors)
	}
	if !strings.Contains(result.Errors[0].Message, "missing-secret") {
		t.Errorf("Message should propagate factory's error; got: %q", result.Errors[0].Message)
	}
}

// TestLoad_TransientAPIError_ReturnsGoError verifies the third
// failure class: API failures (network, RBAC, throttling) propagate
// as Go errors so the caller can retry. Distinct from
// missing/invalid-CR which fall back silently with a snapshot.
func TestLoad_TransientAPIError_ReturnsGoError(t *testing.T) {
	// Construct a client that will fail any Get call. The fake client
	// builder doesn't easily simulate this; use a tiny wrapper.
	l := &Loader{
		Client:      &failingClient{},
		SinkFactory: NoopSinkFactory{},
		ConfigName:  DefaultConfigName,
	}
	_, err := l.Load(context.Background())
	if err == nil {
		t.Fatal("expected Go error on transient API failure")
	}
	if !strings.Contains(err.Error(), "get AIBOMControllerConfig") {
		t.Errorf("error should name the failing operation; got: %v", err)
	}
}

// ---------- Happy-path parsing ----------

// TestLoad_ValidCR_AppliesSpec exercises the happy path: a valid CR
// produces a Snapshot derived from spec, no errors, Source=config-cr.
func TestLoad_ValidCR_AppliesSpec(t *testing.T) {
	cr := &aibomv1alpha1.AIBOMControllerConfig{
		ObjectMeta: metav1.ObjectMeta{Name: DefaultConfigName, Generation: 7},
		Spec: aibomv1alpha1.AIBOMControllerConfigSpec{
			BOMGeneration: aibomv1alpha1.BOMGenerationConfig{
				InlineThresholdBytes: 65536, // 64 KiB
			},
			Discovery: aibomv1alpha1.DiscoveryConfig{
				NamespaceSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"team": "platform"},
				},
				InferenceRuntimeImagePatterns: []aibomv1alpha1.RuntimeImagePattern{
					{Runtime: "custom", Pattern: `^my-mirror/.*custom.*`},
				},
			},
		},
	}
	stub := &stubSinkFactory{sinks: []sink.Sink{}, errs: nil}
	l := newLoader(t, stub, cr)
	result, err := l.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if result.HasErrors() {
		t.Fatalf("unexpected errors: %s", result.AggregateMessage())
	}

	snap := result.Snapshot
	if snap.Source != SourceConfigCR {
		t.Errorf("Source = %q, want %q", snap.Source, SourceConfigCR)
	}
	if snap.SourceGeneration != 7 {
		t.Errorf("SourceGeneration = %d, want 7", snap.SourceGeneration)
	}
	if snap.InlineThreshold != 65536 {
		t.Errorf("InlineThreshold = %d, want 65536", snap.InlineThreshold)
	}
	// Selector materialized from CR
	if !snap.NamespaceSelector.Matches(labelSet("team=platform")) {
		t.Errorf("NamespaceSelector should match team=platform")
	}
	if snap.NamespaceSelector.Matches(labelSet(DefaultNamespaceOptInLabel + "=true")) {
		t.Errorf("NamespaceSelector should NOT match default opt-in label when CR overrides")
	}
	// Custom pattern present, defaults preserved for other runtimes
	if got, _ := snap.Patterns.DetectRuntime("my-mirror/team-x/custom-server:v1"); got != "custom" {
		t.Errorf("DetectRuntime on custom image = %q, want %q", got, "custom")
	}
	// Customer patterns REPLACE defaults; vllm is no longer detected
	if got, _ := snap.Patterns.DetectRuntime("vllm/vllm-openai:v0.6.3"); got != "" {
		t.Errorf("DetectRuntime on vllm image = %q, want empty (customer patterns replace defaults)", got)
	}
	// Non-pattern allowlists (env vars, args, volumes) preserved from defaults
	if !snap.Patterns.IsModelEnvVarName("HF_MODEL_ID") {
		t.Errorf("HF_MODEL_ID should still be in modelEnvVarNames (compiled defaults)")
	}
}

// TestLoad_NilDiscovery_DefaultsApplied verifies that a CR with
// Discovery section entirely omitted falls through to compiled
// defaults for that section (without triggering the all-or-nothing
// fallback that would mark Source=compiled-defaults).
func TestLoad_NilDiscovery_DefaultsApplied(t *testing.T) {
	cr := &aibomv1alpha1.AIBOMControllerConfig{
		ObjectMeta: metav1.ObjectMeta{Name: DefaultConfigName, Generation: 3},
		Spec: aibomv1alpha1.AIBOMControllerConfigSpec{
			// Discovery omitted entirely
			BOMGeneration: aibomv1alpha1.BOMGenerationConfig{
				InlineThresholdBytes: 0, // also omitted; resolver fills in default
			},
		},
	}
	l := newLoader(t, nil, cr)
	result, _ := l.Load(context.Background())
	if result.HasErrors() {
		t.Fatalf("unexpected errors: %s", result.AggregateMessage())
	}

	snap := result.Snapshot
	// Source IS config-cr — the CR exists and is valid, even with no
	// content. Distinct from missing-CR (Source=compiled-defaults).
	if snap.Source != SourceConfigCR {
		t.Errorf("Source = %q, want %q (CR exists)", snap.Source, SourceConfigCR)
	}
	if snap.InlineThreshold != DefaultInlineThresholdBytes {
		t.Errorf("InlineThreshold = %d, want default %d (zero-value resolved)",
			snap.InlineThreshold, DefaultInlineThresholdBytes)
	}
	// Default namespace selector applied
	if !snap.NamespaceSelector.Matches(labelSet(DefaultNamespaceOptInLabel + "=true")) {
		t.Errorf("nil NamespaceSelector should fall back to compiled default opt-in label")
	}
	// Default patterns preserved
	if got, _ := snap.Patterns.DetectRuntime("vllm/vllm-openai:v0.6.3"); got != "vllm" {
		t.Errorf("default vLLM pattern should still match when CR doesn't override; got %q", got)
	}
}

func TestLoad_BOMGeneration_InlineThresholdClamping(t *testing.T) {
	cases := []struct {
		name       string
		inputValue int64
		wantValue  int64
	}{
		{
			name:       "Too small threshold - clamped to min 1024",
			inputValue: 500,
			wantValue:  1024,
		},
		{
			name:       "Too large threshold - clamped to max 1 MiB",
			inputValue: 5000000,
			wantValue:  1048576,
		},
		{
			name:       "Valid threshold - unchanged",
			inputValue: 50000,
			wantValue:  50000,
		},
		{
			name:       "Negative threshold - falls back to default 256 KiB",
			inputValue: -50,
			wantValue:  262144,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cr := &aibomv1alpha1.AIBOMControllerConfig{
				ObjectMeta: metav1.ObjectMeta{Name: DefaultConfigName},
				Spec: aibomv1alpha1.AIBOMControllerConfigSpec{
					BOMGeneration: aibomv1alpha1.BOMGenerationConfig{
						InlineThresholdBytes: tc.inputValue,
					},
				},
			}
			l := newLoader(t, nil, cr)
			result, err := l.Load(context.Background())
			if err != nil {
				t.Fatalf("Load failed: %v", err)
			}
			if result.HasErrors() {
				t.Fatalf("unexpected errors: %s", result.AggregateMessage())
			}
			if got := result.Snapshot.InlineThreshold; got != tc.wantValue {
				t.Errorf("InlineThreshold = %d, want %d", got, tc.wantValue)
			}
		})
	}
}

// ---------- Store + Snapshot atomic behavior ----------

func TestStore_LoadStoreAtomic(t *testing.T) {
	s := NewStore(DefaultSnapshot())

	first := s.Load()
	if first == nil {
		t.Fatal("Load returned nil")
	}
	if first.Source != SourceCompiledDefaults {
		t.Errorf("initial Source = %q", first.Source)
	}

	// Replace with a different snapshot
	updated := &Snapshot{Source: SourceConfigCR, InlineThreshold: 1024}
	s.Store(updated)

	got := s.Load()
	if got.Source != SourceConfigCR {
		t.Errorf("after Store, Source = %q, want %q", got.Source, SourceConfigCR)
	}
	if got.InlineThreshold != 1024 {
		t.Errorf("after Store, InlineThreshold = %d", got.InlineThreshold)
	}
}

func TestStore_RejectsNilInitial(t *testing.T) {
	// NewStore with nil → falls back to defaults, never hosts nil.
	s := NewStore(nil)
	if s.Load() == nil {
		t.Fatal("Store should never host nil; NewStore(nil) must use DefaultSnapshot")
	}
	if s.Load().Source != SourceCompiledDefaults {
		t.Errorf("NewStore(nil).Load().Source = %q, want compiled-defaults",
			s.Load().Source)
	}
}

func TestStore_RejectsNilStore(t *testing.T) {
	s := NewStore(DefaultSnapshot())
	before := s.Load()
	s.Store(nil) // documented to be ignored
	after := s.Load()
	if after != before {
		t.Error("Store(nil) should leave the current snapshot unchanged")
	}
}

func TestDefaultSnapshot_Shape(t *testing.T) {
	d := DefaultSnapshot()
	if d.Patterns == nil {
		t.Fatal("Patterns is nil")
	}
	if d.InlineThreshold != DefaultInlineThresholdBytes {
		t.Errorf("InlineThreshold = %d, want %d", d.InlineThreshold, DefaultInlineThresholdBytes)
	}
	if d.NamespaceSelector == nil {
		t.Fatal("NamespaceSelector is nil")
	}
	if !d.NamespaceSelector.Matches(labelSet(DefaultNamespaceOptInLabel + "=true")) {
		t.Errorf("default selector should match the opt-in label")
	}
	if len(d.ExternalSinks) != 0 {
		t.Errorf("default ExternalSinks should be empty (PRD FR6.6 safe-by-default); got %d",
			len(d.ExternalSinks))
	}
	if d.Source != SourceCompiledDefaults {
		t.Errorf("Source = %q, want %q", d.Source, SourceCompiledDefaults)
	}
}

// ---------- helpers ----------

// stubSinkFactory is a SinkFactory test double. Returns whatever
// sinks/errs it was constructed with.
type stubSinkFactory struct {
	sinks []sink.Sink
	errs  []LoadError
}

func (f *stubSinkFactory) BuildSinks(_ context.Context, _ []aibomv1alpha1.SinkConfig) ([]sink.Sink, []LoadError) {
	return f.sinks, f.errs
}

// failingClient is a minimal client.Client that returns an error on
// every Get. Used to simulate transient API failures.
type failingClient struct {
	client.Client // embed for the methods we don't override
}

func (failingClient) Get(_ context.Context, _ client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
	return errFakeAPIFailure
}

var errFakeAPIFailure = &apiError{msg: "fake API server unreachable"}

type apiError struct{ msg string }

func (e *apiError) Error() string { return e.msg }

// labelSet constructs a labels.Set from a "k=v" string for test
// convenience. Only handles single-pair labels; sufficient for the
// selector tests in this file. Returns labels.Set (which implements
// labels.Labels) so it can be passed to Selector.Matches.
func labelSet(s string) labels.Set {
	parts := strings.SplitN(s, "=", 2)
	if len(parts) != 2 {
		return labels.Set{}
	}
	return labels.Set{parts[0]: parts[1]}
}
