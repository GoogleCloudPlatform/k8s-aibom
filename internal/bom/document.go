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

// Package bom is the internal representation of a generated BOM document
// as it flows from the BOMBuilder to the Sinks. Sinks read whichever form
// they need (JSON bytes for GCS/webhook; the parsed structure for the
// CRD-status sink's summary fields).
package bom

import cdx "github.com/CycloneDX/cyclonedx-go"

// Format is a stable enum-style label for the document's encoding. v1
// emits only FormatCycloneDX.
type Format string

const (
	FormatCycloneDX Format = "CycloneDX"
)

// CycloneDXSpecVersion is the wire-format spec version this controller
// emits. Captured as a package constant so the version is stamped in one
// place and the tests + builder agree.
const CycloneDXSpecVersion = "1.6"

// Document is the controller-internal representation of a finished BOM.
//
// JSON is the canonical, deterministic serialization of the BOM (sorted
// keys where the format permits, fixed timestamp placement under explicit
// builder control). SHA256 is the hex-encoded sha256 of JSON. Format and
// Version identify the encoding (e.g., "CycloneDX" / "1.6"). CDX is the
// parsed structured representation, for sinks that need to introspect the
// BOM without re-parsing the JSON (notably the CRD-status sink's summary
// builder).
//
// Invariant: JSON is the canonical serialization of CDX. Sinks MUST NOT
// mutate CDX in place; that would invalidate JSON and SHA256.
type Document struct {
	Format  Format
	Version string
	JSON    []byte
	SHA256  string
	CDX     *cdx.BOM
}

// Size returns the byte length of the serialized BOM. The reconciler uses
// this against AIBOMControllerConfig.bomGeneration.inlineThresholdBytes to
// decide between the inline CR-status path and the external sink path.
func (d *Document) Size() int {
	if d == nil {
		return 0
	}
	return len(d.JSON)
}
