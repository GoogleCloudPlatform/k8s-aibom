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

// AIBOMControllerConfigSpec is the cluster-scoped runtime configuration
// for the k8s-aibom controller. Phase 12 wires this CR as the canonical
// configuration surface; the controller reads only the CR named
// "default" (singleton convention; admission webhook enforcement is
// post-v1.0).
//
// Behavior on missing or semantically invalid CR is documented in the
// project memory entry "AIBOMControllerConfig v1 behavior" and in
// README's troubleshooting section: the controller falls back to
// compiled-in safe defaults rather than failing to start, and surfaces
// the failure via a Ready=False condition on this CR (when present)
// plus a K8s Event on the controller's own Deployment.
type AIBOMControllerConfigSpec struct {
	// Discovery configures workload selection.
	// +optional
	Discovery DiscoveryConfig `json:"discovery,omitempty"`

	// BOMGeneration configures BOM-building behavior.
	// +optional
	BOMGeneration BOMGenerationConfig `json:"bomGeneration,omitempty"`

	// Sinks is the ordered list of external sinks. Empty or absent
	// means CRD-status-only (the CRD-status sink is always-on; it is
	// NOT listed here). When multiple sinks are configured, the
	// reconciler fans out in parallel; ExternalBOMRef.URL prefers
	// non-WriteOnly sinks (GCS over webhook) when both succeed.
	// +optional
	// +listType=map
	// +listMapKey=name
	Sinks []SinkConfig `json:"sinks,omitempty"`

	// Logging configures the controller's log level and format.
	// +optional
	Logging LoggingConfig `json:"logging,omitempty"`
}

// DiscoveryConfig narrows the set of workloads the controller tracks.
type DiscoveryConfig struct {
	// NamespaceSelector selects which namespaces the controller
	// scrapes. The controller checks this selector on each reconcile
	// against the workload's Namespace; non-matching namespaces are
	// silently skipped. If absent or empty, defaults to matching the
	// label `aibom.k8saibom.dev/enabled=true`.
	// +optional
	NamespaceSelector *metav1.LabelSelector `json:"namespaceSelector,omitempty"`

	// InferenceRuntimeImagePatterns configures the regex patterns
	// the InferenceSpecScraper uses to detect runtime frameworks
	// from container image references. If absent or empty, the
	// compiled-in v1 defaults apply (see internal/scraper/v1-runtime-patterns.yaml).
	//
	// Conservative-detection rule: prefer anchored patterns. See
	// docs/scraper-heuristics.md "Known false negatives" for context.
	// +optional
	InferenceRuntimeImagePatterns []RuntimeImagePattern `json:"inferenceRuntimeImagePatterns,omitempty"`
}

// RuntimeImagePattern is one runtime-detection rule.
type RuntimeImagePattern struct {
	// Runtime is the identifier emitted as the detected runtime
	// name (e.g., "vllm", "triton", "sglang").
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	Runtime string `json:"runtime"`

	// Pattern is a Go regexp matched against the full image
	// reference (registry/path:tag).
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	Pattern string `json:"pattern"`
}

// BOMGenerationConfig tunes the BOM builder.
type BOMGenerationConfig struct {
	// InlineThresholdBytes is the size threshold below which generated
	// BOMs are stored inline in AIBOM.status.bomDocument.inline.
	// BOMs above this size are offloaded to an external sink (or
	// truncated if no external sink is configured).
	//
	// Default: 262144 (256 KiB). Hard ceiling 1 MiB to stay safely
	// under K8s' etcd object size limit.
	// +optional
	// +kubebuilder:default=262144
	// +kubebuilder:validation:Minimum=1024
	// +kubebuilder:validation:Maximum=1048576
	// StaleThresholdReconciles is the number of consecutive reconciles with
	// extraction errors before the AIBOM is marked Stale.
	// +optional
	// +kubebuilder:default=3
	// +kubebuilder:validation:Minimum=1
	StaleThresholdReconciles int32 `json:"staleThresholdReconciles,omitempty"`

	InlineThresholdBytes int64 `json:"inlineThresholdBytes,omitempty"`
}

