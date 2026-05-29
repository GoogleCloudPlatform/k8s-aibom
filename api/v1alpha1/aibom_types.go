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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AIBOMSpec defines the desired state of an AIBOM resource.
//
// In v1 the AIBOM CR is fully controller-owned: the controller creates it
// when a tracked workload is discovered and garbage-collects it via owner
// references when the workload is deleted. Customer-authored AIBOM CRs
// are NOT supported in v1; admission webhook enforcement is post-v1.0.
// The Spec exists so the workload identity is visible without descending
// into ownerReferences.
type AIBOMSpec struct {
	// WorkloadRef identifies the workload this AIBOM describes. Set by
	// the controller from the discovered workload's TypeMeta and Name.
	//
	// Immutable: once set on the AIBOM, the workload identity cannot
	// change. If a workload is renamed or its kind changes, the
	// resulting state is a NEW AIBOM (created by the controller for the
	// new workload) and the OLD AIBOM is garbage-collected via owner
	// references when the original workload is deleted.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="workloadRef is immutable"
	WorkloadRef WorkloadRef `json:"workloadRef"`

	// BOMFormat is the wire-format identifier of the generated BOM. v1
	// emits only "CycloneDX". The enum is intentionally expandable:
	// adding "SPDX" in a future release will not break existing AIBOM
	// CRs because they will continue to declare "CycloneDX".
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=CycloneDX
	// +kubebuilder:validation:MaxLength=64
	BOMFormat string `json:"bomFormat"`

	// BOMSpecVersion is the spec version of the generated BOM. v1 emits
	// "1.6". Constrained to a major.minor shape; patch versions are not
	// used by the CycloneDX wire format.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^\d+\.\d+$`
	// +kubebuilder:validation:MaxLength=16
	BOMSpecVersion string `json:"bomSpecVersion"`
}

// WorkloadRef identifies a Kubernetes workload by API group/version, kind,
// and name within the same namespace as the AIBOM. UID is intentionally
// absent — it appears on the AIBOM's metadata.ownerReferences for
// garbage collection.
type WorkloadRef struct {
	// APIVersion is the group/version of the workload, in the standard
	// K8s "group/version" form (e.g., "apps/v1", "serving.kserve.io/v1beta1").
	// Core kinds use a plain version (e.g., "v1").
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	APIVersion string `json:"apiVersion"`

	// Kind is the workload kind (e.g., "Deployment", "StatefulSet",
	// "InferenceService").
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	Kind string `json:"kind"`

	// Name is the workload's metadata.name. Must be DNS-1123-subdomain
	// safe; the MaxLength matches the Kubernetes name length limit.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
}

