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

package main

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

const validSecurityDefaults = `
POWERCONTEXT_SERVER_HTTP_HOST=127.0.0.1
POWERCONTEXT_SERVER_AUTH_ENABLED=false
POWERCONTEXT_SERVER_ALLOW_UNAUTHENTICATED_NON_LOOPBACK=false
POWERCONTEXT_CLIENT_SERVER_URL=http://127.0.0.1:8000
`

func TestReleaseSecurityDefaultsAcceptRepositoryExample(t *testing.T) {
	if err := verifySecurityDefaults(filepath.Join("..", "..", ".env.example")); err != nil {
		t.Fatalf("verify repository security defaults: %v", err)
	}
}

func TestWriteFailureDiagnosticsRedactsPrivateValuesAndBoundsOutput(t *testing.T) {
	report := filepath.Join(t.TempDir(), "diagnostics.txt")
	root := filepath.Join(t.TempDir(), "private-root")
	cause := errors.New(strings.Join([]string{
		"startup failed at " + root,
		smokeScope,
		smokeText,
		smokeToken,
		strings.Repeat("界", maximumFailureDiagnosticBytes),
	}, "\n"))
	if err := writeFailureDiagnostics(report, root, cause); err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(report)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) > maximumFailureDiagnosticBytes {
		t.Fatalf("diagnostics length = %d, want at most %d", len(payload), maximumFailureDiagnosticBytes)
	}
	if !utf8.Valid(payload) {
		t.Fatalf("diagnostics are not valid UTF-8: %q", payload)
	}
	for _, private := range []string{root, smokeScope, smokeText, smokeToken} {
		if strings.Contains(string(payload), private) {
			t.Fatalf("diagnostics leaked %q: %s", private, payload)
		}
	}
	if !strings.Contains(string(payload), "[redacted]") || !strings.Contains(string(payload), "[truncated]") {
		t.Fatalf("diagnostics = %q, want redaction and truncation markers", payload)
	}
}

func TestWriteFailureDiagnosticsPreservesUTF8AtTheTruncationBoundary(t *testing.T) {
	report := filepath.Join(t.TempDir(), "diagnostics.txt")
	const truncation = "\n[truncated]\n"
	limit := maximumFailureDiagnosticBytes - len(truncation)
	prefix := "status=failed\n"
	cause := errors.New(strings.Repeat("x", limit-len(prefix)-1) + "界" + strings.Repeat("x", len(truncation)+1))
	if err := writeFailureDiagnostics(report, "", cause); err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(report)
	if err != nil {
		t.Fatal(err)
	}
	if !utf8.Valid(payload) {
		t.Fatalf("diagnostics are not valid UTF-8: %q", payload)
	}
}

