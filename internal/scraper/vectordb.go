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

// CategoryVectorDB identifies workloads serving vector databases or RAG pipelines.
const CategoryVectorDB WorkloadCategory = "vector-database"

// VectorDBPattern matches a vector database image to its logical name.
type VectorDBPattern struct {
	Name    string
	Pattern *regexp.Regexp
}

// DefaultVectorDBPatterns provides the built-in patterns for popular vector DBs.
var DefaultVectorDBPatterns = []VectorDBPattern{
	{Name: "milvus", Pattern: regexp.MustCompile(`^milvusdb/milvus.*`)},
	{Name: "qdrant", Pattern: regexp.MustCompile(`^qdrant/qdrant.*`)},
	{Name: "weaviate", Pattern: regexp.MustCompile(`^(?:.*/)?weaviate(?:[:@]|$)`)},
	{Name: "chroma", Pattern: regexp.MustCompile(`^chromadb/chroma.*`)},
	{Name: "pgvector", Pattern: regexp.MustCompile(`^(?:.*/)?pgvector(?:[:@]|$)`)},
}

type VectorDBSpecScraper struct{}

func NewVectorDBSpecScraper() *VectorDBSpecScraper {
	return &VectorDBSpecScraper{}
}

func (s *VectorDBSpecScraper) Name() string {
	return "vectordb.spec"
}

func (s *VectorDBSpecScraper) Scrape(ctx context.Context, w Workload, cfg *InferenceConfig) (*BOMInputs, error) {
	t := time.Now().UTC()
	inputs := &BOMInputs{
		ScraperName:     s.Name(),
		ScrapeTimestamp: t,
		Confidence:      ConfidenceUnresolved,
	}

	for _, pod := range w.Pods {
		for _, c := range pod.Spec.Containers {
			for _, p := range DefaultVectorDBPatterns {
				if p.Pattern.MatchString(c.Image) {
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
					inputs.Confidence = ConfidenceInferred
				}
			}
		}
	}

	if len(inputs.Components) > 0 {
		inputs.Category = CategoryVectorDB
		inputs.Provenance = []Provenance{{
			ScraperName:     s.Name(),
			ScraperVersion:  ScraperVersion,
			ScrapeMethod:    "spec",
			ScrapeTimestamp: t,
		}}
	}

	return inputs, nil
}

func (s *VectorDBSpecScraper) HandlesKind(k WorkloadKind) bool {
	return k.Group == "apps" && (k.Kind == "Deployment" || k.Kind == "StatefulSet" || k.Kind == "DaemonSet")
}
