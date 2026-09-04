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
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const reviewedReleaseIntegrations = `{
  "schema_version": 1,
  "integrations": [
    {"id":"bub","class":"python-package","required_paths":["integrations/bub/pyproject.toml"],"lock_paths":["integrations/bub/uv.lock"],"consumer_mode":"python"},
    {"id":"claude-code","class":"command-host","required_paths":["integrations/claude-code/plugins/powercontext/.claude-plugin/plugin.json"],"lock_paths":[],"consumer_mode":"command"},
    {"id":"codex","class":"command-host","required_paths":["integrations/codex/plugins/powercontext/.codex-plugin/plugin.json"],"lock_paths":["integrations/codex/plugins/powercontext/uv.lock"],"consumer_mode":"command"},
    {"id":"dsh","class":"command-host","required_paths":["integrations/dsh/plugins/powercontext/lib/index.js"],"lock_paths":["integrations/dsh/plugins/powercontext/pnpm-lock.yaml"],"consumer_mode":"command"},
    {"id":"hermes","class":"command-host","required_paths":["integrations/hermes/plugins/powercontext/plugin.yaml"],"lock_paths":[],"consumer_mode":"command"},
    {"id":"langchain","class":"python-package","required_paths":["integrations/langchain/pyproject.toml"],"lock_paths":["integrations/langchain/uv.lock"],"consumer_mode":"python"},
    {"id":"langgraph","class":"python-package","required_paths":["integrations/langgraph/pyproject.toml"],"lock_paths":["integrations/langgraph/uv.lock"],"consumer_mode":"python"},
    {"id":"openclaw","class":"command-host","required_paths":["integrations/openclaw/plugins/memory-powercontext/dist/index.js"],"lock_paths":["integrations/openclaw/plugins/memory-powercontext/pnpm-lock.yaml"],"consumer_mode":"command"},
    {"id":"opencode","class":"command-host","required_paths":["integrations/opencode/plugins/powercontext/lib/index.js"],"lock_paths":["integrations/opencode/plugins/powercontext/pnpm-lock.yaml"],"consumer_mode":"command"},
    {"id":"pi","class":"command-host","required_paths":["integrations/pi/plugins/powercontext/extensions/powercontext.ts"],"lock_paths":["integrations/pi/plugins/powercontext/pnpm-lock.yaml"],"consumer_mode":"command"},
    {"id":"pydantic-ai","class":"python-package","required_paths":["integrations/pydantic-ai/pyproject.toml"],"lock_paths":["integrations/pydantic-ai/uv.lock"],"consumer_mode":"python"},
    {"id":"workbuddy","class":"command-host","required_paths":["integrations/workbuddy/plugins/powercontext/hooks/hooks.workbuddy.json"],"lock_paths":[],"consumer_mode":"command"}
  ]
}`

