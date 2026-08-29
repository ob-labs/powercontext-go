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
	"bufio"
	"bytes"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/tools/go/gcexportdata"
)

func TestWriteBaselineIsDeterministicAndPathFree(t *testing.T) {
	moduleRoot := writeProbeModule(t)
	first := filepath.Join(t.TempDir(), "first.apidiff")
	second := filepath.Join(t.TempDir(), "second.apidiff")
	packages := []string{"example.com/probe/sample"}

	if err := writeBaseline(moduleRoot, first, "example.com/probe", packages); err != nil {
		t.Fatal(err)
	}
	if err := writeBaseline(moduleRoot, second, "example.com/probe", packages); err != nil {
		t.Fatal(err)
	}
	firstBytes, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	secondBytes, err := os.ReadFile(second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstBytes, secondBytes) {
		t.Fatal("identical public packages produced different API baselines")
	}
	for _, forbidden := range []string{moduleRoot, filepath.ToSlash(moduleRoot)} {
		if bytes.Contains(firstBytes, []byte(forbidden)) {
			t.Fatalf("API baseline contains module checkout path %q", forbidden)
		}
	}

	contents, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(bytes.NewReader(contents))
	modulePath, err := reader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSuffix(modulePath, "\n"); got != "example.com/probe" {
		t.Fatalf("baseline module path = %q, want example.com/probe", got)
	}
	bundle, err := gcexportdata.ReadBundle(reader, token.NewFileSet(), map[string]*types.Package{})
	if err != nil {
		t.Fatal(err)
	}
	gotPackages := make([]string, 0, len(bundle))
	for _, pkg := range bundle {
		gotPackages = append(gotPackages, pkg.Path())
	}
	slices.Sort(gotPackages)
	if !slices.Equal(gotPackages, packages) {
		t.Fatalf("baseline packages = %q, want %q", gotPackages, packages)
	}
}

func TestWriteBaselineFailurePreservesExistingOutput(t *testing.T) {
	moduleRoot := writeProbeModule(t)
	output := filepath.Join(t.TempDir(), "baseline.apidiff")
	const sentinel = "approved baseline"
	if err := os.WriteFile(output, []byte(sentinel), 0o600); err != nil {
		t.Fatal(err)
	}

	err := writeBaseline(moduleRoot, output, "example.com/probe", []string{"example.com/probe/missing"})
	if err == nil {
		t.Fatal("writeBaseline accepted a missing public package")
	}
	payload, readErr := os.ReadFile(output)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(payload) != sentinel {
		t.Fatalf("failed baseline generation replaced approved output with %q", payload)
	}
}

func TestCheckBaselineRejectsPackageInventoryDrift(t *testing.T) {
	moduleRoot := writeProbeModule(t)
	output := filepath.Join(t.TempDir(), "baseline.apidiff")
	packages := []string{"example.com/probe/sample"}
	if err := writeBaseline(moduleRoot, output, "example.com/probe", packages); err != nil {
		t.Fatal(err)
	}
	if err := checkBaseline(output, "example.com/probe", packages); err != nil {
		t.Fatalf("checkBaseline rejected the exact package inventory: %v", err)
	}
	withMissingBaseline := append(slices.Clone(packages), "example.com/probe/other")
	if err := checkBaseline(output, "example.com/probe", withMissingBaseline); err == nil {
		t.Fatal("checkBaseline accepted a baseline missing a deliberate public package")
	}
}

func TestWriteBaselineRejectsDuplicatePackages(t *testing.T) {
	moduleRoot := writeProbeModule(t)
	output := filepath.Join(t.TempDir(), "baseline.apidiff")
	packagePath := "example.com/probe/sample"
	if err := writeBaseline(moduleRoot, output, "example.com/probe", []string{packagePath, packagePath}); err == nil {
		t.Fatal("writeBaseline accepted a duplicate public package")
	}
}

func writeProbeModule(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/probe\n\ngo 1.27.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	packageDirectory := filepath.Join(root, "sample")
	if err := os.Mkdir(packageDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	const source = "package sample\n\ntype Value struct {\n\tName string\n}\n"
	if err := os.WriteFile(filepath.Join(packageDirectory, "sample.go"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}
