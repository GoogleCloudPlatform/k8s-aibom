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

package controller

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"

	// This test deliberately imports BOTH API versions: it proves the
	// dual-serving contract of Design 001 (schema-identical versions,
	// conversion None, storage on v1beta1).
	aibomv1alpha1 "github.com/GoogleCloudPlatform/k8s-aibom/api/v1alpha1"
	aibomv1beta1 "github.com/GoogleCloudPlatform/k8s-aibom/api/v1beta1"
)

// TestIntegration_DualVersionServing proves the graduation contract on a
// real API server: an object written via v1alpha1 (stored as v1beta1 —
// the CRDs' storage version) reads back identically via v1beta1, and a
// v1beta1 write reads back identically via v1alpha1. Conversion strategy
// is None; the schemas are field-identical by Design 001.
func TestIntegration_DualVersionServing(t *testing.T) {
	env := startEnvTest(t)
	ctx := context.Background()

	alphaScheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(alphaScheme))
	utilruntime.Must(aibomv1alpha1.AddToScheme(alphaScheme))
	alphaClient, err := client.New(env.cfg, client.Options{Scheme: alphaScheme})
	if err != nil {
		t.Fatalf("v1alpha1 client: %v", err)
	}

	// Write via the OLD version.
	alpha := &aibomv1alpha1.AIBOMControllerConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "dual-version-serving-test"},
		Spec: aibomv1alpha1.AIBOMControllerConfigSpec{
			BOMGeneration: aibomv1alpha1.BOMGenerationConfig{
				InlineThresholdBytes:     131072,
				StaleThresholdReconciles: 5,
			},
			Sinks: []aibomv1alpha1.SinkConfig{{
				Name: "private-ca-webhook",
				Type: aibomv1alpha1.SinkTypeWebhook,
				Webhook: &aibomv1alpha1.WebhookSinkSpec{
					Endpoint: "https://collector.example.test/bom",
					Auth: &aibomv1alpha1.WebhookAuth{BearerToken: &aibomv1alpha1.BearerTokenAuth{
						SecretRef:   aibomv1alpha1.SecretKeyRef{Name: "webhook-token", Key: "token"},
						CASecretRef: &aibomv1alpha1.SecretKeyRef{Name: "alpha-ca", Key: "alpha-ca.crt"},
					}},
				},
			}},
		},
	}
	if err := alphaClient.Create(ctx, alpha); err != nil {
		t.Fatalf("create via v1alpha1: %v", err)
	}
	t.Cleanup(func() { _ = alphaClient.Delete(context.Background(), alpha) })

	// Read via the NEW version: identical fields, same UID.
	var beta aibomv1beta1.AIBOMControllerConfig
	if err := env.k8sClient.Get(ctx, types.NamespacedName{Name: "dual-version-serving-test"}, &beta); err != nil {
		t.Fatalf("get via v1beta1: %v", err)
	}
	if beta.UID != alpha.UID {
		t.Errorf("UID mismatch across versions: %s vs %s", beta.UID, alpha.UID)
	}
	if beta.Spec.BOMGeneration.InlineThresholdBytes != 131072 {
		t.Errorf("v1beta1 read: InlineThresholdBytes = %d, want 131072", beta.Spec.BOMGeneration.InlineThresholdBytes)
	}

	if len(beta.Spec.Sinks) != 1 || beta.Spec.Sinks[0].Webhook == nil || beta.Spec.Sinks[0].Webhook.Auth == nil || beta.Spec.Sinks[0].Webhook.Auth.BearerToken == nil {
		t.Fatal("v1beta1 read lost the bearer webhook configuration")
	}
	betaCA := beta.Spec.Sinks[0].Webhook.Auth.BearerToken.CASecretRef
	if betaCA == nil || betaCA.Name != "alpha-ca" || betaCA.Key != "alpha-ca.crt" {
		t.Fatalf("v1beta1 read lost caSecretRef name/key: %+v", betaCA)
	}

	// Write new CA reference values via the NEW version; read back via the OLD.
	beta.Spec.Sinks[0].Webhook.Auth.BearerToken.CASecretRef = &aibomv1beta1.SecretKeyRef{Name: "beta-ca", Key: "beta-ca.crt"}
	beta.Spec.BOMGeneration.StaleThresholdReconciles = 7
	if err := env.k8sClient.Update(ctx, &beta); err != nil {
		t.Fatalf("update via v1beta1: %v", err)
	}
	var alphaAgain aibomv1alpha1.AIBOMControllerConfig
	if err := alphaClient.Get(ctx, types.NamespacedName{Name: "dual-version-serving-test"}, &alphaAgain); err != nil {
		t.Fatalf("get via v1alpha1: %v", err)
	}
	if alphaAgain.Spec.BOMGeneration.StaleThresholdReconciles != 7 {
		t.Errorf("v1alpha1 read after v1beta1 write: StaleThresholdReconciles = %d, want 7", alphaAgain.Spec.BOMGeneration.StaleThresholdReconciles)
	}
	if len(alphaAgain.Spec.Sinks) != 1 || alphaAgain.Spec.Sinks[0].Webhook == nil || alphaAgain.Spec.Sinks[0].Webhook.Auth == nil || alphaAgain.Spec.Sinks[0].Webhook.Auth.BearerToken == nil {
		t.Fatal("v1alpha1 read after v1beta1 write lost the bearer webhook configuration")
	}
	alphaCA := alphaAgain.Spec.Sinks[0].Webhook.Auth.BearerToken.CASecretRef
	if alphaCA == nil || alphaCA.Name != "beta-ca" || alphaCA.Key != "beta-ca.crt" {
		t.Errorf("v1alpha1 read after v1beta1 write lost caSecretRef name/key: %+v", alphaCA)
	}
}

