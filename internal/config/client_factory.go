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

package config

import (
	"context"
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	aibomv1alpha1 "github.com/GoogleCloudPlatform/k8s-aibom/api/v1alpha1"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/sink"
)

// ClientSinkFactory is the production SinkFactory implementation.
// Reads referenced Secrets via the K8s API client and constructs
// GCSSink and WebhookSink instances from the CRD spec.
//
// Per PRD §FR4.4, all referenced Secrets MUST live in the controller's
// own namespace; the factory's Namespace field bounds Secret lookups
// to that namespace and the SecretKeyRef type carries only Name + Key
// (no Namespace field). This is the sole-writer security model: a
// compromised inference workload cannot trick the controller into
// reading a different namespace's Secret by manipulating the CR.
//
// Error messages follow the auditor-facing-precision standard locked
// by Checkpoint 2's discriminator-mismatch tests. Each Secret-related
// failure mode produces a message that names the sink, the field path,
// the Secret name + key, and an actionable suggestion. Tests in
// client_factory_test.go assert message shape via substring assertions
// (NOT exact-match) so future contributors can refine wording without
// breaking tests, but cannot accidentally regress to "secret not found."
type ClientSinkFactory struct {
	// Client reads referenced Secrets. Required.
	Client client.Client

	// Namespace is the controller's own namespace. All referenced
	// Secrets are looked up here. Cross-namespace Secret references
	// are intentionally impossible: the SecretKeyRef API type carries
	// only Name + Key.
	Namespace string

	// ControllerVersion is stamped into WebhookSink's
	// X-Aibom-Controller-Version header. Captured at factory
	// construction time; rebuilding the factory updates it.
	ControllerVersion string
}

// BuildSinks implements SinkFactory. Per the loader's all-or-nothing
// rule, the loader applies fallback to defaults if any LoadError is
// returned; the factory's job is to report per-sink outcomes faithfully.
func (f *ClientSinkFactory) BuildSinks(ctx context.Context, specs []aibomv1alpha1.SinkConfig) ([]sink.Sink, []LoadError) {
	var sinks []sink.Sink
	var errs []LoadError
	for _, s := range specs {
		switch s.Type {
		case aibomv1alpha1.SinkTypeGCS:
			built, le := f.buildGCSSink(ctx, s)
			if le != nil {
				errs = append(errs, *le)
				continue
			}
			sinks = append(sinks, built)
		case aibomv1alpha1.SinkTypeWebhook:
			built, le := f.buildWebhookSink(ctx, s)
			if le != nil {
				errs = append(errs, *le)
				continue
			}
			sinks = append(sinks, built)
		default:
			// validateSinkShapes already catches unknown types at the
			// loader level; the factory's switch is defensive in case
			// validation order ever changes.
			errs = append(errs, LoadError{
				Field: fmt.Sprintf("spec.sinks[name=%s].type", s.Name),
				Message: fmt.Sprintf(
					"Sink %q has unknown Type=%q. Allowed values: GCS, Webhook.",
					s.Name, s.Type,
				),
			})
		}
	}
	return sinks, errs
}

// buildGCSSink constructs a GCSSink from a SinkConfig with Type=GCS.
// Returns a LoadError for any construction failure (missing Secret,
// bad credentials, sink-internal validation failure). Callers MUST
// have validated that s.GCS is non-nil before calling.
func (f *ClientSinkFactory) buildGCSSink(ctx context.Context, s aibomv1alpha1.SinkConfig) (sink.Sink, *LoadError) {
	cfg := sink.GCSSinkConfig{
		Bucket:       s.GCS.Bucket,
		PathTemplate: s.GCS.PathTemplate,
	}
	if s.GCS.CredentialsSecretRef != nil {
		data, le := f.readSecretKey(ctx, s.Name, "Type=GCS", "gcs.credentialsSecretRef", *s.GCS.CredentialsSecretRef)
		if le != nil {
			return nil, le
		}
		cfg.CredentialsJSON = data
	}
	built, err := sink.NewGCSSink(ctx, cfg)
	if err != nil {
		le := LoadError{
			Field: fmt.Sprintf("spec.sinks[name=%s].gcs", s.Name),
			Message: fmt.Sprintf(
				"Sink %q (Type=GCS) failed to construct: %v. Verify spec.sinks[name=%s].gcs.bucket and any referenced credentials.",
				s.Name, err, s.Name,
			),
		}
		return nil, &le
	}
	return built, nil
}

