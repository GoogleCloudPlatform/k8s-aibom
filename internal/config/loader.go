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
	"io"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	aibomv1alpha1 "github.com/GoogleCloudPlatform/k8s-aibom/api/v1alpha1"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/scraper"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/sink"
)

// Loader translates an AIBOMControllerConfig CR into a Snapshot,
// applying the fallback semantics documented in the project memory
// entry "AIBOMControllerConfig v1 behavior":
//
//   - Missing CR  → DefaultSnapshot, no errors.
//   - Invalid CR  → DefaultSnapshot, errors populated.
//   - Valid CR    → snapshot derived from spec, no errors.
//   - API error   → Go error returned (caller retries).
//
// All-or-nothing on invalid: when any semantic validation fails,
// the snapshot is the compiled-defaults fallback regardless of how
// many fields were individually valid. Partial-load was deliberately
// rejected — see project memory for the rationale.
type Loader struct {
	// Client reads the AIBOMControllerConfig CR. Required.
	Client client.Client

	// SinkFactory constructs external sinks from the CR's sink list.
	// Required. Tests may use NoopSinkFactory; production uses the
	// Checkpoint 3 implementation that reads Secrets.
	SinkFactory SinkFactory

	// ConfigName is the AIBOMControllerConfig name to read. Defaults
	// to "default" (the singleton convention) when empty.
	ConfigName string
}

// Load reads the configured AIBOMControllerConfig CR and returns a
// LoadResult.
//
// Returns a Go error only on transient API failure (caller should
// retry). Missing-CR and invalid-CR both return a successful
// LoadResult with the appropriate Snapshot and Errors fields.
func (l *Loader) Load(ctx context.Context) (LoadResult, error) {
	name := l.ConfigName
	if name == "" {
		name = DefaultConfigName
	}
	cr := &aibomv1alpha1.AIBOMControllerConfig{}
	err := l.Client.Get(ctx, types.NamespacedName{Name: name}, cr)
	if apierrors.IsNotFound(err) {
		// Missing-CR fallback: compiled defaults, no errors.
		// Caller's AIBOMControllerConfigReconciler will surface this
		// state via a K8s Event on the controller's own Deployment
		// (no CR exists to set conditions on).
		return LoadResult{Snapshot: DefaultSnapshot()}, nil
	}
	if err != nil {
		return LoadResult{}, fmt.Errorf("get AIBOMControllerConfig/%s: %w", name, err)
	}
	return l.parseSpec(ctx, cr), nil
}

// parseSpec translates a fetched AIBOMControllerConfig CR into a
// LoadResult. Applies the all-or-nothing rule: any LoadError causes
// the returned Snapshot to be DefaultSnapshot, regardless of how
// many fields parsed successfully.
func (l *Loader) parseSpec(ctx context.Context, cr *aibomv1alpha1.AIBOMControllerConfig) LoadResult {
	var errs []LoadError

	// Parse namespaceSelector — typically fast since the CRD schema
	// pre-validates the label-selector shape, but customers can
	// still produce invalid expressions via matchExpressions.
	namespaceSelector, nsErr := parseNamespaceSelector(cr.Spec.Discovery.NamespaceSelector)
	if nsErr != nil {
		errs = append(errs, *nsErr)
	}

	// Parse inference runtime patterns. Per-pattern errors are
	// collected so customers can see ALL failing patterns in one
	// pass (matches the all-or-nothing-on-invalid documentation).
	patterns, patternErrs := parsePatterns(cr.Spec.Discovery.InferenceRuntimeImagePatterns)
	errs = append(errs, patternErrs...)

	// Validate sink shapes BEFORE invoking the factory. Shape errors
	// (Type=GCS but GCS body nil, duplicate names, etc.) are loader-
	// detectable without needing the K8s API. The factory is only
	// invoked when shape validation passes — keeps test-side stubs
	// simple and avoids spurious Secret-fetch attempts against
	// already-broken specs.
	shapeErrs := validateSinkShapes(cr.Spec.Sinks)
	errs = append(errs, shapeErrs...)

	var sinks []sink.Sink
	if len(shapeErrs) == 0 {
		var buildErrs []LoadError
		sinks, buildErrs = l.SinkFactory.BuildSinks(ctx, cr.Spec.Sinks)
		errs = append(errs, buildErrs...)
	}

	// All-or-nothing rule: any LoadError → fallback to defaults.
	if len(errs) > 0 {
		// Prevent resource leaks: if we built sinks but are discarding them
		// due to other validation errors, close them immediately.
		if len(sinks) > 0 {
			for _, s := range sinks {
				if closer, ok := s.(io.Closer); ok {
					_ = closer.Close()
				}
			}
		}
		return LoadResult{Snapshot: DefaultSnapshot(), Errors: errs}
	}

	// Construct snapshot from spec.
	snap := &Snapshot{
		Patterns:                 patterns,
		InlineThreshold:          resolveInlineThreshold(cr.Spec.BOMGeneration.InlineThresholdBytes),
		StaleThresholdReconciles: resolveStaleThreshold(cr.Spec.BOMGeneration.StaleThresholdReconciles),
		NamespaceSelector:        namespaceSelector,
		ExternalSinks:            sinks,
		Source:                   SourceConfigCR,
		SourceGeneration:         cr.Generation,
		LoadedAt:                 time.Now(),
	}
	return LoadResult{Snapshot: snap}
}