// SinkType identifies the implementation of an external sink.
// +kubebuilder:validation:Enum=GCS;Webhook
type SinkType string

const (
	SinkTypeGCS     SinkType = "GCS"
	SinkTypeWebhook SinkType = "Webhook"
)

// SinkConfig configures one external sink. Exactly one of GCS or
// Webhook MUST be populated based on Type.
type SinkConfig struct {
	// Name is the customer-chosen identifier for this sink instance,
	// distinct from the sink type. Appears in
	// AIBOM.status.bomDocument.externalRef.sink and in failure
	// conditions. Must be DNS-1123 label safe.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Name string `json:"name"`

	// Type selects the sink implementation. The corresponding
	// type-specific field (GCS or Webhook) MUST be populated; the
	// other MUST be absent.
	// +kubebuilder:validation:Required
	Type SinkType `json:"type"`

	// GCS configures a GCS sink. Required when Type=GCS.
	// +optional
	GCS *GCSSinkSpec `json:"gcs,omitempty"`

	// Webhook configures a webhook sink. Required when Type=Webhook.
	// +optional
	Webhook *WebhookSinkSpec `json:"webhook,omitempty"`
}

// GCSSinkSpec configures a GCS bucket sink. Auth is via Application
// Default Credentials by default (works on GKE Workload Identity, EKS/
// AKS via Workload Identity Federation, local via gcloud). The
// service-account-key-file fallback is via CredentialsSecretRef.
type GCSSinkSpec struct {
	// Bucket is the target GCS bucket name.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=3
	// +kubebuilder:validation:MaxLength=222
	Bucket string `json:"bucket"`

	// PathTemplate is the object path layout. Placeholders:
	// {namespace}, {kind}, {name}, {timestamp}, {hash}, {category}.
	// Default: mlbom/{namespace}/{kind}-{name}/{timestamp}.json.
	// +optional
	// +kubebuilder:validation:MaxLength=512
	PathTemplate string `json:"pathTemplate,omitempty"`

	// CredentialsSecretRef optionally references a Secret containing
	// a service-account JSON key file. If unset, Application Default
	// Credentials are used (recommended; works on GKE Workload Identity).
	// The Secret MUST live in the controller's namespace.
	// +optional
	CredentialsSecretRef *SecretKeyRef `json:"credentialsSecretRef,omitempty"`
}

// WebhookSinkSpec configures a webhook sink.
type WebhookSinkSpec struct {
	// Endpoint is the full HTTPS URL to POST BOMs to.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^https?://`
	// +kubebuilder:validation:MaxLength=2048
	Endpoint string `json:"endpoint"`

	// Auth configures the webhook authentication. Exactly one of
	// BearerToken / MTLS may be set. Absent Auth means no
	// authentication is sent (development / private-network only).
	// +optional
	Auth *WebhookAuth `json:"auth,omitempty"`
}

// WebhookAuth selects exactly one webhook authentication mechanism.
type WebhookAuth struct {
	// BearerToken uses an "Authorization: Bearer <token>" header.
	// The token is read from the referenced Secret.
	// +optional
	BearerToken *BearerTokenAuth `json:"bearerToken,omitempty"`

	// MTLS uses mutual TLS with a client certificate / key. The
	// optional CASecretRef provides a custom CA bundle for verifying
	// the server's certificate.
	// +optional
	MTLS *MTLSAuth `json:"mtls,omitempty"`
}

// BearerTokenAuth holds a bearer-token Secret reference.
type BearerTokenAuth struct {
	// SecretRef points at the K8s Secret containing the token.
	// +kubebuilder:validation:Required
	SecretRef SecretKeyRef `json:"secretRef"`
}