// buildWebhookSink constructs a WebhookSink from a SinkConfig with
// Type=Webhook. Returns a LoadError for any construction failure.
// Callers MUST have validated that s.Webhook is non-nil before calling.
func (f *ClientSinkFactory) buildWebhookSink(ctx context.Context, s aibomv1alpha1.SinkConfig) (sink.Sink, *LoadError) {
	cfg := sink.WebhookSinkConfig{
		Endpoint:          s.Webhook.Endpoint,
		ControllerVersion: f.ControllerVersion,
	}
	if s.Webhook.Auth != nil {
		// Reject "both BearerToken and MTLS" at the factory level —
		// the API surface allows it structurally (separate fields),
		// but exactly one auth mechanism per sink is the design rule.
		if s.Webhook.Auth.BearerToken != nil && s.Webhook.Auth.MTLS != nil {
			le := LoadError{
				Field: fmt.Sprintf("spec.sinks[name=%s].webhook.auth", s.Name),
				Message: fmt.Sprintf(
					"Sink %q (Type=Webhook) has both auth.bearerToken and auth.mtls set. Exactly one auth mechanism per sink; remove one of them.",
					s.Name,
				),
			}
			return nil, &le
		}
		if s.Webhook.Auth.BearerToken != nil {
			data, le := f.readSecretKey(ctx, s.Name, "Type=Webhook", "webhook.auth.bearerToken.secretRef", s.Webhook.Auth.BearerToken.SecretRef)
			if le != nil {
				return nil, le
			}
			cfg.BearerToken = data
		}
		if s.Webhook.Auth.MTLS != nil {
			certData, le := f.readSecretKey(ctx, s.Name, "Type=Webhook", "webhook.auth.mtls.clientCertSecretRef", s.Webhook.Auth.MTLS.ClientCertSecretRef)
			if le != nil {
				return nil, le
			}
			keyData, le := f.readSecretKey(ctx, s.Name, "Type=Webhook", "webhook.auth.mtls.clientKeySecretRef", s.Webhook.Auth.MTLS.ClientKeySecretRef)
			if le != nil {
				return nil, le
			}
			cfg.ClientCertPEM = certData
			cfg.ClientKeyPEM = keyData
			if s.Webhook.Auth.MTLS.CASecretRef != nil {
				caData, le := f.readSecretKey(ctx, s.Name, "Type=Webhook", "webhook.auth.mtls.caSecretRef", *s.Webhook.Auth.MTLS.CASecretRef)
				if le != nil {
					return nil, le
				}
				cfg.CAPEM = caData
			}
		}
	}
	built, err := sink.NewWebhookSink(cfg)
	if err != nil {
		le := LoadError{
			Field: fmt.Sprintf("spec.sinks[name=%s].webhook", s.Name),
			Message: fmt.Sprintf(
				"Sink %q (Type=Webhook) failed to construct: %v. Verify spec.sinks[name=%s].webhook.endpoint and any referenced credentials.",
				s.Name, err, s.Name,
			),
		}
		return nil, &le
	}
	return built, nil
}

// readSecretKey fetches the Secret named ref.Name in the factory's
// namespace and returns the value at ref.Key. Produces specifically-
// actionable LoadErrors for each failure mode:
//
//   - Secret not found in namespace → "create the Secret or update
//     <fieldPath>.name"
//   - Secret found but key missing → "Available keys: [...]" with
//     the actual keys so the customer can spot typos
//   - Key found but value empty → "populate the Secret data or
//     update <fieldPath>.key"
//   - Other API error → propagate with context
//
// sinkName + sinkContext (e.g., "Type=GCS") + fieldPath (e.g.,
// "webhook.auth.bearerToken.secretRef") are combined into the error
// message so the customer can find the failing sink in their CR
// without scanning controller logs.
func (f *ClientSinkFactory) readSecretKey(ctx context.Context, sinkName, sinkContext, fieldPath string, ref aibomv1alpha1.SecretKeyRef) ([]byte, *LoadError) {
	var secret corev1.Secret
	err := f.Client.Get(ctx, types.NamespacedName{Name: ref.Name, Namespace: f.Namespace}, &secret)

	if apierrors.IsNotFound(err) {
		le := LoadError{
			Field: fmt.Sprintf("spec.sinks[name=%s].%s", sinkName, fieldPath),
			Message: fmt.Sprintf(
				"Sink %q (%s) %s: Secret %q not found in namespace %q. Create the Secret or update %s.name.",
				sinkName, sinkContext, fieldPath, ref.Name, f.Namespace, fieldPath,
			),
		}
		return nil, &le
	}
	if err != nil {
		le := LoadError{
			Field: fmt.Sprintf("spec.sinks[name=%s].%s", sinkName, fieldPath),
			Message: fmt.Sprintf(
				"Sink %q (%s) %s: failed to read Secret %q in namespace %q: %v.",
				sinkName, sinkContext, fieldPath, ref.Name, f.Namespace, err,
			),
		}
		return nil, &le
	}

	data, ok := secret.Data[ref.Key]
	if !ok {
		le := LoadError{
			Field: fmt.Sprintf("spec.sinks[name=%s].%s.key", sinkName, fieldPath),
			Message: fmt.Sprintf(
				"Sink %q (%s) %s: Secret %q has no key %q. Available keys: %v. Update %s.key to one of these, or add the missing key to the Secret.",
				sinkName, sinkContext, fieldPath, ref.Name, ref.Key, secretKeyNames(secret), fieldPath,
			),
		}
		return nil, &le
	}
	if len(data) == 0 {
		le := LoadError{
			Field: fmt.Sprintf("spec.sinks[name=%s].%s", sinkName, fieldPath),
			Message: fmt.Sprintf(
				"Sink %q (%s) %s: Secret %q key %q has empty value. Populate the Secret data or update %s.key.",
				sinkName, sinkContext, fieldPath, ref.Name, ref.Key, fieldPath,
			),
		}
		return nil, &le
	}
	return data, nil
}

// secretKeyNames returns the sorted list of data keys present in the
// Secret, used for the "Available keys: [...]" diagnostic message.
// Sorted so the message is deterministic across test runs and across
// different K8s API server versions that may return Secret data in
// different map-iteration orders.
func secretKeyNames(s corev1.Secret) []string {
	out := make([]string, 0, len(s.Data))
	for k := range s.Data {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
