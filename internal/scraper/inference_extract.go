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
	"fmt"
	"regexp"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// semverLikeTag recognizes tags that "look like" a semantic version:
// optional leading v, two or three numeric segments, optional pre-release
// or metadata suffix. Used to decide whether a runtime application
// Component's Version field should be populated from the image tag.
//
// Matches: v0.6.3, 0.6.3, v1.0, 24.01-py3, v0.6.3-rc1
// Rejects: latest, nightly-20251025, main, git SHAs, empty.
//
// See docs/scraper-heuristics.md for rationale.
var semverLikeTag = regexp.MustCompile(`^v?\d+\.\d+(\.\d+)?(-[\w.]+)?$`)

// looksSemverTag reports whether the given image tag is shaped like a
// semantic version. Empty input returns false.
func looksSemverTag(tag string) bool {
	if tag == "" {
		return false
	}
	return semverLikeTag.MatchString(tag)
}

// validSHA256Digest is the strict shape the scraper accepts as a sha256
// digest reference: literal "sha256:" plus exactly 64 lowercase hex
// characters. Anything else is treated as "no digest extracted" by the
// digest-extraction helpers below — the resulting Component is then
// marked ConfidenceUnresolved rather than passing a malformed value
// downstream to fail at BOM-schema validation time.
//
// This is part of the defensive-correctness pattern: validate at the
// scraper boundary so downstream sinks never receive a BOM that fails
// CycloneDX 1.6's hash content pattern (^[a-fA-F0-9]{64}$ for SHA-256).
var validSHA256Digest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// cleanSHA256Digest returns s unchanged if it matches validSHA256Digest,
// or "" otherwise. The empty return is the signal upstream that "we
// extracted something digest-shaped but it didn't pass format validation,
// treat as if we extracted nothing."
func cleanSHA256Digest(s string) string {
	if validSHA256Digest.MatchString(s) {
		return s
	}
	return ""
}

// modelAnnotationPrefix is the annotation key prefix that identifies
// declared model claims. Annotations under this prefix on either the
// workload object or the pod template are scraped.
const modelAnnotationPrefix = "model.k8saibom.dev/"

// parseImageRef splits an OCI image reference of the form
// "[registry[:port]/[namespace/]]name[:tag][@digest]" into its parts.
// Falls through to ("ref", "", "") if no separators are present.
//
// Tag and digest are returned without their separator characters. If the
// digest portion of the reference is not a valid sha256:HEX{64} string,
// the digest return is empty — see cleanSHA256Digest.
func parseImageRef(ref string) (name, tag, digest string) {
	if at := strings.LastIndex(ref, "@"); at >= 0 {
		digest = cleanSHA256Digest(ref[at+1:])
		ref = ref[:at]
	}
	prefix := ""
	lastComponent := ref
	if slash := strings.LastIndex(ref, "/"); slash >= 0 {
		prefix = ref[:slash+1]
		lastComponent = ref[slash+1:]
	}
	if colon := strings.LastIndex(lastComponent, ":"); colon >= 0 {
		tag = lastComponent[colon+1:]
		lastComponent = lastComponent[:colon]
	}
	name = prefix + lastComponent
	return name, tag, digest
}

// parseImageDigest extracts a "sha256:..." digest from a pod-status
// imageID string. K8s reports imageID in several forms depending on
// runtime; this handles the common ones:
//
//	"docker-pullable://image@sha256:abc..."
//	"image@sha256:abc..."
//	"sha256:abc..."
//
// Returns "" if no digest is recognized OR if the recognized digest
// does not pass strict validSHA256Digest format validation. Malformed
// imageIDs (truncated digests, non-hex characters, wrong case) are
// treated identically to "no digest reported" — the caller marks the
// resulting Component ConfidenceUnresolved. The BOM thus never carries
// a digest that fails CycloneDX 1.6 schema validation.
func parseImageDigest(imageID string) string {
	if at := strings.Index(imageID, "@sha256:"); at >= 0 {
		return cleanSHA256Digest(imageID[at+1:])
	}
	if strings.HasPrefix(imageID, "sha256:") {
		return cleanSHA256Digest(imageID)
	}
	return ""
}

