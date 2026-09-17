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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/tuf"
	"github.com/sigstore/sigstore-go/pkg/verify"
)

// RekorVerifier is the Design 002 implementation: it resolves a signature
// reference to a Sigstore bundle (fetch.go), verifies chain and Rekor
// inclusion against the configured trust root, and maps the cryptographic
// findings through the amended §3/§4 binding rules (outcome.go).
//
// Identity policy is enforced by outcome.go, not by sigstore-go's policy
// layer: the design records the signer identity as fact on every
// chain-valid outcome, including when constraints are absent or
// unsatisfied — so verification runs identity-open and the constraint
// decision happens in decideOutcome.
type RekorVerifier struct {
	cfg  Config
	http *http.Client

	cache *resultCache

	// Trust material is resolved lazily on first use so constructing the
	// verifier never blocks on the network (TrustPublic and TrustTUFMirror
	// fetch trusted_root.json through TUF; the TUF client itself carries
	// an embedded initial root of trust). Until resolution succeeds,
	// verifications return OutcomeError/"tuf-refresh" and retry on the
	// next resync — the same degradation shape as an unreachable Rekor.
	trustMu    sync.Mutex
	trusted    root.TrustedMaterial
	trustEpoch string
}

// NewRekorVerifier validates static configuration; it performs no I/O.
func NewRekorVerifier(cfg Config) (*RekorVerifier, error) {
	switch cfg.TrustRootMode {
	case TrustPublic, "":
		cfg.TrustRootMode = TrustPublic
	case TrustTUFMirror:
		if cfg.TUFMirrorURL == "" {
			return nil, fmt.Errorf("verifier: trustRootMode tufMirror requires tufMirrorURL")
		}
	case TrustStaticBundle:
		if cfg.StaticBundlePath == "" {
			return nil, fmt.Errorf("verifier: trustRootMode staticBundle requires staticBundlePath")
		}
	default:
		return nil, fmt.Errorf("verifier: unknown trustRootMode %q", cfg.TrustRootMode)
	}
	if cfg.PerClaimTimeout <= 0 {
		cfg.PerClaimTimeout = DefaultPerClaimTimeout
	}
	if cfg.CacheTTL <= 0 {
		cfg.CacheTTL = DefaultCacheTTL
	}
	return &RekorVerifier{
		cfg:   cfg,
		http:  newHTTPClient(),
		cache: newResultCache(cfg.CacheTTL, time.Now),
	}, nil
}

// trustedMaterial resolves (and memoizes) the trust root per §2.
func (v *RekorVerifier) trustedMaterial() (root.TrustedMaterial, string, error) {
	v.trustMu.Lock()
	defer v.trustMu.Unlock()
	if v.trusted != nil {
		return v.trusted, v.trustEpoch, nil
	}

	var trustedRootJSON []byte
	var err error
	switch v.cfg.TrustRootMode {
	case TrustStaticBundle:
		trustedRootJSON, err = os.ReadFile(v.cfg.StaticBundlePath)
		if err != nil {
			return nil, "", fmt.Errorf("static trust bundle: %w", err)
		}
	default: // TrustPublic, TrustTUFMirror — via TUF (embedded initial root)
		opts := tuf.DefaultOptions()
		if v.cfg.TrustRootMode == TrustTUFMirror {
			opts.RepositoryBaseURL = v.cfg.TUFMirrorURL
		}
		client, err := tuf.New(opts)
		if err != nil {
			return nil, "", fmt.Errorf("tuf client: %w", err)
		}
		trustedRootJSON, err = client.GetTarget("trusted_root.json")
		if err != nil {
			return nil, "", fmt.Errorf("tuf trusted_root: %w", err)
		}
	}

	tr, err := root.NewTrustedRootFromJSON(trustedRootJSON)
	if err != nil {
		return nil, "", fmt.Errorf("trusted root parse: %w", err)
	}
	sum := sha256.Sum256(trustedRootJSON)
	v.trusted = tr
	v.trustEpoch = hex.EncodeToString(sum[:8])
	return v.trusted, v.trustEpoch, nil
}

