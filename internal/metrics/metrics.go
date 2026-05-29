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

package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	SinkEmitFailures = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "aibom_external_sink_emit_failures_total",
			Help: "Number of failures emitting AIBOM to external sinks",
		},
		[]string{"sink", "namespace", "kind"},
	)

	ScraperExtractionErrors = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "aibom_scraper_extraction_errors_total",
			Help: "Number of non-fatal extraction errors during scraping",
		},
		[]string{"scraper", "evidence_source"},
	)

	WorkloadsTotal = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "aibom_workloads_total",
			Help: "Current number of AI workloads tracked by the controller",
		},
		[]string{"category", "runtime"},
	)

	ConfigReloads = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "aibom_controller_config_reloads_total",
			Help: "Number of configuration reload events",
		},
		[]string{"result"}, // values: loaded, invalid_using_defaults, invalid_using_lkg, recovered
	)
)

func init() {
	metrics.Registry.MustRegister(SinkEmitFailures, ScraperExtractionErrors, ConfigReloads, WorkloadsTotal)
}
