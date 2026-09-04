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

	stagedByEdition := make(map[string][]string, 2)
	for _, edition := range []string{"standard", "full"} {
		t.Run(edition, func(t *testing.T) {
			root := t.TempDir()
			options, facts := releaseIntegrationStagingOptions(t, edition)
			if err := stageRelease(repository, root, options, facts); err != nil {
				t.Fatal(err)
			}
			stagedByEdition[edition] = stagedReleaseIntegrationPaths(t, root, integrations)
			assertWorkspaceStateIsNotStaged(t, root)
			assertEditionNativeAssets(t, root, edition)
		})
	}
	if !slices.Equal(stagedByEdition["standard"], stagedByEdition["full"]) {
		t.Fatalf("staged integration inventory differs between editions (-standard +full):\n%s", strings.Join(stagedByEdition["standard"], "\n")+"\n"+strings.Join(stagedByEdition["full"], "\n"))
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

			destination := t.TempDir()
			err := stageIntegrations(repository, destination)
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("stageIntegrations error = %v, want %q", err, test.message)
			}
			entries, readErr := os.ReadDir(destination)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if len(entries) != 0 {
				t.Fatalf("staging destination has entries after rejected staging: %v", entries)
			}
			for _, path := range []string{".claude-plugin", "integrations"} {
				if _, statErr := os.Stat(filepath.Join(destination, path)); !errors.Is(statErr, fs.ErrNotExist) {
					t.Fatalf("staging destination %q exists after rejected staging: %v", path, statErr)
				}
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
	for _, path := range []string{
		"LICENSE",
		"README.md",
		".env.example",
		"openapi/powercontext.yaml",
		"docs/release/INSTALL.md",
	} {
		fixturePath := filepath.Join(repository, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(fixturePath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fixturePath, []byte("fixture\n"), 0o600); err != nil {
			t.Fatal(err)
		}
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

func releaseIntegrationStagingOptions(t *testing.T, edition string) (packageOptions, binaryFacts) {
	t.Helper()
	inputs := t.TempDir()
	binary := filepath.Join(inputs, "powercontext")
	if err := os.WriteFile(binary, []byte("binary\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	options := packageOptions{Binary: binary, Edition: edition}
	if edition == "full" {
		onnxRuntime := filepath.Join(inputs, "onnxruntime")
		if err := os.Mkdir(onnxRuntime, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(onnxRuntime, "libonnxruntime.so"), []byte("onnx\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		options.ONNXRuntimeDir = onnxRuntime
	}
	return options, binaryFacts{GOOS: "linux"}
}

func stagedReleaseIntegrationPaths(t *testing.T, root string, integrations []releaseIntegration) []string {
	t.Helper()
	paths := make([]string, 0)
	for _, integration := range integrations {
		for _, path := range append(slices.Clone(integration.RequiredPaths), integration.LockPaths...) {
			info, err := os.Stat(filepath.Join(root, filepath.FromSlash(path)))
			if err != nil || !info.Mode().IsRegular() {
				t.Errorf("release integration %q did not stage %q: %v", integration.ID, path, err)
				continue
			}
			paths = append(paths, path)
		}
	}
	slices.Sort(paths)
	return paths
}

func assertEditionNativeAssets(t *testing.T, root, edition string) {
	t.Helper()
	path := filepath.Join(root, "lib", "onnxruntime", "libonnxruntime.so")
	_, err := os.Stat(path)
	if edition == "full" && err != nil {
		t.Fatalf("full edition ONNX Runtime library = %v", err)
	}
	if edition == "standard" && !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("standard edition staged ONNX Runtime library: %v", err)
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