// AIBOMStatus is the auditor-facing summary of the generated BOM and the
// controller's reconciliation state. Fields are designed for stability;
// see docs/prd-deviations.md for guidance on treating this as a deliberate
// external contract distinct from the internal scraper / builder types.
type AIBOMStatus struct {
	// Conditions follow the Kubernetes standard
	// (k8s.io/apimachinery/pkg/apis/meta/v1.Condition). See
	// api/v1alpha1/aibom_conditions.go for the type and reason constants.
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Summary is the deliberately small, auditor-readable summary of the
	// BOM. Always populated (best-effort) regardless of whether the full
	// BOM is inline or in an external sink, so `kubectl get aibom -o yaml`
	// is informative on its own.
	Summary *AIBOMSummary `json:"summary,omitempty"`

	// BOMDocument identifies where the full BOM lives — inline in this
	// status, in an external sink, or truncated (no inline + no external).
	BOMDocument *BOMDocumentRef `json:"bomDocument,omitempty"`

	// LastReconciled is the time the controller last reconciled this
	// AIBOM, whether or not the BOM was regenerated.
	LastReconciled *metav1.Time `json:"lastReconciled,omitempty"`

	// BOMHash is the SHA-256 of the most recently generated BOM
	// (canonical JSON). Used by external consumers for cache
	// invalidation and to detect content changes between reconciles.
	// Distinct from InputHash: BOMHash captures the OUTPUT bytes
	// (which include the BOM's generation timestamp), whereas
	// InputHash captures the INPUT scrape result (which is time-
	// independent). Two reconciles of an unchanged workload produce
	// different BOMHash values but the same InputHash.
	BOMHash string `json:"bomHash,omitempty"`

	// InputHash is the SHA-256 hex digest of the BOMInputs that
	// produced this AIBOM, computed with scrape timestamps zeroed.
	// The reconciler compares this against a freshly-computed
	// inputs hash on every reconcile pass; when they match, the
	// reconciler skips BOM regeneration and external-sink emission
	// (the fast path), updating only LastReconciled and
	// ObservedGeneration. See docs/build-progress.md for the dedup
	// design rationale.
	InputHash string `json:"inputHash,omitempty"`

	// ObservedGeneration is the .metadata.generation of the AIBOM at
	// the time the status was last updated. Follows the Kubernetes
	// generation/observedGeneration convention.
	// ConsecutiveErrors counts how many consecutive reconciles have produced
	// extraction errors. Used to trigger the Stale condition.
	ConsecutiveErrors int32 `json:"consecutiveErrors,omitempty"`

	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// AIBOMSummary is the auditor-readable digest of the BOM. Fields here are
// intentionally selected for stability and small size; the full BOM
// (with every component's evidence and properties) lives in
// BOMDocument.Inline or in an external sink.
type AIBOMSummary struct {
	// Workload identifies the workload the BOM describes.
	Workload WorkloadSummary `json:"workload"`

	// Runtime is the detected inference runtime, if any.
	Runtime *RuntimeSummary `json:"runtime,omitempty"`

	// Models is the set of declared/inferred model identities. Multiple
	// entries are emitted when different evidence sources name the same
	// model with different confidence (e.g., --model arg + HF_MODEL_ID
	// env var) so auditors see both signals.
	Models []ModelSummary `json:"models,omitempty"`

	// Confidence is the workload-level aggregated confidence. One of
	// "declared", "inferred", "unresolved" — see
	// docs/scraper-heuristics.md for the aggregation rule.
	Confidence string `json:"confidence,omitempty"`
}

// WorkloadSummary identifies a tracked workload by its Kubernetes
// coordinates, plus the controller's high-level category classification.
type WorkloadSummary struct {
	Kind       string `json:"kind"`
	APIVersion string `json:"apiVersion"`
	Name       string `json:"name"`
	Namespace  string `json:"namespace"`
	// Category is the high-level AI lifecycle role assigned by the
	// discovery layer. v1 always emits "inference"; v2+ may emit
	// "training", "agent", "evaluation", "pipeline", "notebook".
	Category string `json:"category,omitempty"`
}

// RuntimeSummary describes a detected inference runtime.
type RuntimeSummary struct {
	// Name is the runtime identifier (e.g., "vllm", "triton", "tgi").
	Name string `json:"name"`
	// Version is the runtime version, populated only when the image tag
	// is semver-shaped (see docs/scraper-heuristics.md). Empty otherwise.
	Version string `json:"version,omitempty"`
	// Confidence is the per-attribute confidence. v1 emits "inferred"
	// for image-pattern-derived runtimes; never "declared".
	Confidence string `json:"confidence,omitempty"`
}

// ModelSummary is a single claimed-or-inferred model identity.
type ModelSummary struct {
	// Identity is the model identifier as it appeared in the source
	// (env var value, arg value, annotation value). NOT normalized.
	Identity string `json:"identity"`
	// Source is the evidence source the identity came from (e.g.,
	// "env_var", "container_arg", "workload_annotation"). Matches the
	// EvidenceSource enum in internal/scraper.
	Source string `json:"source,omitempty"`
	// Confidence is the per-attribute confidence ("declared" or
	// "inferred"). Never "unresolved" — unresolved attributes are not
	// summarized.
	Confidence string `json:"confidence,omitempty"`
	// Signed encodes signature presence and verification. One of
	// "unsigned", "claimed", "verified". v1 never emits "verified"
	// (see docs/schema-divergences.md D-001).
	Signed string `json:"signed,omitempty"`
}

// BOMDocumentRef identifies where the full BOM lives. Exactly one of
// Inline, External, or Truncated is the "active" form. When Truncated is
// true, neither Inline nor External carries the full BOM; auditors must
// configure an external sink to retrieve it.
type BOMDocumentRef struct {
	// Format is the BOM encoding identifier; v1 always "CycloneDX".
	Format string `json:"format"`
	// SpecVersion is the BOM spec version; v1 always "1.6".
	SpecVersion string `json:"specVersion"`
	// SHA256 is the hex-encoded sha256 of the canonical BOM JSON.
	// Stable across the inline/external/truncated representations: it
	// is the hash of the full BOM regardless of where it is stored.
	SHA256 string `json:"sha256"`
	// Size is the byte length of the canonical BOM JSON. Useful for
	// monitoring and for customers deciding whether to configure an
	// external sink.
	Size int64 `json:"size,omitempty"`

	// Inline carries the full BOM directly in this CR's status. Set
	// when Size is below AIBOMControllerConfig.bomGeneration.inlineThresholdBytes.
	// Mutually exclusive with External and Truncated.
	Inline *InlineBOM `json:"inline,omitempty"`

	// External points at the BOM in a configured external sink.
	// Set when Size exceeds the inline threshold AND at least one
	// external sink is configured.
	External *ExternalBOMRef `json:"external,omitempty"`

	// Truncated is true when Size exceeds the inline threshold AND no
	// external sink is configured. In this state, neither Inline nor
	// External is set; Summary remains populated. This is honest
	// degradation: the customer is told what to do to recover the
	// full BOM (configure an external sink).
	Truncated bool `json:"truncated,omitempty"`

	// TruncationReason is a human-readable explanation when Truncated
	// is true (e.g., "BOM size 384KB exceeds inline threshold 256KB and
	// no external sink is configured").
	TruncationReason string `json:"truncationReason,omitempty"`
}

// InlineBOM carries a full BOM document inline in the CR status.
type InlineBOM struct {
	// Data is the canonical BOM JSON. Encoded as base64 in YAML/JSON
	// via Go's []byte JSON convention.
	Data []byte `json:"data"`
}

// ExternalBOMRef points at a BOM stored in an external sink.
type ExternalBOMRef struct {
	// Sink is the configured sink that holds the BOM (e.g., "gcs",
	// "webhook", "guac"). Matches the Sink.Name() value.
	Sink string `json:"sink"`
	// URL is the BOM's external location at the named sink. When
	// WriteOnly is false (the default), URL is a canonical retrieval
	// URL — e.g., a gs:// URL on a readable GCS bucket. When
	// WriteOnly is true, URL identifies the delivery destination only
	// (e.g., a webhook endpoint posting to a SIEM) and is NOT
	// retrievable; auditors should seek the BOM via the inline form
	// or via a separately-configured retrievable sink.
	URL string `json:"url,omitempty"`
	// WriteOnly indicates that URL is informational rather than
	// retrievable. False (default) for object-storage sinks like GCS;
	// true for write-only sinks like webhook receivers. The controller
	// prefers non-WriteOnly sinks when multiple succeed, so this field
	// being true means either (a) only WriteOnly sinks were configured,
	// or (b) the non-WriteOnly sinks failed and a WriteOnly fallback
	// is reporting the delivery.
	WriteOnly bool `json:"writeOnly,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=aiboms,scope=Namespaced,shortName=aibom
// +kubebuilder:metadata:annotations="api-approved.kubernetes.io=unapproved, experimental-only"
// +kubebuilder:printcolumn:name="Workload",type=string,JSONPath=`.spec.workloadRef.kind`
// +kubebuilder:printcolumn:name="Name",type=string,JSONPath=`.spec.workloadRef.name`
// +kubebuilder:printcolumn:name="Category",type=string,JSONPath=`.status.summary.workload.category`
// +kubebuilder:printcolumn:name="Runtime",type=string,JSONPath=`.status.summary.runtime.name`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// AIBOM is the runtime AI Bill of Materials for a single top-level workload
// (Deployment, StatefulSet, KServe InferenceService, etc.). One AIBOM exists
// per workload, never per pod replica.
type AIBOM struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AIBOMSpec   `json:"spec,omitempty"`
	Status AIBOMStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// AIBOMList is a list of AIBOM resources.
type AIBOMList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AIBOM `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AIBOM{}, &AIBOMList{})
}
