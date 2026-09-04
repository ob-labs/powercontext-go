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
	"reflect"
	"runtime"
	"strings"
	"testing"
)

const releaseCommandHostCheckoutMutation = "POWERCONTEXT_RELEASE_COMMAND_HOST_CHECKOUT_MUTATION"

func TestReleaseArchiveProvidesConsumableCommandHosts(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("release command-host consumption is verified against the Linux release artifact")
	}

	repository := filepath.Clean(filepath.Join("..", ".."))
	checkoutRoot, err := filepath.Abs(repository)
	if err != nil {
		t.Fatal(err)
	}
	releaseRoot := unpackReleaseArchive(t, buildAdapterConsumerArchive(t, repository))
	markers := markReleaseCommandHostArchive(t, releaseRoot)
	sourceRoot := releaseRoot
	if os.Getenv(releaseCommandHostCheckoutMutation) == "1" {
		sourceRoot = checkoutRoot
	}
	binary := filepath.Join(releaseRoot, "bin", "powercontext")

	t.Run("codex", func(t *testing.T) {
		commands := setupReleaseCommandHost(t, releaseRoot, sourceRoot, "codex")
		assertReleaseCommandLog(t, commands, [][]string{
			{"codex", "plugin", "marketplace", "add", sourceRoot, "--json"},
			{"codex", "plugin", "add", "powercontext@powercontext", "--json"},
			{"codex", "plugin", "list", "--json"},
		})
		assertNoCheckoutPath(t, commands, checkoutRoot, sourceRoot == releaseRoot)
	})

	t.Run("claude-code", func(t *testing.T) {
		commands := setupReleaseCommandHost(t, releaseRoot, sourceRoot, "claude-code")
		assertReleaseCommandLog(t, commands, [][]string{
			{"claude", "plugin", "marketplace", "list", "--json"},
			{"claude", "plugin", "list", "--json"},
			{"claude", "plugin", "marketplace", "add", sourceRoot, "--scope", "user"},
			{"claude", "plugin", "install", "powercontext@powercontext", "--scope", "user"},
			{"claude", "plugin", "list", "--json"},
		})
		assertNoCheckoutPath(t, commands, checkoutRoot, sourceRoot == releaseRoot)
	})

	t.Run("dsh", func(t *testing.T) {
		commands := setupReleaseCommandHost(t, releaseRoot, sourceRoot, "dsh")
		plugin := filepath.Join(sourceRoot, "integrations", "dsh", "plugins", "powercontext")
		assertReleaseCommandLog(t, commands, [][]string{
			{"dsh", "plugin", "--profile", "web", "add", plugin},
			{"dsh", "--profile", "web", "--dump-config"},
		})
		assertNoCheckoutPath(t, commands, checkoutRoot, sourceRoot == releaseRoot)
	})

	t.Run("hermes", func(t *testing.T) {
		home := filepath.Join(t.TempDir(), "hermes")
		commands := setupReleaseCommandHost(t, releaseRoot, sourceRoot, "hermes", "HERMES_HOME", home)
		assertHermesStagingPaths(t, commands, home)
		assertReleaseCommandLog(t, normalizeHermesCommandLog(commands, home), [][]string{
			{"hermes", "--version"},
			{"hermes", "plugins", "doctor", "--ci", filepath.Join(home, "plugins", ".powercontext-") + "<random>"},
			{"hermes", "plugins", "doctor", "--ci", filepath.Join(home, "plugins", ".powercontext-") + "<random>"},
			{"hermes", "plugins", "enable", "powercontext-command"},
			{"hermes", "--version"},
			{"hermes", "plugins", "doctor", "--ci", filepath.Join(home, "plugins", "powercontext")},
			{"hermes", "plugins", "doctor", "--ci", filepath.Join(home, "plugins", "powercontext-command")},
		})
		assertFileContains(t, filepath.Join(home, "plugins", "powercontext", "plugin.yaml"), markers.hermes)
		assertFileContains(t, filepath.Join(home, "plugins", "powercontext-command", "plugin.yaml"), markers.hermes)
		assertArchiveFileIdentity(t,
			filepath.Join(releaseRoot, "integrations", "hermes", "plugins", "powercontext", "plugin.yaml"),
			filepath.Join(home, "plugins", "powercontext", "plugin.yaml"),
		)
		assertArchiveFileIdentity(t,
			filepath.Join(releaseRoot, "integrations", "hermes", "plugins", "powercontext-command", "plugin.yaml"),
			filepath.Join(home, "plugins", "powercontext-command", "plugin.yaml"),
		)
	})

	t.Run("opencode", func(t *testing.T) {
		config := filepath.Join(t.TempDir(), "opencode")
		commands := setupReleaseCommandHost(t, releaseRoot, sourceRoot, "opencode", "FAKE_OPENCODE_CONFIG", config)
		assertReleaseCommandLog(t, commands, [][]string{
			{"opencode", "--version"},
			{"opencode", "debug", "paths"},
		})
		sourceBundle := filepath.Join(releaseRoot, "integrations", "opencode", "plugins", "powercontext", "lib", "index.js")
		installedBundle := filepath.Join(config, "plugins", "powercontext-opencode.js")
		assertFileContains(t, installedBundle, markers.openCode)
		assertArchiveFileIdentity(t, sourceBundle, installedBundle)
		assertOpenCodeOwnershipManifest(t, filepath.Join(config, "plugins", ".powercontext-opencode.json"))
	})

	t.Run("openclaw", func(t *testing.T) {
		commands := setupReleaseCommandHost(t, releaseRoot, sourceRoot, "openclaw")
		plugin := filepath.Join(sourceRoot, "integrations", "openclaw", "plugins", "memory-powercontext")
		assertOpenClawCommandLog(t, commands, plugin)
		assertNoCheckoutPath(t, commands, checkoutRoot, sourceRoot == releaseRoot)
	})

	t.Run("pi", func(t *testing.T) {
		plugin := filepath.Join(sourceRoot, "integrations", "pi", "plugins", "powercontext")
		commands := setupReleaseCommandHost(t, releaseRoot, sourceRoot, "pi", "FAKE_PI_PACKAGE", plugin)
		assertReleaseCommandLog(t, commands, [][]string{
			{"pi", "install", plugin},
			{"pi", "list"},
		})
		assertNoCheckoutPath(t, commands, checkoutRoot, sourceRoot == releaseRoot)
	})

	t.Run("workbuddy", func(t *testing.T) {
		home := filepath.Join(t.TempDir(), "workbuddy")
		t.Setenv("WORKBUDDY_HOME", home)
		t.Setenv("POWERCONTEXT_HOME", filepath.Join(t.TempDir(), "powercontext-home"))
		setup := exec.CommandContext(t.Context(), binary, "setup", "workbuddy", "--source", sourceRoot)
		setup.Dir = releaseRoot
		if output, setupErr := setup.CombinedOutput(); setupErr != nil {
			t.Fatalf("packaged WorkBuddy setup failed: %v\n%s", setupErr, output)
		}

		settingsPath := filepath.Join(home, "settings.json")
		settings, settingsErr := os.ReadFile(settingsPath)
		if settingsErr != nil {
			t.Fatal(settingsErr)
		}
		assertWorkBuddyReleaseHook(t, settings, binary, checkoutRoot)
		assertFileContains(t, filepath.Join(home, "skills", "project-context", "SKILL.md"), markers.workBuddy)

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

	if sourceRoot == releaseRoot {
		t.Run("checkout-substitution-mutant", func(t *testing.T) {
			assertCheckoutSubstitutionFails(t)
		})
	}
}

func assertCheckoutSubstitutionFails(t *testing.T) {
	t.Helper()
	for _, mutation := range []struct {
		host   string
		marker string
	}{
		{host: "hermes", marker: "release-archive-marker-hermes"},
		{host: "opencode", marker: "release-archive-marker-opencode"},
		{host: "workbuddy", marker: "release-archive-marker-workbuddy"},
	} {
		t.Run(mutation.host, func(t *testing.T) {
			command := exec.CommandContext(
				t.Context(), os.Args[0], "-test.count=1",
				"-test.run=^TestReleaseArchiveProvidesConsumableCommandHosts/"+mutation.host+"$",
			)
			command.Env = append(os.Environ(), releaseCommandHostCheckoutMutation+"=1")
			output, runErr := command.CombinedOutput()
			if runErr == nil {
				t.Fatalf("%s checkout substitution mutant unexpectedly passed:\n%s", mutation.host, output)
			}
			if !strings.Contains(string(output), mutation.marker) {
				t.Fatalf("%s checkout substitution mutant did not fail marker %q:\n%s", mutation.host, mutation.marker, output)
			}
		})
	}
}

type releaseCommandHostMarkers struct {
	hermes    string
	openCode  string
	workBuddy string
}

func markReleaseCommandHostArchive(t *testing.T, releaseRoot string) releaseCommandHostMarkers {
	t.Helper()
	markers := releaseCommandHostMarkers{
		hermes:    "release-archive-marker-hermes",
		openCode:  "release-archive-marker-opencode",
		workBuddy: "release-archive-marker-workbuddy",
	}
	appendArchiveMarker(t,
		filepath.Join(releaseRoot, "integrations", "hermes", "plugins", "powercontext", "plugin.yaml"),
		"\n# "+markers.hermes+"\n",
	)
	appendArchiveMarker(t,
		filepath.Join(releaseRoot, "integrations", "hermes", "plugins", "powercontext-command", "plugin.yaml"),
		"\n# "+markers.hermes+"\n",
	)
	appendArchiveMarker(t,
		filepath.Join(releaseRoot, "integrations", "opencode", "plugins", "powercontext", "lib", "index.js"),
		"\n// "+markers.openCode+"\n",
	)
	appendArchiveMarker(t,
		filepath.Join(releaseRoot, "integrations", "workbuddy", "plugins", "powercontext", "skills", "project-context", "SKILL.md"),
		"\n<!-- "+markers.workBuddy+" -->\n",
	)
	return markers
}

func appendArchiveMarker(t *testing.T, path, marker string) {
	t.Helper()
	content, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if writeErr := os.WriteFile(path, append(content, marker...), 0o644); writeErr != nil {
		t.Fatal(writeErr)
	}
}

func setupReleaseCommandHost(t *testing.T, releaseRoot, sourceRoot, host string, extraEnvironment ...string) [][]string {
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
	command := exec.CommandContext(t.Context(), filepath.Join(releaseRoot, "bin", "powercontext"), "setup", host, "--source", sourceRoot)
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
	var parsed [][]string
	for line := range strings.SplitSeq(strings.TrimSpace(string(commands)), "\n") {
		parsed = append(parsed, strings.Split(line, "\t"))
	}
	return parsed
}

func assertReleaseCommandLog(t *testing.T, got, want [][]string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("host commands = %#v, want %#v", got, want)
	}
}

