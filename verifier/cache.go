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
	"sync"
	"time"
)

// resultCache implements Design 002 §5: results cached by
// (signatureRef digest, trust-root epoch) with TTL, plus in-flight
// deduplication so a burst of identical claims performs one verification.
// Steady-state cost approaches zero: a claim re-verifies only on
// reference change, TTL expiry, or trust-root rotation (which changes
// the epoch component of the key).
//
// Error outcomes are cached too, but with a short fixed TTL so a
// transient Rekor/network failure retries on the next resync rather
// than sticking for the full cache TTL.

const errorCacheTTL = 1 * time.Minute

type cacheEntry struct {
	result  Result
	expires time.Time
}

type inflightCall struct {
	done   chan struct{}
	result Result
}

type resultCache struct {
	ttl time.Duration
	now func() time.Time

	mu       sync.Mutex
	entries  map[string]cacheEntry
	inflight map[string]*inflightCall
}

func newResultCache(ttl time.Duration, now func() time.Time) *resultCache {
	if ttl <= 0 {
		ttl = DefaultCacheTTL
	}
	if now == nil {
		now = time.Now
	}
	return &resultCache{
		ttl:      ttl,
		now:      now,
		entries:  map[string]cacheEntry{},
		inflight: map[string]*inflightCall{},
	}
}

// do returns the cached result for key, or runs fn exactly once per key
// across concurrent callers and caches its result. Callers that arrive
// while fn runs wait for the leader's result.
func (c *resultCache) do(key string, fn func() Result) Result {
	c.mu.Lock()
	if e, ok := c.entries[key]; ok && c.now().Before(e.expires) {
		c.mu.Unlock()
		return e.result
	}
	if call, ok := c.inflight[key]; ok {
		c.mu.Unlock()
		<-call.done
		return call.result
	}
	call := &inflightCall{done: make(chan struct{})}
	c.inflight[key] = call
	c.mu.Unlock()

	res := fn()

	ttl := c.ttl
	if res.Outcome == OutcomeError {
		ttl = errorCacheTTL
	}
	c.mu.Lock()
	c.entries[key] = cacheEntry{result: res, expires: c.now().Add(ttl)}
	delete(c.inflight, key)
	c.mu.Unlock()

	call.result = res
	close(call.done)
	return res
}
