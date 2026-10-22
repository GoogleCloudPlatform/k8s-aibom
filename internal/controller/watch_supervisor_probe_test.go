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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// probeReader is a client.Reader whose List returns the configured
// items and whose Get returns the configured error: the shape of an
// API server that serves lists from its per-version watch cache while
// single-object reads go through a conversion webhook (#216).
type probeReader struct {
	client.Reader
	items  int
	getErr error
	gets   int
	lists  int
}

func (r *probeReader) List(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
	r.lists++
	ul := list.(*unstructured.UnstructuredList)
	for i := 0; i < r.items; i++ {
		u := unstructured.Unstructured{}
		u.SetName("graph-" + string(rune('a'+i)))
		u.SetNamespace("team-a")
		ul.Items = append(ul.Items, u)
	}
	return nil
}

func (r *probeReader) Get(_ context.Context, _ client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
	r.gets++
	return r.getErr
}

var probeGVK = schema.GroupVersionKind{Group: "nvidia.com", Version: "v1beta1", Kind: "DynamoGraphDeployment"}

func TestProbe_GetFailureIsAnOutageEvenWhenListSucceeds(t *testing.T) {
	webhookDown := errors.New(`conversion webhook for nvidia.com/v1alpha1, Kind=DynamoGraphDeployment failed: connection refused`)
	r := &probeReader{items: 1, getErr: webhookDown}
	s := &WatchSupervisor{APIReader: r}
	err := s.probe(context.Background(), ThirdPartyWatch{Name: "DynamoGraphDeployment", GVK: probeGVK})
	if !errors.Is(err, webhookDown) {
		t.Fatalf("probe error = %v, want the GET's webhook error", err)
	}
	if r.lists != 1 || r.gets != 1 {
		t.Fatalf("lists=%d gets=%d, want one of each", r.lists, r.gets)
	}
}

func TestProbe_HealthyWhenGetSucceeds(t *testing.T) {
	r := &probeReader{items: 2}
	s := &WatchSupervisor{APIReader: r}
	if err := s.probe(context.Background(), ThirdPartyWatch{Name: "DynamoGraphDeployment", GVK: probeGVK}); err != nil {
		t.Fatalf("probe = %v, want nil", err)
	}
	if r.gets != 1 {
		t.Fatalf("gets=%d, want exactly one GET (the first listed item)", r.gets)
	}
}

func TestProbe_NoObjectsFallsBackToTheList(t *testing.T) {
	r := &probeReader{items: 0, getErr: errors.New("must not be called")}
	s := &WatchSupervisor{APIReader: r}
	if err := s.probe(context.Background(), ThirdPartyWatch{Name: "DynamoGraphDeployment", GVK: probeGVK}); err != nil {
		t.Fatalf("probe = %v, want nil with no objects", err)
	}
	if r.gets != 0 {
		t.Fatalf("gets=%d, want 0 when the list is empty", r.gets)
	}
}

func TestProbe_NotFoundBetweenListAndGetIsNotAnOutage(t *testing.T) {
	nf := apierrors.NewNotFound(schema.GroupResource{Group: "nvidia.com", Resource: "dynamographdeployments"}, "graph-a")
	r := &probeReader{items: 1, getErr: nf}
	s := &WatchSupervisor{APIReader: r}
	if err := s.probe(context.Background(), ThirdPartyWatch{Name: "DynamoGraphDeployment", GVK: probeGVK}); err != nil {
		t.Fatalf("probe = %v, want nil on NotFound", err)
	}
}
