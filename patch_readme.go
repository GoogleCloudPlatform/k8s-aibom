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

package main

import (
	"log"
	"os"
	"strings"
)

func main() {
	b, err := os.ReadFile("README.md")
	if err != nil {
		log.Fatalf("Error reading file: %v", err)
	}
	s := string(b)

	// Fix introduction
	s = strings.Replace(s, "for AI inference workloads at runtime", "across the entire AI lifecycle (Inference, Agents, Data Stores, Training, Evals) at runtime", 1)

	// Fix "What it does"
	s = strings.Replace(s, "k8s-aibom observes inference workloads running in a Kubernetes cluster — Deployments, StatefulSets, DaemonSets, KServe `InferenceService` resources — and produces a CycloneDX 1.6 ML-BOM document for each one. The BOM captures:\n- The serving runtime in use (vLLM, TGI, Triton, Ollama, Ray Serve, llm-d, SGLang, LMDeploy, HuggingFace TEI) and its version", "k8s-aibom observes AI workloads running in a Kubernetes cluster — Deployments, StatefulSets, DaemonSets, Jobs, CronJobs, and KServe `InferenceService` resources — and produces a CycloneDX 1.6 ML-BOM document for each one. The BOM captures:\n- The framework in use (vLLM, LangChain, PyTorch, Milvus, Ragas, etc.) and its version\n- External API dependencies (e.g., OpenAI, Gemini, Anthropic)\n- Mounted training datasets and volumes", 1)

	if err := os.WriteFile("README.md", []byte(s), 0644); err != nil {
		log.Fatalf("Error writing file: %v", err)
	}
}
