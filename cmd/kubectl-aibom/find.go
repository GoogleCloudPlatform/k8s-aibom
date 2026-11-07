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

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"text/tabwriter"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// find answers the incident question — "where is runtime R / model M /
// image I / digest D serving right now?" — as a filtered summary table.
// It reads the same AIBOM objects with the same RBAC as summary; there
// is no new CRD and no controller flag.
//
// Two contracts matter more than the filters:
//
//   - Exit 0 whenever the command ran, including zero matches, which
//     print "0 matches" on stdout. An incident script must be able to
//     tell "none found" from "plugin broke".
//   - A row whose inline document is unavailable (external sink,
//     truncated, not written yet, or unreadable) is SHOWN, not
//     excluded, when an image or digest filter is active — with IMAGES
//     reading external/truncated/no-document/unreadable and a one-line
//     stderr warning. Silently dropping it would turn "cannot evaluate"
//     into "not present", which is the one lie this command must not
//     tell. Under --runtime, such a row is matched against its summary
//     runtime, which names only one; if that does not match, the row is
//     left out and named in a stderr warning, even after "0 matches".
//     Zero matches is never proof of absence: namespaces that are
//     not opted in, unattributed images, and unresolved digests are all
//     invisible here by design.

type findFilter struct {
	runtime string // exact match on any status.summary runtime or any runtime in the inline document
	model   string // case-sensitive substring on status.summary.models[].identity
	image   string // substring on container image.reference (inline document)
	digest  string // sha256 hex, bare or sha256:-prefixed; exact if 64 chars, else prefix (inline document)
	signed  string // unsigned | claimed | verified — any model in that state
}

func (f findFilter) needsDocument() bool { return f.image != "" || f.digest != "" }

// containerFacts is what find needs from the inline CycloneDX document:
// the container image references and their SHA-256 digests, and the
// runtime names the document attributes (a controller older than
// status.summary.runtimes summarises only the first of them).
type containerFacts struct {
	images   []string
	digests  []string
	runtimes []string
}

// inlineContainers decodes the inline document and collects container
// facts. unavailable is "" when the document was read, otherwise
// "external", "truncated" or "no-document" (the reason the document is
// not inline), or "unreadable" when inline data is there but does not
// decode to a JSON document. An AIBOM whose document is missing or
// unreadable is one row the command cannot evaluate, not a reason for
// the whole command to fail; view and verify still report the error.
func inlineContainers(u *unstructured.Unstructured) (facts containerFacts, unavailable string) {
	// A document flagged truncated is unavailable for filtering even if
	// partial bytes are inline: a truncated BOM can never support the
	// negative conclusion "this image/digest is not here".
	if truncated, _, _ := unstructured.NestedBool(u.Object, "status", "bomDocument", "truncated"); truncated {
		return containerFacts{}, "truncated"
	}
	doc, err := inlineBOM(u)
	if err != nil {
		if errors.Is(err, errNoDocument) {
			return containerFacts{}, "no-document"
		}
		var et errTruncated
		if errors.As(err, &et) {
			if strings.Contains(et.reason, "external") {
				return containerFacts{}, "external"
			}
			return containerFacts{}, "truncated"
		}
		return containerFacts{}, "unreadable"
	}
	var parsed struct {
		Components []struct {
			Type   string `json:"type"`
			Hashes []struct {
				Alg     string `json:"alg"`
				Content string `json:"content"`
			} `json:"hashes"`
			Properties []struct {
				Name  string `json:"name"`
				Value string `json:"value"`
			} `json:"properties"`
		} `json:"components"`
	}
	if err := json.Unmarshal(doc, &parsed); err != nil {
		return containerFacts{}, "unreadable"
	}
	for _, c := range parsed.Components {
		if c.Type == "application" {
			for _, p := range c.Properties {
				if p.Name == "runtime.name" && p.Value != "" {
					facts.runtimes = append(facts.runtimes, p.Value)
				}
			}
			continue
		}
		if c.Type != "container" {
			continue
		}
		for _, p := range c.Properties {
			if p.Name == "image.reference" && p.Value != "" {
				facts.images = append(facts.images, p.Value)
			}
		}
		for _, h := range c.Hashes {
			if strings.EqualFold(h.Alg, "SHA-256") && h.Content != "" {
				facts.digests = append(facts.digests, strings.ToLower(h.Content))
			}
		}
	}
	return facts, ""
}

// normalizeDigest strips an optional sha256: prefix (any case) and
// lower-cases.
func normalizeDigest(d string) string {
	d = strings.ToLower(strings.TrimSpace(d))
	return strings.TrimPrefix(d, "sha256:")
}

