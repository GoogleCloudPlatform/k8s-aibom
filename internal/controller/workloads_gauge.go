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

package controller

import (
	"context"
	"sync"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	aibomv1beta1 "github.com/GoogleCloudPlatform/k8s-aibom/api/v1beta1"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/metrics"
)

// DefaultWorkloadsGaugeInterval is how often WorkloadsGauge recounts.
const DefaultWorkloadsGaugeInterval = 15 * time.Second

const (
	// workloadsGaugeUnknownCategory labels an AIBOM whose status has no
	// summary yet (for example, its first reconcile failed).
	workloadsGaugeUnknownCategory = "unknown"
	// workloadsGaugeNoRuntime labels an AIBOM with no detected runtime
	// (training, agent and vector-database workloads usually have none).
	workloadsGaugeNoRuntime = "none"
)

type workloadsGaugeKey struct{ category, runtime string }

// WorkloadsGauge keeps aibom_workloads_total equal to the AIBOMs that
// exist, by category and runtime (#398). It recounts from the reader
// (the manager's cache) on every tick instead of counting reconciles,
// so the value cannot drift on deletion, garbage collection or restart.
// The runtime label reads status.summary.runtime.name.
type WorkloadsGauge struct {
	Reader   client.Reader
	Interval time.Duration

	mu        sync.Mutex
	published map[workloadsGaugeKey]struct{}
}

// NewWorkloadsGauge returns a gauge that reads AIBOMs from r every
// DefaultWorkloadsGaugeInterval.
func NewWorkloadsGauge(r client.Reader) *WorkloadsGauge {
	return &WorkloadsGauge{Reader: r, Interval: DefaultWorkloadsGaugeInterval}
}

var _ manager.LeaderElectionRunnable = (*WorkloadsGauge)(nil)

// NeedLeaderElection: only the leader reports, like the reconcilers, so
// a sum across replicas does not double count.
func (g *WorkloadsGauge) NeedLeaderElection() bool { return true }

// Start refreshes once, then every Interval until ctx ends. A failed
// refresh is logged and keeps the last values.
func (g *WorkloadsGauge) Start(ctx context.Context) error {
	log := ctrl.Log.WithName("workloads-gauge")
	interval := g.Interval
	if interval <= 0 {
		interval = DefaultWorkloadsGaugeInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := g.Refresh(ctx); err != nil && ctx.Err() == nil {
			log.Error(err, "unable to count AIBOMs; keeping the last aibom_workloads_total values")
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// Refresh recounts the AIBOMs and updates aibom_workloads_total. Label
// pairs that no longer have any AIBOM are removed. On a list error the
// previous values are left unchanged.
func (g *WorkloadsGauge) Refresh(ctx context.Context) error {
	var list aibomv1beta1.AIBOMList
	if err := g.Reader.List(ctx, &list); err != nil {
		return err
	}
	counts := map[workloadsGaugeKey]int{}
	for i := range list.Items {
		a := &list.Items[i]
		if a.DeletionTimestamp != nil {
			continue
		}
		key := workloadsGaugeKey{category: workloadsGaugeUnknownCategory, runtime: workloadsGaugeNoRuntime}
		if s := a.Status.Summary; s != nil {
			if s.Workload.Category != "" {
				key.category = s.Workload.Category
			}
			if s.Runtime != nil && s.Runtime.Name != "" {
				key.runtime = s.Runtime.Name
			}
		}
		counts[key]++
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	for key, n := range counts {
		metrics.WorkloadsTotal.WithLabelValues(key.category, key.runtime).Set(float64(n))
	}
	for key := range g.published {
		if _, ok := counts[key]; !ok {
			metrics.WorkloadsTotal.DeleteLabelValues(key.category, key.runtime)
		}
	}
	g.published = make(map[workloadsGaugeKey]struct{}, len(counts))
	for key := range counts {
		g.published[key] = struct{}{}
	}
	return nil
}
