// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package scraper

import (
	"context"
	"fmt"
	"regexp"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

// CategoryAgent identifies agentic AI workloads.

// AgentImagePattern matches low-code agent UI/framework images.
type AgentImagePattern struct {
	Name    string
	Pattern *regexp.Regexp
}

var DefaultAgentImagePatterns = []AgentImagePattern{
	{Name: "langflow", Pattern: regexp.MustCompile(`^langflowai/langflow.*`)},
	{Name: "flowise", Pattern: regexp.MustCompile(`^flowiseai/flowise.*`)},
	{Name: "chainlit", Pattern: regexp.MustCompile(`^chainlit/chainlit.*`)},
	{Name: "haystack", Pattern: regexp.MustCompile(`^deepset/haystack.*`)},
	{Name: "llamaindex", Pattern: regexp.MustCompile(`^(?:.*/)?llama-index(?:[:@]|$)`)},
}

// AgentEnvSignature represents environment variables that indicate agent framework usage.
var AgentEnvSignatures = map[string]string{
	"LANGCHAIN_TRACING_V2":     "langchain",
	"LANGCHAIN_API_KEY":        "langchain",
	"AUTOGEN_USE_DOCKER":       "autogen",
	"CREWAI_TELEMETRY_OPT_OUT": "crewai",
	"PIPELINE_YAML_PATH":       "haystack",
	"DSPY_PROFILES_PATH":       "dspy",
	"LLAMA_CLOUD_API_KEY":      "llamaindex",
}

// ExternalAPISignatures maps API key environment variables to the remote model/service.
var ExternalAPISignatures = map[string]string{
	"OPENAI_API_KEY":        "openai-api",
	"ANTHROPIC_API_KEY":     "anthropic-api",
	"GEMINI_API_KEY":        "gemini-api",
	"COHERE_API_KEY":        "cohere-api",
	"AZURE_OPENAI_API_KEY":  "azure-openai-api",
	"AZURE_OPENAI_ENDPOINT": "azure-openai-api",
}

type AgentSpecScraper struct{}

func NewAgentSpecScraper() *AgentSpecScraper {
	return &AgentSpecScraper{}
}

func (s *AgentSpecScraper) Name() string {
	return "agent.spec"
}

func (s *AgentSpecScraper) HandlesKind(k WorkloadKind) bool {
	return k.Group == "apps" && (k.Kind == "Deployment" || k.Kind == "StatefulSet" || k.Kind == "DaemonSet")
}

func (s *AgentSpecScraper) Scrape(ctx context.Context, w Workload, cfg *InferenceConfig) (*BOMInputs, error) {
	t := time.Now().UTC()
	inputs := &BOMInputs{
		ScraperName:     s.Name(),
		ScrapeTimestamp: t,
		Confidence:      ConfidenceUnresolved,
	}

	var template *corev1.PodTemplateSpec

	switch obj := w.Object.(type) {
	case *appsv1.Deployment:
		template = &obj.Spec.Template
	case *appsv1.StatefulSet:
		template = &obj.Spec.Template
	case *appsv1.DaemonSet:
		template = &obj.Spec.Template
	default:
		return nil, fmt.Errorf("agent.spec: unsupported object type %T for kind %s/%s/%s",
			w.Object, w.Kind.Group, w.Kind.Version, w.Kind.Kind)
	}

	hasFramework, hasComponents := s.scrapePodTemplate(inputs, template)

	if hasComponents {
		inputs.Confidence = ConfidenceInferred
		inputs.Provenance = []Provenance{{
			ScraperName:     s.Name(),
			ScraperVersion:  ScraperVersion,
			ScrapeMethod:    "spec",
			ScrapeTimestamp: t,
		}}
		if hasFramework {
			inputs.Category = CategoryAgent
		}
	}

	return inputs, nil
}

func (s *AgentSpecScraper) scrapePodTemplate(inputs *BOMInputs, template *corev1.PodTemplateSpec) (bool, bool) {
	hasFramework := false
	hasComponents := false

	for i, c := range template.Spec.Containers {
		// 1. Check for Agent UI/framework images
		for _, p := range DefaultAgentImagePatterns {
			if p.Pattern.MatchString(c.Image) {
				hasFramework = true
				hasComponents = true
				comp := Component{
					Type:       ComponentApplication,
					Name:       p.Name,
					Confidence: ConfidenceInferred,
					Evidence: Evidence{
						Source:  SourceImagePattern,
						Locator: fmt.Sprintf("spec.template.spec.containers[%d].image", i),
					},
					Properties: map[string]string{
						"runtime.name":    p.Name,
						"runtime.pattern": p.Pattern.String(),
					},
				}
				inputs.Components = append(inputs.Components, comp)
			}
		}

		// 2. Check for Framework Env Vars
		for _, env := range c.Env {
			if fwName, ok := AgentEnvSignatures[env.Name]; ok {
				hasFramework = true
				hasComponents = true
				comp := Component{
					Type:       ComponentApplication,
					Name:       fwName,
					Confidence: ConfidenceInferred,
					Evidence: Evidence{
						Source:  SourceEnvVarNamePresent,
						Locator: fmt.Sprintf("spec.template.spec.containers[%d].env[%s]", i, env.Name),
					},
					Properties: map[string]string{
						"runtime.name": fwName,
					},
				}
				inputs.Components = append(inputs.Components, comp)
			}
		}

		// 3. Extract Remote LLM API Dependencies
		for _, env := range c.Env {
			if apiName, ok := ExternalAPISignatures[env.Name]; ok {
				hasComponents = true
				if env.ValueFrom != nil {
					comp := Component{
						Type:       ComponentMLModel,
						Name:       apiName,
						Confidence: ConfidenceUnresolved,
						Evidence: Evidence{
							Source:  SourceEnvVarNamePresent,
							Locator: fmt.Sprintf("spec.template.spec.containers[%d].env[%s].valueFrom", i, env.Name),
						},
					}
					inputs.Components = append(inputs.Components, comp)
					continue
				}
				// We just record that the dependency exists, never the key value
				comp := Component{
					Type:       ComponentMLModel,
					Name:       apiName,
					Confidence: ConfidenceInferred,
					Evidence: Evidence{
						Source:  SourceEnvVarNamePresent,
						Locator: fmt.Sprintf("spec.template.spec.containers[%d].env[%s]", i, env.Name),
					},
					Properties: map[string]string{
						"dependency.type": "remote-api",
					},
				}
				inputs.Components = append(inputs.Components, comp)
			}
		}
	}
	return hasFramework, hasComponents
}
