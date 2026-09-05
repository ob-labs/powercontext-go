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
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
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

func TestSetupSelectRequiresHostWithoutTTYOrWithJSON(t *testing.T) {
	for _, arguments := range [][]string{{"setup", "select"}, {"setup", "select", "--json"}} {
		commands := &scriptedSystemCommands{t: t}
		_, _, err := executeSystemCLIWithInput(t, commands, strings.NewReader(""), arguments...)
		if err == nil || !strings.Contains(err.Error(), "--host") {
			t.Fatalf("setup select %v error = %v", arguments, err)
		}
		assertNoSetupCommands(t, commands)
	}
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
			if err == nil || !strings.Contains(err.Error(), "--host") {
				t.Fatalf("setup select error = %v", err)
			}
			assertNoSetupCommands(t, commands)
		})
	}
}

func TestSetupSelectRejectsUnknownHostBeforeInstalling(t *testing.T) {
	commands := &scriptedSystemCommands{t: t}
	_, _, err := executeSystemCLIWithInput(t, commands, strings.NewReader(""), "setup", "select", "--host", "unknown")
	if err == nil || !strings.Contains(err.Error(), "unknown host: unknown") || !strings.Contains(err.Error(), "codex, workbuddy") {
		t.Fatalf("setup select error = %v", err)
	}
	assertNoSetupCommands(t, commands)
}

func TestSetupSelectRejectsUnsupportedPublishedHostsBeforeInstalling(t *testing.T) {
	for _, host := range []string{"claude-code", "dsh", "hermes", "openclaw", "opencode", "pi"} {
		commands := &scriptedSystemCommands{t: t}
		_, _, err := executeSystemCLIWithInput(t, commands, strings.NewReader(""), "setup", "select", "--host", host)
		var usage *UsageError
		var unsupported *UnsupportedIntegrationError
		if !errors.As(err, &usage) || !errors.As(err, &unsupported) || ExitCode(err) != 2 {
			t.Fatalf("setup select %q error = %T %v, want typed usage refusal", host, err, err)
		}
		assertNoSetupCommands(t, commands)
	}
}

func TestSetupSelectInstallsOnlyRequestedHostsAndDeduplicatesFlags(t *testing.T) {
	t.Setenv("POWERCONTEXT_HOME", filepath.Join(t.TempDir(), "data"))
	commands := successfulCodexCommands(t, 1)
	stdout, _, err := executeSystemCLIWithInput(
		t, commands, strings.NewReader(""), "setup", "select", "--host", "codex", "--host", "codex", "--json",
	)
	if err != nil {
		t.Fatal(err)
	}
	rows := setupRowsByHost(t, stdout)
	assertSetupRow(t, rows, "codex", "installed", "")
	assertSetupRow(t, rows, "workbuddy", "skipped", "")
	if !slices.Equal(commands.lookups, []string{"codex", "codex"}) || len(commands.calls) != 3 {
		t.Fatalf("Codex setup commands = lookups %v calls %v", commands.lookups, commands.calls)
	}
}

func TestSetupSelectReportsSuccessfulCodexRerunAsInstalled(t *testing.T) {
	t.Setenv("POWERCONTEXT_HOME", filepath.Join(t.TempDir(), "data"))
	commands := successfulCodexCommands(t, 2)
	for attempt := range 2 {
		stdout, _, err := executeSystemCLIWithInput(t, commands, strings.NewReader(""), "setup", "select", "--host", "codex", "--json")
		if err != nil {
			t.Fatalf("attempt %d: %v", attempt+1, err)
		}
		assertSetupRow(t, setupRowsByHost(t, stdout), "codex", "installed", "")
	}
}

