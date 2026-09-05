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
	"os/exec"
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
			assertSupportedIntegrationRoots(t, root)
			stagedByEdition[edition] = stagedReleaseIntegrationPaths(t, root, integrations)
			assertWorkspaceStateIsNotStaged(t, root)
			assertEditionNativeAssets(t, root, edition)
		})
	}
	if !slices.Equal(stagedByEdition["standard"], stagedByEdition["full"]) {
		t.Fatalf("staged integration inventory differs between editions (-standard +full):\n%s", strings.Join(stagedByEdition["standard"], "\n")+"\n"+strings.Join(stagedByEdition["full"], "\n"))
	}
}

func TestReleaseIntegrationStagingExcludesIgnoredFiles(t *testing.T) {
	repository := writeReleaseIntegrationRepository(t, reviewedReleaseIntegrations, nil)
	writeIntegrationStagingFixture(t, repository)
	for _, path := range []string{
		"integrations/codex/.env",
		"integrations/codex/.env.local",
		"integrations/codex/trace.log",
	} {
		if err := os.WriteFile(filepath.Join(repository, filepath.FromSlash(path)), []byte("private\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		runIntegrationStagingGit(t, repository, "check-ignore", "--quiet", "--", path)
	}

	root := t.TempDir()
	if err := stageIntegrations(repository, root); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"integrations/codex/.env",
		"integrations/codex/.env.local",
		"integrations/codex/trace.log",
	} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(path))); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("ignored integration file %q was staged: %v", path, err)
		}
	}
	for _, path := range []string{
		"integrations/codex/plugins/powercontext/.codex-plugin/plugin.json",
		"integrations/workbuddy/plugins/powercontext/hooks/hooks.workbuddy.json",
	} {
		info, err := os.Stat(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil || !info.Mode().IsRegular() {
			t.Errorf("tracked integration file %q was not staged: %v", path, err)
		}
	}
	integrations, err := readReleaseIntegrations(repository)
	if err != nil {
		t.Fatal(err)
	}
	stagedReleaseIntegrationPaths(t, root, integrations)
}

func TestReleaseIntegrationStagingFromSourceCopyUsesReviewedManifest(t *testing.T) {
	repository := writeReleaseIntegrationRepository(t, reviewedReleaseIntegrations, nil)
	writeIntegrationStagingSourceFixture(t, repository)
	for _, path := range []string{
		"integrations/codex/.env",
		"integrations/codex/trace.log",
	} {
		if err := os.WriteFile(filepath.Join(repository, filepath.FromSlash(path)), []byte("private\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(repository, ".git")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("source copy has Git metadata: %v", err)
	}

	root := t.TempDir()
	if err := stageIntegrations(repository, root); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"integrations/codex/plugins/powercontext/.codex-plugin/plugin.json",
		"integrations/workbuddy/plugins/powercontext/hooks/hooks.workbuddy.json",
	} {
		if info, err := os.Stat(filepath.Join(root, filepath.FromSlash(path))); err != nil || !info.Mode().IsRegular() {
			t.Errorf("reviewed integration file %q was not staged: %v", path, err)
		}
	}
	for _, path := range []string{
		"integrations/codex/.env",
		"integrations/codex/trace.log",
		"integrations/claude-code/README.md",
	} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(path))); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("extra source-copy file %q was staged: %v", path, err)
		}
	}
}

