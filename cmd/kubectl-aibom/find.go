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
//     truncated, or not written yet) is SHOWN, not excluded, when an
//     image or digest filter is active — with IMAGES reading
//     external/truncated/no-document and a one-line stderr warning. Silently dropping it would turn "cannot evaluate"
//     into "not present", which is the one lie this command must not
//     tell. Zero matches is never proof of absence: namespaces that are
//     not opted in, unattributed images, and unresolved digests are all
//     invisible here by design.

type findFilter struct {
	runtime string // exact match on status.summary.runtime.name
	model   string // case-sensitive substring on status.summary.models[].identity
	image   string // substring on container image.reference (inline document)
	digest  string // sha256 hex, bare or sha256:-prefixed; exact if 64 chars, else prefix (inline document)
	signed  string // unsigned | claimed | verified — any model in that state
}

func (f findFilter) needsDocument() bool { return f.image != "" || f.digest != "" }

// containerFacts is what find needs from the inline CycloneDX document:
// the container image references and their SHA-256 digests.
type containerFacts struct {
	images  []string
	digests []string
}

// inlineContainers decodes the inline document and collects container
// facts. unavailable is "" when the document was read, otherwise
// "external", "truncated" or "no-document" (the reason the document is
// not inline). An AIBOM whose status has no document yet is one row the
// command cannot evaluate, not a reason for the whole command to fail.
func inlineContainers(u *unstructured.Unstructured) (facts containerFacts, unavailable string, err error) {
	// A document flagged truncated is unavailable for filtering even if
	// partial bytes are inline: a truncated BOM can never support the
	// negative conclusion "this image/digest is not here".
	if truncated, _, _ := unstructured.NestedBool(u.Object, "status", "bomDocument", "truncated"); truncated {
		return containerFacts{}, "truncated", nil
	}
	doc, err := inlineBOM(u)
	if err != nil {
		if errors.Is(err, errNoDocument) {
			return containerFacts{}, "no-document", nil
		}
		var et errTruncated
		if errors.As(err, &et) {
			if strings.Contains(et.reason, "external") {
				return containerFacts{}, "external", nil
			}
			return containerFacts{}, "truncated", nil
		}
		return containerFacts{}, "", err
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
		return containerFacts{}, "", fmt.Errorf("parsing inline document: %w", err)
	}
	for _, c := range parsed.Components {
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
	return facts, "", nil
}

// normalizeDigest strips an optional sha256: prefix and lower-cases.
func normalizeDigest(d string) string {
	d = strings.TrimSpace(d)
	d = strings.TrimPrefix(strings.TrimPrefix(d, "sha256:"), "SHA256:")
	return strings.ToLower(d)
}

func digestMatches(want string, have []string) bool {
	w := normalizeDigest(want)
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
// not be evaluated because the document is not inline (the row is then
// treated as matched and the caller warns).
func findMatch(u *unstructured.Unstructured, f findFilter) (matched bool, facts containerFacts, unavailable string, err error) {
	get := func(fields ...string) string {
		v, _, _ := unstructured.NestedString(u.Object, fields...)
		return v
	}
	if f.runtime != "" && get("status", "summary", "runtime", "name") != f.runtime {
		return false, containerFacts{}, "", nil
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
			return false, containerFacts{}, "", nil
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
			return false, containerFacts{}, "", nil
		}
	}
	// Image/digest criteria need the document. Always collect facts
	// (IMAGES column) even when no such filter is set.
	facts, unavailable, err = inlineContainers(u)
	if err != nil {
		return false, containerFacts{}, "", err
	}
	if f.needsDocument() && unavailable != "" {
		// Cannot evaluate: show, never exclude.
		return true, facts, unavailable, nil
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
			return false, facts, "", nil
		}
	}
	if f.digest != "" && !digestMatches(f.digest, facts.digests) {
		return false, facts, "", nil
	}
	return true, facts, unavailable, nil
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

// runFind prints the filtered table. It returns an error only for
// malformed objects; zero matches is a successful run.
func runFind(items []unstructured.Unstructured, f findFilter, w, errw io.Writer) error {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAMESPACE\tKIND\tNAME\tRUNTIME\tMODELS\tIMAGES\tSIGNED\tCONFIDENCE")
	matches := 0
	var unavailableRows []string
	for i := range items {
		u := &items[i]
		matched, facts, unavailable, err := findMatch(u, f)
		if err != nil {
			return fmt.Errorf("%s/%s: %w", u.GetNamespace(), u.GetName(), err)
		}
		if !matched {
			continue
		}
		matches++
		get := func(fields ...string) string {
			v, _, _ := unstructured.NestedString(u.Object, fields...)
			return v
		}
		identities, states := summaryModels(u)
		images := joinTruncated(facts.images, 2)
		if unavailable != "" {
			images = unavailable
			unavailableRows = append(unavailableRows, u.GetNamespace()+"/"+u.GetName())
		}
		seen := map[string]bool{}
		var uniqStates []string
		for _, st := range states {
			if !seen[st] {
				seen[st] = true
				uniqStates = append(uniqStates, st)
			}
		}
		// NAME is the AIBOM object name — what you pass to view/verify
		// next; KIND comes from the summary.
		fmt.Fprintln(tw, strings.Join([]string{
			u.GetNamespace(),
			orDash(get("status", "summary", "workload", "kind")),
			u.GetName(),
			orDash(get("status", "summary", "runtime", "name")),
			joinTruncated(identities, 2),
			images,
			joinTruncated(uniqStates, 3),
			orDash(get("status", "summary", "confidence")),
		}, "\t"))
	}
	if matches == 0 {
		fmt.Fprintln(w, "0 matches")
		return nil
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if len(unavailableRows) > 0 && f.needsDocument() {
		fmt.Fprintf(errw, "warning: %d row(s) have no inline document (external sink, truncated, or not written yet), so the image/digest filter could not be evaluated for them; they are shown, not excluded: %s\n",
			len(unavailableRows), strings.Join(unavailableRows, ", "))
	}
	return nil
}
