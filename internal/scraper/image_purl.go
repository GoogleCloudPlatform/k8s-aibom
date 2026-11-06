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

import "strings"

// ociPURL builds the package URL for a container image, following the
// purl specification's `oci` type:
//
//	pkg:oci/<name>[@sha256:<digest>]?repository_url=<repository>[&tag=<tag>]
//
// name is the last path segment of the repository, lowercased (the spec
// makes it case-insensitive and requires lowercase). The version is the
// resolved image digest when one is known, which is the only part of
// the purl that identifies the artifact; a tag-only image gets no
// version rather than a fabricated one (A1). repository_url is the
// repository as written in the workload, without tag or digest, so the
// purl states where the owner said the image comes from and does not
// expand registry defaults on their behalf. Qualifiers are emitted in
// key order with percent-encoded values, matching the canonical form in
// the purl test suite ("docker.io%2Flibrary%2Fdebian").
//
// ref is the image reference as written; digest is the resolved
// "sha256:<hex>" (or bare hex) when known, "" otherwise. Returns "" when
// no name can be read from ref.
func ociPURL(ref, digest string) string {
	repo, tag, _ := parseImageRef(ref)
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return ""
	}
	name := repo
	if i := strings.LastIndex(repo, "/"); i >= 0 {
		name = repo[i+1:]
	}
	name = strings.ToLower(name)
	if name == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("pkg:oci/")
	b.WriteString(purlEscape(name))
	if d := strings.TrimPrefix(digest, "sha256:"); d != "" {
		b.WriteString("@sha256:")
		b.WriteString(strings.ToLower(d))
	}
	b.WriteString("?repository_url=")
	b.WriteString(purlEscape(repo))
	if tag != "" {
		b.WriteString("&tag=")
		b.WriteString(purlEscape(tag))
	}
	return b.String()
}

// purlEscape percent-encodes a purl name, version or qualifier value:
// unreserved characters (RFC 3986) plus ':' pass through, everything
// else, including '/', '@' and '?', is encoded. This is the encoding the
// purl specification's canonical test outputs use.
func purlEscape(s string) string {
	const upperhex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9',
			c == '-', c == '.', c == '_', c == '~', c == ':':
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(upperhex[c>>4])
			b.WriteByte(upperhex[c&15])
		}
	}
	return b.String()
}
