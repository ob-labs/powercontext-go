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
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type systemCommandCall struct {
	executable string
	arguments  []string
}

func (c systemCommandCall) String() string {
	return strings.Join(append([]string{c.executable}, c.arguments...), " ")
}

type systemCommandResult struct {
	output string
	err    error
	after  func(systemCommandCall)
}

type scriptedSystemCommands struct {
	t       *testing.T
	paths   map[string]string
	lookups []string
	calls   []systemCommandCall
	results []systemCommandResult
}

func (s *scriptedSystemCommands) LookPath(name string) (string, error) {
	s.t.Helper()
	s.lookups = append(s.lookups, name)
	if path := s.paths[name]; path != "" {
		return path, nil
	}
	return "", fmt.Errorf("%s: %w", name, os.ErrNotExist)
}

func (s *scriptedSystemCommands) Run(_ context.Context, executable string, arguments ...string) ([]byte, error) {
	s.t.Helper()
	call := systemCommandCall{executable: executable, arguments: append([]string(nil), arguments...)}
	s.calls = append(s.calls, call)
	if len(s.results) == 0 {
		s.t.Fatalf("unexpected external command: %s", call.String())
	}
	result := s.results[0]
	s.results = s.results[1:]
	if result.after != nil {
		result.after(call)
	}
	return []byte(result.output), result.err
}

func (s *scriptedSystemCommands) RunEnv(
	ctx context.Context,
	_ map[string]string,
	executable string,
	arguments ...string,
) ([]byte, error) {
	return s.Run(ctx, executable, arguments...)
}

func (s *scriptedSystemCommands) RunTimeout(
	ctx context.Context,
	_ time.Duration,
	executable string,
	arguments ...string,
) ([]byte, error) {
	return s.Run(ctx, executable, arguments...)
}

func executeSystemCLI(
	t *testing.T,
	httpClient *http.Client,
	commands systemCommandExecutor,
	arguments ...string,
) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	command := newCommandWithAllDependencies(
		VersionInfo{Version: "0.0.1"}, &stdout, &stderr, httpClient, nil, commands,
	)
	command.SetArgs(arguments)
	err := command.ExecuteContext(context.Background())
	return stdout.String(), stderr.String(), err
}

func decodeSystemOutput(t *testing.T, output string) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal([]byte(output), &value); err != nil {
		t.Fatalf("decode output %q: %v", output, err)
	}
	return value
}

func TestSetupCodexRemoteRefPreparesStorageAndVerifiesPlugin(t *testing.T) {
	home := filepath.Join(t.TempDir(), "data")
	t.Setenv("POWERCONTEXT_HOME", home)
	commands := &scriptedSystemCommands{
		t:     t,
		paths: map[string]string{"codex": "/usr/bin/codex"},
		results: []systemCommandResult{
			{output: `{"marketplaceName":"powercontext","alreadyAdded":false}`},
			{output: `{"name":"powercontext","version":"0.1.0"}`},
			{output: `{"installed":[{"name":"powercontext","pluginId":"powercontext@powercontext","installed":true,"enabled":true}]}`},
		},
	}

	stdout, _, err := executeSystemCLI(t, nil, commands,
		"setup", "codex", "--source", "ob-labs/powercontext-go", "--ref", "tested-ref", "--json")
	if err != nil {
		t.Fatal(err)
	}
	resolvedHome, err := resolvePath(home)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"marketplace": "powercontext", "plugin": "powercontext",
		"plugin_version": "0.1.0", "data_dir": resolvedHome,
	}
	if got := decodeSystemOutput(t, stdout); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("setup output = %#v, want %#v", got, want)
	}
	if info, statErr := os.Stat(resolvedHome); statErr != nil || !info.IsDir() {
		t.Fatalf("data directory was not prepared: %v", statErr)
	}
	wantCalls := []string{
		"codex plugin marketplace add ob-labs/powercontext-go --ref tested-ref --json",
		"codex plugin add powercontext@powercontext --json",
		"codex plugin list --json",
	}
	if got := commandCallStrings(commands.calls); fmt.Sprint(got) != fmt.Sprint(wantCalls) {
		t.Fatalf("commands = %v, want %v", got, wantCalls)
	}
}

