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
	"encoding/json/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	configBeginMarker = "# >>> powercontext managed configuration >>>"
	configEndMarker   = "# <<< powercontext managed configuration <<<"
)

func TestConfigInitShowAndValidateManageOneCredentialFreeEnvironmentFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "powercontext.env")
	stdout, _, err := executeSystemCLI(
		t, nil, &scriptedSystemCommands{t: t}, "config", "init", "--output", path, "--non-interactive",
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "configuration initialized") {
		t.Fatalf("init output = %q", stdout)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), configBeginMarker) || !strings.Contains(string(content), "POWERCONTEXT_SERVER_DATABASE_URL=") {
		t.Fatalf("configuration content = %q", content)
	}
	updated := strings.Replace(string(content), "OPENROUTER_API_KEY=\"\"", "OPENROUTER_API_KEY=runtime-secret", 1)
	if err := os.WriteFile(path, []byte(updated), 0o600); err != nil {
		t.Fatal(err)
	}

	shown, _, showErr := executeSystemCLI(t, nil, &scriptedSystemCommands{t: t}, "config", "show", "--env-file", path)
	if showErr != nil || !strings.Contains(shown, "OPENROUTER_API_KEY=<redacted>") || strings.Contains(shown, "runtime-secret") {
		t.Fatalf("show output = %q, error = %v", shown, showErr)
	}
	if _, _, validateErr := executeSystemCLI(t, nil, &scriptedSystemCommands{t: t}, "config", "validate", "--env-file", path); validateErr != nil {
		t.Fatal(validateErr)
	}
}

func TestConfigValidateRejectsRelativeSQLitePersistencePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relative.env")
	content := strings.Join([]string{
		configBeginMarker,
		"POWERCONTEXT_SERVER_DATABASE_KIND=sqlite",
		"POWERCONTEXT_SERVER_DATABASE_URL=sqlite+aiosqlite:///relative.db",
		configEndMarker,
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	_, _, err := executeSystemCLI(t, nil, &scriptedSystemCommands{t: t}, "config", "validate", "--env-file", path)
	if err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("validate error = %v", err)
	}
}

func TestConfigInitForceReplacesManagedBlockAndPreservesUnknownAssignments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing.env")
	content := strings.Join([]string{
		"UNRELATED_SETTING=keep",
		configBeginMarker,
		"POWERCONTEXT_SERVER_HTTP_PORT=9999",
		configEndMarker,
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, _, err := executeSystemCLI(
		t, nil, &scriptedSystemCommands{t: t}, "config", "init", "--output", path, "--force", "--non-interactive",
	); err != nil {
		t.Fatal(err)
	}
	updated, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(updated), "UNRELATED_SETTING=keep") || strings.Count(string(updated), configBeginMarker) != 1 ||
		strings.Contains(string(updated), "POWERCONTEXT_SERVER_HTTP_PORT=9999") {
		t.Fatalf("updated configuration = %q", updated)
	}
}

func TestConfigShowRejectsMissingEnvironmentFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.env")
	_, _, err := executeSystemCLI(t, nil, &scriptedSystemCommands{t: t}, "config", "show", "--env-file", path)
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("show error = %v", err)
	}
}

func TestParseConfigEnvironmentAcceptsShellCompatibleAssignments(t *testing.T) {
	values, err := parseConfigEnvironment(strings.Join([]string{
		"export QUOTED=\"value with spaces\"",
		"SINGLE='literal # value'",
		"PLAIN=value # comment",
		"EMPTY=",
		"",
	}, "\n"))
	if err != nil {
		t.Fatal(err)
	}
	if values["QUOTED"] != "value with spaces" || values["SINGLE"] != "literal # value" ||
		values["PLAIN"] != "value" || values["EMPTY"] != "" {
		t.Fatalf("parsed values = %#v", values)
	}
}

