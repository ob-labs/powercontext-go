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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupWorkBuddyInstallsLocalPluginAndDoctorReportsOK(t *testing.T) {
	checkout := filepath.Join(t.TempDir(), "powercontext")
	plugin := writeWorkBuddyPlugin(t, checkout)
	home := filepath.Join(t.TempDir(), "workbuddy")
	dataDirectory := filepath.Join(t.TempDir(), "data")
	t.Setenv("WORKBUDDY_HOME", home)
	t.Setenv("POWERCONTEXT_HOME", dataDirectory)
	resolvedHome, err := resolvePath(home)
	if err != nil {
		t.Fatal(err)
	}
	resolvedDataDirectory, err := resolvePath(dataDirectory)
	if err != nil {
		t.Fatal(err)
	}

	stdout, _, err := executeSystemCLI(
		t, nil, &scriptedSystemCommands{t: t}, "setup", "workbuddy", "--source", checkout, "--json",
	)
	if err != nil {
		t.Fatal(err)
	}
	result := decodeSystemOutput(t, stdout)
	if result["plugin"] != "powercontext" || result["plugin_path"] != plugin ||
		result["workbuddy_home"] != resolvedHome || result["hooks_dir"] != filepath.Join(resolvedHome, "hooks") ||
		result["data_dir"] != resolvedDataDirectory {
		t.Fatalf("setup result = %#v", result)
	}
	for _, name := range []string{
		"workbuddy_powercontext_hook.py", "workbuddy_settings.py", "prepared_context.py", "powercontext_project_scope.py",
	} {
		if info, statErr := os.Stat(filepath.Join(home, "hooks", name)); statErr != nil || !info.Mode().IsRegular() {
			t.Fatalf("installed hook %q error = %v", name, statErr)
		}
	}
	if info, statErr := os.Stat(filepath.Join(home, "skills", "project-context", "SKILL.md")); statErr != nil || !info.Mode().IsRegular() {
		t.Fatalf("installed Skill error = %v", statErr)
	}

	doctorOutput, _, doctorErr := executeSystemCLI(
		t, nil, &scriptedSystemCommands{t: t}, "doctor", "workbuddy", "--json",
	)
	if doctorErr != nil {
		t.Fatal(doctorErr)
	}
	payload := decodeSystemOutput(t, doctorOutput)
	if payload["ok"] != true || payload["status"] != "ok" {
		t.Fatalf("doctor result = %#v", payload)
	}
	for name, value := range payload["checks"].(map[string]any) {
		if value.(map[string]any)["status"] != "ok" {
			t.Fatalf("doctor check %s = %#v", name, value)
		}
	}
}

func TestSetupWorkBuddyPreservesExistingSettingsAndMCP(t *testing.T) {
	checkout := filepath.Join(t.TempDir(), "powercontext")
	writeWorkBuddyPlugin(t, checkout)
	home := filepath.Join(t.TempDir(), "workbuddy")
	t.Setenv("WORKBUDDY_HOME", home)
	t.Setenv("POWERCONTEXT_HOME", filepath.Join(t.TempDir(), "data"))
	writeWorkBuddyTestJSON(t, filepath.Join(home, "settings.json"), map[string]any{
		"enabledPlugins": map[string]any{"other-plugin": true},
		"sandbox":        map[string]any{"enabled": true},
		"hooks": map[string]any{"UserPromptSubmit": []any{map[string]any{
			"hooks": []any{map[string]any{"type": "command", "command": "echo hello", "timeout": float64(5)}},
		}}},
	})
	writeWorkBuddyTestJSON(t, filepath.Join(home, "mcp.json"), map[string]any{
		"mcpServers": map[string]any{"other-server": map[string]any{"type": "stdio", "command": "other", "args": []any{}}},
	})

	if _, _, err := executeSystemCLI(t, nil, &scriptedSystemCommands{t: t}, "setup", "workbuddy", "--source", checkout); err != nil {
		t.Fatal(err)
	}
	settings := readWorkBuddyJSON(t, filepath.Join(home, "settings.json"))
	if settings["enabledPlugins"].(map[string]any)["other-plugin"] != true ||
		settings["sandbox"].(map[string]any)["enabled"] != true ||
		!settingsHaveWorkBuddyHook(settings) {
		t.Fatalf("settings = %#v", settings)
	}
	mcp := readWorkBuddyJSON(t, filepath.Join(home, "mcp.json"))
	servers := mcp["mcpServers"].(map[string]any)
	if servers["other-server"].(map[string]any)["command"] != "other" ||
		servers[workBuddyPluginName].(map[string]any)["url"] != workBuddyServerURLTemplate {
		t.Fatalf("MCP = %#v", mcp)
	}
}

