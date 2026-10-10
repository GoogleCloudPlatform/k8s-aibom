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
	"fmt"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// KnownWorkloadKinds is the compiled-in set of kinds the controller can
// inventory (Design 006 §1). spec.discovery.workloadKinds entries are
// validated against it: a typo is a config error, never a silent no-op.
// Order is the documentation order; it has no runtime meaning.
//
// Adding a kind here is part of adding a scraper (see
// docs/external-crd-versions.md "Process for adding a new external
// CRD").
var KnownWorkloadKinds = []schema.GroupKind{
	{Group: "apps", Kind: "Deployment"},
	{Group: "apps", Kind: "StatefulSet"},
	{Group: "apps", Kind: "DaemonSet"},
	{Group: "batch", Kind: "Job"},
	{Group: "batch", Kind: "CronJob"},
	{Group: "serving.kserve.io", Kind: "InferenceService"},
	{Group: "nvidia.com", Kind: "DynamoGraphDeployment"},
	{Group: "nvidia.com", Kind: "DynamoComponentDeployment"},
	{Group: "apps.nvidia.com", Kind: "NIMService"},
	{Group: "leaderworkerset.x-k8s.io", Kind: "LeaderWorkerSet"},
	{Group: "disaggregatedset.x-k8s.io", Kind: "DisaggregatedSet"},
}

// coreGroupSpelling is how the core API group is written in a
// Group/Kind entry, since the empty string cannot carry a slash.
const coreGroupSpelling = "core"

// WorkloadKindSet is the parsed spec.discovery.workloadKinds. The zero
// value allows every kind, which is also what an absent or empty list
// means, so a Snapshot that never set the field behaves as before.
type WorkloadKindSet struct {
	restricted bool
	kinds      map[schema.GroupKind]struct{}
}

// AllWorkloadKinds returns the unrestricted set (today's behavior).
func AllWorkloadKinds() WorkloadKindSet { return WorkloadKindSet{} }

// NewWorkloadKindSet returns a set restricted to exactly kinds. Used by
// tests and by callers that already hold validated GroupKinds; the
// loader goes through parseWorkloadKinds.
func NewWorkloadKindSet(kinds ...schema.GroupKind) WorkloadKindSet {
	s := WorkloadKindSet{restricted: true, kinds: make(map[schema.GroupKind]struct{}, len(kinds))}
	for _, gk := range kinds {
		s.kinds[gk] = struct{}{}
	}
	return s
}

// Allows reports whether the controller inventories gk.
func (s WorkloadKindSet) Allows(gk schema.GroupKind) bool {
	if !s.restricted {
		return true
	}
	_, ok := s.kinds[gk]
	return ok
}

// AllowsAll reports whether the set is unrestricted.
func (s WorkloadKindSet) AllowsAll() bool { return !s.restricted }

// Equal reports whether two sets allow exactly the same kinds.
func (s WorkloadKindSet) Equal(o WorkloadKindSet) bool {
	if s.restricted != o.restricted {
		return false
	}
	if !s.restricted {
		return true
	}
	if len(s.kinds) != len(o.kinds) {
		return false
	}
	for gk := range s.kinds {
		if _, ok := o.kinds[gk]; !ok {
			return false
		}
	}
	return true
}

// Kinds returns the allowed kinds sorted by Group/Kind, or nil when
// the set is unrestricted.
func (s WorkloadKindSet) Kinds() []schema.GroupKind {
	if !s.restricted {
		return nil
	}
	out := make([]schema.GroupKind, 0, len(s.kinds))
	for gk := range s.kinds {
		out = append(out, gk)
	}
	sort.Slice(out, func(i, j int) bool { return FormatWorkloadKind(out[i]) < FormatWorkloadKind(out[j]) })
	return out
}

// String renders the set for logs and conditions: "all" or the sorted
// comma-separated Group/Kind list.
func (s WorkloadKindSet) String() string {
	if !s.restricted {
		return "all"
	}
	kinds := s.Kinds()
	parts := make([]string, 0, len(kinds))
	for _, gk := range kinds {
		parts = append(parts, FormatWorkloadKind(gk))
	}
	return strings.Join(parts, ",")
}

// FormatWorkloadKind renders gk as a workloadKinds entry: "Group/Kind",
// with the core group written as "core/Kind".
func FormatWorkloadKind(gk schema.GroupKind) string {
	if gk.Group == "" {
		return coreGroupSpelling + "/" + gk.Kind
	}
	return gk.Group + "/" + gk.Kind
}

// ParseWorkloadKind parses one "Group/Kind" entry. Shape only; whether
// the kind is known is checked by parseWorkloadKinds.
func ParseWorkloadKind(entry string) (schema.GroupKind, error) {
	parts := strings.Split(entry, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return schema.GroupKind{}, fmt.Errorf("want Group/Kind (for example apps/Deployment; the core group is written core/Kind)")
	}
	if strings.ContainsAny(entry, " \t") {
		return schema.GroupKind{}, fmt.Errorf("must not contain whitespace")
	}
	gk := schema.GroupKind{Group: parts[0], Kind: parts[1]}
	if gk.Group == coreGroupSpelling {
		gk.Group = ""
	}
	return gk, nil
}

// knownWorkloadKindsList renders KnownWorkloadKinds for error messages.
func knownWorkloadKindsList() string {
	parts := make([]string, 0, len(KnownWorkloadKinds))
	for _, gk := range KnownWorkloadKinds {
		parts = append(parts, FormatWorkloadKind(gk))
	}
	return strings.Join(parts, ", ")
}

func isKnownWorkloadKind(gk schema.GroupKind) bool {
	for _, k := range KnownWorkloadKinds {
		if k == gk {
			return true
		}
	}
	return false
}

// parseWorkloadKinds validates spec.discovery.workloadKinds. Every
// failing entry is reported (all-or-nothing on invalid, like patterns),
// each with its index so the operator can find it. An absent or empty
// list is the unrestricted set.
func parseWorkloadKinds(entries []string) (WorkloadKindSet, []LoadError) {
	if len(entries) == 0 {
		return AllWorkloadKinds(), nil
	}
	var errs []LoadError
	seen := make(map[schema.GroupKind]int, len(entries))
	var kinds []schema.GroupKind
	for i, entry := range entries {
		gk, err := ParseWorkloadKind(entry)
		if err != nil {
			errs = append(errs, errWorkloadKindMalformed(i, entry, err))
			continue
		}
		if !isKnownWorkloadKind(gk) {
			errs = append(errs, errWorkloadKindUnknown(i, entry))
			continue
		}
		if first, dup := seen[gk]; dup {
			errs = append(errs, errWorkloadKindDuplicate(i, entry, first))
			continue
		}
		seen[gk] = i
		kinds = append(kinds, gk)
	}
	if len(errs) > 0 {
		return AllWorkloadKinds(), errs
	}
	return NewWorkloadKindSet(kinds...), nil
}
