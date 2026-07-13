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

// CategoryTraining identifies model training and fine-tuning workloads.
const CategoryTraining WorkloadCategory = "training"

// TrainingImagePattern matches known training framework images.
type TrainingImagePattern struct {
	Name    string
	Pattern *regexp.Regexp
}

var DefaultTrainingImagePatterns = []TrainingImagePattern{
	{Name: "pytorch", Pattern: regexp.MustCompile(`^pytorch/pytorch.*`)},
	{Name: "kuberay", Pattern: regexp.MustCompile(`^rayproject/ray.*`)},
	{Name: "jax", Pattern: regexp.MustCompile(`^ghcr\.io/google/jax.*`)},
	{Name: "accelerate", Pattern: regexp.MustCompile(`^huggingface/accelerate.*`)},
}

// TrainingEnvSignatures matches environment variables that indicate training frameworks.
var TrainingEnvSignatures = map[string]string{
	"WANDB_API_KEY":          "wandb",
}

type TrainingSpecScraper struct{}

func NewTrainingSpecScraper() *TrainingSpecScraper {
	return &TrainingSpecScraper{}
}
func (s *TrainingSpecScraper) Name() string {
	return "training.spec"
}
func (s *TrainingSpecScraper) HandlesKind(k WorkloadKind) bool {
	return k.Group == "batch" && k.Kind == "Job" || k.Group == "kubeflow.org" && k.Kind == "PyTorchJob" || k.Group == "ray.io" && k.Kind == "RayJob"
}
func (s *TrainingSpecScraper) Scrape(ctx context.Context, w Workload, cfg *InferenceConfig) (*BOMInputs, error) {
	t := time.Now().UTC()
	inputs := &BOMInputs{
		ScraperName:     s.Name(),
		ScrapeTimestamp: t,
		Confidence:      ConfidenceUnresolved,
	}

	var template *corev1.PodTemplateSpec

	switch obj := w.Object.(type) {
	case *batchv1.Job:
		template = &obj.Spec.Template
	default:
		// For unstructured CRDs, fallback to scraping running Pods and de-duplicating
		s.scrapePods(inputs, w.Pods)
		inputs.Deduplicate()
		return inputs, nil
	}

	s.scrapePodTemplate(inputs, template)
	return inputs, nil
}

func (s *TrainingSpecScraper) scrapePodTemplate(inputs *BOMInputs, template *corev1.PodTemplateSpec) {
	isTraining := false
	for i, c := range template.Spec.Containers {
		// Check for training images
		for _, p := range DefaultTrainingImagePatterns {
			if p.Pattern.MatchString(c.Image) {
				isTraining = true
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
		// Check Env Vars
		for _, env := range c.Env {
			if fwName, ok := TrainingEnvSignatures[env.Name]; ok {
				isTraining = true
				comp := Component{
					Type:       ComponentApplication,
					Name:       fwName,
					Confidence: ConfidenceInferred,
					Evidence: Evidence{
						Source:  SourceEnvVarNamePresent,
						Locator: fmt.Sprintf("spec.template.spec.containers[%d].env[%s]", i, env.Name),
					},
				}
				inputs.Components = append(inputs.Components, comp)
			}
		}
		// Map Volume Mounts as training datasets (simplified logic)
		for _, vm := range c.VolumeMounts {
			if vm.Name == "dataset" || vm.Name == "training-data" || vm.MountPath == "/data" {
				comp := Component{
					Type:       ComponentData,
					Name:       vm.Name,
					Confidence: ConfidenceInferred,
					Evidence: Evidence{
						Source:  SourceVolumeSource,
						Locator: fmt.Sprintf("spec.template.spec.containers[%d].volumeMounts[%s]", i, vm.Name),
					},
					Properties: map[string]string{
						"dataset.path": vm.MountPath,
					},
				}
				inputs.Components = append(inputs.Components, comp)
			}
		}
	}

	if isTraining {
		inputs.Confidence = ConfidenceInferred
		inputs.Category = CategoryTraining
		inputs.Provenance = []Provenance{{
			ScraperName:     s.Name(),
			ScraperVersion:  ScraperVersion,
			ScrapeMethod:    "spec",
			ScrapeTimestamp: inputs.ScrapeTimestamp,
		}}
	}
}

func (s *TrainingSpecScraper) scrapePods(inputs *BOMInputs, pods []corev1.Pod) {
	isTraining := false
	for _, pod := range pods {
		for _, c := range pod.Spec.Containers {
			// Check for training images
			for _, p := range DefaultTrainingImagePatterns {
				if p.Pattern.MatchString(c.Image) {
					isTraining = true
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
			// Check Env Vars
			for _, env := range c.Env {
				if fwName, ok := TrainingEnvSignatures[env.Name]; ok {
					isTraining = true
					comp := Component{
						Type:       ComponentApplication,
						Name:       fwName,
						Confidence: ConfidenceInferred,
						Evidence: Evidence{
							Source:  SourceEnvVarNamePresent,
							Locator: "spec.containers.env[" + env.Name + "]",
						},
					}
					inputs.Components = append(inputs.Components, comp)
				}
			}
			// Map Volume Mounts as training datasets (simplified logic)
			for _, vm := range c.VolumeMounts {
				if vm.Name == "dataset" || vm.Name == "training-data" || vm.MountPath == "/data" {
					comp := Component{
						Type:       ComponentData,
						Name:       vm.Name,
						Confidence: ConfidenceInferred,
						Evidence: Evidence{
							Source:  SourceVolumeSource,
							Locator: "spec.containers.volumeMounts[" + vm.Name + "]",
						},
						Properties: map[string]string{
							"dataset.path": vm.MountPath,
						},
					}
					inputs.Components = append(inputs.Components, comp)
				}
			}
		}
	}

	if isTraining {
		inputs.Confidence = ConfidenceInferred
		inputs.Category = CategoryTraining
		inputs.Provenance = []Provenance{{
			ScraperName:     s.Name(),
			ScraperVersion:  ScraperVersion,
			ScrapeMethod:    "spec",
			ScrapeTimestamp: inputs.ScrapeTimestamp,
		}}
	}
}
