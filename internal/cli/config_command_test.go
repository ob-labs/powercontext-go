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
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	configBeginMarker = "# >>> powercontext managed configuration >>>"
	configEndMarker   = "# <<< powercontext managed configuration <<<"
)

func TestConfigInitShowAndValidateManageOneProviderEnvironmentFile(t *testing.T) {
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
	updated := strings.Replace(string(content), "OPENAI_API_KEY=\"\"", "OPENAI_API_KEY=runtime-secret", 1)
	if err := os.WriteFile(path, []byte(updated), 0o600); err != nil {
		t.Fatal(err)
	}

	shown, _, showErr := executeSystemCLI(t, nil, &scriptedSystemCommands{t: t}, "config", "show", "--env-file", path)
	if showErr != nil || !strings.Contains(shown, "OPENAI_API_KEY=<redacted>") || strings.Contains(shown, "runtime-secret") {
		t.Fatalf("show output = %q, error = %v", shown, showErr)
	}
	if _, _, validateErr := executeSystemCLI(t, nil, &scriptedSystemCommands{t: t}, "config", "validate", "--env-file", path); validateErr != nil {
		t.Fatal(validateErr)
	}
}

func TestConfigInitCollectsOpenAIProtocolAndRedactsCredential(t *testing.T) {
	path := filepath.Join(t.TempDir(), "powercontext.env")
	input := strings.NewReader("\n\n\n\n\nshared-secret\n\n\n")
	stdout, stderr, err := executeSystemCLIWithInput(
		t, &scriptedSystemCommands{t: t}, input, "config", "init", "--output", path,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "configuration initialized") ||
		!strings.Contains(stderr, "Generation API protocol") ||
		!strings.Contains(stderr, "Generation API Base URL") ||
		!strings.Contains(stderr, "Generation API key") ||
		!strings.Contains(stderr, "Generation model") {
		t.Fatalf("stdout/stderr = %q / %q", stdout, stderr)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	values, err := parseConfigEnvironment(string(content))
	if err != nil {
		t.Fatal(err)
	}
	if values["OPENAI_API_KEY"] != "shared-secret" || values["OPENAI_BASE_URL"] != "https://api.openai.com/v1" ||
		values["POWERCONTEXT_SERVER_INFERENCE_GENERATION_MODEL"] != "openai-chat:gpt-4.1-mini" {
		t.Fatalf("environment = %#v", values)
	}
	shown, _, showErr := executeSystemCLI(t, nil, &scriptedSystemCommands{t: t}, "config", "show", "--env-file", path)
	if showErr != nil || !strings.Contains(shown, "OPENAI_API_KEY=<redacted>") || strings.Contains(shown, "shared-secret") {
		t.Fatalf("show output = %q, error = %v", shown, showErr)
	}
}

func TestConfigInitRefusesToReplaceExistingEnvironmentWithoutForce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing.env")
	if err := os.WriteFile(path, []byte("EXISTING=value\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, _, err := executeSystemCLI(t, nil, &scriptedSystemCommands{t: t}, "config", "init", "--output", path, "--non-interactive")
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("init error = %v", err)
	}
	content, readErr := os.ReadFile(path)
	if readErr != nil || string(content) != "EXISTING=value\n" {
		t.Fatalf("existing environment = %q, %v", content, readErr)
	}
}

func TestConfigInitRejectsServerInvalidConfigurationBeforeWriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid.env")
	input := strings.NewReader(strings.Join([]string{
		strings.Repeat("p", 256), "", "", "", "", "shared-secret", "", "",
	}, "\n") + "\n")

	_, _, err := executeSystemCLIWithInput(t, &scriptedSystemCommands{t: t}, input, "config", "init", "--output", path)
	if err == nil || !strings.Contains(err.Error(), "Scope ID must contain at most 255 characters") {
		t.Fatalf("init error = %v", err)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("invalid configuration wrote %q: %v", path, statErr)
	}
}

