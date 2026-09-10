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
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func fixtureAIBOM(t *testing.T, doc []byte, tamperSHA bool) *unstructured.Unstructured {
	t.Helper()
	sum := sha256.Sum256(doc)
	published := hex.EncodeToString(sum[:])
	if tamperSHA {
		published = strings.Repeat("0", 64)
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "aibom.k8saibom.dev/v1beta1",
		"kind":       "AIBOM",
		"metadata": map[string]interface{}{
			"name":      "apps-deployment-vllm-qwen",
			"namespace": "prod-jobs",
		},
		"status": map[string]interface{}{
			"conditions": []interface{}{
				map[string]interface{}{"type": "Ready", "status": "True"},
				map[string]interface{}{"type": "Synced", "status": "True"},
			},
			"summary": map[string]interface{}{
				"workload": map[string]interface{}{
					"kind": "Deployment", "name": "vllm-qwen",
					"namespace": "prod-jobs", "category": "inference",
				},
				"runtime": map[string]interface{}{"name": "vllm"},
				"models": []interface{}{
					// Field is `identity` per ModelSummary — verified against
					// a live v1.4.0 AIBOM after the first draft wrongly
					// assumed `name` and rendered "-".
					map[string]interface{}{"identity": "Qwen/Qwen2.5-0.5B-Instruct", "source": "container_arg", "confidence": "declared"},
					map[string]interface{}{"identity": "Qwen/Qwen2.5-0.5B-Instruct", "source": "env_var", "confidence": "claimed"}, // dup identity: dedup expected
				},
				"confidence": "declared",
			},
			"bomDocument": map[string]interface{}{
				"format": "CycloneDX", "specVersion": "1.6",
				"sha256": published,
				"inline": map[string]interface{}{
					"data": base64.StdEncoding.EncodeToString(doc),
				},
			},
		},
	}}
}

func fixtureTruncated() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{"name": "big", "namespace": "prod-jobs"},
		"status": map[string]interface{}{
			"bomDocument": map[string]interface{}{
				"sha256":           strings.Repeat("a", 64),
				"truncated":        true,
				"truncationReason": "BOM size 384KB exceeds inline threshold 256KB and no external sink is configured",
			},
		},
	}}
}

func TestRunViewPrettyAndRaw(t *testing.T) {
	doc := []byte(`{"bomFormat":"CycloneDX","components":[{"name":"vllm"}]}`)
	u := fixtureAIBOM(t, doc, false)

	var raw bytes.Buffer
	if err := runView(u, true, &raw); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw.Bytes(), doc) {
		t.Fatalf("--raw altered bytes")
	}

	var pretty bytes.Buffer
	if err := runView(u, false, &pretty); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pretty.String(), "\n  \"bomFormat\": \"CycloneDX\"") {
		t.Fatalf("not pretty-printed: %q", pretty.String())
	}
}

func TestRunViewTruncatedIsActionable(t *testing.T) {
	err := runView(fixtureTruncated(), false, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "external sink") {
		t.Fatalf("want actionable truncation error, got %v", err)
	}
}

func TestRunVerify(t *testing.T) {
	doc := []byte(`{"bomFormat":"CycloneDX"}`)

	var out bytes.Buffer
	ok, err := runVerify(fixtureAIBOM(t, doc, false), &out)
	if err != nil || !ok {
		t.Fatalf("want match, got ok=%v err=%v", ok, err)
	}
	if !strings.Contains(out.String(), "OK:") {
		t.Fatalf("missing OK line: %q", out.String())
	}

	out.Reset()
	ok, err = runVerify(fixtureAIBOM(t, doc, true), &out)
	if err != nil || ok {
		t.Fatalf("want mismatch, got ok=%v err=%v", ok, err)
	}
	if !strings.Contains(out.String(), "MISMATCH") {
		t.Fatalf("missing MISMATCH line: %q", out.String())
	}
}

func TestRunSummaryTable(t *testing.T) {
	doc := []byte(`{}`)
	items := []unstructured.Unstructured{*fixtureAIBOM(t, doc, false), *fixtureTruncated()}
	var out bytes.Buffer
	if err := runSummary(items, &out); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	for _, want := range []string{
		"NAMESPACE", "READY",
		"apps-deployment-vllm-qwen", "Deployment", "vllm-qwen", "inference", "vllm",
		"declared", "True",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("summary missing %q:\n%s", want, s)
		}
	}
	// Deduplicated model list: name appears once per row.
	if strings.Count(s, "Qwen/Qwen2.5-0.5B-Instruct") != 1 {
		t.Fatalf("model not deduplicated:\n%s", s)
	}
	// The truncated fixture renders with dashes, not a crash.
	if !strings.Contains(s, "big") {
		t.Fatalf("second row missing:\n%s", s)
	}
}
