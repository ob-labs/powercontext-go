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
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type workBuddyDiagnosticTransport struct {
	paths []string
}

func (t *workBuddyDiagnosticTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	t.paths = append(t.paths, request.URL.Path)
	payload := `{"status":"ok"}`
	if request.URL.Path == "/health/ready" {
		payload = `{"status":"ready","checks":{}}`
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(payload)),
		Request:    request,
	}, nil
}

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

	var doctorOutput, doctorStderr bytes.Buffer
	transport := &workBuddyDiagnosticTransport{}
	doctorCommand := newCommandWithAllDependencies(
		VersionInfo{Version: "0.0.1"}, &doctorOutput, &doctorStderr, &http.Client{Transport: transport}, nil, &scriptedSystemCommands{t: t},
	)
	doctorCommand.SetArgs([]string{"doctor", "workbuddy", "--json"})
	doctorErr := doctorCommand.ExecuteContext(t.Context())
	if doctorErr != nil {
		t.Fatal(doctorErr)
	}
	payload := decodeSystemOutput(t, doctorOutput.String())
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
		servers[workBuddyPluginName].(map[string]any)["url"] != "http://127.0.0.1:8000/mcp" {
		t.Fatalf("MCP = %#v", mcp)
	}
}

func TestSetupWorkBuddyWritesCredentialFreeConfiguration(t *testing.T) {
	checkout := filepath.Join(t.TempDir(), "powercontext")
	writeWorkBuddyPlugin(t, checkout)
	home := filepath.Join(t.TempDir(), "workbuddy")
	t.Setenv("WORKBUDDY_HOME", home)
	t.Setenv("POWERCONTEXT_HOME", filepath.Join(t.TempDir(), "data"))

	stdout, _, err := executeSystemCLI(t, nil, &scriptedSystemCommands{t: t},
		"setup", "workbuddy", "--source", checkout, "--server-url", "http://127.0.0.1:8765/", "--json")
	if err != nil {
		t.Fatal(err)
	}
	result := decodeSystemOutput(t, stdout)
	if result["server_url"] != "http://127.0.0.1:8765" || result["scope_mode"] != "project" {
		t.Fatalf("setup output = %#v", result)
	}

	configuration := readWorkBuddyJSON(t, filepath.Join(home, "powercontext.json"))
	if fmt.Sprint(configuration["schema"]) != "1" ||
		configuration["server_url"] != "http://127.0.0.1:8765" ||
		configuration["authorization_environment"] != "POWERCONTEXT_WORKBUDDY_AUTHORIZATION" ||
		configuration["request_timeout_seconds"] != float64(1.5) ||
		configuration["request_budget_seconds"] != float64(3) ||
		configuration["prepare_max_bytes"] != float64(8000) ||
		configuration["source_max_bytes"] != float64(16384) {
		t.Fatalf("configuration = %#v", configuration)
	}
	payload, err := os.ReadFile(filepath.Join(home, "powercontext.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "Bearer") || strings.Contains(string(payload), "token") {
		t.Fatalf("configuration persisted a credential: %s", payload)
	}
	mcp := readWorkBuddyJSON(t, filepath.Join(home, "mcp.json"))
	entry := mcp["mcpServers"].(map[string]any)[workBuddyPluginName].(map[string]any)
	if entry["url"] != "http://127.0.0.1:8765/mcp" ||
		entry["headers"].(map[string]any)["Authorization"] != "${POWERCONTEXT_WORKBUDDY_AUTHORIZATION:-}" {
		t.Fatalf("MCP entry = %#v", entry)
	}
}

func TestSetupWorkBuddyRefreshesOwnedCustomAuthorizationEnvironment(t *testing.T) {
	checkout := filepath.Join(t.TempDir(), "powercontext")
	writeWorkBuddyPlugin(t, checkout)
	home := filepath.Join(t.TempDir(), "workbuddy")
	t.Setenv("WORKBUDDY_HOME", home)
	t.Setenv("POWERCONTEXT_HOME", filepath.Join(t.TempDir(), "data"))

	for range 2 {
		if _, _, err := executeSystemCLI(t, nil, &scriptedSystemCommands{t: t},
			"setup", "workbuddy", "--source", checkout, "--authorization-environment", "WORKBUDDY_TOKEN"); err != nil {
			t.Fatal(err)
		}
	}
	entry := readWorkBuddyJSON(t, filepath.Join(home, "mcp.json"))["mcpServers"].(map[string]any)[workBuddyPluginName].(map[string]any)
	if entry["headers"].(map[string]any)["Authorization"] != "${WORKBUDDY_TOKEN:-}" {
		t.Fatalf("MCP entry = %#v", entry)
	}
}

func TestSetupWorkBuddyRejectsRemotePlaintextBeforePythonLookup(t *testing.T) {
	commands := &scriptedSystemCommands{t: t}

	_, _, err := executeSystemCLI(t, nil, commands, "setup", "workbuddy", "--server-url", "http://198.51.100.8:8000")
	if err == nil || !strings.Contains(err.Error(), "loopback") || len(commands.lookups) != 0 {
		t.Fatalf("setup error = %v, lookups = %v", err, commands.lookups)
	}
}

func TestSetupWorkBuddyPreservesRemoteAuthenticatedMCPConfiguration(t *testing.T) {
	checkout := filepath.Join(t.TempDir(), "powercontext")
	writeWorkBuddyPlugin(t, checkout)
	home := filepath.Join(t.TempDir(), "workbuddy")
	t.Setenv("WORKBUDDY_HOME", home)
	t.Setenv("POWERCONTEXT_HOME", filepath.Join(t.TempDir(), "data"))
	original := map[string]any{
		"mcpServers": map[string]any{workBuddyPluginName: map[string]any{
			"type": "http", "url": "https://memory.example.test/mcp",
			"headers": map[string]any{"Authorization": "Bearer existing-token"}, "disabled": true,
		}},
	}
	writeWorkBuddyTestJSON(t, filepath.Join(home, "mcp.json"), original)

	if _, _, err := executeSystemCLI(t, nil, &scriptedSystemCommands{t: t}, "setup", "workbuddy", "--source", checkout); err != nil {
		t.Fatal(err)
	}
	entry := readWorkBuddyJSON(t, filepath.Join(home, "mcp.json"))["mcpServers"].(map[string]any)[workBuddyPluginName].(map[string]any)
	if entry["url"] != "https://memory.example.test/mcp" ||
		entry["headers"].(map[string]any)["Authorization"] != "Bearer existing-token" ||
		entry["disabled"] != false {
		t.Fatalf("MCP after setup = %#v", entry)
	}
	configuration, readErr := os.ReadFile(filepath.Join(home, workBuddyConfigFilename))
	if readErr != nil || strings.Contains(string(configuration), "existing-token") {
		t.Fatalf("credential-free configuration = %q, error = %v", configuration, readErr)
	}
}

func TestSetupWorkBuddyMigratesLegacyMCPEntry(t *testing.T) {
	checkout := filepath.Join(t.TempDir(), "powercontext")
	writeWorkBuddyPlugin(t, checkout)
	home := filepath.Join(t.TempDir(), "workbuddy")
	t.Setenv("WORKBUDDY_HOME", home)
	t.Setenv("POWERCONTEXT_HOME", filepath.Join(t.TempDir(), "data"))
	writeWorkBuddyTestJSON(t, filepath.Join(home, "mcp.json"), map[string]any{
		"mcpServers": map[string]any{workBuddyPluginName: map[string]any{
			"type": "http", "url": workBuddyLegacyMCPURL, "headers": map[string]any{},
			"description": workBuddyMCPDescription, "disabled": false,
		}},
	})

	if _, _, err := executeSystemCLI(t, nil, &scriptedSystemCommands{t: t}, "setup", "workbuddy", "--source", checkout); err != nil {
		t.Fatal(err)
	}
	entry := readWorkBuddyJSON(t, filepath.Join(home, "mcp.json"))["mcpServers"].(map[string]any)[workBuddyPluginName].(map[string]any)
	if entry["url"] != "http://127.0.0.1:8000/mcp" ||
		entry["headers"].(map[string]any)["Authorization"] != workBuddyAuthorizationTemplate ||
		entry["disabled"] != false {
		t.Fatalf("MCP entry = %#v", entry)
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

func TestDoctorWorkBuddyChecksTheConfiguredServerReadiness(t *testing.T) {
	checkout := filepath.Join(t.TempDir(), "powercontext")
	writeWorkBuddyPlugin(t, checkout)
	home := filepath.Join(t.TempDir(), "workbuddy")
	t.Setenv("WORKBUDDY_HOME", home)
	t.Setenv("POWERCONTEXT_HOME", filepath.Join(t.TempDir(), "data"))
	commands := &scriptedSystemCommands{t: t}
	serverURL := "http://127.0.0.1:8000"
	if _, _, err := executeSystemCLI(t, nil, commands, "setup", "workbuddy", "--source", checkout, "--server-url", serverURL); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	transport := &workBuddyDiagnosticTransport{}
	command := newCommandWithAllDependencies(
		VersionInfo{Version: "0.0.1"}, &stdout, &stderr, &http.Client{Transport: transport}, nil, commands,
	)
	command.SetArgs([]string{"doctor", "workbuddy", "--json"})
	err := command.ExecuteContext(t.Context())
	if err != nil {
		t.Fatalf("doctor error = %v, output = %s", err, stdout.String())
	}
	checks := decodeSystemOutput(t, stdout.String())["checks"].(map[string]any)
	if checks["server_liveness"].(map[string]any)["status"] != "ok" || checks["server_readiness"].(map[string]any)["status"] != "ok" {
		t.Fatalf("doctor checks = %#v", checks)
	}
	if strings.Join(transport.paths, ",") != "/health/live,/health/ready" {
		t.Fatalf("health requests = %v", transport.paths)
	}
}

func TestDoctorWorkBuddyRedactsInvalidCredentialConfiguration(t *testing.T) {
	home := filepath.Join(t.TempDir(), "workbuddy")
	secret := "workbuddy-should-not-appear"
	writeTestFile(t, filepath.Join(home, "powercontext.json"), `{"authorization":"Bearer `+secret+`"}`)
	t.Setenv("WORKBUDDY_HOME", home)

	stdout, stderr, err := executeSystemCLI(t, nil, &scriptedSystemCommands{t: t}, "doctor", "workbuddy", "--json")
	if err == nil {
		t.Fatal("doctor unexpectedly accepted an invalid WorkBuddy configuration")
	}
	if strings.Contains(stdout+stderr+err.Error(), secret) {
		t.Fatalf("doctor exposed configuration secret: stdout=%q stderr=%q error=%v", stdout, stderr, err)
	}
}

func writeWorkBuddyPlugin(t *testing.T, root string) string {
	t.Helper()
	plugin := filepath.Join(root, "integrations", "workbuddy", "plugins", "powercontext")
	for _, name := range []string{"workbuddy_powercontext_hook.py", "workbuddy_settings.py", "prepared_context.py"} {
		writeTestFile(t, filepath.Join(plugin, "hooks", name), "# "+name+"\n")
	}
	writeTestFile(t, filepath.Join(plugin, "powercontext.json.example"), "{}\n")
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
