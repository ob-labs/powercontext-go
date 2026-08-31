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
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestSetupAndDoctorExposeCurrentHostMatrix(t *testing.T) {
	command := newCommandWithAllDependencies(
		VersionInfo{Version: "test"}, &strings.Builder{}, &strings.Builder{}, nil, nil, &scriptedSystemCommands{t: t},
	)
	for parentName, want := range map[string][]string{
		"setup":  {"claude-code", "codex", "dsh", "hermes", "openclaw", "opencode", "pi", "select"},
		"doctor": {"claude-code", "codex", "dsh", "hermes", "integrations", "openclaw", "opencode", "pi"},
	} {
		parent, _, err := command.Find([]string{parentName})
		if err != nil {
			t.Fatal(err)
		}
		got := make([]string, 0, len(parent.Commands()))
		for _, child := range parent.Commands() {
			if !child.IsAdditionalHelpTopicCommand() {
				got = append(got, child.Name())
			}
		}
		slices.Sort(got)
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("%s integrations = %v, want %v", parentName, got, want)
		}
	}
}

func TestIntegrationGitSourcesAreCredentialFreeAndCanonical(t *testing.T) {
	t.Parallel()
	for input, want := range map[string]string{
		"ob-labs/powercontext-go":                        "https://github.com/ob-labs/powercontext-go.git",
		"https://github.com/ob-labs/powercontext-go":     "https://github.com/ob-labs/powercontext-go.git",
		"https://github.com/ob-labs/powercontext-go.git": "https://github.com/ob-labs/powercontext-go.git",
		"git@github.com:ob-labs/powercontext-go":         "git@github.com:ob-labs/powercontext-go.git",
	} {
		got, err := githubRepositoryCloneURL(input)
		if err != nil || got != want {
			t.Errorf("githubRepositoryCloneURL(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	const marker = "redacted-value"
	for _, input := range []string{
		"https://" + marker + "@github.com/ob-labs/powercontext-go",
		"https://github.com/ob-labs/powercontext-go?token=" + marker,
		"https://github.com/ob-labs/powercontext-go#" + marker,
		"ssh://git@github.com/ob-labs/powercontext-go",
	} {
		for name, normalize := range map[string]func(string) (string, error){
			"shared": githubRepositoryCloneURL,
			"dsh":    githubCloneURL,
		} {
			_, err := normalize(input)
			if err == nil || strings.Contains(err.Error(), marker) {
				t.Errorf("%s accepted or disclosed %q: %v", name, input, err)
			}
		}
	}
}

func TestClaudeCodeRejectsConflictingMarketplaceBeforeMutation(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(t.TempDir(), "claude"))
	commands := &scriptedSystemCommands{
		t: t, paths: map[string]string{"claude": "/resolved/bin/claude"},
		results: []systemCommandResult{{output: `[{"name":"powercontext","source":"github","repo":"other/powercontext","ref":"tested-ref"}]`}},
	}
	_, _, err := executeSystemCLI(t, nil, commands,
		"setup", "claude-code", "--source", "ob-labs/powercontext-go", "--ref", "tested-ref")
	if err == nil || !strings.Contains(err.Error(), "marketplace remove powercontext") || len(commands.calls) != 1 {
		t.Fatalf("error = %v, commands = %v", err, commands.calls)
	}
}

func TestClaudeCodeFailureRestoresPreexistingDisabledSettings(t *testing.T) {
	config := filepath.Join(t.TempDir(), "claude")
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	if err := os.MkdirAll(config, 0o700); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(config, "settings.json")
	previous := []byte(`{"enabledPlugins":{"powercontext@powercontext":false},"pluginConfigs":{"powercontext@powercontext":{"options":{"server_url":"http://127.0.0.1:7000","capture_prompts":false}}},"unrelated":{"preserved":true}}`)
	if err := os.WriteFile(settingsPath, previous, 0o600); err != nil {
		t.Fatal(err)
	}
	commands := &scriptedSystemCommands{
		t: t, paths: map[string]string{"claude": "/resolved/bin/claude"},
		results: []systemCommandResult{
			{output: `[{"name":"powercontext","source":"github","repo":"ob-labs/powercontext-go","ref":"main"}]`},
			{output: `[{"id":"powercontext@powercontext","version":"0.1.0","enabled":false}]`},
			{after: func(systemCommandCall) {
				if err := os.WriteFile(settingsPath, []byte(`{"enabledPlugins":{"powercontext@powercontext":true}}`), 0o600); err != nil {
					t.Fatal(err)
				}
			}},
			{output: `[]`},
		},
	}
	_, _, err := executeSystemCLI(t, nil, commands, "setup", "claude-code")
	if err == nil {
		t.Fatal("setup unexpectedly succeeded")
	}
	restored, readErr := os.ReadFile(settingsPath)
	if readErr != nil || string(restored) != string(previous) {
		t.Fatalf("restored settings = %s, error = %v", restored, readErr)
	}
	if got := commandCallStrings(commands.calls); len(got) != 4 || strings.Contains(strings.Join(got, "\n"), "plugin uninstall") {
		t.Fatalf("preexisting plugin was mutated during rollback: %v", got)
	}
}

func TestClaudeMarketplaceSourceAndRefCompatibilityMatchPython(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		source string
		ref    string
		want   string
	}{
		{source: "ob-labs/powercontext-go", ref: "release/v1", want: "ob-labs/powercontext-go@release/v1"},
		{source: "https://github.com/ob-labs/powercontext-go.git", ref: "release/v1", want: "https://github.com/ob-labs/powercontext-go.git#release/v1"},
	} {
		got, err := normalizeClaudeMarketplaceSource(test.source, test.ref)
		if err != nil || got != test.want {
			t.Errorf("normalizeClaudeMarketplaceSource(%q, %q) = %q, %v; want %q", test.source, test.ref, got, err, test.want)
		}
	}
	requested := "ob-labs/powercontext-go@release/v1"
	for _, test := range []struct {
		name     string
		existing map[string]any
		want     bool
	}{
		{name: "exact", existing: map[string]any{"source": "github", "repo": "ob-labs/powercontext-go", "ref": "release/v1"}, want: true},
		{name: "case insensitive repository", existing: map[string]any{"source": "github", "repo": "OB-LABS/POWERCONTEXT-GO", "ref": "release/v1"}, want: true},
		{name: "omitted ref is compatible", existing: map[string]any{"source": "github", "repo": "ob-labs/powercontext-go"}, want: true},
		{name: "different ref", existing: map[string]any{"source": "github", "repo": "ob-labs/powercontext-go", "ref": "main"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := claudeMarketplaceMatches(test.existing, requested); got != test.want {
				t.Fatalf("claudeMarketplaceMatches(%#v, %q) = %v, want %v", test.existing, requested, got, test.want)
			}
		})
	}
}

func TestHostDiagnosticsCoverUnavailableAndInactiveInstallations(t *testing.T) {
	t.Run("pi missing", func(t *testing.T) {
		checks := runPiDiagnostics(t.Context(), &scriptedSystemCommands{t: t})
		if checks["pi"].Status != "failed" || checks["package"].Status != "skipped" {
			t.Fatalf("checks = %#v", checks)
		}
	})
	t.Run("hermes unsupported", func(t *testing.T) {
		commands := &scriptedSystemCommands{
			t: t, paths: map[string]string{"hermes": "/resolved/bin/hermes"},
			results: []systemCommandResult{{output: "Hermes Agent v0.20.3\n"}},
		}
		checks := runHermesDiagnostics(t.Context(), commands)
		if checks["hermes"].Status != "failed" || checks["plugin"].Status != "skipped" {
			t.Fatalf("checks = %#v", checks)
		}
	})
	t.Run("openclaw inactive", func(t *testing.T) {
		commands := &scriptedSystemCommands{
			t: t, paths: map[string]string{"openclaw": "/resolved/bin/openclaw"},
			results: []systemCommandResult{{output: `{"plugins":[{"id":"memory-powercontext","enabled":true,"status":"loaded","memorySlotSelected":false}]}`}},
		}
		checks := runOpenClawDiagnostics(t.Context(), commands)
		if checks["openclaw"].Status != "ok" || checks["plugin"].Status != "failed" {
			t.Fatalf("checks = %#v", checks)
		}
	})
	t.Run("claude missing", func(t *testing.T) {
		checks := runClaudeCodeDiagnostics(t.Context(), &scriptedSystemCommands{t: t})
		if checks["claude_code"].Status != "failed" || checks["plugin"].Status != "skipped" {
			t.Fatalf("checks = %#v", checks)
		}
	})
	t.Run("claude disabled", func(t *testing.T) {
		commands := &scriptedSystemCommands{
			t: t, paths: map[string]string{"claude": "/resolved/bin/claude"},
			results: []systemCommandResult{{output: `[{"id":"powercontext@powercontext","enabled":false}]`}},
		}
		checks := runClaudeCodeDiagnostics(t.Context(), commands)
		if checks["claude_code"].Status != "ok" || checks["plugin"].Status != "failed" ||
			!strings.Contains(checks["plugin"].Detail, "enabled=false") {
			t.Fatalf("checks = %#v", checks)
		}
	})
}

func TestSetupOpenCodeRejectsUnsupportedVersionAndUnbuiltBundle(t *testing.T) {
	t.Run("unsupported version", func(t *testing.T) {
		commands := &scriptedSystemCommands{
			t: t, paths: map[string]string{"opencode": "/resolved/bin/opencode"},
			results: []systemCommandResult{{output: "1.18.20\n"}},
		}
		_, _, err := executeSystemCLI(t, nil, commands, "setup", "opencode")
		if err == nil || !strings.Contains(err.Error(), "unsupported") || len(commands.calls) != 1 {
			t.Fatalf("error = %v, commands = %v", err, commands.calls)
		}
	})
	t.Run("unbuilt bundle", func(t *testing.T) {
		checkout := filepath.Join(t.TempDir(), "checkout")
		plugin := writeOpenCodePlugin(t, checkout)
		if err := os.Remove(filepath.Join(plugin, "lib", "index.js")); err != nil {
			t.Fatal(err)
		}
		commands := &scriptedSystemCommands{
			t: t, paths: map[string]string{"opencode": "/resolved/bin/opencode"},
			results: []systemCommandResult{{output: "1.18.21\n"}},
		}
		_, _, err := executeSystemCLI(t, nil, commands, "setup", "opencode", "--source", checkout)
		if err == nil || !strings.Contains(err.Error(), "missing lib/index.js") || len(commands.calls) != 1 {
			t.Fatalf("error = %v, commands = %v", err, commands.calls)
		}
	})
}

func TestIntegrationCheckoutCloneFailurePreservesExistingCheckoutAndRedactsOutput(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	target := filepath.Join(parent, "current")
	writeTestFile(t, filepath.Join(target, "preserved.txt"), "old checkout\n")
	const marker = "redacted-value"
	commands := &scriptedSystemCommands{
		t: t,
		results: []systemCommandResult{{
			err: errors.New("fatal: https://" + marker + "@github.com/failed"),
		}},
	}
	_, err := refreshIntegrationCheckout(
		t.Context(), commands, "https://github.com/ob-labs/powercontext-go.git", "main", target,
		func(string) error { return nil },
	)
	if err == nil || strings.Contains(err.Error(), marker) {
		t.Fatalf("clone error = %v", err)
	}
	content, readErr := os.ReadFile(filepath.Join(target, "preserved.txt"))
	if readErr != nil || string(content) != "old checkout\n" {
		t.Fatalf("preserved checkout = %q, error = %v", content, readErr)
	}
	entries, readDirErr := os.ReadDir(parent)
	if readDirErr != nil {
		t.Fatal(readDirErr)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".current-") {
			t.Fatalf("staging path survived failed refresh: %s", entry.Name())
		}
	}
}

