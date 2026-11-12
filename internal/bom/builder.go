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

package bom

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	cdx "github.com/CycloneDX/cyclonedx-go"
	"github.com/google/uuid"

	"github.com/GoogleCloudPlatform/k8s-aibom/internal/scraper"
)

// BuildOptions carries the workload-level metadata that the BOMInputs
// alone does not — namely, the workload identity that becomes the BOM's
// metadata.component (the "subject" the BOM describes).
type BuildOptions struct {
	WorkloadKind      string // e.g., "Deployment"
	WorkloadGroup     string // e.g., "apps"
	WorkloadAPIVer    string // e.g., "v1"
	WorkloadNamespace string
	WorkloadName      string
	WorkloadUID       string
	WorkloadCategory  string // "inference" in v1
	ControllerName    string // emitter identifier, recorded in metadata.tools
	ControllerVersion string
	// OwnedWorkloads lists tracked workloads this document absorbs under
	// the ownership roll-up (Design 005), as "Kind/name", already sorted.
	// Emitted as k8s-aibom:rollup:owned:<i> metadata properties; empty
	// for documents that absorb nothing.
	OwnedWorkloads []string
	// InferencePools lists the InferencePools whose selector matches one
	// of the workload's Pod templates (Design 007 §2, Part 2a), already
	// sorted and deduplicated. Emitted on metadata.component as
	// k8s-aibom:routing:inferencePool:<i> (and :endpointPicker); empty
	// for workloads no pool matches (byte-identical to before).
	InferencePools []scraper.InferencePoolMembership
}

// Builder converts a *scraper.BOMInputs (plus per-workload metadata) into
// a *Document containing a CycloneDX 1.6 ML-BOM. The Builder is the only
// component that depends on cyclonedx-go; the scraper layer remains
// CycloneDX-agnostic.
//
// Builder is safe for concurrent invocation. The clock function `now` is
// injectable so tests can pin metadata.timestamp; in production it is
// time.Now().
type Builder struct {
	now func() time.Time
}

// NewBuilder constructs a Builder that uses time.Now for metadata
// timestamps. Tests should set the clock via WithClock.
func NewBuilder() *Builder {
	return &Builder{now: time.Now}
}

// WithClock returns a Builder using the given clock for metadata
// timestamps. Used by tests to pin output deterministically.
func (b *Builder) WithClock(now func() time.Time) *Builder {
	nb := *b
	nb.now = now
	return &nb
}

// Build converts BOMInputs + per-workload options into a *Document
// carrying a CycloneDX 1.6 BOM. The resulting Document has both the
// structured CDX form (for sinks that introspect) and the canonical JSON
// serialization (for sinks that emit bytes), plus a sha256 of the JSON.
//
// Build is deterministic: identical inputs (including the injected clock)
// produce a byte-identical Document.JSON and SHA256.
func (b *Builder) Build(inputs *scraper.BOMInputs, opts BuildOptions) (*Document, error) {
	if inputs == nil {
		return nil, fmt.Errorf("bom: BOMInputs is nil")
	}
	ts := b.now().UTC().Format(time.RFC3339)
	root := buildRootComponent(opts)
	bom := &cdx.BOM{
		BOMFormat:    "CycloneDX",
		SpecVersion:  cdx.SpecVersion1_6,
		SerialNumber: serialNumber(opts.WorkloadUID),
		Version:      1,
		Metadata: &cdx.Metadata{
			Timestamp: ts,
			// Every document is an as-deployed inventory observed from
			// the Kubernetes API. CycloneDX expresses that context as a
			// lifecycle phase on the metadata (the maintainers' guidance
			// on spec#990), not as a perspective or a property; a
			// training-side record of the same model would be a separate
			// document with a different phase.
			Lifecycles: &[]cdx.Lifecycle{{Phase: cdx.LifecyclePhaseOperations}},
			Tools: &cdx.ToolsChoice{
				Components: &[]cdx.Component{{
					Type:    cdx.ComponentTypeApplication,
					Name:    opts.ControllerName,
					Version: opts.ControllerVersion,
				}},
			},
			Component:  root,
			Properties: buildMetadataProperties(inputs, opts),
		},
	}
	comps, err := buildComponents(inputs.Components, opts, "")
	if err != nil {
		return nil, err
	}
	if len(comps) > 0 {
		bom.Components = &comps
	}
	bom.Dependencies = buildDependencies(root.BOMRef, comps)
	if svcs := buildServices(inputs.Services); len(svcs) > 0 {
		bom.Services = &svcs
	}

	// Canonical JSON serialization. We use cyclonedx-go's encoder with no
	// indentation so the output is compact. The encoder writes JSON to the
	// buffer in insertion order; combined with the scraper's deterministic
	// component ordering, this produces byte-stable output.
	var buf bytes.Buffer
	enc := cdx.NewBOMEncoder(&buf, cdx.BOMFileFormatJSON)
	enc.SetPretty(false)
	if err := enc.EncodeVersion(bom, cdx.SpecVersion1_6); err != nil {
		return nil, fmt.Errorf("bom: encode: %w", err)
	}
	jsonBytes := buf.Bytes()
	sum := sha256.Sum256(jsonBytes)

	return &Document{
		Format:  FormatCycloneDX,
		Version: CycloneDXSpecVersion,
		JSON:    jsonBytes,
		SHA256:  hex.EncodeToString(sum[:]),
		CDX:     bom,
	}, nil
}

