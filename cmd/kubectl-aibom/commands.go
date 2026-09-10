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
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// The command logic operates on unstructured AIBOM objects so the plugin
// works against both served API versions and never needs a scheme. All
// functions here are pure (object in, bytes/rows out) — main.go owns the
// client, these own the behavior, and the tests exercise these directly.

// errTruncated is returned by inlineBOM when the document is not carried
// inline; the message tells the operator how to recover the full BOM.
type errTruncated struct{ reason string }

func (e errTruncated) Error() string {
	if e.reason == "" {
		return "BOM is not stored inline (truncated or external); configure an external sink or fetch from the configured sink"
	}
	return "BOM is not stored inline: " + e.reason
}

// inlineBOM extracts and decodes status.bomDocument.inline.data.
func inlineBOM(u *unstructured.Unstructured) ([]byte, error) {
	data, found, err := unstructured.NestedString(u.Object, "status", "bomDocument", "inline", "data")
	if err != nil {
		return nil, fmt.Errorf("reading status.bomDocument.inline.data: %w", err)
	}
	if !found || data == "" {
		truncated, _, _ := unstructured.NestedBool(u.Object, "status", "bomDocument", "truncated")
		reason, _, _ := unstructured.NestedString(u.Object, "status", "bomDocument", "truncationReason")
		if truncated {
			return nil, errTruncated{reason: reason}
		}
		if ext, found, _ := unstructured.NestedMap(u.Object, "status", "bomDocument", "external"); found && len(ext) > 0 {
			return nil, errTruncated{reason: "document is in an external sink (status.bomDocument.external)"}
		}
		return nil, fmt.Errorf("no BOM document in status (is the AIBOM Ready?)")
	}
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return nil, fmt.Errorf("decoding inline BOM: %w", err)
	}
	return raw, nil
}

// runView writes the BOM document to w — pretty-printed JSON by default,
// canonical bytes when raw is set (the form the published sha256 covers).
func runView(u *unstructured.Unstructured, raw bool, w io.Writer) error {
	doc, err := inlineBOM(u)
	if err != nil {
		return err
	}
	if raw {
		_, err = w.Write(doc)
		return err
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, doc, "", "  "); err != nil {
		return fmt.Errorf("BOM is not valid JSON (view --raw to inspect): %w", err)
	}
	pretty.WriteByte('\n')
	_, err = w.Write(pretty.Bytes())
	return err
}

// runVerify recomputes sha256 over the canonical inline bytes and
// compares it to the published status.bomDocument.sha256. It reports both
// digests; the returned bool is the verdict (false = mismatch, which
// main exits non-zero on).
func runVerify(u *unstructured.Unstructured, w io.Writer) (bool, error) {
	published, found, err := unstructured.NestedString(u.Object, "status", "bomDocument", "sha256")
	if err != nil || !found || published == "" {
		return false, fmt.Errorf("no published sha256 in status.bomDocument")
	}
	doc, err := inlineBOM(u)
	if err != nil {
		return false, err
	}
	sum := sha256.Sum256(doc)
	computed := hex.EncodeToString(sum[:])
	match := computed == strings.TrimPrefix(published, "sha256:")
	fmt.Fprintf(w, "published: %s\ncomputed:  %s\n", published, computed)
	if match {
		fmt.Fprintf(w, "OK: document bytes match the published digest\n")
	} else {
		fmt.Fprintf(w, "MISMATCH: document does not match the published digest\n")
	}
	return match, nil
}

// summaryRow flattens one AIBOM into table columns from status.summary.
func summaryRow(u *unstructured.Unstructured) []string {
	get := func(fields ...string) string {
		v, _, _ := unstructured.NestedString(u.Object, fields...)
		return v
	}
	models := "-"
	if list, found, _ := unstructured.NestedSlice(u.Object, "status", "summary", "models"); found && len(list) > 0 {
		names := make([]string, 0, len(list))
		seen := map[string]bool{}
		for _, m := range list {
			mm, ok := m.(map[string]interface{})
			if !ok {
				continue
			}
			if n, ok := mm["identity"].(string); ok && n != "" && !seen[n] {
				names = append(names, n)
				seen[n] = true
			}
		}
		if len(names) > 0 {
			models = strings.Join(names, ",")
		}
	}
	ready := "-"
	if conds, found, _ := unstructured.NestedSlice(u.Object, "status", "conditions"); found {
		for _, c := range conds {
			cm, ok := c.(map[string]interface{})
			if !ok {
				continue
			}
			if cm["type"] == "Ready" {
				if s, ok := cm["status"].(string); ok {
					ready = s
				}
			}
		}
	}
	runtime := get("status", "summary", "runtime", "name")
	if runtime == "" {
		runtime = "-"
	}
	return []string{
		u.GetNamespace(),
		u.GetName(),
		orDash(get("status", "summary", "workload", "kind")),
		orDash(get("status", "summary", "workload", "name")),
		orDash(get("status", "summary", "workload", "category")),
		runtime,
		models,
		orDash(get("status", "summary", "confidence")),
		ready,
	}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// runSummary renders the table for a list of AIBOMs.
func runSummary(items []unstructured.Unstructured, w io.Writer) error {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAMESPACE\tNAME\tWORKLOAD\tWORKLOAD-NAME\tCATEGORY\tRUNTIME\tMODELS\tCONFIDENCE\tREADY")
	for i := range items {
		fmt.Fprintln(tw, strings.Join(summaryRow(&items[i]), "\t"))
	}
	return tw.Flush()
}
