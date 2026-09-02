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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupWithoutSubcommandPrintsHelpAndInstallsNothing(t *testing.T) {
	commands := &scriptedSystemCommands{t: t}
	stdout, _, err := executeSystemCLIWithInput(t, commands, strings.NewReader(""), "setup")
	if err == nil || ExitCode(err) != 2 {
		t.Fatalf("setup error = %v, exit = %d", err, ExitCode(err))
	}
	if !strings.Contains(stdout, "Usage:") || !strings.Contains(stdout, "Install and configure PowerContext integrations.") {
		t.Fatalf("setup output = %q", stdout)
	}
	assertNoSetupCommands(t, commands)
}

func TestSetupSelectJSONRequiresHostBeforeInstalling(t *testing.T) {
	commands := &scriptedSystemCommands{t: t}
	_, _, err := executeSystemCLIWithInput(t, commands, strings.NewReader(""), "setup", "select", "--json")
	if err == nil || ExitCode(err) != 1 || !strings.Contains(err.Error(), "--host") {
		t.Fatalf("setup select error = %v, exit = %d", err, ExitCode(err))
	}
	assertNoSetupCommands(t, commands)
}

func TestSetupSelectNonTTYRequiresHostBeforeInstalling(t *testing.T) {
	commands := &scriptedSystemCommands{t: t}
	_, _, err := executeSystemCLIWithInput(t, commands, strings.NewReader(""), "setup", "select")
	if err == nil || ExitCode(err) != 1 || !strings.Contains(err.Error(), "--host") {
		t.Fatalf("setup select error = %v, exit = %d", err, ExitCode(err))
	}
	assertNoSetupCommands(t, commands)
}

func TestSetupSelectTreatsNonTerminalFilesAsNonTTY(t *testing.T) {
	for _, test := range []struct {
		name string
		open func(*testing.T) *os.File
	}{
		{
			name: "null device",
			open: func(t *testing.T) *os.File {
				input, err := os.Open(os.DevNull)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = input.Close() })
				return input
			},
		},
		{
			name: "pipe",
			open: func(t *testing.T) *os.File {
				input, output, err := os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				if err := output.Close(); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = input.Close() })
				return input
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			commands := &scriptedSystemCommands{t: t}
			_, _, err := executeSystemCLIWithInput(t, commands, test.open(t), "setup", "select")
			if err == nil || ExitCode(err) != 1 || !strings.Contains(err.Error(), "--host") {
				t.Fatalf("setup select error = %v, exit = %d", err, ExitCode(err))
			}
			assertNoSetupCommands(t, commands)
		})
	}
}

func TestSetupSelectRejectsUnknownHostBeforeInstalling(t *testing.T) {
	commands := &scriptedSystemCommands{t: t}
	_, _, err := executeSystemCLIWithInput(
		t, commands, strings.NewReader(""), "setup", "select", "--host", "unknown",
	)
	if err == nil || ExitCode(err) != 1 || !strings.Contains(err.Error(), "unknown host: unknown") ||
		!strings.Contains(err.Error(), "codex") {
		t.Fatalf("setup select error = %v, exit = %d", err, ExitCode(err))
	}
	assertNoSetupCommands(t, commands)
}

func TestSetupSelectInstallsOnlyRequestedHostsAndDeduplicatesFlags(t *testing.T) {
	t.Setenv("POWERCONTEXT_HOME", filepath.Join(t.TempDir(), "data"))
	commands := successfulCodexCommands(t, 1)
	stdout, _, err := executeSystemCLIWithInput(
		t, commands, strings.NewReader(""),
		"setup", "select", "--host", "codex", "--host", "codex", "--json",
	)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout, "Select hosts") || strings.Contains(stdout, "Next:") {
		t.Fatalf("JSON output contains interactive or human text: %q", stdout)
	}
	rows := setupRowsByHost(t, stdout)
	assertSetupRow(t, rows, "codex", "installed", "")
	for _, host := range []string{"claude-code", "dsh", "openclaw", "opencode", "pi", "hermes"} {
		assertSetupRow(t, rows, host, "skipped", "")
	}
	if got := fmt.Sprint(commands.lookups); got != "[codex codex]" {
		t.Fatalf("PATH lookups = %s", got)
	}
	if len(commands.calls) != 3 {
		t.Fatalf("external commands = %v", commands.calls)
	}
}