func TestParseConfigEnvironmentRejectsDuplicateAssignments(t *testing.T) {
	_, err := parseConfigEnvironment("VALUE=one\nVALUE=two\n")
	if err == nil || !strings.Contains(err.Error(), "duplicate environment assignment") {
		t.Fatalf("parse error = %v", err)
	}
}

func TestConfigValidateReportsInvalidProviderAwareNumericValueWithoutTraceback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid.env")
	config := defaultProviderAwareConfig()
	block, err := renderProviderAwareConfigBlock(config)
	if err != nil {
		t.Fatal(err)
	}
	content := strings.Replace(block, "POWERCONTEXT_SERVER_INFERENCE_EMBEDDING_DIMENSION=\"1536\"", "POWERCONTEXT_SERVER_INFERENCE_EMBEDDING_DIMENSION=invalid", 1)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	_, stderr, validateErr := executeSystemCLI(t, nil, &scriptedSystemCommands{t: t}, "config", "validate", "--env-file", path)
	if validateErr == nil || !strings.Contains(validateErr.Error(), "must be an integer") || strings.Contains(stderr, "Traceback") {
		t.Fatalf("validate error/stderr = %v / %q", validateErr, stderr)
	}
}

func TestConfigShowRedactsRecordedCustomCredentialAndKeepsUnmarkedValue(t *testing.T) {
	config := defaultProviderAwareConfig()
	config.generation = configModelSelection{
		model: "openai:custom-generation",
		environment: []configProviderVariable{
			{name: "CUSTOM_CREDENTIAL", value: "custom-secret"},
			{name: "AWS_REGION", value: "us-west-2"},
		},
	}
	config.embedding = configModelSelection{
		model:       "openai:custom-embedding",
		environment: []configProviderVariable{{name: "CUSTOM_CREDENTIAL", value: "custom-secret"}},
	}
	config.credentials = []string{"CUSTOM_CREDENTIAL"}
	block, err := renderProviderAwareConfigBlock(config)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "custom.env")
	if err := os.WriteFile(path, []byte(block), 0o600); err != nil {
		t.Fatal(err)
	}

	shown, _, showErr := executeSystemCLI(t, nil, &scriptedSystemCommands{t: t}, "config", "show", "--env-file", path)
	if showErr != nil || !strings.Contains(shown, "CUSTOM_CREDENTIAL=<redacted>") ||
		strings.Contains(shown, "custom-secret") || !strings.Contains(shown, "AWS_REGION=us-west-2") {
		t.Fatalf("show output = %q, error = %v", shown, showErr)
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

func TestParseConfigEnvironmentMatchesV010EnvironmentFileContract(t *testing.T) {
	tests := []struct {
		name    string
		content string
		key     string
		want    string
		wantErr bool
	}{
		{name: "URL fragment", content: "URL=https://example.com/page#section\n", key: "URL", want: "https://example.com/page#section"},
		{name: "export hash", content: "export BEARER=token#a1\n", key: "BEARER", want: "token#a1"},
		{name: "full line comments", content: "# leading comment\n\nTOKEN=value # explanation\n", key: "TOKEN", want: "value"},
		{name: "even backslash before space", content: "TOKEN=abc\\\\ #comment\n", key: "TOKEN", want: "abc\\"},
		{name: "escaped quote", content: "TOKEN=abc\\\" #comment\n", key: "TOKEN", want: "abc\""},
		{name: "escaped tab", content: "TOKEN=abc\\\t#123\n", key: "TOKEN", want: "abc\t#123"},
		{name: "trailing escaped space", content: "TOKEN=abc\\ \n", key: "TOKEN", want: "abc "},
		{name: "trailing escaped tab", content: "TOKEN=abc\\\t\n", key: "TOKEN", want: "abc\t"},
		{name: "unterminated quote", content: "TOKEN=\"unterminated\n", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			values, err := parseConfigEnvironment(test.content)
			if test.wantErr {
				if err == nil {
					t.Fatal("parseConfigEnvironment accepted an unterminated quote")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(values) != 1 || values[test.key] != test.want {
				t.Fatalf("parsed values = %#v, want %s=%q", values, test.key, test.want)
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
