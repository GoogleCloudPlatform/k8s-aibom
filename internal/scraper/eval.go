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

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
)

// CategoryEval identifies model evaluation workloads.
const CategoryEval WorkloadCategory = "evaluation"

// EvalImagePattern matches known evaluation framework images.
type EvalImagePattern struct {
	Name    string
	Pattern *regexp.Regexp
}

var DefaultEvalImagePatterns = []EvalImagePattern{
	{Name: "lm-eval", Pattern: regexp.MustCompile(`^eleutherai/lm-eval.*`)},
	{Name: "ragas", Pattern: regexp.MustCompile(`^(?:.*/)?ragas(?:[:@]|$)`)},
	{Name: "trulens", Pattern: regexp.MustCompile(`^(?:.*/)?trulens(?:[:@]|$)`)},
}

type EvalSpecScraper struct{}

func NewEvalSpecScraper() *EvalSpecScraper {
	return &EvalSpecScraper{}
}
func (s *EvalSpecScraper) Name() string {
	return "eval.spec"
}
func (s *EvalSpecScraper) HandlesKind(k WorkloadKind) bool {
	return k.Group == "batch" && k.Kind == "Job" || k.Group == "batch" && k.Kind == "CronJob"
}
func (s *EvalSpecScraper) Scrape(ctx context.Context, w Workload, cfg *InferenceConfig) (*BOMInputs, error) {
	t := time.Now().UTC()
	inputs := &BOMInputs{
		ScraperName:     s.Name(),
		ScrapeTimestamp: t,
		Confidence:      ConfidenceUnresolved,
	}

	var template *corev1.PodTemplateSpec
	// specLocator is the path of the pod spec in the workload object, so
	// every evidence locator names a field that exists in that kind.
	var specLocator string

	switch obj := w.Object.(type) {
	case *batchv1.Job:
		template = &obj.Spec.Template
		specLocator = "spec.template.spec"
	case *batchv1.CronJob:
		template = &obj.Spec.JobTemplate.Spec.Template
		specLocator = "spec.jobTemplate.spec.template.spec"
	default:
		return nil, fmt.Errorf("eval.spec: unsupported object type %T for kind %s/%s/%s",
			w.Object, w.Kind.Group, w.Kind.Version, w.Kind.Kind)
	}

	isEval := s.scrapePodTemplate(inputs, template, specLocator)

	if isEval {
		inputs.Confidence = ConfidenceInferred
		inputs.Category = CategoryEval
		inputs.Provenance = []Provenance{{
			ScraperName:     s.Name(),
			ScraperVersion:  ScraperVersion,
			ScrapeMethod:    "spec",
			ScrapeTimestamp: t,
		}}
	}
	return inputs, nil
}

func (s *EvalSpecScraper) scrapePodTemplate(inputs *BOMInputs, template *corev1.PodTemplateSpec, specLocator string) bool {
	isEval := false
	for i, c := range template.Spec.Containers {
		// Check for evaluation images
		for _, p := range DefaultEvalImagePatterns {
			if p.Pattern.MatchString(c.Image) {
				isEval = true
				comp := Component{
					Type:       ComponentApplication,
					Name:       p.Name,
					Confidence: ConfidenceInferred,
					Evidence: Evidence{
						Source:  SourceImagePattern,
						Locator: fmt.Sprintf("%s.containers[%d].image", specLocator, i),
					},
					Properties: map[string]string{
						"runtime.name":    p.Name,
						"runtime.pattern": p.Pattern.String(),
					},
				}
				inputs.Components = append(inputs.Components, comp)
			}
		}
		// Datasets the evaluation reads, found the same way as for
		// training jobs. Kept only when the job is an evaluation: the
		// scraper's result is dropped while its confidence is unresolved.
		inputs.Components = append(inputs.Components, datasetMountComponents(c, i, specLocator)...)
	}
	return isEval
}
