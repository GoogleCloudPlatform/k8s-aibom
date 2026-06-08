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
	"fmt"
	"strings"

	"cloud.google.com/go/storage"
	"google.golang.org/api/option"

	"github.com/GoogleCloudPlatform/k8s-aibom/internal/bom"
)

// DefaultGCSPathTemplate is the default object path layout for BOMs written
// to a GCS bucket. Placeholders are replaced with values from the SinkMeta
// passed to Emit.
//
// Supported placeholders:
//
//	{namespace} — workload namespace
//	{kind}      — workload kind (e.g., "Deployment")
//	{name}      — workload name
//	{timestamp} — RFC3339-shaped UTC timestamp with colons replaced by dashes
//	{hash}      — short (12-char) prefix of the BOM's sha256 hash
//
// The default uses timestamp uniqueness to satisfy the FR4.4 immutability
// guarantee: distinct reconcile cycles produce distinct object paths;
// within-second collisions are caught by the DoesNotExist precondition
// on the write and surfaced as a per-write error.
const DefaultGCSPathTemplate = "mlbom/{namespace}/{kind}-{name}/{timestamp}.json"

// GCSSinkConfig is the per-sink configuration for a GCSSink. Validate with
// (*GCSSinkConfig).Validate before passing to NewGCSSink.
type GCSSinkConfig struct {
	// Bucket is the target GCS bucket name. Required.
	Bucket string

	// PathTemplate is the object path layout. If empty,
	// DefaultGCSPathTemplate is used.
	PathTemplate string

	// CredentialsFile is the absolute path to a Google service-account
	// JSON key file. If empty AND CredentialsJSON is unset, the
	// storage client uses Application Default Credentials (ADC).
	//
	// ADC is the preferred mechanism: it works on GKE via Workload
	// Identity, on EKS/AKS/on-prem via Workload Identity Federation,
	// and locally via `gcloud auth application-default login`. Use
	// CredentialsFile only when ADC is not viable; the project does
	// not manage key rotation.
	//
	// Mutually exclusive with CredentialsJSON. If both are set,
	// CredentialsJSON wins (it's the in-memory form preferred by
	// the CRD-driven config path; see config.ClientSinkFactory).
	CredentialsFile string

	// CredentialsJSON is the in-memory form of a Google service-account
	// JSON key. Used by the AIBOMControllerConfig-driven path, which
	// reads credentials from a K8s Secret and passes the bytes
	// directly without writing a temp file. Same security caveats
	// as CredentialsFile: prefer ADC when possible.
	CredentialsJSON []byte
}

// Validate returns an error if the config is unusable.
func (c *GCSSinkConfig) Validate() error {
	if c.Bucket == "" {
		return errors.New("gcs sink: Bucket is required")
	}
	// PathTemplate empty is OK (default applied at use time).
	return nil
}

// GCSSink writes generated BOMs to a configured GCS bucket. It is the v1
// archival sink for long-term retention and external consumption (GRC
// pipelines, customer SIEMs, etc.).
//
// Security model (PRD FR4.4):
//   - GCSSink is the SOLE writer to the configured bucket from the cluster.
//   - Requires only roles/storage.objectCreator on the bucket (write-only).
//   - Object names are timestamped; objects are never overwritten (a
//     DoesNotExist precondition on the write enforces this).
//   - Workload pods MUST NOT have any IAM grant on this bucket.
//
// Error handling:
//   - Errors returned by Emit are safe to surface in CR conditions: they
//     name the bucket and object path but never include credentials or
//     auth tokens (the underlying library does not leak these in its
//     error messages).
//   - A failed Emit does not block the reconciler's CRD-status update;
//     the controller treats it as a SinkFailed condition and the next
//     reconcile retries.
//
// Retry and recovery semantics:
//   - Each Emit is bounded by the per-sink context deadline set by the
//     reconciler (DefaultExternalSinkTimeout = 30s). The cloud.google.com/go/storage
//     library applies its default retry policy within that window.
//   - Transient failures (network, rate-limit, 5xx from GCS) result in
//     the BOM being re-emitted on the next reconcile pass (typically
//     within minutes). The controller does not run an inner retry loop;
//     reconciliation is the retry mechanism.
type GCSSink struct {
	cfg    GCSSinkConfig
	client *storage.Client
}

// NewGCSSink constructs a GCSSink with the given config. The storage
// client is created with the cfg.CredentialsFile auth path or via ADC
// when CredentialsFile is empty.
func NewGCSSink(ctx context.Context, cfg GCSSinkConfig) (*GCSSink, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if cfg.PathTemplate == "" {
		cfg.PathTemplate = DefaultGCSPathTemplate
	}
	var opts []option.ClientOption
	switch {
	case len(cfg.CredentialsJSON) > 0:
		// CRD-driven path: bytes from a K8s Secret.
		opts = append(opts, option.WithCredentialsJSON(cfg.CredentialsJSON)) //nolint:staticcheck
	case cfg.CredentialsFile != "":
		// File-based path: env-var or volume-mounted Secret file.
		opts = append(opts, option.WithCredentialsFile(cfg.CredentialsFile)) //nolint:staticcheck
	}
	// When neither is set, no auth option is added; the storage
	// client falls through to ADC discovery.
	client, err := storage.NewClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("gcs sink: create storage client: %w", err)
	}
	return &GCSSink{cfg: cfg, client: client}, nil
}

