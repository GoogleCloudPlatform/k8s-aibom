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

import "testing"

// The strict-readiness contract: a fresh Store reports valid config
// (absent CR is defaults-by-choice, not failure), and the bit follows
// SetConfigInvalid.
func TestStore_ConfigInvalid(t *testing.T) {
	s := NewStore(DefaultSnapshot())
	if s.ConfigInvalid() {
		t.Error("fresh Store: ConfigInvalid() = true, want false")
	}
	s.SetConfigInvalid(true)
	if !s.ConfigInvalid() {
		t.Error("after SetConfigInvalid(true): ConfigInvalid() = false")
	}
	s.SetConfigInvalid(false)
	if s.ConfigInvalid() {
		t.Error("after SetConfigInvalid(false): ConfigInvalid() = true")
	}
}
