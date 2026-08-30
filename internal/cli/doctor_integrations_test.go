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
	"strings"
	"testing"
)

func TestDoctorIntegrationsJSONIncludesEveryFirstClassHost(t *testing.T) {
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
	if len(hosts) != len(firstClassIntegrationHosts) {
		t.Fatalf("doctor integrations hosts = %d, want %d", len(hosts), len(firstClassIntegrationHosts))
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
		`"codex"`, `"claude-code"`, `"dsh"`, `"openclaw"`, `"opencode"`, `"pi"`, `"hermes"`,
	)
}

func TestDoctorIntegrationsTreatsMissingCLIsAsSuccess(t *testing.T) {
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
	pi := hosts["pi"].(map[string]any)
	if payload["ok"] != true || payload["status"] != "ok" || codex["presence"] != "present" ||
		codex["plugin"].(map[string]any)["ok"] != true || pi["presence"] != "missing" ||
		pi["package"].(map[string]any)["status"] != "skipped" {
		t.Fatalf("doctor integrations = %#v", payload)
	}
}

func TestDoctorIntegrationsPrintsHumanMatrix(t *testing.T) {
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
		"claude-code: missing - cli=failed plugin=skipped",
		"opencode: missing - cli=failed plugin=skipped skill=skipped",
		"pi: missing - cli=failed package=skipped",
	} {
		if !strings.Contains(stdout, line+"\n") {
			t.Fatalf("doctor integrations output %q does not contain %q", stdout, line)
		}
	}
	assertOrderedFragments(t, stdout, "codex:", "claude-code:", "dsh:", "openclaw:", "opencode:", "pi:", "hermes:")
}

func TestDoctorIntegrationsFailsWhenPresentPluginIsBroken(t *testing.T) {
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
		hosts["dsh"].(map[string]any)["presence"] != "missing" {
		t.Fatalf("doctor integrations = %#v", payload)
	}
}

func TestDoctorIntegrationsFailsWhenPresentCLICannotListPlugins(t *testing.T) {
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

func TestDoctorIntegrationsFailsWhenPresentOpenCodeSkillIsBroken(t *testing.T) {
	config := t.TempDir()
	plugin := writeOpenCodePlugin(t, t.TempDir())
	base := &scriptedSystemCommands{
		t: t, paths: map[string]string{"opencode": "/usr/bin/opencode"},
		results: []systemCommandResult{
			{output: "1.18.21\n"},
			{output: "config " + config + "\n"},
			{output: fmt.Sprintf(`{"plugin":[%q]}`, plugin)},
		},
	}
	commands := &environmentAwareCommands{scriptedSystemCommands: base}
	commands.runEnv = func(
		ctx context.Context,
		environment map[string]string,
		executable string,
		arguments ...string,
	) ([]byte, error) {
		output, err := base.Run(ctx, executable, arguments...)
		if err == nil {
			if writeErr := os.WriteFile(environment[openCodeProbePath], []byte(environment[openCodeProbeNonce]), 0o600); writeErr != nil {
				t.Fatal(writeErr)
			}
		}
		return output, err
	}
	stdout, _, err := executeSystemCLI(t, nil, commands, "doctor", "integrations", "--json")
	if err == nil || !ErrorAlreadyReported(err) || ExitCode(err) != 1 {
		t.Fatalf("doctor integrations error = %v, exit = %d", err, ExitCode(err))
	}
	opencode := decodeSystemOutput(t, stdout)["hosts"].(map[string]any)["opencode"].(map[string]any)
	if opencode["presence"] != "present" || opencode["plugin"].(map[string]any)["status"] != "ok" ||
		opencode["skill"].(map[string]any)["status"] != "failed" {
		t.Fatalf("OpenCode diagnostics = %#v", opencode)
	}
	if _, statErr := os.Stat(filepath.Join(config, "skills", "project-context", "SKILL.md")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("doctor integrations mutated OpenCode Skill: %v", statErr)
	}
}

func TestDoctorIntegrationsSucceedsWhenEveryHostIsMissing(t *testing.T) {
	stdout, _, err := executeSystemCLI(t, nil, &scriptedSystemCommands{t: t}, "doctor", "integrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{
		"codex: missing - cli=failed plugin=skipped",
		"hermes: missing - cli=failed plugin=skipped",
	} {
		if !strings.Contains(stdout, line) {
			t.Fatalf("doctor integrations output %q does not contain %q", stdout, line)
		}
	}
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
