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

package sink

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// mTLS authentication path tests.
//
// The webhook protocol commits to supporting mTLS (see
// docs/webhook-sink-protocol.md and WebhookSinkConfig.ClientCertFile/
// ClientKeyFile godoc). "Documented but untested" is the worst
// combination — these tests exercise the mTLS code path end-to-end
// against an httptest server configured to require client cert auth.
//
// Cert generation uses only standard library (crypto/rsa, crypto/x509,
// encoding/pem). The helper produces a minimal CA + server + client
// cert chain valid for one hour, which is more than enough for a
// test that runs in seconds.

// mtlsCerts is the set of cert/key file paths produced by
// generateMTLSCerts. All files live under the same t.TempDir().
type mtlsCerts struct {
	CAFile         string
	ServerCertFile string
	ServerKeyFile  string
	ClientCertFile string
	ClientKeyFile  string
}

// generateMTLSCerts produces a self-signed CA, a server cert
// (signed by the CA, valid for 127.0.0.1), and a client cert (signed
// by the CA, with the test CN "test-aibom-client"). Files are written
// PEM-encoded into a t.TempDir() and cleaned up automatically when
// the test completes.
func generateMTLSCerts(t *testing.T) mtlsCerts {
	t.Helper()
	dir := t.TempDir()
	now := time.Now()
	expires := now.Add(1 * time.Hour)

	// CA
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-aibom-ca"},
		NotBefore:             now,
		NotAfter:              expires,
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create CA cert: %v", err)
	}
	caParsed, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("parse CA cert: %v", err)
	}

	// Server cert (signed by CA, valid for 127.0.0.1)
	serverKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate server key: %v", err)
	}
	serverTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    now,
		NotAfter:     expires,
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, serverTmpl, caParsed, &serverKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create server cert: %v", err)
	}

	// Client cert (signed by CA, with a stable CN we can assert on)
	clientKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate client key: %v", err)
	}
	clientTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(3),
		Subject:      pkix.Name{CommonName: "test-aibom-client"},
		NotBefore:    now,
		NotAfter:     expires,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	clientDER, err := x509.CreateCertificate(rand.Reader, clientTmpl, caParsed, &clientKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create client cert: %v", err)
	}

	out := mtlsCerts{
		CAFile:         filepath.Join(dir, "ca.crt"),
		ServerCertFile: filepath.Join(dir, "server.crt"),
		ServerKeyFile:  filepath.Join(dir, "server.key"),
		ClientCertFile: filepath.Join(dir, "client.crt"),
		ClientKeyFile:  filepath.Join(dir, "client.key"),
	}
	writePEM(t, out.CAFile, "CERTIFICATE", caDER)
	writePEM(t, out.ServerCertFile, "CERTIFICATE", serverDER)
	writePEM(t, out.ServerKeyFile, "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(serverKey))
	writePEM(t, out.ClientCertFile, "CERTIFICATE", clientDER)
	writePEM(t, out.ClientKeyFile, "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(clientKey))
	return out
}

func writePEM(t *testing.T, path, blockType string, der []byte) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()
	if err := pem.Encode(f, &pem.Block{Type: blockType, Bytes: der}); err != nil {
		t.Fatalf("pem encode %s: %v", path, err)
	}
}

// newMTLSTestServer returns an httptest.Server configured to require
// and verify client certs signed by the given CA. The handler runs
// for every request and may inspect r.TLS.PeerCertificates.
func newMTLSTestServer(t *testing.T, certs mtlsCerts, handler http.Handler) *httptest.Server {
	t.Helper()
	caBytes, err := os.ReadFile(certs.CAFile)
	if err != nil {
		t.Fatalf("read CA: %v", err)
	}
	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caBytes) {
		t.Fatal("append CA cert: no certs parsed")
	}
	serverCert, err := tls.LoadX509KeyPair(certs.ServerCertFile, certs.ServerKeyFile)
	if err != nil {
		t.Fatalf("load server cert/key: %v", err)
	}
	srv := httptest.NewUnstartedServer(handler)
	srv.TLS = &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientCAs:    caPool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS12,
	}
	srv.StartTLS()
	return srv
}

