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

import "strings"

// Namespace is the top-level namespace registered for k8s-aibom in the
// CycloneDX Property Taxonomy. Every property the builder emits is named
// under it, using the taxonomy's colon-delimited grammar; the full list
// is docs/cyclonedx-property-taxonomy.md.
const Namespace = "k8s-aibom"

// Emitted property names that other packages read back from a finished
// document (the controller's summary builder, the kubectl plugin).
// Scrapers keep their internal dotted keys; the builder maps every key
// through PropertyName at emission, so these constants are the only
// place the emitted spelling is written down in Go.
const (
	PropConfidence        = Namespace + ":confidence"
	PropEvidenceSource    = Namespace + ":evidence:source"
	PropEvidenceLocator   = Namespace + ":evidence:locator"
	PropRedactionApplied  = Namespace + ":redaction:applied"
	PropTruncationApplied = Namespace + ":truncation:applied"
	PropRuntimeName       = Namespace + ":runtime:name"
	PropImageReference    = Namespace + ":image:reference"
	PropSigningStatus     = Namespace + ":security:signing:status"
	PropWorkloadCategory  = Namespace + ":workload:category"
)

const (
	legacyAibomPrefix     = "aibom."
	legacySignaturePrefix = "signature."
	dynamoGraphPrefix     = "dynamo.component."
	dynamoGraphSuffix     = ".type"
)

// PropertyName maps a scraper-layer property key to the name emitted in
// the CycloneDX document. The scraper layer is CycloneDX-agnostic and
// keeps the dotted convention it has always used; this function is the
// single place that convention meets the registered taxonomy:
//
//	aibom.workload.kind        -> k8s-aibom:workload:kind
//	aibom.scrape.0.scraperName -> k8s-aibom:scrape:0:scraperName
//	signature.status           -> k8s-aibom:security:signing:status
//	runtime.name               -> k8s-aibom:runtime:name
//	dynamo.component.<name>.type -> k8s-aibom:dynamo:component:<name>:type
//
// The Dynamo graph-component form is the one key whose middle segment is
// owner-written text; it is kept verbatim (dots included) rather than
// split. A key already under the namespace is returned unchanged, so
// the mapping is idempotent.
func PropertyName(key string) string {
	switch {
	case key == "":
		return ""
	case strings.HasPrefix(key, Namespace+":"):
		return key
	case strings.HasPrefix(key, legacyAibomPrefix):
		return Namespace + ":" + dotsToColons(strings.TrimPrefix(key, legacyAibomPrefix))
	case strings.HasPrefix(key, legacySignaturePrefix):
		return Namespace + ":security:signing:" + dotsToColons(strings.TrimPrefix(key, legacySignaturePrefix))
	case strings.HasPrefix(key, dynamoGraphPrefix) && strings.HasSuffix(key, dynamoGraphSuffix):
		middle := strings.TrimSuffix(strings.TrimPrefix(key, dynamoGraphPrefix), dynamoGraphSuffix)
		if middle != "" && middle != "name" && middle != "type" {
			return Namespace + ":dynamo:component:" + middle + ":type"
		}
	}
	return Namespace + ":" + dotsToColons(key)
}

func dotsToColons(s string) string {
	return strings.ReplaceAll(s, ".", ":")
}
