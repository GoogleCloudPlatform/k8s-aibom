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

package scraper

import "testing"

const purlTestDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestOCIPURL(t *testing.T) {
	cases := []struct {
		name, ref, digest, want string
	}{
		{
			name:   "digest resolved, tag known",
			ref:    "vllm/vllm-openai:v0.6.3",
			digest: "sha256:" + purlTestDigest,
			want:   "pkg:oci/vllm-openai@sha256:" + purlTestDigest + "?repository_url=vllm%2Fvllm-openai&tag=v0.6.3",
		},
		{
			name:   "digest given bare, uppercase hex lowered",
			ref:    "vllm/vllm-openai:v0.6.3",
			digest: "0123456789ABCDEF0123456789abcdef0123456789abcdef0123456789abcdef",
			want:   "pkg:oci/vllm-openai@sha256:" + purlTestDigest + "?repository_url=vllm%2Fvllm-openai&tag=v0.6.3",
		},
		{
			name: "tag only: no version, never a fabricated digest",
			ref:  "nvcr.io/nim/meta/llama-3.1-8b-instruct:1.3.3",
			want: "pkg:oci/llama-3.1-8b-instruct?repository_url=nvcr.io%2Fnim%2Fmeta%2Fllama-3.1-8b-instruct&tag=1.3.3",
		},
		{
			name: "no tag, no digest",
			ref:  "ollama/ollama",
			want: "pkg:oci/ollama?repository_url=ollama%2Follama",
		},
		{
			name:   "digest in the reference, registry with port",
			ref:    "registry.internal:5000/team/Serving@sha256:" + purlTestDigest,
			digest: "sha256:" + purlTestDigest,
			want:   "pkg:oci/serving@sha256:" + purlTestDigest + "?repository_url=registry.internal:5000%2Fteam%2FServing",
		},
		{
			name: "single segment, tag",
			ref:  "triton:24.01-py3",
			want: "pkg:oci/triton?repository_url=triton&tag=24.01-py3",
		},
		{
			name: "empty reference",
			ref:  "",
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ociPURL(tc.ref, tc.digest); got != tc.want {
				t.Fatalf("ociPURL(%q, %q)\n got %s\nwant %s", tc.ref, tc.digest, got, tc.want)
			}
		})
	}
}

// The canonical oci example from the purl specification test suite,
// built from its parts, round-trips through the same encoder.
func TestOCIPURL_SpecCanonicalForm(t *testing.T) {
	got := ociPURL("docker.io/library/debian:latest", "sha256:244fd47e07d10")
	want := "pkg:oci/debian@sha256:244fd47e07d10?repository_url=docker.io%2Flibrary%2Fdebian&tag=latest"
	if got != want {
		t.Fatalf("got %s\nwant %s", got, want)
	}
}