func normalizeHermesCommandLog(commands [][]string, home string) [][]string {
	stagingPrefix := filepath.Join(home, "plugins", ".powercontext-")
	normalized := make([][]string, 0, len(commands))
	for _, command := range commands {
		copy := append([]string(nil), command...)
		for index, argument := range copy {
			if strings.HasPrefix(argument, stagingPrefix) {
				copy[index] = stagingPrefix + "<random>"
			}
		}
		normalized = append(normalized, copy)
	}
	return normalized
}

func assertHermesStagingPaths(t *testing.T, commands [][]string, home string) {
	t.Helper()
	if len(commands) < 3 {
		t.Fatalf("Hermes commands = %#v, want staging doctors at positions 1 and 2", commands)
	}
	stagingPrefix := filepath.Join(home, "plugins", ".powercontext-")
	paths := make([]string, 0, 2)
	for _, index := range []int{1, 2} {
		command := commands[index]
		if len(command) != 5 || !reflect.DeepEqual(command[:4], []string{"hermes", "plugins", "doctor", "--ci"}) {
			t.Fatalf("Hermes staging command %d = %#v", index, command)
		}
		suffix, hasPrefix := strings.CutPrefix(command[4], stagingPrefix)
		if !hasPrefix || suffix == "" {
			t.Fatalf("Hermes staging path %q must have nonempty random suffix after %q", command[4], stagingPrefix)
		}
		paths = append(paths, command[4])
	}
	if paths[0] == paths[1] {
		t.Fatalf("Hermes provider and command plugin staging paths must differ: %q", paths[0])
	}
}

