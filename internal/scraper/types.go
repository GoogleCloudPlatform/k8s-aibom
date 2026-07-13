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

// Package scraper contains the workload-type-neutral types and interfaces
// that the controller's discovery and extraction pipeline operate on. The
// types in this package are INTERNAL — they flow Scraper -> BOMBuilder ->
// Sink, never directly into a CR status field or BOM output. The deliberate
// API-facing types live in github.com/GoogleCloudPlatform/k8s-aibom/api/v1alpha1.
package scraper

import (
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// WorkloadKind identifies a Kubernetes resource kind (group/version/kind).
// It is the routing key the scraper pipeline uses to decide which scrapers
// handle which workload.
type WorkloadKind struct {
	Group   string
	Version string
	Kind    string
}

// WorkloadCategory is the high-level AI lifecycle role of a workload. v1
// always emits CategoryInference; v2+ scrapers populate the other values.
type WorkloadCategory string

const (
	CategoryInference  WorkloadCategory = "inference"
	CategoryAgent      WorkloadCategory = "agent"
	CategoryEvaluation WorkloadCategory = "evaluation"
	CategoryPipeline   WorkloadCategory = "pipeline"
	CategoryNotebook   WorkloadCategory = "notebook"
)

// Workload is the discovery-layer view of a single top-level Kubernetes
// object that the controller is tracking, together with the running pods
// owned by it. The Category is assigned by the discovery layer based on
// heuristic classification.
//
// Object holds the typed Kubernetes object (e.g., *appsv1.Deployment) when
// the kind is in the controller-runtime scheme, or an *unstructured.Unstructured
// for arbitrary CRDs. Scrapers type-assert based on Kind.
type Workload struct {
	Kind      WorkloadKind
	Category  WorkloadCategory
	Namespace string
	Name      string
	UID       types.UID
	Object    client.Object
	Pods      []corev1.Pod
}

// EvidenceSource is the canonical, closed-enumerable set of source kinds
// that a BOM attribute can be extracted from. Every value-bearing field in
// BOMInputs carries one of these so auditors can trace each attribute back
// to its source from the BOM output alone, without access to the cluster or
// the controller logs.
type EvidenceSource string

const (
	// SourcePodAnnotation identifies values extracted from a live pod's
	// metadata.annotations (the pod-instance annotations, as opposed to
	// the pod-template annotations on the workload). v1 does not currently
	// emit this; v2 may use it for mutating-webhook-injected annotations
	// that appear on pods but not in the workload's template.
	SourcePodAnnotation EvidenceSource = "pod_annotation"

	// SourcePodTemplateAnnotation identifies values extracted from
	// spec.template.metadata.annotations on a workload (Deployment,
	// StatefulSet, etc.). These are the annotations the workload owner
	// authored; they propagate to every pod replica.
	SourcePodTemplateAnnotation EvidenceSource = "pod_template_annotation"

	// SourceWorkloadAnnotation identifies values extracted from
	// metadata.annotations on the top-level workload object (Deployment,
	// StatefulSet, etc., as opposed to its pod template).
	SourceWorkloadAnnotation EvidenceSource = "workload_annotation"

	// SourceImageReference identifies values extracted from the
	// container's image: field itself (e.g., a sha256 digest embedded in
	// the image reference). Distinct from SourceImagePattern (regex match
	// against the image string) and SourceImageLabel (OCI image labels).
	SourceImageReference EvidenceSource = "image_reference"

	// SourceContainerArg identifies values extracted from a regular
	// container's args[] (not initContainers, not command). The arg index
	// and flag name should appear in the Evidence.Locator.
	SourceContainerArg EvidenceSource = "container_arg"

	// SourceInitContainerArg identifies values extracted from an init
	// container's args[]. v1 does NOT scrape init container args for
	// model claims (per the "honest not clever" rule); the constant is
	// reserved for v2 use.
	SourceInitContainerArg EvidenceSource = "init_container_arg"

	SourceEnvVar          EvidenceSource = "env_var"
	SourceEnvVarNamePresent EvidenceSource = "env_var_name_present"
	SourceImagePattern    EvidenceSource = "image_pattern"
	SourceImageLabel      EvidenceSource = "image_label"
	SourceCRDField        EvidenceSource = "crd_field"
	SourceVolumeSource    EvidenceSource = "volume_source"
	SourceResourceRequest EvidenceSource = "resource_request"
	SourceNodeSelector    EvidenceSource = "node_selector"

	// SourcePodStatus identifies values extracted from pod.status (notably
	// imageID, which is how v1 resolves image digests per the digest-
	// resolution policy).
	SourcePodStatus EvidenceSource = "pod_status"
)

// Evidence describes where a specific attribute value was extracted from.
// Locator is a free-form but conventional identifier of the specific field
// path (e.g., "spec.template.spec.containers[0].env[HF_MODEL_ID]" or
// "status.containerStatuses[0].imageID"). Locator MUST NOT carry secret
// data; it is intended for audit-trail use.
//
// JSON tags are present so this type round-trips cleanly through any future
// API surface that mirrors it (e.g., a deliberate API type in api/v1alpha1
// that exposes per-attribute evidence in the CR status). Internal use of
// Evidence does not depend on the tags; see types_marshal_test.go.
type Evidence struct {
	Source  EvidenceSource `json:"source,omitempty"`
	Locator string         `json:"locator,omitempty"`
}

// Confidence is the per-attribute or per-workload confidence label. At
// attribute level all three values are possible. At workload level (the
// aggregate confidence), only Declared and Inferred are emitted —
// Unresolved is reserved for individual attributes that could not be
// determined (e.g., image digest with no Ready pod yet).
type Confidence string

const (
	ConfidenceDeclared   Confidence = "declared"
	ConfidenceInferred   Confidence = "inferred"
	ConfidenceUnresolved Confidence = "unresolved"
)

// ComponentType is the high-level kind of a Component, mapping to the
// CycloneDX component type vocabulary at BOM-build time. The internal set
// is intentionally small; the BOM builder is responsible for the
// translation into the CycloneDX type field.
type ComponentType string

const (
	ComponentContainer   ComponentType = "container"
	ComponentApplication ComponentType = "application"
	ComponentMLModel     ComponentType = "machine-learning-model"
	ComponentData        ComponentType = "data"
)

// Component is the internal, pre-CycloneDX representation of a single BOM
// component (container image, serving runtime application, ML model, etc.).
// The BOM builder maps these into CycloneDX 1.6 ML-BOM components.
//
// Properties is a free-form string map used for attributes that don't have
// a dedicated field on the struct. Each key SHOULD use a dotted-name
// convention (e.g., "runtime.name", "hardware.accelerator") and the BOM
// builder maps these into CycloneDX properties[].
//
// Children represents nested components owned by this one (e.g., the
// transformers Python package inside a serving runtime application).
//
// JSON tags are present so this type round-trips cleanly through any future
// API surface that mirrors it; see types_marshal_test.go.
type Component struct {
	Type       ComponentType     `json:"type,omitempty"`
	Name       string            `json:"name,omitempty"`
	Version    string            `json:"version,omitempty"`
	PURL       string            `json:"purl,omitempty"`
	Hashes     map[string]string `json:"hashes,omitempty"`
	Evidence   Evidence          `json:"evidence,omitzero"`
	Confidence Confidence        `json:"confidence,omitempty"`
	Properties map[string]string `json:"properties,omitempty"`
	Children   []Component       `json:"children,omitempty"`
}

// Service is the internal, pre-CycloneDX representation of a service or
// endpoint observed for the workload (e.g., the cluster-internal Service
// or an Inference Gateway endpoint). v1 produces these from the workload
// and its associated Services; future scrapers may produce them from
// network policy or mesh telemetry.
//
// JSON tags are present so this type round-trips cleanly; see
// types_marshal_test.go.
type Service struct {
	Name      string   `json:"name,omitempty"`
	Endpoints []string `json:"endpoints,omitempty"`
	Evidence  Evidence `json:"evidence,omitzero"`
}

// Provenance records how a particular scraper's contribution was produced.
// Each contributing scraper appends one entry. The BOM builder serializes
// all provenance entries into the CycloneDX metadata.properties[] block.
//
// JSON tags are present so this type round-trips cleanly; see
// types_marshal_test.go.
type Provenance struct {
	ScraperName     string    `json:"scraperName,omitempty"`
	ScraperVersion  string    `json:"scraperVersion,omitempty"`
	ScrapeMethod    string    `json:"scrapeMethod,omitempty"` // "spec" for v1; "ebpf" for future
	ScrapeTimestamp time.Time `json:"scrapeTimestamp,omitzero"`
}

// BOMInputs is the output of a single Scraper invocation: the partial set
// of components, services, and provenance entries the scraper contributed,
// together with any non-fatal errors encountered.
//
// JSON tags are present so the golden-file test infrastructure can read
// fixtures from testdata/. The Errors field is tagged json:"-" so it
// never serializes; errors are surfaced in controller logs and metrics
// only. The Sink contract receives a finished bom.Document, not the raw
// BOMInputs, so Errors cannot leak into customer-visible output.
type BOMInputs struct {
	ScraperName     string    `json:"scraperName,omitempty"`
	ScrapeTimestamp time.Time `json:"scrapeTimestamp,omitzero"`
	// Confidence is the scraper's aggregate workload-level confidence for
	// its own contribution. The discovery layer may further aggregate
	// across multiple scrapers when more than one applies (v2+).
	Confidence Confidence       `json:"confidence,omitempty"`
	Components []Component      `json:"components,omitempty"`
	Services   []Service        `json:"services,omitempty"`
	Provenance []Provenance     `json:"provenance,omitempty"`
	Errors     []error          `json:"-"`
	Category   WorkloadCategory `json:"category,omitempty"`
}

// Deduplicate removes duplicate components and services from the inputs,
// preserving order.
func (b *BOMInputs) Deduplicate() {
	if b == nil {
		return
	}
	b.Components = deduplicateComponents(b.Components)
	b.Services = deduplicateServices(b.Services)
}

func deduplicateComponents(in []Component) []Component {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]bool)
	var out []Component
	for _, c := range in {
		if len(c.Children) > 0 {
			c.Children = deduplicateComponents(c.Children)
		}
		// Key on coordinates that identify a component and its extraction source.
		key := string(c.Type) + "|" + c.Name + "|" + c.Version + "|" + string(c.Evidence.Source) + "|" + c.Evidence.Locator
		if !seen[key] {
			seen[key] = true
			out = append(out, c)
		}
	}
	return out
}

func deduplicateServices(in []Service) []Service {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]bool)
	var out []Service
	for _, s := range in {
		if !seen[s.Name] {
			seen[s.Name] = true
			out = append(out, s)
		}
	}
	return out
}
