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
	"encoding/base64"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const testDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// containerDoc is a minimal emitted-shape CycloneDX document: one
// container component with image.reference and a SHA-256 hash, plus a
// model component that find must ignore for image/digest purposes.
func containerDoc(image, digest string) []byte {
	return []byte(`{"bomFormat":"CycloneDX","specVersion":"1.6","components":[
	 {"type":"container","name":"x","hashes":[{"alg":"SHA-256","content":"` + digest + `"}],
	  "properties":[{"name":"image.reference","value":"` + image + `"},{"name":"container.name","value":"vllm"}]},
	 {"type":"machine-learning-model","name":"m","properties":[{"name":"identity.confidence","value":"claimed"}]}
	]}`)
}

func findItems(t *testing.T) []unstructured.Unstructured {
	t.Helper()
	// fixtureAIBOM: runtime vllm, model Qwen/Qwen2.5-0.5B-Instruct,
	// signed verified, confidence declared, namespace prod-jobs.
	a := fixtureAIBOM(t, containerDoc("vllm/vllm-openai:v0.6.3", testDigest), false)
	// A second workload on a different runtime and image.
	b := fixtureAIBOM(t, containerDoc("ghcr.io/berriai/litellm:main-v1.81.0", strings.Repeat("ab", 32)), false)
	b.SetName("apps-deployment-gateway")
	_ = unstructured.SetNestedField(b.Object, "litellm", "status", "summary", "runtime", "name")
	_ = unstructured.SetNestedField(b.Object, "gateway", "status", "summary", "workload", "name")
	_ = unstructured.SetNestedSlice(b.Object, []interface{}{
		map[string]interface{}{"identity": "openai/gpt-4o", "signed": "unsigned"},
	}, "status", "summary", "models")
	return []unstructured.Unstructured{*a, *b, *fixtureTruncated()}
}

func runFindCapture(t *testing.T, items []unstructured.Unstructured, f findFilter) (out, errOut string) {
	t.Helper()
	var o, e bytes.Buffer
	if err := runFind(items, f, &o, &e); err != nil {
		t.Fatalf("runFind: %v", err)
	}
	return o.String(), e.String()
}

func TestFind_RuntimeExact(t *testing.T) {
	out, _ := runFindCapture(t, findItems(t), findFilter{runtime: "vllm"})
	if !strings.Contains(out, "vllm-qwen") || strings.Contains(out, "gateway") {
		t.Fatalf("runtime filter wrong:\n%s", out)
	}
	if strings.Contains(out, "0 matches") {
		t.Fatalf("expected a match:\n%s", out)
	}
}

func TestFind_ModelSubstringCaseSensitive(t *testing.T) {
	out, _ := runFindCapture(t, findItems(t), findFilter{model: "Qwen2.5"})
	if !strings.Contains(out, "vllm-qwen") || strings.Contains(out, "gateway") {
		t.Fatalf("model substring wrong:\n%s", out)
	}
	out, _ = runFindCapture(t, findItems(t), findFilter{model: "qwen2.5"})
	if !strings.Contains(out, "0 matches") {
		t.Fatalf("model match must be case-sensitive (registry identities are):\n%s", out)
	}
}

func TestFind_DigestBareAndPrefixed(t *testing.T) {
	for _, d := range []string{testDigest, "sha256:" + testDigest, testDigest[:12]} {
		out, _ := runFindCapture(t, findItems(t), findFilter{digest: d})
		if !strings.Contains(out, "vllm-qwen") || strings.Contains(out, "gateway") {
			t.Fatalf("digest %q wrong:\n%s", d, out)
		}
	}
}

func TestFind_ImageSubstring(t *testing.T) {
	out, _ := runFindCapture(t, findItems(t), findFilter{image: "berriai/litellm"})
	if !strings.Contains(out, "gateway") || strings.Contains(out, "vllm-qwen") {
		t.Fatalf("image filter wrong:\n%s", out)
	}
}

func TestFind_SignedState(t *testing.T) {
	out, _ := runFindCapture(t, findItems(t), findFilter{signed: "verified"})
	if !strings.Contains(out, "vllm-qwen") || strings.Contains(out, "gateway") {
		t.Fatalf("signed filter wrong:\n%s", out)
	}
}

func TestFind_FiltersAreANDed(t *testing.T) {
	out, _ := runFindCapture(t, findItems(t), findFilter{runtime: "vllm", image: "litellm"})
	if !strings.Contains(out, "0 matches") {
		t.Fatalf("AND of disjoint filters must be empty:\n%s", out)
	}
}

func TestFind_ZeroMatchesIsASuccessfulRun(t *testing.T) {
	out, errOut := runFindCapture(t, findItems(t), findFilter{runtime: "triton"})
	if strings.TrimSpace(out) != "0 matches" {
		t.Fatalf("zero matches must print exactly '0 matches' on stdout, got %q", out)
	}
	if errOut != "" {
		t.Fatalf("no warning expected: %q", errOut)
	}
}