func assertOpenClawCommandLog(t *testing.T, commands [][]string, plugin string) {
	t.Helper()
	if len(commands) != 9 {
		t.Fatalf("OpenClaw command count = %d, want 9: %#v", len(commands), commands)
	}
	for index, expected := range map[int][]string{
		0: {"openclaw", "--version"},
		1: {"pnpm", "--dir", plugin, "install", "--frozen-lockfile"},
		2: {"pnpm", "--dir", plugin, "run", "build"},
		3: {"openclaw", "plugins", "install", "--link", "--force", plugin},
		4: {"openclaw", "config", "get", "gateway.mode", "--json"},
		6: {"openclaw", "config", "get", "tools.alsoAllow", "--json"},
		8: {"openclaw", "gateway", "restart"},
	} {
		if !reflect.DeepEqual(commands[index], expected) {
			t.Fatalf("OpenClaw command %d = %#v, want %#v", index, commands[index], expected)
		}
	}
	if got := commands[5]; len(got) != 5 || !reflect.DeepEqual(got[:4], []string{"openclaw", "config", "set", "--batch-json"}) {
		t.Fatalf("OpenClaw batch configuration command = %#v", got)
	}
	assertOpenClawJSON(t, commands[5][4], []map[string]any{
		{"path": "gateway.mode", "value": "local"},
		{"path": "plugins.entries.memory-powercontext.enabled", "value": true},
		{"path": "plugins.entries.memory-powercontext.config.endpoint", "value": "http://127.0.0.1:8000"},
		{"path": "plugins.entries.memory-powercontext.config.autoRecall", "value": true},
		{"path": "plugins.entries.memory-powercontext.config.autoCapture", "value": true},
		{"path": "plugins.entries.memory-powercontext.config.scopeMode", "value": "agent"},
		{"path": "plugins.entries.memory-powercontext.hooks.allowConversationAccess", "value": true},
		{"path": "plugins.slots.memory", "value": "memory-powercontext"},
	})
	if got := commands[7]; len(got) != 6 || !reflect.DeepEqual(got[:4], []string{"openclaw", "config", "set", "tools.alsoAllow"}) || got[5] != "--strict-json" {
		t.Fatalf("OpenClaw allowlist command = %#v", got)
	}
	assertOpenClawJSON(t, commands[7][4], []any{
		"powercontext_memory_search",
		"powercontext_memory_get",
		"powercontext_memory_store",
		"powercontext_memory_revise",
		"powercontext_memory_retire",
	})
}