func TestReleaseSecurityDefaultsRejectUnsafeOrAmbiguousValues(t *testing.T) {
	tests := []struct {
		name       string
		contents   string
		wantField  string
		secretText string
	}{
		{
			name:      "remote Server host",
			contents:  strings.Replace(validSecurityDefaults, "127.0.0.1", "0.0.0.0", 1),
			wantField: "POWERCONTEXT_SERVER_HTTP_HOST",
		},
		{
			name:      "authentication enabled by default",
			contents:  strings.Replace(validSecurityDefaults, "AUTH_ENABLED=false", "AUTH_ENABLED=true", 1),
			wantField: "POWERCONTEXT_SERVER_AUTH_ENABLED",
		},
		{
			name:      "non-loopback opt-in enabled",
			contents:  strings.Replace(validSecurityDefaults, "NON_LOOPBACK=false", "NON_LOOPBACK=true", 1),
			wantField: "POWERCONTEXT_SERVER_ALLOW_UNAUTHENTICATED_NON_LOOPBACK",
		},
		{
			name:      "remote plaintext Client URL",
			contents:  strings.Replace(validSecurityDefaults, "http://127.0.0.1:8000", "http://memory.example:8000", 1),
			wantField: "POWERCONTEXT_CLIENT_SERVER_URL",
		},
		{
			name:       "active example bearer token",
			contents:   validSecurityDefaults + "POWERCONTEXT_SERVER_AUTH_TOKEN=secret-sentinel\n",
			wantField:  "POWERCONTEXT_SERVER_AUTH_TOKEN",
			secretText: "secret-sentinel",
		},
		{
			name:       "exported example bearer token",
			contents:   validSecurityDefaults + "export POWERCONTEXT_SERVER_AUTH_TOKEN=secret-sentinel\n",
			wantField:  "POWERCONTEXT_SERVER_AUTH_TOKEN",
			secretText: "secret-sentinel",
		},
		{
			name:       "tab-exported example bearer token",
			contents:   validSecurityDefaults + "export\tPOWERCONTEXT_SERVER_AUTH_TOKEN=secret-sentinel\n",
			wantField:  "POWERCONTEXT_SERVER_AUTH_TOKEN",
			secretText: "secret-sentinel",
		},
		{
			name:       "duplicate Server host",
			contents:   validSecurityDefaults + "POWERCONTEXT_SERVER_HTTP_HOST=127.0.0.2\n",
			wantField:  "POWERCONTEXT_SERVER_HTTP_HOST",
			secretText: "127.0.0.2",
		},
		{
			name:      "missing Client URL",
			contents:  strings.Replace(validSecurityDefaults, "POWERCONTEXT_CLIENT_SERVER_URL=http://127.0.0.1:8000\n", "", 1),
			wantField: "POWERCONTEXT_CLIENT_SERVER_URL",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ".env.example")
			if err := os.WriteFile(path, []byte(strings.TrimSpace(test.contents)+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			err := verifySecurityDefaults(path)
			if err == nil || !strings.Contains(err.Error(), test.wantField) {
				t.Fatalf("verifySecurityDefaults() error = %v, want field %s", err, test.wantField)
			}
			if test.secretText != "" && strings.Contains(err.Error(), test.secretText) {
				t.Fatalf("verifySecurityDefaults() exposed configured value in %q", err)
			}
		})
	}
}

func TestReleaseSecurityDefaultsReadFailureDoesNotExposePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private-config", ".env.example")
	err := verifySecurityDefaults(path)
	if err == nil {
		t.Fatal("verifySecurityDefaults() accepted a missing file")
	}
	if strings.Contains(err.Error(), path) {
		t.Fatalf("verifySecurityDefaults() exposed local path in %q", err)
	}
}

func TestIsolatedEnvironmentRemovesPowerContextContamination(t *testing.T) {
	source := []string{
		"PATH=/usr/bin",
		"POWERCONTEXT_HOME=/private/user-data",
		"POWERCONTEXT_SERVER_AUTH_TOKEN=secret",
		"POWERCONTEXT_CLIENT_API_TOKEN=secret",
		"UNRELATED=value=with=equals",
		"MALFORMED",
	}
	result := isolatedEnvironment(source, "/isolated/home")
	for _, forbidden := range []string{"/private/user-data", "secret", "MALFORMED"} {
		if strings.Contains(strings.Join(result, "\n"), forbidden) {
			t.Fatalf("isolated environment contains %q: %v", forbidden, result)
		}
	}
	for _, expected := range []string{
		"PATH=/usr/bin",
		"UNRELATED=value=with=equals",
		"POWERCONTEXT_HOME=/isolated/home",
		"POWERCONTEXT_SERVER_LOGGING_FORMAT=json",
		"POWERCONTEXT_SERVER_LOGGING_ACCESS=true",
	} {
		if !slices.Contains(result, expected) {
			t.Fatalf("isolated environment is missing %q: %v", expected, result)
		}
	}
}

func TestFrozenBaseToolNamesAreUnique(t *testing.T) {
	seen := make(map[string]struct{}, len(baseToolNames))
	for _, name := range baseToolNames {
		if _, duplicate := seen[name]; duplicate {
			t.Fatalf("duplicate MCP tool %q", name)
		}
		seen[name] = struct{}{}
	}
	if len(seen) != 20 {
		t.Fatalf("base MCP tools = %d, want 20", len(seen))
	}
}

func TestStoppedServerProcessCanBeStoppedAgain(t *testing.T) {
	log, err := os.CreateTemp(t.TempDir(), "server.log")
	if err != nil {
		t.Fatal(err)
	}
	process := &serverProcess{log: log, done: make(chan struct{})}
	close(process.done)

	for range 2 {
		if err := process.stop(t.Context()); err != nil {
			t.Fatalf("stop completed Server: %v", err)
		}
	}
	if _, err := log.Write([]byte("closed")); err == nil {
		t.Fatal("stop did not close the completed Server log")
	}
}
