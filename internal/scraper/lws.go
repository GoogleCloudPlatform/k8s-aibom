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
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

// lwsHandledKinds enumerates the WorkloadKinds this scraper handles.
// Pinned to leaderworkerset.x-k8s.io/v1.LeaderWorkerSet, the only
// served version. See docs/external-crd-versions.md.
var lwsHandledKinds = []WorkloadKind{
	{Group: "leaderworkerset.x-k8s.io", Version: "v1", Kind: "LeaderWorkerSet"},
}

// lwsTemplateLocator is the path of the LWS pod templates within the CR.
const lwsTemplateLocator = "spec.leaderWorkerTemplate"

// LeaderWorkerSetScraper extracts BOM inputs from kubernetes-sigs/lws
// LeaderWorkerSet CRs (Design 003 §2), the multi-host serving primitive
// used by vLLM / SGLang multi-node deployments and llm-d's wide
// expert-parallelism path.
//
// An LWS carries two ordinary PodTemplateSpecs — an optional
// leaderTemplate and a required workerTemplate — so the existing
// inference extraction applies unchanged. What this scraper adds is the
// watch, the RBAC, locators that say which template carried each fact
// (spec.leaderWorkerTemplate.leaderTemplate… / …workerTemplate…), an
// lws.role property on every extracted component, and the group shape
// (lws.size, lws.replicas) on container components.
//
// One AIBOM per LWS, keyed to the LWS UID: the LWS is the controlling
// template, not the StatefulSets it materializes. Those roll up to this
// AIBOM under the ownership roll-up (Design 005), which also hands the
// reconciler their pods, so digests resolve from pod status.
type LeaderWorkerSetScraper struct {
	inner    *InferenceSpecScraper
	verifier SignatureVerifier
	now      func() time.Time
}

// NewLeaderWorkerSetScraper constructs a scraper. Pass nil verifier for
// NoopVerifier.
func NewLeaderWorkerSetScraper(verifier SignatureVerifier) *LeaderWorkerSetScraper {
	if verifier == nil {
		verifier = NoopVerifier{}
	}
	return &LeaderWorkerSetScraper{
		inner:    NewInferenceSpecScraper(verifier),
		verifier: verifier,
		now:      time.Now,
	}
}

// Name returns the stable scraper identifier.
func (s *LeaderWorkerSetScraper) Name() string { return "inference.lws" }

// HandlesKind reports whether this scraper produces BOM inputs for the
// given workload kind. Only leaderworkerset.x-k8s.io/v1.LeaderWorkerSet.
func (s *LeaderWorkerSetScraper) HandlesKind(k WorkloadKind) bool {
	for _, kk := range lwsHandledKinds {
		if kk == k {
			return true
		}
	}
	return false
}