func TestOpenCodeRemoteCheckoutCacheIsSourceAndResolvedCommitScoped(t *testing.T) {
	t.Parallel()
	dataDirectory := t.TempDir()
	commits := []string{strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("a", 40)}
	results := make([]systemCommandResult, 0, len(commits)*2)
	for _, commit := range commits {
		results = append(results,
			systemCommandResult{after: func(call systemCommandCall) {
				writeOpenCodePlugin(t, call.arguments[len(call.arguments)-1])
			}},
			systemCommandResult{output: commit + "\n"},
		)
	}
	commands := &scriptedSystemCommands{t: t, results: results}
	first, err := materializeOpenCodeCheckout(t.Context(), commands, "owner-a/repo", "master", dataDirectory)
	if err != nil {
		t.Fatal(err)
	}
	refreshed, err := materializeOpenCodeCheckout(t.Context(), commands, "owner-a/repo", "master", dataDirectory)
	if err != nil {
		t.Fatal(err)
	}
	otherSource, err := materializeOpenCodeCheckout(t.Context(), commands, "owner-b/repo", "master", dataDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if first == refreshed || first == otherSource || filepath.Base(first) != commits[0] ||
		filepath.Base(refreshed) != commits[1] || filepath.Base(otherSource) != commits[2] {
		t.Fatalf("checkout identities = first %q, refreshed %q, other %q", first, refreshed, otherSource)
	}
	left, leftErr := normalizedGitHubIdentity("https://github.com/owner-a/repo.git")
	right, rightErr := normalizedGitHubIdentity("git@github.com:OWNER-A/REPO.git")
	if leftErr != nil || rightErr != nil || left != right {
		t.Fatalf("normalized identities = %q/%v and %q/%v", left, leftErr, right, rightErr)
	}
}

func TestOpenCodeRemoteCheckoutFailureLeavesPreviousCommit(t *testing.T) {
	t.Parallel()
	dataDirectory := t.TempDir()
	commit := strings.Repeat("a", 40)
	commands := &scriptedSystemCommands{
		t: t,
		results: []systemCommandResult{
			{after: func(call systemCommandCall) {
				writeOpenCodePlugin(t, call.arguments[len(call.arguments)-1])
			}},
			{output: commit + "\n"},
			{err: errors.New("simulated clone failure")},
		},
	}
	current, err := materializeOpenCodeCheckout(t.Context(), commands, "owner/repo", "master", dataDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if _, refreshErr := materializeOpenCodeCheckout(t.Context(), commands, "owner/repo", "master", dataDirectory); refreshErr == nil {
		t.Fatal("refresh unexpectedly succeeded")
	}
	if _, statErr := os.Stat(filepath.Join(current, filepath.FromSlash(openCodeRelative), "package.json")); statErr != nil {
		t.Fatalf("previous immutable checkout was lost: %v", statErr)
	}
	entries, err := os.ReadDir(filepath.Dir(current))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".checkout-") {
			t.Fatalf("staging path survived failed refresh: %s", entry.Name())
		}
	}
}

