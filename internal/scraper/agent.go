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
	"regexp"
	"time"
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
}

// AgentEnvSignature represents environment variables that indicate agent framework usage.
var AgentEnvSignatures = map[string]string{
	"LANGCHAIN_TRACING_V2":     "langchain",
	"LANGCHAIN_API_KEY":        "langchain",
	"AUTOGEN_USE_DOCKER":       "autogen",
	"CREWAI_TELEMETRY_OPT_OUT": "crewai",
}

// ExternalAPISignatures maps API key environment variables to the remote model/service.
var ExternalAPISignatures = map[string]string{
	"OPENAI_API_KEY":    "openai-api",
	"ANTHROPIC_API_KEY": "anthropic-api",
	"GEMINI_API_KEY":    "gemini-api",
	"GOOGLE_API_KEY":    "gemini-api",
	"COHERE_API_KEY":    "cohere-api",
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

	isAgent := false
	var agentName string

	for _, pod := range w.Pods {
		for i, c := range pod.Spec.Containers {
			// 1. Check for Agent UI/framework images
			for _, p := range DefaultAgentImagePatterns {
				if p.Pattern.MatchString(c.Image) {
					isAgent = true
					agentName = p.Name
					comp := Component{
						Type:       ComponentApplication,
						Name:       p.Name,
						Confidence: ConfidenceInferred,
						Evidence: Evidence{
							Source:  SourceImagePattern,
							Locator: "spec.containers.image",
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
					isAgent = true
					if agentName == "" {
						agentName = fwName
					}
					comp := Component{
						Type:       ComponentApplication,
						Name:       fwName,
						Confidence: ConfidenceInferred,
						Evidence: Evidence{
							Source:  SourceEnvVar,
							Locator: "spec.containers.env[" + env.Name + "]",
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
					if env.ValueFrom != nil {
						comp := Component{Type: ComponentMLModel, Name: apiName, Confidence: ConfidenceUnresolved, Evidence: Evidence{Source: SourceEnvVar, Locator: "spec.containers.env[" + env.Name + "].valueFrom"}}
						inputs.Components = append(inputs.Components, comp)
						isAgent = true
						continue
					}
					// We just record that the dependency exists, never the key value
					comp := Component{
						Type:       ComponentMLModel,
						Name:       apiName,
						Confidence: ConfidenceDeclared,
						Evidence: Evidence{
							Source:  SourceEnvVar,
							Locator: "spec.containers.env[" + env.Name + "]",
						},
						Properties: map[string]string{
							"dependency.type": "remote-api",
						},
					}
					inputs.Components = append(inputs.Components, comp)
					// If they use an LLM API, it heavily implies an AI workload
					isAgent = true
				}
			}

			// Same for EnvFrom (ConfigMaps/Secrets)
			for _, envFrom := range c.EnvFrom {
				if envFrom.SecretRef != nil || envFrom.ConfigMapRef != nil {
					// We don't resolve the actual secret/configmap contents yet (Phase 2),
					// but we could at least flag that we skipped them.
				}
			}
			_ = i
		}
	}

	if isAgent {
		inputs.Confidence = ConfidenceInferred
		inputs.Category = CategoryAgent
		inputs.Provenance = []Provenance{{
			ScraperName:     s.Name(),
			ScraperVersion:  ScraperVersion,
			ScrapeMethod:    "spec",
			ScrapeTimestamp: t,
		}}
	}

	return inputs, nil
}
