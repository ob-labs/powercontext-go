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

// Command portable-sdk-check rejects direct native, filesystem, environment,
// process-lifecycle, and SQL imports from the deliberate pure public SDK.
package main

import (
	"errors"
	"flag"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

var pureSDKRoots = []string{
	"api/v1",
	"artifact",
	"client",
	"inference",
	"source",
	"trigger",
}

var forbiddenImports = map[string]struct{}{
	"C":             {},
	"database/sql":  {},
	"io/fs":         {},
	"os":            {},
	"os/exec":       {},
	"path/filepath": {},
	"plugin":        {},
	"runtime":       {},
	"syscall":       {},
}

func main() {
	root := flag.String("root", ".", "repository root containing the public SDK")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "portable-sdk-check: unexpected positional arguments")
		os.Exit(2)
	}
	if err := check(*root); err != nil {
		fmt.Fprintln(os.Stderr, "portable-sdk-check:", err)
		os.Exit(1)
	}
}

func check(root string) error {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve repository root: %w", err)
	}
	violations := make([]string, 0)
	for _, packageRoot := range pureSDKRoots {
		rootPath := filepath.Join(absRoot, filepath.FromSlash(packageRoot))
		if _, statErr := os.Stat(rootPath); errors.Is(statErr, os.ErrNotExist) {
			continue
		} else if statErr != nil {
			return fmt.Errorf("inspect %s: %w", packageRoot, statErr)
		}
		walkErr := filepath.WalkDir(rootPath, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			relative, relativeErr := filepath.Rel(absRoot, path)
			if relativeErr != nil {
				return relativeErr
			}
			relative = filepath.ToSlash(relative)
			if entry.IsDir() {
				// artifact/skill deliberately owns host-facing Skill discovery and
				// projection, so filesystem imports there are part of its public
				// contract rather than accidental pure-SDK dependencies.
				if relative == "artifact/skill" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
				return nil
			}
			file, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
			if parseErr != nil {
				return fmt.Errorf("parse %s: %w", relative, parseErr)
			}
			for _, importSpec := range file.Imports {
				importPath, unquoteErr := strconv.Unquote(importSpec.Path.Value)
				if unquoteErr != nil {
					return fmt.Errorf("parse import in %s: %w", relative, unquoteErr)
				}
				if _, forbidden := forbiddenImports[importPath]; forbidden {
					violations = append(violations, fmt.Sprintf("%s imports forbidden package %q", relative, importPath))
				}
			}
			return nil
		})
		if walkErr != nil {
			return fmt.Errorf("scan %s: %w", packageRoot, walkErr)
		}
	}
	if len(violations) == 0 {
		return nil
	}
	slices.Sort(violations)
	return errors.New(strings.Join(violations, "\n"))
}