func TestSetupCodexLocalMarketplaceOmitsRef(t *testing.T) {
	marketplace := filepath.Join(t.TempDir(), "marketplace")
	if err := os.MkdirAll(marketplace, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("POWERCONTEXT_HOME", filepath.Join(t.TempDir(), "data"))
	commands := &scriptedSystemCommands{
		t: t, paths: map[string]string{"codex": "/usr/bin/codex"},
		results: []systemCommandResult{
			{output: `{"marketplaceName":"powercontext-local"}`},
			{output: `{"name":"powercontext","version":"0.1.0"}`},
			{output: `{"installed":[{"name":"powercontext","pluginId":"powercontext@powercontext-local","installed":true,"enabled":true}]}`},
		},
	}

	if _, _, err := executeSystemCLI(t, nil, commands, "setup", "codex", "--source", marketplace); err != nil {
		t.Fatal(err)
	}
	resolvedMarketplace, err := resolvePath(marketplace)
	if err != nil {
		t.Fatal(err)
	}
	first := commands.calls[0].String()
	if first != "codex plugin marketplace add "+resolvedMarketplace+" --json" || strings.Contains(first, "--ref") {
		t.Fatalf("marketplace command = %q", first)
	}
}

func TestDoctorJSONReportsEveryCheckAndReturnsFailure(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("connection refused")
	})}
	commands := &scriptedSystemCommands{t: t}
	stdout, _, err := executeSystemCLI(t, client, commands, "doctor", "--json")
	if err == nil || !ErrorAlreadyReported(err) || ExitCode(err) != 1 {
		t.Fatalf("doctor error = %v, exit = %d", err, ExitCode(err))
	}
	payload := decodeSystemOutput(t, stdout)
	if payload["ok"] != false || payload["status"] != "failed" {
		t.Fatalf("doctor summary = %#v", payload)
	}
	checks := payload["checks"].(map[string]any)
	if len(checks) != 3 || checks["package"].(map[string]any)["status"] != "ok" ||
		checks["server_liveness"].(map[string]any)["status"] != "failed" ||
		checks["server_readiness"].(map[string]any)["status"] != "skipped" {
		t.Fatalf("doctor checks = %#v", checks)
	}
}

func TestDoctorChecksOnlyServerByDefault(t *testing.T) {
	var paths []string
	client := fakeHTTPClient(func(writer http.ResponseWriter, request *http.Request) {
		paths = append(paths, request.URL.Path)
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/health/live":
			_, _ = writer.Write([]byte(`{"status":"ok"}`))
		case "/health/ready":
			_, _ = writer.Write([]byte(`{"status":"ready","checks":{"runtime":"ready","database":"ready"}}`))
		default:
			t.Fatalf("unexpected diagnostics path %q", request.URL.Path)
		}
	})
	commands := &scriptedSystemCommands{t: t}
	stdout, _, err := executeSystemCLI(t, client, commands, "doctor", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(paths) != "[/health/live /health/ready]" || len(commands.lookups) != 0 || len(commands.calls) != 0 {
		t.Fatalf("HTTP paths = %v, lookups = %v, commands = %v", paths, commands.lookups, commands.calls)
	}
	if payload := decodeSystemOutput(t, stdout); payload["ok"] != true || payload["status"] != "ok" {
		t.Fatalf("doctor output = %#v", payload)
	}
}

func TestDoctorSkipsReadinessWhenLivenessIsUnreachable(t *testing.T) {
	requests := 0
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		requests++
		return nil, errors.New("connection refused")
	})}
	stdout, _, err := executeSystemCLI(t, client, &scriptedSystemCommands{t: t}, "doctor")
	if err == nil || requests != 1 {
		t.Fatalf("doctor error = %v, requests = %d", err, requests)
	}
	for _, text := range []string{
		"server liveness: failed - cannot reach http://127.0.0.1:8000",
		"server readiness: skipped - not checked because Server liveness failed",
	} {
		if !strings.Contains(stdout, text) {
			t.Fatalf("doctor output %q does not contain %q", stdout, text)
		}
	}
}

