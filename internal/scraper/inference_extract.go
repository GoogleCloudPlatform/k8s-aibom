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
	"slices"
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
//     the BOM intentionally reports the running digest. When pods
//     report different digests for the container (a rollout in
//     progress, or a moving tag such as :latest pulled at different
//     times), the digest the most pods report wins, and a tie goes to
//     the lowest digest. The pods come from the informer cache in no
//     fixed order, so taking the first match would change the digest,
//     and with it the input hash, from one reconcile to the next.
//  3. If nothing resolves, return ("", "") and the caller marks the
//     component ConfidenceUnresolved.
//
// init=true selects initContainerStatuses on pods; init=false selects
// containerStatuses.
func resolveContainerDigest(specImage, containerName string, pods []corev1.Pod, init bool) (digest string, source EvidenceSource) {
	specName, _, d := parseImageRef(specImage)
	if d != "" {
		return d, SourceImageReference
	}
	counts := map[string]int{}
	for _, pod := range pods {
		statuses := pod.Status.ContainerStatuses
		if init {
			statuses = pod.Status.InitContainerStatuses
		}
		for _, cs := range statuses {
			if cs.Name != containerName {
				continue
			}
			// Belt-and-braces against mis-attribution: the candidate
			// must be running the same image name the workload spec
			// declares. Container-name equality alone is not identity
			// — a same-named container from another image must never
			// supply this component's digest. Tag differences are
			// allowed (a rollout reports the running digest); name
			// differences are disqualifying. An empty status image is
			// treated as name-unknown and permitted: ownership
			// filtering upstream is the primary guard, and real
			// kubelets always populate it.
			if cs.Image != "" {
				csName, _, _ := parseImageRef(cs.Image)
				if normalizeImageName(csName) != normalizeImageName(specName) {
					continue
				}
			}
			if d := parseImageDigest(cs.ImageID); d != "" {
				counts[d]++
			}
		}
	}
	best := ""
	for d, n := range counts {
		if best == "" || n > counts[best] || (n == counts[best] && d < best) {
			best = d
		}
	}
	if best == "" {
		return "", ""
	}
	return best, SourcePodStatus
}

// normalizeImageName equates the forms a registry-default image name
// takes between a workload spec and a kubelet-reported container
// status: "docker.io/" and "index.docker.io/" prefixes are dropped,
// then a "library/" prefix is dropped. "vllm/vllm-openai",
// "docker.io/vllm/vllm-openai" and "index.docker.io/vllm/vllm-openai"
// compare equal; nothing else is rewritten.
func normalizeImageName(name string) string {
	for _, p := range []string{"index.docker.io/", "docker.io/"} {
		if strings.HasPrefix(name, p) {
			name = strings.TrimPrefix(name, p)
			break
		}
	}
	return strings.TrimPrefix(name, "library/")
}