func TestWebhookSink_MTLS_HappyPath(t *testing.T) {
	certs := generateMTLSCerts(t)

	var sawClientCN string
	srv := newMTLSTestServer(t, certs, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
			sawClientCN = r.TLS.PeerCertificates[0].Subject.CommonName
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	s, err := NewWebhookSink(WebhookSinkConfig{
		Endpoint:       srv.URL,
		ClientCertFile: certs.ClientCertFile,
		ClientKeyFile:  certs.ClientKeyFile,
		CAFile:         certs.CAFile,
	})
	if err != nil {
		t.Fatalf("NewWebhookSink: %v", err)
	}
	if _, err := s.Emit(context.Background(), newTestWebhookDoc(), newTestWebhookMeta()); err != nil {
		t.Fatalf("Emit over mTLS: %v", err)
	}
	if sawClientCN != "test-aibom-client" {
		t.Errorf("server saw client CN %q, want %q (the client cert was not presented)",
			sawClientCN, "test-aibom-client")
	}
}

func TestWebhookSink_MTLS_RejectsCallerWithoutClientCert(t *testing.T) {
	// Server requires a client cert; sink configured WITHOUT one.
	// The TLS handshake must fail and Emit must return an error.
	certs := generateMTLSCerts(t)
	srv := newMTLSTestServer(t, certs, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// Configure CAFile (so we trust the server) but NO client cert.
	s, err := NewWebhookSink(WebhookSinkConfig{
		Endpoint: srv.URL,
		CAFile:   certs.CAFile,
	})
	if err != nil {
		t.Fatalf("NewWebhookSink: %v", err)
	}

	// Tighten the retry backoff so the test completes quickly.
	orig := WebhookRetryBackoffs
	WebhookRetryBackoffs = []time.Duration{1 * time.Millisecond, 1 * time.Millisecond, 1 * time.Millisecond}
	defer func() { WebhookRetryBackoffs = orig }()

	_, err = s.Emit(context.Background(), newTestWebhookDoc(), newTestWebhookMeta())
	if err == nil {
		t.Fatal("Emit succeeded; expected TLS error for missing client cert")
	}
}

func TestWebhookSink_MTLS_RejectsServerWithUntrustedCA(t *testing.T) {
	// Sink is configured to trust the WRONG CA. The handshake against
	// the test server (which presents a cert signed by the right CA)
	// must fail.
	correctCerts := generateMTLSCerts(t)
	wrongCerts := generateMTLSCerts(t) // independent CA

	srv := newMTLSTestServer(t, correctCerts, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s, err := NewWebhookSink(WebhookSinkConfig{
		Endpoint:       srv.URL,
		ClientCertFile: correctCerts.ClientCertFile,
		ClientKeyFile:  correctCerts.ClientKeyFile,
		CAFile:         wrongCerts.CAFile, // wrong CA
	})
	if err != nil {
		t.Fatalf("NewWebhookSink: %v", err)
	}

	orig := WebhookRetryBackoffs
	WebhookRetryBackoffs = []time.Duration{1 * time.Millisecond, 1 * time.Millisecond, 1 * time.Millisecond}
	defer func() { WebhookRetryBackoffs = orig }()

	_, err = s.Emit(context.Background(), newTestWebhookDoc(), newTestWebhookMeta())
	if err == nil {
		t.Fatal("Emit succeeded; expected TLS error for untrusted server CA")
	}
}

func TestWebhookSink_MTLS_InvalidCertFiles_FailsConstruction(t *testing.T) {
	dir := t.TempDir()
	// Write garbage into "cert" and "key" files.
	garbage := []byte("this is not a PEM cert")
	certFile := filepath.Join(dir, "bad.crt")
	keyFile := filepath.Join(dir, "bad.key")
	if err := os.WriteFile(certFile, garbage, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, garbage, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := NewWebhookSink(WebhookSinkConfig{
		Endpoint:       "https://example.com/sink",
		ClientCertFile: certFile,
		ClientKeyFile:  keyFile,
	})
	if err == nil {
		t.Fatal("NewWebhookSink accepted garbage cert/key files")
	}
}
