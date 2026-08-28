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
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

var (
	darwinORTLibrary = regexp.MustCompile(`^libonnxruntime(?:_[A-Za-z0-9_]+)?(?:\.[0-9]+)*\.dylib$`)
	linuxORTLibrary  = regexp.MustCompile(`^libonnxruntime(?:_[A-Za-z0-9_]+)?\.so(?:\.[0-9]+)*$`)
)

func copyTree(source, destination string) error {
	root, err := filepath.Abs(source)
	if err != nil {
		return err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	info, err := os.Stat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("release tree %q is not a directory", filepath.Base(source))
	}
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return os.MkdirAll(destination, 0o755)
		}
		if skipReleaseEntry(entry) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			resolved, err := filepath.EvalSymlinks(path)
			if err != nil {
				return err
			}
			inside, err := filepath.Rel(root, resolved)
			if err != nil || inside == ".." || strings.HasPrefix(inside, ".."+string(filepath.Separator)) {
				return fmt.Errorf("release symlink %q escapes its source tree", relative)
			}
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			if filepath.IsAbs(link) {
				return fmt.Errorf("release symlink %q is absolute", relative)
			}
			return os.Symlink(link, target)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported release entry %q", relative)
		}
		return copyRegularFile(path, target, os.FileMode(normalizedFileMode(info.Mode())))
	})
}

// copyONNXRuntime keeps release bundles relocatable and limited to the native
// libraries needed at runtime. Upstream archives also contain CMake metadata,
// pkg-config files, and (on macOS) large dSYM trees; those are build inputs, not
// runtime dependencies.
func copyONNXRuntime(source, destination, goos string) error {
	root, err := filepath.Abs(source)
	if err != nil {
		return err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	info, err := os.Stat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("ONNX Runtime library source %q is not a directory", filepath.Base(source))
	}
	if goos != "darwin" && goos != "linux" {
		return fmt.Errorf("unsupported ONNX Runtime release target %q", goos)
	}
	if err := os.MkdirAll(destination, 0o755); err != nil {
		return err
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	copiedRegular := false
	for _, entry := range entries {
		if !isONNXRuntimeLibrary(entry.Name(), goos) {
			continue
		}
		sourcePath := filepath.Join(root, entry.Name())
		targetPath := filepath.Join(destination, entry.Name())
		entryInfo, err := os.Lstat(sourcePath)
		if err != nil {
			return err
		}
		if entryInfo.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(sourcePath)
			if err != nil {
				return err
			}
			if filepath.IsAbs(link) {
				return fmt.Errorf("ONNX Runtime symlink %q is absolute", entry.Name())
			}
			resolved, err := filepath.EvalSymlinks(sourcePath)
			if err != nil {
				return err
			}
			inside, err := filepath.Rel(root, resolved)
			if err != nil || inside == ".." || strings.HasPrefix(inside, ".."+string(filepath.Separator)) {
				return fmt.Errorf("ONNX Runtime symlink %q escapes its source tree", entry.Name())
			}
			if !isONNXRuntimeLibrary(filepath.Base(resolved), goos) {
				return fmt.Errorf("ONNX Runtime symlink %q has an invalid target", entry.Name())
			}
			if err := os.Symlink(link, targetPath); err != nil {
				return err
			}
			continue
		}
		if !entryInfo.Mode().IsRegular() {
			return fmt.Errorf("unsupported ONNX Runtime entry %q", entry.Name())
		}
		if err := copyRegularFile(sourcePath, targetPath, os.FileMode(normalizedFileMode(entryInfo.Mode()))); err != nil {
			return err
		}
		copiedRegular = true
	}
	if !copiedRegular {
		return fmt.Errorf("ONNX Runtime library directory %q contains no runtime library for %s", filepath.Base(source), goos)
	}
	return nil
}

func isONNXRuntimeLibrary(name, goos string) bool {
	switch goos {
	case "darwin":
		return darwinORTLibrary.MatchString(name)
	case "linux":
		return linuxORTLibrary.MatchString(name)
	default:
		return false
	}
}

func skipReleaseEntry(entry fs.DirEntry) bool {
	name := entry.Name()
	if entry.IsDir() && slices.Contains([]string{
		".git", ".mypy_cache", ".pytest_cache", ".ruff_cache", ".venv",
		"__pycache__", "coverage", "dist", "node_modules",
	}, name) {
		return true
	}
	return name == ".DS_Store" || strings.HasSuffix(name, ".pyc") || strings.HasSuffix(name, ".pyo")
}

func copyRegularFile(source, destination string, mode os.FileMode) (returnErr error) {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("release input %q is not a regular file", filepath.Base(source))
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = input.Close() }()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := output.Close(); returnErr == nil {
			returnErr = closeErr
		}
	}()
	_, err = io.Copy(output, input)
	return err
}

func writeJSON(path string, value any) error {
	var contents strings.Builder
	encoder := json.NewEncoder(&contents)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(contents.String()), 0o644)
}

func writeNewFile(path string, contents []byte, mode os.FileMode) (returnErr error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := file.Close(); returnErr == nil {
			returnErr = closeErr
		}
	}()
	_, err = file.Write(contents)
	return err
}
