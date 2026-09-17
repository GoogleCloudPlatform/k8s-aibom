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

package sigverify

import (
	"context"
	"encoding/base64"
	"os"
	"testing"

	"github.com/GoogleCloudPlatform/k8s-aibom/internal/config"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/scraper"
	"github.com/GoogleCloudPlatform/k8s-aibom/verifier"
)

func storeWith(verification *verifier.Config) *config.Store {
	snap := config.DefaultSnapshot()
	snap.Verification = verification
	return config.NewStore(snap)
}

func TestAdapterDisabledMatchesNoop(t *testing.T) {
	a := New(storeWith(nil))
	ctx := context.Background()

	res, err := a.Verify(ctx, scraper.SignatureClaim{})
	if err != nil || res.Status != scraper.SignatureUnsigned {
		t.Fatalf("empty ref: %+v err=%v", res, err)
	}

	res, err = a.Verify(ctx, scraper.SignatureClaim{SignatureRef: "base64:xx"})
	if err != nil || res.Status != scraper.SignatureClaimed || res.Outcome != "" {
		t.Fatalf("disabled must be claimed with no outcome facts: %+v err=%v", res, err)
	}
}

func TestAdapterEnabledStaticRootVerifiesRealBundle(t *testing.T) {
	// Reuses the verifier module's committed fixtures: real bundle,
	// real trust root, fully offline.
	st := storeWith(&verifier.Config{
		TrustRootMode:    verifier.TrustStaticBundle,
		StaticBundlePath: "../../verifier/testdata/trusted-root-public-good.json",
	})
	a := New(st)

	raw := readFixtureBundleRef(t)
	res, err := a.Verify(context.Background(), scraper.SignatureClaim{
		ModelIdentity: "pkg:npm/sigstore@1.3.0",
		SignatureRef:  raw,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != scraper.SignatureVerified || res.Outcome != string(verifier.OutcomeVerified) {
		t.Fatalf("want verified, got %+v", res)
	}
	if res.Identity == "" || res.RekorEntry == "" || res.Timestamp.IsZero() {
		t.Fatalf("facts not mapped: %+v", res)
	}
}

func TestAdapterHotReloadRebuildsOnConfigChange(t *testing.T) {
	snapA := config.DefaultSnapshot()
	snapA.Verification = &verifier.Config{
		TrustRootMode:    verifier.TrustStaticBundle,
		StaticBundlePath: "../../verifier/testdata/trusted-root-public-good.json",
	}
	st := config.NewStore(snapA)
	a := New(st)

	ref := readFixtureBundleRef(t)
	first, _ := a.Verify(context.Background(), scraper.SignatureClaim{
		ModelIdentity: "pkg:npm/sigstore@1.3.0", SignatureRef: ref,
	})
	if first.Status != scraper.SignatureVerified {
		t.Fatalf("precondition: %+v", first)
	}

	// Swap in a constraining config; the adapter must rebuild (a stale
	// verifier would serve the cached verified result).
	snapB := config.DefaultSnapshot()
	snapB.Verification = &verifier.Config{
		TrustRootMode:    verifier.TrustStaticBundle,
		StaticBundlePath: "../../verifier/testdata/trusted-root-public-good.json",
		Identities:       []verifier.IdentityConstraint{{SubjectPattern: `^nobody@nowhere\.example$`}},
	}
	st.Store(snapB)

	second, _ := a.Verify(context.Background(), scraper.SignatureClaim{
		ModelIdentity: "pkg:npm/sigstore@1.3.0", SignatureRef: ref,
	})
	if second.Status != scraper.SignatureClaimed || second.Outcome != string(verifier.OutcomeIdentityMismatch) {
		t.Fatalf("hot reload did not take effect: %+v", second)
	}
}

func readFixtureBundleRef(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../../verifier/testdata/bundle-provenance.json")
	if err != nil {
		t.Fatal(err)
	}
	return "base64:" + base64.StdEncoding.EncodeToString(raw)
}