// extractContainerComponentAt produces a container-class Component for a
// single container in the workload's pod spec, including digest resolution
// and (if the image matches a configured runtime pattern) a sibling
// application-class Component identifying the runtime.
//
// Returns one or two Components: always the container; optionally the
// runtime application Component.
func (s *InferenceSpecScraper) extractContainerComponentAt(specLocator string, c corev1.Container, init bool, idx int, pods []corev1.Pod, cfg *InferenceConfig) []Component {
	name, tag, digestFromSpec := parseImageRef(c.Image)

	containerLocatorBase := fmt.Sprintf("%s.containers[%d]", specLocator, idx)
	if init {
		containerLocatorBase = fmt.Sprintf("%s.initContainers[%d]", specLocator, idx)
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
		Name:    TruncateString(name, MaxComponentNameLength),
		Version: TruncateString(tag, MaxComponentNameLength),
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
	// Image identity in the CycloneDX core model: a pkg:oci purl built
	// from the reference as written and the resolved digest (empty when
	// the digest is unknown; the purl then has no version).
	comp.PURL = ociPURL(c.Image, digest)
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

// extractEnvVarModelsAt emits ML-model-class Components for env vars whose
// names match the configured model-env-var allowlist.
func (s *InferenceSpecScraper) extractEnvVarModelsAt(specLocator string, c corev1.Container, init bool, idx int, cfg *InferenceConfig) []Component {
	listKey := "containers"
	if init {
		listKey = "initContainers"
	}
	return envVarModels(c.Env, c.Name, fmt.Sprintf("%s.%s[%d]", specLocator, listKey, idx), cfg)
}

// envVarModels is the list-level form of extractEnvVarModelsAt.
// locatorBase is the path of the object that holds the env list.
// containerName may be empty for CRs whose env list is not inside a
// container (NIMService spec.env); the container.name property is then
// omitted rather than written empty.
func envVarModels(env []corev1.EnvVar, containerName, locatorBase string, cfg *InferenceConfig) []Component {
	var out []Component
	for envIdx, e := range env {
		if !cfg.IsModelEnvVarName(e.Name) {
			continue
		}
		if e.Value == "" {
			// Honest extraction: empty value is not a model identity.
			continue
		}
		props := map[string]string{
			"identity.confidence": "claimed",
			"identity.envVarName": e.Name,
		}
		if containerName != "" {
			props["container.name"] = containerName
		}
		out = append(out, Component{
			Type:       ComponentMLModel,
			Name:       TruncateString(e.Value, MaxComponentNameLength),
			Confidence: ConfidenceInferred,
			Evidence: Evidence{
				Source:  SourceEnvVar,
				Locator: fmt.Sprintf("%s.env[%d](%s)", locatorBase, envIdx, e.Name),
			},
			Properties: props,
		})
	}
	return out
}

// extractArgModelsAt emits ML-model-class Components for container args
// whose flag name matches the configured model-arg-flag allowlist. Handles
// both `--flag value` (positional) and `--flag=value` (joined) forms.
func (s *InferenceSpecScraper) extractArgModelsAt(specLocator string, c corev1.Container, init bool, idx int, cfg *InferenceConfig) []Component {
	listKey := "containers"
	if init {
		listKey = "initContainers"
	}
	return argModels(c.Args, c.Env, c.Name, fmt.Sprintf("%s.%s[%d]", specLocator, listKey, idx), cfg)
}

// expandVarRefs expands the $(VAR_NAME) references in s the way the
// kubelet expands container args and env values (see Container.args in
// the Kubernetes API): "$$" becomes "$", "$(NAME)" becomes lookup(NAME),
// and any other "$", including an unterminated "$(", is kept. A
// reference lookup cannot resolve is kept as written and ok is false.
func expandVarRefs(s string, lookup func(string) (string, bool)) (expanded string, ok bool) {
	if strings.IndexByte(s, '$') < 0 {
		return s, true
	}
	ok = true
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '$' || i+1 == len(s) {
			b.WriteByte(s[i])
			continue
		}
		switch s[i+1] {
		case '$':
			b.WriteByte('$')
			i++
		case '(':
			end := strings.IndexByte(s[i+2:], ')')
			if end < 0 {
				b.WriteString("$(")
				i++
				continue
			}
			name := s[i+2 : i+2+end]
			if v, found := lookup(name); found {
				b.WriteString(v)
			} else {
				b.WriteString(s[i : i+3+end])
				ok = false
			}
			i += 2 + end
		default:
			b.WriteByte('$')
		}
	}
	return b.String(), ok
}

// specEnvValues returns the env vars whose values the container spec
// states, resolved in order as the kubelet resolves them: each value is
// expanded against the entries before it, and a later entry replaces an
// earlier one of the same name. An entry set through valueFrom, or whose
// value refers to a name not resolved this way, is left out: its value
// is not in the spec.
func specEnvValues(env []corev1.EnvVar) map[string]string {
	vars := make(map[string]string, len(env))
	lookup := func(name string) (string, bool) {
		v, found := vars[name]
		return v, found
	}
	for _, e := range env {
		if e.ValueFrom != nil {
			delete(vars, e.Name)
			continue
		}
		v, ok := expandVarRefs(e.Value, lookup)
		if !ok {
			delete(vars, e.Name)
			continue
		}
		vars[e.Name] = v
	}
	return vars
}

// expandedArgs is the result of expandArgs.
type expandedArgs struct {
	// args as the kubelet passes them to the container.
	args []string
	// unresolved holds the indices of args that refer to a variable whose
	// value the spec does not state (valueFrom, envFrom, a Service
	// variable, or none at all). Such an arg cannot name a model: the BOM
	// would record the reference text, not the model the runtime is
	// started with.
	unresolved map[int]bool
	// refs holds, for each arg in which at least one reference was
	// expanded, the referenced names in order of first appearance. A "$$"
	// escape is not a reference.
	refs map[int][]string
}

// expandArgs expands the $(VAR_NAME) references in args from the
// container's env. Args without a "$" are returned as they are.
func expandArgs(args []string, env []corev1.EnvVar) expandedArgs {
	res := expandedArgs{args: args}
	var vars map[string]string
	var names []string
	lookup := func(name string) (string, bool) {
		v, found := vars[name]
		if found && !slices.Contains(names, name) {
			names = append(names, name)
		}
		return v, found
	}
	copied := false
	for i, a := range args {
		if strings.IndexByte(a, '$') < 0 {
			continue
		}
		if !copied {
			res.args = append([]string(nil), args...)
			vars = specEnvValues(env)
			copied = true
		}
		names = nil
		v, ok := expandVarRefs(a, lookup)
		res.args[i] = v
		if !ok {
			if res.unresolved == nil {
				res.unresolved = map[int]bool{}
			}
			res.unresolved[i] = true
		}
		if len(names) > 0 {
			if res.refs == nil {
				res.refs = map[int][]string{}
			}
			res.refs[i] = names
		}
	}
	return res
}

// withEnvVarName records in props the env vars that the model arg at
// index i was expanded from, so the evidence chain from the arg locator,
// which shows the reference, reaches the value. Several references are
// listed comma-separated in order of first appearance.
func (e expandedArgs) withEnvVarName(i int, props map[string]string) map[string]string {
	if names := e.refs[i]; len(names) > 0 {
		props["identity.envVarName"] = strings.Join(names, ",")
	}
	return props
}

// argModels is the list-level form of extractArgModelsAt; see
// envVarModels for the locatorBase / containerName conventions. env is
// the env list of the same container, used to expand $(VAR_NAME)
// references in args; an arg whose reference it cannot resolve names no
// model.
func argModels(args []string, env []corev1.EnvVar, containerName, locatorBase string, cfg *InferenceConfig) []Component {
	var out []Component
	withContainer := func(props map[string]string) map[string]string {
		if containerName != "" {
			props["container.name"] = containerName
		}
		return props
	}
	expanded := expandArgs(args, env)
	args, unresolved := expanded.args, expanded.unresolved
	for i := 0; i < len(args); i++ {
		arg := args[i]
		// --flag=value form
		if eq := strings.IndexByte(arg, '='); eq >= 0 {
			flag := arg[:eq]
			value := strings.ReplaceAll(strings.ReplaceAll(arg[eq+1:], "\n", " "), "\r", " ")
			if cfg.IsModelArgFlag(flag) && value != "" && !strings.HasPrefix(value, "--") && !unresolved[i] {
				out = append(out, Component{
					Type:       ComponentMLModel,
					Name:       TruncateString(value, MaxComponentNameLength),
					Confidence: ConfidenceDeclared,
					Evidence: Evidence{
						Source:  SourceContainerArg,
						Locator: fmt.Sprintf("%s.args[%d](%s=)", locatorBase, i, flag),
					},
					Properties: withContainer(expanded.withEnvVarName(i, map[string]string{
						"identity.confidence": "claimed",
						"identity.argFlag":    flag,
					})),
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
			if unresolved[i+1] {
				i++ // the flag's value, but not one the spec states
				continue
			}
			out = append(out, Component{
				Type:       ComponentMLModel,
				Name:       TruncateString(value, MaxComponentNameLength),
				Confidence: ConfidenceDeclared,
				Evidence: Evidence{
					Source:  SourceContainerArg,
					Locator: fmt.Sprintf("%s.args[%d %d](%s)", locatorBase, i, i+1, arg),
				},
				Properties: withContainer(expanded.withEnvVarName(i+1, map[string]string{
					"identity.confidence": "claimed",
					"identity.argFlag":    arg,
				})),
			})
			i++ // consume value
		}
	}
	return out
}

// servedModelNameFlag is vLLM's alias flag: the name its metrics and
// OpenAI-compatible API publish the model under, when set.
const servedModelNameFlag = "--served-model-name"

// servedModelName returns the first value of --served-model-name in args
// (vLLM accepts several; the first is the one its metrics carry), after
// $(VAR) expansion against env, or "" when the flag is absent, empty,
// or references a variable the spec does not state.
func servedModelName(args []string, env []corev1.EnvVar) string {
	expanded := expandArgs(args, env)
	xs, unresolved := expanded.args, expanded.unresolved
	for i := 0; i < len(xs); i++ {
		arg := xs[i]
		if strings.HasPrefix(arg, servedModelNameFlag+"=") {
			v := strings.TrimPrefix(arg, servedModelNameFlag+"=")
			if v == "" || unresolved[i] {
				return ""
			}
			return TruncateString(strings.Fields(v)[0], MaxComponentNameLength)
		}
		if arg == servedModelNameFlag && i+1 < len(xs) {
			v := xs[i+1]
			if v == "" || strings.HasPrefix(v, "--") || unresolved[i+1] {
				return ""
			}
			return TruncateString(v, MaxComponentNameLength)
		}
	}
	return ""
}

// applyServedModelName records the alias on every ML-model component in
// comps. A no-op when name is empty, so documents without the flag are
// byte-identical to before.
func applyServedModelName(comps []Component, name string) {
	if name == "" {
		return
	}
	for i := range comps {
		if comps[i].Type != ComponentMLModel {
			continue
		}
		if comps[i].Properties == nil {
			comps[i].Properties = map[string]string{}
		}
		comps[i].Properties["identity.servedModelName"] = name
	}
}

// extractVolumeMountModelsAt emits data-class Components for volume mounts
// whose paths match the configured model-volume-path allowlist. The
// Component identifies the underlying volume source (PVC name, configMap
// name, hostPath path, or a generic source-type label for other kinds).
func (s *InferenceSpecScraper) extractVolumeMountModelsAt(specLocator string, c corev1.Container, volumes []corev1.Volume, init bool, idx int, cfg *InferenceConfig) []Component {
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
				Locator: fmt.Sprintf("%s.%s[%d].volumeMounts[%d](%s -> %s)",
					specLocator, listKey, idx, mIdx, m.Name, m.MountPath),
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
		case v.Image != nil:
			// An OCI image or artifact mounted as a volume (KEP-4639),
			// the Kubernetes-native way to ship model weights. The
			// reference names the model the way a PVC claim name names
			// a volume of weights.
			if v.Image.Reference == "" {
				return volumeName, "image"
			}
			return TruncateString(v.Image.Reference, MaxComponentNameLength), "image"
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

// MaxComponentsPerDocument caps the number of components a single
// workload can put into its BOM. A tenant authoring a pathological
// spec (hundreds of containers/env claims) bounds document growth in
// memory and on the wire, not only in etcd. Truncation is recorded
// (aibom.truncation.applied), never silent — mirroring the redaction
// rule. 256 is far above any legitimate workload observed.
const MaxComponentsPerDocument = 256

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
		if reservedModelAnnotation(k) {
			// Signature claim metadata, not a model identity
			// (signature_apply.go); extracting these as models would
			// emit a phantom component named after a URL or digest.
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
