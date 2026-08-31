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

func TestValidateInventoryAcceptsClassifiedOwnedModules(t *testing.T) {
	root := t.TempDir()
	writeModule(t, root, ".")
	writeModule(t, root, "test/downstream")
	inventory := writeInventory(t, root, `{
  "schema_version": 2,
  "modules": [
    {"path": ".", "kind": "production"},
    {"path": "test/downstream", "kind": "external-consumer"}
  ],
  "generated_consumers": {"mode": "temporary", "package": "tools/generated-consumers"}
}`)

	if err := validateInventory(t.Context(), root, inventory); err != nil {
		t.Fatalf("validate complete module inventory: %v", err)
	}
}

func TestReadInventoryRejectsMixedModuleClassifications(t *testing.T) {
	tests := map[string]string{
		"production test module": `{
  "schema_version": 2,
  "modules": [{"path": "test/downstream", "kind": "production"}],
  "generated_consumers": {"mode": "temporary", "package": "tools/generated-consumers"}
}`,
		"consumer outside test": `{
  "schema_version": 2,
  "modules": [{"path": ".", "kind": "external-consumer"}],
  "generated_consumers": {"mode": "temporary", "package": "tools/generated-consumers"}
}`,
		"persistent generated consumer": `{
  "schema_version": 2,
  "modules": [{"path": ".", "kind": "production"}],
  "generated_consumers": {"mode": "checked-in", "package": "tools/generated-consumers"}
}`,
	}
	for name, contents := range tests {
		t.Run(name, func(t *testing.T) {
			inventory := writeInventory(t, t.TempDir(), contents)
			if _, err := readInventory(t.Context(), inventory); err == nil {
				t.Fatal("readInventory accepted an invalid module classification")
			}
		})
	}
}

func TestValidateInventoryRejectsUninventoriedNestedModule(t *testing.T) {
	root := t.TempDir()
	writeModule(t, root, ".")
	writeModule(t, root, "test/downstream")
	inventory := writeInventory(t, root, `{
  "schema_version": 2,
  "modules": [
    {"path": ".", "kind": "production"}
  ],
  "generated_consumers": {"mode": "temporary", "package": "tools/generated-consumers"}
}`)

	err := validateInventory(t.Context(), root, inventory)
	if err == nil || !strings.Contains(err.Error(), "test/downstream") {
		t.Fatalf("uninventoried nested module error = %v", err)
	}
}

func TestValidateInventoryRejectsOwnedModuleBelowGeneratedLikeDirectory(t *testing.T) {
	root := t.TempDir()
	writeModule(t, root, ".")
	writeModule(t, root, "tools/bin/owned")
	inventory := writeInventory(t, root, `{
  "schema_version": 2,
  "modules": [
    {"path": ".", "kind": "production"}
  ],
  "generated_consumers": {"mode": "temporary", "package": "tools/generated-consumers"}
}`)

	err := validateInventory(t.Context(), root, inventory)
	if err == nil || !strings.Contains(err.Error(), "tools/bin/owned") {
		t.Fatalf("uninventoried module below generated-like directory error = %v", err)
	}
}

func TestReadInventoryRejectsMalformedJSONContracts(t *testing.T) {
	tests := map[string]string{
		"unknown member": `{
  "schema_version": 1,
  "modules": [{"path": "."}],
  "unexpected": true
}`,
		"duplicate member": `{
  "schema_version": 1,
  "schema_version": 1,
  "modules": [{"path": "."}]
}`,
		"trailing value": `{
  "schema_version": 1,
  "modules": [{"path": "."}]
} {}`,
	}
	for name, contents := range tests {
		t.Run(name, func(t *testing.T) {
			inventory := writeInventory(t, t.TempDir(), contents)
			if _, err := readInventory(t.Context(), inventory); err == nil {
				t.Fatal("readInventory accepted malformed JSON contract")
			}
		})
	}
}

func TestValidateInventoryRejectsRepositoryParentPath(t *testing.T) {
	root := t.TempDir()
	writeModule(t, root, ".")
	inventory := writeInventory(t, root, `{
  "schema_version": 2,
  "modules": [
    {"path": "..", "kind": "production"}
  ],
  "generated_consumers": {"mode": "temporary", "package": "tools/generated-consumers"}
}`)

	err := validateInventory(t.Context(), root, inventory)
	if err == nil || !strings.Contains(err.Error(), "escapes the repository root") {
		t.Fatalf("repository parent path error = %v", err)
	}
}

func TestValidateInventoryRejectsInvalidChecksum(t *testing.T) {
	tests := map[string]func(*testing.T, string){
		"missing": func(t *testing.T, path string) {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		},
		"empty": func(t *testing.T, path string) {
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, invalidate := range tests {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeModule(t, root, ".")
			writeModule(t, root, "test/downstream")
			invalidate(t, filepath.Join(root, "test", "downstream", "go.sum"))
			inventory := writeInventory(t, root, `{
  "schema_version": 2,
  "modules": [
    {"path": ".", "kind": "production"},
    {"path": "test/downstream", "kind": "external-consumer"}
  ],
  "generated_consumers": {"mode": "temporary", "package": "tools/generated-consumers"}
}`)

			err := validateInventory(t.Context(), root, inventory)
			if err == nil || !strings.Contains(err.Error(), "test/downstream") {
				t.Fatalf("invalid checksum error = %v", err)
			}
		})
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
