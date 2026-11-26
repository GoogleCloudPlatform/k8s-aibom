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

	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/config"
)

// The priority queue stays off while go.mod is on controller-runtime
// v0.24 (#417). Delete this test with the override on the v0.25 bump.
func TestManagerControllerOptions_PriorityQueueOff(t *testing.T) {
	o := ManagerControllerOptions()
	if o.UsePriorityQueue == nil || *o.UsePriorityQueue {
		t.Fatalf("UsePriorityQueue = %v, want an explicit false (#417)", o.UsePriorityQueue)
	}
}

// testControllerOptions is ManagerControllerOptions plus what test
// managers need: several managers per process register the same
// controller names.
func testControllerOptions() config.Controller {
	o := ManagerControllerOptions()
	o.SkipNameValidation = ptr.To(true)
	return o
}