// Scrape extracts BOM inputs from the LeaderWorkerSet CR. The
// Workload.Object MUST be a *unstructured.Unstructured; cfg MUST NOT be
// nil (both templates go through the pattern and allowlist extraction).
func (s *LeaderWorkerSetScraper) Scrape(ctx context.Context, w Workload, cfg *InferenceConfig) (*BOMInputs, error) {
	if w.Object == nil {
		return nil, fmt.Errorf("inference.lws: workload Object is nil for kind %s/%s/%s",
			w.Kind.Group, w.Kind.Version, w.Kind.Kind)
	}
	u, ok := w.Object.(*unstructured.Unstructured)
	if !ok {
		return nil, fmt.Errorf("inference.lws: workload Object is %T, want *unstructured.Unstructured",
			w.Object)
	}
	if cfg == nil {
		return nil, fmt.Errorf("inference.lws: cfg is nil; reconciler must pass a non-nil InferenceConfig")
	}
	t := s.now().UTC()
	inputs := &BOMInputs{
		ScraperName:     s.Name(),
		Category:        CategoryInference,
		ScrapeTimestamp: t,
	}

	// Group shape, recorded on container components (the thing that
	// scales). Absent fields are omitted, never written as zero.
	groupProps := map[string]string{}
	if size, found, _ := unstructured.NestedInt64(u.Object, "spec", "leaderWorkerTemplate", "size"); found {
		groupProps["lws.size"] = strconv.FormatInt(size, 10)
	}
	if replicas, found, _ := unstructured.NestedInt64(u.Object, "spec", "replicas"); found {
		groupProps["lws.replicas"] = strconv.FormatInt(replicas, 10)
	}

	var annotationMaps []map[string]string
	for _, role := range []string{"leader", "worker"} {
		key := role + "Template"
		pt, found, _ := unstructured.NestedMap(u.Object, "spec", "leaderWorkerTemplate", key)
		if !found {
			if _, present := nestedPresent(u.Object, "spec", "leaderWorkerTemplate", key); present {
				inputs.Errors = append(inputs.Errors,
					fmt.Errorf("%s.%s: not an object; skipped", lwsTemplateLocator, key))
			}
			continue
		}
		if tmplAnnotations := s.scrapeTemplate(inputs, pt, lwsTemplateLocator+"."+key, map[string]string{"lws.role": role}, groupProps, w.Pods, cfg); tmplAnnotations != nil {
			annotationMaps = append(annotationMaps, tmplAnnotations)
		}
	}

	inputs.Components = append(inputs.Components,
		extractAnnotationModels(u.GetAnnotations(), SourceWorkloadAnnotation, "metadata.annotations")...)

	// Signature claims: the LWS's own annotations first, then each
	// template's — the same precedence inference.spec applies to a
	// Deployment and its pod template.
	applySignatures(ctx, s.verifier, inputs.Components,
		append([]map[string]string{u.GetAnnotations()}, annotationMaps...)...)

	sortComponents(inputs.Components)
	if len(inputs.Components) > MaxComponentsPerDocument {
		inputs.TruncatedComponents = len(inputs.Components) - MaxComponentsPerDocument
		inputs.Components = inputs.Components[:MaxComponentsPerDocument]
	}
	inputs.Confidence = aggregateConfidence(inputs.Components)
	inputs.Provenance = []Provenance{{
		ScraperName:     s.Name(),
		ScraperVersion:  ScraperVersion,
		ScrapeMethod:    "spec",
		ScrapeTimestamp: t,
	}}
	return inputs, nil
}

// scrapeTemplate decodes one template, runs the shared extraction with
// locators rooted at locator, tags every resulting component with
// componentProps (lws.role, and for a DisaggregatedSet also the role
// name) and container components with the group shape, and returns the
// template's annotations for signature lookup (nil when the template
// did not decode; the failure is recorded, never fatal). Shared with
// DisaggregatedSetScraper (Design 007 §1), which is this extraction
// applied once per role.
func (s *LeaderWorkerSetScraper) scrapeTemplate(inputs *BOMInputs, pt map[string]interface{}, locator string, componentProps, groupProps map[string]string, pods []corev1.Pod, cfg *InferenceConfig) map[string]string {
	var tmpl corev1.PodTemplateSpec
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(pt, &tmpl); err != nil {
		inputs.Errors = append(inputs.Errors, fmt.Errorf("%s: not a PodTemplateSpec: %w; skipped", locator, err))
		return nil
	}
	before := len(inputs.Components)
	s.inner.scrapePodSpecAt(inputs, &tmpl.Spec, tmpl.Annotations, pods, cfg,
		locator+".spec", locator+".metadata.annotations")
	for k := before; k < len(inputs.Components); k++ {
		c := &inputs.Components[k]
		if c.Properties == nil {
			c.Properties = map[string]string{}
		}
		for pk, pv := range componentProps {
			c.Properties[pk] = pv
		}
		if c.Type == ComponentContainer {
			for pk, pv := range groupProps {
				c.Properties[pk] = pv
			}
		}
	}
	if tmpl.Annotations == nil {
		return map[string]string{}
	}
	return tmpl.Annotations
}

// nestedPresent reports whether a key exists at the path regardless of
// its type (NestedMap reports found=false for a present non-map value,
// which we want to surface as a malformed-field error rather than
// silently treat as absent).
func nestedPresent(obj map[string]interface{}, fields ...string) (interface{}, bool) {
	v, found, _ := unstructured.NestedFieldNoCopy(obj, fields...)
	return v, found
}
