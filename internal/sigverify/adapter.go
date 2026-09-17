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

// Package sigverify adapts the verifier module's RekorVerifier to the
// scraper.SignatureVerifier interface, with hot-reload from the config
// Store. The dependency direction is deliberate: the verifier module
// never imports the root module (Design 002 §1); this package owns the
// translation in the root.
package sigverify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"

	"github.com/GoogleCloudPlatform/k8s-aibom/internal/config"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/scraper"
	"github.com/GoogleCloudPlatform/k8s-aibom/verifier"
)

// Adapter implements scraper.SignatureVerifier. It consults the config
// Store's current snapshot on every call: verification disabled (nil
// Verification) behaves exactly like NoopVerifier, and a configuration
// change swaps in a freshly-built RekorVerifier on the next call — the
// same snapshot-swap hot-reload contract the reconcile path uses.
type Adapter struct {
	store *config.Store

	mu     sync.Mutex
	cfgKey string
	rv     *verifier.RekorVerifier
}

// New constructs an Adapter over the given config store.
func New(store *config.Store) *Adapter {
	return &Adapter{store: store}
}

// Name implements scraper.SignatureVerifier.
func (a *Adapter) Name() string { return "rekor" }

// Verify implements scraper.SignatureVerifier. It never returns an
// error: verification failures are outcomes, not failures (Design 002
// §4), and the floor is SignatureClaimed.
func (a *Adapter) Verify(ctx context.Context, claim scraper.SignatureClaim) (scraper.SignatureResult, error) {
	if claim.SignatureRef == "" {
		return scraper.SignatureResult{Status: scraper.SignatureUnsigned}, nil
	}
	vc := a.store.Load().Verification
	if vc == nil {
		// Verification disabled: claimed, no outcome facts —
		// pre-v1.5.0 behavior exactly.
		return scraper.SignatureResult{Status: scraper.SignatureClaimed}, nil
	}

	rv, err := a.current(vc)
	if err != nil {
		// Config was load-validated, so this is unexpected; degrade to
		// claimed with the reason recorded rather than erroring.
		return scraper.SignatureResult{
			Status:  scraper.SignatureClaimed,
			Outcome: string(verifier.OutcomeError),
			Reason:  "verifier construction: " + err.Error(),
		}, nil
	}

	res := rv.Verify(ctx, verifier.Claim{
		ModelIdentity:  claim.ModelIdentity,
		SignatureRef:   claim.SignatureRef,
		DeclaredDigest: claim.DeclaredDigest,
	})

	out := scraper.SignatureResult{
		Status:              scraper.SignatureClaimed,
		Identity:            res.Identity,
		RekorEntry:          res.RekorEntry,
		Outcome:             string(res.Outcome),
		Reason:              res.Reason,
		SubjectNameMismatch: res.SubjectNameMismatch,
		Timestamp:           res.VerifiedAt,
	}
	if res.Verified {
		out.Status = scraper.SignatureVerified
	}
	return out, nil
}

// current returns a RekorVerifier built for the given config, rebuilding
// only when the config changed since the last call. The old verifier's
// cache is discarded with it — a trust-policy change must not reuse
// results computed under the previous policy.
func (a *Adapter) current(vc *verifier.Config) (*verifier.RekorVerifier, error) {
	key := configKey(vc)
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.rv != nil && a.cfgKey == key {
		return a.rv, nil
	}
	rv, err := verifier.NewRekorVerifier(*vc)
	if err != nil {
		return nil, err
	}
	a.rv = rv
	a.cfgKey = key
	return rv, nil
}

func configKey(vc *verifier.Config) string {
	raw, _ := json.Marshal(vc)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:8])
}

// Compile-time interface assertion.
var _ scraper.SignatureVerifier = (*Adapter)(nil)