// serialNamespace is the UUID namespace of document serial numbers: the
// UUID v5 of "https://github.com/GoogleCloudPlatform/k8s-aibom" in the
// RFC 4122 URL namespace, so a peer project can derive it, and any
// workload's serial, without the cluster (docs/schema-divergences.md).
var serialNamespace = uuid.MustParse("c8b031ec-958e-5f54-93dd-f9b3da80bfd0")

// serialNumber returns the document's CycloneDX serialNumber: a UUID v5 of
// the workload's UID in serialNamespace, as a urn:uuid. The serial is a
// pure function of the UID, so the same workload keeps it across
// reconciles and controller restarts and another document can BOM-Link to
// ours (urn:cdx:<serial>/<version>#<bom-ref>). With no UID it is empty and
// the field is omitted, rather than one serial shared by every such
// document.
func serialNumber(workloadUID string) string {
	if workloadUID == "" {
		return ""
	}
	return uuid.NewSHA1(serialNamespace, []byte(workloadUID)).URN()
}

// buildRootComponent constructs the metadata.component entry that
// identifies the workload itself — the "subject" the BOM is describing.
func buildRootComponent(opts BuildOptions) *cdx.Component {
	props := []cdx.Property{
		prop("aibom.workload.kind", opts.WorkloadKind),
		prop("aibom.workload.group", opts.WorkloadGroup),
		prop("aibom.workload.apiVersion", opts.WorkloadAPIVer),
		prop("aibom.workload.namespace", opts.WorkloadNamespace),
		prop("aibom.workload.name", opts.WorkloadName),
		prop("aibom.workload.uid", opts.WorkloadUID),
		prop("aibom.workload.category", opts.WorkloadCategory),
	}
	props = append(props, routingProperties(opts.InferencePools)...)
	c := &cdx.Component{
		BOMRef:     workloadBOMRef(opts),
		Type:       cdx.ComponentTypeApplication,
		Name:       opts.WorkloadName,
		Group:      opts.WorkloadNamespace,
		Properties: filterProps(props),
	}
	return c
}

// Routing property names (Design 007 §2, Part 2a; the accepted
// k8s-aibom:routing sub-namespace of docs/cyclonedx-property-taxonomy.md).
const (
	RoutingInferencePoolPrefix  = "k8s-aibom:routing:inferencePool:"
	routingEndpointPickerSuffix = ":endpointPicker"
)

// routingProperties renders InferencePool membership as indexed root
// properties: k8s-aibom:routing:inferencePool:<i> = <namespace>/<name>,
// and :endpointPicker when the pool names one. The input is already
// sorted, so the numbering is deterministic.
func routingProperties(pools []scraper.InferencePoolMembership) []cdx.Property {
	var out []cdx.Property
	for i, p := range pools {
		name := fmt.Sprintf("%s%d", RoutingInferencePoolPrefix, i)
		out = append(out, cdx.Property{Name: name, Value: p.Pool})
		if p.EndpointPicker != "" {
			out = append(out, cdx.Property{Name: name + routingEndpointPickerSuffix, Value: p.EndpointPicker})
		}
	}
	return out
}

// buildMetadataProperties packs scrape-time provenance and the aggregate
// workload-level confidence into metadata.properties so consumers can read
// them without descending into individual components.
func buildMetadataProperties(inputs *scraper.BOMInputs, opts BuildOptions) *[]cdx.Property {
	props := []cdx.Property{
		prop("aibom.confidence", string(inputs.Confidence)),
		prop("aibom.controller.name", opts.ControllerName),
		prop("aibom.controller.version", opts.ControllerVersion),
	}
	if inputs.TruncatedComponents > 0 {
		// Mirror of the redaction marker: a consumer must be able to
		// distinguish "complete document" from "capped document".
		props = append(props, prop("aibom.truncation.applied",
			fmt.Sprintf("components-dropped:%d", inputs.TruncatedComponents)))
	}
	// Absorbed workloads (Design 005): one indexed property each, so a
	// consumer can see which StatefulSets, Deployments or Jobs this
	// document stands in for.
	for i, o := range opts.OwnedWorkloads {
		props = append(props, prop(fmt.Sprintf("aibom.rollup.owned.%d", i), o))
	}
	// One property block per Provenance entry. Index suffix keeps names
	// unique under sort-by-name consumers.
	for i, p := range inputs.Provenance {
		base := fmt.Sprintf("aibom.scrape.%d", i)
		props = append(props,
			prop(base+".scraperName", p.ScraperName),
			prop(base+".scraperVersion", p.ScraperVersion),
			prop(base+".method", p.ScrapeMethod),
			prop(base+".timestamp", p.ScrapeTimestamp.UTC().Format(time.RFC3339)),
		)
	}
	out := filterProps(props)
	if out == nil {
		return nil
	}
	return out
}

