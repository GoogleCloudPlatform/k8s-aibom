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

package config

import (
	"testing"
	"time"

	aibomv1beta1 "github.com/GoogleCloudPlatform/k8s-aibom/api/v1beta1"
	"github.com/GoogleCloudPlatform/k8s-aibom/verifier"
)

func TestParseVerification(t *testing.T) {
	t.Run("absent means nil", func(t *testing.T) {
		got, errs := parseVerification(nil)
		if got != nil || len(errs) != 0 {
			t.Fatalf("got %v errs %v", got, errs)
		}
	})
	t.Run("disabled means nil", func(t *testing.T) {
		got, errs := parseVerification(&aibomv1beta1.VerificationConfig{Enabled: false, TrustRootMode: "public"})
		if got != nil || len(errs) != 0 {
			t.Fatalf("got %v errs %v", got, errs)
		}
	})
	t.Run("valid tufMirror with identities and durations", func(t *testing.T) {
		got, errs := parseVerification(&aibomv1beta1.VerificationConfig{
			Enabled:                true,
			TrustRootMode:          "tufMirror",
			TUFMirrorURL:           "https://tuf.internal.example",
			Identities:             []aibomv1beta1.IdentityConstraint{{Issuer: "https://issuer.example", SubjectPattern: `^release@corp\.example$`}},
			PerClaimTimeoutSeconds: 5,
			CacheTTLMinutes:        60,
		})
		if len(errs) != 0 {
			t.Fatalf("errs: %v", errs)
		}
		if got.TrustRootMode != verifier.TrustTUFMirror ||
			got.PerClaimTimeout != 5*time.Second ||
			got.CacheTTL != time.Hour ||
			len(got.Identities) != 1 {
			t.Fatalf("mapping wrong: %+v", got)
		}
	})
	t.Run("tufMirror without URL errors", func(t *testing.T) {
		got, errs := parseVerification(&aibomv1beta1.VerificationConfig{Enabled: true, TrustRootMode: "tufMirror"})
		if got != nil || len(errs) != 1 {
			t.Fatalf("got %v errs %v", got, errs)
		}
	})
	t.Run("bad subjectPattern regex errors", func(t *testing.T) {
		got, errs := parseVerification(&aibomv1beta1.VerificationConfig{
			Enabled:    true,
			Identities: []aibomv1beta1.IdentityConstraint{{SubjectPattern: "(unclosed"}},
		})
		if got != nil || len(errs) != 1 {
			t.Fatalf("got %v errs %v", got, errs)
		}
	})
}