func TestSetupWorkBuddyPreservesRemoteMCPAndMigratesLegacyEntry(t *testing.T) {
	for _, test := range []struct {
		name     string
		existing map[string]any
		wantURL  string
		wantAuth string
	}{
		{
			name: "remote authenticated",
			existing: map[string]any{
				"type": "http", "url": "https://memory.example.test/mcp",
				"headers": map[string]any{"Authorization": "Bearer existing-token"}, "disabled": true,
			},
			wantURL: "https://memory.example.test/mcp", wantAuth: "Bearer existing-token",
		},
		{
			name: "legacy generated",
			existing: map[string]any{
				"type": "http", "url": workBuddyLegacyMCPURL, "headers": map[string]any{},
				"description": workBuddyMCPDescription, "disabled": false,
			},
			wantURL: workBuddyServerURLTemplate, wantAuth: workBuddyAuthorizationTemplate,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			checkout := filepath.Join(t.TempDir(), "powercontext")
			writeWorkBuddyPlugin(t, checkout)
			home := filepath.Join(t.TempDir(), "workbuddy")
			t.Setenv("WORKBUDDY_HOME", home)
			t.Setenv("POWERCONTEXT_HOME", filepath.Join(t.TempDir(), "data"))
			writeWorkBuddyTestJSON(t, filepath.Join(home, "mcp.json"), map[string]any{
				"mcpServers": map[string]any{workBuddyPluginName: test.existing},
			})

			if _, _, err := executeSystemCLI(t, nil, &scriptedSystemCommands{t: t}, "setup", "workbuddy", "--source", checkout); err != nil {
				t.Fatal(err)
			}
			entry := readWorkBuddyJSON(t, filepath.Join(home, "mcp.json"))["mcpServers"].(map[string]any)[workBuddyPluginName].(map[string]any)
			if entry["url"] != test.wantURL || entry["headers"].(map[string]any)["Authorization"] != test.wantAuth || entry["disabled"] != false {
				t.Fatalf("MCP entry = %#v", entry)
			}
		})
	}
}

func TestSetupWorkBuddyRefusesUnownedSkillBeforeWriting(t *testing.T) {
	checkout := filepath.Join(t.TempDir(), "powercontext")
	writeWorkBuddyPlugin(t, checkout)
	home := filepath.Join(t.TempDir(), "workbuddy")
	skill := filepath.Join(home, "skills", workBuddySkillName)
	writeTestFile(t, filepath.Join(skill, "SKILL.md"), "user-owned\n")
	t.Setenv("WORKBUDDY_HOME", home)
	t.Setenv("POWERCONTEXT_HOME", filepath.Join(t.TempDir(), "data"))

	_, _, err := executeSystemCLI(t, nil, &scriptedSystemCommands{t: t}, "setup", "workbuddy", "--source", checkout)
	if err == nil || !strings.Contains(err.Error(), "not owned by PowerContext") {
		t.Fatalf("setup error = %v", err)
	}
	for _, path := range []string{filepath.Join(home, "hooks"), filepath.Join(home, "settings.json"), filepath.Join(home, "mcp.json")} {
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("setup mutated %q: %v", path, statErr)
		}
	}
}

