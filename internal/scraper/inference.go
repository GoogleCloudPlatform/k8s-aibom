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
	"fmt"
	"sort"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

// ScraperVersion is the version label stamped into BOMInputs.Provenance.
// Pre-release while the project is in early development; will be
// ldflags-stamped at build time when releases start.
const ScraperVersion = "0.1.0"

// InferenceSpecScraper is the v1 lighthouse Scraper. It extracts BOM
// inputs from inference workloads via their Kubernetes spec and the status
// of their running pods (per the A1 image-digest-resolution policy).
//
// Handles the apps/v1 pod-template-bearing kinds: Deployment, StatefulSet,
// and DaemonSet. KServe InferenceService is handled separately by
// KServeInferenceServiceScraper (different shape — declared runtime +
// storage URI rather than image pattern + pod template).
//
// The Scraper interface is workload-type-neutral; this implementation
// is inference-specific via the active *InferenceConfig passed at
// Scrape time.
//
// Per the project's "scrapers are honest, not clever" rule: this scraper
// only emits Components for things it directly observes, with correct
// Evidence attribution. It never normalizes, infers, or canonicalizes
// values beyond pattern matching against the configured allowlists.
//
// Configuration is per-call, not stateful: the Scrape method accepts
// the active *InferenceConfig as a parameter and threads it through
// every internal helper. The scraper holds NO mutable config state,
// so hot-reload is the caller's responsibility (and is trivially
// correct: the next Scrape call sees whatever config the caller passes).
type InferenceSpecScraper struct {
	verifier SignatureVerifier
	now      func() time.Time
}

// NewInferenceSpecScraper constructs an InferenceSpecScraper. Pass nil
// verifier to use NoopVerifier. Configuration is passed per-call to
// Scrape rather than at construction; see the type-level docstring.
func NewInferenceSpecScraper(verifier SignatureVerifier) *InferenceSpecScraper {
	if verifier == nil {
		verifier = NoopVerifier{}
	}
	return &InferenceSpecScraper{
		verifier: verifier,
		now:      time.Now,
	}
}

// Name returns the stable identifier for this scraper.
func (s *InferenceSpecScraper) Name() string { return "inference.spec" }

// inferenceSpecHandledKinds enumerates the WorkloadKinds this scraper
// handles. All three apps/v1 pod-template-bearing kinds share an
// identical extraction path (scrapePodSpec); they're listed here so the
// discovery layer routes them to this scraper rather than expecting a
// kind-specific scraper.
//
// KServe InferenceService is NOT in this set — it has a different
// shape (declared runtime + storage URI, no pod template) and is
// handled by KServeInferenceServiceScraper.
//
// Per the project's "honest, not clever" rule, if any of these kinds'
// extraction logic ever diverges meaningfully (different field paths,
// different evidence sourcing rules), that's a signal to split into
// per-kind scrapers rather than letting the type switch in Scrape grow
// into a tangle.
var inferenceSpecHandledKinds = []WorkloadKind{
	{Group: "apps", Version: "v1", Kind: "Deployment"},
	{Group: "apps", Version: "v1", Kind: "StatefulSet"},
	{Group: "apps", Version: "v1", Kind: "DaemonSet"},
}

// HandlesKind reports whether this scraper produces BOM inputs for the
// given workload kind.
func (s *InferenceSpecScraper) HandlesKind(k WorkloadKind) bool {
	for _, kk := range inferenceSpecHandledKinds {
		if kk == k {
			return true
		}
	}
	return false
}

