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

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	aibomv1alpha1 "github.com/GoogleCloudPlatform/k8s-aibom/api/v1alpha1"
)

// Tests in this file lock the auditor-facing-precision contract on
// the production ClientSinkFactory's error messages. Each Secret-
// related failure mode is asserted via substring checks (NOT exact-
// match) so contributors can refine wording without breaking tests,
// but cannot regress to generic "secret not found" diagnostics that
// fail to name the failing sink, field path, or actionable fix.
//
// GCS sink happy-path construction requires Google Cloud ADC and is
// exercised in integration tests and the smoke-test harness. Unit
// tests here verify the GCS dispatch path via failure modes only —
// do NOT add a mocked-half-of-the-SDK happy path here; it will be
// flaky and will not catch real auth misconfigurations.

const testControllerNamespace = "k8s-aibom-system"

// newClientFactory constructs a ClientSinkFactory wired to a fake
// K8s client seeded with the given Secrets. The factory's namespace
// is fixed to testControllerNamespace; tests that need to assert
// cross-namespace impossibility seed Secrets in OTHER namespaces and
// verify the factory still reports "not found in
// k8s-aibom-system."
func newClientFactory(t *testing.T, objs ...client.Object) *ClientSinkFactory {
	t.Helper()
	c := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(objs...).
		Build()
	return &ClientSinkFactory{
		Client:            c,
		Namespace:         testControllerNamespace,
		ControllerVersion: "test-v0",
	}
}

func secretWith(name string, data map[string][]byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: testControllerNamespace,
		},
		Data: data,
	}
}

// ---------- Failure modes: auditor-precision message contract ----------

// TestBuildSinks_SecretNotFound locks the message shape for the
// most common Secret failure: the referenced Secret simply does not
// exist in the controller's namespace. Customer sees the sink name,
// the failing Secret name, the namespace, and an actionable next
// step naming the exact CR field to edit.
func TestBuildSinks_SecretNotFound(t *testing.T) {
	f := newClientFactory(t) // no Secrets seeded
	specs := []aibomv1alpha1.SinkConfig{
		{
			Name: "audit-webhook",
			Type: aibomv1alpha1.SinkTypeWebhook,
			Webhook: &aibomv1alpha1.WebhookSinkSpec{
				Endpoint: "https://example.com/sink",
				Auth: &aibomv1alpha1.WebhookAuth{
					BearerToken: &aibomv1alpha1.BearerTokenAuth{
						SecretRef: aibomv1alpha1.SecretKeyRef{
							Name: "webhook-creds",
							Key:  "token",
						},
					},
				},
			},
		},
	}

	sinks, errs := f.BuildSinks(context.Background(), specs)
	if len(sinks) != 0 {
		t.Errorf("sinks = %d, want 0 (Secret not found should prevent construction)", len(sinks))
	}
	if len(errs) != 1 {
		t.Fatalf("errs = %d, want 1: %+v", len(errs), errs)
	}
	got := errs[0]

	if got.Field != "spec.sinks[name=audit-webhook].webhook.auth.bearerToken.secretRef" {
		t.Errorf("Field = %q", got.Field)
	}

	// Auditor-precision substrings: sink name in quotes, the sink-type
	// context, the failing Secret name in quotes, the namespace name
	// in quotes, the actionable fix.
	required := []string{
		`"audit-webhook"`,
		"Type=Webhook",
		`"webhook-creds"`,
		`"k8s-aibom-system"`,
		"not found",
		"Create the Secret",
		"webhook.auth.bearerToken.secretRef.name",
	}
	for _, sub := range required {
		if !strings.Contains(got.Message, sub) {
			t.Errorf("Message missing required substring %q.\nGot: %q", sub, got.Message)
		}
	}
}

