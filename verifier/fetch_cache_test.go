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
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestFetchBundleConstraints(t *testing.T) {
	ctx := context.Background()

	t.Run("http scheme refused", func(t *testing.T) {
		_, err := fetchBundle(ctx, http.DefaultClient, "http://example.com/bundle")
		if err == nil || !strings.Contains(err.Error(), "https only") {
			t.Fatalf("want https-only error, got %v", err)
		}
	})
	t.Run("credentials in reference refused", func(t *testing.T) {
		_, err := fetchBundle(ctx, http.DefaultClient, "https://user:pw@example.com/bundle")
		if err == nil || !strings.Contains(err.Error(), "credentials") {
			t.Fatalf("want credential rejection, got %v", err)
		}
	})
	t.Run("inline base64 roundtrip", func(t *testing.T) {
		want := []byte(`{"bundle":"ok"}`)
		got, err := fetchBundle(ctx, http.DefaultClient, inlinePrefix+base64.StdEncoding.EncodeToString(want))
		if err != nil || string(got) != string(want) {
			t.Fatalf("got %q err %v", got, err)
		}
	})
	t.Run("inline base64 invalid", func(t *testing.T) {
		if _, err := fetchBundle(ctx, http.DefaultClient, inlinePrefix+"not-base64!!!"); err == nil {
			t.Fatalf("want decode error")
		}
	})
	t.Run("oversize body capped", func(t *testing.T) {
		ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			big := make([]byte, MaxBundleBytes+512)
			_, _ = w.Write(big)
		}))
		defer ts.Close()
		_, err := fetchBundle(ctx, ts.Client(), ts.URL)
		if err == nil || !strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("want size-cap error, got %v", err)
		}
	})
	t.Run("cross-host redirect refused", func(t *testing.T) {
		other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("leaked"))
		}))
		defer other.Close()
		ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, other.URL, http.StatusFound)
		}))
		defer ts.Close()
		client := ts.Client()
		client.CheckRedirect = newHTTPClient().CheckRedirect
		_, err := fetchBundle(ctx, client, ts.URL)
		if err == nil || !strings.Contains(err.Error(), "cross-host redirect") {
			t.Fatalf("want cross-host refusal, got %v", err)
		}
	})
}

func TestResultCacheSingleflightAndTTL(t *testing.T) {
	var clockMu sync.Mutex
	now := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { clockMu.Lock(); defer clockMu.Unlock(); return now }
	advance := func(d time.Duration) { clockMu.Lock(); now = now.Add(d); clockMu.Unlock() }

	c := newResultCache(1*time.Hour, clock)
	var calls atomic.Int64

	// Singleflight: N concurrent callers, one execution.
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			c.do("k", func() Result {
				calls.Add(1)
				time.Sleep(20 * time.Millisecond)
				return Result{Outcome: OutcomeVerified, Verified: true}
			})
		}()
	}
	close(start)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("singleflight: %d executions, want 1", calls.Load())
	}

	// Cached within TTL.
	c.do("k", func() Result { calls.Add(1); return Result{} })
	if calls.Load() != 1 {
		t.Fatalf("cache miss within TTL")
	}

	// Expired after TTL.
	advance(2 * time.Hour)
	c.do("k", func() Result { calls.Add(1); return Result{Outcome: OutcomeVerified} })
	if calls.Load() != 2 {
		t.Fatalf("expected re-execution after TTL")
	}

	// Error outcomes use the short error TTL.
	c.do("err", func() Result { calls.Add(1); return Result{Outcome: OutcomeError, Reason: "network"} })
	advance(2 * time.Minute)
	c.do("err", func() Result { calls.Add(1); return Result{Outcome: OutcomeVerified} })
	if calls.Load() != 4 {
		t.Fatalf("error result outlived errorCacheTTL: calls=%d", calls.Load())
	}
}