func TestFind_UnavailableDocumentIsShownNotExcluded(t *testing.T) {
	// The truncated fixture cannot be evaluated for a digest filter. It
	// must appear in the output (IMAGES=truncated) with a warning — never
	// be silently counted as a non-match.
	out, errOut := runFindCapture(t, findItems(t), findFilter{digest: "deadbeef"})
	if !strings.Contains(out, "big") || !strings.Contains(out, "truncated") {
		t.Fatalf("truncated row must be shown under a digest filter:\n%s", out)
	}
	if !strings.Contains(errOut, "shown, not excluded") {
		t.Fatalf("expected a stderr warning naming the unevaluated row, got %q", errOut)
	}
	// And rows that WERE evaluated and did not match are excluded.
	if strings.Contains(out, "vllm-qwen") || strings.Contains(out, "gateway") {
		t.Fatalf("evaluated non-matches leaked into output:\n%s", out)
	}
}

func TestFind_NoFilterListsEverythingWithImages(t *testing.T) {
	out, _ := runFindCapture(t, findItems(t), findFilter{})
	for _, want := range []string{"NAMESPACE", "IMAGES", "vllm/vllm-openai:v0.6.3", "ghcr.io/berriai/litellm:main-v1.81.0", "big"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q:\n%s", want, out)
		}
	}
}

// A model named by both --model and an env var, and an image shared by
// an LWS leader and worker template, are listed once each in their
// columns, as summary already does for MODELS; the +N suffix counts
// distinct values only.
func TestFind_ColumnsListEachValueOnce(t *testing.T) {
	doc := []byte(`{"bomFormat":"CycloneDX","specVersion":"1.6","components":[
	 {"type":"container","name":"vllm","hashes":[{"alg":"SHA-256","content":"` + testDigest + `"}],
	  "properties":[{"name":"image.reference","value":"vllm/vllm-openai:v0.6.3"},{"name":"lws.role","value":"leader"}]},
	 {"type":"container","name":"vllm","hashes":[{"alg":"SHA-256","content":"` + testDigest + `"}],
	  "properties":[{"name":"image.reference","value":"vllm/vllm-openai:v0.6.3"},{"name":"lws.role","value":"worker"}]},
	 {"type":"container","name":"envoy","properties":[{"name":"image.reference","value":"envoyproxy/envoy:v1.31.0"}]}
	]}`)
	// fixtureAIBOM lists Qwen/Qwen2.5-0.5B-Instruct twice in
	// status.summary.models (container_arg and env_var).
	u := fixtureAIBOM(t, doc, false)
	out, _ := runFindCapture(t, []unstructured.Unstructured{*u}, findFilter{})
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("want a header and one row:\n%s", out)
	}
	got := strings.Fields(lines[1])
	if len(got) != 8 {
		t.Fatalf("want 8 columns, got %q", got)
	}
	if models := got[4]; models != "Qwen/Qwen2.5-0.5B-Instruct" {
		t.Errorf("MODELS = %q, want the identity once", models)
	}
	if images := got[5]; images != "vllm/vllm-openai:v0.6.3,envoyproxy/envoy:v1.31.0" {
		t.Errorf("IMAGES = %q, want each image once and no +N", images)
	}
}

// fixtureNoDocument is an AIBOM whose status has not been written yet:
// the controller creates the object before its status update lands, and
// a failed status write leaves it that way until the next reconcile.
func fixtureNoDocument() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{"name": "fresh", "namespace": "prod-jobs"},
	}}
}

func TestFind_RowWithoutDocumentDoesNotFailTheRun(t *testing.T) {
	items := append(findItems(t), *fixtureNoDocument())

	// No filter: every row is listed, the document-less one with
	// IMAGES=no-document, and the other rows are unaffected.
	out, errOut := runFindCapture(t, items, findFilter{})
	for _, want := range []string{"fresh", "no-document", "vllm/vllm-openai:v0.6.3", "ghcr.io/berriai/litellm:main-v1.81.0"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q:\n%s", want, out)
		}
	}
	if errOut != "" {
		t.Fatalf("no warning expected without an image/digest filter: %q", errOut)
	}

	// An image filter cannot be evaluated for the row: shown, not
	// excluded, with the warning — the same contract as truncated.
	out, errOut = runFindCapture(t, items, findFilter{image: "berriai/litellm"})
	if !strings.Contains(out, "gateway") || !strings.Contains(out, "fresh") || strings.Contains(out, "vllm-qwen") {
		t.Fatalf("image filter wrong with a document-less row:\n%s", out)
	}
	if !strings.Contains(errOut, "prod-jobs/fresh") || !strings.Contains(errOut, "shown, not excluded") {
		t.Fatalf("expected a warning naming the unevaluated row, got %q", errOut)
	}

	// A summary filter excludes it (it has no summary yet) without error.
	out, _ = runFindCapture(t, items, findFilter{runtime: "vllm"})
	if !strings.Contains(out, "vllm-qwen") || strings.Contains(out, "fresh") {
		t.Fatalf("runtime filter wrong with a document-less row:\n%s", out)
	}
}