func TestDoctorPreservesNotReadyChecks(t *testing.T) {
	client := healthDiagnosticClient(t, "not_ready", http.StatusServiceUnavailable,
		map[string]string{"runtime": "ready", "database": "unavailable"})
	human, _, humanErr := executeSystemCLI(t, client, &scriptedSystemCommands{t: t}, "doctor")
	if humanErr == nil || !strings.Contains(human, "server readiness: failed - http://127.0.0.1:8000 status=not_ready") ||
		!strings.Contains(human, "  database: unavailable") {
		t.Fatalf("human output = %q, error = %v", human, humanErr)
	}
	machine, _, machineErr := executeSystemCLI(t, client, &scriptedSystemCommands{t: t}, "doctor", "--json")
	if machineErr == nil {
		t.Fatal("JSON doctor unexpectedly succeeded")
	}
	readiness := decodeSystemOutput(t, machine)["checks"].(map[string]any)["server_readiness"].(map[string]any)
	if readiness["status"] != "failed" || readiness["detail"] != "http://127.0.0.1:8000 status=not_ready" ||
		readiness["checks"].(map[string]any)["database"] != "unavailable" {
		t.Fatalf("readiness = %#v", readiness)
	}
}

func TestDoctorPreservesDegradedChecks(t *testing.T) {
	client := healthDiagnosticClient(t, "degraded", http.StatusOK,
		map[string]string{"runtime": "ready", "database": "ready", "inference.embedding": "misconfigured"})
	human, _, humanErr := executeSystemCLI(t, client, &scriptedSystemCommands{t: t}, "doctor")
	if humanErr == nil || !strings.Contains(human, "server readiness: degraded - http://127.0.0.1:8000 status=degraded") ||
		!strings.Contains(human, "  inference.embedding: misconfigured") {
		t.Fatalf("human output = %q, error = %v", human, humanErr)
	}
	machine, _, machineErr := executeSystemCLI(t, client, &scriptedSystemCommands{t: t}, "doctor", "--json")
	if machineErr == nil {
		t.Fatal("JSON doctor unexpectedly succeeded")
	}
	payload := decodeSystemOutput(t, machine)
	if payload["ok"] != false || payload["status"] != "degraded" {
		t.Fatalf("doctor output = %#v", payload)
	}
}

func TestDoctorCodexReportsMissingCLI(t *testing.T) {
	stdout, _, err := executeSystemCLI(t, nil, &scriptedSystemCommands{t: t}, "doctor", "codex", "--json")
	if err == nil || ExitCode(err) != 1 {
		t.Fatalf("doctor codex error = %v, exit = %d", err, ExitCode(err))
	}
	checks := decodeSystemOutput(t, stdout)["checks"].(map[string]any)
	if checks["codex"].(map[string]any)["detail"] != "Codex CLI is not installed or is not on PATH" ||
		checks["plugin"].(map[string]any)["status"] != "skipped" {
		t.Fatalf("checks = %#v", checks)
	}
}

func TestDoctorCodexRequiresEnabledPlugin(t *testing.T) {
	commands := &scriptedSystemCommands{
		t: t, paths: map[string]string{"codex": "/usr/bin/codex"},
		results: []systemCommandResult{{output: `{"installed":[]}`}},
	}
	stdout, _, err := executeSystemCLI(t, nil, commands, "doctor", "codex")
	if err == nil || !strings.Contains(stdout, "codex: ok - /usr/bin/codex") ||
		!strings.Contains(stdout, "plugin: failed - PowerContext plugin is not installed") {
		t.Fatalf("output = %q, error = %v", stdout, err)
	}
}

func TestSetupDSHLocalCheckoutAndVerifiesPlugin(t *testing.T) {
	checkout := filepath.Join(t.TempDir(), "powercontext")
	plugin := writeDSHPlugin(t, checkout, true)
	home := filepath.Join(t.TempDir(), "data")
	t.Setenv("POWERCONTEXT_HOME", home)
	commands := &scriptedSystemCommands{
		t: t, paths: map[string]string{"dsh": "/usr/bin/dsh"},
		results: []systemCommandResult{{}, {output: "id: powercontext-dsh\n"}},
	}
	stdout, _, err := executeSystemCLI(t, nil, commands, "setup", "dsh", "--source", checkout, "--json")
	if err != nil {
		t.Fatal(err)
	}
	plugin, err = resolvePath(plugin)
	if err != nil {
		t.Fatal(err)
	}
	home, err = resolvePath(home)
	if err != nil {
		t.Fatal(err)
	}
	payload := decodeSystemOutput(t, stdout)
	if payload["plugin"] != dshPluginName || payload["plugin_path"] != plugin || payload["data_dir"] != home {
		t.Fatalf("setup output = %#v", payload)
	}
	want := []string{
		"/usr/bin/dsh plugin --profile web add " + plugin,
		"/usr/bin/dsh --profile web --dump-config",
	}
	if got := commandCallStrings(commands.calls); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("commands = %v, want %v", got, want)
	}
}

