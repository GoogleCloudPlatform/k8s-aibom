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

	StatusPersistFailures = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "aibom_status_persist_failures_total",
			Help: "Number of non-conflict failures persisting AIBOM status",
		},
		[]string{"namespace", "kind"},
	)
	// WorkloadReconcileOutcomes answers the question a quiet cluster
	// cannot otherwise answer: is the controller looking at my
	// workloads and deciding "no"? Outcomes: not_opted_in (namespace
	// selector did not match), unmatched (opted in, but no inference
	// signal — conservative detection declined), matched (an AIBOM is
	// produced). A nonzero unmatched count in an opted-in namespace
	// is "working as intended, nothing recognized", which is
	// distinguishable from a broken controller (#106).
	WorkloadReconcileOutcomes = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "aibom_workload_reconcile_outcomes_total",
			Help: "Workload reconcile outcomes by kind: not_opted_in, unmatched (opted in, no inference signal), matched, rolled_up (owned by a tracked kind; reported on the owner), rollup_unresolved (owner chain unreadable; reported as a root)",
		},
		[]string{"kind", "outcome"},
	)

	// WatchHealthy is 1 while a third-party kind's watch (Dynamo,
	// NIMService, LeaderWorkerSet, KServe) is synced and error-free, 0
	// while it is starting or degraded (Design 004). A down Dynamo
	// operator shows here as dynamographdeployment=0 while every other
	// series stays 1.
	WatchHealthy = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "aibom_watch_healthy",
			Help: "1 when the third-party kind's watch is synced and error-free, 0 while starting or degraded",
		},
		[]string{"kind"},
	)
	// WatchErrors counts list/watch/probe/start failures per third-party
	// kind. Rises during an outage; flat while healthy.
	WatchErrors = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "aibom_watch_errors_total",
			Help: "Number of list/watch/probe/start failures for third-party kind watches",
		},
		[]string{"kind"},
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
	metrics.Registry.MustRegister(SinkEmitFailures, ScraperExtractionErrors, StatusPersistFailures, ConfigReloads, WorkloadsTotal, WorkloadReconcileOutcomes, WatchHealthy, WatchErrors)
}
