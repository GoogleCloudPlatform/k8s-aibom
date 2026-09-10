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
	"regexp"
	"sort"
	"strings"

	"github.com/GoogleCloudPlatform/k8s-aibom/internal/scraper"
)

// Output sanitization (issue #57). Every string that reaches an emitted
// document passes through redactString at the BOM-build boundary. The
// scrapers are already conservative by construction (env values only from
// the model-identity allowlist, args only from model flags, name-presence
// evidence for API keys); this pass is the guarantee behind the remaining
// vectors the audit identified: URI-shaped identity fields emitted
// verbatim (KServe storageUri, model.k8saibom.dev/* annotations) and
// operator-extended allowlists capturing credential-bearing values.
//
// Redaction is recorded, never silent: any component or service whose
// fields were altered carries an `aibom.redaction.applied` property
// naming the redaction classes, so a reviewer knows the emitted value is
// not the raw cluster state. Determinism is preserved — redaction is a
// pure function of the input, so identical inputs still produce
// byte-identical documents.

const redactionPropertyKey = "aibom.redaction.applied"

// Redaction classes, stable strings recorded in the property.
const (
	redactURIUserinfo    = "uri-userinfo"
	redactQueryParameter = "query-credential"
	redactTokenShape     = "token"
)

var (
	// scheme://user:secret@host — the userinfo section of a URI.
	reURIUserinfo = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)([^/@\s]+)@`)

	// Known credential-bearing query parameters (pre-signed URLs, SAS
	// tokens, bare token params). Case-insensitive on the key; the value
	// runs to the next separator.
	reQueryCredential = regexp.MustCompile(`(?i)([?&](?:x-amz-signature|x-amz-credential|x-amz-security-token|x-goog-signature|x-goog-credential|sig|signature|token|access_token|id_token|api_key|apikey|private_token|sas)=)[^&\s]+`)

	// Well-known secret token shapes. Deliberately conservative: prefixes
	// that identify credential families, plus the JWT three-part shape and
	// Authorization-header bearer values.
	reTokenShapes = []*regexp.Regexp{
		regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{16,}`),
		regexp.MustCompile(`\bghp_[A-Za-z0-9]{20,}`),
		regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}`),
		regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{16,}`),
		regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{10,}`),
		regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
		regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`),
		regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`),
		regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/-]{15,}=*`),
	}
)

// redactString returns s with credential material replaced, plus the
// sorted, de-duplicated redaction classes that fired. An empty class list
// means s was returned unchanged.
func redactString(s string) (string, []string) {
	if s == "" {
		return s, nil
	}
	classes := map[string]bool{}

	if reURIUserinfo.MatchString(s) {
		s = reURIUserinfo.ReplaceAllString(s, "${1}[REDACTED:"+redactURIUserinfo+"]@")
		classes[redactURIUserinfo] = true
	}
	if reQueryCredential.MatchString(s) {
		s = reQueryCredential.ReplaceAllString(s, "${1}[REDACTED:"+redactQueryParameter+"]")
		classes[redactQueryParameter] = true
	}
	for _, re := range reTokenShapes {
		if re.MatchString(s) {
			s = re.ReplaceAllString(s, "[REDACTED:"+redactTokenShape+"]")
			classes[redactTokenShape] = true
		}
	}

	if len(classes) == 0 {
		return s, nil
	}
	out := make([]string, 0, len(classes))
	for c := range classes {
		out = append(out, c)
	}
	sort.Strings(out)
	return s, out
}

// redactComponent returns a shallow-redacted copy of c: Name, Version,
// PURL, Evidence.Locator, and all Properties values pass through
// redactString. Children are not descended here — buildComponents
// recurses and each child is redacted at its own level. If anything was
// redacted, the returned component's Properties carry
// aibom.redaction.applied with the class list.
func redactComponent(c scraper.Component) scraper.Component {
	classes := map[string]bool{}
	collect := func(s string) string {
		out, hit := redactString(s)
		for _, h := range hit {
			classes[h] = true
		}
		return out
	}

	c.Name = collect(c.Name)
	c.Version = collect(c.Version)
	c.PURL = collect(c.PURL)
	c.Evidence.Locator = collect(c.Evidence.Locator)

	if len(c.Properties) > 0 {
		props := make(map[string]string, len(c.Properties))
		for k, v := range c.Properties {
			props[k] = collect(v)
		}
		c.Properties = props
	}

	if len(classes) > 0 {
		out := make([]string, 0, len(classes))
		for cl := range classes {
			out = append(out, cl)
		}
		sort.Strings(out)
		if c.Properties == nil {
			c.Properties = map[string]string{}
		}
		c.Properties[redactionPropertyKey] = strings.Join(out, ",")
	}
	return c
}

// redactService returns a redacted copy of s (Name and Endpoints), with
// the redaction classes that fired, for buildServices to record.
func redactService(s scraper.Service) (scraper.Service, []string) {
	classes := map[string]bool{}
	collect := func(v string) string {
		out, hit := redactString(v)
		for _, h := range hit {
			classes[h] = true
		}
		return out
	}
	s.Name = collect(s.Name)
	if len(s.Endpoints) > 0 {
		eps := make([]string, len(s.Endpoints))
		for i, e := range s.Endpoints {
			eps[i] = collect(e)
		}
		s.Endpoints = eps
	}
	if len(classes) == 0 {
		return s, nil
	}
	out := make([]string, 0, len(classes))
	for c := range classes {
		out = append(out, c)
	}
	sort.Strings(out)
	return s, out
}
