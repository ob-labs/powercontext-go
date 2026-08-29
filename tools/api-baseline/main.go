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

// Command api-baseline writes path-independent Go export data for an approved
// subset of one module's public packages.
package main

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/tools/go/gcexportdata"
	"golang.org/x/tools/go/packages"
)

func main() {
	moduleRoot := flag.String("module-root", ".", "directory containing the module go.mod")
	modulePath := flag.String("module", "", "module path recorded in the baseline")
	output := flag.String("output", "", "output export-data bundle")
	check := flag.String("check", "", "verify an existing bundle contains the exact package inventory")
	flag.Parse()
	if *modulePath == "" || (*output == "") == (*check == "") || flag.NArg() == 0 {
		flag.Usage()
		os.Exit(2)
	}
	var err error
	if *check != "" {
		err = checkBaseline(*check, *modulePath, flag.Args())
	} else {
		err = writeBaseline(*moduleRoot, *output, *modulePath, flag.Args())
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "api-baseline: %v\n", err)
		os.Exit(1)
	}
}

func writeBaseline(moduleRoot, output, modulePath string, importPaths []string) error {
	paths, err := normalizePackages(modulePath, importPaths)
	if err != nil {
		return err
	}

	config := &packages.Config{
		Dir: moduleRoot,
		Mode: packages.NeedName | packages.NeedTypes | packages.NeedImports |
			packages.NeedDeps | packages.NeedModule,
	}
	loaded, err := packages.Load(config, paths...)
	if err != nil {
		return fmt.Errorf("load public packages: %w", err)
	}
	byPath := make(map[string]*types.Package, len(loaded))
	var loadErrors []error
	for _, pkg := range loaded {
		for _, packageError := range pkg.Errors {
			loadErrors = append(loadErrors, errors.New(packageError.Error()))
		}
		if pkg.Module == nil || pkg.Module.Path != modulePath {
			loadErrors = append(loadErrors, fmt.Errorf("public package %q is not owned by module %q", pkg.PkgPath, modulePath))
			continue
		}
		if pkg.Types != nil {
			byPath[pkg.PkgPath] = pkg.Types
		}
	}
	if loadErr := errors.Join(loadErrors...); loadErr != nil {
		return loadErr
	}

	bundle := make([]*types.Package, 0, len(paths))
	for _, importPath := range paths {
		pkg, ok := byPath[importPath]
		if !ok {
			return fmt.Errorf("public package %q did not load", importPath)
		}
		bundle = append(bundle, pkg)
	}

	outputDirectory := filepath.Dir(output)
	if mkdirErr := os.MkdirAll(outputDirectory, 0o755); mkdirErr != nil {
		return fmt.Errorf("create baseline directory: %w", mkdirErr)
	}
	temporary, err := os.CreateTemp(outputDirectory, ".api-baseline-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary baseline: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()

	if _, writeErr := fmt.Fprintln(temporary, modulePath); writeErr != nil {
		return errors.Join(fmt.Errorf("write module path: %w", writeErr), temporary.Close())
	}
	if writeErr := gcexportdata.WriteBundle(temporary, token.NewFileSet(), bundle); writeErr != nil {
		return errors.Join(fmt.Errorf("write public package bundle: %w", writeErr), temporary.Close())
	}
	if chmodErr := temporary.Chmod(0o644); chmodErr != nil {
		return errors.Join(fmt.Errorf("set baseline permissions: %w", chmodErr), temporary.Close())
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close public API baseline: %w", err)
	}
	if err := os.Rename(temporaryPath, output); err != nil {
		return fmt.Errorf("publish public API baseline: %w", err)
	}
	return nil
}

func checkBaseline(path, modulePath string, importPaths []string) error {
	expected, err := normalizePackages(modulePath, importPaths)
	if err != nil {
		return err
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read public API baseline: %w", err)
	}
	reader := bufio.NewReader(bytes.NewReader(contents))
	recordedModule, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("read baseline module path: %w", err)
	}
	if recordedModule = strings.TrimSuffix(recordedModule, "\n"); recordedModule != modulePath {
		return fmt.Errorf("baseline module = %q, want %q", recordedModule, modulePath)
	}
	bundle, err := gcexportdata.ReadBundle(reader, token.NewFileSet(), map[string]*types.Package{})
	if err != nil {
		return fmt.Errorf("read public package bundle: %w", err)
	}
	actual := make([]string, 0, len(bundle))
	for _, pkg := range bundle {
		actual = append(actual, pkg.Path())
	}
	slices.Sort(actual)
	if !slices.Equal(actual, expected) {
		return fmt.Errorf("baseline packages = %q, want %q", actual, expected)
	}
	return nil
}

func normalizePackages(modulePath string, importPaths []string) ([]string, error) {
	if modulePath == "" {
		return nil, errors.New("module path is empty")
	}
	if len(importPaths) == 0 {
		return nil, errors.New("public package list is empty")
	}
	paths := slices.Clone(importPaths)
	slices.Sort(paths)
	for index, importPath := range paths {
		if importPath != modulePath && !strings.HasPrefix(importPath, modulePath+"/") {
			return nil, fmt.Errorf("public package %q is outside module %q", importPath, modulePath)
		}
		if index > 0 && paths[index-1] == importPath {
			return nil, fmt.Errorf("duplicate public package %q", importPath)
		}
	}
	return paths, nil
}