// Verify implements the Verifier interface. Results are cached by
// (signature reference digest, trust-root epoch) per §5.
func (v *RekorVerifier) Verify(ctx context.Context, c Claim) Result {
	if c.SignatureRef == "" {
		// Callers should not reach here (unsigned claims never enter the
		// verifier), but never panic on hostile or empty input.
		return Result{Outcome: OutcomeFailed, Reason: "empty signature reference"}
	}

	trusted, epoch, err := v.trustedMaterial()
	if err != nil {
		// Trust root unavailable: infrastructure error, uncached beyond
		// the short error TTL, retried on the next resync.
		return v.cache.do("trust-unresolved:"+hashKey(c.SignatureRef), func() Result {
			return decideOutcome(v.cfg, c, findings{errClass: "tuf-refresh: " + err.Error()}, time.Now().UTC())
		})
	}

	key := hashKey(c.SignatureRef) + ":" + epoch
	return v.cache.do(key, func() Result {
		vctx, cancel := context.WithTimeout(ctx, v.cfg.PerClaimTimeout)
		defer cancel()
		f := v.gatherFindings(vctx, trusted, c)
		return decideOutcome(v.cfg, c, f, time.Now().UTC())
	})
}

// gatherFindings performs fetch + cryptographic verification and returns
// facts only — no binding decisions (those belong to decideOutcome).
func (v *RekorVerifier) gatherFindings(ctx context.Context, trusted root.TrustedMaterial, c Claim) findings {
	raw, err := fetchBundle(ctx, v.http, c.SignatureRef)
	if err != nil {
		if ctx.Err() != nil {
			return findings{errClass: "timeout: " + ctx.Err().Error()}
		}
		return findings{errClass: "fetch: " + err.Error()}
	}

	var b bundle.Bundle
	if err := b.UnmarshalJSON(raw); err != nil {
		return findings{chainValid: false, chainErr: "bundle parse: " + err.Error()}
	}

	sev, err := verify.NewVerifier(trusted,
		verify.WithTransparencyLog(1),
		verify.WithObserverTimestamps(1),
	)
	if err != nil {
		return findings{errClass: "verifier init: " + err.Error()}
	}

	// Identity-open, artifact-open policy: the DSSE statement *is* the
	// verified payload (model-signing manifests), and identity
	// constraints are applied in decideOutcome so the identity is
	// recorded as fact on every chain-valid outcome.
	policy := verify.NewPolicy(verify.WithoutArtifactUnsafe(), verify.WithoutIdentitiesUnsafe())
	res, err := sev.Verify(&b, policy)
	if err != nil {
		return findings{chainValid: false, chainErr: err.Error()}
	}

	f := findings{
		chainValid:  true,
		rekorProven: true, // WithTransparencyLog(1) makes inclusion a verify-time requirement
	}
	if res.VerifiedIdentity != nil {
		f.identity = res.VerifiedIdentity.SubjectAlternativeName.SubjectAlternativeName
		f.issuer = res.VerifiedIdentity.Issuer.Issuer
	} else if res.Signature != nil && res.Signature.Certificate != nil {
		f.identity = res.Signature.Certificate.SubjectAlternativeName
		f.issuer = res.Signature.Certificate.Issuer
	}
	if res.Statement != nil && len(res.Statement.Subject) > 0 {
		subj := res.Statement.Subject[0]
		f.subjectName = subj.Name
		if d, ok := subj.Digest["sha256"]; ok && d != "" {
			f.rootDigest = "sha256:" + d
		}
	}
	// Rekor entry identifier from the bundle's log entries (best effort;
	// inclusion itself was already enforced by the verifier).
	if entries := b.Bundle.GetVerificationMaterial().GetTlogEntries(); len(entries) > 0 {
		f.rekorEntry = strconv.FormatInt(entries[0].GetLogIndex(), 10)
	}
	return f
}

func hashKey(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}