// buildComponents maps internal scraper.Components into CycloneDX
// components, preserving Evidence + Confidence as properties on each
// component so auditors can trace every value back to its source.
//
// Returns an error if any component contains a value the builder cannot
// translate into valid CycloneDX (currently: unknown hash algorithm
// keys). The builder's contract is "if I return without error, the
// result is schema-valid"; pushing validation downstream would leak
// implementation details upward.
func buildComponents(in []scraper.Component, opts BuildOptions, parentRef string) ([]cdx.Component, error) {
	out := make([]cdx.Component, 0, len(in))
	for i, c := range in {
		cdxComp, err := toCDXComponent(c, opts, i, parentRef)
		if err != nil {
			return nil, fmt.Errorf("component %d (%s/%s): %w", i, c.Type, c.Name, err)
		}
		out = append(out, cdxComp)
	}
	return out, nil
}

// toCDXComponent translates one internal Component. The index argument is
// used to disambiguate BOMRef when two components share name+type
// (e.g., two ml-model components both named "meta-llama/Llama-3.1-8B-Instruct"
// but with different evidence sources).
func toCDXComponent(c scraper.Component, opts BuildOptions, idx int, parentRef string) (cdx.Component, error) {
	// Output sanitization boundary (redact.go / issue #57): every string
	// leaving the cluster in a component passes through redaction here.
	// BOMRef is computed from the redacted component so refs never carry
	// raw credential material either.
	c = redactComponent(c)
	out := cdx.Component{
		BOMRef:  componentBOMRef(opts, c, idx, parentRef),
		Type:    mapComponentType(c.Type),
		Name:    c.Name,
		Version: c.Version,
	}
	if len(c.Hashes) > 0 {
		hashes, err := mapHashes(c.Hashes)
		if err != nil {
			return cdx.Component{}, err
		}
		out.Hashes = hashes
	}
	if c.PURL != "" {
		out.PackageURL = c.PURL
	}
	props := []cdx.Property{
		prop("aibom.confidence", string(c.Confidence)),
		prop("aibom.evidence.source", string(c.Evidence.Source)),
		prop("aibom.evidence.locator", c.Evidence.Locator),
	}
	// Scraper keys are sorted under their internal spelling; the mapping
	// to taxonomy names is order-preserving for every key in use, so the
	// emitted order is deterministic either way.
	for _, k := range sortedKeys(c.Properties) {
		props = append(props, prop(k, c.Properties[k]))
	}
	out.Properties = filterProps(props)
	if len(c.Children) > 0 {
		children, err := buildComponents(c.Children, opts, out.BOMRef)
		if err != nil {
			return cdx.Component{}, err
		}
		out.Components = &children
	}
	return out, nil
}

// buildDependencies emits the document's dependency graph: one entry
// for the workload (metadata.component) depending on every top-level
// component, in document order. Nested components and edges between
// components are not emitted, because nothing the scrapers observe says
// that one component depends on another (a model is not shown to be a
// dependency of the runtime that serves it). The workload entry is
// present even with no components, so consumers that look for the
// subject in the graph (Dependency-Track) always find it.
func buildDependencies(workloadRef string, comps []cdx.Component) *[]cdx.Dependency {
	dep := cdx.Dependency{Ref: workloadRef}
	if len(comps) > 0 {
		refs := make([]string, 0, len(comps))
		for _, c := range comps {
			refs = append(refs, c.BOMRef)
		}
		dep.Dependencies = &refs
	}
	return &[]cdx.Dependency{dep}
}

// buildServices maps internal Services. v1 scrapers do not currently
// produce these (none of the apps/v1 or KServe paths emit Services);
// the mapping is in place for future scrapers (eBPF-based observation,
// mesh-telemetry-derived endpoints, etc.) that will populate them.
func buildServices(in []scraper.Service) []cdx.Service {
	if len(in) == 0 {
		return nil
	}
	out := make([]cdx.Service, 0, len(in))
	for _, s := range in {
		// Output sanitization boundary (redact.go / issue #57): service
		// endpoints are the likeliest future URI carriers.
		s, redacted := redactService(s)
		props := []cdx.Property{
			prop("aibom.evidence.source", string(s.Evidence.Source)),
			prop("aibom.evidence.locator", s.Evidence.Locator),
		}
		if len(redacted) > 0 {
			props = append(props, prop(redactionPropertyKey, strings.Join(redacted, ",")))
		}
		out = append(out, cdx.Service{
			Name: s.Name,
			Endpoints: func() *[]string {
				if len(s.Endpoints) == 0 {
					return nil
				}
				eps := append([]string(nil), s.Endpoints...)
				return &eps
			}(),
			Properties: filterProps(props),
		})
	}
	return out
}

