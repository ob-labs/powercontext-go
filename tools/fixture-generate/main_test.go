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
	"encoding/json/v2"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestRunRecordsConfiguredReleaseCommit(t *testing.T) {
	temp := t.TempDir()
	pythonRoot := filepath.Join(temp, "python")
	fixtureRoot := filepath.Join(temp, "fixtures")
	mustWriteFixtureGeneratorFile(t, filepath.Join(pythonRoot, "openapi", "powercontext.yaml"), "openapi: 3.1.0\n")
	writeRenderedPromptSources(t, pythonRoot)
	for _, name := range []string{
		"authority.db",
		"domain-contract.json",
		"handoff-report-digests.json",
		"provider-matrix.json",
		"scheduler.db",
	} {
		mustWriteFixtureGeneratorFile(t, filepath.Join(fixtureRoot, name), name+"\n")
	}
	mustWriteFixtureGeneratorFile(t, filepath.Join(fixtureRoot, "authority-rows.json"), `{"schema_sha256":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}`)
	initFixtureGeneratorGitRepository(t, pythonRoot)

	output := filepath.Join(fixtureRoot, "manifest.json")
	commit := gitOutputForFixtureGeneratorTest(t, pythonRoot, "rev-parse", "HEAD")
	if err := run(pythonRoot, output, false, fixtureConfig{oracleCommit: commit}); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var got manifest
	if err := json.Unmarshal(contents, &got); err != nil {
		t.Fatal(err)
	}
	if got.OracleCommit != commit {
		t.Fatalf("manifest Oracle commit = %s, want %s", got.OracleCommit, commit)
	}
}

func writeRenderedPromptSources(t *testing.T, root string) {
	t.Helper()
	byPath := make(map[string]string)
	for _, spec := range renderedPromptSpecs {
		path := filepath.Join(root, "src", "powercontext", "builtin", "artifacts", filepath.FromSlash(spec.path))
		byPath[path] += spec.versionName + " = \"" + spec.key + "\"\n" + spec.promptName + " = f\"\"\"" + spec.versionName + "\"\"\".strip()\n"
	}
	for path, contents := range byPath {
		mustWriteFixtureGeneratorFile(t, path, contents)
	}
}

func initFixtureGeneratorGitRepository(t *testing.T, root string) {
	t.Helper()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "fixtures@example.invalid"},
		{"config", "user.name", "Fixture Generator Test"},
		{"add", "."},
		{"commit", "-m", "fixture source"},
	} {
		command := exec.Command("git", append([]string{"-C", root}, args...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
}

func gitOutputForFixtureGeneratorTest(t *testing.T, root string, args ...string) string {
	t.Helper()
	value, err := gitOutput(root, args...)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func mustWriteFixtureGeneratorFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}
