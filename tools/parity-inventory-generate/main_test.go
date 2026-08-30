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

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadJSONRejectsAmbiguousDocuments(t *testing.T) {
	tests := []struct {
		name     string
		contents string
	}{
		{name: "duplicate member", contents: `{"schema_version": 999, "schema_version": 1}`},
		{name: "unknown member", contents: `{"schema_version": 1, "future_semantics": true}`},
		{name: "trailing value", contents: `{"schema_version": 1} {}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "document.json")
			if err := os.WriteFile(path, []byte(test.contents), 0o600); err != nil {
				t.Fatal(err)
			}
			var document struct {
				SchemaVersion int `json:"schema_version"`
			}
			if err := readJSON(path, &document); err == nil {
				t.Error("accepted ambiguous JSON document")
			}
		})
	}
}

func TestDeclaresTestRequiresTypeScriptCall(t *testing.T) {
	tests := []struct {
		name     string
		contents string
		want     bool
	}{
		{name: "single quoted call", contents: "  it('records evidence', () => {})", want: true},
		{name: "double quoted call", contents: `it("records evidence", () => {})`, want: true},
		{name: "comment", contents: "// it('records evidence', () => {})", want: false},
		{name: "disabled alias", contents: "xit('records evidence', () => {})", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := declaresTest("ts", []byte(test.contents), "records evidence"); got != test.want {
				t.Errorf("declaresTest() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestResolveOutputPathKeepsAbsoluteAndRootsRelative(t *testing.T) {
	root := filepath.Join(t.TempDir(), "repository")
	absolute := filepath.Join(t.TempDir(), "parity-inventory.json")
	for _, test := range []struct {
		name   string
		output string
		want   string
	}{
		{name: "absolute", output: absolute, want: absolute},
		{name: "relative", output: filepath.Join("test", "parity-inventory.json"), want: filepath.Join(root, "test", "parity-inventory.json")},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := resolveOutputPath(root, test.output); got != test.want {
				t.Fatalf("resolveOutputPath(%q, %q) = %q, want %q", root, test.output, got, test.want)
			}
		})
	}
}
