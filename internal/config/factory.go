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

package config

import (
	"context"

	aibomv1alpha1 "github.com/GoogleCloudPlatform/k8s-aibom/api/v1alpha1"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/sink"
)

// SinkFactory is the contract for building external sinks from CRD
// spec entries. Checkpoint 3 provides the production implementation
// (which reads Secrets via the K8s API and constructs GCSSink /
// WebhookSink instances). Checkpoint 2 declares the interface so the
// Loader's fallback semantics can be tested with stub factories.
//
// The factory's contract is per-sink: each spec produces either a
// successfully-constructed Sink or a LoadError, in input order. The
// LOADER applies the all-or-nothing fallback at the snapshot-decision
// level; the factory itself just reports per-sink outcomes.
type SinkFactory interface {
	BuildSinks(ctx context.Context, specs []aibomv1alpha1.SinkConfig) (sinks []sink.Sink, errs []LoadError)
}

// NoopSinkFactory is a SinkFactory that returns no sinks and no
// errors regardless of input. Used in tests that exercise the
// Loader's parse path without exercising sink construction. The
// production AIBOMControllerConfigReconciler uses a real factory
// (Checkpoint 3).
type NoopSinkFactory struct{}

// BuildSinks implements SinkFactory; returns empty results.
func (NoopSinkFactory) BuildSinks(_ context.Context, _ []aibomv1alpha1.SinkConfig) ([]sink.Sink, []LoadError) {
	return nil, nil
}
