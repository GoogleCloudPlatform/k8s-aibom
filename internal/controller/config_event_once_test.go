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
	"errors"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	aibomv1beta1 "github.com/GoogleCloudPlatform/k8s-aibom/api/v1beta1"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/config"
	"github.com/GoogleCloudPlatform/k8s-aibom/internal/metrics"
)

// The transition event is emitted before the status write, so a failed
// status write must not make the retry emit it again: the state change
// was already observed and reported. lastObserved used to be advanced
// only after a successful status write, so a conflict (or any other
// status error) re-sent ConfigLoaded and counted the reload twice in
// aibom_controller_config_reloads_total.
func TestReconcile_StatusWriteFailure_TransitionEventOnce(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"conflict", apierrors.NewConflict(schema.GroupResource{Group: aibomv1beta1.GroupVersion.Group, Resource: "aibomcontrollerconfigs"}, config.DefaultConfigName, errors.New("object was modified"))},
		{"other error", errors.New("status write failed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			failures := 1
			c := fake.NewClientBuilder().
				WithScheme(newConfigFakeScheme(t)).
				WithObjects(validCR()).
				WithStatusSubresource(&aibomv1beta1.AIBOMControllerConfig{}).
				WithInterceptorFuncs(interceptor.Funcs{
					SubResourceUpdate: func(ctx context.Context, cl client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
						if failures > 0 {
							failures--
							return tc.err
						}
						return cl.SubResource(sub).Update(ctx, obj, opts...)
					},
				}).
				Build()
			rec := &captureRecorder{}
			r := &AIBOMControllerConfigReconciler{
				Client: c,
				Loader: &config.Loader{
					Client:      c,
					SinkFactory: config.NoopSinkFactory{},
					ConfigName:  config.DefaultConfigName,
				},
				ConfigStore:   config.NewStore(config.DefaultSnapshot()),
				Recorder:      rec,
				ControllerPod: &corev1.ObjectReference{Kind: "Pod", Name: "ctl", Namespace: "ns"},
				ConfigName:    config.DefaultConfigName,
			}
			loaded := metrics.ConfigReloads.WithLabelValues("loaded")
			before := testutil.ToFloat64(loaded)

			req := ctrl.Request{NamespacedName: types.NamespacedName{Name: config.DefaultConfigName}}
			// First pass: the status write fails; the reconcile asks
			// to be retried (requeue on conflict, error otherwise).
			_, _ = r.Reconcile(context.Background(), req)
			// The retry: the status write succeeds.
			if _, err := r.Reconcile(context.Background(), req); err != nil {
				t.Fatalf("second Reconcile: %v", err)
			}

			if got := len(rec.eventsWithReason(EventReasonConfigLoaded)); got != 1 {
				t.Errorf("%s events = %d, want 1: the retry after a failed status write re-emitted the transition", EventReasonConfigLoaded, got)
			}
			if got := testutil.ToFloat64(loaded) - before; got != 1 {
				t.Errorf("config reloads (loaded) grew by %v, want 1", got)
			}
			if r.lastObserved != stateValid {
				t.Errorf("lastObserved = %v, want valid", r.lastObserved)
			}
		})
	}
}