// Scrape extracts BOM inputs from the workload. The Scraper interface
// contract: identical Workload inputs (including pod state) produce
// identical BOMInputs modulo ScrapeTimestamp.
//
// cfg MUST NOT be nil — see the Scraper interface contract. The
// reconciler is responsible for substituting DefaultV1Config when
// its snapshot's Patterns field is nil-shaped.
func (s *InferenceSpecScraper) Scrape(_ context.Context, w Workload, cfg *InferenceConfig) (*BOMInputs, error) {
	if w.Object == nil {
		return nil, fmt.Errorf("inference.spec: workload Object is nil for kind %s/%s/%s",
			w.Kind.Group, w.Kind.Version, w.Kind.Kind)
	}
	if cfg == nil {
		return nil, fmt.Errorf("inference.spec: cfg is nil; reconciler must pass a non-nil InferenceConfig")
	}
	t := s.now().UTC()
	inputs := &BOMInputs{
		ScraperName:     s.Name(),
		Category:        CategoryInference,
		ScrapeTimestamp: t,
	}

	switch obj := w.Object.(type) {
	case *appsv1.Deployment:
		s.scrapePodSpec(inputs, &obj.Spec.Template.Spec, obj.Spec.Template.Annotations, w.Pods, cfg)
	case *appsv1.StatefulSet:
		s.scrapePodSpec(inputs, &obj.Spec.Template.Spec, obj.Spec.Template.Annotations, w.Pods, cfg)
	case *appsv1.DaemonSet:
		s.scrapePodSpec(inputs, &obj.Spec.Template.Spec, obj.Spec.Template.Annotations, w.Pods, cfg)
	default:
		return nil, fmt.Errorf("inference.spec: unsupported object type %T for kind %s/%s/%s",
			w.Object, w.Kind.Group, w.Kind.Version, w.Kind.Kind)
	}

	// Annotations on the top-level workload object (independent of the
	// pod template's annotations, which scrapePodSpec already processed).
	inputs.Components = append(inputs.Components,
		extractAnnotationModels(w.Object.GetAnnotations(),
			SourceWorkloadAnnotation, "metadata.annotations")...)

	// Deterministic ordering: sort components by (Type, Name, Evidence.Locator)
	// so byte-stable BOM output is achievable downstream and tests don't
	// rely on map iteration order.
	sortComponents(inputs.Components)

	// Workload-level confidence aggregation
	inputs.Confidence = aggregateConfidence(inputs.Components)

	// Provenance
	inputs.Provenance = []Provenance{{
		ScraperName:     s.Name(),
		ScraperVersion:  ScraperVersion,
		ScrapeMethod:    "spec",
		ScrapeTimestamp: t,
	}}

	return inputs, nil
}

// scrapePodSpec runs all per-container extractions over a PodSpec plus its
// own annotations. Used by every workload kind whose spec contains a
// PodTemplateSpec — Deployment, StatefulSet, and DaemonSet share this
// extraction path. cfg flows from Scrape() and is passed unchanged to
// every helper.
func (s *InferenceSpecScraper) scrapePodSpec(inputs *BOMInputs, spec *corev1.PodSpec, podTemplateAnnotations map[string]string, pods []corev1.Pod, cfg *InferenceConfig) {
	for i, c := range spec.Containers {
		inputs.Components = append(inputs.Components,
			s.extractContainerComponent(c, false, i, pods, cfg)...)
		inputs.Components = append(inputs.Components,
			s.extractEnvVarModels(c, false, i, cfg)...)
		inputs.Components = append(inputs.Components,
			s.extractArgModels(c, false, i, cfg)...)
		inputs.Components = append(inputs.Components,
			s.extractVolumeMountModels(c, spec.Volumes, false, i, cfg)...)
	}
	for i, c := range spec.InitContainers {
		inputs.Components = append(inputs.Components,
			s.extractContainerComponent(c, true, i, pods, cfg)...)
		// Per "honest, not clever": init containers' env vars and args
		// are NOT scraped for model claims in v1. Init containers
		// typically don't serve models; treating their args/env as model
		// claims would be inference beyond what v1 should do. The image
		// is captured for fleet visibility.
	}
	inputs.Components = append(inputs.Components,
		extractAnnotationModels(podTemplateAnnotations,
			SourcePodTemplateAnnotation, "spec.template.metadata.annotations")...)
}

// sortComponents orders components deterministically. The order is:
// Type, Name, Evidence.Locator. Children slices are sorted recursively.
func sortComponents(cs []Component) {
	sort.SliceStable(cs, func(i, j int) bool {
		if cs[i].Type != cs[j].Type {
			return cs[i].Type < cs[j].Type
		}
		if cs[i].Name != cs[j].Name {
			return cs[i].Name < cs[j].Name
		}
		return cs[i].Evidence.Locator < cs[j].Evidence.Locator
	})
	for i := range cs {
		if len(cs[i].Children) > 0 {
			sortComponents(cs[i].Children)
		}
	}
}

// aggregateConfidence computes a workload-level Confidence from per-component
// confidences. Rule (per the project memory):
//   - Attributes with ConfidenceUnresolved are EXCLUDED from the aggregation.
//   - The workload-level confidence is the lowest tier among the remaining
//     attributes (Declared > Inferred).
//   - If every attribute is Unresolved, the workload is Unresolved and the
//     discovery layer decides whether to suppress it.
func aggregateConfidence(cs []Component) Confidence {
	seenDeclared := false
	seenInferred := false
	seenAny := false
	for _, c := range cs {
		if c.Type != ComponentApplication && c.Type != ComponentMLModel {
			continue
		}
		switch c.Confidence {
		case ConfidenceDeclared:
			seenDeclared = true
			seenAny = true
		case ConfidenceInferred:
			seenInferred = true
			seenAny = true
		case ConfidenceUnresolved:
			// excluded
		}
	}
	if !seenAny {
		return ConfidenceUnresolved
	}
	if seenInferred {
		return ConfidenceInferred
	}
	if seenDeclared {
		return ConfidenceDeclared
	}
	return ConfidenceUnresolved
}