// Name returns the stable sink identifier.
func (s *GCSSink) Name() string { return "gcs" }

// Close cleanly shuts down the GCS client.
func (s *GCSSink) Close() error {
	if s.client != nil {
		return s.client.Close()
	}
	return nil
}

// HealthCheck returns nil unconditionally for v1. A meaningful probe
// would require read permission on the bucket (Bucket.Attrs needs
// storage.buckets.get), but the sole-writer security model grants only
// storage.objectCreator. The sink is health-checked by use: a failing
// Emit surfaces the failure on the AIBOM CR's SinkFailed condition.
func (s *GCSSink) HealthCheck(_ context.Context) error { return nil }

// WriteOnly returns false: the gs:// URL returned by Emit is canonical
// and readable by any principal with storage.objectViewer (which can
// be granted separately to consumers without affecting the controller's
// sole-writer IAM).
func (s *GCSSink) WriteOnly() bool { return false }

// Emit writes the BOM JSON to the configured bucket at a path derived
// from the SinkMeta plus the configured PathTemplate. Returns the
// canonical gs:// URL of the written object on success.
//
// The write is gated by a DoesNotExist precondition: if an object with
// the same name already exists (rare, only possible on second-resolution
// timestamp collisions), the write fails with a clear error and the
// caller surfaces it on the SinkFailed condition. The next reconcile
// produces a new timestamp and succeeds.
func (s *GCSSink) Emit(ctx context.Context, doc *bom.Document, meta SinkMeta) (string, error) {
	if doc == nil {
		return "", errors.New("gcs sink: doc is nil")
	}
	objectName := renderGCSPath(s.cfg.PathTemplate, meta)
	url := fmt.Sprintf("gs://%s/%s", s.cfg.Bucket, objectName)

	obj := s.client.Bucket(s.cfg.Bucket).Object(objectName)
	w := obj.If(storage.Conditions{DoesNotExist: true}).NewWriter(ctx)
	w.ContentType = "application/json"
	if _, err := w.Write(doc.JSON); err != nil {
		// Always Close to release resources; the Write error is the
		// authoritative cause.
		_ = w.Close()
		return "", fmt.Errorf("gcs sink: write to %s: %w", url, err)
	}
	if err := w.Close(); err != nil {
		return "", fmt.Errorf("gcs sink: finalize %s: %w", url, err)
	}
	return url, nil
}

// renderGCSPath substitutes SinkMeta values into the path template. The
// substitution is intentionally a plain string replace (not text/template)
// to keep the surface minimal and the path-template syntax trivially
// auditable in customer-facing docs.
//
// {timestamp} resolves to millisecond-precision RFC3339-shaped UTC with
// colons replaced by dashes (e.g., "2026-05-10T12-00-00.123Z"). The
// millisecond precision is defense-in-depth against same-second
// timestamp collisions on fast reconciles; the primary mechanism for
// avoiding redundant writes is the reconciler's input-hash dedup
// (Status.InputHash).
func renderGCSPath(template string, meta SinkMeta) string {
	// Millisecond-precision RFC3339-shape with colons replaced.
	// Format string yields e.g. "2026-05-10T12:00:00.123Z"; the
	// ReplaceAll then yields "2026-05-10T12-00-00.123Z". Output
	// remains lexicographically sortable.
	// Defend against path manipulation (directory traversal)
	// by replacing path separators and dots in workload-provided strings.
	sanitize := func(s string) string {
		s = strings.ReplaceAll(s, "/", "_")
		s = strings.ReplaceAll(s, "\\", "_")
		s = strings.ReplaceAll(s, ".", "_")
		s = strings.ReplaceAll(s, "\n", "_")
		s = strings.ReplaceAll(s, "\r", "_")
		return s
	}

	ts := meta.Timestamp.UTC().Format("2006-01-02T15:04:05.000Z")
	ts = strings.ReplaceAll(ts, ":", "-")
	hashShort := meta.BOMHash
	if len(hashShort) > 12 {
		hashShort = hashShort[:12]
	}
	r := strings.NewReplacer(
		"{namespace}", sanitize(meta.WorkloadNamespace),
		"{kind}", sanitize(strings.ToLower(meta.WorkloadKind)),
		"{name}", sanitize(meta.WorkloadName),
		"{timestamp}", ts,
		"{hash}", hashShort,
		"{category}", sanitize(meta.WorkloadCategory),
	)
	return r.Replace(template)
}
