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
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSetupAndDoctorExposeCurrentHostMatrix(t *testing.T) {
	command := newCommandWithAllDependencies(
		VersionInfo{Version: "test"}, &strings.Builder{}, &strings.Builder{}, nil, nil, &scriptedSystemCommands{t: t},
	)
	for parentName, want := range map[string][]string{
		"setup":  {"claude-code", "codex", "dsh", "hermes", "openclaw", "opencode", "pi", "select", "workbuddy"},
		"doctor": {"claude-code", "codex", "dsh", "hermes", "integrations", "openclaw", "opencode", "pi", "workbuddy"},
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

func TestUnsupportedSetupAndDoctorCommandsRefuseBeforeSideEffects(t *testing.T) {
	for _, host := range []string{"claude-code", "dsh", "hermes", "openclaw", "opencode", "pi"} {
		for _, parent := range []string{"setup", "doctor"} {
			t.Run(parent+"/"+host, func(t *testing.T) {
				dataDir := filepath.Join(t.TempDir(), "powercontext-data")
				t.Setenv("POWERCONTEXT_HOME", dataDir)
				commands := &scriptedSystemCommands{t: t}

				_, _, err := executeSystemCLI(t, nil, commands, parent, host)
				var usage *UsageError
				var unsupported *UnsupportedIntegrationError
				if !errors.As(err, &usage) || !errors.As(err, &unsupported) || ExitCode(err) != 2 {
					t.Fatalf("%s %s error = %T %v, want typed usage refusal", parent, host, err, err)
				}
				if !strings.Contains(err.Error(), "only Codex and WorkBuddy are supported") || strings.Contains(err.Error(), host) {
					t.Fatalf("unsupported integration error is not stable and redacted: %v", err)
				}
				if len(commands.lookups) != 0 || len(commands.calls) != 0 {
					t.Fatalf("%s %s reached external commands: lookups=%v calls=%v", parent, host, commands.lookups, commands.calls)
				}
				if _, statErr := os.Stat(dataDir); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("%s %s created the data directory before refusal: %v", parent, host, statErr)
				}
			})
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
			commands.runEnv = func(_ context.Context, environment map[string]string, _ string, _ ...string) ([]byte, error) {
				if test.activate {
					if writeErr := os.WriteFile(environment[openCodeProbePath], []byte(environment[openCodeProbeNonce]), 0o600); writeErr != nil {
						t.Fatal(writeErr)
					}
				}
				return nil, nil
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

func TestOpenCodeDiagnosticsAcceptsAnOwnedAutoloadBundle(t *testing.T) {
	plugin := writeOpenCodePlugin(t, filepath.Join(t.TempDir(), "checkout"))
	config := filepath.Join(t.TempDir(), "config")
	bundle := filepath.Join(config, "plugins", openCodePluginName+".js")
	if err := installOpenCodePlugin(filepath.Join(plugin, "lib", "index.js"), bundle); err != nil {
		t.Fatal(err)
	}
	skill := filepath.Join(config, "skills", "project-context")
	if err := installOpenCodeSkill(filepath.Join(plugin, "skills", "project-context"), skill); err != nil {
		t.Fatal(err)
	}
	base := &scriptedSystemCommands{
		t: t, paths: map[string]string{"opencode": "/resolved/bin/opencode"},
		results: []systemCommandResult{
			{output: "1.18.21\n"},
			{output: "config     " + config + "\n"},
			{output: `{}`},
		},
	}
	commands := &environmentAwareCommands{scriptedSystemCommands: base}
	commands.runEnv = func(_ context.Context, environment map[string]string, _ string, _ ...string) ([]byte, error) {
		return nil, os.WriteFile(environment[openCodeProbePath], []byte(environment[openCodeProbeNonce]), 0o600)
	}

	checks := runOpenCodeDiagnostics(t.Context(), commands)
	if checks["plugin"].Status != "ok" || checks["skill"].Status != "ok" {
		t.Fatalf("diagnostics = %#v", checks)
	}
}

func TestOpenCodeActivationProbeUsesHeadlessServerWithoutModel(t *testing.T) {
	plugin := writeOpenCodePlugin(t, filepath.Join(t.TempDir(), "checkout"))
	commands := &scriptedSystemCommands{
		t:       t,
		results: []systemCommandResult{{output: fmt.Sprintf(`{"plugin":[%q]}`, plugin)}},
	}
	var observed []string
	runner := func(_ context.Context, command []string, environment map[string]string, timeout time.Duration) error {
		observed = slices.Clone(command)
		if timeout <= 0 {
			t.Fatalf("probe timeout = %s", timeout)
		}
		return os.WriteFile(environment[openCodeProbePath], []byte(environment[openCodeProbeNonce]), 0o600)
	}

	configured, activated, err := probeOpenCodeActivationWithRunner(
		t.Context(), commands, "/usr/bin/opencode", runner,
	)
	if err != nil || !configured || !activated {
		t.Fatalf("configured = %v, activated = %v, error = %v", configured, activated, err)
	}
	if len(observed) != 6 || observed[0] != "/usr/bin/opencode" || observed[1] != "serve" ||
		observed[2] != "--hostname" || observed[3] != "127.0.0.1" || observed[4] != "--port" {
		t.Fatalf("probe command = %v", observed)
	}
	if _, portErr := strconv.Atoi(observed[5]); portErr != nil {
		t.Fatalf("probe port = %q", observed[5])
	}
	if slices.Contains(observed, "--model") {
		t.Fatalf("probe command invokes a model: %v", observed)
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

func TestInterruptedOpenCodePluginInstallRecoversOnRetry(t *testing.T) {
	source := filepath.Join(t.TempDir(), "index.js")
	writeTestFile(t, source, "export default {}\n")
	target := filepath.Join(t.TempDir(), "config", "plugins", openCodePluginName+".js")
	replacements := 0
	interruptedRename := func(oldPath, newPath string) error {
		replacements++
		if replacements == 2 {
			return errors.New("interrupted")
		}
		return os.Rename(oldPath, newPath)
	}

	err := installOpenCodePluginWithRename(source, target, interruptedRename)
	if err == nil {
		t.Fatal("interrupted install succeeded")
	}
	manifest := filepath.Join(filepath.Dir(target), openCodePluginOwner)
	if !ownedOpenCodePlugin(target) {
		t.Fatalf("ownership manifest %q was not published before the interrupted bundle replacement", manifest)
	}
	if _, statErr := os.Stat(target); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("plugin target survived interrupted first install: %v", statErr)
	}
	entries, readErr := os.ReadDir(filepath.Dir(target))
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") {
			t.Fatalf("temporary plugin path survived interruption: %s", entry.Name())
		}
	}

	if retryErr := installOpenCodePlugin(source, target); retryErr != nil {
		t.Fatal(retryErr)
	}
	content, readErr := os.ReadFile(target)
	if readErr != nil || string(content) != "export default {}\n" || !ownedOpenCodePlugin(target) {
		t.Fatalf("repaired plugin content = %q, owned = %v, error = %v", content, ownedOpenCodePlugin(target), readErr)
	}
}

func TestHermesPluginPairRestoresBothTargetsWhenSecondActivationFails(t *testing.T) {
	checkout := filepath.Join(t.TempDir(), "checkout")
	writeHermesPlugin(t, checkout)
	home := filepath.Join(t.TempDir(), "hermes")
	provider := filepath.Join(home, "plugins", hermesPluginName)
	command := filepath.Join(home, "plugins", hermesCommandPluginName)
	for path, marker := range map[string]string{provider: "old provider\n", command: "old command\n"} {
		writeTestFile(t, filepath.Join(path, "__init__.py"), "def register(): pass\n")
		writeTestFile(t, filepath.Join(path, "plugin.yaml"), "name: test\n")
		writeTestFile(t, filepath.Join(path, "version.txt"), marker)
	}
	writeTestFile(t, filepath.Join(home, "plugins", "other", "keep.txt"), "keep\n")
	sources, ok := findHermesPluginPair(checkout)
	if !ok {
		t.Fatal("source pair is missing")
	}
	original := hermesRename
	t.Cleanup(func() { hermesRename = original })
	failed := false
	hermesRename = func(source, target string) error {
		if !failed && target == command && strings.HasPrefix(filepath.Base(source), ".powercontext-") {
			failed = true
			return errors.New("injected second activation failure")
		}
		return original(source, target)
	}
	err := installHermesPlugins(t.Context(), &scriptedSystemCommands{t: t, results: []systemCommandResult{{}, {}}}, "/usr/bin/hermes", sources, map[string]string{hermesPluginName: provider, hermesCommandPluginName: command})
	if err == nil || !failed {
		t.Fatalf("install error = %v, failed = %t", err, failed)
	}
	for path, want := range map[string]string{provider: "old provider\n", command: "old command\n"} {
		got, readErr := os.ReadFile(filepath.Join(path, "version.txt"))
		if readErr != nil || string(got) != want {
			t.Fatalf("restored %s = %q, %v", path, got, readErr)
		}
	}
	if got, err := os.ReadFile(filepath.Join(home, "plugins", "other", "keep.txt")); err != nil || string(got) != "keep\n" {
		t.Fatalf("unrelated plugin = %q, %v", got, err)
	}
}

func TestHermesPluginPairRestoresBothTargetsWhenEnableFails(t *testing.T) {
	checkout := filepath.Join(t.TempDir(), "checkout")
	writeHermesPlugin(t, checkout)
	home := filepath.Join(t.TempDir(), "hermes")
	provider := filepath.Join(home, "plugins", hermesPluginName)
	command := filepath.Join(home, "plugins", hermesCommandPluginName)
	for path, marker := range map[string]string{provider: "old provider\n", command: "old command\n"} {
		writeTestFile(t, filepath.Join(path, "__init__.py"), "def register(): pass\n")
		writeTestFile(t, filepath.Join(path, "plugin.yaml"), "name: test\n")
		writeTestFile(t, filepath.Join(path, "version.txt"), marker)
	}
	writeTestFile(t, filepath.Join(home, "plugins", "other", "keep.txt"), "keep\n")
	sources, ok := findHermesPluginPair(checkout)
	if !ok {
		t.Fatal("source pair is missing")
	}
	commands := &scriptedSystemCommands{t: t, results: []systemCommandResult{{}, {}, {err: errors.New("enable failed")}}}
	err := installHermesPlugins(t.Context(), commands, "/usr/bin/hermes", sources, map[string]string{hermesPluginName: provider, hermesCommandPluginName: command})
	if err == nil {
		t.Fatal("enable failure unexpectedly succeeded")
	}
	for path, want := range map[string]string{provider: "old provider\n", command: "old command\n"} {
		got, readErr := os.ReadFile(filepath.Join(path, "version.txt"))
		if readErr != nil || string(got) != want {
			t.Fatalf("restored %s = %q, %v", path, got, readErr)
		}
	}
	if got, err := os.ReadFile(filepath.Join(home, "plugins", "other", "keep.txt")); err != nil || string(got) != "keep\n" {
		t.Fatalf("unrelated plugin = %q, %v", got, err)
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
		commandPlugin := filepath.Join(home, "plugins", hermesCommandPluginName)
		writeTestFile(t, filepath.Join(commandPlugin, "__init__.py"), "def register(context): pass\n")
		writeTestFile(t, filepath.Join(commandPlugin, "plugin.yaml"), "name: powercontext-command\nkind: standalone\n")
		commands := &scriptedSystemCommands{
			t: t, paths: map[string]string{"hermes": "/resolved/bin/hermes"},
			results: []systemCommandResult{{output: "Hermes Agent v0.20.4\n"}, {}, {}},
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
		commandPlugin := filepath.Join(home, "plugins", hermesCommandPluginName)
		writeTestFile(t, filepath.Join(commandPlugin, "__init__.py"), "def register(context): pass\n")
		writeTestFile(t, filepath.Join(commandPlugin, "plugin.yaml"), "name: powercontext-command\nkind: standalone\n")
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
	command := filepath.Join(root, filepath.FromSlash(hermesCommandRelative))
	writeTestFile(t, filepath.Join(command, "__init__.py"), "def register(context): pass\n")
	writeTestFile(t, filepath.Join(command, "plugin.yaml"), "name: powercontext-command\nkind: standalone\n")
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

func (e *environmentAwareCommands) runOpenCodeProbe(
	ctx context.Context,
	command []string,
	environment map[string]string,
	_ time.Duration,
) error {
	_, err := e.runEnv(ctx, environment, command[0], command[1:]...)
	return err
}