func TestSetupSelectContinuesAfterSelectedHostFails(t *testing.T) {
	t.Setenv("POWERCONTEXT_HOME", filepath.Join(t.TempDir(), "data"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(t.TempDir(), "claude"))
	commands := successfulClaudeCommands(t)
	stdout, _, err := executeSystemCLIWithInput(
		t, commands, strings.NewReader(""),
		"setup", "select", "--host", "codex", "--host", "claude-code", "--json",
	)
	if err == nil || !ErrorAlreadyReported(err) || ExitCode(err) != 1 {
		t.Fatalf("setup select error = %v, exit = %d", err, ExitCode(err))
	}
	rows := setupRowsByHost(t, stdout)
	assertSetupRow(t, rows, "codex", "failed", "Codex CLI is not installed or is not on PATH")
	assertSetupRow(t, rows, "claude-code", "installed", "")
	for _, host := range []string{"dsh", "openclaw", "opencode", "pi", "hermes"} {
		assertSetupRow(t, rows, host, "skipped", "")
	}
	if got := fmt.Sprint(commands.lookups); got != "[codex claude]" {
		t.Fatalf("PATH lookups = %s", got)
	}
}

func TestSetupSelectHumanReportIncludesFailureAndContinues(t *testing.T) {
	t.Setenv("POWERCONTEXT_HOME", filepath.Join(t.TempDir(), "data"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(t.TempDir(), "claude"))
	commands := successfulClaudeCommands(t)
	stdout, _, err := executeSystemCLIWithInput(
		t, commands, strings.NewReader(""), "setup", "select", "--host", "codex", "--host", "claude-code",
	)
	if err == nil || !ErrorAlreadyReported(err) || ExitCode(err) != 1 {
		t.Fatalf("setup select error = %v, exit = %d", err, ExitCode(err))
	}
	for _, fragment := range []string{
		"codex: failed - Codex CLI is not installed or is not on PATH",
		"claude-code: installed",
		"dsh: skipped",
		"Next:",
	} {
		if !strings.Contains(stdout, fragment) {
			t.Fatalf("setup select output %q does not contain %q", stdout, fragment)
		}
	}
}

func TestSetupSelectReportsSuccessfulRerunAsInstalled(t *testing.T) {
	t.Setenv("POWERCONTEXT_HOME", filepath.Join(t.TempDir(), "data"))
	commands := successfulCodexCommands(t, 2)
	for attempt := range 2 {
		stdout, _, err := executeSystemCLIWithInput(
			t, commands, strings.NewReader(""), "setup", "select", "--host", "codex", "--json",
		)
		if err != nil {
			t.Fatalf("attempt %d: %v", attempt+1, err)
		}
		assertSetupRow(t, setupRowsByHost(t, stdout), "codex", "installed", "")
	}
}

func TestSetupSelectContinuesAfterPostInstallVerificationFails(t *testing.T) {
	t.Setenv("POWERCONTEXT_HOME", filepath.Join(t.TempDir(), "data"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(t.TempDir(), "claude"))
	commands := successfulClaudeCommands(t)
	commands.paths["codex"] = "/usr/bin/codex"
	commands.results = append([]systemCommandResult{
		{output: `{"marketplaceName":"powercontext","alreadyAdded":false}`},
		{output: `{"name":"powercontext","version":"0.1.0"}`},
		{output: `{"installed":[]}`},
	}, commands.results...)
	stdout, _, err := executeSystemCLIWithInput(
		t, commands, strings.NewReader(""),
		"setup", "select", "--host", "codex", "--host", "claude-code", "--json",
	)
	if err == nil || !ErrorAlreadyReported(err) || ExitCode(err) != 1 {
		t.Fatalf("setup select error = %v, exit = %d", err, ExitCode(err))
	}
	rows := setupRowsByHost(t, stdout)
	assertSetupRow(
		t, rows, "codex", "failed",
		"post-install verification failed: plugin: PowerContext plugin is not installed",
	)
	assertSetupRow(t, rows, "claude-code", "installed", "")
}

func TestSetupSelectFailsOpenCodeRowWhenActivationVerificationFails(t *testing.T) {
	checkout := filepath.Join(t.TempDir(), "checkout")
	plugin := writeOpenCodePlugin(t, checkout)
	config := filepath.Join(t.TempDir(), "config")
	t.Setenv("POWERCONTEXT_HOME", filepath.Join(t.TempDir(), "data"))
	base := &scriptedSystemCommands{
		t: t, paths: map[string]string{"opencode": "/usr/bin/opencode"},
		results: []systemCommandResult{
			{output: "1.18.21\n"},
			{output: "config " + config + "\n"},
			{output: "1.18.21\n"},
			{output: "config " + config + "\n"},
			{output: fmt.Sprintf(`{"plugin":[%q]}`, plugin)},
		},
	}
	commands := &environmentAwareCommands{scriptedSystemCommands: base}
	commands.runEnv = func(context.Context, map[string]string, string, ...string) ([]byte, error) {
		return nil, nil
	}
	stdout, _, err := executeSystemCLIWithInput(
		t, commands, strings.NewReader(""),
		"setup", "select", "--host", "opencode", "--source", checkout, "--json",
	)
	if err == nil || !ErrorAlreadyReported(err) || ExitCode(err) != 1 {
		t.Fatalf("setup select error = %v, exit = %d", err, ExitCode(err))
	}
	assertSetupRow(
		t, setupRowsByHost(t, stdout), "opencode", "failed",
		"post-install verification failed: plugin: PowerContext OpenCode plugin is configured but did not activate",
	)
}

func TestSetupSelectFailsRowsWhenPostInstallVerificationFails(t *testing.T) {
	for _, test := range []struct {
		name    string
		host    string
		prepare func(*testing.T) (systemCommandExecutor, []string)
		want    string
	}{
		{
			name: "dsh", host: "dsh", want: "post-install verification failed: plugin: PowerContext DSH plugin is not installed",
			prepare: func(t *testing.T) (systemCommandExecutor, []string) {
				checkout := filepath.Join(t.TempDir(), "checkout")
				plugin := filepath.Join(checkout, "integrations", "dsh", "plugins", "powercontext")
				writeTestFile(t, filepath.Join(plugin, "package.json"), `{"name":"powercontext-dsh"}`)
				writeTestFile(t, filepath.Join(plugin, "lib", "index.js"), "export default {}\n")
				t.Setenv("POWERCONTEXT_HOME", filepath.Join(t.TempDir(), "data"))
				commands := &scriptedSystemCommands{
					t: t, paths: map[string]string{"dsh": "/usr/bin/dsh"},
					results: []systemCommandResult{{}, {output: "plugins:\n"}},
				}
				return commands, []string{"--source", checkout}
			},
		},
		{
			name: "pi", host: "pi", want: "post-install verification failed: package: PowerContext Pi package is not installed",
			prepare: func(t *testing.T) (systemCommandExecutor, []string) {
				checkout := filepath.Join(t.TempDir(), "checkout")
				writePiPackage(t, checkout)
				t.Setenv("POWERCONTEXT_HOME", filepath.Join(t.TempDir(), "data"))
				commands := &scriptedSystemCommands{
					t: t, paths: map[string]string{"pi": "/usr/bin/pi"},
					results: []systemCommandResult{{}, {output: "User packages:\n"}, {output: "User packages:\n"}},
				}
				return commands, []string{"--source", checkout}
			},
		},
		{
			name: "hermes", host: "hermes", want: "post-install verification failed: plugin: provider doctor failed",
			prepare: func(t *testing.T) (systemCommandExecutor, []string) {
				checkout := filepath.Join(t.TempDir(), "checkout")
				writeHermesPlugin(t, checkout)
				t.Setenv("HERMES_HOME", filepath.Join(t.TempDir(), "hermes"))
				t.Setenv("POWERCONTEXT_HOME", filepath.Join(t.TempDir(), "data"))
				commands := &scriptedSystemCommands{
					t: t, paths: map[string]string{"hermes": "/usr/bin/hermes"},
					results: []systemCommandResult{
						{output: "Hermes Agent v0.20.4\n"},
						{},
						{},
						{},
						{output: "Hermes Agent v0.20.4\n"},
						{err: errors.New("provider doctor failed")},
					},
				}
				return commands, []string{"--source", checkout}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			commands, extra := test.prepare(t)
			arguments := []string{"setup", "select", "--host", test.host, "--json"}
			arguments = append(arguments, extra...)
			stdout, _, err := executeSystemCLIWithInput(t, commands, strings.NewReader(""), arguments...)
			if err == nil || !ErrorAlreadyReported(err) || ExitCode(err) != 1 {
				t.Fatalf("setup select error = %v, exit = %d", err, ExitCode(err))
			}
			assertSetupRow(t, setupRowsByHost(t, stdout), test.host, "failed", test.want)
		})
	}
}

func TestSetupSelectPrintsHermesNextStepOnlyWhenInstalled(t *testing.T) {
	checkout := filepath.Join(t.TempDir(), "checkout")
	writeHermesPlugin(t, checkout)
	t.Setenv("HERMES_HOME", filepath.Join(t.TempDir(), "hermes"))
	t.Setenv("POWERCONTEXT_HOME", filepath.Join(t.TempDir(), "data"))
	hermesCommands := &scriptedSystemCommands{
		t: t, paths: map[string]string{"hermes": "/usr/bin/hermes"},
		results: []systemCommandResult{
			{output: "Hermes Agent v0.20.4\n"}, {}, {}, {}, {output: "Hermes Agent v0.20.4\n"}, {}, {},
		},
	}
	installed, _, err := executeSystemCLIWithInput(
		t, hermesCommands, strings.NewReader(""),
		"setup", "select", "--host", "hermes", "--source", checkout,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(installed, "`hermes memory setup`") {
		t.Fatalf("Hermes setup output = %q", installed)
	}

	codexCommands := successfulCodexCommands(t, 1)
	other, _, err := executeSystemCLIWithInput(
		t, codexCommands, strings.NewReader(""), "setup", "select", "--host", "codex",
	)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(other, "`hermes memory setup`") {
		t.Fatalf("non-Hermes setup output = %q", other)
	}
}

func TestSetupSelectPassesSourceRefAndClaudeOptions(t *testing.T) {
	config := filepath.Join(t.TempDir(), "claude")
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	t.Setenv("POWERCONTEXT_HOME", filepath.Join(t.TempDir(), "data"))
	commands := successfulClaudeCommands(t)
	stdout, _, err := executeSystemCLIWithInput(
		t, commands, strings.NewReader(""),
		"setup", "select", "--host", "claude-code",
		"--source", "ob-labs/custom-powercontext", "--ref", "tested-ref",
		"--server-url", "https://memory.example", "--no-capture-prompts", "--json",
	)
	if err != nil {
		t.Fatal(err)
	}
	assertSetupRow(t, setupRowsByHost(t, stdout), "claude-code", "installed", "")
	calls := strings.Join(commandCallStrings(commands.calls), "\n")
	if !strings.Contains(calls, "plugin marketplace add ob-labs/custom-powercontext@tested-ref --scope user") {
		t.Fatalf("Claude commands = %s", calls)
	}
	content, err := os.ReadFile(filepath.Join(config, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal(content, &settings); err != nil {
		t.Fatal(err)
	}
	options := settings["pluginConfigs"].(map[string]any)[claudePluginID].(map[string]any)["options"].(map[string]any)
	if options["server_url"] != "https://memory.example" || options["capture_prompts"] != false {
		t.Fatalf("Claude options = %#v", options)
	}
}

func TestSetupSelectPreservesExplicitBlankServerURL(t *testing.T) {
	for _, test := range []struct {
		host     string
		want     string
		commands func(*testing.T) *scriptedSystemCommands
	}{
		{
			host: "claude-code", want: "PowerContext Server URL must use HTTP or HTTPS",
			commands: successfulClaudeCommands,
		},
		{
			host: "openclaw", want: "OpenClaw PowerContext Server URL must use HTTP or HTTPS",
			commands: func(t *testing.T) *scriptedSystemCommands {
				return &scriptedSystemCommands{
					t: t, paths: map[string]string{"openclaw": "/usr/bin/openclaw"},
					results: []systemCommandResult{{output: "OpenClaw 2026.8.1-beta.2\n"}},
				}
			},
		},
	} {
		t.Run(test.host, func(t *testing.T) {
			t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(t.TempDir(), "claude"))
			t.Setenv("POWERCONTEXT_HOME", filepath.Join(t.TempDir(), "data"))
			commands := test.commands(t)
			stdout, _, err := executeSystemCLIWithInput(
				t, commands, strings.NewReader(""),
				"setup", "select", "--host", test.host, "--server-url", "", "--json",
			)
			if err == nil || !ErrorAlreadyReported(err) || ExitCode(err) != 1 {
				t.Fatalf("setup select error = %v, exit = %d", err, ExitCode(err))
			}
			assertSetupRow(t, setupRowsByHost(t, stdout), test.host, "failed", test.want)
			if len(commands.calls) != 0 {
				t.Fatalf("explicit blank server URL ran external commands: %v", commands.calls)
			}
		})
	}
}

func TestSetupSelectPassesServerAndScopeOverridesToOpenClaw(t *testing.T) {
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
			{output: `[]`},
			{},
			{},
		},
	}
	stdout, _, err := executeSystemCLIWithInput(
		t, commands, strings.NewReader(""),
		"setup", "select", "--host", "openclaw", "--source", checkout, "--ref", "tested-ref",
		"--server-url", "https://memory.example", "--scope-mode", "project", "--json",
	)
	if err != nil {
		t.Fatal(err)
	}
	assertSetupRow(t, setupRowsByHost(t, stdout), "openclaw", "installed", "")
	var settings []map[string]any
	if call := commands.calls[5]; len(call.arguments) != 4 ||
		json.Unmarshal([]byte(call.arguments[3]), &settings) != nil {
		t.Fatalf("OpenClaw batch settings command = %v", call)
	}
	values := make(map[string]any, len(settings))
	for _, setting := range settings {
		values[setting["path"].(string)] = setting["value"]
	}
	if values["plugins.entries.memory-powercontext.config.endpoint"] != "https://memory.example" ||
		values["plugins.entries.memory-powercontext.config.scopeMode"] != "project" {
		t.Fatalf("OpenClaw settings = %#v", values)
	}
}

func TestSetupSelectReadsTTYSelectionByNumber(t *testing.T) {
	t.Setenv("POWERCONTEXT_HOME", filepath.Join(t.TempDir(), "data"))
	commands := successfulCodexCommands(t, 1)
	stdout, _, err := executeSystemCLIWithInput(
		t, commands, setupTTYInput{Reader: strings.NewReader("1\n")}, "setup", "select",
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{
		"Official first-class integrations:", "1) Codex (codex)", "3) DeepSeek Harness (dsh)",
		"Select hosts", "codex: installed", "claude-code: skipped", "Next:",
	} {
		if !strings.Contains(stdout, fragment) {
			t.Fatalf("setup select output %q does not contain %q", stdout, fragment)
		}
	}
}

func TestSetupSelectCancelsEmptyTTYSelection(t *testing.T) {
	commands := &scriptedSystemCommands{t: t}
	stdout, _, err := executeSystemCLIWithInput(
		t, commands, setupTTYInput{Reader: strings.NewReader("\n")}, "setup", "select",
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "Select hosts") || strings.Contains(stdout, "codex: skipped") {
		t.Fatalf("setup select cancel output = %q", stdout)
	}
	assertNoSetupCommands(t, commands)
}

func TestSetupSelectRejectsInvalidTTYTokenBeforeInstalling(t *testing.T) {
	commands := &scriptedSystemCommands{t: t}
	_, _, err := executeSystemCLIWithInput(
		t, commands, setupTTYInput{Reader: strings.NewReader("nope\n")}, "setup", "select",
	)
	if err == nil || !strings.Contains(err.Error(), "unknown host: nope") {
		t.Fatalf("setup select error = %v", err)
	}
	assertNoSetupCommands(t, commands)
}

func TestParseSetupHostSelectionAcceptsNamesAndCatalogNumbers(t *testing.T) {
	for _, test := range []struct {
		input string
		want  string
	}{
		{input: "dsh,codex", want: "[codex dsh]"},
		{input: "4", want: "[openclaw]"},
		{input: "5", want: "[opencode]"},
		{input: "7", want: "[hermes]"},
		{input: "", want: "[]"},
		{input: "  ", want: "[]"},
	} {
		got, err := parseSetupHostSelection(test.input)
		if err != nil || fmt.Sprint(got) != test.want {
			t.Errorf("parseSetupHostSelection(%q) = %v, %v; want %s", test.input, got, err, test.want)
		}
	}
}

func executeSystemCLIWithInput(
	t *testing.T,
	commands systemCommandExecutor,
	input io.Reader,
	arguments ...string,
) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	command := newCommandWithAllDependencies(
		VersionInfo{Version: "0.0.1"}, &stdout, &stderr, nil, nil, commands,
	)
	command.SetIn(input)
	command.SetArgs(arguments)
	err := command.ExecuteContext(t.Context())
	return stdout.String(), stderr.String(), err
}

func assertNoSetupCommands(t *testing.T, commands *scriptedSystemCommands) {
	t.Helper()
	if len(commands.lookups) != 0 || len(commands.calls) != 0 {
		t.Fatalf("setup inspected PATH or ran commands: lookups=%v calls=%v", commands.lookups, commands.calls)
	}
}

func setupRowsByHost(t *testing.T, output string) map[string]map[string]any {
	t.Helper()
	payload := decodeSystemOutput(t, output)
	values, ok := payload["hosts"].([]any)
	if !ok || len(values) != len(firstClassIntegrationHosts) {
		t.Fatalf("setup select payload = %#v", payload)
	}
	rows := make(map[string]map[string]any, len(values))
	for _, value := range values {
		row, ok := value.(map[string]any)
		if !ok {
			t.Fatalf("setup select row = %#v", value)
		}
		host, _ := row["host"].(string)
		rows[host] = row
	}
	return rows
}

func assertSetupRow(t *testing.T, rows map[string]map[string]any, host, status, detail string) {
	t.Helper()
	row := rows[host]
	if row["host"] != host || row["status"] != status {
		t.Fatalf("setup row %s = %#v", host, row)
	}
	got, _ := row["error"].(string)
	if got != detail {
		t.Fatalf("setup row %s error = %q, want %q", host, got, detail)
	}
}

type setupTTYInput struct {
	*strings.Reader
}

func (setupTTYInput) SetupInputIsTerminal() bool { return true }

func successfulCodexCommands(t *testing.T, repetitions int) *scriptedSystemCommands {
	t.Helper()
	results := make([]systemCommandResult, 0, repetitions*3)
	for range repetitions {
		results = append(results,
			systemCommandResult{output: `{"marketplaceName":"powercontext","alreadyAdded":false}`},
			systemCommandResult{output: `{"name":"powercontext","version":"0.1.0"}`},
			systemCommandResult{output: `{"installed":[{"name":"powercontext","pluginId":"powercontext@powercontext","installed":true,"enabled":true}]}`},
		)
	}
	return &scriptedSystemCommands{t: t, paths: map[string]string{"codex": "/usr/bin/codex"}, results: results}
}

func successfulClaudeCommands(t *testing.T) *scriptedSystemCommands {
	t.Helper()
	return &scriptedSystemCommands{
		t: t, paths: map[string]string{"claude": "/usr/bin/claude"},
		results: []systemCommandResult{
			{output: `[]`},
			{output: `[]`},
			{},
			{},
			{output: `[{"id":"powercontext@powercontext","version":"0.1.0","enabled":true}]`},
		},
	}
}
