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
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

// nimServiceHandledKinds enumerates the WorkloadKinds this scraper
// handles. Pinned to apps.nvidia.com/v1alpha1.NIMService, the NIM
// Operator's only served version. See docs/external-crd-versions.md.
var nimServiceHandledKinds = []WorkloadKind{
	{Group: "apps.nvidia.com", Version: "v1alpha1", Kind: "NIMService"},
}

// nimImagePrefix is the registry path under which NVIDIA publishes NIM
// images, one model per image (nvcr.io/nim/<org>/<name>). That
// one-model-per-image property is specific to NIM — it is what makes
// the image-path model derivation below valid, and why no such
// derivation exists for Dynamo or generic images.
const nimImagePrefix = "nvcr.io/nim/"

// NIMServiceScraper extracts BOM inputs from NVIDIA NIM Operator
// NIMService CRs (Design 003 §1).
//
//   - the CR kind itself            → runtime `nim` (Declared: the
//     customer chose NIM serving; no inference involved)
//   - spec.image.{repository,tag}   → container component; digest only
//     when the repository is digest-pinned (no pods are listed here)
//   - spec.env / spec.args           → model claims through the existing
//     env-name and arg-flag allowlists (NIM_MODEL_NAME,
//     NIM_SERVED_MODEL_NAME, --model, …)
//   - nvcr.io/nim/<org>/<name>      → model identity <org>/<name>
//     (Inferred), ONLY when nothing is declared via env, args or
//     annotations — declared sources always win (Design 003 open
//     question 3, resolved on AICR review)
//   - spec.storage.*                → model-source facts recorded as
//     properties on the runtime and every model component (nimCache is
//     one shape of several; emptyDir / pvc / hostPath are recorded too)
//   - spec.multiNode, spec.inferencePlatform → topology properties on
//     the runtime component
//   - metadata.annotations (model.k8saibom.dev/*) → additional claims;
//     signature references (Design 002)
//
// Not read: status.*, spec.initContainers, spec.sidecarContainers
// (operator-injected helpers, not the serving runtime), NIMCache
// contents (recorded as a reference, never resolved — Design 003
// non-goal 3).
type NIMServiceScraper struct {
	verifier SignatureVerifier
	now      func() time.Time
}

// NewNIMServiceScraper constructs a scraper. Pass nil verifier for
// NoopVerifier.
func NewNIMServiceScraper(verifier SignatureVerifier) *NIMServiceScraper {
	if verifier == nil {
		verifier = NoopVerifier{}
	}
	return &NIMServiceScraper{verifier: verifier, now: time.Now}
}

// Name returns the stable scraper identifier.
func (s *NIMServiceScraper) Name() string { return "inference.nimservice" }

// HandlesKind reports whether this scraper produces BOM inputs for the
// given workload kind. Only apps.nvidia.com/v1alpha1.NIMService.
func (s *NIMServiceScraper) HandlesKind(k WorkloadKind) bool {
	for _, kk := range nimServiceHandledKinds {
		if kk == k {
			return true
		}
	}
	return false
}

