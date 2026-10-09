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

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// disaggregatedSetHandledKinds enumerates the WorkloadKinds this scraper
// handles. Pinned to disaggregatedset.x-k8s.io/v1.DisaggregatedSet (the
// LeaderWorkerSet project, lws >= 0.11), the only served version. See
// docs/external-crd-versions.md.
var disaggregatedSetHandledKinds = []WorkloadKind{
	{Group: "disaggregatedset.x-k8s.io", Version: "v1", Kind: "DisaggregatedSet"},
}

// DisaggregatedSetScraper extracts BOM inputs from DisaggregatedSet CRs
// (Design 007 §1): llm-d's prefill/decode disaggregated serving
// primitive on NVIDIA hardware (the wide expert-parallelism and P/D
// guides). A DisaggregatedSet is a list of roles, each of which inlines
// a complete LeaderWorkerSet template (metadata + spec), so the
// extraction is the LWS extraction applied once per role:
//
//	spec.roles[i].name
//	spec.roles[i].spec.replicas
//	spec.roles[i].spec.leaderWorkerTemplate.size
//	spec.roles[i].spec.leaderWorkerTemplate.leaderTemplate   (optional)
//	spec.roles[i].spec.leaderWorkerTemplate.workerTemplate
//	spec.slices
//
// Every component a role produces carries disaggregatedset.role (the
// role name) and lws.role (leader | worker); container components also
// carry lws.size, lws.replicas and disaggregatedset.slices when set.
// Evidence locators are rooted at the role's template, so a finding is
// traceable to the exact field in the CR.
//
// One AIBOM per DisaggregatedSet, keyed to its UID. The LeaderWorkerSets
// it materializes (one per role) are controller-owned by it and roll up
// into this document under Design 005, their StatefulSets and pods with
// them, so pod-status digests reach the DisaggregatedSet's document.
// DisaggregatedSetRoleScaler is a scaling adapter, not a workload, and
// is not read.
type DisaggregatedSetScraper struct {
	lws *LeaderWorkerSetScraper
	now func() time.Time
}

// NewDisaggregatedSetScraper constructs a scraper. Pass nil verifier for
// NoopVerifier.
func NewDisaggregatedSetScraper(verifier SignatureVerifier) *DisaggregatedSetScraper {
	return &DisaggregatedSetScraper{lws: NewLeaderWorkerSetScraper(verifier), now: time.Now}
}

// Name returns the stable scraper identifier.
func (s *DisaggregatedSetScraper) Name() string { return "inference.disaggregatedset" }

// HandlesKind reports whether this scraper produces BOM inputs for the
// given workload kind. Only disaggregatedset.x-k8s.io/v1.DisaggregatedSet.
func (s *DisaggregatedSetScraper) HandlesKind(k WorkloadKind) bool {
	for _, kk := range disaggregatedSetHandledKinds {
		if kk == k {
			return true
		}
	}
	return false
}

// Scrape extracts BOM inputs from the DisaggregatedSet CR. The
// Workload.Object MUST be a *unstructured.Unstructured; cfg MUST NOT be
// nil. A role that is not an object, or a template that does not decode,
// is recorded on Errors and skipped; the other roles still extract.
func (s *DisaggregatedSetScraper) Scrape(ctx context.Context, w Workload, cfg *InferenceConfig) (*BOMInputs, error) {
	if w.Object == nil {
		return nil, fmt.Errorf("inference.disaggregatedset: workload Object is nil for kind %s/%s/%s",
			w.Kind.Group, w.Kind.Version, w.Kind.Kind)
	}
	u, ok := w.Object.(*unstructured.Unstructured)
	if !ok {
		return nil, fmt.Errorf("inference.disaggregatedset: workload Object is %T, want *unstructured.Unstructured",
			w.Object)
	}
	if cfg == nil {
		return nil, fmt.Errorf("inference.disaggregatedset: cfg is nil; reconciler must pass a non-nil InferenceConfig")
	}
	t := s.now().UTC()
	inputs := &BOMInputs{
		ScraperName:     s.Name(),
		Category:        CategoryInference,
		ScrapeTimestamp: t,
	}

	setProps := map[string]string{}
	if slices, found, _ := unstructured.NestedInt64(u.Object, "spec", "slices"); found {
		setProps["disaggregatedset.slices"] = strconv.FormatInt(slices, 10)
	}

	annotationMaps := []map[string]string{u.GetAnnotations()}
	roles, found, _ := unstructured.NestedSlice(u.Object, "spec", "roles")
	if !found {
		if _, present := nestedPresent(u.Object, "spec", "roles"); present {
			inputs.Errors = append(inputs.Errors, fmt.Errorf("spec.roles: not a list; skipped"))
		}
	}
	for i, raw := range roles {
		roleLocator := fmt.Sprintf("spec.roles[%d]", i)
		role, ok := raw.(map[string]interface{})
		if !ok {
			inputs.Errors = append(inputs.Errors, fmt.Errorf("%s: not an object; skipped", roleLocator))
			continue
		}
		name, _, _ := unstructured.NestedString(role, "name")
		if name == "" {
			inputs.Errors = append(inputs.Errors, fmt.Errorf("%s.name: missing; role identified by index", roleLocator))
			name = strconv.Itoa(i)
		}

		// Group shape per role: the role's LWS replicas and group size,
		// plus the set-wide slice count. Absent fields are omitted.
		groupProps := map[string]string{}
		for k, v := range setProps {
			groupProps[k] = v
		}
		if size, found, _ := unstructured.NestedInt64(role, "spec", "leaderWorkerTemplate", "size"); found {
			groupProps["lws.size"] = strconv.FormatInt(size, 10)
		}
		if replicas, found, _ := unstructured.NestedInt64(role, "spec", "replicas"); found {
			groupProps["lws.replicas"] = strconv.FormatInt(replicas, 10)
		}

		// The role's own metadata (the LWS it materializes) may carry
		// signature references; it sits between the set and the
		// templates in precedence.
		if ann, found, _ := unstructured.NestedStringMap(role, "metadata", "annotations"); found && len(ann) > 0 {
			annotationMaps = append(annotationMaps, ann)
		}

		for _, lwsRole := range []string{"leader", "worker"} {
			key := lwsRole + "Template"
			pt, found, _ := unstructured.NestedMap(role, "spec", "leaderWorkerTemplate", key)
			if !found {
				if _, present := nestedPresent(role, "spec", "leaderWorkerTemplate", key); present {
					inputs.Errors = append(inputs.Errors,
						fmt.Errorf("%s.spec.leaderWorkerTemplate.%s: not an object; skipped", roleLocator, key))
				}
				continue
			}
			locator := roleLocator + ".spec.leaderWorkerTemplate." + key
			componentProps := map[string]string{"disaggregatedset.role": name, "lws.role": lwsRole}
			if tmplAnnotations := s.lws.scrapeTemplate(inputs, pt, locator, componentProps, groupProps, w.Pods, cfg); tmplAnnotations != nil {
				annotationMaps = append(annotationMaps, tmplAnnotations)
			}
		}
	}

	inputs.Components = append(inputs.Components,
		extractAnnotationModels(u.GetAnnotations(), SourceWorkloadAnnotation, "metadata.annotations")...)

	// Signature claims: the set's own annotations first, then each
	// role's metadata, then each template's — outermost wins, the same
	// precedence inference.lws applies.
	applySignatures(ctx, s.lws.verifier, inputs.Components, annotationMaps...)

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
