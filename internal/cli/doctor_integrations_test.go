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
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestDoctorIntegrationsJSONIncludesEveryFirstClassHost(t *testing.T) {
	setMissingWorkBuddyHome(t)
	stdout, _, err := executeSystemCLI(
		t, nil, &scriptedSystemCommands{t: t}, "doctor", "integrations", "--json",
	)
	if err != nil {
		t.Fatal(err)
	}
	payload := decodeSystemOutput(t, stdout)
	if payload["ok"] != true || payload["status"] != "ok" {
		t.Fatalf("doctor integrations summary = %#v", payload)
	}
	hosts := payload["hosts"].(map[string]any)
	if len(hosts) != 2 || hosts["codex"] == nil || hosts["workbuddy"] == nil {
		t.Fatalf("doctor integrations hosts = %#v, want only Codex and WorkBuddy", hosts)
	}
	for _, spec := range firstClassIntegrationHosts {
		host := hosts[spec.name].(map[string]any)
		if host["presence"] != "missing" || host[spec.cliKey] == nil {
			t.Fatalf("host %s = %#v", spec.name, host)
		}
		for _, key := range spec.integrationKeys {
			if host[key] == nil {
				t.Fatalf("host %s does not include %s: %#v", spec.name, key, host)
			}
		}
	}
	assertOrderedFragments(t, stdout,
		`"codex"`, `"workbuddy"`,
	)
}

func TestDoctorIntegrationsTreatsMissingCLIsAsSuccess(t *testing.T) {
	setMissingWorkBuddyHome(t)
	commands := &scriptedSystemCommands{
		t: t, paths: map[string]string{"codex": "/usr/bin/codex"},
		results: []systemCommandResult{{
			output: `{"installed":[{"name":"powercontext","pluginId":"powercontext@powercontext","installed":true,"enabled":true}]}`,
		}},
	}
	stdout, _, err := executeSystemCLI(t, nil, commands, "doctor", "integrations", "--json")
	if err != nil {
		t.Fatal(err)
	}
	payload := decodeSystemOutput(t, stdout)
	hosts := payload["hosts"].(map[string]any)
	codex := hosts["codex"].(map[string]any)
	workBuddy := hosts["workbuddy"].(map[string]any)
	if payload["ok"] != true || payload["status"] != "ok" || codex["presence"] != "present" ||
		codex["plugin"].(map[string]any)["ok"] != true || workBuddy["presence"] != "missing" ||
		workBuddy["hooks"].(map[string]any)["status"] != "skipped" {
		t.Fatalf("doctor integrations = %#v", payload)
	}
}

func TestDoctorIntegrationsPrintsHumanMatrix(t *testing.T) {
	setMissingWorkBuddyHome(t)
	commands := &scriptedSystemCommands{
		t: t, paths: map[string]string{"codex": "/usr/bin/codex"},
		results: []systemCommandResult{{
			output: `{"installed":[{"name":"powercontext","pluginId":"powercontext@powercontext","installed":true,"enabled":true}]}`,
		}},
	}
	stdout, _, err := executeSystemCLI(t, nil, commands, "doctor", "integrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{
		"codex: present - cli=ok plugin=ok",
		"workbuddy: missing - config=failed hooks=skipped settings=skipped mcp=skipped skill=skipped",
	} {
		if !strings.Contains(stdout, line+"\n") {
			t.Fatalf("doctor integrations output %q does not contain %q", stdout, line)
		}
	}
	assertOrderedFragments(t, stdout, "codex:", "workbuddy:")
	for _, unsupported := range []string{"claude-code:", "dsh:", "hermes:", "openclaw:", "opencode:", "pi:"} {
		if strings.Contains(stdout, unsupported) {
			t.Fatalf("doctor integrations output contains unsupported host %q: %s", unsupported, stdout)
		}
	}
}

func TestDoctorIntegrationsFailsWhenPresentPluginIsBroken(t *testing.T) {
	setMissingWorkBuddyHome(t)
	commands := &scriptedSystemCommands{
		t: t, paths: map[string]string{"codex": "/usr/bin/codex"},
		results: []systemCommandResult{{output: `{"installed":[]}`}},
	}
	stdout, _, err := executeSystemCLI(t, nil, commands, "doctor", "integrations", "--json")
	if err == nil || !ErrorAlreadyReported(err) || ExitCode(err) != 1 {
		t.Fatalf("doctor integrations error = %v, exit = %d", err, ExitCode(err))
	}
	payload := decodeSystemOutput(t, stdout)
	hosts := payload["hosts"].(map[string]any)
	codex := hosts["codex"].(map[string]any)
	if payload["ok"] != false || payload["status"] != "failed" || codex["presence"] != "present" ||
		codex["plugin"].(map[string]any)["status"] != "failed" ||
		hosts["workbuddy"].(map[string]any)["presence"] != "missing" {
		t.Fatalf("doctor integrations = %#v", payload)
	}
}

func TestDoctorIntegrationsFailsWhenPresentCLICannotListPlugins(t *testing.T) {
	setMissingWorkBuddyHome(t)
	commands := &scriptedSystemCommands{
		t: t, paths: map[string]string{"codex": "/usr/bin/codex"},
		results: []systemCommandResult{{err: errors.New("codex plugin list failed: timeout")}},
	}
	stdout, _, err := executeSystemCLI(t, nil, commands, "doctor", "integrations", "--json")
	if err == nil || !ErrorAlreadyReported(err) || ExitCode(err) != 1 {
		t.Fatalf("doctor integrations error = %v, exit = %d", err, ExitCode(err))
	}
	codex := decodeSystemOutput(t, stdout)["hosts"].(map[string]any)["codex"].(map[string]any)
	if codex["presence"] != "present" || codex["codex"].(map[string]any)["status"] != "failed" ||
		codex["plugin"].(map[string]any)["status"] != "skipped" ||
		strings.Contains(codex["codex"].(map[string]any)["detail"].(string), "is not installed or is not on PATH") {
		t.Fatalf("codex diagnostics = %#v", codex)
	}
}

func TestDoctorIntegrationsSucceedsWhenEveryHostIsMissing(t *testing.T) {
	setMissingWorkBuddyHome(t)
	stdout, _, err := executeSystemCLI(t, nil, &scriptedSystemCommands{t: t}, "doctor", "integrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{
		"codex: missing - cli=failed plugin=skipped",
		"workbuddy: missing - config=failed hooks=skipped settings=skipped mcp=skipped skill=skipped",
	} {
		if !strings.Contains(stdout, line) {
			t.Fatalf("doctor integrations output %q does not contain %q", stdout, line)
		}
	}
}

func setMissingWorkBuddyHome(t *testing.T) {
	t.Helper()
	t.Setenv("WORKBUDDY_HOME", filepath.Join(t.TempDir(), "workbuddy"))
}

func assertOrderedFragments(t *testing.T, value string, fragments ...string) {
	t.Helper()
	last := -1
	for _, fragment := range fragments {
		index := strings.Index(value, fragment)
		if index <= last {
			t.Fatalf("%q is not after the previous fragment in %q", fragment, value)
		}
		last = index
	}
}
