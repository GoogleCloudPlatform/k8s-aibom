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
	_ "embed"
	"fmt"
	"regexp"

	"sigs.k8s.io/yaml"
)

//go:embed v1-runtime-patterns.yaml
var defaultV1PatternsYAML []byte

// InferenceConfig is the per-scraper configuration that drives extraction
// for inference workloads. It is an audit-reviewable allowlist: the scraper
// recognizes only what this config describes, never anything inferred
// beyond it.
//
// Phase 5 loads InferenceConfig from the embedded YAML (DefaultV1Config).
// Phase 13 wires the AIBOMControllerConfig CRD as the override path.
type InferenceConfig struct {
	// RuntimeImagePatterns matches container image references against a set
	// of known inference-runtime patterns. Each match produces an
	// application-class Component identifying the runtime.
	RuntimeImagePatterns []RuntimeImagePattern `json:"runtimeImagePatterns,omitempty"`

	// ModelEnvVarNames is the allowlist of container environment variable
	// names whose values are interpreted as declared model identities.
	ModelEnvVarNames []string `json:"modelEnvVarNames,omitempty"`

	// ModelArgFlags is the allowlist of container argument flags (e.g.,
	// "--model", "--model-id") whose values are interpreted as declared
	// model identities. The flag is recognized in both `--flag value` and
	// `--flag=value` forms.
	ModelArgFlags []string `json:"modelArgFlags,omitempty"`

	// ModelVolumePathPrefixes is the allowlist of mount path prefixes that
	// signal a volume contains model artifacts. A volumeMount whose path
	// starts with any of these prefixes contributes a data-class Component.
	ModelVolumePathPrefixes []string `json:"modelVolumePathPrefixes,omitempty"`
}

// RuntimeImagePattern names an inference runtime and the regex that
// recognizes its container image. Pattern is a Go regexp; LoadInferenceConfig
// compiles each pattern at load time and fails fast on a malformed pattern.
type RuntimeImagePattern struct {
	Runtime string `json:"runtime"`
	Pattern string `json:"pattern"`

	// compiled is populated by LoadInferenceConfig and consulted by Match.
	// It is intentionally unexported so it does not surface in JSON or YAML.
	compiled *regexp.Regexp
}

// Match returns true if the given image reference matches this pattern.
// Returns false if the pattern was not compiled (i.e., the config wasn't
// loaded through LoadInferenceConfig).
func (p *RuntimeImagePattern) Match(image string) bool {
	if p == nil || p.compiled == nil {
		return false
	}
	return p.compiled.MatchString(image)
}

// LoadInferenceConfig parses YAML data into an InferenceConfig and
// pre-compiles all regex patterns. Returns an error if the YAML is malformed
// or any pattern fails to compile.
func LoadInferenceConfig(data []byte) (*InferenceConfig, error) {
	var c InferenceConfig
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("inference config: yaml unmarshal: %w", err)
	}
	for i := range c.RuntimeImagePatterns {
		p := &c.RuntimeImagePatterns[i]
		re, err := regexp.Compile(p.Pattern)
		if err != nil {
			return nil, fmt.Errorf("inference config: runtime %q pattern %q: %w",
				p.Runtime, p.Pattern, err)
		}
		p.compiled = re
	}
	return &c, nil
}

// DefaultV1Config returns the InferenceConfig loaded from the embedded
// v1 YAML. Panics if the embedded YAML is malformed — that is a build-time
// bug and must not be discoverable only at runtime.
func DefaultV1Config() *InferenceConfig {
	c, err := LoadInferenceConfig(defaultV1PatternsYAML)
	if err != nil {
		panic(fmt.Errorf("k8s-aibom: invalid embedded v1 config: %w", err))
	}
	return c
}

// NewRuntimeImagePattern constructs a RuntimeImagePattern with its
// regex pre-compiled. Returns an error if the pattern does not compile
// as a Go regexp.
//
// Exported so the internal/config Loader can build patterns from
// AIBOMControllerConfig.spec.discovery.inferenceRuntimeImagePatterns
// without needing to know about the unexported compiled field.
func NewRuntimeImagePattern(runtime, pattern string) (RuntimeImagePattern, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return RuntimeImagePattern{}, err
	}
	return RuntimeImagePattern{
		Runtime:  runtime,
		Pattern:  pattern,
		compiled: re,
	}, nil
}

// DetectRuntime returns the runtime name for an image that matches one of
// the configured runtime image patterns, plus the pattern that matched
// (for evidence attribution). Returns ("", nil) if no pattern matches.
func (c *InferenceConfig) DetectRuntime(image string) (runtime string, matched *RuntimeImagePattern) {
	if c == nil {
		return "", nil
	}
	for i := range c.RuntimeImagePatterns {
		p := &c.RuntimeImagePatterns[i]
		if p.Match(image) {
			return p.Runtime, p
		}
	}
	return "", nil
}

// IsModelEnvVarName reports whether the given env var name is in the
// allowlist. Case-sensitive match.
func (c *InferenceConfig) IsModelEnvVarName(name string) bool {
	if c == nil {
		return false
	}
	for _, n := range c.ModelEnvVarNames {
		if n == name {
			return true
		}
	}
	return false
}

// IsModelArgFlag reports whether the given flag (e.g., "--model") is in
// the allowlist. Case-sensitive match on the full flag including dashes.
func (c *InferenceConfig) IsModelArgFlag(flag string) bool {
	if c == nil {
		return false
	}
	for _, f := range c.ModelArgFlags {
		if f == flag {
			return true
		}
	}
	return false
}

// IsModelVolumePath reports whether the given mount path matches one of
// the configured model volume path prefixes. The match is a prefix match
// at path-boundary granularity: "/models" matches "/models" and
// "/models/llama" but NOT "/models-shared".
func (c *InferenceConfig) IsModelVolumePath(path string) bool {
	if c == nil || path == "" {
		return false
	}
	for _, prefix := range c.ModelVolumePathPrefixes {
		if prefix == "" {
			continue
		}
		if path == prefix {
			return true
		}
		if len(path) > len(prefix) && path[:len(prefix)] == prefix && path[len(prefix)] == '/' {
			return true
		}
	}
	return false
}