// resolveContainerDigest returns the canonical sha256 digest for a
// container's image, plus the evidence source describing where the digest
// was found.
//
// Resolution order (per the A1 image-digest policy):
//  1. If the spec image reference already carries a digest, use that.
//     The evidence source is SourceImageReference because the digest came
//     from the image: field itself.
//  2. Otherwise, walk pod.status to find a container status matching the
//     container name and extract the digest from imageID. The evidence
//     source is SourcePodStatus. The pod status describes what is
//     ACTUALLY RUNNING, which may differ from the spec during a rollout —
//     the BOM intentionally reports the running digest.
//  3. If nothing resolves, return ("", "") and the caller marks the
//     component ConfidenceUnresolved.
//
// init=true selects initContainerStatuses on pods; init=false selects
// containerStatuses.
func resolveContainerDigest(specImage, containerName string, pods []corev1.Pod, init bool) (digest string, source EvidenceSource) {
	if _, _, d := parseImageRef(specImage); d != "" {
		return d, SourceImageReference
	}
	for _, pod := range pods {
		statuses := pod.Status.ContainerStatuses
		if init {
			statuses = pod.Status.InitContainerStatuses
		}
		for _, cs := range statuses {
			if cs.Name != containerName {
				continue
			}
			if d := parseImageDigest(cs.ImageID); d != "" {
				return d, SourcePodStatus
			}
		}
	}
	return "", ""
}

// extractContainerComponent produces a container-class Component for a
// single container in the workload's pod spec, including digest resolution
// and (if the image matches a configured runtime pattern) a sibling
// application-class Component identifying the runtime.
//
// Returns one or two Components: always the container; optionally the
// runtime application Component.
func (s *InferenceSpecScraper) extractContainerComponent(c corev1.Container, init bool, idx int, pods []corev1.Pod, cfg *InferenceConfig) []Component {
	name, tag, digestFromSpec := parseImageRef(c.Image)

	containerLocatorBase := fmt.Sprintf("spec.template.spec.containers[%d]", idx)
	if init {
		containerLocatorBase = fmt.Sprintf("spec.template.spec.initContainers[%d]", idx)
	}

	digest, digestSource := digestFromSpec, SourceImageReference
	digestLocator := containerLocatorBase + ".image"
	if digest == "" {
		digest, digestSource = resolveContainerDigest(c.Image, c.Name, pods, init)
		if digestSource == SourcePodStatus {
			digestLocator = "status.containerStatuses[name=" + c.Name + "].imageID"
			if init {
				digestLocator = "status.initContainerStatuses[name=" + c.Name + "].imageID"
			}
		}
	}

	comp := Component{
		Type:    ComponentContainer,
		Name:    name,
		Version: tag,
		Evidence: Evidence{
			Source:  SourceImageReference,
			Locator: digestLocator,
		},
		Properties: map[string]string{
			"image.reference": c.Image,
			"container.name":  c.Name,
			"container.init":  boolStr(init),
		},
	}
	if digest != "" {
		comp.Hashes = map[string]string{"sha256": strings.TrimPrefix(digest, "sha256:")}
		comp.Confidence = ConfidenceDeclared
		if digestSource == SourcePodStatus {
			comp.Evidence.Source = SourcePodStatus
		}
	} else {
		// Per A1: do not fabricate a digest. Mark unresolved so auditors
		// can see "not yet known" distinctly from "known".
		comp.Confidence = ConfidenceUnresolved
	}

	result := []Component{comp}

	// Runtime detection. Per the runtime-version heuristic: pattern-matched
	// runtime is always Inferred (not Declared), and the version is the
	// image tag only if it is semver-shaped — otherwise empty.
	if runtime, matched := cfg.DetectRuntime(c.Image); runtime != "" {
		runtimeVersion := ""
		if looksSemverTag(tag) {
			runtimeVersion = tag
		}
		result = append(result, Component{
			Type:       ComponentApplication,
			Name:       runtime,
			Version:    runtimeVersion,
			Confidence: ConfidenceInferred,
			Evidence: Evidence{
				Source:  SourceImagePattern,
				Locator: containerLocatorBase + ".image (pattern: " + matched.Pattern + ")",
			},
			Properties: map[string]string{
				"runtime.name":    runtime,
				"runtime.pattern": matched.Pattern,
				"container.name":  c.Name,
				"image.tag":       tag,
			},
		})
	}

	return result
}