func TestConfigShowRedactsStandardSensitiveAssignments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sensitive.env")
	content := strings.Join([]string{
		"POWERCONTEXT_SERVER_DATABASE_KIND=oceanbase",
		"POWERCONTEXT_SERVER_DATABASE_URL='mysql+aoceanbase://root:db-secret@127.0.0.1:2881/powercontext?charset=utf8mb4'",
		"OTEL_EXPORTER_OTLP_HEADERS=authorization=Bearer-demo-secret",
		"PLAIN=value",
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	human, _, humanErr := executeSystemCLI(t, nil, &scriptedSystemCommands{t: t}, "config", "show", "--env-file", path)
	if humanErr != nil {
		t.Fatal(humanErr)
	}
	if strings.Contains(human, "db-secret") || strings.Contains(human, "demo-secret") ||
		!strings.Contains(human, "POWERCONTEXT_SERVER_DATABASE_URL=<redacted>") ||
		!strings.Contains(human, "OTEL_EXPORTER_OTLP_HEADERS=<redacted>") ||
		!strings.Contains(human, "PLAIN=value") {
		t.Fatalf("show output = %q", human)
	}

	machine, _, machineErr := executeSystemCLI(
		t, nil, &scriptedSystemCommands{t: t}, "config", "show", "--env-file", path, "--json",
	)
	if machineErr != nil {
		t.Fatal(machineErr)
	}
	var view map[string]string
	if err := json.Unmarshal([]byte(machine), &view); err != nil {
		t.Fatal(err)
	}
	if view["POWERCONTEXT_SERVER_DATABASE_URL"] != "<redacted>" ||
		view["OTEL_EXPORTER_OTLP_HEADERS"] != "<redacted>" || view["PLAIN"] != "value" {
		t.Fatalf("JSON show output = %#v", view)
	}
}

func TestConfigInitForceTreatsMarkerTextInsideCredentialAsLiteral(t *testing.T) {
	path := filepath.Join(t.TempDir(), "marker-value.env")
	content := renderConfigBlock(map[string]string{"OPENROUTER_API_KEY": configManagedBegin}) + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, _, err := executeSystemCLI(
		t, nil, &scriptedSystemCommands{t: t}, "config", "init", "--output", path, "--force", "--non-interactive",
	); err != nil {
		t.Fatalf("force init with marker-valued credential: %v", err)
	}
}

func TestConfigShowRejectsRepeatedManagedMarkers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repeated-marker.env")
	content := renderConfigBlock(map[string]string{"OPENROUTER_API_KEY": "secret"}) + "\n" + configManagedBegin + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	_, _, err := executeSystemCLI(t, nil, &scriptedSystemCommands{t: t}, "config", "show", "--env-file", path)
	if err == nil || !strings.Contains(err.Error(), "mismatched or repeated") {
		t.Fatalf("show error = %v", err)
	}
}

func TestParseConfigEnvironmentSupportsShellLiteralSyntax(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{name: "tab after export", content: "export\tTOKEN=abc\n", want: "abc"},
		{name: "escaped space", content: "TOKEN=abc\\ #123\n", want: "abc #123"},
		{name: "tab comment boundary", content: "TOKEN=abc\t#comment\n", want: "abc"},
		{name: "single quoted expansion", content: "TOKEN='$ROOT/data'\n", want: "$ROOT/data"},
		{name: "escaped expansion", content: "TOKEN=\\$ROOT/data\n", want: "$ROOT/data"},
		{name: "hash starts value", content: "TOKEN=#literal\n", want: "#literal"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			values, err := parseConfigEnvironment(test.content)
			if err != nil {
				t.Fatal(err)
			}
			if values["TOKEN"] != test.want {
				t.Fatalf("TOKEN = %q, want %q", values["TOKEN"], test.want)
			}
		})
	}
}

func TestParseConfigEnvironmentRejectsExpansionAndControlInput(t *testing.T) {
	for _, value := range []string{"$ROOT/data", "${ROOT}/data", "$(command)", "`command`", "~/data", "value\x00tail"} {
		t.Run(value, func(t *testing.T) {
			if _, err := parseConfigEnvironment("TOKEN=" + value + "\n"); err == nil {
				t.Fatalf("accepted unsafe value %q", value)
			}
		})
	}
}

func TestConfigInitForceRemovesStandaloneManagedAssignments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.env")
	content := "UNRELATED_SETTING=keep\nPOWERCONTEXT_SERVER_HTTP_PORT=9999\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, _, err := executeSystemCLI(
		t, nil, &scriptedSystemCommands{t: t}, "config", "init", "--output", path, "--force", "--non-interactive",
	); err != nil {
		t.Fatal(err)
	}
	updated, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(updated), "UNRELATED_SETTING=keep") ||
		strings.Count(string(updated), "POWERCONTEXT_SERVER_HTTP_PORT=") != 1 {
		t.Fatalf("updated configuration = %q", updated)
	}
	if _, parseErr := parseConfigEnvironment(string(updated)); parseErr != nil {
		t.Fatalf("updated configuration is not parseable: %v", parseErr)
	}
}

func TestConfigValidateAcceptsSupportedNonSQLiteAndPartialSettings(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{
			name: "OceanBase",
			content: "POWERCONTEXT_SERVER_DATABASE_KIND=oceanbase\n" +
				"POWERCONTEXT_SERVER_DATABASE_URL='mysql+aoceanbase://root:test@127.0.0.1:2881/powercontext?charset=utf8mb4'\n",
		},
		{name: "partial defaults", content: "POWERCONTEXT_SERVER_HTTP_HOST=127.0.0.1\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "valid.env")
			if err := os.WriteFile(path, []byte(test.content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := executeSystemCLI(
				t, nil, &scriptedSystemCommands{t: t}, "config", "validate", "--env-file", path,
			); err != nil {
				t.Fatalf("validate supported configuration: %v", err)
			}
		})
	}
}

func TestConfigShowRejectsInvalidUTF8(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid.env")
	if err := os.WriteFile(path, []byte("TOKEN=\xff\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, _, err := executeSystemCLI(t, nil, &scriptedSystemCommands{t: t}, "config", "show", "--env-file", path)
	if err == nil || !strings.Contains(err.Error(), "UTF-8") {
		t.Fatalf("show error = %v", err)
	}
}