func assertOpenClawJSON(t *testing.T, actual string, expected any) {
	t.Helper()
	var decoded any
	if decodeErr := json.Unmarshal([]byte(actual), &decoded); decodeErr != nil {
		t.Fatalf("OpenClaw JSON = %q: %v", actual, decodeErr)
	}
	expectedJSON, marshalErr := json.Marshal(expected)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	var expectedDecoded any
	if decodeErr := json.Unmarshal(expectedJSON, &expectedDecoded); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if !reflect.DeepEqual(decoded, expectedDecoded) {
		t.Fatalf("OpenClaw JSON = %#v, want %#v", decoded, expectedDecoded)
	}
}

func assertNoCheckoutPath(t *testing.T, commands [][]string, checkoutRoot string, required bool) {
	t.Helper()
	if !required {
		return
	}
	for _, command := range commands {
		for _, argument := range command {
			if strings.Contains(argument, checkoutRoot) {
				t.Fatalf("host command consumed checkout path %#v", command)
			}
		}
	}
}

func assertArchiveFileIdentity(t *testing.T, source, installed string) {
	t.Helper()
	sourceContent, sourceErr := os.ReadFile(source)
	installedContent, installedErr := os.ReadFile(installed)
	if sourceErr != nil || installedErr != nil || !bytes.Equal(sourceContent, installedContent) {
		t.Fatalf("packaged adapter source = %q/%v, installed = %q/%v", sourceContent, sourceErr, installedContent, installedErr)
	}
}

