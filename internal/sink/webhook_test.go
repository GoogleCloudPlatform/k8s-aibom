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

package sink

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/k8s-aibom/internal/bom"
)

// All WebhookSink tests run against a local httptest.Server — no
// external dependencies. The webhook protocol's wire shape is
// part of the customer-facing contract documented in
// docs/webhook-sink-protocol.md; tests in this file LOCK that shape.

func newTestWebhookDoc() *bom.Document {
	return &bom.Document{
		Format:  bom.FormatCycloneDX,
		Version: "1.6",
		JSON:    []byte(`{"bomFormat":"CycloneDX","specVersion":"1.6","version":1}`),
		SHA256:  "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}
}

func newTestWebhookMeta() SinkMeta {
	return SinkMeta{
		WorkloadKind:      "Deployment",
		WorkloadNamespace: "prod",
		WorkloadName:      "vllm",
		WorkloadCategory:  "inference",
		BOMHash:           "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Timestamp:         time.Now(),
	}
}

func TestWebhookSinkConfig_Validate(t *testing.T) {
	cases := []struct {
		name    string
		cfg     WebhookSinkConfig
		wantErr bool
	}{
		{"valid https", WebhookSinkConfig{Endpoint: "https://example.com/sink"}, false},
		{"valid http", WebhookSinkConfig{Endpoint: "http://example.com/sink"}, false},
		{"empty endpoint", WebhookSinkConfig{}, true},
		{"cert without key", WebhookSinkConfig{
			Endpoint: "https://example.com/sink", ClientCertFile: "/x",
		}, true},
		{"key without cert", WebhookSinkConfig{
			Endpoint: "https://example.com/sink", ClientKeyFile: "/x",
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if (err != nil) != tc.wantErr {
				t.Errorf("Validate() err=%v, wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

func TestWebhookSink_HappyPath_SetsAllProtocolHeaders(t *testing.T) {
	// Captures the request the sink makes and asserts every documented
	// header is present with the expected value.
	var receivedReq *http.Request
	var receivedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedReq = r.Clone(r.Context())
		receivedBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	s, err := NewWebhookSink(WebhookSinkConfig{
		Endpoint:          srv.URL,
		ControllerVersion: "0.1.0-test",
	})
	if err != nil {
		t.Fatalf("NewWebhookSink: %v", err)
	}
	doc := newTestWebhookDoc()
	meta := newTestWebhookMeta()
	url, err := s.Emit(context.Background(), doc, meta)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if url != srv.URL {
		t.Errorf("returned URL = %q, want %q", url, srv.URL)
	}

	// Headers — these are the customer-facing protocol; if any of these
	// assertions need updating, the corresponding entry in
	// docs/webhook-sink-protocol.md also needs updating.
	cases := map[string]string{
		"Content-Type":             WebhookContentType,
		WebhookHeaderDocumentHash:  "sha256:" + doc.SHA256,
		WebhookHeaderWorkloadKind:  meta.WorkloadKind,
		WebhookHeaderWorkloadNS:    meta.WorkloadNamespace,
		WebhookHeaderWorkloadName:  meta.WorkloadName,
		WebhookHeaderCategory:      meta.WorkloadCategory,
		WebhookHeaderControllerVer: "0.1.0-test",
	}
	for h, want := range cases {
		if got := receivedReq.Header.Get(h); got != want {
			t.Errorf("header %s = %q, want %q", h, got, want)
		}
	}
	if !bytesEqual(receivedBody, doc.JSON) {
		t.Errorf("body mismatch.\ngot:  %s\nwant: %s", receivedBody, doc.JSON)
	}
}

func TestWebhookSink_BearerToken_AddsAuthorizationHeader(t *testing.T) {
	// Token is read from a file at construction; the file contents
	// become the bearer token. Token MUST appear in the Authorization
	// header on every request.
	const tokenValue = "deadbeef-test-token-not-real"
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenFile, []byte(tokenValue+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s, err := NewWebhookSink(WebhookSinkConfig{
		Endpoint:        srv.URL,
		BearerTokenFile: tokenFile,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Emit(context.Background(), newTestWebhookDoc(), newTestWebhookMeta()); err != nil {
		t.Fatal(err)
	}
	want := "Bearer " + tokenValue
	if gotAuth != want {
		t.Errorf("Authorization = %q, want %q", gotAuth, want)
	}
}

func TestWebhookSink_NoBearerToken_NoAuthorizationHeader(t *testing.T) {
	// When no bearer token is configured, no Authorization header is
	// sent. (Customers using mTLS or no-auth dev mode rely on this.)
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s, err := NewWebhookSink(WebhookSinkConfig{Endpoint: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Emit(context.Background(), newTestWebhookDoc(), newTestWebhookMeta()); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "" {
		t.Errorf("unexpected Authorization header: %q", gotAuth)
	}
}

func TestWebhookSink_EmptyBearerTokenFile_FailsConstruction(t *testing.T) {
	// A bearer token file that's empty is a config bug — the customer
	// thought they configured auth but didn't. Fail fast.
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "empty-token")
	if err := os.WriteFile(tokenFile, []byte("   \n  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := NewWebhookSink(WebhookSinkConfig{
		Endpoint:        "https://example.com/sink",
		BearerTokenFile: tokenFile,
	})
	if err == nil {
		t.Fatal("expected error for empty BearerTokenFile")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("error should mention empty file; got: %v", err)
	}
}

func TestWebhookSink_5xx_RetriesWithBackoff(t *testing.T) {
	// First two requests return 503; third returns 200. The sink must
	// succeed without surfacing the transient failures.
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&attempts, 1)
		if n < 3 {
			http.Error(w, "transient", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s, err := NewWebhookSink(WebhookSinkConfig{Endpoint: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	// Use the small-backoff override to keep the test fast.
	origBackoffs := WebhookRetryBackoffs
	WebhookRetryBackoffs = []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 40 * time.Millisecond}
	defer func() { WebhookRetryBackoffs = origBackoffs }()

	if _, err := s.Emit(context.Background(), newTestWebhookDoc(), newTestWebhookMeta()); err != nil {
		t.Errorf("Emit returned error despite eventual 200: %v", err)
	}
	if got := atomic.LoadInt32(&attempts); got != 3 {
		t.Errorf("attempts = %d, want 3 (two 5xx + one 200)", got)
	}
}

func TestWebhookSink_4xx_NoRetry(t *testing.T) {
	// 4xx is treated as a customer config problem. No retry; surface
	// the response body in the error message.
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("invalid bearer token"))
	}))
	defer srv.Close()

	s, err := NewWebhookSink(WebhookSinkConfig{Endpoint: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Emit(context.Background(), newTestWebhookDoc(), newTestWebhookMeta())
	if err == nil {
		t.Fatal("expected error on 401")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error should include status code 401; got: %v", err)
	}
	if !strings.Contains(err.Error(), "invalid bearer token") {
		t.Errorf("error should include response body; got: %v", err)
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Errorf("attempts = %d, want 1 (no retry on 4xx)", got)
	}
}

func TestWebhookSink_5xx_GivesUpAfterMaxRetries(t *testing.T) {
	// Persistent 5xx eventually returns the last error to the caller.
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		http.Error(w, "still down", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	s, err := NewWebhookSink(WebhookSinkConfig{Endpoint: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	origBackoffs := WebhookRetryBackoffs
	WebhookRetryBackoffs = []time.Duration{1 * time.Millisecond, 1 * time.Millisecond, 1 * time.Millisecond}
	defer func() { WebhookRetryBackoffs = origBackoffs }()

	_, err = s.Emit(context.Background(), newTestWebhookDoc(), newTestWebhookMeta())
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("error should reference HTTP 503; got: %v", err)
	}
	if got := atomic.LoadInt32(&attempts); got != int32(len(WebhookRetryBackoffs)+1) {
		t.Errorf("attempts = %d, want %d", got, len(WebhookRetryBackoffs)+1)
	}
}

func TestWebhookSink_ErrorMessageRedaction(t *testing.T) {
	// Error message must NOT echo any request headers (notably
	// Authorization). It MAY include the response body. Long bodies
	// are truncated.
	const longBody = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(longBody))
	}))
	defer srv.Close()

	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "token")
	const secretValue = "secret-bearer-NEVER-IN-ERRORS"
	_ = os.WriteFile(tokenFile, []byte(secretValue), 0o600)

	s, err := NewWebhookSink(WebhookSinkConfig{
		Endpoint: srv.URL, BearerTokenFile: tokenFile,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Emit(context.Background(), newTestWebhookDoc(), newTestWebhookMeta())
	if err == nil {
		t.Fatal("expected error on 400")
	}
	msg := err.Error()
	if strings.Contains(msg, secretValue) {
		t.Errorf("error message leaked the bearer token: %q", msg)
	}
	if !strings.Contains(msg, "truncated") {
		t.Errorf("error message should indicate truncation for long body; got: %q", msg)
	}
}

func TestWebhookSink_ContextCanceled_AbortsRetryLoop(t *testing.T) {
	// Cancellation during a backoff sleep must abort the retry loop
	// promptly.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	s, err := NewWebhookSink(WebhookSinkConfig{Endpoint: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	origBackoffs := WebhookRetryBackoffs
	WebhookRetryBackoffs = []time.Duration{200 * time.Millisecond, 500 * time.Millisecond, 500 * time.Millisecond}
	defer func() { WebhookRetryBackoffs = origBackoffs }()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = s.Emit(ctx, newTestWebhookDoc(), newTestWebhookMeta())
	if err == nil {
		t.Fatal("expected error on canceled context")
	}
	if time.Since(start) > 1*time.Second {
		t.Errorf("emit did not honor cancellation promptly: took %v", time.Since(start))
	}
}

func TestWebhookSink_ConcurrentEmits_Safe(t *testing.T) {
	// Sinks may be invoked concurrently from multiple reconciles.
	// Verify no data races and no header bleeding between requests.
	var nilNamespaceCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(WebhookHeaderWorkloadNS) == "" {
			atomic.AddInt32(&nilNamespaceCount, 1)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s, err := NewWebhookSink(WebhookSinkConfig{Endpoint: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			meta := newTestWebhookMeta()
			meta.WorkloadNamespace = "ns-" + string(rune('a'+(i%26)))
			_, _ = s.Emit(context.Background(), newTestWebhookDoc(), meta)
		}(i)
	}
	wg.Wait()
	if got := atomic.LoadInt32(&nilNamespaceCount); got != 0 {
		t.Errorf("%d concurrent emits saw empty namespace header — possible header race", got)
	}
}

func TestWebhookSink_Name(t *testing.T) {
	s := &WebhookSink{}
	if got := s.Name(); got != "webhook" {
		t.Errorf("Name() = %q, want %q", got, "webhook")
	}
}

func TestWebhookSink_HealthCheck_AlwaysNil(t *testing.T) {
	s := &WebhookSink{}
	if err := s.HealthCheck(context.Background()); err != nil {
		t.Errorf("HealthCheck = %v, want nil", err)
	}
}

func TestWebhookSink_WriteOnly_True(t *testing.T) {
	// Webhook endpoints don't serve the BOM back. WriteOnly() MUST be
	// true so the StatusBuilder prefers retrievable sinks (GCS) when
	// both are configured and report this URL as informational-only
	// when webhook is the only successful sink.
	s := &WebhookSink{}
	if !s.WriteOnly() {
		t.Error("WebhookSink.WriteOnly() = false, want true (webhooks are write-only)")
	}
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestMain(m *testing.M) {
	_ = os.Setenv("AIBOM_DISABLE_SSRF_CHECKS", "true")
	os.Exit(m.Run())
}