// checkDigestFilter rejects a --digest value that cannot name a sha256
// digest or a prefix of one: empty once normalized (a bare "sha256:"
// would otherwise prefix-match every digest), not hex, or longer than
// 64 characters (which could never match). Run it before listing, so a
// typo is an error rather than a wrong answer.
func checkDigestFilter(d string) error {
	w := normalizeDigest(d)
	if w == "" || len(w) > 64 || strings.Trim(w, "0123456789abcdef") != "" {
		return fmt.Errorf("--digest must be a sha256 digest or a prefix of one: 1 to 64 hex characters, bare or sha256:-prefixed (got %q)", d)
	}
	return nil
}

func digestMatches(want string, have []string) bool {
	w := normalizeDigest(want)
	if w == "" {
		// Never a wildcard: checkDigestFilter rejects this up front.
		return false
	}
	for _, h := range have {
		if len(w) == 64 {
			if h == w {
				return true
			}
		} else if strings.HasPrefix(h, w) {
			return true
		}
	}
	return false
}

func summaryModels(u *unstructured.Unstructured) (identities []string, signedStates []string) {
	list, found, _ := unstructured.NestedSlice(u.Object, "status", "summary", "models")
	if !found {
		return nil, nil
	}
	for _, m := range list {
		mm, ok := m.(map[string]interface{})
		if !ok {
			continue
		}
		if id, ok := mm["identity"].(string); ok && id != "" {
			identities = append(identities, id)
		}
		st, _ := mm["signed"].(string)
		if st == "" {
			st = "unsigned"
		}
		signedStates = append(signedStates, st)
	}
	return identities, signedStates
}

// findMatch evaluates the filter against one AIBOM. matched reports the
// verdict; unavailable is non-empty when an image/digest filter could
// not be evaluated because the document is not inline or unreadable (the
// row is then treated as matched and the caller warns). A row that is
// not matched but has unavailable set passed every other filter, and
// --runtime could only be checked against an older controller's single
// summary runtime: the caller leaves it out and names it in a warning.
func findMatch(u *unstructured.Unstructured, f findFilter) (matched bool, facts containerFacts, unavailable string) {
	// Image/digest criteria and the runtime list need the document.
	// Always collect facts (IMAGES and RUNTIME columns) even when no
	// such filter is set.
	facts, unavailable = inlineContainers(u)
	runtimeUnchecked := false
	summaryRTs, summaryComplete := summaryRuntimes(u)
	if f.runtime != "" && !hasRuntime(summaryRTs, facts.runtimes, f.runtime) {
		if unavailable == "" || summaryComplete {
			return false, containerFacts{}, ""
		}
		// An older controller's summary names one runtime, and the
		// document that lists the others cannot be read: excluded, but
		// not silently.
		runtimeUnchecked = true
	}
	identities, states := summaryModels(u)
	if f.model != "" {
		hit := false
		for _, id := range identities {
			if strings.Contains(id, f.model) {
				hit = true
				break
			}
		}
		if !hit {
			return false, containerFacts{}, ""
		}
	}
	if f.signed != "" {
		hit := false
		for _, st := range states {
			if st == f.signed {
				hit = true
				break
			}
		}
		if !hit {
			return false, containerFacts{}, ""
		}
	}
	if runtimeUnchecked {
		return false, facts, unavailable
	}
	if f.needsDocument() && unavailable != "" {
		// Cannot evaluate: show, never exclude.
		return true, facts, unavailable
	}
	if f.image != "" {
		hit := false
		for _, img := range facts.images {
			if strings.Contains(img, f.image) {
				hit = true
				break
			}
		}
		if !hit {
			return false, facts, ""
		}
	}
	if f.digest != "" && !digestMatches(f.digest, facts.digests) {
		return false, facts, ""
	}
	return true, facts, unavailable
}

// summaryRuntimes returns the runtime names in status.summary: every
// entry of runtimes, or the single runtime a controller older than
// runtimes wrote. complete is true when the runtimes list is present,
// so the summary alone names every runtime in the document.
func summaryRuntimes(u *unstructured.Unstructured) (names []string, complete bool) {
	if list, found, _ := unstructured.NestedSlice(u.Object, "status", "summary", "runtimes"); found {
		for _, r := range list {
			rm, ok := r.(map[string]interface{})
			if !ok {
				continue
			}
			if n, ok := rm["name"].(string); ok && n != "" {
				names = append(names, n)
			}
		}
		if len(names) > 0 {
			return uniqueStrings(names), true
		}
	}
	if n, _, _ := unstructured.NestedString(u.Object, "status", "summary", "runtime", "name"); n != "" {
		names = append(names, n)
	}
	return names, false
}

