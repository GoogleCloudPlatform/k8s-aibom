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

package bom

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/GoogleCloudPlatform/k8s-aibom/internal/scraper"
)

// emittedSerial is the part of the emitted JSON a BOM-Link resolves
// against: urn:cdx:<serialNumber>/<version>#<bom-ref>.
type emittedSerial struct {
	SerialNumber *string `json:"serialNumber"`
	Version      int     `json:"version"`
}

func buildSerial(t *testing.T, b *Builder, inputs *scraper.BOMInputs, opts BuildOptions) (emittedSerial, []byte) {
	t.Helper()
	doc, err := b.Build(inputs, opts)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	assertValidCycloneDX(t, doc.JSON)
	var s emittedSerial
	if err := json.Unmarshal(doc.JSON, &s); err != nil {
		t.Fatalf("unmarshal emitted BOM: %v", err)
	}
	return s, doc.JSON
}

// TestSerialNamespace_IsDerivedFromTheProjectURL pins the namespace to the
// rule docs/schema-divergences.md gives, so a peer project can compute it.
func TestSerialNamespace_IsDerivedFromTheProjectURL(t *testing.T) {
	want := uuid.NewSHA1(uuid.NameSpaceURL, []byte("https://github.com/GoogleCloudPlatform/k8s-aibom"))
	if serialNamespace != want {
		t.Fatalf("serialNamespace = %s, want %s", serialNamespace, want)
	}
}

// TestBuild_SerialNumberIsUUIDv5OfTheWorkloadUID checks the emitted
// serialNumber: a urn:uuid holding the version 5 UUID of the workload UID
// in serialNamespace, with the document version left at 1.
func TestBuild_SerialNumberIsUUIDv5OfTheWorkloadUID(t *testing.T) {
	opts := testBuildOptions()
	s, _ := buildSerial(t, newTestBuilder(), vllmFixtureBOMInputs(), opts)
	if s.SerialNumber == nil {
		t.Fatal("serialNumber missing from the document")
	}
	want := "urn:uuid:" + uuid.NewSHA1(serialNamespace, []byte(opts.WorkloadUID)).String()
	if *s.SerialNumber != want {
		t.Errorf("serialNumber = %q, want %q", *s.SerialNumber, want)
	}
	u, err := uuid.Parse(*s.SerialNumber)
	if err != nil {
		t.Fatalf("serialNumber does not parse as a UUID: %v", err)
	}
	if u.Version() != 5 {
		t.Errorf("serialNumber UUID version = %d, want 5", u.Version())
	}
	if s.Version != 1 {
		t.Errorf("version = %d, want 1", s.Version)
	}
}

// TestBuild_SerialNumberChangesWithTheWorkloadUIDAndNothingElse builds the
// document twice with identical inputs (byte-identical, same serial), then
// changes everything but the UID (same serial), then only the UID (new
// serial).
func TestBuild_SerialNumberChangesWithTheWorkloadUIDAndNothingElse(t *testing.T) {
	opts := testBuildOptions()
	first, firstJSON := buildSerial(t, newTestBuilder(), vllmFixtureBOMInputs(), opts)
	second, secondJSON := buildSerial(t, newTestBuilder(), vllmFixtureBOMInputs(), opts)
	if string(firstJSON) != string(secondJSON) {
		t.Fatal("identical inputs produced different bytes")
	}
	if first.SerialNumber == nil || second.SerialNumber == nil {
		t.Fatal("serialNumber missing from the document")
	}
	if *first.SerialNumber != *second.SerialNumber {
		t.Fatalf("serialNumber differs across builds: %q vs %q", *first.SerialNumber, *second.SerialNumber)
	}

	// Everything but the UID changes: name, namespace, controller version,
	// roll-up, components and clock.
	other := opts
	other.WorkloadName = "vllm-llama3-renamed"
	other.WorkloadNamespace = "other-ns"
	other.ControllerVersion = "9.9.9"
	other.OwnedWorkloads = []string{"Deployment/extra"}
	inputs := vllmFixtureBOMInputs()
	inputs.Components = inputs.Components[:1]
	later := NewBuilder().WithClock(func() time.Time { return fixedBuildTime.Add(time.Hour) })
	same, _ := buildSerial(t, later, inputs, other)
	if same.SerialNumber == nil || *same.SerialNumber != *first.SerialNumber {
		t.Errorf("serialNumber changed without a UID change: %v vs %q", same.SerialNumber, *first.SerialNumber)
	}

	// Only the UID changes: a deleted and recreated workload is a new subject.
	recreated := opts
	recreated.WorkloadUID = "def-456"
	changed, _ := buildSerial(t, newTestBuilder(), vllmFixtureBOMInputs(), recreated)
	if changed.SerialNumber == nil || *changed.SerialNumber == *first.SerialNumber {
		t.Errorf("serialNumber did not change with the UID: %v", changed.SerialNumber)
	}
}

// TestBuild_NoSerialNumberWithoutAWorkloadUID checks that a document whose
// workload UID is unknown carries no serialNumber, instead of one serial
// shared by every such document.
func TestBuild_NoSerialNumberWithoutAWorkloadUID(t *testing.T) {
	opts := testBuildOptions()
	opts.WorkloadUID = ""
	s, _ := buildSerial(t, newTestBuilder(), vllmFixtureBOMInputs(), opts)
	if s.SerialNumber != nil {
		t.Errorf("serialNumber = %q, want the field omitted", *s.SerialNumber)
	}
}
