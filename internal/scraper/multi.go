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

import "context"

type MultiScraper struct {
	scrapers []Scraper
}

func NewMultiScraper(scrapers ...Scraper) *MultiScraper {
	return &MultiScraper{scrapers: scrapers}
}

func (m *MultiScraper) Name() string {
	return "multi.spec"
}

func (m *MultiScraper) Scrape(ctx context.Context, w Workload, cfg *InferenceConfig) (*BOMInputs, error) {
	var finalInputs *BOMInputs
	merged := false

	for _, s := range m.scrapers {
		if !s.HandlesKind(w.Kind) {
			continue
		}
		inputs, err := s.Scrape(ctx, w, cfg)
		if err != nil {
			return nil, err
		}
		if inputs.Confidence != ConfidenceUnresolved {
			if finalInputs == nil {
				finalInputs = inputs
			} else {
				merged = true
				finalInputs.Components = append(finalInputs.Components, inputs.Components...)
				finalInputs.Services = append(finalInputs.Services, inputs.Services...)
				finalInputs.Provenance = append(finalInputs.Provenance, inputs.Provenance...)
				finalInputs.Errors = append(finalInputs.Errors, inputs.Errors...)
				finalInputs.TruncatedComponents += inputs.TruncatedComponents
				// Downgrade confidence if one is inferred
				if inputs.Confidence == ConfidenceInferred {
					finalInputs.Confidence = ConfidenceInferred
				}
				if inputs.Category != "" {
					if finalInputs.Category == "" || categoryPriority[inputs.Category] > categoryPriority[finalInputs.Category] {
						finalInputs.Category = inputs.Category
					}
				}
			}
		}
	}

	if finalInputs == nil {
		return &BOMInputs{
			ScraperName: m.Name(),
			Confidence:  ConfidenceUnresolved,
		}, nil
	}

	if merged {
		// Scrapers can report the same field: training.spec and
		// eval.spec share datasetMountComponents, so a Job both match
		// (an lm-eval run logging to W&B) would list each dataset mount
		// twice. Deduplicate keys on type, name, version, source and
		// locator, so it drops only those copies. A single scraper's
		// result is returned as it was.
		finalInputs.Deduplicate()
	}
	// MaxComponentsPerDocument bounds the document, not one scraper's
	// share of it: agent.spec, vectordb.spec, training.spec and
	// eval.spec do not cap their own lists, and merging appends to a
	// list inference.spec has already capped. Cap the result the same
	// way, keeping the first components in scraper order, and record
	// the drop.
	if n := len(finalInputs.Components); n > MaxComponentsPerDocument {
		finalInputs.TruncatedComponents += n - MaxComponentsPerDocument
		finalInputs.Components = finalInputs.Components[:MaxComponentsPerDocument]
	}
	return finalInputs, nil
}

func (m *MultiScraper) HandlesKind(k WorkloadKind) bool {
	for _, s := range m.scrapers {
		if s.HandlesKind(k) {
			return true
		}
	}
	return false
}

var categoryPriority = map[WorkloadCategory]int{
	CategoryAgent:      7,
	CategoryTraining:   6,
	CategoryEvaluation: 5,
	CategoryVectorDB:   4,
	CategoryPipeline:   3,
	CategoryNotebook:   2,
	CategoryInference:  1,
}