// MTLSAuth holds Secret references for the mTLS client cert + key,
// and an optional custom CA bundle.
type MTLSAuth struct {
	// ClientCertSecretRef holds the PEM-encoded client certificate.
	// +kubebuilder:validation:Required
	ClientCertSecretRef SecretKeyRef `json:"clientCertSecretRef"`

	// ClientKeySecretRef holds the PEM-encoded client private key.
	// +kubebuilder:validation:Required
	ClientKeySecretRef SecretKeyRef `json:"clientKeySecretRef"`

	// CASecretRef optionally holds a custom CA bundle for verifying
	// the server's certificate. If unset, system roots are used.
	// +optional
	CASecretRef *SecretKeyRef `json:"caSecretRef,omitempty"`
}

// SecretKeyRef points at a single key within a K8s Secret. The
// referenced Secret MUST live in the controller's namespace
// (k8s-aibom-system per PRD FR4.4); namespace cross-references are
// rejected.
//
// Secret-rotation semantics: the controller re-reads the Secret only
// when the AIBOMControllerConfig CR is changed (re-apply, annotation
// touch, or any spec edit). Direct Secret content changes do NOT
// trigger reload in v1.0; v1.1+ may add Secret watching.
type SecretKeyRef struct {
	// Name is the Secret's metadata.name.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`

	// Key is the data key within the Secret whose value holds the
	// credential. Forcing the customer to be explicit about which
	// key matches the cert-manager / external-secrets convention.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Key string `json:"key"`
}

// LoggingConfig tunes the controller's logger.
type LoggingConfig struct {
	// Level is the controller log verbosity. Hot-reloadable.
	// +optional
	// +kubebuilder:default=info
	// +kubebuilder:validation:Enum=debug;info;warn;error
	Level string `json:"level,omitempty"`

	// Format is the log output format. RESTART-REQUIRED — the logger
	// is constructed at controller startup; format changes require a
	// pod restart to take effect. Exposed in the spec for honest
	// surface; the controller logs a notice when this changes
	// post-startup.
	// +optional
	// +kubebuilder:default=json
	// +kubebuilder:validation:Enum=json;text
	Format string `json:"format,omitempty"`
}

// AIBOMControllerConfigStatus reports the controller's view of this
// configuration: whether it loaded successfully, when it was last
// re-read, and any current failure modes.
type AIBOMControllerConfigStatus struct {
	// Conditions follow the K8s standard. The condition types
	// emitted by the controller are documented in
	// api/v1alpha1/aibomcontrollerconfig_conditions.go.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ObservedGeneration is the spec.generation at the time the
	// status was last set.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// LastLoadedAt is the time the controller last successfully
	// parsed this configuration into a runtime snapshot. Distinct
	// from LastTransitionTime on the Ready condition (which moves
	// only when the condition's status value transitions).
	// +optional
	LastLoadedAt *metav1.Time `json:"lastLoadedAt,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=aibomcontrollerconfigs,scope=Cluster,shortName=aibomcfg
// +kubebuilder:metadata:annotations="api-approved.kubernetes.io=unapproved, experimental-only"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Last-Loaded",type=date,JSONPath=`.status.lastLoadedAt`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// AIBOMControllerConfig is the cluster-scoped runtime configuration for the
// k8s-aibom controller.
//
// Singleton convention: the controller consults the AIBOMControllerConfig
// whose metadata.name is exactly "default". Other AIBOMControllerConfig
// resources are tolerated (cluster admins may create them as drafts) but
// are not consulted by the running controller. Customers wishing to
// update controller behavior MUST edit the "default" CR — creating a
// differently-named CR has no effect on controller behavior.
//
// Formal admission-webhook enforcement of the singleton (rejecting
// creation of CRs with names other than "default") is post-v1.0. v1
// relies on documentation and the controller's "only watch default"
// behavior. This is acceptable for v1 because cluster admins authoring
// AIBOMControllerConfig are trusted users; the singleton enforcement is
// a usability guard against accidental misconfiguration, not a security
// boundary.
type AIBOMControllerConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AIBOMControllerConfigSpec   `json:"spec,omitempty"`
	Status AIBOMControllerConfigStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// AIBOMControllerConfigList is a list of AIBOMControllerConfig resources.
type AIBOMControllerConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AIBOMControllerConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AIBOMControllerConfig{}, &AIBOMControllerConfigList{})
}