// Scrape extracts BOM inputs from the NIMService CR. The Workload.Object
// MUST be a *unstructured.Unstructured. cfg MUST NOT be nil: spec.env
// and spec.args go through the configured allowlists.
func (s *NIMServiceScraper) Scrape(ctx context.Context, w Workload, cfg *InferenceConfig) (*BOMInputs, error) {
	if w.Object == nil {
		return nil, fmt.Errorf("inference.nimservice: workload Object is nil for kind %s/%s/%s",
			w.Kind.Group, w.Kind.Version, w.Kind.Kind)
	}
	u, ok := w.Object.(*unstructured.Unstructured)
	if !ok {
		return nil, fmt.Errorf("inference.nimservice: workload Object is %T, want *unstructured.Unstructured",
			w.Object)
	}
	if cfg == nil {
		return nil, fmt.Errorf("inference.nimservice: cfg is nil; reconciler must pass a non-nil InferenceConfig")
	}
	t := s.now().UTC()
	inputs := &BOMInputs{
		ScraperName:     s.Name(),
		Category:        CategoryInference,
		ScrapeTimestamp: t,
	}

	storageProps := nimStorageProps(u)

	// 1. The kind itself is the declared runtime.
	rtProps := map[string]string{
		"runtime.name":   "nim",
		"runtime.source": "nimservice.kind",
	}
	for k, v := range storageProps {
		rtProps[k] = v
	}
	if platform, _, _ := unstructured.NestedString(u.Object, "spec", "inferencePlatform"); platform != "" {
		rtProps["nim.inferencePlatform"] = platform
	}
	if mn, found, _ := unstructured.NestedMap(u.Object, "spec", "multiNode"); found {
		rtProps["nim.multiNode"] = "true"
		if bt, _, _ := unstructured.NestedString(mn, "backendType"); bt != "" {
			rtProps["nim.multiNode.backendType"] = bt
		}
		if n, found, _ := unstructured.NestedInt64(mn, "parallelism", "tensor"); found {
			rtProps["nim.multiNode.parallelism.tensor"] = strconv.FormatInt(n, 10)
		}
		if n, found, _ := unstructured.NestedInt64(mn, "parallelism", "pipeline"); found {
			rtProps["nim.multiNode.parallelism.pipeline"] = strconv.FormatInt(n, 10)
		}
	}
	inputs.Components = append(inputs.Components, Component{
		Type:       ComponentApplication,
		Name:       "nim",
		Confidence: ConfidenceDeclared,
		Evidence:   Evidence{Source: SourceCRDField, Locator: "kind"},
		Properties: rtProps,
	})

	// 2. The serving image. Confidence on a container component means
	// digest provenance everywhere in this codebase (A1: never fabricate
	// a digest), so a tag-only reference is unresolved here exactly as
	// it is on a Deployment whose pods have not been listed.
	repo, _, _ := unstructured.NestedString(u.Object, "spec", "image", "repository")
	tag, _, _ := unstructured.NestedString(u.Object, "spec", "image", "tag")
	if repo != "" {
		ref := repo
		if tag != "" && !strings.Contains(repo, "@") {
			ref = repo + ":" + tag
		}
		name, parsedTag, digest := parseImageRef(ref)
		comp := Component{
			Type:     ComponentContainer,
			Name:     TruncateString(name, MaxComponentNameLength),
			Version:  TruncateString(parsedTag, MaxComponentNameLength),
			Evidence: Evidence{Source: SourceImageReference, Locator: "spec.image.repository"},
			Properties: map[string]string{
				"image.reference":  ref,
				"image.repository": repo,
				"image.tag":        tag,
			},
		}
		if digest != "" {
			comp.Hashes = map[string]string{"sha256": strings.TrimPrefix(digest, "sha256:")}
			comp.Confidence = ConfidenceDeclared
		} else {
			comp.Confidence = ConfidenceUnresolved
		}
		inputs.Components = append(inputs.Components, comp)
	}

	// 3. Declared claims: spec.env and spec.args through the allowlists,
	// then workload annotations. These are the sources that win.
	var declared []Component
	if rawEnv, found, err := unstructured.NestedSlice(u.Object, "spec", "env"); err != nil {
		inputs.Errors = append(inputs.Errors, fmt.Errorf("spec.env: %w; skipped", err))
	} else if found {
		env := make([]corev1.EnvVar, 0, len(rawEnv))
		for i, item := range rawEnv {
			m, ok := item.(map[string]interface{})
			if !ok {
				inputs.Errors = append(inputs.Errors, fmt.Errorf("spec.env[%d]: not an object (%T); skipped", i, item))
				continue
			}
			var e corev1.EnvVar
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(m, &e); err != nil {
				inputs.Errors = append(inputs.Errors, fmt.Errorf("spec.env[%d]: %w; skipped", i, err))
				continue
			}
			env = append(env, e)
		}
		declared = append(declared, envVarModels(env, "", "spec", cfg)...)
	}
	if rawArgs, found, err := unstructured.NestedSlice(u.Object, "spec", "args"); err != nil {
		inputs.Errors = append(inputs.Errors, fmt.Errorf("spec.args: %w; skipped", err))
	} else if found {
		// A non-string element is recorded and left empty in place, so
		// the remaining args keep their indices in evidence locators and
		// an empty value is never read as a model.
		args := make([]string, len(rawArgs))
		for i, item := range rawArgs {
			s, ok := item.(string)
			if !ok {
				inputs.Errors = append(inputs.Errors, fmt.Errorf("spec.args[%d]: not a string (%T); skipped", i, item))
				continue
			}
			args[i] = s
		}
		declared = append(declared, argModels(args, "", "spec", cfg)...)
	}
	declared = append(declared,
		extractAnnotationModels(u.GetAnnotations(), SourceWorkloadAnnotation, "metadata.annotations")...)
	inputs.Components = append(inputs.Components, declared...)

	// 4. Image-path derivation, only when nothing was declared. Valid
	// solely because nvcr.io/nim/ is one model per image.
	if len(declared) == 0 {
		if model := nimModelFromImagePath(repo); model != "" {
			inputs.Components = append(inputs.Components, Component{
				Type:       ComponentMLModel,
				Name:       TruncateString(model, MaxComponentNameLength),
				Confidence: ConfidenceInferred,
				Evidence:   Evidence{Source: SourceImageReference, Locator: "spec.image.repository"},
				Properties: map[string]string{
					"identity.confidence": "claimed",
					"identity.source":     "nim.imagePath",
				},
			})
		}
	}

	// 5. Storage facts travel with every model component so an auditor
	// reading one model entry sees where the weights were loaded from.
	for i := range inputs.Components {
		if inputs.Components[i].Type != ComponentMLModel {
			continue
		}
		if inputs.Components[i].Properties == nil {
			inputs.Components[i].Properties = map[string]string{}
		}
		for k, v := range storageProps {
			if _, exists := inputs.Components[i].Properties[k]; !exists {
				inputs.Components[i].Properties[k] = v
			}
		}
	}

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

// nimModelFromImagePath returns <org>/<name> for a repository of the
// form nvcr.io/nim/<org>/<name> and "" for anything else (other
// registries, deeper or shallower paths, digest-suffixed names are
// stripped first). Exact, case-sensitive prefix: NIM images are only
// published there.
func nimModelFromImagePath(repo string) string {
	if !strings.HasPrefix(repo, nimImagePrefix) {
		return ""
	}
	rest := strings.TrimPrefix(repo, nimImagePrefix)
	if at := strings.IndexByte(rest, '@'); at >= 0 {
		rest = rest[:at]
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return ""
	}
	return rest
}

// nimStorageProps records every storage shape declared under
// spec.storage. The operator picks one at runtime; which one is its
// business — the BOM records what the customer wrote. nim.storage.kind
// lists the shapes present, sorted, so the property is deterministic.
func nimStorageProps(u *unstructured.Unstructured) map[string]string {
	props := map[string]string{}
	var kinds []string
	if name, _, _ := unstructured.NestedString(u.Object, "spec", "storage", "nimCache", "name"); name != "" {
		kinds = append(kinds, "nimCache")
		props["nim.storage.nimCache.name"] = TruncateString(name, MaxComponentNameLength)
		if profile, _, _ := unstructured.NestedString(u.Object, "spec", "storage", "nimCache", "profile"); profile != "" {
			props["nim.storage.nimCache.profile"] = TruncateString(profile, MaxComponentNameLength)
		}
	}
	if pvc, found, _ := unstructured.NestedMap(u.Object, "spec", "storage", "pvc"); found && len(pvc) > 0 {
		kinds = append(kinds, "pvc")
		if name, _, _ := unstructured.NestedString(pvc, "name"); name != "" {
			props["nim.storage.pvc.name"] = TruncateString(name, MaxComponentNameLength)
		}
	}
	if hp, _, _ := unstructured.NestedString(u.Object, "spec", "storage", "hostPath"); hp != "" {
		kinds = append(kinds, "hostPath")
		props["nim.storage.hostPath"] = TruncateString(hp, MaxComponentNameLength)
	}
	if _, found, _ := unstructured.NestedMap(u.Object, "spec", "storage", "emptyDir"); found {
		kinds = append(kinds, "emptyDir")
	}
	if len(kinds) > 0 {
		sort.Strings(kinds)
		props["nim.storage.kind"] = strings.Join(kinds, ",")
	}
	return props
}