func TestOpenCodeSkillRefreshIsAtomicAndOwned(t *testing.T) {
	t.Parallel()
	first := filepath.Join(t.TempDir(), "first")
	second := filepath.Join(t.TempDir(), "second")
	writeTestFile(t, filepath.Join(first, "SKILL.md"), "first\n")
	writeTestFile(t, filepath.Join(second, "SKILL.md"), "second\n")
	target := filepath.Join(t.TempDir(), "skills", "project-context")
	if err := installOpenCodeSkill(first, target); err != nil {
		t.Fatal(err)
	}
	if err := installOpenCodeSkill(second, target); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(target, "SKILL.md"))
	if err != nil || string(content) != "second\n" || !ownedOpenCodeSkill(target) {
		t.Fatalf("refreshed skill = %q, owned = %v, error = %v", content, ownedOpenCodeSkill(target), err)
	}
	entries, err := os.ReadDir(filepath.Dir(target))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), "backup") || strings.HasPrefix(entry.Name(), ".project-context-") {
			t.Fatalf("temporary Skill path survived replacement: %s", entry.Name())
		}
	}
}

func TestOpenCodeDiagnosticsRequireActivationAndOwnedSkill(t *testing.T) {
	plugin := writeOpenCodePlugin(t, filepath.Join(t.TempDir(), "checkout"))
	config := filepath.Join(t.TempDir(), "config")
	skill := filepath.Join(config, "skills", "project-context")
	if err := installOpenCodeSkill(filepath.Join(plugin, "skills", "project-context"), skill); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name       string
		activate   bool
		wantStatus string
	}{
		{name: "active", activate: true, wantStatus: "ok"},
		{name: "configured but inactive", wantStatus: "failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			base := &scriptedSystemCommands{
				t: t, paths: map[string]string{"opencode": "/resolved/bin/opencode"},
				results: []systemCommandResult{
					{output: "1.18.21\n"},
					{output: "config     " + config + "\n"},
					{output: fmt.Sprintf(`{"plugin":[%q]}`, plugin)},
				},
			}
			commands := &environmentAwareCommands{scriptedSystemCommands: base}
			commands.runEnv = func(ctx context.Context, environment map[string]string, executable string, arguments ...string) ([]byte, error) {
				output, err := base.Run(ctx, executable, arguments...)
				if err == nil && test.activate {
					if writeErr := os.WriteFile(environment[openCodeProbePath], []byte(environment[openCodeProbeNonce]), 0o600); writeErr != nil {
						t.Fatal(writeErr)
					}
				}
				return output, err
			}
			checks := runOpenCodeDiagnostics(t.Context(), commands)
			if checks["plugin"].Status != test.wantStatus || checks["skill"].Status != "ok" {
				t.Fatalf("checks = %#v", checks)
			}
			if !test.activate && !strings.Contains(checks["plugin"].Detail, "did not activate") {
				t.Fatalf("inactive detail = %q", checks["plugin"].Detail)
			}
		})
	}
}

