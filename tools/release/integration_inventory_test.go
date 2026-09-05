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
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

const reviewedReleaseIntegrations = `{
  "schema_version": 1,
  "integrations": [
    {"id":"codex","class":"command-host","required_paths":["integrations/codex/plugins/powercontext/.codex-plugin/plugin.json"],"lock_paths":["integrations/codex/plugins/powercontext/uv.lock"],"consumer_mode":"command"},
    {"id":"workbuddy","class":"command-host","required_paths":["integrations/workbuddy/plugins/powercontext/hooks/hooks.workbuddy.json"],"lock_paths":[],"consumer_mode":"command"}
  ]
}`

var reviewedReleaseIntegrationRecords = []releaseIntegration{
	{ID: "codex", Class: "command-host", RequiredPaths: []string{"integrations/codex/plugins/powercontext/.codex-plugin/plugin.json"}, LockPaths: []string{"integrations/codex/plugins/powercontext/uv.lock"}, ConsumerMode: "command"},
	{ID: "workbuddy", Class: "command-host", RequiredPaths: []string{"integrations/workbuddy/plugins/powercontext/hooks/hooks.workbuddy.json"}, LockPaths: []string{}, ConsumerMode: "command"},
}

func TestReleaseIntegrationInventoryCommittedInventoryMatchesReviewedReleaseContract(t *testing.T) {
	repository := filepath.Clean(filepath.Join("..", ".."))
	integrations, err := readReleaseIntegrations(repository)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateReleaseIntegrations(repository, integrations); err != nil {
		t.Fatal(err)
	}
	if difference := cmp.Diff(reviewedReleaseIntegrationRecords, integrations); difference != "" {
		t.Fatalf("committed release integration inventory differs from the reviewed contract (-want +got):\n%s", difference)
	}
}

func TestReleaseIntegrationInventoryRejectsInvalidManifest(t *testing.T) {
	tests := map[string]struct {
		manifest string
	}{
		"duplicate field": {
			manifest: strings.Replace(reviewedReleaseIntegrations, `"schema_version": 1,`, `"schema_version": 1, "schema_version": 1,`, 1),
		},
		"unknown field": {
			manifest: strings.Replace(reviewedReleaseIntegrations, `"schema_version": 1,`, `"schema_version": 1, "unexpected": true,`, 1),
		},
		"duplicate ID": {
			manifest: strings.Replace(reviewedReleaseIntegrations, `"id":"workbuddy"`, `"id":"codex"`, 1),
		},
		"absolute path": {
			manifest: strings.Replace(reviewedReleaseIntegrations, `"integrations/codex/plugins/powercontext/.codex-plugin/plugin.json"`, `"/integrations/codex/plugins/powercontext/.codex-plugin/plugin.json"`, 1),
		},
		"parent path": {
			manifest: strings.Replace(reviewedReleaseIntegrations, `"integrations/codex/plugins/powercontext/.codex-plugin/plugin.json"`, `"../integrations/codex/plugins/powercontext/.codex-plugin/plugin.json"`, 1),
		},
		"invalid class": {
			manifest: strings.Replace(reviewedReleaseIntegrations, `"class":"command-host"`, `"class":"unsupported"`, 1),
		},
		"empty required path": {
			manifest: strings.Replace(reviewedReleaseIntegrations, `"required_paths":["integrations/codex/plugins/powercontext/.codex-plugin/plugin.json"]`, `"required_paths":[""]`, 1),
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			repository := writeReleaseIntegrationRepository(t, test.manifest, nil)
			if _, err := readReleaseIntegrations(repository); err == nil {
				t.Fatal("readReleaseIntegrations accepted an invalid manifest")
			}
		})
	}
}

func TestReleaseIntegrationInventoryRejectsRepositoryDrift(t *testing.T) {
	tests := map[string]releaseIntegrationFixtureOptions{
		"missing integration directory": {
			removeRoots: true,
		},
		"missing required file": {
			omittedPath: "integrations/codex/plugins/powercontext/.codex-plugin/plugin.json",
		},
		"stale inventory entry": {
			manifest: strings.Replace(
				reviewedReleaseIntegrations,
				`{"id":"workbuddy","class":"command-host","required_paths":["integrations/workbuddy/plugins/powercontext/hooks/hooks.workbuddy.json"],"lock_paths":[],"consumer_mode":"command"}`,
				`{"id":"retired","class":"command-host","required_paths":["integrations/retired/plugin.json"],"lock_paths":[],"consumer_mode":"command"}`,
				1,
			),
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			manifest := test.manifest
			if manifest == "" {
				manifest = reviewedReleaseIntegrations
			}
			repository := writeReleaseIntegrationRepository(t, manifest, &test)
			integrations, err := readReleaseIntegrations(repository)
			if err != nil {
				t.Fatal(err)
			}
			if err := validateReleaseIntegrations(repository, integrations); err == nil {
				t.Fatal("validateReleaseIntegrations accepted repository drift")
			}
		})
	}
}

func TestReleaseIntegrationInventoryAllowsUnlistedSourceRoots(t *testing.T) {
	options := releaseIntegrationFixtureOptions{unlistedRoot: "historical"}
	repository := writeReleaseIntegrationRepository(t, reviewedReleaseIntegrations, &options)
	integrations, err := readReleaseIntegrations(repository)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateReleaseIntegrations(repository, integrations); err != nil {
		t.Fatalf("validateReleaseIntegrations rejected an unlisted historical source root: %v", err)
	}
}

func TestReleaseIntegrationInventoryMissingRepositoryRootPreservesNotExist(t *testing.T) {
	repository := writeReleaseIntegrationRepository(t, reviewedReleaseIntegrations, nil)
	integrations, err := readReleaseIntegrations(repository)
	if err != nil {
		t.Fatal(err)
	}
	err = validateReleaseIntegrations(filepath.Join(repository, "missing-root"), integrations)
	if !errors.Is(err, fs.ErrNotExist) || !strings.Contains(err.Error(), "read release repository") {
		t.Fatalf("missing repository root error = %v, want repository-root not-exist error", err)
	}
}

type releaseIntegrationFixtureOptions struct {
	manifest     string
	omittedPath  string
	unlistedRoot string
	removeRoots  bool
}

func writeReleaseIntegrationRepository(t *testing.T, manifest string, options *releaseIntegrationFixtureOptions) string {
	t.Helper()
	repository := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repository, "build"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, "build", "release-integrations.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range releaseIntegrationFixturePaths {
		if err := os.MkdirAll(filepath.Join(repository, filepath.FromSlash(filepath.Dir(path))), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repository, filepath.FromSlash(path)), []byte("fixture\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if options != nil {
		test := *options
		if test.removeRoots {
			if err := os.RemoveAll(filepath.Join(repository, "integrations")); err != nil {
				t.Fatal(err)
			}
		}
		if test.omittedPath != "" {
			if err := os.Remove(filepath.Join(repository, filepath.FromSlash(test.omittedPath))); err != nil {
				t.Fatal(err)
			}
		}
		if test.unlistedRoot != "" {
			if err := os.Mkdir(filepath.Join(repository, "integrations", test.unlistedRoot), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	return repository
}

var releaseIntegrationFixturePaths = []string{
	"integrations/codex/plugins/powercontext/.codex-plugin/plugin.json",
	"integrations/codex/plugins/powercontext/uv.lock",
	"integrations/workbuddy/plugins/powercontext/hooks/hooks.workbuddy.json",
}