// TestBuildSinks_SecretFoundButKeyMissing locks the "wrong key"
// diagnostic. The customer's CR points at a real Secret but the
// named key isn't in its Data map — almost always a typo. The
// message MUST list the available keys so the customer can spot
// the typo without scanning the cluster.
func TestBuildSinks_SecretFoundButKeyMissing(t *testing.T) {
	// Seed a Secret with multiple keys; customer's CR points at
	// "token" but the Secret only has "bearer" and "api-key".
	sec := secretWith("webhook-creds", map[string][]byte{
		"bearer":  []byte("the-actual-token"),
		"api-key": []byte("unrelated"),
	})
	f := newClientFactory(t, sec)
	specs := []aibomv1alpha1.SinkConfig{
		{
			Name: "audit-webhook",
			Type: aibomv1alpha1.SinkTypeWebhook,
			Webhook: &aibomv1alpha1.WebhookSinkSpec{
				Endpoint: "https://example.com/sink",
				Auth: &aibomv1alpha1.WebhookAuth{
					BearerToken: &aibomv1alpha1.BearerTokenAuth{
						SecretRef: aibomv1alpha1.SecretKeyRef{
							Name: "webhook-creds",
							Key:  "token", // typo: should be "bearer"
						},
					},
				},
			},
		},
	}

	_, errs := f.BuildSinks(context.Background(), specs)
	if len(errs) != 1 {
		t.Fatalf("errs = %d, want 1: %+v", len(errs), errs)
	}
	got := errs[0]

	if got.Field != "spec.sinks[name=audit-webhook].webhook.auth.bearerToken.secretRef.key" {
		t.Errorf("Field = %q (should point at .key, not .name — the Secret was found)", got.Field)
	}

	required := []string{
		`"audit-webhook"`,
		`"webhook-creds"`,
		`"token"`,        // the missing key, in quotes
		"no key",         // the failing condition
		"Available keys", // the diagnostic header
		"api-key",        // actual key 1 (sorted first)
		"bearer",         // actual key 2 (sorted second)
		"Update",         // actionable verb
	}
	for _, sub := range required {
		if !strings.Contains(got.Message, sub) {
			t.Errorf("Message missing required substring %q.\nGot: %q", sub, got.Message)
		}
	}

	// The "Available keys" list MUST be sorted for determinism across
	// K8s API server versions and test runs. Verify api-key precedes
	// bearer within the keys list (the substring after "Available keys:").
	// We scope the search to the keys list because "bearer" also appears
	// earlier in the message as part of the field path.
	keysIdx := strings.Index(got.Message, "Available keys:")
	if keysIdx < 0 {
		t.Fatalf("Message has no 'Available keys:' section: %q", got.Message)
	}
	keysSection := got.Message[keysIdx:]
	apiIdx := strings.Index(keysSection, "api-key")
	bearerIdx := strings.Index(keysSection, "bearer")
	if apiIdx < 0 || bearerIdx < 0 || apiIdx > bearerIdx {
		t.Errorf("Available keys not sorted within keys section: api-key idx=%d, bearer idx=%d.\nKeys section: %q",
			apiIdx, bearerIdx, keysSection)
	}
}

// TestBuildSinks_SecretKeyEmpty locks the "key exists but value is
// empty" diagnostic. Distinct from "key missing": the customer
// targeted the right key but the Secret data is blank, usually
// because the Secret was created without `--from-literal` or with
// an empty file.
func TestBuildSinks_SecretKeyEmpty(t *testing.T) {
	sec := secretWith("webhook-creds", map[string][]byte{
		"token": []byte(""), // empty value
	})
	f := newClientFactory(t, sec)
	specs := []aibomv1alpha1.SinkConfig{
		{
			Name: "audit-webhook",
			Type: aibomv1alpha1.SinkTypeWebhook,
			Webhook: &aibomv1alpha1.WebhookSinkSpec{
				Endpoint: "https://example.com/sink",
				Auth: &aibomv1alpha1.WebhookAuth{
					BearerToken: &aibomv1alpha1.BearerTokenAuth{
						SecretRef: aibomv1alpha1.SecretKeyRef{
							Name: "webhook-creds",
							Key:  "token",
						},
					},
				},
			},
		},
	}

	_, errs := f.BuildSinks(context.Background(), specs)
	if len(errs) != 1 {
		t.Fatalf("errs = %d, want 1: %+v", len(errs), errs)
	}
	got := errs[0]

	required := []string{
		`"audit-webhook"`,
		`"webhook-creds"`,
		`"token"`,
		"empty value",
		"Populate the Secret",
	}
	for _, sub := range required {
		if !strings.Contains(got.Message, sub) {
			t.Errorf("Message missing required substring %q.\nGot: %q", sub, got.Message)
		}
	}
}

