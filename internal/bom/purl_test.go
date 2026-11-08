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
	"testing"

	cdx "github.com/CycloneDX/cyclonedx-go"
)

// A container component's purl reaches the document unchanged, and a
// component without one gets none (the builder never fabricates image
// identity).
func TestBuild_ContainerPURL(t *testing.T) {
	doc, err := newTestBuilder().Build(vllmFixtureBOMInputs(), testBuildOptions())
	if err != nil {
		t.Fatal(err)
	}
	want := "pkg:oci/vllm-openai@sha256:" + testDigest + "?repository_url=vllm%2Fvllm-openai&tag=v0.6.3"
	var seen bool
	for _, c := range *doc.CDX.Components {
		switch c.Type {
		case cdx.ComponentTypeContainer:
			seen = true
			if c.PackageURL != want {
				t.Errorf("container purl = %q, want %q", c.PackageURL, want)
			}
		default:
			if c.PackageURL != "" {
				t.Errorf("%s %s: unexpected purl %q", c.Type, c.Name, c.PackageURL)
			}
		}
	}
	if !seen {
		t.Fatal("fixture has no container component")
	}
}