func TestSetupDSHReportsMissingCLI(t *testing.T) {
	home := filepath.Join(t.TempDir(), "data")
	t.Setenv("POWERCONTEXT_HOME", home)
	_, _, err := executeSystemCLI(t, nil, &scriptedSystemCommands{t: t},
		"setup", "dsh", "--source", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "DeepSeek Harness CLI is not installed") {
		t.Fatalf("setup error = %v", err)
	}
	if _, statErr := os.Stat(home); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("missing CLI changed data directory: %v", statErr)
	}
}

func TestDoctorDSHReportsMissingCLI(t *testing.T) {
	stdout, _, err := executeSystemCLI(t, nil, &scriptedSystemCommands{t: t}, "doctor", "dsh", "--json")
	if err == nil {
		t.Fatal("doctor dsh unexpectedly succeeded")
	}
	checks := decodeSystemOutput(t, stdout)["checks"].(map[string]any)
	if checks["dsh"].(map[string]any)["detail"] != "DeepSeek Harness CLI is not installed or is not on PATH" ||
		checks["plugin"].(map[string]any)["status"] != "skipped" {
		t.Fatalf("checks = %#v", checks)
	}
}

func TestDoctorDSHRequiresInstalledPlugin(t *testing.T) {
	commands := &scriptedSystemCommands{
		t: t, paths: map[string]string{"dsh": "/usr/bin/dsh"},
		results: []systemCommandResult{{output: "id: other-plugin\n"}},
	}
	stdout, _, err := executeSystemCLI(t, nil, commands, "doctor", "dsh")
	if err == nil || !strings.Contains(stdout, "dsh: ok - /usr/bin/dsh") ||
		!strings.Contains(stdout, "plugin: failed - PowerContext DSH plugin is not installed") {
		t.Fatalf("output = %q, error = %v", stdout, err)
	}
}

func TestDSHExecutablePrefersWindowsCommandShim(t *testing.T) {
	commands := &scriptedSystemCommands{
		t: t, paths: map[string]string{"dsh.cmd": `C:\\bin\\dsh.cmd`, "dsh": `C:\\bin\\dsh.exe`},
	}
	got, err := dshExecutable(commands, "windows")
	if err != nil {
		t.Fatal(err)
	}
	if got != `C:\\bin\\dsh.cmd` || fmt.Sprint(commands.lookups) != "[dsh.cmd]" {
		t.Fatalf("executable = %q, lookups = %v", got, commands.lookups)
	}
}

func TestSetupDSHRejectsMissingBundleBeforeCommand(t *testing.T) {
	checkout := filepath.Join(t.TempDir(), "powercontext")
	writeDSHPlugin(t, checkout, false)
	t.Setenv("POWERCONTEXT_HOME", filepath.Join(t.TempDir(), "data"))
	commands := &scriptedSystemCommands{t: t, paths: map[string]string{"dsh": "/usr/bin/dsh"}}
	_, _, err := executeSystemCLI(t, nil, commands, "setup", "dsh", "--source", checkout)
	if err == nil || !strings.Contains(err.Error(), "lib/index.js") || len(commands.calls) != 0 {
		t.Fatalf("setup error = %v, commands = %v", err, commands.calls)
	}
}

func TestResolveDSHPluginRejectsEscapingRef(t *testing.T) {
	_, err := resolveDSHPlugin(
		context.Background(), &scriptedSystemCommands{t: t},
		"ob-labs/powercontext-go", "../../etc", filepath.Join(t.TempDir(), "data"),
	)
	if err == nil || !strings.Contains(err.Error(), "invalid DeepSeek Harness ref") {
		t.Fatalf("resolve error = %v", err)
	}
}