func assertFileContains(t *testing.T, path, marker string) {
	t.Helper()
	content, readErr := os.ReadFile(path)
	if readErr != nil || !strings.Contains(string(content), marker) {
		t.Fatalf("archive consumer marker %q missing from %q: %v", marker, path, readErr)
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

func assertWorkBuddyReleaseHook(t *testing.T, settings []byte, binary, checkoutRoot string) {
	t.Helper()
	var value map[string]any
	if decodeErr := json.Unmarshal(settings, &value); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	hooks, ok := value["hooks"].(map[string]any)
	if !ok {
		t.Fatalf("WorkBuddy settings hooks = %#v", value["hooks"])
	}
	matchers, ok := hooks["UserPromptSubmit"].([]any)
	if !ok {
		t.Fatalf("WorkBuddy UserPromptSubmit hooks = %#v", hooks["UserPromptSubmit"])
	}
	var candidates []map[string]any
	for _, matcher := range matchers {
		matcherObject, matcherOK := matcher.(map[string]any)
		if !matcherOK {
			t.Fatalf("WorkBuddy matcher = %#v", matcher)
		}
		entries, entriesOK := matcherObject["hooks"].([]any)
		if !entriesOK {
			t.Fatalf("WorkBuddy matcher hooks = %#v", matcherObject["hooks"])
		}
		for _, item := range entries {
			entry, entryOK := item.(map[string]any)
			if !entryOK {
				t.Fatalf("WorkBuddy hook entry = %#v", item)
			}
			command, _ := entry["command"].(string)
			if strings.Contains(command, "powercontext") {
				candidates = append(candidates, entry)
			}
		}
	}
	if len(candidates) != 1 {
		t.Fatalf("PowerContext WorkBuddy hook candidates = %#v, want exactly one", candidates)
	}
	entry := candidates[0]
	wantCommand := shellQuoteReleaseBinary(binary) + " hook workbuddy"
	if len(entry) != 4 || entry["type"] != "command" || entry["command"] != wantCommand || entry["timeout"] != float64(10) || entry["statusMessage"] != "Syncing PowerContext" {
		t.Fatalf("PowerContext WorkBuddy hook = %#v, want command %q", entry, wantCommand)
	}
	command := entry["command"].(string)
	if strings.Contains(command, checkoutRoot) || strings.Contains(command, "python") || strings.Contains(command, ".py") {
		t.Fatalf("PowerContext WorkBuddy hook must use only the extracted binary: %q", command)
	}
}

func shellQuoteReleaseBinary(value string) string {
	if runtime.GOOS == "windows" {
		return `"` + strings.ReplaceAll(value, `"`, `\"`) + `"`
	}
	if value != "" && !strings.ContainsAny(value, " \t\n'\"\\$`;&|<>()[]{}*?!") {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
