// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package sink

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/GoogleCloudPlatform/k8s-aibom/internal/bom"
)

// Webhook headers / constants
const (
	WebhookContentType         = "application/json"
	WebhookHeaderDocumentHash  = "X-Aibom-Document-Hash"
	WebhookHeaderWorkloadKind  = "X-Aibom-Workload-Kind"
	WebhookHeaderWorkloadNS    = "X-Aibom-Workload-Namespace"
	WebhookHeaderWorkloadName  = "X-Aibom-Workload-Name"
	WebhookHeaderCategory      = "X-Aibom-Workload-Category"
	WebhookHeaderControllerVer = "X-Aibom-Controller-Version"
)

var (
	WebhookRetryBackoffs = []time.Duration{
		250 * time.Millisecond,
		1 * time.Second,
		3 * time.Second,
	}
)

var ansiRe = regexp.MustCompile("\x1b\\[[0-9;]*[a-zA-Z]")

const maxErrorBodyBytes = 256

type WebhookSinkConfig struct {
	Endpoint           string
	InsecureSkipVerify bool
	CAFile             string
	CAPEM              []byte
	ClientCertFile     string
	ClientKeyFile      string
	ClientCertPEM      []byte
	ClientKeyPEM       []byte
	BearerTokenFile    string
	BearerToken        []byte
	Timeout            time.Duration
	ControllerVersion  string
}

func (c *WebhookSinkConfig) Validate() error {
	if c.Endpoint == "" {
		return errors.New("webhook sink: endpoint URL is required")
	}
	u, err := url.Parse(c.Endpoint)
	if err != nil {
		return fmt.Errorf("webhook sink: invalid endpoint URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("webhook sink: endpoint scheme must be http or https")
	}

	if (len(c.ClientCertPEM) > 0 && len(c.ClientKeyPEM) == 0) || (len(c.ClientCertPEM) == 0 && len(c.ClientKeyPEM) > 0) {
		return errors.New("webhook sink: both ClientCertPEM and ClientKeyPEM must be provided")
	}
	if (c.ClientCertFile != "" && c.ClientKeyFile == "") || (c.ClientCertFile == "" && c.ClientKeyFile != "") {
		return errors.New("webhook sink: both ClientCertFile and ClientKeyFile must be provided")
	}

	disableSSRFChecks := os.Getenv("AIBOM_DISABLE_SSRF_CHECKS") == "true"
	if !disableSSRFChecks {
		h := strings.ToLower(u.Hostname())
		if h == "localhost" || strings.HasPrefix(h, "127.") || h == "[::1]" || h == "0.0.0.0" || strings.HasPrefix(h, "169.254.") {
			return errors.New("webhook sink: endpoint URL points to loopback/private interface")
		}
	}
	return nil
}

// isPrivateIP checks if an IP belongs to a loopback or link-local/private block.
func isPrivateIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsMulticast() || ip.IsInterfaceLocalMulticast() || ip.IsLinkLocalMulticast()
}

// safeDialContext creates a dialer that uses Control to validate the resolved IP before connection, preventing DNS rebinding.
func safeDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	disableSSRFChecks := os.Getenv("AIBOM_DISABLE_SSRF_CHECKS") == "true"

	dialer := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
		Control: func(network, address string, c syscall.RawConn) error {
			if disableSSRFChecks {
				return nil
			}
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				host = address
			}
			// Remove zone index if present
			if idx := strings.IndexByte(host, '%'); idx != -1 {
				host = host[:idx]
			}
			ip := net.ParseIP(host)
			if ip == nil {
				return fmt.Errorf("webhook sink: SSRF protection blocked connection to unparsable IP: %s", host)
			}
			if isPrivateIP(ip) {
				return fmt.Errorf("webhook sink: SSRF protection blocked connection to private/loopback/multicast IP: %s", ip.String())
			}
			return nil
		},
	}
	return dialer.DialContext(ctx, network, addr)
}

type WebhookSink struct {
	cfg    WebhookSinkConfig
	client *http.Client
	token  string
}

