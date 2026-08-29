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

package skill

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"
)

type packageFile struct {
	path     string
	relative string
}

func packageFingerprint(packagePath string) (string, error) {
	var files []packageFile
	err := filepath.WalkDir(packagePath, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == packagePath {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("Agent Skill packages containing symlinks are not supported")
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		relative, err := filepath.Rel(packagePath, path)
		if err != nil {
			return err
		}
		files = append(files, packageFile{path: path, relative: filepath.ToSlash(relative)})
		return nil
	})
	if err != nil {
		return "", err
	}
	slices.SortFunc(files, func(left, right packageFile) int { return strings.Compare(left.relative, right.relative) })
	if len(files) < 1 || len(files) > MaxExternalFiles {
		return "", fmt.Errorf("Agent Skill package has an unsupported file count")
	}
	digest := sha256.New()
	total := 0
	var size [8]byte
	for _, file := range files {
		content, err := readBounded(file.path, MaxExternalPackageBytes-total)
		if err != nil {
			return "", err
		}
		total += len(content)
		if total > MaxExternalPackageBytes {
			return "", fmt.Errorf("Agent Skill package exceeds the supported size")
		}
		relative := []byte(file.relative)
		binary.BigEndian.PutUint32(size[:4], uint32(len(relative)))
		_, _ = digest.Write(size[:4])
		_, _ = digest.Write(relative)
		binary.BigEndian.PutUint64(size[:], uint64(len(content)))
		_, _ = digest.Write(size[:])
		_, _ = digest.Write(content)
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func readManifest(path string) (string, error) {
	infoBefore, err := os.Lstat(path)
	if err != nil || infoBefore.Mode()&os.ModeSymlink != 0 || !infoBefore.Mode().IsRegular() {
		return "", fmt.Errorf("external Skill manifest is not a regular file")
	}
	content, err := readBounded(path, MaxExternalManifestBytes)
	if err != nil {
		return "", err
	}
	infoAfter, err := os.Lstat(path)
	if err != nil || !os.SameFile(infoBefore, infoAfter) || infoAfter.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("external Skill manifest changed while reading")
	}
	if !utf8.Valid(content) {
		return "", fmt.Errorf("external Skill manifest must be UTF-8")
	}
	return string(content), nil
}

func readBounded(path string, maximum int) ([]byte, error) {
	if maximum < 0 {
		return nil, fmt.Errorf("file bound exhausted")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	content, err := io.ReadAll(io.LimitReader(file, int64(maximum)+1))
	if err != nil {
		return nil, err
	}
	if len(content) > maximum {
		return nil, fmt.Errorf("external Skill file exceeds the supported size")
	}
	return content, nil
}

func externalText(label, value string, maximum int) error {
	trimmed := trimPythonWhitespace(value)
	if !utf8.ValidString(value) || trimmed == "" || value != trimmed {
		return fmt.Errorf("external Skill %s must be non-empty and trimmed", label)
	}
	if utf8.RuneCountInString(value) > maximum {
		return fmt.Errorf("external Skill %s must not exceed %d characters", label, maximum)
	}
	return nil
}

func resolveLoose(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	absolute = filepath.Clean(absolute)
	resolved, err := filepath.EvalSymlinks(absolute)
	if err == nil {
		return filepath.Clean(resolved), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	// filepath.EvalSymlinks requires the full path to exist, while Python's
	// Path.resolve(strict=False) resolves every existing ancestor. Preserve
	// that behavior so a future target below /var and the same live path below
	// /private/var cannot acquire two different identities on macOS.
	ancestor := absolute
	var suffix []string
	for {
		if _, statErr := os.Lstat(ancestor); statErr == nil {
			resolvedAncestor, resolveErr := filepath.EvalSymlinks(ancestor)
			if resolveErr != nil {
				return "", resolveErr
			}
			for index := len(suffix) - 1; index >= 0; index-- {
				resolvedAncestor = filepath.Join(resolvedAncestor, suffix[index])
			}
			return filepath.Clean(resolvedAncestor), nil
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return "", statErr
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return absolute, nil
		}
		suffix = append(suffix, filepath.Base(ancestor))
		ancestor = parent
	}
}
