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
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/GoogleCloudPlatform/k8s-aibom/internal/scraper"
)

// updateGolden, when set, regenerates the golden output files in
// testdata/golden/ to match the current builder output. Use via:
//
//	go test ./internal/bom/ -update-golden -run TestGolden
//
// or the Makefile shorthand:
//
//	make update-golden
//
// PR workflow on golden churn:
//  1. CI run fails on a TestGolden_* diff.
//  2. If the diff is an UNINTENDED behavior change — fix the builder.
//  3. If it's an INTENDED change — `make update-golden`, eyeball the
//     diff in the PR, commit the updated golden file.
//
// Locked, single-input golden test ensures regressions surface in PR
// review rather than at customer time.
var updateGolden = flag.Bool("update-golden", false,
	"regenerate golden output files in testdata/golden/ to match current builder output")

// TestGolden_VLLMDeployment locks the BOM the builder produces from a
// representative vLLM Deployment fixture. The fixture exercises:
//
//   - Container image with sha256 digest (from pod status)
//   - Image-pattern runtime detection (vllm)
//   - Volume-mount data component (PVC mounted at /models)
//   - Container-arg model claim (--model flag)
//   - Env-var model claim (HF_MODEL_ID)
//   - Workload-level Inferred confidence (mix of declared + inferred attributes)
//
// One realistic golden case is more valuable than several narrow ones —
// it serves as a worked example for new contributors and locks the
// majority of the builder's behavior in a single artifact.
func TestGolden_VLLMDeployment(t *testing.T) {
	inputJSON := mustReadTestdata(t, "golden/vllm-deployment-input.json")
	var inputs scraper.BOMInputs
	if err := json.Unmarshal(inputJSON, &inputs); err != nil {
		t.Fatalf("parse input fixture: %v", err)
	}

	b := newTestBuilder()
	doc, err := b.Build(&inputs, testBuildOptions())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	// First: BOM must be schema-valid. This is a hard precondition for
	// even considering it as a golden-output candidate.
	assertValidCycloneDX(t, doc.JSON)

	goldenPath := filepath.Join("testdata", "golden", "vllm-deployment-output.json")
	prettyGot := mustPrettyJSON(t, doc.JSON)

	if *updateGolden {
		if err := os.WriteFile(goldenPath, prettyGot, 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Logf("updated %s", goldenPath)
		return
	}

	prettyWant, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden (%s): %v\n(Run `make update-golden` to create it.)", goldenPath, err)
	}

	// Structural comparison: parse both sides and deep-equal. This is
	// robust to incidental whitespace differences in the golden file
	// (line endings, trailing newlines) while still failing on real
	// shape or value changes.
	var gotParsed, wantParsed any
	if err := json.Unmarshal(prettyGot, &gotParsed); err != nil {
		t.Fatalf("re-parse generated BOM: %v", err)
	}
	if err := json.Unmarshal(prettyWant, &wantParsed); err != nil {
		t.Fatalf("parse golden file %s: %v", goldenPath, err)
	}
	if !reflect.DeepEqual(gotParsed, wantParsed) {
		t.Errorf("BOM differs from golden %s.\n\nIf this change is INTENTIONAL, run:\n  make update-golden\nand commit the diff.\n\nGenerated BOM:\n%s",
			goldenPath, prettyGot)
	}
}

// TestGolden_InputFixtureRoundTrips is a sanity test on the input
// fixture: it must parse cleanly into BOMInputs (i.e., the v1alpha1
// internal types' JSON tags are stable). If a contributor breaks JSON
// tag stability on Component/Service/etc., this test fails before
// TestGolden_VLLMDeployment runs.
func TestGolden_InputFixtureRoundTrips(t *testing.T) {
	inputJSON := mustReadTestdata(t, "golden/vllm-deployment-input.json")
	var inputs scraper.BOMInputs
	if err := json.Unmarshal(inputJSON, &inputs); err != nil {
		t.Fatalf("input fixture failed to parse: %v", err)
	}
	if inputs.ScraperName == "" {
		t.Error("scraperName missing after round-trip")
	}
	if len(inputs.Components) == 0 {
		t.Error("no components after round-trip")
	}
	if len(inputs.Provenance) == 0 {
		t.Error("no provenance after round-trip")
	}
}

// ---------- helpers ----------

func mustReadTestdata(t *testing.T, relPath string) []byte {
	t.Helper()
	full := filepath.Join("testdata", relPath)
	b, err := os.ReadFile(full)
	if err != nil {
		t.Fatalf("read testdata %s: %v", full, err)
	}
	return b
}

// mustPrettyJSON indents JSON for human-readable golden diffs. Returns
// the indented bytes with a trailing newline (POSIX convention).
func mustPrettyJSON(t *testing.T, raw []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		t.Fatalf("indent JSON: %v", err)
	}
	buf.WriteByte('\n')
	return buf.Bytes()
}
