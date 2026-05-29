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

package scraper

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeScraper is a minimal Scraper used to lock the interface shape in this
// package's tests and to serve as the reference implementation pattern for
// future scraper contributors. It mirrors the fakeSink pattern in
// internal/sink/sink_test.go.
//
// Concrete v1+ scrapers (InferenceSpecScraper, etc.) follow this shape:
//   - Hold a stable name string (returned by Name()).
//   - Hold a list of kinds the scraper handles (consulted by HandlesKind).
//   - Optionally hold dependencies (signature verifier, config, K8s client).
//   - Scrape produces a *BOMInputs whose ScraperName matches Name(); on
//     non-fatal extraction failures, errors go in BOMInputs.Errors and
//     Scrape returns nil error.
type fakeScraper struct {
	name   string
	kinds  []WorkloadKind
	inputs *BOMInputs
	err    error
}

func (f *fakeScraper) Name() string { return f.name }

func (f *fakeScraper) HandlesKind(k WorkloadKind) bool {
	for _, kk := range f.kinds {
		if kk == k {
			return true
		}
	}
	return false
}

func (f *fakeScraper) Scrape(_ context.Context, _ Workload, _ *InferenceConfig) (*BOMInputs, error) {
	return f.inputs, f.err
}

// Compile-time check: fakeScraper satisfies Scraper. If the Scraper
// interface gains or loses a method, this assertion fails to compile and
// forces the fake (and the contributor contract) to be updated.
var _ Scraper = (*fakeScraper)(nil)

func TestScraperInterfaceShape_HappyPath(t *testing.T) {
	want := &BOMInputs{
		ScraperName:     "inference.spec",
		ScrapeTimestamp: time.Unix(1700000000, 0).UTC(),
		Confidence:      ConfidenceDeclared,
		Components: []Component{
			{Type: ComponentContainer, Name: "vllm", Version: "v0.6.3", Confidence: ConfidenceDeclared,
				Evidence: Evidence{Source: SourceContainerArg, Locator: "spec.template.spec.containers[0].image"}},
		},
	}
	var s Scraper = &fakeScraper{
		name:   "inference.spec",
		kinds:  []WorkloadKind{{Group: "apps", Version: "v1", Kind: "Deployment"}},
		inputs: want,
	}
	if got := s.Name(); got != "inference.spec" {
		t.Errorf("Name() = %q, want %q", got, "inference.spec")
	}
	if !s.HandlesKind(WorkloadKind{Group: "apps", Version: "v1", Kind: "Deployment"}) {
		t.Error("HandlesKind: expected match on apps/v1/Deployment")
	}
	if s.HandlesKind(WorkloadKind{Group: "apps", Version: "v1", Kind: "StatefulSet"}) {
		t.Error("HandlesKind: expected no match on apps/v1/StatefulSet")
	}
	got, err := s.Scrape(context.Background(), Workload{}, nil)
	if err != nil {
		t.Fatalf("Scrape returned err: %v", err)
	}
	if got != want {
		t.Errorf("Scrape returned %p, want %p", got, want)
	}
}

func TestScraperInterfaceShape_ErrorPath(t *testing.T) {
	sentinel := errors.New("scrape failed")
	var s Scraper = &fakeScraper{name: "fake", err: sentinel}
	_, err := s.Scrape(context.Background(), Workload{}, nil)
	if !errors.Is(err, sentinel) {
		t.Errorf("Scrape returned %v, want %v", err, sentinel)
	}
}

func TestScraperInterfaceShape_ZeroHandlesKind(t *testing.T) {
	// A scraper with no registered kinds must reject every kind. This is
	// the fail-closed default — accidental misconfiguration produces a
	// no-op scraper rather than one that silently scrapes everything.
	var s Scraper = &fakeScraper{name: "empty"}
	if s.HandlesKind(WorkloadKind{Group: "apps", Version: "v1", Kind: "Deployment"}) {
		t.Error("empty fakeScraper unexpectedly handles apps/v1/Deployment")
	}
	if s.HandlesKind(WorkloadKind{}) {
		t.Error("empty fakeScraper unexpectedly handles zero kind")
	}
}
