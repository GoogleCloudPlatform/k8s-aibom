//go:build integration

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

// Package sink integration tests. These tests run only when the
// "integration" build tag is set:
//
//	go test -tags=integration ./internal/sink/
//
// They additionally require a real GCS bucket to write to. Set the
// K8S_AIBOM_TEST_GCS_BUCKET env var to enable. Authentication is via
// the standard ADC mechanisms (gcloud auth application-default login,
// or GOOGLE_APPLICATION_CREDENTIALS pointing at a key file).
//
// The bucket should be a personal/dev bucket — each test run writes
// a small number of objects and does NOT clean them up. A lifecycle
// rule on the bucket (e.g., delete after 30 days) is appropriate.

package sink

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/k8s-aibom/internal/bom"
)

const testBucketEnvVar = "K8S_AIBOM_TEST_GCS_BUCKET"

// integrationBucket returns the GCS bucket for integration tests or
// skips the test if the env var is unset.
func integrationBucket(t *testing.T) string {
	t.Helper()
	bucket := os.Getenv(testBucketEnvVar)
	if bucket == "" {
		t.Skipf("%s not set; skipping integration test", testBucketEnvVar)
	}
	return bucket
}

func newTestDoc() *bom.Document {
	return &bom.Document{
		Format:  bom.FormatCycloneDX,
		Version: "1.6",
		JSON:    []byte(`{"bomFormat":"CycloneDX","specVersion":"1.6","version":1}`),
		SHA256:  "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}
}

func testMeta() SinkMeta {
	return SinkMeta{
		WorkloadKind:      "Deployment",
		WorkloadNamespace: "k8s-aibom-itest",
		WorkloadName:      "vllm-itest",
		WorkloadCategory:  "inference",
		BOMHash:           "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Timestamp:         time.Now().UTC(),
	}
}

func TestIntegration_GCSSink_HappyPath(t *testing.T) {
	bucket := integrationBucket(t)
	ctx := context.Background()
	s, err := NewGCSSink(ctx, GCSSinkConfig{Bucket: bucket})
	if err != nil {
		t.Fatalf("NewGCSSink: %v", err)
	}
	url, err := s.Emit(ctx, newTestDoc(), testMeta())
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if !strings.HasPrefix(url, "gs://"+bucket+"/") {
		t.Errorf("URL = %q, expected gs://%s/ prefix", url, bucket)
	}
	if !strings.HasSuffix(url, ".json") {
		t.Errorf("URL = %q, expected .json suffix", url)
	}
	t.Logf("wrote %s", url)
}

func TestIntegration_GCSSink_BucketDoesNotExist(t *testing.T) {
	_ = integrationBucket(t) // skip if not in integration mode
	ctx := context.Background()
	s, err := NewGCSSink(ctx, GCSSinkConfig{
		Bucket: "k8s-aibom-this-bucket-does-not-exist-zzzzzzz-12345",
	})
	if err != nil {
		// Construction may fail at client-init time or may defer to
		// first call; either is acceptable provided the error is clear.
		if !strings.Contains(err.Error(), "gcs sink") {
			t.Errorf("error should be prefixed with 'gcs sink': %v", err)
		}
		return
	}
	_, err = s.Emit(ctx, newTestDoc(), testMeta())
	if err == nil {
		t.Fatal("expected error writing to nonexistent bucket")
	}
	// Error must clearly identify the bucket so an admin can recover.
	if !strings.Contains(err.Error(), "k8s-aibom-this-bucket-does-not-exist") {
		t.Errorf("error should name the offending bucket; got: %v", err)
	}
	// Error must not leak credentials (no PEM markers, no oauth/access tokens).
	for _, leak := range []string{
		"BEGIN PRIVATE KEY", "BEGIN RSA", "ya29.",
	} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("error message appears to leak credential material (contains %q): %v", leak, err)
		}
	}
}

func TestIntegration_GCSSink_ImmutabilityPrecondition(t *testing.T) {
	bucket := integrationBucket(t)
	ctx := context.Background()
	s, err := NewGCSSink(ctx, GCSSinkConfig{Bucket: bucket})
	if err != nil {
		t.Fatal(err)
	}
	meta := testMeta()
	// Pin the timestamp to make the object name identical across both
	// writes — exercises the DoesNotExist precondition.
	meta.Timestamp = time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	// Stable hash for stable path.
	meta.BOMHash = "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	meta.WorkloadName = "vllm-itest-immutability"

	// First write should succeed.
	if _, err := s.Emit(ctx, newTestDoc(), meta); err != nil {
		t.Fatalf("first write: %v", err)
	}
	// Second write at the same path MUST fail (DoesNotExist precondition).
	_, err = s.Emit(ctx, newTestDoc(), meta)
	if err == nil {
		t.Fatal("expected DoesNotExist precondition failure on second write")
	}
	t.Logf("second write correctly rejected: %v", err)
}