func TestRunView_NoDocumentKeepsItsMessage(t *testing.T) {
	err := runView(fixtureNoDocument(), false, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "is the AIBOM Ready?") {
		t.Fatalf("want the not-Ready error, got %v", err)
	}
}

func TestFind_EmptyDigestIsNotAWildcard(t *testing.T) {
	// "sha256:" and whitespace normalize to "", which prefix-matched
	// every digest and listed every workload as running the image.
	for _, d := range []string{"sha256:", "SHA256:", "  "} {
		out, _ := runFindCapture(t, findItems(t), findFilter{digest: d})
		if strings.Contains(out, "vllm-qwen") || strings.Contains(out, "gateway") {
			t.Fatalf("digest %q matched rows it does not name:\n%s", d, out)
		}
	}
}

func TestFind_DigestPrefixIsCaseInsensitive(t *testing.T) {
	for _, d := range []string{"SHA256:" + testDigest, "Sha256:" + strings.ToUpper(testDigest[:12])} {
		out, _ := runFindCapture(t, findItems(t), findFilter{digest: d})
		if !strings.Contains(out, "vllm-qwen") || strings.Contains(out, "gateway") {
			t.Fatalf("digest %q wrong:\n%s", d, out)
		}
	}
}

func TestCheckDigestFilter(t *testing.T) {
	for _, d := range []string{testDigest, "sha256:" + testDigest, testDigest[:12], "SHA256:ABCDEF", " 0123 "} {
		if err := checkDigestFilter(d); err != nil {
			t.Errorf("checkDigestFilter(%q) = %v, want nil", d, err)
		}
	}
	for _, d := range []string{
		"sha256:",
		"   ",
		"sha256:" + testDigest + "0", // 65 hex characters
		"vllm/vllm-openai@sha256:" + testDigest,
		"sha512:" + testDigest,
		"0123-4567",
	} {
		if err := checkDigestFilter(d); err == nil {
			t.Errorf("checkDigestFilter(%q) = nil, want an error", d)
		}
	}
}

// fixtureUnreadable is an AIBOM whose status carries inline data that
// find cannot read: not base64, base64 of something that is not JSON,
// or a value that is not a string at all.
func fixtureUnreadable(t *testing.T, name string, data interface{}) *unstructured.Unstructured {
	t.Helper()
	u := fixtureAIBOM(t, containerDoc("vllm/vllm-openai:v0.6.3", testDigest), false)
	u.SetName(name)
	_ = unstructured.SetNestedField(u.Object, data, "status", "bomDocument", "inline", "data")
	return u
}

func TestFind_UnreadableDocumentDoesNotFailTheRun(t *testing.T) {
	bad := []unstructured.Unstructured{
		*fixtureUnreadable(t, "not-base64", "%%% not base64 %%%"),
		*fixtureUnreadable(t, "not-json", base64.StdEncoding.EncodeToString([]byte("{not json"))),
		*fixtureUnreadable(t, "not-a-string", int64(42)),
	}
	items := append(findItems(t), bad...)

	// No filter: every row is listed, the unreadable ones with
	// IMAGES=unreadable, and the other rows are unaffected.
	out, errOut := runFindCapture(t, items, findFilter{})
	for _, want := range []string{"not-base64", "not-json", "not-a-string", "unreadable", "ghcr.io/berriai/litellm:main-v1.81.0"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q:\n%s", want, out)
		}
	}
	if errOut != "" {
		t.Fatalf("no warning expected without an image/digest filter: %q", errOut)
	}

	// A summary filter still evaluates the rows: the summary is readable
	// even when the document is not.
	out, errOut = runFindCapture(t, items, findFilter{runtime: "litellm"})
	if !strings.Contains(out, "gateway") || strings.Contains(out, "not-json") || errOut != "" {
		t.Fatalf("runtime filter wrong with unreadable rows:\n%s\nstderr: %q", out, errOut)
	}

	// An image filter cannot be evaluated for them: shown, not excluded,
	// with the warning naming each row.
	out, errOut = runFindCapture(t, items, findFilter{image: "berriai/litellm"})
	if !strings.Contains(out, "gateway") || !strings.Contains(out, "not-json") || strings.Contains(out, "vllm-qwen") {
		t.Fatalf("image filter wrong with unreadable rows:\n%s", out)
	}
	for _, want := range []string{"prod-jobs/not-base64", "prod-jobs/not-json", "prod-jobs/not-a-string", "shown, not excluded"} {
		if !strings.Contains(errOut, want) {
			t.Fatalf("warning missing %q: %q", want, errOut)
		}
	}
}
