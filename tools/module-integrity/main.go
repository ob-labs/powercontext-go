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

// Command module-integrity verifies that every Go module owned by this
// repository has an explicit inventory entry and a committed checksum file.
package main

import (
	"context"
	"encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const inventorySchemaVersion = 1

type inventory struct {
	SchemaVersion int           `json:"schema_version"`
	Modules       []moduleEntry `json:"modules"`
}

type moduleEntry struct {
	Path string `json:"path"`
}

func main() {
	root := flag.String("root", ".", "repository root containing owned Go modules")
	inventoryPath := flag.String("inventory", "test/module-inventory.json", "module inventory JSON file")
	flag.Parse()

	if err := validateInventory(context.Background(), *root, *inventoryPath); err != nil {
		fmt.Fprintln(os.Stderr, "module-integrity:", err)
		os.Exit(1)
	}
}

func validateInventory(ctx context.Context, root, inventoryPath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve repository root: %w", err)
	}
	declared, err := readInventory(ctx, inventoryPath)
	if err != nil {
		return err
	}
	actual, err := findModules(ctx, absoluteRoot)
	if err != nil {
		return err
	}
	if missing := difference(declared, actual); len(missing) > 0 {
		return fmt.Errorf("inventory declares missing module paths: %s", strings.Join(missing, ", "))
	}
	if missing := difference(actual, declared); len(missing) > 0 {
		return fmt.Errorf("Go modules are missing from inventory: %s", strings.Join(missing, ", "))
	}
	for _, path := range declared {
		if err := ctx.Err(); err != nil {
			return err
		}
		checksum := filepath.Join(absoluteRoot, filepath.FromSlash(path), "go.sum")
		info, statErr := os.Stat(checksum)
		if statErr != nil {
			return fmt.Errorf("module %s checksum file: %w", path, statErr)
		}
		if info.IsDir() || info.Size() == 0 {
			return fmt.Errorf("module %s checksum file must be a non-empty regular file", path)
		}
	}
	return nil
}

func readInventory(ctx context.Context, path string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read module inventory: %w", err)
	}
	var value inventory
	if err := json.Unmarshal(contents, &value, json.RejectUnknownMembers(true)); err != nil {
		return nil, fmt.Errorf("decode module inventory: %w", err)
	}
	if value.SchemaVersion != inventorySchemaVersion {
		return nil, fmt.Errorf("module inventory schema_version = %d, want %d", value.SchemaVersion, inventorySchemaVersion)
	}
	if len(value.Modules) == 0 {
		return nil, errors.New("module inventory must declare at least one module")
	}
	paths := make([]string, 0, len(value.Modules))
	seen := make(map[string]struct{}, len(value.Modules))
	for _, entry := range value.Modules {
		path, err := cleanModulePath(entry.Path)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[path]; exists {
			return nil, fmt.Errorf("module inventory declares %s more than once", path)
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
	}
	slices.Sort(paths)
	return paths, nil
}

func cleanModulePath(path string) (string, error) {
	if path == "." {
		return path, nil
	}
	if path == "" || filepath.IsAbs(path) || strings.Contains(path, `\`) {
		return "", fmt.Errorf("module path %q must be a repository-relative slash path", path)
	}
	cleaned := filepath.ToSlash(filepath.Clean(path))
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("module path %q escapes the repository root", path)
	}
	return cleaned, nil
}

func findModules(ctx context.Context, root string) ([]string, error) {
	paths := make([]string, 0)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() && path != root {
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			if ignoredDirectory(relative) {
				return filepath.SkipDir
			}
		}
		if entry.IsDir() || entry.Name() != "go.mod" {
			return nil
		}
		directory := filepath.Dir(path)
		relative, err := filepath.Rel(root, directory)
		if err != nil {
			return err
		}
		if relative == "." {
			paths = append(paths, ".")
			return nil
		}
		paths = append(paths, filepath.ToSlash(relative))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("discover owned Go modules: %w", err)
	}
	slices.Sort(paths)
	return paths, nil
}

func ignoredDirectory(relative string) bool {
	switch filepath.Base(relative) {
	case ".git", "node_modules":
		return true
	}
	switch filepath.ToSlash(relative) {
	case ".tools", "bin", "coverage", "dist", "site":
		return true
	}
	return false
}

func difference(left, right []string) []string {
	values := make(map[string]struct{}, len(right))
	for _, value := range right {
		values[value] = struct{}{}
	}
	missing := make([]string, 0)
	for _, value := range left {
		if _, found := values[value]; !found {
			missing = append(missing, value)
		}
	}
	return missing
}
