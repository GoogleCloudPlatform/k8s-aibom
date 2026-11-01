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
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	aibomv1beta1 "github.com/GoogleCloudPlatform/k8s-aibom/api/v1beta1"
)

// TestConfigChangeReloads pins which updates to the config CR reload it.
// SecretKeyRef documents touching an annotation as the way to make the
// controller re-read a rotated Secret. Annotations live in metadata, so
// with the status subresource they do not bump metadata.generation; a
// generation-only check dropped that update and the Secret was not
// re-read.
func TestConfigChangeReloads(t *testing.T) {
	base := func() *aibomv1beta1.AIBOMControllerConfig {
		return &aibomv1beta1.AIBOMControllerConfig{ObjectMeta: metav1.ObjectMeta{
			Name:        "default",
			Generation:  3,
			Annotations: map[string]string{"meta.helm.sh/release-name": "k8s-aibom"},
			Labels:      map[string]string{"app.kubernetes.io/name": "k8s-aibom"},
		}}
	}
	cases := []struct {
		name   string
		mutate func(c *aibomv1beta1.AIBOMControllerConfig)
		want   bool
	}{
		{
			name:   "spec change bumps generation",
			mutate: func(c *aibomv1beta1.AIBOMControllerConfig) { c.Generation++ },
			want:   true,
		},
		{
			name: "annotation added (rotation touch)",
			mutate: func(c *aibomv1beta1.AIBOMControllerConfig) {
				c.Annotations["aibom.k8saibom.dev/reloaded-at"] = "1730505600"
			},
			want: true,
		},
		{
			name: "annotation value changed",
			mutate: func(c *aibomv1beta1.AIBOMControllerConfig) {
				c.Annotations["meta.helm.sh/release-name"] = "other"
			},
			want: true,
		},
		{
			name:   "annotation removed",
			mutate: func(c *aibomv1beta1.AIBOMControllerConfig) { c.Annotations = nil },
			want:   true,
		},
		{
			name: "status-only update (the controller's own write)",
			mutate: func(c *aibomv1beta1.AIBOMControllerConfig) {
				c.ResourceVersion = "42"
				now := metav1.Now()
				c.Status.LastLoadedAt = &now
				c.Status.ObservedGeneration = c.Generation
			},
			want: false,
		},
		{
			name: "label change",
			mutate: func(c *aibomv1beta1.AIBOMControllerConfig) {
				c.Labels["team"] = "platform"
			},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			oldObj, newObj := base(), base()
			tc.mutate(newObj)
			if got := configChangeReloads(oldObj, newObj); got != tc.want {
				t.Errorf("configChangeReloads = %v, want %v", got, tc.want)
			}
		})
	}
}