// TestBuildSinks_BothBearerAndMTLS locks the "exactly one auth
// mechanism" invariant at the factory level. The API surface allows
// both fields to be set structurally (separate optional pointers),
// so the factory must reject this combination explicitly.
func TestBuildSinks_BothBearerAndMTLS(t *testing.T) {
	f := newClientFactory(t)
	specs := []aibomv1alpha1.SinkConfig{
		{
			Name: "ambiguous-auth",
			Type: aibomv1alpha1.SinkTypeWebhook,
			Webhook: &aibomv1alpha1.WebhookSinkSpec{
				Endpoint: "https://example.com/sink",
				Auth: &aibomv1alpha1.WebhookAuth{
					BearerToken: &aibomv1alpha1.BearerTokenAuth{
						SecretRef: aibomv1alpha1.SecretKeyRef{Name: "a", Key: "b"},
					},
					MTLS: &aibomv1alpha1.MTLSAuth{
						ClientCertSecretRef: aibomv1alpha1.SecretKeyRef{Name: "c", Key: "d"},
						ClientKeySecretRef:  aibomv1alpha1.SecretKeyRef{Name: "e", Key: "f"},
					},
				},
			},
		},
	}

	_, errs := f.BuildSinks(context.Background(), specs)
	if len(errs) != 1 {
		t.Fatalf("errs = %d, want 1: %+v", len(errs), errs)
	}
	got := errs[0]

	if got.Field != "spec.sinks[name=ambiguous-auth].webhook.auth" {
		t.Errorf("Field = %q", got.Field)
	}

	required := []string{
		`"ambiguous-auth"`,
		"both auth.bearerToken and auth.mtls",
		"Exactly one auth mechanism",
		"remove one",
	}
	for _, sub := range required {
		if !strings.Contains(got.Message, sub) {
			t.Errorf("Message missing required substring %q.\nGot: %q", sub, got.Message)
		}
	}
}

// TestBuildSinks_MTLSMissingCertSecret exercises the mTLS code path
// for the missing-Secret diagnostic. The field path differs from the
// bearer-token case; the message-shape contract is identical.
func TestBuildSinks_MTLSMissingCertSecret(t *testing.T) {
	f := newClientFactory(t) // no Secrets seeded
	specs := []aibomv1alpha1.SinkConfig{
		{
			Name: "mtls-webhook",
			Type: aibomv1alpha1.SinkTypeWebhook,
			Webhook: &aibomv1alpha1.WebhookSinkSpec{
				Endpoint: "https://example.com/sink",
				Auth: &aibomv1alpha1.WebhookAuth{
					MTLS: &aibomv1alpha1.MTLSAuth{
						ClientCertSecretRef: aibomv1alpha1.SecretKeyRef{
							Name: "mtls-cert",
							Key:  "tls.crt",
						},
						ClientKeySecretRef: aibomv1alpha1.SecretKeyRef{
							Name: "mtls-key",
							Key:  "tls.key",
						},
					},
				},
			},
		},
	}

	_, errs := f.BuildSinks(context.Background(), specs)
	if len(errs) != 1 {
		t.Fatalf("errs = %d, want 1: %+v", len(errs), errs)
	}
	got := errs[0]

	if got.Field != "spec.sinks[name=mtls-webhook].webhook.auth.mtls.clientCertSecretRef" {
		t.Errorf("Field = %q", got.Field)
	}
	required := []string{
		`"mtls-webhook"`,
		`"mtls-cert"`,
		"not found",
		"webhook.auth.mtls.clientCertSecretRef.name",
	}
	for _, sub := range required {
		if !strings.Contains(got.Message, sub) {
			t.Errorf("Message missing required substring %q.\nGot: %q", sub, got.Message)
		}
	}
}