func TestDoctorWorkBuddyReportsFailuresBeforeInstall(t *testing.T) {
	t.Setenv("WORKBUDDY_HOME", filepath.Join(t.TempDir(), "workbuddy"))
	stdout, _, err := executeSystemCLI(t, nil, &scriptedSystemCommands{t: t}, "doctor", "workbuddy", "--json")
	if err == nil || !ErrorAlreadyReported(err) || ExitCode(err) != 1 {
		t.Fatalf("doctor error = %v, exit = %d", err, ExitCode(err))
	}
	payload := decodeSystemOutput(t, stdout)
	if payload["ok"] != false || payload["status"] != "failed" {
		t.Fatalf("doctor output = %#v", payload)
	}
	for name, value := range payload["checks"].(map[string]any) {
		if value.(map[string]any)["status"] != "failed" {
			t.Fatalf("doctor check %s = %#v", name, value)
		}
	}
}

func TestSetupWorkBuddyUpdatesExistingHookAndPreservesSharedScripts(t *testing.T) {
	checkout := filepath.Join(t.TempDir(), "powercontext")
	writeWorkBuddyPlugin(t, checkout)
	home := filepath.Join(t.TempDir(), "workbuddy")
	t.Setenv("WORKBUDDY_HOME", home)
	t.Setenv("POWERCONTEXT_HOME", filepath.Join(t.TempDir(), "data"))
	writeWorkBuddyTestJSON(t, filepath.Join(home, "settings.json"), map[string]any{
		"hooks": map[string]any{"UserPromptSubmit": []any{map[string]any{
			"hooks": []any{map[string]any{
				"type": "command", "command": "python3 /old/workbuddy_powercontext_hook.py", "timeout": float64(3), "custom": "preserved",
			}},
		}}},
	})
	shared := filepath.Join(home, "hooks", "scripts", "other_hook.py")
	writeTestFile(t, shared, "# owned by another hook\n")

	if _, _, err := executeSystemCLI(t, nil, &scriptedSystemCommands{t: t}, "setup", "workbuddy", "--source", checkout); err != nil {
		t.Fatal(err)
	}
	settings := readWorkBuddyJSON(t, filepath.Join(home, "settings.json"))
	matchers := settings["hooks"].(map[string]any)["UserPromptSubmit"].([]any)
	entry := matchers[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)
	if !strings.Contains(entry["command"].(string), "workbuddy_powercontext_hook.py") ||
		entry["timeout"] != float64(10) || entry["statusMessage"] != "Syncing PowerContext" || entry["custom"] != "preserved" {
		t.Fatalf("updated hook = %#v", entry)
	}
	content, err := os.ReadFile(shared)
	if err != nil || string(content) != "# owned by another hook\n" {
		t.Fatalf("shared hook content = %q, error = %v", content, err)
	}
}

func TestSetupWorkBuddyRefreshesOwnedSkill(t *testing.T) {
	checkout := filepath.Join(t.TempDir(), "powercontext")
	plugin := writeWorkBuddyPlugin(t, checkout)
	home := filepath.Join(t.TempDir(), "workbuddy")
	t.Setenv("WORKBUDDY_HOME", home)
	t.Setenv("POWERCONTEXT_HOME", filepath.Join(t.TempDir(), "data"))

	if _, _, err := executeSystemCLI(t, nil, &scriptedSystemCommands{t: t}, "setup", "workbuddy", "--source", checkout); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(plugin, "skills", workBuddySkillName, "SKILL.md"), "updated\n${POWERCONTEXT_PYTHON} ${POWERCONTEXT_PROJECT_SCOPE_SCRIPT}\n")
	if _, _, err := executeSystemCLI(t, nil, &scriptedSystemCommands{t: t}, "setup", "workbuddy", "--source", checkout); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(home, "skills", workBuddySkillName, "SKILL.md"))
	if err != nil || !strings.HasPrefix(string(content), "updated\n") || strings.Contains(string(content), "${POWERCONTEXT_") {
		t.Fatalf("refreshed Skill = %q, error = %v", content, err)
	}
}

