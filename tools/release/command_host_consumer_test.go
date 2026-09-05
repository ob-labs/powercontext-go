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

const releaseCommandHostArchive = "POWERCONTEXT_RELEASE_COMMAND_HOST_ARCHIVE"

func TestReleaseArchiveProvidesConsumableCommandHosts(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("release command-host consumption is verified against the Linux release artifact")
	}

	repository := filepath.Clean(filepath.Join("..", ".."))
	checkoutRoot, err := filepath.Abs(repository)
	if err != nil {
		t.Fatal(err)
	}
	archive := os.Getenv(releaseCommandHostArchive)
	if archive == "" {
		archive = buildAdapterConsumerArchive(t, repository)
	}
	releaseRoot := unpackReleaseArchive(t, archive)
	markers := markReleaseCommandHostArchive(t, releaseRoot)
	sourceRoot := releaseRoot
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
}

type releaseCommandHostMarkers struct {
	workBuddy string
}

func markReleaseCommandHostArchive(t *testing.T, releaseRoot string) releaseCommandHostMarkers {
	t.Helper()
	markers := releaseCommandHostMarkers{
		workBuddy: "release-archive-marker-workbuddy",
	}
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

func assertFileContains(t *testing.T, path, marker string) {
	t.Helper()
	content, readErr := os.ReadFile(path)
	if readErr != nil || !strings.Contains(string(content), marker) {
		t.Fatalf("archive consumer marker %q missing from %q: %v", marker, path, readErr)
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
