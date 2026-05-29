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

// Package controller contains the AIBOM reconciler and the supporting
// status-building helpers that turn a *bom.Document into a deliberately
// designed v1alpha1.AIBOMStatus. The package is the boundary where the
// controller's INTERNAL types (BOMInputs, bom.Document, internal Components)
// meet the controller's EXTERNAL types (api/v1alpha1.AIBOMStatus,
// AIBOMSummary, BOMDocumentRef).
//
// **Concurrency model.** The reconciler relies on controller-runtime's
// shared cache for read consistency (Get returns deep-copied objects
// snapshotted from the cache) and on the API server for write
// serialization (CreateOrUpdate and Status().Update issue conditional
// writes via ResourceVersion; concurrent updates surface as 409 Conflict
// and are requeued). No internal locks are required. The recordingSink
// and similar test doubles need their own synchronization because the
// reconciler's parallel external-sink fan-out invokes them concurrently
// (see internal/sink and the recordingSink mutex).
package controller

import (
	cdx "github.com/CycloneDX/cyclonedx-go"

	aibomv1alpha1 "github.com/GoogleCloudPlatform/k8s-aibom/api/v1alpha1"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/bom"
)

// buildSummary translates a finished BOM document plus per-workload
// metadata into the auditor-facing AIBOMSummary. The summary intentionally
// surfaces only the most-asked-about facts: workload identity, runtime,
// model identities, and the aggregate confidence. The full per-component
// detail lives in BOMDocumentRef.Inline (or in an external sink).
func buildSummary(doc *bom.Document, opts SummaryOptions) *aibomv1alpha1.AIBOMSummary {
	if doc == nil || doc.CDX == nil {
		return nil
	}
	summary := &aibomv1alpha1.AIBOMSummary{
		Workload: aibomv1alpha1.WorkloadSummary{
			Kind:       opts.WorkloadKind,
			APIVersion: opts.WorkloadAPIVersion,
			Name:       opts.WorkloadName,
			Namespace:  opts.WorkloadNamespace,
			Category:   opts.WorkloadCategory,
		},
		Confidence: readMetadataProperty(doc.CDX, "aibom.confidence"),
	}
	if doc.CDX.Components == nil {
		return summary
	}
	for _, c := range *doc.CDX.Components {
		switch c.Type {
		case cdx.ComponentTypeApplication:
			if name := readProperty(c, "runtime.name"); name != "" && summary.Runtime == nil {
				// First runtime wins. A workload running two different
				// inference runtimes simultaneously is exotic and would
				// merit a richer summary type; v1 records the first
				// detection and the full set is in the BOM components.
				summary.Runtime = &aibomv1alpha1.RuntimeSummary{
					Name:       name,
					Version:    c.Version,
					Confidence: readProperty(c, "aibom.confidence"),
				}
			}
		case cdx.ComponentTypeMachineLearningModel:
			summary.Models = append(summary.Models, aibomv1alpha1.ModelSummary{
				Identity:   c.Name,
				Source:     readProperty(c, "aibom.evidence.source"),
				Confidence: readProperty(c, "aibom.confidence"),
				// Signed: populated by Phase 8+ signature plumbing.
				// v1 NoopVerifier-derived value is encoded in the BOM's
				// component properties as "identity.confidence=claimed";
				// see docs/schema-divergences.md D-001.
				Signed: readProperty(c, "signature.status"),
			})
		}
	}
	return summary
}

// SummaryOptions carries the workload-identity facts the summary builder
// needs but which are not in the BOM's metadata.component in a directly
// usable form (some fields like APIVersion are not on cdx.Component).
type SummaryOptions struct {
	WorkloadKind       string
	WorkloadAPIVersion string
	WorkloadName       string
	WorkloadNamespace  string
	WorkloadCategory   string
}

// readMetadataProperty returns the value of a top-level metadata property
// by name, or "" if not present.
func readMetadataProperty(b *cdx.BOM, name string) string {
	if b == nil || b.Metadata == nil || b.Metadata.Properties == nil {
		return ""
	}
	for _, p := range *b.Metadata.Properties {
		if p.Name == name {
			return p.Value
		}
	}
	return ""
}

// readProperty returns the value of a property on a component, or "".
func readProperty(c cdx.Component, name string) string {
	if c.Properties == nil {
		return ""
	}
	for _, p := range *c.Properties {
		if p.Name == name {
			return p.Value
		}
	}
	return ""
}
