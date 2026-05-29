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
	"strings"
	"testing"
	"time"
)

// These tests cover the parts of GCSSink that don't require a real GCS
// endpoint: config validation, path-template rendering, and the public
// invariants the sink upholds even before reaching the network. The
// integration tests in gcs_integration_test.go exercise the actual
// network write against a real bucket and are gated by the "integration"
// build tag.

func TestGCSSinkConfig_Validate(t *testing.T) {
	cases := []struct {
		name    string
		cfg     GCSSinkConfig
		wantErr bool
	}{
		{"valid minimal", GCSSinkConfig{Bucket: "my-bucket"}, false},
		{"valid with template", GCSSinkConfig{Bucket: "b", PathTemplate: "foo/{name}.json"}, false},
		{"missing bucket", GCSSinkConfig{}, true},
		{"empty bucket", GCSSinkConfig{Bucket: ""}, true},
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

func TestRenderGCSPath_DefaultTemplate(t *testing.T) {
	meta := SinkMeta{
		WorkloadKind:      "Deployment",
		WorkloadNamespace: "prod-inference",
		WorkloadName:      "vllm-llama3",
		WorkloadCategory:  "inference",
		BOMHash:           "abcdef0123456789ffffffffffffffffffffffffffffffffffffffffffffffff",
		Timestamp:         time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC),
	}
	got := renderGCSPath(DefaultGCSPathTemplate, meta)
	want := "mlbom/prod-inference/deployment-vllm-llama3/2026-05-10T12-00-00.000Z.json"
	if got != want {
		t.Errorf("renderGCSPath() = %q, want %q", got, want)
	}
}

func TestRenderGCSPath_CustomTemplate(t *testing.T) {
	meta := SinkMeta{
		WorkloadKind:      "Deployment",
		WorkloadNamespace: "ns1",
		WorkloadName:      "x",
		WorkloadCategory:  "inference",
		BOMHash:           "0123456789abcdef" + strings.Repeat("0", 48),
		Timestamp:         time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC),
	}
	cases := []struct {
		template string
		want     string
	}{
		{"{namespace}/{name}.json", "ns1/x.json"},
		{"{category}/{kind}/{name}-{hash}.json", "inference/deployment/x-0123456789ab.json"},
		{"static/path/no/placeholders.json", "static/path/no/placeholders.json"},
		{"{name}-{timestamp}", "x-2026-05-10T12-00-00.000Z"},
	}
	for _, tc := range cases {
		t.Run(tc.template, func(t *testing.T) {
			if got := renderGCSPath(tc.template, meta); got != tc.want {
				t.Errorf("renderGCSPath(%q) = %q, want %q", tc.template, got, tc.want)
			}
		})
	}
}

// TestRenderGCSPath_NoColonsInOutput pins one of the FR4.4 immutability
// path properties: the rendered object name MUST NOT contain colons (which
// are unsafe across some object stores and CLI tools). RFC3339 timestamps
// natively contain colons; the renderer strips them.
func TestRenderGCSPath_NoColonsInOutput(t *testing.T) {
	meta := SinkMeta{
		WorkloadKind:      "Deployment",
		WorkloadNamespace: "ns",
		WorkloadName:      "x",
		Timestamp:         time.Date(2026, 5, 10, 12, 34, 56, 789, time.UTC),
	}
	got := renderGCSPath(DefaultGCSPathTemplate, meta)
	if strings.Contains(got, ":") {
		t.Errorf("rendered path contains colon: %q", got)
	}
}

// TestRenderGCSPath_TimestampIsUTC pins that the timestamp in the
// rendered path is UTC regardless of the caller's location.
func TestRenderGCSPath_TimestampIsUTC(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("timezone data unavailable")
	}
	meta := SinkMeta{
		WorkloadKind:      "Deployment",
		WorkloadNamespace: "ns",
		WorkloadName:      "x",
		// 2026-05-10 12:00:00 in NY (-04:00) = 16:00:00 UTC
		Timestamp: time.Date(2026, 5, 10, 12, 0, 0, 0, loc),
	}
	got := renderGCSPath(DefaultGCSPathTemplate, meta)
	if !strings.Contains(got, "2026-05-10T16-00-00.000Z") {
		t.Errorf("rendered path %q does not contain expected UTC timestamp", got)
	}
}

func TestGCSSink_Name(t *testing.T) {
	s := &GCSSink{}
	if got := s.Name(); got != "gcs" {
		t.Errorf("Name() = %q, want %q", got, "gcs")
	}
}

func TestGCSSink_HealthCheck_AlwaysNil(t *testing.T) {
	// HealthCheck deliberately returns nil to avoid requiring a read
	// permission beyond the sole-writer model's storage.objectCreator.
	// Document the choice; lock it with a test.
	s := &GCSSink{}
	if err := s.HealthCheck(nil); err != nil {
		t.Errorf("HealthCheck = %v, want nil (sole-writer model)", err)
	}
}

func TestGCSSink_WriteOnly_False(t *testing.T) {
	// GCS gs:// URLs are retrievable; this MUST report false so the
	// StatusBuilder prefers GCS over webhook for the ExternalBOMRef.
	s := &GCSSink{}
	if s.WriteOnly() {
		t.Error("GCSSink.WriteOnly() = true, want false (gs:// URLs are retrievable)")
	}
}