// TestIntegration_DualVersionServing_WorkloadKinds proves that
// spec.discovery.workloadKinds survives writes through either version:
// the field was v1beta1-only in v1.6.0, so a v1alpha1 write pruned it.
func TestIntegration_DualVersionServing_WorkloadKinds(t *testing.T) {
	env := startEnvTest(t)
	ctx := context.Background()

	alphaScheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(alphaScheme))
	utilruntime.Must(aibomv1alpha1.AddToScheme(alphaScheme))
	alphaClient, err := client.New(env.cfg, client.Options{Scheme: alphaScheme})
	if err != nil {
		t.Fatalf("v1alpha1 client: %v", err)
	}

	const name = "dual-version-workload-kinds-test"
	kinds := []string{"apps/Deployment", "batch/CronJob"}

	// Create through v1alpha1 with the allowlist set.
	alpha := &aibomv1alpha1.AIBOMControllerConfig{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: aibomv1alpha1.AIBOMControllerConfigSpec{
			BOMGeneration: aibomv1alpha1.BOMGenerationConfig{
				InlineThresholdBytes:     131072,
				StaleThresholdReconciles: 5,
			},
			Discovery: aibomv1alpha1.DiscoveryConfig{
				WorkloadKinds: kinds,
			},
		},
	}
	if err := alphaClient.Create(ctx, alpha); err != nil {
		t.Fatalf("create via v1alpha1: %v", err)
	}
	t.Cleanup(func() { _ = alphaClient.Delete(context.Background(), alpha) })

	var beta aibomv1beta1.AIBOMControllerConfig
	if err := env.k8sClient.Get(ctx, types.NamespacedName{Name: name}, &beta); err != nil {
		t.Fatalf("get via v1beta1: %v", err)
	}
	if got := beta.Spec.Discovery.WorkloadKinds; !equalStrings(got, kinds) {
		t.Fatalf("v1beta1 read after v1alpha1 create: workloadKinds = %v, want %v", got, kinds)
	}

	// Patch through v1alpha1 without mentioning the field: the stored
	// allowlist must survive.
	var alphaAgain aibomv1alpha1.AIBOMControllerConfig
	if err := alphaClient.Get(ctx, types.NamespacedName{Name: name}, &alphaAgain); err != nil {
		t.Fatalf("get via v1alpha1: %v", err)
	}
	patched := alphaAgain.DeepCopy()
	patched.Spec.BOMGeneration.StaleThresholdReconciles = 7
	if err := alphaClient.Patch(ctx, patched, client.MergeFrom(&alphaAgain)); err != nil {
		t.Fatalf("patch via v1alpha1: %v", err)
	}
	if err := env.k8sClient.Get(ctx, types.NamespacedName{Name: name}, &beta); err != nil {
		t.Fatalf("get via v1beta1 after patch: %v", err)
	}
	if beta.Spec.BOMGeneration.StaleThresholdReconciles != 7 {
		t.Errorf("v1beta1 read after v1alpha1 patch: StaleThresholdReconciles = %d, want 7", beta.Spec.BOMGeneration.StaleThresholdReconciles)
	}
	if got := beta.Spec.Discovery.WorkloadKinds; !equalStrings(got, kinds) {
		t.Errorf("v1beta1 read after v1alpha1 patch: workloadKinds = %v, want %v", got, kinds)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
