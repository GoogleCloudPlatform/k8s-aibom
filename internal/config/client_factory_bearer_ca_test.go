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
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	aibomv1beta1 "github.com/GoogleCloudPlatform/k8s-aibom/api/v1beta1"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/bom"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/sink"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestBuildSinks_Webhook_BearerCASecret(t *testing.T) {
	const dummyToken = "BEARER_TOKEN_CONTENT_SENTINEL_96"
	const invalidCAPEM = "INVALID_CA_CONTENT_SENTINEL_96"
	t.Setenv("AIBOM_DISABLE_SSRF_CHECKS", "true")
	authorization := make(chan string, 1)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization <- r.Header.Get("Authorization")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	ref := &aibomv1beta1.SecretKeyRef{Name: "webhook-ca", Key: "ca.crt"}
	foreign := secretWith(ref.Name, map[string][]byte{ref.Key: caPEM})
	foreign.Namespace = "workload-namespace"
	field := "spec.sinks[name=audit-webhook].webhook.auth.bearerToken.caSecretRef"

	for _, tc := range []struct {
		name      string
		ref       *aibomv1beta1.SecretKeyRef
		secrets   []client.Object
		wantField string
		wantText  []string
		deliver   bool
	}{
		{name: "unset"},
		{name: "set", ref: ref, secrets: []client.Object{secretWith(ref.Name, map[string][]byte{ref.Key: caPEM})}, deliver: true},
		{name: "missing secret", ref: ref, wantField: field, wantText: []string{"webhook-ca", "not found", testControllerNamespace, "Create the Secret"}},
		{name: "missing key", ref: ref, secrets: []client.Object{secretWith(ref.Name, map[string][]byte{"other": caPEM})}, wantField: field + ".key", wantText: []string{"webhook-ca", "ca.crt", "Available keys: [other]", "Update"}},
		{name: "empty value", ref: ref, secrets: []client.Object{secretWith(ref.Name, map[string][]byte{ref.Key: {}})}, wantField: field, wantText: []string{"webhook-ca", "ca.crt", "empty value", "Populate"}},
		{name: "other namespace", ref: ref, secrets: []client.Object{foreign}, wantField: field, wantText: []string{"webhook-ca", "not found", testControllerNamespace}},
		{name: "invalid PEM", ref: ref, secrets: []client.Object{secretWith(ref.Name, map[string][]byte{ref.Key: []byte(invalidCAPEM)})}, wantField: "spec.sinks[name=audit-webhook].webhook", wantText: []string{"failed to construct", "failed to parse CA certs"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			objects := append([]client.Object{secretWith("webhook-token", map[string][]byte{"token": []byte(dummyToken)})}, tc.secrets...)
			factory := newClientFactory(t, objects...)
			spec := aibomv1beta1.SinkConfig{
				Name: "audit-webhook",
				Type: aibomv1beta1.SinkTypeWebhook,
				Webhook: &aibomv1beta1.WebhookSinkSpec{
					Endpoint: srv.URL,
					Auth: &aibomv1beta1.WebhookAuth{BearerToken: &aibomv1beta1.BearerTokenAuth{
						SecretRef:   aibomv1beta1.SecretKeyRef{Name: "webhook-token", Key: "token"},
						CASecretRef: tc.ref,
					}},
				},
			}
			sinks, errs := factory.BuildSinks(context.Background(), []aibomv1beta1.SinkConfig{spec})
			if tc.wantField != "" {
				if len(sinks) != 0 || len(errs) != 1 {
					t.Fatalf("want one LoadError and no sinks; sinks=%d errors=%+v", len(sinks), errs)
				}
				if errs[0].Field != tc.wantField {
					t.Errorf("Field = %q, want %q", errs[0].Field, tc.wantField)
				}
				for _, secretValue := range []string{invalidCAPEM, dummyToken} {
					if strings.Contains(errs[0].Error(), secretValue) {
						t.Error("returned error exposes Secret contents")
					}
				}
				for _, text := range append([]string{"audit-webhook", "Type=Webhook"}, tc.wantText...) {
					if !strings.Contains(errs[0].Message, text) {
						t.Errorf("Message missing %q: %s", text, errs[0].Message)
					}
				}
				return
			}
			if len(sinks) != 1 || len(errs) != 0 {
				t.Fatalf("want one sink and no errors; sinks=%d errors=%+v", len(sinks), errs)
			}
			if tc.deliver {
				doc := &bom.Document{JSON: []byte(`{"test":"bearer-ca"}`), SHA256: "test-hash"}
				if _, err := sinks[0].Emit(context.Background(), doc, sink.SinkMeta{}); err != nil {
					t.Fatalf("Emit using CA and token from Secrets: %v", err)
				}
				if got := <-authorization; got != "Bearer "+dummyToken {
					t.Errorf("Authorization = %q", got)
				}
			}
		})
	}
}