func TestSetupWorkBuddyStopsBeforeWritesWhenSettingsSnapshotIsUnreadable(t *testing.T) {
	checkout := filepath.Join(t.TempDir(), "powercontext")
	writeWorkBuddyPlugin(t, checkout)
	home := filepath.Join(t.TempDir(), "workbuddy")
	if err := os.MkdirAll(filepath.Join(home, "settings.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WORKBUDDY_HOME", home)
	t.Setenv("POWERCONTEXT_HOME", filepath.Join(t.TempDir(), "data"))

	_, _, err := executeSystemCLI(t, nil, &scriptedSystemCommands{t: t}, "setup", "workbuddy", "--source", checkout)
	if err == nil || !strings.Contains(err.Error(), "Cannot update WorkBuddy settings") {
		t.Fatalf("setup error = %v", err)
	}
	for _, path := range []string{filepath.Join(home, "hooks"), filepath.Join(home, "mcp.json")} {
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("setup wrote %q: %v", path, statErr)
		}
	}
}

func TestSetupWorkBuddyRollsBackJSONChangesWhenSkillCopyFails(t *testing.T) {
	checkout := filepath.Join(t.TempDir(), "powercontext")
	plugin := writeWorkBuddyPlugin(t, checkout)
	writeTestFile(t, filepath.Join(plugin, "skills", workBuddySkillName, "oversized.bin"), strings.Repeat("x", maximumCommandOutput+1))
	home := filepath.Join(t.TempDir(), "workbuddy")
	t.Setenv("WORKBUDDY_HOME", home)
	t.Setenv("POWERCONTEXT_HOME", filepath.Join(t.TempDir(), "data"))
	previousSettings := map[string]any{"enabledPlugins": map[string]any{"other-plugin": true}}
	previousMCP := map[string]any{"mcpServers": map[string]any{"other-server": map[string]any{"type": "stdio", "command": "other"}}}
	writeWorkBuddyTestJSON(t, filepath.Join(home, "settings.json"), previousSettings)
	writeWorkBuddyTestJSON(t, filepath.Join(home, "mcp.json"), previousMCP)

	_, _, err := executeSystemCLI(t, nil, &scriptedSystemCommands{t: t}, "setup", "workbuddy", "--source", checkout)
	if err == nil || !strings.Contains(err.Error(), "cannot copy WorkBuddy Skill") {
		t.Fatalf("setup error = %v", err)
	}
	if got := readWorkBuddyJSON(t, filepath.Join(home, "settings.json")); fmt.Sprint(got) != fmt.Sprint(previousSettings) {
		t.Fatalf("settings after rollback = %#v", got)
	}
	if got := readWorkBuddyJSON(t, filepath.Join(home, "mcp.json")); fmt.Sprint(got) != fmt.Sprint(previousMCP) {
		t.Fatalf("MCP after rollback = %#v", got)
	}
	if _, statErr := os.Stat(filepath.Join(home, "hooks")); !os.IsNotExist(statErr) {
		t.Fatalf("hooks survived failed setup: %v", statErr)
	}
}

func TestSetupWorkBuddyRejectsMissingPlugin(t *testing.T) {
	home := filepath.Join(t.TempDir(), "workbuddy")
	t.Setenv("WORKBUDDY_HOME", home)
	t.Setenv("POWERCONTEXT_HOME", filepath.Join(t.TempDir(), "data"))
	_, _, err := executeSystemCLI(t, nil, &scriptedSystemCommands{t: t}, "setup", "workbuddy", "--source", filepath.Join(t.TempDir(), "missing"))
	if err == nil || !strings.Contains(err.Error(), "WorkBuddy plugin was not found") {
		t.Fatalf("setup error = %v", err)
	}
}

