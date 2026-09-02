// Copyright (c) 2026 OceanBase.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package conformance_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFullCapabilityGuidesBindMemoryEvidenceToCapturedSource(t *testing.T) {
	for _, document := range []string{
		filepath.Join("..", "..", "docs", "en", "docs", "how-to", "full-capability-runtime.md"),
		filepath.Join("..", "..", "docs", "zh", "docs", "how-to", "full-capability-runtime.md"),
	} {
		t.Run(document, func(t *testing.T) {
			contents, err := os.ReadFile(document)
			if err != nil {
				t.Fatal(err)
			}
			guide := string(contents)
			for _, fragment := range []string{
				`SOURCE_ID="quickstart-$(date +%s)-$$"`,
				"/v1/memory/entries/list",
				"source_refs",
				"current_cursor",
				"position",
				"entry_id",
				"matched_by",
			} {
				if !strings.Contains(guide, fragment) {
					t.Fatalf("guide does not bind captured Memory evidence through %q", fragment)
				}
			}
		})
	}
}
