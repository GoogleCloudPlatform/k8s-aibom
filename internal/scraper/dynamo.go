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
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

// dynamoHandledKinds enumerates the WorkloadKinds this scraper handles.
// Pinned to nvidia.com/v1beta1.DynamoGraphDeployment. In the operator
// versions shipped today (dynamo-platform 1.2–1.4) the STORAGE version
// is still the deprecated v1alpha1 and every v1beta1 read goes through
// the Dynamo operator's conversion webhook; this scraper never decodes
// the v1alpha1 shape (spec.services map). See
// docs/external-crd-versions.md for the pinning policy, Design 003 §4
// for the extraction map, and #127 for the conversion-failure gap.
var dynamoHandledKinds = []WorkloadKind{
	{Group: "nvidia.com", Version: "v1beta1", Kind: "DynamoGraphDeployment"},
	// A standalone DynamoComponentDeployment (no graph parent) is its own
	// root (Design 003 §4 / Design 005 §4): the shared component spec is
	// read as a one-component graph with locators rooted at spec.
	{Group: "nvidia.com", Version: "v1beta1", Kind: "DynamoComponentDeployment"},
}

// dynamoComponent is one component to extract: its map and the locator
// root that names where it lives in the CR.
type dynamoComponent struct {
	m       map[string]interface{}
	locator string
}

// dynamoComponents returns the components of a graph
// (spec.components[i]) or the single component a standalone
// DynamoComponentDeployment is (spec). Non-object entries are reported
// as errors on inputs and skipped.
func dynamoComponents(u *unstructured.Unstructured, inputs *BOMInputs) []dynamoComponent {
	if u.GetKind() == "DynamoComponentDeployment" {
		spec, found, _ := unstructured.NestedMap(u.Object, "spec")
		if !found {
			return nil
		}
		return []dynamoComponent{{m: spec, locator: "spec"}}
	}
	raw, _, _ := unstructured.NestedSlice(u.Object, "spec", "components")
	out := make([]dynamoComponent, 0, len(raw))
	for i, item := range raw {
		m, ok := item.(map[string]interface{})
		if !ok {
			inputs.Errors = append(inputs.Errors,
				fmt.Errorf("spec.components[%d]: not an object (%T); skipped", i, item))
			continue
		}
		out = append(out, dynamoComponent{m: m, locator: fmt.Sprintf("spec.components[%d]", i)})
	}
	return out
}

// dynamoBackendRuntimes maps the API-validated spec.backendFramework
// enum (sglang | vllm | trtllm) onto the runtime names the pattern
// table already emits for the matching Dynamo runtime images, so a
// declared backend and an image-inferred runtime agree on the name and
// summarize as one runtime rather than two spellings of it.
var dynamoBackendRuntimes = map[string]string{
	"vllm":   "vllm",
	"sglang": "sglang",
	"trtllm": "tensorrt-llm",
}

// DynamoGraphDeploymentScraper extracts BOM inputs from NVIDIA Dynamo
// DynamoGraphDeployment CRs (Design 003 §4).
//
// A DynamoGraphDeployment declares an inference graph as typed
// components, each carrying a full PodTemplateSpec. The scraper reads:
//
//   - spec.backendFramework                → serving runtime (Declared;
//     the customer wrote it and the API server enforced the vocabulary)
//   - spec.components[i].modelRef.{name,revision} → model identity
//     (Declared), attributed to the component that carries it
//   - spec.components[i].type              → recorded as graph-topology
//     properties; roles never imply models
//   - spec.components[i].podTemplate and spec.components[i].roles[j].podTemplate
//     → the existing inference extraction (images, env/arg allowlists,
//     volume mounts, template annotations) with evidence locators
//     prefixed by the component path
//   - metadata.annotations (model.k8saibom.dev/*) → additional claims
//
// Binding caveat from the AICR review of Design 003: Dynamo runtime
// images carry no model identity, so there is NO image-path model
// derivation here. Absent modelRef and declared env/args, the model
// stays unresolved. The pattern table still attributes the runtime
// from the image (Inferred), which is a runtime fact, not a model one.
//
// Pods reach a graph through the ownership roll-up (Design 005): the
// reconciler passes the pods its DynamoComponentDeployments → Deployments
// / LWS / Grove chain owns, and the shared extraction resolves digests
// from their status exactly as for a Deployment.
//
// Access uses *unstructured.Unstructured; the dynamo operator's Go
// module is not a dependency (it pulls in gateway-api-inference-
// extension and more). The field surface read here is small and
// pinned in docs/external-crd-versions.md.
type DynamoGraphDeploymentScraper struct {
	// inner performs the per-pod-template extraction shared with the
	// apps/v1 kinds, parameterized by evidence-locator prefix.
	inner    *InferenceSpecScraper
	verifier SignatureVerifier
	now      func() time.Time
}