// TestBuildSinks_GCSMissingCredentialsSecret exercises the GCS code
// path for the missing-Secret diagnostic.
func TestBuildSinks_GCSMissingCredentialsSecret(t *testing.T) {
	f := newClientFactory(t)
	specs := []aibomv1alpha1.SinkConfig{
		{
			Name: "archive-gcs",
			Type: aibomv1alpha1.SinkTypeGCS,
			GCS: &aibomv1alpha1.GCSSinkSpec{
				Bucket: "my-bucket",
				CredentialsSecretRef: &aibomv1alpha1.SecretKeyRef{
					Name: "gcs-creds",
					Key:  "key.json",
				},
			},
		},
	}

	_, errs := f.BuildSinks(context.Background(), specs)
	if len(errs) != 1 {
		t.Fatalf("errs = %d, want 1: %+v", len(errs), errs)
	}
	got := errs[0]

	if got.Field != "spec.sinks[name=archive-gcs].gcs.credentialsSecretRef" {
		t.Errorf("Field = %q", got.Field)
	}
	required := []string{
		`"archive-gcs"`,
		"Type=GCS",
		`"gcs-creds"`,
		"not found",
		"gcs.credentialsSecretRef.name",
	}
	for _, sub := range required {
		if !strings.Contains(got.Message, sub) {
			t.Errorf("Message missing required substring %q.\nGot: %q", sub, got.Message)
		}
	}
}

// TestBuildSinks_CrossNamespaceSecretIsInvisible verifies the
// sole-writer security model from PRD §FR4.4. A Secret in a
// different namespace is invisible to the factory: even if it
// has the right name and key, the customer still sees "not found
// in <controller-namespace>." Without this guarantee, a compromised
// inference workload could trick the controller into reading its
// own namespace's Secrets.
func TestBuildSinks_CrossNamespaceSecretIsInvisible(t *testing.T) {
	// Seed a Secret with the right name+key, but in a DIFFERENT
	// namespace than the factory's. The factory MUST NOT read it.
	wrongNS := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "webhook-creds",
			Namespace: "attacker-namespace",
		},
		Data: map[string][]byte{"token": []byte("would-be-leaked")},
	}
	f := newClientFactory(t, wrongNS)
	specs := []aibomv1alpha1.SinkConfig{
		{
			Name: "audit-webhook",
			Type: aibomv1alpha1.SinkTypeWebhook,
			Webhook: &aibomv1alpha1.WebhookSinkSpec{
				Endpoint: "https://example.com/sink",
				Auth: &aibomv1alpha1.WebhookAuth{
					BearerToken: &aibomv1alpha1.BearerTokenAuth{
						SecretRef: aibomv1alpha1.SecretKeyRef{
							Name: "webhook-creds",
							Key:  "token",
						},
					},
				},
			},
		},
	}

	_, errs := f.BuildSinks(context.Background(), specs)
	if len(errs) != 1 {
		t.Fatalf("errs = %d, want 1: %+v", len(errs), errs)
	}
	got := errs[0]
	if !strings.Contains(got.Message, "not found") {
		t.Errorf("cross-namespace Secret should be invisible (not found); got: %q", got.Message)
	}
	// And critically: the attacker's namespace name MUST NOT appear
	// in the diagnostic. The factory has no business naming
	// foreign namespaces.
	if strings.Contains(got.Message, "attacker-namespace") {
		t.Errorf("diagnostic leaks foreign namespace name; got: %q", got.Message)
	}
}

// ---------- Happy paths ----------

// TestBuildSinks_HappyPath_Webhook_Bearer verifies that a fully
// specified webhook sink with a valid bearer-token Secret builds a
// real sink.Sink with no errors.
func TestBuildSinks_HappyPath_Webhook_Bearer(t *testing.T) {
	sec := secretWith("webhook-creds", map[string][]byte{
		"token": []byte("secret-bearer-value"),
	})
	f := newClientFactory(t, sec)
	specs := []aibomv1alpha1.SinkConfig{
		{
			Name: "audit-webhook",
			Type: aibomv1alpha1.SinkTypeWebhook,
			Webhook: &aibomv1alpha1.WebhookSinkSpec{
				Endpoint: "https://example.com/sink",
				Auth: &aibomv1alpha1.WebhookAuth{
					BearerToken: &aibomv1alpha1.BearerTokenAuth{
						SecretRef: aibomv1alpha1.SecretKeyRef{
							Name: "webhook-creds",
							Key:  "token",
						},
					},
				},
			},
		},
	}

	sinks, errs := f.BuildSinks(context.Background(), specs)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %+v", errs)
	}
	if len(sinks) != 1 {
		t.Fatalf("sinks = %d, want 1", len(sinks))
	}
	if sinks[0].Name() != "webhook" {
		t.Errorf("sink Name = %q, want \"webhook\"", sinks[0].Name())
	}
}

