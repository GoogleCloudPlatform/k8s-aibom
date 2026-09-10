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

	"github.com/GoogleCloudPlatform/k8s-aibom/internal/scraper"
)

// Invariant vectors from the issue #57 audit. Each case is a string an
// operator or workload author could realistically get into an emitted
// field (KServe storageUri, model annotation, extended allowlist value).
func TestRedactString(t *testing.T) {
	cases := []struct {
		name        string
		in          string
		wantClasses []string
		wantGone    []string // substrings that must NOT survive
		wantKept    []string // substrings that must survive
	}{
		{
			name:        "clean model identity untouched",
			in:          "meta-llama/Llama-3.1-8B-Instruct",
			wantClasses: nil,
			wantKept:    []string{"meta-llama/Llama-3.1-8B-Instruct"},
		},
		{
			name:        "clean storage uri untouched",
			in:          "gs://models-bucket/llama-3.1/",
			wantClasses: nil,
			wantKept:    []string{"gs://models-bucket/llama-3.1/"},
		},
		{
			name:        "presigned S3 url query credentials stripped",
			in:          "https://bucket.s3.amazonaws.com/model.safetensors?X-Amz-Credential=AKIA0000EXAMPLE00000%2F20260910&X-Amz-Signature=deadbeefcafe&X-Amz-Expires=3600",
			wantClasses: []string{redactQueryParameter},
			wantGone:    []string{"deadbeefcafe", "AKIA0000EXAMPLE0"},
			wantKept:    []string{"bucket.s3.amazonaws.com/model.safetensors", "X-Amz-Expires=3600"},
		},
		{
			name:        "azure SAS sig stripped",
			in:          "https://acct.blob.core.windows.net/models/m.bin?sv=2024-01-01&sig=abc123secret&se=2026-12-31",
			wantClasses: []string{redactQueryParameter},
			wantGone:    []string{"abc123secret"},
			wantKept:    []string{"acct.blob.core.windows.net/models/m.bin", "sv=2024-01-01"},
		},
		{
			name:        "uri userinfo stripped",
			in:          "https://svc:hunter2token@models.internal/repo",
			wantClasses: []string{redactURIUserinfo},
			wantGone:    []string{"hunter2token"},
			wantKept:    []string{"https://", "@models.internal/repo"},
		},
		{
			name:        "openai-style key redacted",
			in:          "sk-proj0123456789abcdefghij",
			wantClasses: []string{redactTokenShape},
			wantGone:    []string{"sk-proj0123456789abcdefghij"},
		},
		{
			name:        "github token redacted",
			in:          "ghp_0123456789abcdefghijklmnop",
			wantClasses: []string{redactTokenShape},
			wantGone:    []string{"ghp_0123456789abcdefghijklmnop"},
		},
		{
			name:        "jwt redacted",
			in:          "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJtb2RlbHMifQ.c2lnbmF0dXJlLXBhcnQ",
			wantClasses: []string{redactTokenShape},
			wantGone:    []string{"c2lnbmF0dXJlLXBhcnQ"},
		},
		{
			name:        "hf model id with eyJ-free name untouched",
			in:          "google/gemma-3-27b-it",
			wantClasses: nil,
			wantKept:    []string{"google/gemma-3-27b-it"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, classes := redactString(tc.in)
			if len(tc.wantClasses) == 0 && len(classes) != 0 {
				t.Fatalf("expected no redaction, got classes %v (out=%q)", classes, got)
			}
			for _, want := range tc.wantClasses {
				found := false
				for _, c := range classes {
					if c == want {
						found = true
					}
				}
				if !found {
					t.Errorf("missing class %q in %v", want, classes)
				}
			}
			for _, g := range tc.wantGone {
				if strings.Contains(got, g) {
					t.Errorf("credential survived redaction: %q in %q", g, got)
				}
			}
			for _, k := range tc.wantKept {
				if !strings.Contains(got, k) {
					t.Errorf("legitimate content lost: %q not in %q", k, got)
				}
			}
			if len(tc.wantClasses) == 0 && got != tc.in {
				t.Errorf("clean input altered: %q -> %q", tc.in, got)
			}
		})
	}
}

// The redaction fact must be recorded on the component, and clean
// components must be byte-identical (determinism/golden safety).
func TestRedactComponentRecordsFact(t *testing.T) {
	dirty := scraper.Component{
		Type:       scraper.ComponentMLModel,
		Name:       "https://bucket.s3.amazonaws.com/m.bin?X-Amz-Signature=deadbeef",
		Confidence: scraper.ConfidenceDeclared,
		Properties: map[string]string{"identity.source": "kserve.storageUri"},
	}
	got := redactComponent(dirty)
	if strings.Contains(got.Name, "deadbeef") {
		t.Fatalf("signature survived: %q", got.Name)
	}
	if got.Properties[redactionPropertyKey] != redactQueryParameter {
		t.Fatalf("redaction fact missing/wrong: %q", got.Properties[redactionPropertyKey])
	}
	// Original must be untouched (copy semantics for the properties map).
	if strings.Contains(dirty.Properties[redactionPropertyKey], redactQueryParameter) {
		t.Fatalf("input component mutated")
	}

	clean := scraper.Component{
		Type:       scraper.ComponentMLModel,
		Name:       "facebook/opt-125m",
		Confidence: scraper.ConfidenceDeclared,
		Properties: map[string]string{"identity.confidence": "declared"},
	}
	gotClean := redactComponent(clean)
	if gotClean.Name != clean.Name {
		t.Fatalf("clean name altered: %q", gotClean.Name)
	}
	if _, ok := gotClean.Properties[redactionPropertyKey]; ok {
		t.Fatalf("redaction fact on clean component")
	}
}

func TestRedactServiceEndpoints(t *testing.T) {
	s := scraper.Service{
		Name:      "external-llm",
		Endpoints: []string{"https://user:tok3nvalue@api.example.com/v1"},
	}
	got, classes := redactService(s)
	if strings.Contains(got.Endpoints[0], "tok3nvalue") {
		t.Fatalf("endpoint credential survived: %q", got.Endpoints[0])
	}
	if len(classes) == 0 || classes[0] != redactURIUserinfo {
		t.Fatalf("expected uri-userinfo class, got %v", classes)
	}
	if s.Endpoints[0] != "https://user:tok3nvalue@api.example.com/v1" {
		t.Fatalf("input service mutated")
	}
}