func TestSetupSelectInstallsWorkBuddyWithServerOverride(t *testing.T) {
	useWorkBuddyReleaseBinary(t)
	checkout := filepath.Join(t.TempDir(), "powercontext")
	writeWorkBuddyPlugin(t, checkout)
	home := filepath.Join(t.TempDir(), "workbuddy")
	t.Setenv("WORKBUDDY_HOME", home)
	t.Setenv("POWERCONTEXT_HOME", filepath.Join(t.TempDir(), "data"))
	commands := &scriptedSystemCommands{t: t}

	stdout, _, err := executeSystemCLIWithInput(
		t, commands, strings.NewReader(""), "setup", "select", "--host", "workbuddy",
		"--source", checkout, "--server-url", "https://memory.example", "--json",
	)
	if err != nil {
		t.Fatal(err)
	}
	rows := setupRowsByHost(t, stdout)
	assertSetupRow(t, rows, "codex", "skipped", "")
	assertSetupRow(t, rows, "workbuddy", "installed", "")
	configuration, present, readErr := readWorkBuddyConfiguration(filepath.Join(home, workBuddyConfigFilename))
	if readErr != nil || !present || configuration.ServerURL != "https://memory.example" {
		t.Fatalf("WorkBuddy configuration = %#v, %v, present=%t", configuration, readErr, present)
	}
	assertNoSetupCommands(t, commands)
}

func TestSetupSelectRejectsBlankWorkBuddyServerURLBeforeInstalling(t *testing.T) {
	commands := &scriptedSystemCommands{t: t}
	stdout, _, err := executeSystemCLIWithInput(
		t, commands, strings.NewReader(""), "setup", "select", "--host", "workbuddy", "--server-url", "", "--json",
	)
	if err == nil || !ErrorAlreadyReported(err) {
		t.Fatalf("setup select error = %v", err)
	}
	assertSetupRow(t, setupRowsByHost(t, stdout), "workbuddy", "failed", "WorkBuddy PowerContext Server URL must use HTTP or HTTPS")
	assertNoSetupCommands(t, commands)
}

func TestSetupSelectReadsTTYSelectionByNumber(t *testing.T) {
	t.Setenv("POWERCONTEXT_HOME", filepath.Join(t.TempDir(), "data"))
	commands := successfulCodexCommands(t, 1)
	stdout, _, err := executeSystemCLIWithInput(t, commands, setupTTYInput{Reader: strings.NewReader("1\n")}, "setup", "select")
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{
		"Official first-class integrations:", "1) Codex (codex)", "2) WorkBuddy (workbuddy)",
		"Select hosts", "codex: installed", "workbuddy: skipped", "Next:",
	} {
		if !strings.Contains(stdout, fragment) {
			t.Fatalf("setup select output %q does not contain %q", stdout, fragment)
		}
	}
}

func TestSetupSelectCancelsEmptyTTYSelection(t *testing.T) {
	commands := &scriptedSystemCommands{t: t}
	stdout, _, err := executeSystemCLIWithInput(t, commands, setupTTYInput{Reader: strings.NewReader("\n")}, "setup", "select")
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
	_, _, err := executeSystemCLIWithInput(t, commands, setupTTYInput{Reader: strings.NewReader("nope\n")}, "setup", "select")
	if err == nil || !strings.Contains(err.Error(), "unknown host: nope") {
		t.Fatalf("setup select error = %v", err)
	}
	assertNoSetupCommands(t, commands)
}

func TestParseSetupHostSelectionAcceptsSupportedNamesAndCatalogNumbers(t *testing.T) {
	for _, test := range []struct {
		input string
		want  []string
	}{
		{input: "workbuddy,codex", want: []string{"codex", "workbuddy"}},
		{input: "1", want: []string{"codex"}},
		{input: "2", want: []string{"workbuddy"}},
		{input: "", want: nil},
		{input: "  ", want: nil},
	} {
		got, err := parseSetupHostSelection(test.input)
		if err != nil || !slices.Equal(got, test.want) {
			t.Errorf("parseSetupHostSelection(%q) = %v, %v; want %v", test.input, got, err, test.want)
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
