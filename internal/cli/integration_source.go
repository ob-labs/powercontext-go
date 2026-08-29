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

package cli

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func validateRemoteRef(ref string) error {
	if strings.TrimSpace(ref) == "" || ref == "." || ref == ".." || strings.ContainsRune(ref, '\x00') {
		return errors.New("invalid Git ref")
	}
	return nil
}

func githubRepositoryCloneURL(source string) (string, error) {
	value := strings.TrimSpace(source)
	if strings.HasPrefix(value, "git@github.com:") {
		path, err := githubRepositoryPath(strings.TrimPrefix(value, "git@github.com:"))
		if err != nil {
			return "", err
		}
		return "git@github.com:" + path, nil
	}
	if strings.HasPrefix(value, "git@") {
		return "", errors.New("invalid GitHub source")
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return "", errors.New("invalid GitHub source")
	}
	if parsed.Scheme != "" {
		if parsed.Scheme != "https" || parsed.Host != "github.com" || parsed.User != nil ||
			parsed.RawQuery != "" || parsed.Fragment != "" {
			return "", errors.New("invalid GitHub source")
		}
		path, pathErr := githubRepositoryPath(parsed.EscapedPath())
		if pathErr != nil {
			return "", pathErr
		}
		return "https://github.com/" + path, nil
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
		return "", errors.New("invalid GitHub source")
	}
	path, err := githubRepositoryPath(value)
	if err != nil {
		return "", err
	}
	return "https://github.com/" + path, nil
}

func githubRepositoryPath(value string) (string, error) {
	path, err := url.PathUnescape(strings.Trim(strings.TrimSpace(value), "/"))
	if err != nil {
		return "", errors.New("invalid GitHub source")
	}
	path = strings.TrimSuffix(path, ".git")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" ||
		strings.ContainsAny(path, "?#@\\") || strings.IndexFunc(path, func(r rune) bool { return r == '\x00' || r == ' ' || r == '\t' || r == '\r' || r == '\n' }) >= 0 {
		return "", errors.New("invalid GitHub source")
	}
	return path + ".git", nil
}

func refreshIntegrationCheckout(
	ctx context.Context,
	commands systemCommandExecutor,
	cloneURL, ref, target string,
	validate func(string) error,
) (string, error) {
	parent, err := resolvePath(filepath.Dir(target))
	if err != nil {
		return "", err
	}
	if mkdirErr := os.MkdirAll(parent, 0o755); mkdirErr != nil {
		return "", errors.New("cannot create integration checkout directory")
	}
	target, err = resolvePath(target)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(parent, target)
	if err != nil || relative == "." || relative == ".." || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return "", errors.New("integration checkout escapes its data directory")
	}
	staging, err := os.MkdirTemp(parent, "."+filepath.Base(target)+"-")
	if err != nil {
		return "", errors.New("cannot create integration staging directory")
	}
	removeStaging := true
	defer func() {
		if removeStaging {
			_ = os.RemoveAll(staging)
		}
	}()
	if _, cloneErr := commands.Run(ctx, "git", "clone", "--depth", "1", "--branch", ref, cloneURL, staging); cloneErr != nil {
		return "", errors.New("failed to clone the GitHub source")
	}
	if validationErr := validate(staging); validationErr != nil {
		return "", validationErr
	}
	backup := ""
	if _, statErr := os.Lstat(target); statErr == nil {
		backup, err = os.MkdirTemp(parent, "."+filepath.Base(target)+"-previous-")
		if err != nil {
			return "", errors.New("cannot create integration backup path")
		}
		if removeErr := os.Remove(backup); removeErr != nil {
			return "", removeErr
		}
		if renameErr := os.Rename(target, backup); renameErr != nil {
			return "", errors.New("cannot preserve the previous integration checkout")
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", errors.New("cannot inspect integration checkout")
	}
	if activateErr := os.Rename(staging, target); activateErr != nil {
		if backup != "" {
			_ = os.Rename(backup, target)
		}
		return "", errors.New("cannot activate the refreshed integration checkout")
	}
	removeStaging = false
	if backup != "" {
		_ = os.RemoveAll(backup)
	}
	return target, nil
}

func pathsEqual(left, right string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
	}
	return filepath.Clean(left) == filepath.Clean(right)
}

func displayPath(path string) string {
	return fmt.Sprintf("%q", filepath.Base(filepath.Clean(path)))
}
