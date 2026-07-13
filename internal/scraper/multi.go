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
				finalInputs.Components = append(finalInputs.Components, inputs.Components...)
				finalInputs.Services = append(finalInputs.Services, inputs.Services...)
				finalInputs.Provenance = append(finalInputs.Provenance, inputs.Provenance...)
				finalInputs.Errors = append(finalInputs.Errors, inputs.Errors...)
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
