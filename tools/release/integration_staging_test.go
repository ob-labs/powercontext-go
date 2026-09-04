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
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestReleaseIntegrationStagingCopiesReviewedInventoryForEveryEdition(t *testing.T) {
	repository := writeReleaseIntegrationRepository(t, reviewedReleaseIntegrations, nil)
	writeIntegrationStagingFixture(t, repository)
	integrations, err := readReleaseIntegrations(repository)
	if err != nil {
		t.Fatal(err)
	}

	for _, edition := range []string{"standard", "full"} {
		t.Run(edition, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), edition)
			if err := stageIntegrations(repository, root); err != nil {
				t.Fatal(err)
			}
			assertStagedReleaseIntegrations(t, root, integrations)
			assertWorkspaceStateIsNotStaged(t, root)
		})
	}
}

func TestReleaseIntegrationStagingRejectsInventoryRepositoryDrift(t *testing.T) {
	tests := map[string]struct {
		mutate  func(t *testing.T, repository string)
		message string
	}{
		"missing integration root": {
			mutate: func(t *testing.T, repository string) {
				if err := os.RemoveAll(filepath.Join(repository, "integrations", "bub")); err != nil {
					t.Fatal(err)
				}
			},
			message: `release integration "bub" root is missing`,
		},
		"unclassified integration root": {
			mutate: func(t *testing.T, repository string) {
				if err := os.Mkdir(filepath.Join(repository, "integrations", "unclassified"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			message: `integration root "unclassified" is absent from the release inventory`,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			repository := writeReleaseIntegrationRepository(t, reviewedReleaseIntegrations, nil)
			writeIntegrationStagingFixture(t, repository)
			test.mutate(t, repository)

			err := stageIntegrations(repository, t.TempDir())
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("stageIntegrations error = %v, want %q", err, test.message)
			}
		})
	}
}

func writeIntegrationStagingFixture(t *testing.T, repository string) {
	t.Helper()
	marketplace := filepath.Join(repository, ".claude-plugin", "marketplace.json")
	if err := os.MkdirAll(filepath.Dir(marketplace), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marketplace, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, directory := range []string{
		".venv", "node_modules", ".mypy_cache", ".pytest_cache", ".ruff_cache",
		"__pycache__", "coverage", ".omx", ".workbuddy", ".playwright-mcp", "dist",
	} {
		path := filepath.Join(repository, "integrations", "bub", directory)
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "workspace.txt"), []byte("workspace\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func assertStagedReleaseIntegrations(t *testing.T, root string, integrations []releaseIntegration) {
	t.Helper()
	for _, integration := range integrations {
		for _, path := range append(slices.Clone(integration.RequiredPaths), integration.LockPaths...) {
			info, err := os.Stat(filepath.Join(root, filepath.FromSlash(path)))
			if err != nil || !info.Mode().IsRegular() {
				t.Errorf("release integration %q did not stage %q: %v", integration.ID, path, err)
			}
		}
	}
}

func assertWorkspaceStateIsNotStaged(t *testing.T, root string) {
	t.Helper()
	excluded := map[string]struct{}{
		".venv": {}, "node_modules": {}, ".mypy_cache": {}, ".pytest_cache": {},
		".ruff_cache": {}, "__pycache__": {}, "coverage": {}, ".omx": {},
		".workbuddy": {}, ".playwright-mcp": {}, "dist": {},
	}
	err := filepath.WalkDir(filepath.Join(root, "integrations"), func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if _, ok := excluded[entry.Name()]; !ok {
			return nil
		}
		if strings.HasSuffix(filepath.ToSlash(path), "openclaw/plugins/memory-powercontext/dist") {
			return nil
		}
		return errors.New("workspace-only entry was staged: " + filepath.ToSlash(path))
	})
	if err != nil {
		t.Fatal(err)
	}
}
