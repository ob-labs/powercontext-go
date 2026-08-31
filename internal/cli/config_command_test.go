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
