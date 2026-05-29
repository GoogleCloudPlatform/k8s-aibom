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
	"errors"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/k8s-aibom/internal/bom"
)

// fakeSink is a minimal Sink implementation used only to lock the
// interface shape in this package's tests. It also serves as a worked
// example of a no-op sink for future contributor reference.
type fakeSink struct {
	name      string
	url       string
	err       error
	writeOnly bool
}

func (f *fakeSink) Name() string { return f.name }
func (f *fakeSink) Emit(_ context.Context, _ *bom.Document, _ SinkMeta) (string, error) {
	return f.url, f.err
}
func (f *fakeSink) HealthCheck(_ context.Context) error { return f.err }
func (f *fakeSink) WriteOnly() bool                     { return f.writeOnly }

// Compile-time check: fakeSink satisfies Sink. If the Sink interface
// changes shape, this assertion will fail to compile and force the
// fakeSink update before tests can proceed.
var _ Sink = (*fakeSink)(nil)

func TestSinkInterfaceShape_HappyPath(t *testing.T) {
	var s Sink = &fakeSink{name: "fake", url: "gs://bucket/path.json"}
	if got := s.Name(); got != "fake" {
		t.Errorf("Name() = %q, want %q", got, "fake")
	}
	doc := &bom.Document{Format: bom.FormatCycloneDX, Version: "1.6"}
	meta := SinkMeta{
		WorkloadKind:      "Deployment",
		WorkloadNamespace: "prod",
		WorkloadName:      "vllm",
		WorkloadCategory:  "inference",
		BOMHash:           "sha256:deadbeef",
		Timestamp:         time.Now(),
	}
	url, err := s.Emit(context.Background(), doc, meta)
	if err != nil {
		t.Errorf("Emit returned error: %v", err)
	}
	if url != "gs://bucket/path.json" {
		t.Errorf("Emit returned URL %q, want %q", url, "gs://bucket/path.json")
	}
	if err := s.HealthCheck(context.Background()); err != nil {
		t.Errorf("HealthCheck returned error: %v", err)
	}
}

func TestSinkInterfaceShape_ErrorPath(t *testing.T) {
	sentinel := errors.New("boom")
	var s Sink = &fakeSink{name: "fake", err: sentinel}
	if _, err := s.Emit(context.Background(), &bom.Document{}, SinkMeta{}); !errors.Is(err, sentinel) {
		t.Errorf("Emit returned %v, want %v", err, sentinel)
	}
	if err := s.HealthCheck(context.Background()); !errors.Is(err, sentinel) {
		t.Errorf("HealthCheck returned %v, want %v", err, sentinel)
	}
}