// extractEnvVarModels emits ML-model-class Components for env vars whose
// names match the configured model-env-var allowlist.
func (s *InferenceSpecScraper) extractEnvVarModels(c corev1.Container, init bool, idx int, cfg *InferenceConfig) []Component {
	var out []Component
	listKey := "containers"
	if init {
		listKey = "initContainers"
	}
	for envIdx, e := range c.Env {
		if !cfg.IsModelEnvVarName(e.Name) {
			continue
		}
		if e.Value == "" {
			// Honest extraction: empty value is not a model identity.
			continue
		}
		out = append(out, Component{
			Type:       ComponentMLModel,
			Name:       TruncateString(e.Value, MaxComponentNameLength),
			Confidence: ConfidenceInferred,
			Evidence: Evidence{
				Source: SourceEnvVar,
				Locator: fmt.Sprintf("spec.template.spec.%s[%d].env[%d](%s)",
					listKey, idx, envIdx, e.Name),
			},
			Properties: map[string]string{
				"identity.confidence": "claimed",
				"identity.envVarName": e.Name,
				"container.name":      c.Name,
			},
		})
	}
	return out
}

// extractArgModels emits ML-model-class Components for container args
// whose flag name matches the configured model-arg-flag allowlist. Handles
// both `--flag value` (positional) and `--flag=value` (joined) forms.
func (s *InferenceSpecScraper) extractArgModels(c corev1.Container, init bool, idx int, cfg *InferenceConfig) []Component {
	var out []Component
	listKey := "containers"
	if init {
		listKey = "initContainers"
	}
	args := c.Args
	for i := 0; i < len(args); i++ {
		arg := args[i]
		// --flag=value form
		if eq := strings.IndexByte(arg, '='); eq >= 0 {
			flag := arg[:eq]
			value := strings.ReplaceAll(strings.ReplaceAll(arg[eq+1:], "\n", " "), "\r", " ")
			if cfg.IsModelArgFlag(flag) && value != "" && !strings.HasPrefix(value, "--") {
				out = append(out, Component{
					Type:       ComponentMLModel,
					Name:       TruncateString(value, MaxComponentNameLength),
					Confidence: ConfidenceDeclared,
					Evidence: Evidence{
						Source: SourceContainerArg,
						Locator: fmt.Sprintf("spec.template.spec.%s[%d].args[%d](%s=)",
							listKey, idx, i, flag),
					},
					Properties: map[string]string{
						"identity.confidence": "claimed",
						"identity.argFlag":    flag,
						"container.name":      c.Name,
					},
				})
			}
			continue
		}
		// --flag value form
		if cfg.IsModelArgFlag(arg) && i+1 < len(args) {
			value := strings.ReplaceAll(strings.ReplaceAll(args[i+1], "\n", " "), "\r", " ")
			if value == "" || strings.HasPrefix(value, "--") {
				continue
			}
			out = append(out, Component{
				Type:       ComponentMLModel,
				Name:       TruncateString(value, MaxComponentNameLength),
				Confidence: ConfidenceDeclared,
				Evidence: Evidence{
					Source: SourceContainerArg,
					Locator: fmt.Sprintf("spec.template.spec.%s[%d].args[%d %d](%s)",
						listKey, idx, i, i+1, arg),
				},
				Properties: map[string]string{
					"identity.confidence": "claimed",
					"identity.argFlag":    arg,
					"container.name":      c.Name,
				},
			})
			i++ // consume value
		}
	}
	return out
}

