// Copyright (c) 2026 OceanBase.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"bytes"
	"encoding/json/v2"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestReleaseArchiveProvidesConsumableCommandHosts(t *testing.T) {
	t.Run("checkout-substitution-mutant", func(t *testing.T) {
		checkoutRoot, err := filepath.Abs(filepath.Clean(filepath.Join("..", "..")))
		if err != nil {
			t.Fatal(err)
		}
		mutant := []string{"codex|plugin marketplace add " + checkoutRoot + " --json"}
		if !commandLogUsesPath(mutant, checkoutRoot) {
			t.Fatalf("checkout substitution mutant was not detected: %q", mutant)
		}
	})

	if runtime.GOOS != "linux" {
		t.Skip("release command-host consumption is verified against the Linux release artifact")
	}

	repository := filepath.Clean(filepath.Join("..", ".."))
	checkoutRoot, err := filepath.Abs(repository)
	if err != nil {
		t.Fatal(err)
	}
	releaseRoot := unpackReleaseArchive(t, buildAdapterConsumerArchive(t, repository))
	binary := filepath.Join(releaseRoot, "bin", "powercontext")

	t.Run("codex", func(t *testing.T) {
		commands := setupReleaseCommandHost(t, releaseRoot, "codex")
		assertReleaseCommandLog(t, commands,
			"codex|plugin marketplace add "+releaseRoot+" --json",
			"codex|plugin add powercontext@powercontext --json",
			"codex|plugin list --json",
		)
		assertNoCheckoutPath(t, commands, checkoutRoot)
	})

	t.Run("claude-code", func(t *testing.T) {
		commands := setupReleaseCommandHost(t, releaseRoot, "claude-code")
		assertReleaseCommandLog(t, commands,
			"claude|plugin marketplace list --json",
			"claude|plugin list --json",
			"claude|plugin marketplace add "+releaseRoot+" --scope user",
			"claude|plugin install powercontext@powercontext --scope user",
			"claude|plugin list --json",
		)
		assertNoCheckoutPath(t, commands, checkoutRoot)
	})

	t.Run("dsh", func(t *testing.T) {
		commands := setupReleaseCommandHost(t, releaseRoot, "dsh")
		plugin := filepath.Join(releaseRoot, "integrations", "dsh", "plugins", "powercontext")
		assertReleaseCommandLog(t, commands,
			"dsh|plugin --profile web add "+plugin,
			"dsh|--profile web --dump-config",
		)
		assertNoCheckoutPath(t, commands, checkoutRoot)
	})

	t.Run("hermes", func(t *testing.T) {
		home := filepath.Join(t.TempDir(), "hermes")
		commands := setupReleaseCommandHost(t, releaseRoot, "hermes", "HERMES_HOME", home)
		assertCommandLogContains(t, commands,
			"hermes|--version",
			"hermes|plugins enable powercontext-command",
			"hermes|plugins doctor --ci "+filepath.Join(home, "plugins", "powercontext"),
			"hermes|plugins doctor --ci "+filepath.Join(home, "plugins", "powercontext-command"),
		)
		assertArchiveFileIdentity(t,
			filepath.Join(releaseRoot, "integrations", "hermes", "plugins", "powercontext", "plugin.yaml"),
			filepath.Join(home, "plugins", "powercontext", "plugin.yaml"),
		)
		assertArchiveFileIdentity(t,
			filepath.Join(releaseRoot, "integrations", "hermes", "plugins", "powercontext-command", "plugin.yaml"),
			filepath.Join(home, "plugins", "powercontext-command", "plugin.yaml"),
		)
		assertNoCheckoutPath(t, commands, checkoutRoot)
	})

	t.Run("opencode", func(t *testing.T) {
		config := filepath.Join(t.TempDir(), "opencode")
		commands := setupReleaseCommandHost(t, releaseRoot, "opencode", "FAKE_OPENCODE_CONFIG", config)
		assertReleaseCommandLog(t, commands,
			"opencode|--version",
			"opencode|debug paths",
		)
		assertNoCheckoutPath(t, commands, checkoutRoot)

		sourceBundle := filepath.Join(releaseRoot, "integrations", "opencode", "plugins", "powercontext", "lib", "index.js")
		assertArchiveFileIdentity(t, sourceBundle, filepath.Join(config, "plugins", "powercontext-opencode.js"))
		assertOpenCodeOwnershipManifest(t, filepath.Join(config, "plugins", ".powercontext-opencode.json"))
	})

	t.Run("openclaw", func(t *testing.T) {
		commands := setupReleaseCommandHost(t, releaseRoot, "openclaw")
		plugin := filepath.Join(releaseRoot, "integrations", "openclaw", "plugins", "memory-powercontext")
		assertCommandLogContains(t, commands,
			"openclaw|--version",
			"pnpm|--dir "+plugin+" install --frozen-lockfile",
			"pnpm|--dir "+plugin+" run build",
			"openclaw|plugins install --link --force "+plugin,
			"openclaw|config get gateway.mode --json",
			"openclaw|config get tools.alsoAllow --json",
			"openclaw|gateway restart",
		)
		assertNoCheckoutPath(t, commands, checkoutRoot)
	})

	t.Run("pi", func(t *testing.T) {
		plugin := filepath.Join(releaseRoot, "integrations", "pi", "plugins", "powercontext")
		commands := setupReleaseCommandHost(t, releaseRoot, "pi", "FAKE_PI_PACKAGE", plugin)
		assertReleaseCommandLog(t, commands,
			"pi|install "+plugin,
			"pi|list",
		)
		assertNoCheckoutPath(t, commands, checkoutRoot)
	})

	t.Run("workbuddy", func(t *testing.T) {
		home := filepath.Join(t.TempDir(), "workbuddy")
		t.Setenv("WORKBUDDY_HOME", home)
		t.Setenv("POWERCONTEXT_HOME", filepath.Join(t.TempDir(), "powercontext-home"))
		setup := exec.CommandContext(t.Context(), binary, "setup", "workbuddy", "--source", releaseRoot)
		setup.Dir = releaseRoot
		if output, setupErr := setup.CombinedOutput(); setupErr != nil {
			t.Fatalf("packaged WorkBuddy setup failed: %v\n%s", setupErr, output)
		}

		settingsPath := filepath.Join(home, "settings.json")
		settings, settingsErr := os.ReadFile(settingsPath)
		if settingsErr != nil {
			t.Fatal(settingsErr)
		}
		command := releaseWorkBuddyHookCommand(t, settings)
		if !strings.Contains(command, binary) || strings.Contains(command, "python") || strings.Contains(command, ".py") || strings.Contains(command, checkoutRoot) || strings.Contains(command, "token") {
			t.Fatalf("packaged WorkBuddy command = %q, want only the extracted release binary", command)
		}

		hook := exec.CommandContext(t.Context(), binary, "hook", "workbuddy")
		hook.Dir = releaseRoot
		hook.Stdin = strings.NewReader(`{"hook_event_name":"UserPromptSubmit","cwd":"` + releaseRoot + `","prompt":"release WorkBuddy hook","session_id":"release-consumer","prompt_id":"prompt-1"}`)
		hookOutput, hookErr := hook.CombinedOutput()
		if hookErr != nil {
			t.Fatalf("packaged WorkBuddy hook failed: %v\n%s", hookErr, hookOutput)
		}
		var hookResponse struct {
			HookSpecificOutput struct {
				HookEventName string `json:"hookEventName"`
			} `json:"hookSpecificOutput"`
		}
		if decodeErr := json.Unmarshal(hookOutput, &hookResponse); decodeErr != nil || hookResponse.HookSpecificOutput.HookEventName != "UserPromptSubmit" {
			t.Fatalf("packaged WorkBuddy hook response = %q, error = %v", hookOutput, decodeErr)
		}

		outside := filepath.Join(t.TempDir(), "powercontext")
		tampered := replaceReleaseWorkBuddyHookCommand(t, settings, "'"+strings.ReplaceAll(outside, "'", "'\\''")+"' hook workbuddy")
		if writeErr := os.WriteFile(settingsPath, tampered, 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}
		doctor := exec.CommandContext(t.Context(), binary, "doctor", "workbuddy", "--json")
		doctor.Dir = releaseRoot
		diagnostics, doctorErr := doctor.CombinedOutput()
		if doctorErr == nil {
			t.Fatalf("packaged WorkBuddy doctor accepted an outside release command: %s", diagnostics)
		}
		if !strings.Contains(string(diagnostics), `"hooks"`) || !strings.Contains(string(diagnostics), `"failed"`) {
			t.Fatalf("packaged WorkBuddy rejection diagnostics = %q", diagnostics)
		}
		after, afterErr := os.ReadFile(settingsPath)
		if afterErr != nil {
			t.Fatal(afterErr)
		}
		if !bytes.Equal(after, tampered) {
			t.Fatalf("outside WorkBuddy registration mutated settings:\n got %q\nwant %q", after, tampered)
		}
	})
}