func TestResolveDSHPluginReplacesBrokenCheckout(t *testing.T) {
	home := filepath.Join(t.TempDir(), "data")
	stale := filepath.Join(home, "checkouts", "dsh", "main")
	if err := os.MkdirAll(stale, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stale, "README"), []byte("incomplete"), 0o644); err != nil {
		t.Fatal(err)
	}
	commands := &scriptedSystemCommands{
		t: t,
		results: []systemCommandResult{{after: func(call systemCommandCall) {
			writeDSHPlugin(t, call.arguments[len(call.arguments)-1], true)
		}}},
	}
	plugin, err := resolveDSHPlugin(
		context.Background(), commands, "https://github.com/ob-labs/powercontext-go", "main", home,
	)
	if err != nil {
		t.Fatal(err)
	}
	wantPlugin := filepath.Join(stale, "integrations", "dsh", "plugins", "powercontext")
	wantPlugin, err = resolvePath(wantPlugin)
	if err != nil {
		t.Fatal(err)
	}
	stale, err = resolvePath(stale)
	if err != nil {
		t.Fatal(err)
	}
	if plugin != wantPlugin {
		t.Fatalf("plugin = %q, want %q", plugin, wantPlugin)
	}
	wantCommand := "git clone --depth 1 --branch main https://github.com/ob-labs/powercontext-go.git " + stale
	if got := commands.calls[0].String(); got != wantCommand {
		t.Fatalf("clone command = %q, want %q", got, wantCommand)
	}
	if _, statErr := os.Stat(filepath.Join(stale, "README")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("broken checkout was not replaced: %v", statErr)
	}
}

func TestDoctorDSHRequiresPluginID(t *testing.T) {
	commands := &scriptedSystemCommands{
		t: t, paths: map[string]string{"dsh": "/usr/bin/dsh"},
		results: []systemCommandResult{{output: "name: powercontext-dsh\n"}},
	}
	stdout, _, err := executeSystemCLI(t, nil, commands, "doctor", "dsh")
	if err == nil || !strings.Contains(stdout, "plugin: failed - PowerContext DSH plugin is not installed") {
		t.Fatalf("output = %q, error = %v", stdout, err)
	}
}

func TestDoctorDSHReportsInstalledPlugin(t *testing.T) {
	commands := &scriptedSystemCommands{
		t: t, paths: map[string]string{"dsh": "/usr/bin/dsh"},
		results: []systemCommandResult{{output: "- id: powercontext-dsh\n  name: powercontext-dsh\n"}},
	}
	stdout, _, err := executeSystemCLI(t, nil, commands, "doctor", "dsh", "--json")
	if err != nil {
		t.Fatal(err)
	}
	payload := decodeSystemOutput(t, stdout)
	plugin := payload["checks"].(map[string]any)["plugin"].(map[string]any)
	if payload["ok"] != true || plugin["ok"] != true || plugin["status"] != "ok" ||
		plugin["detail"] != "powercontext-dsh is installed" {
		t.Fatalf("doctor output = %#v", payload)
	}
}

func TestSetupRequiresPostInstallHostVerification(t *testing.T) {
	t.Run("codex", func(t *testing.T) {
		t.Setenv("POWERCONTEXT_HOME", filepath.Join(t.TempDir(), "data"))
		commands := &scriptedSystemCommands{
			t: t, paths: map[string]string{"codex": "/usr/bin/codex"},
			results: []systemCommandResult{
				{output: `{"marketplaceName":"powercontext"}`},
				{output: `{"name":"powercontext","version":"0.1.0"}`},
				{output: `{"installed":[]}`},
			},
		}
		stdout, _, err := executeSystemCLI(t, nil, commands, "setup", "codex", "--json")
		if err == nil || !ErrorAlreadyReported(err) {
			t.Fatalf("setup error = %v", err)
		}
		payload := decodeSystemOutput(t, stdout)
		if payload["status"] != "failed" || payload["checks"].(map[string]any)["plugin"].(map[string]any)["status"] != "failed" {
			t.Fatalf("diagnostics = %#v", payload)
		}
	})

	t.Run("dsh", func(t *testing.T) {
		checkout := filepath.Join(t.TempDir(), "powercontext")
		writeDSHPlugin(t, checkout, true)
		t.Setenv("POWERCONTEXT_HOME", filepath.Join(t.TempDir(), "data"))
		commands := &scriptedSystemCommands{
			t: t, paths: map[string]string{"dsh": "/usr/bin/dsh"},
			results: []systemCommandResult{{}, {output: "id: other-plugin\n"}},
		}
		stdout, _, err := executeSystemCLI(t, nil, commands,
			"setup", "dsh", "--source", checkout, "--json")
		if err == nil || !ErrorAlreadyReported(err) {
			t.Fatalf("setup error = %v", err)
		}
		payload := decodeSystemOutput(t, stdout)
		if payload["status"] != "failed" || payload["checks"].(map[string]any)["plugin"].(map[string]any)["status"] != "failed" {
			t.Fatalf("diagnostics = %#v", payload)
		}
	})
}

