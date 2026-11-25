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
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/config"
)

// ManagerControllerOptions returns the controller options every manager in
// this project is built with, in the binary and in the envtest suites.
//
// UsePriorityQueue is false on purpose (#417). controller-runtime v0.24's
// priority queue, its default, can deadlock on shutdown: handleReadyItems
// still counts a worker that already left GetWithPriority, sends to the
// unbuffered get channel and blocks holding the queue locks, so
// Controller.Start never returns. In the binary that delays exit until the
// manager's 30 s runnable grace period; in envtest the stuck goroutine
// fails the leak check of every later case in the package. Fixed upstream
// in kubernetes-sigs/controller-runtime#3540, first released in v0.25.0.
// Remove this override, and its test, when go.mod requires v0.25 or later.
func ManagerControllerOptions() config.Controller {
	return config.Controller{UsePriorityQueue: ptr.To(false)}
}