func TestWorkBuddyHomeHonorsEnvironmentOverride(t *testing.T) {
	home := filepath.Join(t.TempDir(), "workbuddy")
	t.Setenv("WORKBUDDY_HOME", home)
	resolved, err := workBuddyHome()
	want, resolveErr := resolvePath(home)
	if err != nil || resolveErr != nil || resolved != want {
		t.Fatalf("workBuddyHome() = %q, %v; want %q (%v)", resolved, err, want, resolveErr)
	}
}

func TestResolveWorkBuddyPluginRefreshesRemoteRequestedRef(t *testing.T) {
	dataDirectory := filepath.Join(t.TempDir(), "data")
	markers := []string{"first\n", "refreshed\n"}
	commands := &scriptedSystemCommands{t: t}
	for _, marker := range markers {
		marker := marker
		commands.results = append(commands.results, systemCommandResult{after: func(call systemCommandCall) {
			plugin := writeWorkBuddyPlugin(t, call.arguments[len(call.arguments)-1])
			writeTestFile(t, filepath.Join(plugin, "revision.txt"), marker)
		}})
	}
	first, err := resolveWorkBuddyPlugin(t.Context(), commands, "owner/repo", "tested-ref", dataDirectory)
	if err != nil {
		t.Fatal(err)
	}
	refreshed, err := resolveWorkBuddyPlugin(t.Context(), commands, "owner/repo", "tested-ref", dataDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if first != refreshed {
		t.Fatalf("plugin paths = %q and %q", first, refreshed)
	}
	content, err := os.ReadFile(filepath.Join(refreshed, "revision.txt"))
	if err != nil || string(content) != "refreshed\n" {
		t.Fatalf("refreshed marker = %q, error = %v", content, err)
	}
	for _, call := range commands.calls {
		if !strings.Contains(call.String(), "--branch tested-ref") {
			t.Fatalf("clone call did not use requested ref: %s", call.String())
		}
	}
}

func TestBundledWorkBuddyPluginCarriesInstallableHooksSkillAndMCP(t *testing.T) {
	root, err := resolvePath(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	plugin, ok := findWorkBuddyPlugin(root)
	if !ok {
		t.Fatalf("bundled WorkBuddy plugin was not found below %q", root)
	}
	payload, err := os.ReadFile(filepath.Join(plugin, ".mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal(payload, &config); err != nil {
		t.Fatal(err)
	}
	entry := config["mcpServers"].(map[string]any)[workBuddyPluginName].(map[string]any)
	if entry["url"] != workBuddyServerURLTemplate ||
		entry["headers"].(map[string]any)["Authorization"] != workBuddyAuthorizationTemplate ||
		entry["disabled"] != false {
		t.Fatalf("bundled WorkBuddy MCP entry = %#v", entry)
	}
}

func writeWorkBuddyPlugin(t *testing.T, root string) string {
	t.Helper()
	plugin := filepath.Join(root, "integrations", "workbuddy", "plugins", "powercontext")
	for _, name := range []string{"workbuddy_powercontext_hook.py", "workbuddy_settings.py", "prepared_context.py"} {
		writeTestFile(t, filepath.Join(plugin, "hooks", name), "# "+name+"\n")
	}
	writeTestFile(t, filepath.Join(plugin, "scripts", "project_scope.py"), "def resolve_scope_id(*_args): return 'scope'\n")
	writeTestFile(t, filepath.Join(plugin, "skills", "project-context", "SKILL.md"), "${POWERCONTEXT_PYTHON} ${POWERCONTEXT_PROJECT_SCOPE_SCRIPT}\n")
	resolved, err := resolvePath(plugin)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func writeWorkBuddyTestJSON(t *testing.T, path string, value map[string]any) {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, path, string(payload))
}

func readWorkBuddyJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(payload, &value); err != nil {
		t.Fatal(err)
	}
	return value
}