func TestCheckoutTargetRejectsExistingSymlinkEscape(t *testing.T) {
	root := filepath.Join(t.TempDir(), "checkouts", "dsh")
	outside := t.TempDir()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := checkoutTarget(root, filepath.Join("escape", "victim")); err == nil {
		t.Fatal("checkout target followed a symlink outside its trusted root")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("outside directory was changed: %v", err)
	}
}

func TestDoctorExplicitEmptyURLIsDiagnosed(t *testing.T) {
	stdout, _, err := executeSystemCLI(t, nil, &scriptedSystemCommands{t: t},
		"--server-url", "", "doctor", "--json")
	if err == nil {
		t.Fatal("doctor with an empty URL unexpectedly succeeded")
	}
	checks := decodeSystemOutput(t, stdout)["checks"].(map[string]any)
	liveness := checks["server_liveness"].(map[string]any)
	if liveness["detail"] != "Server URL must be an HTTP base URL without credentials or query data" {
		t.Fatalf("liveness = %#v", liveness)
	}
}

func TestDoctorStrictlyValidatesFrozenHealthSchemas(t *testing.T) {
	client := fakeHTTPClient(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/health/live":
			_, _ = writer.Write([]byte(`{"status":"warming"}`))
		case "/health/ready":
			_, _ = writer.Write([]byte(`{"status":"ready"}`))
		}
	})
	live := requestDiagnostic(context.Background(), diagnosticHTTPClient(client),
		"http://powercontext.test", "/health/live", false)
	if live.OK || live.Status != "failed" || live.Detail != "http://powercontext.test status=warming" {
		t.Fatalf("liveness = %#v", live)
	}
	ready := requestDiagnostic(context.Background(), diagnosticHTTPClient(client),
		"http://powercontext.test", "/health/ready", true)
	if ready.Detail != "readiness returned an invalid response" {
		t.Fatalf("readiness = %#v", ready)
	}
}

func healthDiagnosticClient(
	t *testing.T,
	status string,
	readinessStatus int,
	checks map[string]string,
) *http.Client {
	t.Helper()
	return fakeHTTPClient(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if request.URL.Path == "/health/live" {
			_, _ = writer.Write([]byte(`{"status":"ok"}`))
			return
		}
		if request.URL.Path != "/health/ready" {
			t.Fatalf("unexpected path %q", request.URL.Path)
		}
		writer.WriteHeader(readinessStatus)
		_ = json.NewEncoder(writer).Encode(map[string]any{"status": status, "checks": checks})
	})
}

func writeDSHPlugin(t *testing.T, root string, built bool) string {
	t.Helper()
	plugin := filepath.Join(root, "integrations", "dsh", "plugins", "powercontext")
	if err := os.MkdirAll(plugin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plugin, "package.json"), []byte(`{"name":"powercontext-dsh"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if built {
		bundle := filepath.Join(plugin, "lib", "index.js")
		if err := os.MkdirAll(filepath.Dir(bundle), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(bundle, []byte("export const name = 'powercontext-dsh'\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return plugin
}

func commandCallStrings(calls []systemCommandCall) []string {
	values := make([]string, len(calls))
	for index, call := range calls {
		values[index] = call.String()
	}
	return values
}