// extractVolumeMountModels emits data-class Components for volume mounts
// whose paths match the configured model-volume-path allowlist. The
// Component identifies the underlying volume source (PVC name, configMap
// name, hostPath path, or a generic source-type label for other kinds).
func (s *InferenceSpecScraper) extractVolumeMountModels(c corev1.Container, volumes []corev1.Volume, init bool, idx int, cfg *InferenceConfig) []Component {
	var out []Component
	listKey := "containers"
	if init {
		listKey = "initContainers"
	}
	for mIdx, m := range c.VolumeMounts {
		if !cfg.IsModelVolumePath(m.MountPath) {
			continue
		}
		sourceName, sourceKind := lookupVolumeSource(m.Name, volumes)
		out = append(out, Component{
			Type:       ComponentData,
			Name:       sourceName,
			Confidence: ConfidenceInferred,
			Evidence: Evidence{
				Source: SourceVolumeSource,
				Locator: fmt.Sprintf("spec.template.spec.%s[%d].volumeMounts[%d](%s -> %s)",
					listKey, idx, mIdx, m.Name, m.MountPath),
			},
			Properties: map[string]string{
				"volume.name":      m.Name,
				"volume.mountPath": m.MountPath,
				"volume.source":    sourceKind,
				"container.name":   c.Name,
			},
		})
	}
	return out
}

// lookupVolumeSource finds the Volume by name in the pod spec and returns
// a (name, kind) pair identifying its backing source. If the volume is
// referenced by a mount but not declared in the pod spec, returns
// (volumeName, "unknown") — an honest "we saw the mount but couldn't trace
// the source" result.
func lookupVolumeSource(volumeName string, volumes []corev1.Volume) (name, kind string) {
	for _, v := range volumes {
		if v.Name != volumeName {
			continue
		}
		switch {
		case v.PersistentVolumeClaim != nil:
			return v.PersistentVolumeClaim.ClaimName, "persistentVolumeClaim"
		case v.ConfigMap != nil:
			return v.ConfigMap.Name, "configMap"
		case v.Secret != nil:
			return v.Secret.SecretName, "secret"
		case v.HostPath != nil:
			return v.HostPath.Path, "hostPath"
		case v.EmptyDir != nil:
			return volumeName, "emptyDir"
		case v.NFS != nil:
			return v.NFS.Server + ":" + v.NFS.Path, "nfs"
		case v.CSI != nil:
			return v.CSI.Driver, "csi"
		}
		return volumeName, "other"
	}
	return volumeName, "unknown"
}

// extractAnnotationModels emits ML-model-class Components for annotations
// whose keys begin with modelAnnotationPrefix. The annotation VALUE is
// treated as the model identity claim.
//
// source identifies the source level (workload, pod template, or KServe
// CR); locatorBase is prepended to each annotation's evidence locator.
//
// Package-level helper so both InferenceSpecScraper and
// KServeInferenceServiceScraper can use it.
const MaxComponentNameLength = 1024

// TruncateString safely truncates a UTF-8 string to maxLen runes,
// appending an ellipsis if truncated, ensuring the total length does not exceed maxLen.
func TruncateString(s string, maxLen int) string {
	runes := []rune(s)
	suffix := "...[truncated]"
	if len(runes) <= maxLen {
		return s
	}
	if maxLen <= len(suffix) {
		return string(runes[:maxLen])
	}
	return string(runes[:maxLen-len(suffix)]) + suffix
}

func extractAnnotationModels(annotations map[string]string, source EvidenceSource, locatorBase string) []Component {
	var out []Component
	for k, v := range annotations {
		if !strings.HasPrefix(k, modelAnnotationPrefix) {
			continue
		}
		if v == "" {
			continue
		}

		modelName := TruncateString(v, MaxComponentNameLength)

		out = append(out, Component{
			Type:       ComponentMLModel,
			Name:       modelName,
			Confidence: ConfidenceDeclared,
			Evidence: Evidence{
				Source:  source,
				Locator: locatorBase + "[" + k + "]",
			},
			Properties: map[string]string{
				"identity.confidence": "claimed",
				"identity.annotation": k,
			},
		})
	}
	return out
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