func TestSetupClaudeCodeIsTransactionalAndMergesSettings(t *testing.T) {
	config := filepath.Join(t.TempDir(), "claude")
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	if err := os.MkdirAll(config, 0o700); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(config, "settings.json")
	if err := os.WriteFile(settingsPath, []byte(`{"unrelated":{"preserved":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	settingsPath, err := resolvePath(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	commands := &scriptedSystemCommands{
		t: t, paths: map[string]string{"claude": "/usr/bin/claude"},
		results: []systemCommandResult{
			{output: `[]`},
			{output: `[]`},
			{},
			{},
			{output: `[{"id":"powercontext@powercontext","version":"0.1.0","enabled":true}]`},
		},
	}
	stdout, stderr, err := executeSystemCLI(t, nil, commands,
		"setup", "claude-code", "--source", "ob-labs/powercontext-go", "--ref", "tested-ref",
		"--server-url", "http://127.0.0.1:9000/mcp/", "--no-capture-prompts", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "no changes made yet") || !strings.Contains(stderr, settingsPath) {
		t.Fatalf("setup plan = %q", stderr)
	}
	payload := decodeSystemOutput(t, stdout)
	if payload["plugin_version"] != "0.1.0" || payload["settings_file"] != settingsPath {
		t.Fatalf("setup output = %#v", payload)
	}
	var settings map[string]any
	content, readErr := os.ReadFile(settingsPath)
	if readErr != nil || json.Unmarshal(content, &settings) != nil {
		t.Fatalf("settings = %q, error = %v", content, readErr)
	}
	if settings["unrelated"].(map[string]any)["preserved"] != true {
		t.Fatalf("unrelated settings were lost: %#v", settings)
	}
	options := settings["pluginConfigs"].(map[string]any)[claudePluginID].(map[string]any)["options"].(map[string]any)
	if options["server_url"] != "http://127.0.0.1:9000" || options["capture_prompts"] != false {
		t.Fatalf("plugin options = %#v", options)
	}
	wantCalls := []string{
		"/usr/bin/claude plugin marketplace list --json",
		"/usr/bin/claude plugin list --json",
		"/usr/bin/claude plugin marketplace add ob-labs/powercontext-go@tested-ref --scope user",
		"/usr/bin/claude plugin install powercontext@powercontext --scope user",
		"/usr/bin/claude plugin list --json",
	}
	if got := commandCallStrings(commands.calls); fmt.Sprint(got) != fmt.Sprint(wantCalls) {
		t.Fatalf("commands = %v, want %v", got, wantCalls)
	}
}

func TestSetupClaudeCodeRollsBackNewObjectsAfterVerificationFailure(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(t.TempDir(), "claude"))
	commands := &scriptedSystemCommands{
		t: t, paths: map[string]string{"claude": "/usr/bin/claude"},
		results: []systemCommandResult{{output: `[]`}, {output: `[]`}, {}, {}, {output: `[]`}, {}, {}},
	}
	_, _, err := executeSystemCLI(t, nil, commands, "setup", "claude-code")
	if err == nil || !strings.Contains(err.Error(), "enabled PowerContext plugin") {
		t.Fatalf("setup error = %v", err)
	}
	got := commandCallStrings(commands.calls)
	if !slices.Contains(got, "/usr/bin/claude plugin uninstall powercontext@powercontext --scope user") ||
		got[len(got)-1] != "/usr/bin/claude plugin marketplace remove powercontext" {
		t.Fatalf("rollback commands = %v", got)
	}
}

func TestSetupClaudeCodeRejectsUnsafeURLBeforeHostInspection(t *testing.T) {
	commands := &scriptedSystemCommands{t: t, paths: map[string]string{"claude": "/usr/bin/claude"}}
	_, _, err := executeSystemCLI(t, nil, commands,
		"setup", "claude-code", "--server-url", "http://memory.example.com")
	if err == nil || len(commands.calls) != 0 {
		t.Fatalf("error = %v, calls = %v", err, commands.calls)
	}
}

func TestSetupPiInstallsBeforeRemovingSupersededPackages(t *testing.T) {
	checkout := filepath.Join(t.TempDir(), "checkout")
	packagePath := writePiPackage(t, checkout)
	t.Setenv("POWERCONTEXT_HOME", filepath.Join(t.TempDir(), "data"))
	listing := "User packages:\n  " + packagePath + "\n    " + packagePath + "\n"
	commands := &scriptedSystemCommands{
		t: t, paths: map[string]string{"pi": "/usr/bin/pi"},
		results: []systemCommandResult{{}, {output: listing}, {output: listing}},
	}
	stdout, _, err := executeSystemCLI(t, nil, commands, "setup", "pi", "--source", checkout, "--json")
	if err != nil {
		t.Fatal(err)
	}
	if decodeSystemOutput(t, stdout)["package_path"] != packagePath {
		t.Fatalf("setup output = %s", stdout)
	}
	want := []string{"/usr/bin/pi install " + packagePath, "/usr/bin/pi list", "/usr/bin/pi list"}
	if got := commandCallStrings(commands.calls); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("commands = %v, want %v", got, want)
	}
}

func TestSetupPiRefreshesRemoteCheckoutAndRemovesOnlySupersededUserPackage(t *testing.T) {
	dataDirectory, err := resolvePath(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("POWERCONTEXT_HOME", dataDirectory)
	legacyPackage := writePiPackage(t, filepath.Join(t.TempDir(), "legacy"))
	commands := &scriptedSystemCommands{
		t: t, paths: map[string]string{"pi": "/usr/bin/pi"},
		results: []systemCommandResult{
			{after: func(call systemCommandCall) {
				root := call.arguments[len(call.arguments)-1]
				writePiPackage(t, root)
				writeTestFile(t, filepath.Join(root, "source.txt"), "another/powercontext@master\n")
			}},
			{},
			{output: "User packages:\n  " + legacyPackage + "\n    " + legacyPackage + "\n  current\n    placeholder\n"},
			{},
			{output: "User packages:\n  current\n    placeholder\n"},
		},
	}
	// The list payload needs the stable current path, which is known before the
	// clone executes even though its package contents are staged later.
	currentPackage := filepath.Join(dataDirectory, "checkouts", "pi", "current", filepath.FromSlash(piRelative))
	commands.results[2].output = strings.ReplaceAll(commands.results[2].output, "placeholder", currentPackage)
	commands.results[4].output = strings.ReplaceAll(commands.results[4].output, "placeholder", currentPackage)
	stdout, _, err := executeSystemCLI(t, nil, commands,
		"setup", "pi", "--source", "another/powercontext", "--ref", "master", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if decodeSystemOutput(t, stdout)["package_path"] != currentPackage {
		t.Fatalf("setup output = %s", stdout)
	}
	marker, err := os.ReadFile(filepath.Join(dataDirectory, "checkouts", "pi", "current", "source.txt"))
	if err != nil || string(marker) != "another/powercontext@master\n" {
		t.Fatalf("checkout marker = %q, error = %v", marker, err)
	}
	wantCalls := []string{
		"git clone --depth 1 --branch master https://github.com/another/powercontext.git",
		"/usr/bin/pi install " + currentPackage,
		"/usr/bin/pi list",
		"/usr/bin/pi remove " + legacyPackage,
		"/usr/bin/pi list",
	}
	got := commandCallStrings(commands.calls)
	if len(got) != len(wantCalls) || got[1] != wantCalls[1] || got[2] != wantCalls[2] || got[3] != wantCalls[3] || got[4] != wantCalls[4] {
		t.Fatalf("commands = %v", got)
	}
	if !strings.HasPrefix(got[0], "git clone --depth 1 --branch master https://github.com/another/powercontext.git ") {
		t.Fatalf("clone command = %q", got[0])
	}
}

func TestSetupPiInstallationFailurePreservesExistingPackage(t *testing.T) {
	checkout := filepath.Join(t.TempDir(), "replacement")
	writePiPackage(t, checkout)
	existing := writePiPackage(t, filepath.Join(t.TempDir(), "existing"))
	t.Setenv("POWERCONTEXT_HOME", filepath.Join(t.TempDir(), "data"))
	commands := &scriptedSystemCommands{
		t: t, paths: map[string]string{"pi": "/usr/bin/pi"},
		results: []systemCommandResult{{err: errors.New("simulated Pi installation failure")}},
	}
	_, _, err := executeSystemCLI(t, nil, commands, "setup", "pi", "--source", checkout)
	if err == nil || !strings.Contains(err.Error(), "simulated Pi installation failure") {
		t.Fatalf("setup error = %v", err)
	}
	if len(commands.calls) != 1 || commands.calls[0].arguments[0] != "install" {
		t.Fatalf("commands = %v", commands.calls)
	}
	if _, err := os.Stat(filepath.Join(existing, "package.json")); err != nil {
		t.Fatalf("existing package was disturbed: %v", err)
	}
}

func TestPiDiagnosticsReportInstalledPackageAsJSON(t *testing.T) {
	packagePath := writePiPackage(t, filepath.Join(t.TempDir(), "checkout"))
	listing := "User packages:\n  " + packagePath + "\n    " + packagePath + "\n"
	commands := &scriptedSystemCommands{
		t: t, paths: map[string]string{"pi": "/usr/bin/pi"},
		results: []systemCommandResult{{output: listing}},
	}
	stdout, _, err := executeSystemCLI(t, nil, commands, "doctor", "pi", "--json")
	if err != nil {
		t.Fatal(err)
	}
	payload := decodeSystemOutput(t, stdout)
	packageCheck := payload["checks"].(map[string]any)["package"].(map[string]any)
	if payload["ok"] != true || packageCheck["status"] != "ok" || packageCheck["detail"] != piPackageName+" is installed" {
		t.Fatalf("diagnostics = %#v", payload)
	}
}

func TestSetupOpenCodeProtectsAndPublishesOwnedSkill(t *testing.T) {
	checkout := filepath.Join(t.TempDir(), "checkout")
	plugin := writeOpenCodePlugin(t, checkout)
	config := filepath.Join(t.TempDir(), "config")
	t.Setenv("POWERCONTEXT_HOME", filepath.Join(t.TempDir(), "data"))
	commands := &scriptedSystemCommands{
		t: t, paths: map[string]string{"opencode": "/usr/bin/opencode"},
		results: []systemCommandResult{{output: "1.18.21\n"}, {output: "config     " + config + "\n"}, {}},
	}
	if _, _, err := executeSystemCLI(t, nil, commands, "setup", "opencode", "--source", checkout); err != nil {
		t.Fatal(err)
	}
	skill := filepath.Join(config, "skills", "project-context")
	if !ownedOpenCodeSkill(skill) {
		t.Fatalf("skill %q is not marked as PowerContext-owned", skill)
	}
	content, err := os.ReadFile(filepath.Join(skill, "SKILL.md"))
	if err != nil || string(content) != "project context\n" {
		t.Fatalf("skill content = %q, error = %v", content, err)
	}
	if got := commands.calls[2].String(); got != "/usr/bin/opencode plugin "+plugin+" --global --force" {
		t.Fatalf("install command = %q", got)
	}
}

func TestSetupOpenCodeRefusesUnownedSkillBeforePluginMutation(t *testing.T) {
	checkout := filepath.Join(t.TempDir(), "checkout")
	writeOpenCodePlugin(t, checkout)
	config := filepath.Join(t.TempDir(), "config")
	target := filepath.Join(config, "skills", "project-context")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "SKILL.md"), []byte("user owned\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("POWERCONTEXT_HOME", filepath.Join(t.TempDir(), "data"))
	commands := &scriptedSystemCommands{
		t: t, paths: map[string]string{"opencode": "/usr/bin/opencode"},
		results: []systemCommandResult{{output: "1.18.21\n"}, {output: "config " + config + "\n"}},
	}
	_, _, err := executeSystemCLI(t, nil, commands, "setup", "opencode", "--source", checkout)
	if err == nil || !strings.Contains(err.Error(), "not owned by PowerContext") || len(commands.calls) != 2 {
		t.Fatalf("error = %v, commands = %v", err, commands.calls)
	}
}

func TestSetupHermesStagesDoctorAndAtomicallyReplacesPlugin(t *testing.T) {
	checkout := filepath.Join(t.TempDir(), "checkout")
	writeHermesPlugin(t, checkout)
	home := filepath.Join(t.TempDir(), "hermes")
	target := filepath.Join(home, "plugins", hermesPluginName)
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "stale.py"), []byte("stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	home, err := resolvePath(home)
	if err != nil {
		t.Fatal(err)
	}
	target = filepath.Join(home, "plugins", hermesPluginName)
	t.Setenv("HERMES_HOME", home)
	t.Setenv("POWERCONTEXT_HOME", filepath.Join(t.TempDir(), "data"))
	commands := &scriptedSystemCommands{
		t: t, paths: map[string]string{"hermes": "/usr/bin/hermes"},
		results: []systemCommandResult{
			{output: "Hermes Agent v0.20.4\n"}, {}, {output: "Hermes Agent v0.20.4\n"}, {},
		},
	}
	stdout, _, err := executeSystemCLI(t, nil, commands, "setup", "hermes", "--source", checkout, "--json")
	if err != nil {
		t.Fatal(err)
	}
	if decodeSystemOutput(t, stdout)["plugin_path"] != target {
		t.Fatalf("setup output = %s", stdout)
	}
	if _, err := os.Stat(filepath.Join(target, "stale.py")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale plugin content survived replacement: %v", err)
	}
	if _, ok := findHermesPlugin(target); !ok {
		t.Fatal("installed Hermes plugin is incomplete")
	}
}

func TestSetupHermesReportsMissingCLIWithoutMutation(t *testing.T) {
	t.Setenv("POWERCONTEXT_HOME", filepath.Join(t.TempDir(), "data"))
	commands := &scriptedSystemCommands{t: t}
	_, _, err := executeSystemCLI(t, nil, commands, "setup", "hermes")
	if err == nil || !strings.Contains(err.Error(), "Hermes CLI is not installed") || len(commands.calls) != 0 {
		t.Fatalf("error = %v, commands = %v", err, commands.calls)
	}
}

func TestHermesRemoteCheckoutUsesRequestedRefAndSourceScopedCurrent(t *testing.T) {
	t.Parallel()
	dataDirectory := t.TempDir()
	markers := []string{"first\n", "refreshed\n", "other source\n"}
	commands := &scriptedSystemCommands{t: t}
	for _, marker := range markers {
		commands.results = append(commands.results, systemCommandResult{after: func(call systemCommandCall) {
			root := call.arguments[len(call.arguments)-1]
			writeHermesPlugin(t, root)
			writeTestFile(t, filepath.Join(root, "source.txt"), marker)
		}})
	}
	first, err := resolveHermesPlugin(t.Context(), commands, "owner-a/repo", "feature/ref", dataDirectory)
	if err != nil {
		t.Fatal(err)
	}
	refreshed, err := resolveHermesPlugin(t.Context(), commands, "owner-a/repo", "feature/ref", dataDirectory)
	if err != nil {
		t.Fatal(err)
	}
	otherSource, err := resolveHermesPlugin(t.Context(), commands, "owner-b/repo", "feature/ref", dataDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if first != refreshed || first == otherSource {
		t.Fatalf("checkout paths = first %q, refreshed %q, other %q", first, refreshed, otherSource)
	}
	checkoutRoot := strings.TrimSuffix(first, filepath.FromSlash(hermesRelative))
	content, err := os.ReadFile(filepath.Join(checkoutRoot, "source.txt"))
	if err != nil || string(content) != markers[1] {
		t.Fatalf("refreshed marker = %q, error = %v", content, err)
	}
	for _, call := range commands.calls {
		joined := call.String()
		if !strings.Contains(joined, "--branch feature/ref") {
			t.Fatalf("clone did not use requested ref: %q", joined)
		}
	}
}

func TestHermesDiagnosticsCoverInstalledMissingBrokenAndUnsupportedProviders(t *testing.T) {
	t.Run("installed", func(t *testing.T) {
		home := filepath.Join(t.TempDir(), "hermes")
		t.Setenv("HERMES_HOME", home)
		plugin := filepath.Join(home, "plugins", hermesPluginName)
		writeTestFile(t, filepath.Join(plugin, "__init__.py"), "def register(): pass\n")
		writeTestFile(t, filepath.Join(plugin, "plugin.yaml"), "name: powercontext\n")
		commands := &scriptedSystemCommands{
			t: t, paths: map[string]string{"hermes": "/resolved/bin/hermes"},
			results: []systemCommandResult{{output: "Hermes Agent v0.20.4\n"}, {}},
		}
		checks := runHermesDiagnostics(t.Context(), commands)
		if checks["hermes"].Status != "ok" || checks["plugin"].Status != "ok" {
			t.Fatalf("checks = %#v", checks)
		}
	})
	t.Run("missing", func(t *testing.T) {
		t.Setenv("HERMES_HOME", filepath.Join(t.TempDir(), "hermes"))
		commands := &scriptedSystemCommands{
			t: t, paths: map[string]string{"hermes": "/resolved/bin/hermes"},
			results: []systemCommandResult{{output: "Hermes Agent v0.20.4\n"}},
		}
		checks := runHermesDiagnostics(t.Context(), commands)
		if checks["hermes"].Status != "ok" || checks["plugin"].Status != "failed" ||
			!strings.Contains(checks["plugin"].Detail, "not installed") {
			t.Fatalf("checks = %#v", checks)
		}
	})
	t.Run("broken", func(t *testing.T) {
		home := filepath.Join(t.TempDir(), "hermes")
		t.Setenv("HERMES_HOME", home)
		plugin := filepath.Join(home, "plugins", hermesPluginName)
		writeTestFile(t, filepath.Join(plugin, "__init__.py"), "def register(): pass\n")
		writeTestFile(t, filepath.Join(plugin, "plugin.yaml"), "name: powercontext\n")
		commands := &scriptedSystemCommands{
			t: t, paths: map[string]string{"hermes": "/resolved/bin/hermes"},
			results: []systemCommandResult{{output: "Hermes Agent v0.20.4\n"}, {err: errors.New("provider doctor failed")}},
		}
		checks := runHermesDiagnostics(t.Context(), commands)
		if checks["hermes"].Status != "ok" || checks["plugin"].Status != "failed" ||
			!strings.Contains(checks["plugin"].Detail, "doctor failed") {
			t.Fatalf("checks = %#v", checks)
		}
	})
	t.Run("unsupported", func(t *testing.T) {
		commands := &scriptedSystemCommands{
			t: t, paths: map[string]string{"hermes": "/resolved/bin/hermes"},
			results: []systemCommandResult{{output: "Hermes Agent v0.20.3\n"}},
		}
		checks := runHermesDiagnostics(t.Context(), commands)
		if checks["hermes"].Status != "failed" || checks["plugin"].Status != "skipped" {
			t.Fatalf("checks = %#v", checks)
		}
	})
}

func TestSetupOpenClawBuildsAndPreservesToolAllowlist(t *testing.T) {
	checkout := filepath.Join(t.TempDir(), "checkout")
	plugin := writeOpenClawPlugin(t, checkout)
	t.Setenv("POWERCONTEXT_HOME", filepath.Join(t.TempDir(), "data"))
	commands := &scriptedSystemCommands{
		t: t, paths: map[string]string{"openclaw": "/usr/bin/openclaw", "pnpm": "/usr/bin/pnpm"},
		results: []systemCommandResult{
			{output: "OpenClaw 2026.8.1-beta.2\n"},
			{},
			{after: func(systemCommandCall) {
				bundle := filepath.Join(plugin, "dist", "index.js")
				if err := os.MkdirAll(filepath.Dir(bundle), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(bundle, []byte("export default {}\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}},
			{},
			{err: errors.New("missing gateway mode")},
			{},
			{output: `["custom_tool","powercontext_memory_get"]`},
			{},
			{},
		},
	}
	stdout, _, err := executeSystemCLI(t, nil, commands,
		"setup", "openclaw", "--source", checkout, "--server-url", "http://127.0.0.1:8765/", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if decodeSystemOutput(t, stdout)["server_url"] != "http://127.0.0.1:8765" {
		t.Fatalf("setup output = %s", stdout)
	}
	var allowlist []string
	call := commands.calls[7]
	if call.arguments[0] != "config" || call.arguments[1] != "set" || json.Unmarshal([]byte(call.arguments[3]), &allowlist) != nil {
		t.Fatalf("allowlist command = %v", call)
	}
	wantTools := []string{"custom_tool", "powercontext_memory_get"}
	for _, tool := range openClawTools {
		if !slices.Contains(wantTools, tool) {
			wantTools = append(wantTools, tool)
		}
	}
	if fmt.Sprint(allowlist) != fmt.Sprint(wantTools) {
		t.Fatalf("allowlist = %v, want %v", allowlist, wantTools)
	}
	if commands.calls[len(commands.calls)-1].String() != "/usr/bin/openclaw gateway restart" {
		t.Fatalf("last command = %v", commands.calls[len(commands.calls)-1])
	}
}

func TestOpenClawSetupFlagsPreservePythonDefaults(t *testing.T) {
	t.Parallel()
	command := newSetupOpenClawCommand(&commandState{system: &scriptedSystemCommands{t: t}})
	for name, want := range map[string]string{
		"source": defaultMarketplaceSource,
		"ref":    defaultMarketplaceRef, "server-url": defaultServerURL, "scope-mode": "agent",
	} {
		flag := command.Flags().Lookup(name)
		if flag == nil || flag.DefValue != want {
			t.Errorf("--%s default = %v, want %q", name, flag, want)
		}
	}
}

func TestDoctorOpenClawRequiresActiveSelectedMemoryPlugin(t *testing.T) {
	commands := &scriptedSystemCommands{
		t: t, paths: map[string]string{"openclaw": "/usr/bin/openclaw"},
		results: []systemCommandResult{{output: `{"plugins":[{"id":"memory-powercontext","enabled":true,"status":"loaded","memorySlotSelected":false}]}`}},
	}
	stdout, _, err := executeSystemCLI(t, nil, commands, "doctor", "openclaw", "--json")
	if err == nil {
		t.Fatal("doctor unexpectedly accepted an unselected memory plugin")
	}
	plugin := decodeSystemOutput(t, stdout)["checks"].(map[string]any)["plugin"].(map[string]any)
	if plugin["status"] != "failed" {
		t.Fatalf("plugin diagnostic = %#v", plugin)
	}
}

func TestOpenClawDiagnosticsCoverInstalledMissingInactiveAndUnavailablePlugin(t *testing.T) {
	for _, test := range []struct {
		name       string
		paths      map[string]string
		output     string
		wantHost   string
		wantPlugin string
	}{
		{
			name: "installed", paths: map[string]string{"openclaw": "/resolved/bin/openclaw"},
			output:   `{"plugins":[{"id":"memory-powercontext","enabled":true,"status":"loaded","memorySlotSelected":true}]}`,
			wantHost: "ok", wantPlugin: "ok",
		},
		{
			name: "missing", paths: map[string]string{"openclaw": "/resolved/bin/openclaw"},
			output: `{"plugins":[]}`, wantHost: "ok", wantPlugin: "failed",
		},
		{
			name: "inactive", paths: map[string]string{"openclaw": "/resolved/bin/openclaw"},
			output:   `{"plugins":[{"id":"memory-powercontext","enabled":true,"status":"loaded","memorySlotSelected":false}]}`,
			wantHost: "ok", wantPlugin: "failed",
		},
		{name: "unavailable", wantHost: "failed", wantPlugin: "skipped"},
	} {
		t.Run(test.name, func(t *testing.T) {
			commands := &scriptedSystemCommands{t: t, paths: test.paths}
			if test.output != "" {
				commands.results = []systemCommandResult{{output: test.output}}
			}
			checks := runOpenClawDiagnostics(t.Context(), commands)
			if checks["openclaw"].Status != test.wantHost || checks["plugin"].Status != test.wantPlugin {
				t.Fatalf("checks = %#v", checks)
			}
		})
	}
}

func TestOpenClawServerURLNormalizationAndRemoteRefValidationMatchPython(t *testing.T) {
	t.Parallel()
	for input, want := range map[string]string{
		"http://127.0.0.1:8765/":         "http://127.0.0.1:8765",
		"https://memory.example/path///": "https://memory.example/path",
	} {
		got, err := normalizeOpenClawServerURL(input)
		if err != nil || got != want {
			t.Errorf("normalizeOpenClawServerURL(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	for _, input := range []string{
		"", "file:///tmp/socket", "http://user:secret@127.0.0.1", "http://127.0.0.1?q=1", "http://127.0.0.1#fragment",
	} {
		if _, err := normalizeOpenClawServerURL(input); err == nil {
			t.Errorf("normalizeOpenClawServerURL(%q) unexpectedly succeeded", input)
		}
	}
	for _, ref := range []string{"", ".", "..", "bad\x00ref"} {
		if err := validateRemoteRef(ref); err == nil {
			t.Errorf("validateRemoteRef(%q) unexpectedly succeeded", ref)
		}
	}
}

func writePiPackage(t *testing.T, root string) string {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(piRelative))
	writeTestFile(t, filepath.Join(path, "package.json"), `{"name":"powercontext-pi"}`)
	writeTestFile(t, filepath.Join(path, "extensions", "powercontext.ts"), "export default () => {}\n")
	writeTestFile(t, filepath.Join(path, "skills", "project-context", "SKILL.md"), "project context\n")
	resolved, err := resolvePath(path)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func writeOpenCodePlugin(t *testing.T, root string) string {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(openCodeRelative))
	writeTestFile(t, filepath.Join(path, "package.json"), `{"name":"powercontext-opencode"}`)
	writeTestFile(t, filepath.Join(path, "lib", "index.js"), "export default {}\n")
	writeTestFile(t, filepath.Join(path, "skills", "project-context", "SKILL.md"), "project context\n")
	resolved, err := resolvePath(path)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func writeHermesPlugin(t *testing.T, root string) string {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(hermesRelative))
	writeTestFile(t, filepath.Join(path, "__init__.py"), "def register(): pass\n")
	writeTestFile(t, filepath.Join(path, "plugin.yaml"), "name: powercontext\n")
	resolved, err := resolvePath(path)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func writeOpenClawPlugin(t *testing.T, root string) string {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(openClawRelative))
	writeTestFile(t, filepath.Join(path, "package.json"), `{"name":"@oceanbase/openclaw-memory-powercontext"}`)
	resolved, err := resolvePath(path)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

type environmentAwareCommands struct {
	*scriptedSystemCommands
	runEnv func(context.Context, map[string]string, string, ...string) ([]byte, error)
}

func (e *environmentAwareCommands) RunEnv(
	ctx context.Context,
	environment map[string]string,
	executable string,
	arguments ...string,
) ([]byte, error) {
	return e.runEnv(ctx, environment, executable, arguments...)
}