// resolveInlineThreshold returns the CR-specified threshold or the
// compiled default when unset. CRD validation enforces Minimum=1024
// but a zero value can still appear if the CRD is bypassed (e.g.,
// older API version, controller-runtime path that doesn't apply
// defaults). Defensive: treat <= 0 as "use compiled default."
func resolveInlineThreshold(crValue int64) int64 {
	if crValue <= 0 {
		return DefaultInlineThresholdBytes
	}
	if crValue < 1024 {
		return 1024
	}
	if crValue > 1048576 {
		return 1048576
	}
	return crValue
}

// parseNamespaceSelector materializes the CR's *metav1.LabelSelector
// into a labels.Selector. Returns the default selector when the CR
// field is nil; returns a LoadError when materialization fails.
//
// Empty selector ({} with no matchLabels and no matchExpressions) is
// honored literally — it materializes to labels.Everything() which
// matches all namespaces. This is potentially security-relevant; the
// customer must have written {} explicitly and the CRD schema does
// not reject it. v1 trusts the cluster admin's explicit choice.
func parseNamespaceSelector(sel *metav1.LabelSelector) (labels.Selector, *LoadError) {
	if sel == nil {
		return DefaultNamespaceSelector(), nil
	}
	s, err := metav1.LabelSelectorAsSelector(sel)
	if err != nil {
		le := errNamespaceSelectorInvalid(err.Error())
		return nil, &le
	}
	return s, nil
}

// parsePatterns translates CR runtime image patterns into the scraper's
// InferenceConfig. Starts from the compiled-in default config so the
// env-var / arg-flag / volume-path allowlists (which the CR does NOT
// expose in v1) are preserved; only the RuntimeImagePatterns list is
// overridden when the CR specifies any.
//
// Per-pattern compile failures are collected as LoadErrors. The
// returned InferenceConfig contains successfully-compiled patterns;
// the loader's all-or-nothing rule means this partial-success config
// won't actually be used when errors > 0.
func parsePatterns(specs []aibomv1alpha1.RuntimeImagePattern) (*scraper.InferenceConfig, []LoadError) {
	if len(specs) == 0 {
		// No override; compiled defaults handle everything.
		return scraper.DefaultV1Config(), nil
	}
	var errs []LoadError
	cfg := scraper.DefaultV1Config()
	// Replace defaults with customer-supplied list. The other three
	// allowlists (modelEnvVarNames, modelArgFlags, modelVolumePathPrefixes)
	// remain at compiled defaults per the PRD §FR6.4 contract.
	cfg.RuntimeImagePatterns = make([]scraper.RuntimeImagePattern, 0, len(specs))
	for i, p := range specs {
		rp, err := scraper.NewRuntimeImagePattern(p.Runtime, p.Pattern)
		if err != nil {
			errs = append(errs, errPatternCompileFailed(i, p.Runtime, p.Pattern, err))
			continue
		}
		cfg.RuntimeImagePatterns = append(cfg.RuntimeImagePatterns, rp)
	}
	return cfg, errs
}

// validateSinkShapes checks each SinkConfig against the
// discriminator-body rule (Type=X requires X-body populated, no other
// body populated) and the unique-name rule. Loader-side enforcement
// because CRD-side CEL would add debugging complexity and produce
// less helpful error messages.
//
// All shape errors are returned; the customer can see every failing
// sink in one pass.
func validateSinkShapes(sinks []aibomv1alpha1.SinkConfig) []LoadError {
	var errs []LoadError
	seenNames := make(map[string]int) // name -> first occurrence index
	for i, s := range sinks {
		if firstIdx, dup := seenNames[s.Name]; dup {
			errs = append(errs, errSinkDuplicateName(s.Name))
			_ = firstIdx // could be added to message; current shape is sufficient
		} else {
			seenNames[s.Name] = i
		}

		switch s.Type {
		case aibomv1alpha1.SinkTypeGCS:
			if s.GCS == nil {
				errs = append(errs, errSinkMissingTypeBody(s.Name, string(s.Type)))
			}
			if s.Webhook != nil {
				errs = append(errs, errSinkExtraTypeBody(s.Name, string(s.Type), "webhook"))
			}
		case aibomv1alpha1.SinkTypeWebhook:
			if s.Webhook == nil {
				errs = append(errs, errSinkMissingTypeBody(s.Name, string(s.Type)))
			}
			if s.GCS != nil {
				errs = append(errs, errSinkExtraTypeBody(s.Name, string(s.Type), "gcs"))
			}
		default:
			// CRD enum should prevent this. Defensive in case the
			// CRD schema is bypassed (older client, raw API call).
			errs = append(errs, LoadError{
				Field: fmt.Sprintf("spec.sinks[name=%s].type", s.Name),
				Message: fmt.Sprintf(
					"Sink %q has unknown Type=%q. Allowed values: GCS, Webhook.",
					s.Name, s.Type,
				),
			})
		}
	}
	return errs
}

func resolveStaleThreshold(crValue int32) int32 {
	if crValue <= 0 {
		return DefaultStaleThresholdReconciles
	}
	return crValue
}