func TestReleaseIntegrationInventoryReadsReviewedRoots(t *testing.T) {
	repository := filepath.Clean(filepath.Join("..", ".."))
	integrations, err := readReleaseIntegrations(repository)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateReleaseIntegrations(repository, integrations); err != nil {
		t.Fatal(err)
	}
	if got, want := releaseIntegrationIDs(integrations), []string{
		"bub", "claude-code", "codex", "dsh", "hermes", "langchain", "langgraph", "openclaw", "opencode", "pi", "pydantic-ai", "workbuddy",
	}; !slices.Equal(got, want) {
		t.Fatalf("integration IDs = %v, want %v", got, want)
	}
	if got, want := integrations[0], (releaseIntegration{
		ID: "bub", Class: "python-package", RequiredPaths: []string{"integrations/bub/pyproject.toml"},
		LockPaths: []string{"integrations/bub/uv.lock"}, ConsumerMode: "python",
	}); got.ID != want.ID || got.Class != want.Class || got.ConsumerMode != want.ConsumerMode ||
		!slices.Equal(got.RequiredPaths, want.RequiredPaths) || !slices.Equal(got.LockPaths, want.LockPaths) {
		t.Fatalf("bub integration = %#v, want %#v", got, want)
	}
	if got, want := integrations[len(integrations)-1], (releaseIntegration{
		ID: "workbuddy", Class: "command-host", RequiredPaths: []string{"integrations/workbuddy/plugins/powercontext/hooks/hooks.workbuddy.json"},
		ConsumerMode: "command",
	}); got.ID != want.ID || got.Class != want.Class || got.ConsumerMode != want.ConsumerMode ||
		!slices.Equal(got.RequiredPaths, want.RequiredPaths) || !slices.Equal(got.LockPaths, want.LockPaths) {
		t.Fatalf("workbuddy integration = %#v, want %#v", got, want)
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
			manifest: strings.Replace(reviewedReleaseIntegrations, `"id":"claude-code"`, `"id":"bub"`, 1),
		},
		"absolute path": {
			manifest: strings.Replace(reviewedReleaseIntegrations, `"integrations/bub/pyproject.toml"`, `"/integrations/bub/pyproject.toml"`, 1),
		},
		"parent path": {
			manifest: strings.Replace(reviewedReleaseIntegrations, `"integrations/bub/pyproject.toml"`, `"../integrations/bub/pyproject.toml"`, 1),
		},
		"invalid class": {
			manifest: strings.Replace(reviewedReleaseIntegrations, `"class":"python-package"`, `"class":"unsupported"`, 1),
		},
		"empty required path": {
			manifest: strings.Replace(reviewedReleaseIntegrations, `"required_paths":["integrations/bub/pyproject.toml"]`, `"required_paths":[""]`, 1),
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
			omittedPath: "integrations/bub/pyproject.toml",
		},
		"stale inventory entry": {
			manifest: strings.Replace(
				reviewedReleaseIntegrations,
				`{"id":"workbuddy","class":"command-host","required_paths":["integrations/workbuddy/plugins/powercontext/hooks/hooks.workbuddy.json"],"lock_paths":[],"consumer_mode":"command"}`,
				`{"id":"retired","class":"command-host","required_paths":["integrations/retired/plugin.json"],"lock_paths":[],"consumer_mode":"command"}`,
				1,
			),
		},
		"unclassified integration directory": {
			unlistedRoot: "unclassified",
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
	"integrations/bub/pyproject.toml",
	"integrations/bub/uv.lock",
	"integrations/claude-code/plugins/powercontext/.claude-plugin/plugin.json",
	"integrations/codex/plugins/powercontext/.codex-plugin/plugin.json",
	"integrations/codex/plugins/powercontext/uv.lock",
	"integrations/dsh/plugins/powercontext/lib/index.js",
	"integrations/dsh/plugins/powercontext/pnpm-lock.yaml",
	"integrations/hermes/plugins/powercontext/plugin.yaml",
	"integrations/langchain/pyproject.toml",
	"integrations/langchain/uv.lock",
	"integrations/langgraph/pyproject.toml",
	"integrations/langgraph/uv.lock",
	"integrations/openclaw/plugins/memory-powercontext/dist/index.js",
	"integrations/openclaw/plugins/memory-powercontext/pnpm-lock.yaml",
	"integrations/opencode/plugins/powercontext/lib/index.js",
	"integrations/opencode/plugins/powercontext/pnpm-lock.yaml",
	"integrations/pi/plugins/powercontext/extensions/powercontext.ts",
	"integrations/pi/plugins/powercontext/pnpm-lock.yaml",
	"integrations/pydantic-ai/pyproject.toml",
	"integrations/pydantic-ai/uv.lock",
	"integrations/workbuddy/plugins/powercontext/hooks/hooks.workbuddy.json",
}

func releaseIntegrationIDs(integrations []releaseIntegration) []string {
	ids := make([]string, len(integrations))
	for index, integration := range integrations {
		ids[index] = integration.ID
	}
	return ids
}