// TestBuildSinks_HappyPath_Webhook_NoAuth verifies the no-auth path
// (development / private-network deployments). No Secret lookup is
// performed; the factory should build the sink with empty auth
// config.
func TestBuildSinks_HappyPath_Webhook_NoAuth(t *testing.T) {
	f := newClientFactory(t)
	specs := []aibomv1alpha1.SinkConfig{
		{
			Name: "internal-webhook",
			Type: aibomv1alpha1.SinkTypeWebhook,
			Webhook: &aibomv1alpha1.WebhookSinkSpec{
				Endpoint: "https://internal.example.com/sink",
			},
		},
	}
	sinks, errs := f.BuildSinks(context.Background(), specs)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %+v", errs)
	}
	if len(sinks) != 1 {
		t.Fatalf("sinks = %d, want 1", len(sinks))
	}
}

// GCS happy-path construction is not exercised here: storage.NewClient
// performs ADC discovery during construction and fails under unit-test
// conditions where no Google credentials are configured. The GCS
// dispatch path through buildGCSSink is verified by
// TestBuildSinks_GCSMissingCredentialsSecret (which proves
// readSecretKey is invoked for the credentials Secret) and by the
// existing internal/sink/gcs_integration_test.go suite.

// TestBuildSinks_PartialFailureProducesPartialResults verifies the
// factory's per-sink reporting contract: when 3 sinks are specified
// and one fails, the factory returns 2 built sinks + 1 LoadError.
// The loader's all-or-nothing rule decides whether to USE this
// partial result — the factory's contract is faithful reporting.
func TestBuildSinks_PartialFailureProducesPartialResults(t *testing.T) {
	goodSec := secretWith("good-creds", map[string][]byte{"token": []byte("ok")})
	f := newClientFactory(t, goodSec)
	specs := []aibomv1alpha1.SinkConfig{
		{
			Name: "good-bearer",
			Type: aibomv1alpha1.SinkTypeWebhook,
			Webhook: &aibomv1alpha1.WebhookSinkSpec{
				Endpoint: "https://a.example.com",
				Auth: &aibomv1alpha1.WebhookAuth{
					BearerToken: &aibomv1alpha1.BearerTokenAuth{
						SecretRef: aibomv1alpha1.SecretKeyRef{Name: "good-creds", Key: "token"},
					},
				},
			},
		},
		{
			Name: "bad-bearer",
			Type: aibomv1alpha1.SinkTypeWebhook,
			Webhook: &aibomv1alpha1.WebhookSinkSpec{
				Endpoint: "https://b.example.com",
				Auth: &aibomv1alpha1.WebhookAuth{
					BearerToken: &aibomv1alpha1.BearerTokenAuth{
						SecretRef: aibomv1alpha1.SecretKeyRef{Name: "missing-creds", Key: "token"},
					},
				},
			},
		},
		{
			Name: "good-noauth",
			Type: aibomv1alpha1.SinkTypeWebhook,
			Webhook: &aibomv1alpha1.WebhookSinkSpec{
				Endpoint: "https://c.example.com",
			},
		},
	}
	sinks, errs := f.BuildSinks(context.Background(), specs)
	if len(sinks) != 2 {
		t.Errorf("sinks = %d, want 2 (good-bearer + good-noauth)", len(sinks))
	}
	if len(errs) != 1 {
		t.Fatalf("errs = %d, want 1 (bad-bearer): %+v", len(errs), errs)
	}
	if !strings.Contains(errs[0].Message, `"bad-bearer"`) {
		t.Errorf("error should name the failing sink; got: %q", errs[0].Message)
	}
}
