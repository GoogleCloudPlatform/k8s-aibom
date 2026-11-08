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
	"strings"
	"testing"

	cdx "github.com/CycloneDX/cyclonedx-go"
)

// metadata.lifecycles says what every k8s-aibom document is: an
// as-deployed inventory (phase operations), per CycloneDX guidance.
func TestBuild_LifecycleOperations(t *testing.T) {
	doc, err := newTestBuilder().Build(vllmFixtureBOMInputs(), testBuildOptions())
	if err != nil {
		t.Fatal(err)
	}
	lc := doc.CDX.Metadata.Lifecycles
	if lc == nil || len(*lc) != 1 || (*lc)[0].Phase != cdx.LifecyclePhaseOperations || (*lc)[0].Name != "" {
		t.Fatalf("metadata.lifecycles = %+v, want exactly [{phase: operations}]", lc)
	}
	if !strings.Contains(string(doc.JSON), `"lifecycles":[{"phase":"operations"}]`) {
		t.Fatalf("serialized lifecycles missing or malformed:\n%s", doc.JSON)
	}
}
