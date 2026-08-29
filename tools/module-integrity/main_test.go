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
	"strings"
	"testing"
)

func TestValidateInventoryAcceptsEveryOwnedModule(t *testing.T) {
	root := t.TempDir()
	writeModule(t, root, ".")
	writeModule(t, root, "test/downstream")
	inventory := writeInventory(t, root, `{
  "schema_version": 1,
  "modules": [
    {"path": "."},
    {"path": "test/downstream"}
  ]
}`)

	if err := validateInventory(t.Context(), root, inventory); err != nil {
		t.Fatalf("validate complete module inventory: %v", err)
	}
}

func TestValidateInventoryRejectsUninventoriedNestedModule(t *testing.T) {
	root := t.TempDir()
	writeModule(t, root, ".")
	writeModule(t, root, "test/downstream")
	inventory := writeInventory(t, root, `{
  "schema_version": 1,
  "modules": [
    {"path": "."}
  ]
}`)

	err := validateInventory(t.Context(), root, inventory)
	if err == nil || !strings.Contains(err.Error(), "test/downstream") {
		t.Fatalf("uninventoried nested module error = %v", err)
	}
}

func TestValidateInventoryRejectsModuleWithoutChecksum(t *testing.T) {
	root := t.TempDir()
	writeModule(t, root, ".")
	writeModule(t, root, "test/downstream")
	if err := os.Remove(filepath.Join(root, "test", "downstream", "go.sum")); err != nil {
		t.Fatal(err)
	}
	inventory := writeInventory(t, root, `{
  "schema_version": 1,
  "modules": [
    {"path": "."},
    {"path": "test/downstream"}
  ]
}`)

	err := validateInventory(t.Context(), root, inventory)
	if err == nil || !strings.Contains(err.Error(), "test/downstream") {
		t.Fatalf("missing checksum error = %v", err)
	}
}

func writeModule(t *testing.T, root, relative string) {
	t.Helper()
	directory := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, contents := range map[string]string{
		"go.mod": "module example.com/test\n\ngo 1.27.0\n",
		"go.sum": "example.com/test v0.0.0/go.mod h1:placeholder\n",
	} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func writeInventory(t *testing.T, root, contents string) string {
	t.Helper()
	path := filepath.Join(root, "test", "module-inventory.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
