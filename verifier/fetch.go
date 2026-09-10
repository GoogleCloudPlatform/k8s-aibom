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

package verifier

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Design 002 §6 fetch constraints. The signature reference is workload-
// author-controlled input; the fetcher's job is to make the worst case a
// bounded fetch and an error fact:
//
//   - HTTPS only (no http, no file, no oci in v1.5.0)
//   - response capped at MaxBundleBytes
//   - redirects allowed only within the original host
//   - no credentials attached, ever
//   - the caller's context (per-claim timeout) bounds everything
//
// Inline references ("base64:<data>") skip the network entirely and are
// capped at the same size after decoding.

const inlinePrefix = "base64:"

// fetchBundle resolves a signature reference to raw bundle bytes under
// the §6 constraints.
func fetchBundle(ctx context.Context, httpClient *http.Client, ref string) ([]byte, error) {
	if strings.HasPrefix(ref, inlinePrefix) {
		enc := ref[len(inlinePrefix):]
		if base64.StdEncoding.DecodedLen(len(enc)) > MaxBundleBytes {
			return nil, fmt.Errorf("inline bundle exceeds %d bytes", MaxBundleBytes)
		}
		raw, err := base64.StdEncoding.DecodeString(enc)
		if err != nil {
			return nil, fmt.Errorf("inline bundle: %w", err)
		}
		return raw, nil
	}

	u, err := url.Parse(ref)
	if err != nil {
		return nil, fmt.Errorf("signature reference: %w", err)
	}
	if u.Scheme != "https" {
		return nil, fmt.Errorf("signature reference scheme %q not allowed (https only)", u.Scheme)
	}
	if u.User != nil {
		return nil, fmt.Errorf("signature reference must not carry credentials")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("signature fetch: HTTP %d", resp.StatusCode)
	}

	// Cap the read regardless of Content-Length honesty. Read one byte
	// past the cap to distinguish exactly-at-cap from over-cap.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, MaxBundleBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > MaxBundleBytes {
		return nil, fmt.Errorf("signature bundle exceeds %d bytes", MaxBundleBytes)
	}
	return raw, nil
}

// newHTTPClient builds the fetcher's HTTP client: same-host redirects
// only, and no ambient proxy credentials beyond the environment defaults.
func newHTTPClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			if req.URL.Scheme != "https" {
				return fmt.Errorf("redirect to non-https %q refused", req.URL.Scheme)
			}
			if len(via) > 0 && req.URL.Host != via[0].URL.Host {
				return fmt.Errorf("cross-host redirect from %q to %q refused",
					via[0].URL.Host, req.URL.Host)
			}
			return nil
		},
	}
}