func NewWebhookSink(cfg WebhookSinkConfig) (*WebhookSink, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	tlsConfig := &tls.Config{
		InsecureSkipVerify: cfg.InsecureSkipVerify,
	}

	if len(cfg.CAPEM) > 0 {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(cfg.CAPEM) {
			return nil, errors.New("webhook sink: failed to parse CA certs from CAPEM")
		}
		tlsConfig.RootCAs = pool
	} else if cfg.CAFile != "" {
		caCert, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("webhook sink: read CAFile %s: %w", cfg.CAFile, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caCert) {
			return nil, fmt.Errorf("webhook sink: failed to parse CA certs from %s", cfg.CAFile)
		}
		tlsConfig.RootCAs = pool
	}

	switch {
	case len(cfg.ClientCertPEM) > 0 && len(cfg.ClientKeyPEM) > 0:
		cert, err := tls.X509KeyPair(cfg.ClientCertPEM, cfg.ClientKeyPEM)
		if err != nil {
			return nil, fmt.Errorf("webhook sink: parse client cert/key from PEM: %w", err)
		}
		tlsConfig.Certificates = []tls.Certificate{cert}
	case cfg.ClientCertFile != "" && cfg.ClientKeyFile != "":
		cert, err := tls.LoadX509KeyPair(cfg.ClientCertFile, cfg.ClientKeyFile)
		if err != nil {
			return nil, fmt.Errorf("webhook sink: load client cert/key: %w", err)
		}
		tlsConfig.Certificates = []tls.Certificate{cert}
	}

	var token string
	switch {
	case len(cfg.BearerToken) > 0:
		token = strings.TrimSpace(string(cfg.BearerToken))
		if token == "" {
			return nil, errors.New("webhook sink: BearerToken is whitespace-only")
		}
	case cfg.BearerTokenFile != "":
		b, err := os.ReadFile(cfg.BearerTokenFile)
		if err != nil {
			return nil, fmt.Errorf("webhook sink: read BearerTokenFile %s: %w", cfg.BearerTokenFile, err)
		}
		token = strings.TrimSpace(string(b))
		if token == "" {
			return nil, fmt.Errorf("webhook sink: BearerTokenFile %s is empty", cfg.BearerTokenFile)
		}
	}

	transport := &http.Transport{
		TLSClientConfig: tlsConfig,
		DialContext:     safeDialContext,
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   cfg.Timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	return &WebhookSink{cfg: cfg, client: client, token: token}, nil
}

func (s *WebhookSink) Name() string { return "webhook" }

// Close gracefully closes any idle connections on the client transport.
func (s *WebhookSink) Close() error {
	if s.client != nil {
		if transport, ok := s.client.Transport.(*http.Transport); ok {
			transport.CloseIdleConnections()
		}
	}
	return nil
}

func (s *WebhookSink) HealthCheck(_ context.Context) error { return nil }

func (s *WebhookSink) WriteOnly() bool { return true }

func (s *WebhookSink) Emit(ctx context.Context, doc *bom.Document, meta SinkMeta) (string, error) {
	if doc == nil {
		return "", errors.New("webhook sink: doc is nil")
	}
	if err := s.deliver(ctx, doc, meta); err != nil {
		return "", err
	}
	return s.cfg.Endpoint, nil
}

func (s *WebhookSink) deliver(ctx context.Context, doc *bom.Document, meta SinkMeta) error {
	var lastErr error
	totalAttempts := len(WebhookRetryBackoffs) + 1
	for attempt := 0; attempt < totalAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(WebhookRetryBackoffs[attempt-1]):
			}
		}
		req, err := s.buildRequest(ctx, doc, meta)
		if err != nil {
			return err
		}
		resp, err := s.client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("webhook sink: POST %s: %w", s.cfg.Endpoint, err)
			continue
		}

		// Read a slightly larger chunk to ensure we have enough runway to strip ANSI codes properly
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		truncated := len(bodyBytes) == 1024

		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil
		}

		if resp.StatusCode >= 300 && resp.StatusCode < 500 {
			return formatHTTPError(s.cfg.Endpoint, resp.StatusCode, bodyBytes, truncated)
		}

		lastErr = formatHTTPError(s.cfg.Endpoint, resp.StatusCode, bodyBytes, truncated)
	}
	return lastErr
}

func (s *WebhookSink) buildRequest(ctx context.Context, doc *bom.Document, meta SinkMeta) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.Endpoint, bytes.NewReader(doc.JSON))
	if err != nil {
		return nil, fmt.Errorf("webhook sink: build request: %w", err)
	}
	req.Header.Set("Content-Type", WebhookContentType)
	req.Header.Set(WebhookHeaderDocumentHash, "sha256:"+doc.SHA256)
	req.Header.Set(WebhookHeaderWorkloadKind, meta.WorkloadKind)
	req.Header.Set(WebhookHeaderWorkloadNS, meta.WorkloadNamespace)
	req.Header.Set(WebhookHeaderWorkloadName, meta.WorkloadName)
	if meta.WorkloadCategory != "" {
		req.Header.Set(WebhookHeaderCategory, strings.ReplaceAll(strings.ReplaceAll(meta.WorkloadCategory, "\n", ""), "\r", ""))
	}
	if s.cfg.ControllerVersion != "" {
		req.Header.Set(WebhookHeaderControllerVer, s.cfg.ControllerVersion)
	}
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}
	return req, nil
}

func formatHTTPError(endpoint string, status int, body []byte, networkTruncated bool) error {
	// Use the full 1024-byte runway to ensure ANSI sequences are not sliced in half before stripping
	bodyStr := strings.ToValidUTF8(string(body), "")
	bodyStr = strings.ReplaceAll(bodyStr, "\n", " ")
	bodyStr = strings.ReplaceAll(bodyStr, "\r", " ")

	// Strip ANSI
	bodyStr = ansiRe.ReplaceAllString(bodyStr, "")
	bodyStr = strings.ReplaceAll(bodyStr, "\x1b", "")

	// Truncate to 256 runes safely
	runes := []rune(bodyStr)
	runeTruncated := false
	if len(runes) > maxErrorBodyBytes {
		bodyStr = string(runes[:maxErrorBodyBytes])
		runeTruncated = true
	}

	truncated := networkTruncated || len(body) > maxErrorBodyBytes || runeTruncated
	if truncated {
		bodyStr += "...(truncated)"
	}

	if bodyStr == "" {
		return fmt.Errorf("webhook sink: POST %s returned HTTP %d (empty body)", endpoint, status)
	}
	return fmt.Errorf("webhook sink: POST %s returned HTTP %d: %s", endpoint, status, bodyStr)
}
