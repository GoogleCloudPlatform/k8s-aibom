// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package scraper

import "fmt"

// ExtractionError represents a non-fatal failure to extract a specific attribute.
// It carries the EvidenceSource so the controller can bucket metrics appropriately.
type ExtractionError struct {
	EvidenceSource EvidenceSource
	Message        string
}

func (e *ExtractionError) Error() string {
	return fmt.Sprintf("extraction from %s: %s", e.EvidenceSource, e.Message)
}