func TestReleaseIntegrationStagingRejectsTrackedFileManifestDrift(t *testing.T) {
	repository := writeReleaseIntegrationRepository(t, reviewedReleaseIntegrations, nil)
	writeIntegrationStagingFixture(t, repository)
	drift := "integrations/codex/review-drift.txt"
	if err := os.WriteFile(filepath.Join(repository, filepath.FromSlash(drift)), []byte("drift\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runIntegrationStagingGit(t, repository, "add", "--", drift)

	destination := t.TempDir()
	err := stageIntegrations(repository, destination)
	if err == nil || !strings.Contains(err.Error(), "release integration file manifest does not match tracked files") {
		t.Fatalf("stageIntegrations error = %v, want tracked-file manifest drift error", err)
	}
	if entries, readErr := os.ReadDir(destination); readErr != nil || len(entries) != 0 {
		t.Fatalf("staging destination after manifest drift = %v, %v; want empty", entries, readErr)
	}
}

func TestReleaseIntegrationStagingPreflightsEveryReviewedFile(t *testing.T) {
	repository := writeReleaseIntegrationRepository(t, reviewedReleaseIntegrations, nil)
	writeIntegrationStagingFixture(t, repository)
	missing := "integrations/workbuddy/plugins/powercontext/hooks/hooks.workbuddy.json"
	if err := os.Remove(filepath.Join(repository, filepath.FromSlash(missing))); err != nil {
		t.Fatal(err)
	}
	destination := t.TempDir()
	err := stageIntegrations(repository, destination)
	if err == nil || !strings.Contains(err.Error(), missing) {
		t.Fatalf("stageIntegrations error = %v, want missing late reviewed path %q", err, missing)
	}
	entries, readErr := os.ReadDir(destination)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("staging destination after missing reviewed file = %v, %v; want empty", entries, readErr)
	}
}

func TestReleaseIntegrationStagingRejectsInventoryRepositoryDrift(t *testing.T) {
	tests := map[string]struct {
		mutate  func(t *testing.T, repository string)
		message string
	}{
		"missing integration root": {
			mutate: func(t *testing.T, repository string) {
				if err := os.RemoveAll(filepath.Join(repository, "integrations", "codex")); err != nil {
					t.Fatal(err)
				}
			},
			message: `release integration "codex" root is missing`,
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
			for _, path := range []string{"integrations"} {
				if _, statErr := os.Stat(filepath.Join(destination, path)); !errors.Is(statErr, fs.ErrNotExist) {
					t.Fatalf("staging destination %q exists after rejected staging: %v", path, statErr)
				}
			}
		})
	}
}

func writeIntegrationStagingFixture(t *testing.T, repository string) {
	t.Helper()
	tracked := writeIntegrationStagingSourceFixture(t, repository)
	runIntegrationStagingGit(t, repository, "init", "--quiet")
	tracked = append(tracked, ".gitignore", releaseIntegrationFilesManifest, "integrations/claude-code/README.md")
	runIntegrationStagingGit(t, repository, append([]string{"add", "--"}, tracked...)...)
}

func writeIntegrationStagingSourceFixture(t *testing.T, repository string) []string {
	t.Helper()
	ignore := filepath.Join(repository, ".gitignore")
	if err := os.WriteFile(ignore, []byte("*.log\n.env\n.env.*\n"), 0o600); err != nil {
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
	historical := filepath.Join(repository, "integrations", "claude-code", "README.md")
	if err := os.MkdirAll(filepath.Dir(historical), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(historical, []byte("historical source\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, directory := range []string{
		".venv", "node_modules", ".mypy_cache", ".pytest_cache", ".ruff_cache",
		"__pycache__", "coverage", ".omx", ".workbuddy", ".playwright-mcp", "dist",
	} {
		path := filepath.Join(repository, "integrations", "codex", directory)
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "workspace.txt"), []byte("workspace\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	tracked := slices.Clone(releaseIntegrationFixturePaths)
	slices.Sort(tracked)
	manifest := strings.Join(tracked, "\n") + "\n"
	manifestPath := filepath.Join(repository, filepath.FromSlash(releaseIntegrationFilesManifest))
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	return tracked
}

func runIntegrationStagingGit(t *testing.T, repository string, arguments ...string) {
	t.Helper()
	command := exec.CommandContext(t.Context(), "git", append([]string{"-C", repository}, arguments...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
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

func assertSupportedIntegrationRoots(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, "integrations"))
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			got = append(got, entry.Name())
		}
	}
	want := []string{"codex", "workbuddy"}
	if !slices.Equal(got, want) {
		t.Fatalf("staged integration roots = %v, want %v", got, want)
	}
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