func mapComponentType(t scraper.ComponentType) cdx.ComponentType {
	switch t {
	case scraper.ComponentContainer:
		return cdx.ComponentTypeContainer
	case scraper.ComponentApplication:
		return cdx.ComponentTypeApplication
	case scraper.ComponentMLModel:
		return cdx.ComponentTypeMachineLearningModel
	case scraper.ComponentData:
		return cdx.ComponentTypeData
	}
	// Fail-closed: an unrecognized internal type maps to application,
	// which is the most generic and never produces an invalid CycloneDX
	// document. The unknown type is recorded as a property by the caller.
	return cdx.ComponentTypeApplication
}

// mapHashes translates the internal hashes map to CycloneDX's
// []Hash slice, sorted by algorithm name for determinism. Returns an
// error if any algorithm key is not in the supported set — see
// cdxHashAlgFor for the rationale.
func mapHashes(in map[string]string) (*[]cdx.Hash, error) {
	if len(in) == 0 {
		return nil, nil
	}
	keys := sortedKeys(in)
	out := make([]cdx.Hash, 0, len(keys))
	for _, k := range keys {
		alg, err := cdxHashAlgFor(k)
		if err != nil {
			return nil, err
		}
		out = append(out, cdx.Hash{
			Algorithm: alg,
			Value:     in[k],
		})
	}
	return &out, nil
}

// supportedHashAlgKeys is the closed set of hash algorithm keys the
// builder accepts. Listed in the error message returned by cdxHashAlgFor
// for actionable guidance.
var supportedHashAlgKeys = []string{"sha256", "sha384", "sha512", "sha1", "md5"}

// cdxHashAlgFor maps a hash-algorithm key (e.g., "sha256") to the
// CycloneDX HashAlgorithm enum. Returns an error for unknown keys —
// the builder's contract is "if I return without error, the BOM is
// schema-valid", so unknown hash algorithms fail here rather than
// being passed through to the schema validator at sink time.
func cdxHashAlgFor(key string) (cdx.HashAlgorithm, error) {
	switch key {
	case "sha256":
		return cdx.HashAlgoSHA256, nil
	case "sha384":
		return cdx.HashAlgoSHA384, nil
	case "sha512":
		return cdx.HashAlgoSHA512, nil
	case "sha1":
		return cdx.HashAlgoSHA1, nil
	case "md5":
		return cdx.HashAlgoMD5, nil
	}
	return "", fmt.Errorf("unknown hash algorithm key: %q; supported: %s",
		key, strings.Join(supportedHashAlgKeys, ", "))
}

// filterProps drops properties with empty values and returns a pointer
// to the slice (cyclonedx-go uses *[]Property to distinguish absent from
// empty). Returns nil if no properties remain, which results in the
// properties field being omitted entirely (omitempty).
func filterProps(in []cdx.Property) *[]cdx.Property {
	var out []cdx.Property
	for _, p := range in {
		if p.Value == "" {
			continue
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil
	}
	return &out
}

// prop builds one emitted property from a scraper-layer key, mapping the
// key through PropertyName so every name in the document is under the
// registered taxonomy namespace.
func prop(key, value string) cdx.Property {
	return cdx.Property{Name: PropertyName(key), Value: value}
}

// sortedKeys returns the keys of a map[string]string in lexical order so
// downstream iteration is deterministic.
func sortedKeys(m map[string]string) []string {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// workloadBOMRef constructs a stable BOMRef for the workload's
// metadata.component. The shape is intentionally human-readable so
// auditors looking at the BOM can navigate.
func workloadBOMRef(opts BuildOptions) string {
	return fmt.Sprintf("workload:%s/%s/%s", opts.WorkloadNamespace, opts.WorkloadKind, opts.WorkloadName)
}

// componentBOMRef constructs a stable BOMRef for a component. Uniqueness
// is ensured by including the index, which encodes the component's
// position in the deterministically-sorted scraper output.
func componentBOMRef(opts BuildOptions, c scraper.Component, idx int, parentRef string) string {
	base := parentRef
	if base == "" {
		base = workloadBOMRef(opts)
	}
	return fmt.Sprintf("%s/component/%d/%s/%s", base, idx, c.Type, c.Name)
}
