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
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

func runChecksum(arguments []string) error {
	flags := flag.NewFlagSet("checksum", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var output string
	flags.StringVar(&output, "output", "", "checksum manifest path")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if output == "" || flags.NArg() == 0 {
		return errors.New("checksum requires output and at least one file")
	}
	return writeFileChecksums(output, flags.Args())
}

func runVerify(arguments []string) error {
	flags := flag.NewFlagSet("verify", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var expected string
	flags.StringVar(&expected, "sha256", "", "expected lowercase SHA-256")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if !validSHA256(expected) || flags.NArg() != 1 {
		return errors.New("verify requires one file and a lowercase SHA-256")
	}
	actual, _, err := hashFile(flags.Arg(0))
	if err != nil {
		return err
	}
	if actual != expected {
		return fmt.Errorf("SHA-256 mismatch for %q", filepath.Base(flags.Arg(0)))
	}
	return nil
}

func writeFileChecksums(output string, paths []string) error {
	type checksum struct{ name, hash string }
	seen := make(map[string]struct{}, len(paths))
	values := make([]checksum, 0, len(paths))
	for _, path := range paths {
		name := filepath.Base(path)
		if _, exists := seen[name]; exists {
			return fmt.Errorf("duplicate checksum basename %q", name)
		}
		seen[name] = struct{}{}
		hash, _, err := hashFile(path)
		if err != nil {
			return err
		}
		values = append(values, checksum{name: name, hash: hash})
	}
	sort.Slice(values, func(left, right int) bool { return values[left].name < values[right].name })
	var contents strings.Builder
	for _, value := range values {
		fmt.Fprintf(&contents, "%s  %s\n", value.hash, value.name)
	}
	return writeNewFile(output, []byte(contents.String()), 0o644)
}

func writeTreeChecksums(root string) error {
	var paths []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root || entry.IsDir() || filepath.Base(path) == "SHA256SUMS" {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			resolved, err := filepath.EvalSymlinks(path)
			if err != nil {
				return err
			}
			paths = append(paths, resolved+"\x00"+path)
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("unsupported release file %q", path)
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		return err
	}
	sort.Slice(paths, func(left, right int) bool {
		return checksumDisplayPath(root, paths[left]) < checksumDisplayPath(root, paths[right])
	})
	var contents strings.Builder
	for _, value := range paths {
		hashPath, displayPath := value, value
		if resolved, original, ok := strings.Cut(value, "\x00"); ok {
			hashPath, displayPath = resolved, original
		}
		hash, _, err := hashFile(hashPath)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, displayPath)
		if err != nil {
			return err
		}
		fmt.Fprintf(&contents, "%s  %s\n", hash, filepath.ToSlash(relative))
	}
	return os.WriteFile(filepath.Join(root, "SHA256SUMS"), []byte(contents.String()), 0o644)
}

func verifyTreeChecksums(root string) error {
	manifestPath := filepath.Join(root, "SHA256SUMS")
	contents, err := readBoundedFile(manifestPath, maxMetadataBytes)
	if err != nil {
		return fmt.Errorf("read internal checksum manifest: %w", err)
	}
	expected := make(map[string]struct{})
	err = filepath.WalkDir(root, func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if filePath == root || entry.IsDir() || filepath.Base(filePath) == "SHA256SUMS" {
			return nil
		}
		if entry.Type()&os.ModeSymlink == 0 && !entry.Type().IsRegular() {
			return fmt.Errorf("unsupported release file %q", filePath)
		}
		relative, err := filepath.Rel(root, filePath)
		if err != nil {
			return err
		}
		expected[filepath.ToSlash(relative)] = struct{}{}
		return nil
	})
	if err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(expected))
	for line := range strings.SplitSeq(string(contents), "\n") {
		if line == "" {
			continue
		}
		hash, name, ok := strings.Cut(line, "  ")
		if !ok || !validSHA256(hash) || name == "" || strings.ContainsRune(name, '\\') ||
			path.IsAbs(name) || path.Clean(name) != name || name == "." || name == ".." ||
			strings.HasPrefix(name, "../") || name == "SHA256SUMS" {
			return errors.New("internal checksum manifest contains an invalid record")
		}
		if _, duplicate := seen[name]; duplicate {
			return fmt.Errorf("internal checksum manifest contains duplicate path %q", name)
		}
		seen[name] = struct{}{}
		if _, exists := expected[name]; !exists {
			return fmt.Errorf("internal checksum manifest names absent path %q", name)
		}
		actual, _, err := hashFile(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			return fmt.Errorf("verify internal checksum for %q: %w", name, err)
		}
		if actual != hash {
			return fmt.Errorf("internal checksum mismatch for %q", name)
		}
		delete(expected, name)
	}
	if len(expected) != 0 {
		missing := slices.Sorted(maps.Keys(expected))
		return fmt.Errorf("internal checksum manifest is missing path %q", missing[0])
	}
	return nil
}

func checksumDisplayPath(root, value string) string {
	if _, original, ok := strings.Cut(value, "\x00"); ok {
		value = original
	}
	relative, _ := filepath.Rel(root, value)
	return filepath.ToSlash(relative)
}

func hashFile(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}

func hashTree(root string) (string, error) {
	hash := sha256.New()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root || entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		_, _ = io.WriteString(hash, filepath.ToSlash(relative))
		_, _ = hash.Write([]byte{0})
		if entry.Type()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			_, _ = io.WriteString(hash, "link:"+link)
		} else if entry.Type().IsRegular() {
			fileHash, _, err := hashFile(path)
			if err != nil {
				return err
			}
			_, _ = io.WriteString(hash, fileHash)
		} else {
			return fmt.Errorf("unsupported native runtime entry %q", relative)
		}
		_, _ = hash.Write([]byte{0})
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