func setupReleaseCommandHost(t *testing.T, releaseRoot, host string, extraEnvironment ...string) []string {
	t.Helper()
	if len(extraEnvironment)%2 != 0 {
		t.Fatal("release command-host environment must contain key/value pairs")
	}
	binDirectory, commandLog := writeAdapterConsumerHosts(t)
	t.Setenv("PATH", binDirectory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_HOST_LOG", commandLog)
	t.Setenv("POWERCONTEXT_HOME", filepath.Join(t.TempDir(), "powercontext-home"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(t.TempDir(), "claude"))
	for index := 0; index < len(extraEnvironment); index += 2 {
		t.Setenv(extraEnvironment[index], extraEnvironment[index+1])
	}
	command := exec.CommandContext(t.Context(), filepath.Join(releaseRoot, "bin", "powercontext"), "setup", host, "--source", releaseRoot)
	output, commandErr := command.CombinedOutput()
	if commandErr != nil {
		t.Fatalf("packaged %s setup failed: %v\n%s", host, commandErr, output)
	}
	if !strings.Contains(string(output), "setup complete") {
		t.Fatalf("packaged %s setup output = %q", host, output)
	}
	commands, readErr := os.ReadFile(commandLog)
	if readErr != nil {
		t.Fatal(readErr)
	}
	return strings.Split(strings.TrimSpace(string(commands)), "\n")
}

func assertReleaseCommandLog(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("host commands = %q, want %q", got, want)
	}
}

func assertCommandLogContains(t *testing.T, commands []string, expected ...string) {
	t.Helper()
	remaining := append([]string(nil), expected...)
	for _, command := range commands {
		for index, value := range remaining {
			if command == value {
				remaining = append(remaining[:index], remaining[index+1:]...)
				break
			}
		}
	}
	if len(remaining) > 0 {
		t.Fatalf("host commands missing %q in %q", remaining, commands)
	}
}

func assertNoCheckoutPath(t *testing.T, commands []string, checkoutRoot string) {
	t.Helper()
	if commandLogUsesPath(commands, checkoutRoot) {
		t.Fatalf("host command consumed checkout path %q", commands)
	}
}

func commandLogUsesPath(commands []string, path string) bool {
	for _, command := range commands {
		if strings.Contains(command, path) {
			return true
		}
	}
	return false
}

func assertArchiveFileIdentity(t *testing.T, source, installed string) {
	t.Helper()
	sourceContent, sourceErr := os.ReadFile(source)
	installedContent, installedErr := os.ReadFile(installed)
	if sourceErr != nil || installedErr != nil || !bytes.Equal(sourceContent, installedContent) {
		t.Fatalf("packaged adapter source = %q/%v, installed = %q/%v", sourceContent, sourceErr, installedContent, installedErr)
	}
}

func assertOpenCodeOwnershipManifest(t *testing.T, path string) {
	t.Helper()
	manifestContent, manifestErr := os.ReadFile(path)
	var manifest struct {
		Schema      int    `json:"schema"`
		Owner       string `json:"owner"`
		Integration string `json:"integration"`
	}
	if manifestErr != nil || json.Unmarshal(manifestContent, &manifest) != nil || manifest.Schema != 1 ||
		manifest.Owner != "powercontext" || manifest.Integration != "opencode-plugin" {
		t.Fatalf("packaged OpenCode ownership manifest = %q, error = %v", manifestContent, manifestErr)
	}
}