// hasRuntime reports whether want is one of the summary runtimes or one
// of the runtimes in the document. A controller older than
// status.summary.runtimes summarises only the first runtime in the
// document, so a pod that runs vLLM next to a LiteLLM proxy has summary
// runtime litellm; --runtime vllm must still find it. A document that
// is not inline leaves only the summary to match.
func hasRuntime(summary, inDocument []string, want string) bool {
	return slices.Contains(summary, want) || slices.Contains(inDocument, want)
}

// rowRuntimes is the RUNTIME column: the summary runtimes first, then
// the document's other runtimes, each once.
func rowRuntimes(summary, inDocument []string) []string {
	all := append(slices.Clone(summary), inDocument...)
	return uniqueStrings(all)
}

// uniqueStrings returns items without repeats, in first-seen order.
// One document lists the same image once per container that runs it
// (an LWS leader and worker template, say), and the summary lists a
// model once per place it was found (--model and an env var).
func uniqueStrings(items []string) []string {
	seen := make(map[string]bool, len(items))
	out := make([]string, 0, len(items))
	for _, it := range items {
		if !seen[it] {
			seen[it] = true
			out = append(out, it)
		}
	}
	return out
}

// joinTruncated renders up to max items and a +N suffix for the rest;
// view remains the full record.
func joinTruncated(items []string, max int) string {
	if len(items) == 0 {
		return "-"
	}
	if len(items) <= max {
		return strings.Join(items, ",")
	}
	return strings.Join(items[:max], ",") + fmt.Sprintf(",+%d", len(items)-max)
}

// runFind prints the filtered table. Zero matches is a successful run,
// and so is a run with rows whose document could not be read.
func runFind(items []unstructured.Unstructured, f findFilter, w, errw io.Writer) error {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAMESPACE\tKIND\tNAME\tRUNTIME\tMODELS\tIMAGES\tSIGNED\tCONFIDENCE")
	matches := 0
	var unavailableRows, summaryOnlyRows []string
	for i := range items {
		u := &items[i]
		matched, facts, unavailable := findMatch(u, f)
		if !matched {
			if unavailable != "" {
				summaryOnlyRows = append(summaryOnlyRows, u.GetNamespace()+"/"+u.GetName())
			}
			continue
		}
		matches++
		get := func(fields ...string) string {
			v, _, _ := unstructured.NestedString(u.Object, fields...)
			return v
		}
		identities, states := summaryModels(u)
		summaryRTs, _ := summaryRuntimes(u)
		images := joinTruncated(uniqueStrings(facts.images), 2)
		if unavailable != "" {
			images = unavailable
			unavailableRows = append(unavailableRows, u.GetNamespace()+"/"+u.GetName())
		}
		// NAME is the AIBOM object name — what you pass to view/verify
		// next; KIND comes from the summary.
		fmt.Fprintln(tw, strings.Join([]string{
			u.GetNamespace(),
			orDash(get("status", "summary", "workload", "kind")),
			u.GetName(),
			joinTruncated(rowRuntimes(summaryRTs, facts.runtimes), 2),
			joinTruncated(uniqueStrings(identities), 2),
			images,
			joinTruncated(uniqueStrings(states), 3),
			orDash(get("status", "summary", "confidence")),
		}, "\t"))
	}
	if matches == 0 {
		fmt.Fprintln(w, "0 matches")
	} else {
		if err := tw.Flush(); err != nil {
			return err
		}
		if len(unavailableRows) > 0 && f.needsDocument() {
			fmt.Fprintf(errw, "warning: %d row(s) have no readable inline document (external sink, truncated, not written yet, or unreadable), so the image/digest filter could not be evaluated for them; they are shown, not excluded: %s\n",
				len(unavailableRows), strings.Join(unavailableRows, ", "))
		}
	}
	// Printed after "0 matches" too: that answer must not read as
	// "runtime R runs nowhere" when some documents were not checked.
	if len(summaryOnlyRows) > 0 {
		fmt.Fprintf(errw, "warning: %d row(s) have no readable inline document (external sink, truncated, not written yet, or unreadable), so --runtime was matched against their summary runtime only, which names one runtime; they are excluded: %s\n",
			len(summaryOnlyRows), strings.Join(summaryOnlyRows, ", "))
	}
	return nil
}