// NewDynamoGraphDeploymentScraper constructs a scraper. Pass nil
// verifier for NoopVerifier.
func NewDynamoGraphDeploymentScraper(verifier SignatureVerifier) *DynamoGraphDeploymentScraper {
	if verifier == nil {
		verifier = NoopVerifier{}
	}
	return &DynamoGraphDeploymentScraper{
		inner:    NewInferenceSpecScraper(verifier),
		verifier: verifier,
		now:      time.Now,
	}
}

// Name returns the stable scraper identifier.
func (s *DynamoGraphDeploymentScraper) Name() string { return "inference.dynamo" }

// HandlesKind reports whether this scraper produces BOM inputs for
// the given workload kind. Only nvidia.com/v1beta1.DynamoGraphDeployment.
func (s *DynamoGraphDeploymentScraper) HandlesKind(k WorkloadKind) bool {
	for _, kk := range dynamoHandledKinds {
		if kk == k {
			return true
		}
	}
	return false
}

// Scrape extracts BOM inputs from the DynamoGraphDeployment CR. The
// Workload.Object MUST be a *unstructured.Unstructured. cfg MUST NOT be
// nil: per-component pod templates go through the pattern and
// allowlist extraction exactly as a Deployment's template would.
func (s *DynamoGraphDeploymentScraper) Scrape(ctx context.Context, w Workload, cfg *InferenceConfig) (*BOMInputs, error) {
	if w.Object == nil {
		return nil, fmt.Errorf("inference.dynamo: workload Object is nil for kind %s/%s/%s",
			w.Kind.Group, w.Kind.Version, w.Kind.Kind)
	}
	u, ok := w.Object.(*unstructured.Unstructured)
	if !ok {
		return nil, fmt.Errorf("inference.dynamo: workload Object is %T, want *unstructured.Unstructured",
			w.Object)
	}
	if cfg == nil {
		return nil, fmt.Errorf("inference.dynamo: cfg is nil; reconciler must pass a non-nil InferenceConfig")
	}
	t := s.now().UTC()
	inputs := &BOMInputs{
		ScraperName:     s.Name(),
		Category:        CategoryInference,
		ScrapeTimestamp: t,
	}

	components := dynamoComponents(u, inputs)

	// 1. Declared serving runtime from the validated enum, carrying the
	// graph topology (component name → type) as properties. Topology is
	// an auditable fact about the deployment; it never implies a model.
	if backend, _, _ := unstructured.NestedString(u.Object, "spec", "backendFramework"); backend != "" {
		runtime := dynamoBackendRuntimes[backend]
		if runtime == "" {
			// The API server enforces the enum; an unknown value can
			// only come from a newer operator version. Record it
			// verbatim rather than guess a mapping.
			runtime = backend
		}
		props := map[string]string{
			"runtime.name":            runtime,
			"runtime.source":          "dynamo.backendFramework",
			"dynamo.backendFramework": backend,
		}
		for _, comp := range components {
			m := comp.m
			name, _, _ := unstructured.NestedString(m, "name")
			ctype, _, _ := unstructured.NestedString(m, "type")
			if name != "" && ctype != "" {
				props["dynamo.component."+TruncateString(name, MaxComponentNameLength)+".type"] = ctype
			}
		}
		inputs.Components = append(inputs.Components, Component{
			Type:       ComponentApplication,
			Name:       TruncateString(runtime, MaxComponentNameLength),
			Confidence: ConfidenceDeclared,
			Evidence: Evidence{
				Source:  SourceCRDField,
				Locator: "spec.backendFramework",
			},
			Properties: props,
		})
	}

	// 2. Per-component extraction: declared modelRef, then the pod
	// template(s) through the shared inference extraction.
	for _, comp := range components {
		m, base := comp.m, comp.locator
		compName, _, _ := unstructured.NestedString(m, "name")
		compType, _, _ := unstructured.NestedString(m, "type")

		if modelName, _, _ := unstructured.NestedString(m, "modelRef", "name"); modelName != "" {
			revision, _, _ := unstructured.NestedString(m, "modelRef", "revision")
			props := map[string]string{
				"identity.confidence": "claimed",
				"identity.source":     "dynamo.modelRef",
			}
			addDynamoComponentProps(props, compName, compType)
			if revision != "" {
				props["model.revision"] = TruncateString(revision, MaxComponentNameLength)
			}
			inputs.Components = append(inputs.Components, Component{
				Type:       ComponentMLModel,
				Name:       TruncateString(modelName, MaxComponentNameLength),
				Version:    TruncateString(revision, MaxComponentNameLength),
				Confidence: ConfidenceDeclared,
				Evidence: Evidence{
					Source:  SourceCRDField,
					Locator: base + ".modelRef.name",
				},
				Properties: props,
			})
		}

		if pt, found, _ := unstructured.NestedMap(m, "podTemplate"); found {
			s.scrapeTemplate(inputs, pt, base+".podTemplate", compName, compType, "", w.Pods, cfg)
		} else if _, present := m["podTemplate"]; present {
			inputs.Errors = append(inputs.Errors,
				fmt.Errorf("%s.podTemplate: not an object; skipped", base))
		}

		roles, _, _ := unstructured.NestedSlice(m, "roles")
		for j, rawRole := range roles {
			rm, ok := rawRole.(map[string]interface{})
			if !ok {
				inputs.Errors = append(inputs.Errors,
					fmt.Errorf("%s.roles[%d]: not an object (%T); skipped", base, j, rawRole))
				continue
			}
			roleName, _, _ := unstructured.NestedString(rm, "name")
			if pt, found, _ := unstructured.NestedMap(rm, "podTemplate"); found {
				s.scrapeTemplate(inputs, pt, fmt.Sprintf("%s.roles[%d].podTemplate", base, j),
					compName, compType, roleName, w.Pods, cfg)
			} else if _, present := rm["podTemplate"]; present {
				inputs.Errors = append(inputs.Errors,
					fmt.Errorf("%s.roles[%d].podTemplate: not an object; skipped", base, j))
			}
		}
	}

	// 3. Workload-level annotations (model.k8saibom.dev/* prefix).
	inputs.Components = append(inputs.Components,
		extractAnnotationModels(u.GetAnnotations(), SourceWorkloadAnnotation, "metadata.annotations")...)

	// 4. Signature verification (Design 002) from the DGD's own
	// annotations. Per-component template annotations were already
	// consumed as claim sources above; the DGD is the unit of analysis
	// and the place a platform team attaches the signature reference.
	applySignatures(ctx, s.verifier, inputs.Components, u.GetAnnotations())

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

// scrapeTemplate converts one unstructured PodTemplateSpec and runs the
// shared inference extraction over it with evidence locators rooted at
// locator. Every component it produces is tagged with the Dynamo
// component (and role) it came from, so an auditor can read which part
// of the graph carried each image or claim. A template that does not
// decode as a PodTemplateSpec is recorded as an error on the inputs and
// skipped; it never fails the scrape.
func (s *DynamoGraphDeploymentScraper) scrapeTemplate(inputs *BOMInputs, pt map[string]interface{}, locator, compName, compType, roleName string, pods []corev1.Pod, cfg *InferenceConfig) {
	var tmpl corev1.PodTemplateSpec
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(pt, &tmpl); err != nil {
		inputs.Errors = append(inputs.Errors, fmt.Errorf("%s: not a PodTemplateSpec: %w; skipped", locator, err))
		return
	}
	before := len(inputs.Components)
	s.inner.scrapePodSpecAt(inputs, &tmpl.Spec, tmpl.Annotations, pods, cfg,
		locator+".spec", locator+".metadata.annotations")
	for k := before; k < len(inputs.Components); k++ {
		if inputs.Components[k].Properties == nil {
			inputs.Components[k].Properties = map[string]string{}
		}
		addDynamoComponentProps(inputs.Components[k].Properties, compName, compType)
		if roleName != "" {
			inputs.Components[k].Properties["dynamo.role.name"] = TruncateString(roleName, MaxComponentNameLength)
		}
	}
}

// addDynamoComponentProps records which graph component a fact came
// from. Empty values are omitted rather than written as empty strings.
func addDynamoComponentProps(props map[string]string, compName, compType string) {
	if compName != "" {
		props["dynamo.component.name"] = TruncateString(compName, MaxComponentNameLength)
	}
	if compType != "" {
		props["dynamo.component.type"] = compType
	}
}
