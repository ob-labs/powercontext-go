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
	"bytes"
	"encoding/json/v2"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestBareMakeListsSupportedTargets(t *testing.T) {
	defaultOutput, err := runRepositoryMake(t, nil, "", "--no-print-directory")
	if err != nil {
		t.Fatalf("run default Make goal: %v\n%s", err, defaultOutput)
	}
	helpOutput, err := runRepositoryMake(t, nil, "", "--no-print-directory", "help")
	if err != nil {
		t.Fatalf("run Make help goal: %v\n%s", err, helpOutput)
	}
	if defaultOutput != helpOutput {
		t.Errorf("default Make output differs from help output\ndefault:\n%s\nhelp:\n%s", defaultOutput, helpOutput)
	}
	for _, target := range []string{"lint", "check", "check-portable", "api-compat", "generated-consumers", "module-inventory", "module-integrity", "license-dependencies", "test", "build", "package-full", "governance-check"} {
		if !strings.Contains(helpOutput, "  "+target+" ") {
			t.Errorf("Make help output is missing %q\n%s", target, helpOutput)
		}
	}
}

func TestModuleIntegrityRunsEveryOwnedModule(t *testing.T) {
	repository := filepath.Clean(filepath.Join("..", ".."))
	want := expectedModuleIntegrityCalls(t, repository)
	got, output, err := runModuleIntegrityProbe(t, repository, "")
	if err != nil {
		t.Fatalf("make module-integrity failed: %v\n%s", err, output)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("module-integrity calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestModuleIntegrityStopsAfterModuleVerificationFailure(t *testing.T) {
	repository := filepath.Clean(filepath.Join("..", ".."))
	want := expectedModuleIntegrityCalls(t, repository)
	failure := "-C test/downstream mod verify"
	failureIndex := slices.IndexFunc(want, func(call string) bool {
		return strings.Contains(call, failure)
	})
	if failureIndex < 0 {
		t.Fatalf("expected calls do not contain %q", failure)
	}

	got, output, err := runModuleIntegrityProbe(t, repository, failure)
	if err == nil {
		t.Fatalf("make module-integrity ignored a downstream verification failure:\n%s", output)
	}
	want = want[:failureIndex+1]
	if !slices.Equal(got, want) {
		t.Fatalf("calls after downstream verification failure:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func expectedModuleIntegrityCalls(t *testing.T, repository string) []string {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join(repository, "test", "module-inventory.json"))
	if err != nil {
		t.Fatal(err)
	}
	var inventory struct {
		SchemaVersion int `json:"schema_version"`
		Modules       []struct {
			Path string `json:"path"`
		} `json:"modules"`
	}
	if decodeErr := json.Unmarshal(payload, &inventory, json.RejectUnknownMembers(true)); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if inventory.SchemaVersion != 1 {
		t.Fatalf("module inventory schema_version = %d, want 1", inventory.SchemaVersion)
	}

	repository, err = filepath.Abs(repository)
	if err != nil {
		t.Fatal(err)
	}
	repository = filepath.ToSlash(repository)
	binary := repository + "/bin/powercontext-downstream"
	calls := []string{"go|||run ./tools/module-integrity -inventory test/module-inventory.json"}
	for _, module := range inventory.Modules {
		calls = append(calls,
			"go|off||-C "+module.Path+" mod tidy -diff",
			"go|off||-C "+module.Path+" mod verify",
			"go|off||-C "+module.Path+" build -mod=readonly ./...",
		)
		if module.Path == "test/downstream" {
			calls = append(calls,
				"go|||build -tags sqlite_fts5 -o "+binary+" ./cmd/powercontext",
				"go|off|"+binary+"|-C "+module.Path+" test -count=1 -mod=readonly ./...",
			)
		} else {
			calls = append(calls, "go|off||-C "+module.Path+" test -count=1 -mod=readonly ./...")
		}
		directory := repository
		if module.Path != "." {
			directory += "/" + module.Path
		}
		calls = append(calls, "lint|"+directory+"|run --config "+repository+"/.golangci.yml")
	}
	return calls
}

func runModuleIntegrityProbe(t *testing.T, repository, failure string) ([]string, []byte, error) {
	t.Helper()
	temporary := t.TempDir()
	toolsBin := filepath.Join(temporary, "bin")
	if err := os.MkdirAll(toolsBin, 0o755); err != nil {
		t.Fatal(err)
	}
	callLog := filepath.Join(temporary, "calls.txt")
	fakeGo := filepath.Join(temporary, "go")
	const fakeGoScript = `#!/bin/sh
set -eu

case "${1:-} ${2:-}" in
  "env GOEXE")
    exit 0
    ;;
  "env GOVERSION")
    printf 'go1.27.0\n'
    exit 0
    ;;
esac

printf 'go|%s|%s|%s\n' "${GOWORK:-}" "${POWERCONTEXT_DOWNSTREAM_BINARY:-}" "$*" >> "$CALL_LOG"
case "$*" in
  *"${FAIL_MATCH:-never-match}"*)
    exit 23
    ;;
esac
`
	if err := os.WriteFile(fakeGo, []byte(fakeGoScript), 0o755); err != nil {
		t.Fatal(err)
	}

	linter := filepath.Join(toolsBin, "golangci-lint")
	const linterScript = `#!/bin/sh
set -eu
printf 'lint|%s|%s\n' "$PWD" "$*" >> "$CALL_LOG"
case "$PWD|$*" in
  *"${FAIL_MATCH:-never-match}"*)
    exit 24
    ;;
esac
`
	if err := os.WriteFile(linter, []byte(linterScript), 0o755); err != nil {
		t.Fatal(err)
	}
	stamp := filepath.Join(toolsBin, ".golangci-lint-v2.13.1-go1.27.0")
	if err := os.WriteFile(stamp, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	command := exec.CommandContext(
		t.Context(),
		"make",
		"--no-print-directory",
		"module-integrity",
		"GO="+filepath.ToSlash(fakeGo),
		"TOOLS_BIN="+filepath.ToSlash(toolsBin),
	)
	command.Dir = repository
	command.Env = append(
		os.Environ(),
		"CALL_LOG="+filepath.ToSlash(callLog),
		"FAIL_MATCH="+failure,
		"GOWORK=",
		"POWERCONTEXT_DOWNSTREAM_BINARY=",
	)
	output, commandErr := command.CombinedOutput()
	payload, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatalf("read module-integrity call log: %v\n%s", err, output)
	}
	calls := strings.Split(strings.TrimSpace(strings.ReplaceAll(string(payload), `\`, "/")), "\n")
	for index, call := range calls {
		parts := strings.SplitN(call, "|", 3)
		if len(parts) == 3 && parts[0] == "lint" {
			parts[1] = normalizeShellPath(parts[1])
			calls[index] = strings.Join(parts, "|")
		}
	}
	return calls, output, commandErr
}

func normalizeShellPath(path string) string {
	if runtime.GOOS == "windows" && len(path) >= 3 && path[0] == '/' && path[2] == '/' {
		return strings.ToUpper(path[1:2]) + ":" + path[2:]
	}
	return path
}

func TestMakefileRejectsFailedPipelines(t *testing.T) {
	const probe = `.PHONY: strict-shell-probe
strict-shell-probe:
	@false | true
	@printf 'strict shell did not stop\n'
`
	output, err := runRepositoryMake(
		t,
		nil,
		probe,
		"--no-print-directory",
		"-f", "Makefile",
		"-f", "-",
		"strict-shell-probe",
	)
	if err == nil {
		t.Fatalf("failed pipeline did not stop Make\n%s", output)
	}
	if _, ok := errors.AsType[*exec.ExitError](err); !ok {
		t.Fatalf("run strict Make probe: %v\n%s", err, output)
	}
	if strings.Contains(output, "strict shell did not stop") {
		t.Fatalf("Make continued after a failed pipeline\n%s", output)
	}
}

func TestMakefileMissingCredentialTargetsKeepActionableErrors(t *testing.T) {
	tests := []struct {
		name      string
		target    string
		variables []string
		want      string
	}{
		{
			name:      "OceanBase URL",
			target:    "test-oceanbase-live",
			variables: []string{"POWERCONTEXT_TEST_OCEANBASE_URL"},
			want:      "POWERCONTEXT_TEST_OCEANBASE_URL must name a dedicated OceanBase MySQL-mode database",
		},
		{
			name:   "real provider model",
			target: "real-provider-test",
			variables: []string{
				"POWERCONTEXT_REAL_SMOKE_GENERATION_MODEL",
				"POWERCONTEXT_REAL_SMOKE_EMBEDDING_MODEL",
			},
			want: "set at least one POWERCONTEXT_REAL_SMOKE_*_MODEL variable",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			output, err := runRepositoryMake(
				t,
				environmentWithout(test.variables...),
				"",
				"--no-print-directory",
				test.target,
			)
			if err == nil {
				t.Fatalf("make %s succeeded without required configuration\n%s", test.target, output)
			}
			if _, ok := errors.AsType[*exec.ExitError](err); !ok {
				t.Fatalf("run make %s: %v\n%s", test.target, err, output)
			}
			if !strings.Contains(output, test.want) {
				t.Errorf("make %s output is missing %q\n%s", test.target, test.want, output)
			}
			if strings.Contains(output, "unbound variable") {
				t.Errorf("make %s exposed a shell nounset error instead of the target guidance\n%s", test.target, output)
			}
		})
	}
}

func runRepositoryMake(t *testing.T, environment []string, stdin string, arguments ...string) (string, error) {
	t.Helper()
	repository := filepath.Clean(filepath.Join("..", ".."))
	command := exec.CommandContext(t.Context(), "make", arguments...)
	command.Dir = repository
	command.Env = environment
	if stdin != "" {
		command.Stdin = strings.NewReader(stdin)
	}
	output, err := command.CombinedOutput()
	return string(output), err
}

func environmentWithout(names ...string) []string {
	environment := os.Environ()
	filtered := make([]string, 0, len(environment))
	for _, entry := range environment {
		name, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		excluded := false
		for _, candidate := range names {
			if strings.EqualFold(name, candidate) {
				excluded = true
				break
			}
		}
		if !excluded {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

func TestBuildAllUsesReadonlyModuleResolution(t *testing.T) {
	repository := filepath.Clean(filepath.Join("..", ".."))
	command := exec.CommandContext(t.Context(), "make", "--dry-run", "build-all", "GO=go")
	command.Dir = repository
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("make build-all dry-run failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "go build -mod=readonly ./...") {
		t.Fatalf("make build-all does not use readonly module resolution:\n%s", output)
	}
}

func TestDownstreamCompatDisablesGoTestCaching(t *testing.T) {
	repository := filepath.Clean(filepath.Join("..", ".."))
	command := exec.CommandContext(t.Context(), "make", "--dry-run", "downstream-compat", "GO=go")
	command.Dir = repository
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("make downstream-compat dry-run failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "test -count=1 -mod=readonly ./...") {
		t.Fatalf("make downstream-compat can reuse a cached external-Server result:\n%s", output)
	}
}

func TestGeneratedConsumersTargetRunsFreshConsumerVerification(t *testing.T) {
	repository := filepath.Clean(filepath.Join("..", ".."))
	command := exec.CommandContext(t.Context(), "make", "--dry-run", "generated-consumers", "GO=go")
	command.Dir = repository
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("make generated-consumers dry-run failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "go test -count=1 ./tools/generated-consumers") {
		t.Fatalf("make generated-consumers does not run the uncached fresh-consumer check:\n%s", output)
	}
}

func TestAPICompatChecksEveryDeliberatePublicPackage(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the repository Makefile requires a POSIX shell")
	}

	output, baselineLog, apidiffLog, err := runAPICompatWithFake(t, `#!/bin/sh
set -eu
printf '%s|%s\n' "${GOFLAGS:-}" "$*" >> "$API_COMPAT_CALL_LOG"
if [ "$(grep -c '^' "$API_COMPAT_CALL_LOG")" -eq 2 ]; then
	printf '%s\n' '- ./sample.Exported: removed'
fi
`)
	if err != nil {
		t.Fatalf("make api-compat failed: %v\n%s", err, output)
	}
	baselinePayload, err := os.ReadFile(baselineLog)
	if err != nil {
		t.Fatal(err)
	}
	baselineCalls := strings.Split(strings.TrimSpace(string(baselinePayload)), "\n")
	if len(baselineCalls) != 2 {
		t.Fatalf("api baseline calls = %q, want inventory check and current bundle generation", baselineCalls)
	}
	wantPackages := []string{
		"-module", "github.com/ob-labs/powercontext-go",
		"github.com/ob-labs/powercontext-go/api/v1",
		"github.com/ob-labs/powercontext-go/artifact",
		"github.com/ob-labs/powercontext-go/artifact/experience",
		"github.com/ob-labs/powercontext-go/artifact/handoff",
		"github.com/ob-labs/powercontext-go/artifact/memory",
		"github.com/ob-labs/powercontext-go/artifact/skill",
		"github.com/ob-labs/powercontext-go/client",
		"github.com/ob-labs/powercontext-go/inference",
		"github.com/ob-labs/powercontext-go/server",
		"github.com/ob-labs/powercontext-go/source",
		"github.com/ob-labs/powercontext-go/trigger",
	}
	checkFlags, checkArguments := splitAPICompatCall(t, baselineCalls[0])
	if !slices.Contains(strings.Fields(checkFlags), "-trimpath") {
		t.Errorf("api baseline check GOFLAGS = %q, want -trimpath", checkFlags)
	}
	wantCheck := append([]string{"-check", "test/api-compat/pre-release.apidiff"}, wantPackages...)
	if got := strings.Fields(checkArguments); !slices.Equal(got, wantCheck) {
		t.Fatalf("api baseline check arguments = %q, want %q", got, wantCheck)
	}
	writeFlags, writeArguments := splitAPICompatCall(t, baselineCalls[1])
	if !slices.Contains(strings.Fields(writeFlags), "-trimpath") {
		t.Errorf("api baseline write GOFLAGS = %q, want -trimpath", writeFlags)
	}
	gotWrite := strings.Fields(writeArguments)
	if len(gotWrite) < 4 || gotWrite[0] != "-output" {
		t.Fatalf("api baseline write arguments = %q, want output flag", gotWrite)
	}
	currentBundle := gotWrite[1]
	if !slices.Equal(gotWrite[2:], wantPackages) {
		t.Fatalf("api baseline write arguments = %q, want %q", gotWrite[2:], wantPackages)
	}

	apidiffPayload, err := os.ReadFile(apidiffLog)
	if err != nil {
		t.Fatal(err)
	}
	apidiffCalls := strings.Split(strings.TrimSpace(string(apidiffPayload)), "\n")
	if len(apidiffCalls) != 2 {
		t.Fatalf("apidiff calls = %q, want repository comparison and real removal probe", apidiffCalls)
	}
	apidiffFlags, apidiffArguments := splitAPICompatCall(t, apidiffCalls[0])
	if !slices.Contains(strings.Fields(apidiffFlags), "-trimpath") {
		t.Errorf("apidiff GOFLAGS = %q, want -trimpath", apidiffFlags)
	}
	wantAPIDiff := []string{"-m", "-incompatible", "test/api-compat/pre-release.apidiff", currentBundle}
	if got := strings.Fields(apidiffArguments); !slices.Equal(got, wantAPIDiff) {
		t.Fatalf("apidiff arguments = %q, want %q", got, wantAPIDiff)
	}
	probeFlags, probeArguments := splitAPICompatCall(t, apidiffCalls[1])
	if !slices.Contains(strings.Fields(probeFlags), "-trimpath") {
		t.Errorf("apidiff probe GOFLAGS = %q, want -trimpath", probeFlags)
	}
	probe := strings.Fields(probeArguments)
	if len(probe) != 4 || probe[0] != "-m" || probe[1] != "-incompatible" || probe[2] == probe[3] {
		t.Fatalf("apidiff probe arguments = %q, want two distinct module bundles", probe)
	}
}

func TestAPIBaselinesDoNotEmbedMachinePaths(t *testing.T) {
	repository := filepath.Clean(filepath.Join("..", ".."))
	paths, err := filepath.Glob(filepath.Join(repository, "test", "api-compat", "*.apidiff"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || filepath.Base(paths[0]) != "pre-release.apidiff" {
		t.Fatalf("public API baselines = %q, want only pre-release.apidiff", paths)
	}
	for _, path := range paths {
		payload, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		for _, forbidden := range [][]byte{
			[]byte(filepath.Clean(repository)),
			[]byte(filepath.ToSlash(repository)),
			[]byte(`C:\Users\`),
			[]byte(`D:\programs\`),
			[]byte(`/home/`),
			[]byte(`/Users/`),
			[]byte(`/mnt/`),
			[]byte(`/tmp/`),
			[]byte(`/workspace/`),
		} {
			if bytes.Contains(payload, forbidden) {
				t.Fatalf("%s contains machine path prefix %q", filepath.Base(path), forbidden)
			}
		}
		if match := regexp.MustCompile(`[A-Za-z]:\\`).Find(payload); match != nil {
			t.Fatalf("%s contains Windows absolute path prefix %q", filepath.Base(path), match)
		}
	}
}

func TestAPICompatRejectsAnIncompatibleModuleReport(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the repository Makefile requires a POSIX shell")
	}

	output, _, callLog, err := runAPICompatWithFake(t, `#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$API_COMPAT_CALL_LOG"
printf 'incompatible: removed exported field\n'
`)
	if err == nil {
		t.Fatalf("make api-compat accepted an incompatible public API:\n%s", output)
	}
	if !strings.Contains(output, "incompatible public API change") ||
		!strings.Contains(output, "removed exported field") {
		t.Fatalf("api-compat failure is not actionable:\n%s", output)
	}
	payload, readErr := os.ReadFile(callLog)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if got := strings.Split(strings.TrimSpace(string(payload)), "\n"); len(got) != 1 {
		t.Fatalf("apidiff calls after incompatibility = %q, want 1 call", got)
	}
}

func TestAPICompatFailsWhenCurrentBundleGenerationFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the repository Makefile requires a POSIX shell")
	}
	const failingBaseline = `#!/bin/sh
set -eu
mode=''
for argument in "$@"; do
	case "$argument" in
	  '-check') mode='check' ;;
	  '-output') mode='output' ;;
	esac
done
if [ "$mode" = 'check' ]; then
	exit 0
fi
if [ "$mode" = 'output' ]; then
	printf 'current bundle generation failed\n' >&2
	exit 23
fi
printf 'unexpected baseline generator arguments: %s\n' "$*" >&2
exit 64
`
	const unexpectedAPIDiff = `#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$API_COMPAT_CALL_LOG"
printf 'apidiff should not run after generator failure\n' >&2
exit 24
`
	output, _, apidiffLog, err := runAPICompatWithScripts(t, failingBaseline, unexpectedAPIDiff)
	if err == nil {
		t.Fatalf("make api-compat ignored current bundle generation failure:\n%s", output)
	}
	if !strings.Contains(output, "current bundle generation failed") {
		t.Fatalf("generator failure is not actionable:\n%s", output)
	}
	payload, readErr := os.ReadFile(apidiffLog)
	if readErr == nil && strings.TrimSpace(string(payload)) != "" {
		t.Fatalf("apidiff ran after current bundle generation failed: %q", payload)
	}
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		t.Fatal(readErr)
	}
}

func TestAPICompatFailsWhenAPIDiffExecutionFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the repository Makefile requires a POSIX shell")
	}
	const failingAPIDiff = `#!/bin/sh
set -eu
printf 'apidiff bundle read failed\n' >&2
exit 24
`
	output, _, _, err := runAPICompatWithFake(t, failingAPIDiff)
	if err == nil {
		t.Fatalf("make api-compat ignored apidiff execution failure:\n%s", output)
	}
	if !strings.Contains(output, "apidiff bundle read failed") {
		t.Fatalf("apidiff execution failure is not actionable:\n%s", output)
	}
}

const successfulAPIBaselineScript = `#!/bin/sh
set -eu
printf '%s|%s\n' "${GOFLAGS:-}" "$*" >> "$API_BASELINE_CALL_LOG"
output=''
check=''
while [ "$#" -gt 0 ]; do
	case "$1" in
	  '-output')
		shift
		output="$1"
		;;
	  '-check')
		shift
		check="$1"
		;;
	esac
	shift
done
if [ -n "$output" ]; then
	printf 'current bundle\n' > "$output"
elif [ -z "$check" ]; then
	printf 'missing -output or -check\n' >&2
	exit 64
fi
`

func runAPICompatWithFake(t *testing.T, apidiffScript string) (string, string, string, error) {
	t.Helper()
	return runAPICompatWithScripts(t, successfulAPIBaselineScript, apidiffScript)
}

func runAPICompatWithScripts(t *testing.T, baselineScript, apidiffScript string) (string, string, string, error) {
	t.Helper()
	repository := filepath.Clean(filepath.Join("..", ".."))
	temporary := t.TempDir()
	baselineLog := filepath.Join(temporary, "baseline.txt")
	apidiffLog := filepath.Join(temporary, "apidiff.txt")
	fakeBaseline := filepath.Join(temporary, "api-baseline")
	if err := os.WriteFile(fakeBaseline, []byte(baselineScript), 0o755); err != nil {
		t.Fatal(err)
	}
	fakeAPIDiff := filepath.Join(temporary, "apidiff")
	if err := os.WriteFile(fakeAPIDiff, []byte(apidiffScript), 0o755); err != nil {
		t.Fatal(err)
	}
	stamp := filepath.Join(temporary, "apidiff.stamp")
	if err := os.WriteFile(stamp, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(
		t.Context(),
		"make",
		"api-compat",
		"APIDIFF="+fakeAPIDiff,
		"APIDIFF_STAMP="+stamp,
		"API_BASELINE_GENERATOR="+fakeBaseline,
	)
	command.Dir = repository
	command.Env = append(
		os.Environ(),
		"API_BASELINE_CALL_LOG="+baselineLog,
		"API_COMPAT_CALL_LOG="+apidiffLog,
	)
	output, err := command.CombinedOutput()
	return string(output), baselineLog, apidiffLog, err
}

func splitAPICompatCall(t *testing.T, call string) (string, string) {
	t.Helper()
	goFlags, arguments, ok := strings.Cut(call, "|")
	if !ok {
		t.Fatalf("API compatibility call %q has no GOFLAGS boundary", call)
	}
	return goFlags, arguments
}

func TestAPIDiffToolInstallUsesProjectSelectedGoVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the repository Makefile requires a POSIX shell")
	}

	repository := filepath.Clean(filepath.Join("..", ".."))
	temporary := t.TempDir()
	toolsBin := filepath.Join(temporary, "bin")
	installRecord := filepath.Join(temporary, "install.txt")
	fakeGo := filepath.Join(temporary, "go")
	const fakeGoScript = `#!/bin/sh
set -eu

case "${1:-} ${2:-}" in
  "env GOEXE")
    exit 0
    ;;
  "env GOVERSION")
    printf 'go1.27.0\n'
    exit 0
    ;;
  "version -m")
    printf '%s: go1.27.0\n\tpath\tgolang.org/x/exp/cmd/apidiff\n\tmod\tgolang.org/x/exp\tv0.0.0-20260824195058-e88cd73687aa\th1:probe\n' "$3"
    exit 0
    ;;
esac

if [ "${1:-}" != "install" ]; then
  printf 'unexpected go command: %s\n' "$*" >&2
  exit 64
fi

printf '%s|%s\n' "${GOTOOLCHAIN:-}" "$*" > "$APIDIFF_INSTALL_RECORD"
mkdir -p "$GOBIN"
printf '#!/bin/sh\nexit 0\n' > "$GOBIN/apidiff"
chmod +x "$GOBIN/apidiff"
`
	if err := os.WriteFile(fakeGo, []byte(fakeGoScript), 0o755); err != nil {
		t.Fatal(err)
	}

	command := exec.CommandContext(t.Context(), "make", "api-compat-tools", "GO="+fakeGo, "TOOLS_BIN="+toolsBin)
	command.Dir = repository
	command.Env = append(os.Environ(), "GOTOOLCHAIN=auto", "APIDIFF_INSTALL_RECORD="+installRecord)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("make api-compat-tools failed: %v\n%s", err, output)
	}
	payload, err := os.ReadFile(installRecord)
	if err != nil {
		t.Fatal(err)
	}
	want := "go1.27.0+auto|install golang.org/x/exp/cmd/apidiff@v0.0.0-20260824195058-e88cd73687aa"
	if got := strings.TrimSpace(string(payload)); got != want {
		t.Fatalf("apidiff install = %q, want %q", got, want)
	}
}

func TestCheckPortableBuildsEverySupportedTargetWithoutCGO(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the repository Makefile requires a POSIX shell")
	}

	repository := filepath.Clean(filepath.Join("..", ".."))
	temporary := t.TempDir()
	callLog := filepath.Join(temporary, "calls.txt")
	fakeGo := filepath.Join(temporary, "go")
	const fakeGoScript = `#!/bin/sh
set -eu

case "${1:-} ${2:-}" in
  "env GOEXE")
    exit 0
    ;;
  "env GOVERSION")
    printf 'go1.27.0\n'
    exit 0
    ;;
esac

printf '%s|%s|%s|%s\n' "$GOOS" "$GOARCH" "$CGO_ENABLED" "$*" >> "$PORTABLE_CALL_LOG"
`
	if err := os.WriteFile(fakeGo, []byte(fakeGoScript), 0o755); err != nil {
		t.Fatal(err)
	}

	command := exec.CommandContext(t.Context(), "make", "check-portable", "GO="+fakeGo)
	command.Dir = repository
	command.Env = append(os.Environ(), "PORTABLE_CALL_LOG="+callLog)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("make check-portable failed: %v\n%s", err, output)
	}

	payload, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatal(err)
	}
	wantTargets := []string{"darwin/amd64", "darwin/arm64", "linux/amd64", "linux/arm64"}
	wantPackages := []string{
		"./api/...",
		"./artifact/...",
		"./client/...",
		"./inference/...",
		"./openapi/...",
		"./source/...",
		"./trigger/...",
	}
	lines := strings.Split(strings.TrimSpace(string(payload)), "\n")
	gotTargets := make([]string, 0, len(lines))
	for _, line := range lines {
		parts := strings.SplitN(line, "|", 4)
		if len(parts) != 4 {
			t.Fatalf("portable build call %q has %d fields, want 4", line, len(parts))
		}
		gotTargets = append(gotTargets, parts[0]+"/"+parts[1])
		if parts[2] != "0" {
			t.Errorf("portable build %s/%s CGO_ENABLED = %q, want 0", parts[0], parts[1], parts[2])
		}
		arguments := strings.Fields(parts[3])
		if len(arguments) == 0 || arguments[0] != "build" {
			t.Fatalf("portable build %s/%s arguments = %q, want build command", parts[0], parts[1], arguments)
		}
		gotPackages := slices.Sorted(slices.Values(arguments[1:]))
		if !slices.Equal(gotPackages, wantPackages) {
			t.Errorf("portable build %s/%s packages = %q, want %q", parts[0], parts[1], gotPackages, wantPackages)
		}
	}
	gotTargets = slices.Sorted(slices.Values(gotTargets))
	if !slices.Equal(gotTargets, wantTargets) {
		t.Fatalf("portable build targets = %q, want %q", gotTargets, wantTargets)
	}
}

func TestCheckPortableStopsAfterTheFirstFailedBuild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the repository Makefile requires a POSIX shell")
	}

	repository := filepath.Clean(filepath.Join("..", ".."))
	temporary := t.TempDir()
	callLog := filepath.Join(temporary, "calls.txt")
	fakeGo := filepath.Join(temporary, "go")
	const fakeGoScript = `#!/bin/sh
set -eu

case "${1:-} ${2:-}" in
  "env GOEXE")
    exit 0
    ;;
  "env GOVERSION")
    printf 'go1.27.0\n'
    exit 0
    ;;
esac

target="$GOOS/$GOARCH"
printf '%s\n' "$target" >> "$PORTABLE_CALL_LOG"
if [ "$(grep -c '^' "$PORTABLE_CALL_LOG")" -eq 2 ]; then
	exit 23
fi
`
	if err := os.WriteFile(fakeGo, []byte(fakeGoScript), 0o755); err != nil {
		t.Fatal(err)
	}

	command := exec.CommandContext(t.Context(), "make", "check-portable", "GO="+fakeGo)
	command.Dir = repository
	command.Env = append(
		os.Environ(),
		"PORTABLE_CALL_LOG="+callLog,
	)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("make check-portable succeeded after a failed cross-build:\n%s", output)
	}

	payload, readErr := os.ReadFile(callLog)
	if readErr != nil {
		t.Fatal(readErr)
	}
	got := strings.Split(strings.TrimSpace(string(payload)), "\n")
	if len(got) != 2 {
		t.Fatalf("portable build calls after second-call failure = %q, want exactly 2 calls", got)
	}
}

func TestCoverageTargetUsesRaceAtomicProfileAndThreshold(t *testing.T) {
	repository := filepath.Clean(filepath.Join("..", ".."))
	command := exec.CommandContext(t.Context(), "make", "--dry-run", "coverage", "GO=go")
	command.Dir = repository
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("make coverage dry-run failed: %v\n%s", err, output)
	}
	contents := string(output)
	for _, required := range []string{
		"go test -race -covermode=atomic -coverprofile=\"coverage/coverage.out\" ./...",
		"go tool cover -func=\"coverage/coverage.out\"",
		"make coverage-check",
	} {
		if !strings.Contains(contents, required) {
			t.Errorf("make coverage output is missing %q:\n%s", required, output)
		}
	}
}

func TestCoverageCheckValidatesThresholdInputs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("coverage-check requires a POSIX shell and awk")
	}

	repository := filepath.Clean(filepath.Join("..", ".."))
	tests := []struct {
		name        string
		total       string
		minimum     string
		wantSuccess bool
		wantOutput  string
	}{
		{name: "meets minimum", total: "16.1", minimum: "16.0", wantSuccess: true, wantOutput: "meets minimum"},
		{name: "below minimum", total: "16.1", minimum: "16.2", wantOutput: "is below minimum"},
		{name: "invalid minimum", total: "16.1", minimum: "not-a-number", wantOutput: "coverage minimum must be a non-negative number"},
		{name: "invalid total", total: "not-a-number", minimum: "16.0", wantOutput: "coverage total must be a non-negative number"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			summary := filepath.Join(t.TempDir(), "summary.txt")
			contents := "total:\t(statements)\t" + test.total + "%\n"
			if err := os.WriteFile(summary, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			command := exec.CommandContext(
				t.Context(),
				"make",
				"coverage-check",
				"COVERAGE_SUMMARY="+summary,
				"COVERAGE_MINIMUM="+test.minimum,
			)
			command.Dir = repository
			output, err := command.CombinedOutput()
			if test.wantSuccess && err != nil {
				t.Fatalf("coverage-check failed: %v\n%s", err, output)
			}
			if !test.wantSuccess && err == nil {
				t.Fatalf("coverage-check unexpectedly succeeded:\n%s", output)
			}
			if !strings.Contains(string(output), test.wantOutput) {
				t.Fatalf("coverage-check output is missing %q:\n%s", test.wantOutput, output)
			}
		})
	}
}

func TestLintToolInstallUsesProjectSelectedGoVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the repository Makefile requires a POSIX shell")
	}

	repository := filepath.Clean(filepath.Join("..", ".."))
	temporary := t.TempDir()
	toolsBin := filepath.Join(temporary, "bin")
	toolchainRecord := filepath.Join(temporary, "toolchain.txt")
	fakeGo := filepath.Join(temporary, "go")
	const fakeGoScript = `#!/bin/sh
set -eu

case "${1:-} ${2:-}" in
  "env GOEXE")
    exit 0
    ;;
  "env GOVERSION")
    printf 'go1.27.0\n'
    exit 0
    ;;
esac

if [ "${1:-}" != "install" ]; then
  printf 'unexpected go command: %s\n' "$*" >&2
  exit 64
fi

printf '%s\n' "${GOTOOLCHAIN:-}" > "$TOOLCHAIN_RECORD"
mkdir -p "$GOBIN"
cat > "$GOBIN/golangci-lint" <<'EOF'
#!/bin/sh
if [ "${1:-}" = "--version" ]; then
  printf 'golangci-lint has version 2.13.1 built with go1.27.0\n'
fi
exit 0
EOF
chmod +x "$GOBIN/golangci-lint"
`
	if err := os.WriteFile(fakeGo, []byte(fakeGoScript), 0o755); err != nil {
		t.Fatal(err)
	}

	command := exec.CommandContext(t.Context(), "make", "lint-tools", "GO="+fakeGo, "TOOLS_BIN="+toolsBin)
	command.Dir = repository
	command.Env = append(os.Environ(), "GOTOOLCHAIN=auto", "TOOLCHAIN_RECORD="+toolchainRecord)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("make lint-tools failed: %v\n%s", err, output)
	}

	payload, err := os.ReadFile(toolchainRecord)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(string(payload)), "go1.27.0+auto"; got != want {
		t.Fatalf("lint tool install GOTOOLCHAIN = %q, want %q", got, want)
	}
}

func TestLintToolReinstallsWhenGoModSelectsNewGoVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the repository Makefile requires a POSIX shell")
	}

	repository := filepath.Clean(filepath.Join("..", ".."))
	temporary := t.TempDir()
	toolsBin := filepath.Join(temporary, "bin")
	installCount := filepath.Join(temporary, "install-count.txt")
	fakeGo := filepath.Join(temporary, "go")
	const fakeGoScript = `#!/bin/sh
set -eu

case "${1:-} ${2:-}" in
  "env GOEXE")
    exit 0
    ;;
  "env GOVERSION")
    printf 'go1.27.0\n'
    exit 0
    ;;
esac

if [ "${1:-}" != "install" ]; then
  printf 'unexpected go command: %s\n' "$*" >&2
  exit 64
fi

count=0
if [ -f "$INSTALL_COUNT" ]; then
  count="$(cat "$INSTALL_COUNT")"
fi
count=$((count + 1))
printf '%s\n' "$count" > "$INSTALL_COUNT"
mkdir -p "$GOBIN"
cat > "$GOBIN/golangci-lint" <<'EOF'
#!/bin/sh
if [ "${1:-}" = "--version" ]; then
  printf 'golangci-lint has version 2.13.1 built with go1.27.0\n'
fi
exit 0
EOF
chmod +x "$GOBIN/golangci-lint"
`
	if err := os.WriteFile(fakeGo, []byte(fakeGoScript), 0o755); err != nil {
		t.Fatal(err)
	}

	for _, arguments := range [][]string{
		{"lint-tools", "GO=" + fakeGo, "TOOLS_BIN=" + toolsBin},
		{"lint-tools", "-W", "go.mod", "GO=" + fakeGo, "TOOLS_BIN=" + toolsBin},
	} {
		command := exec.CommandContext(t.Context(), "make", arguments...)
		command.Dir = repository
		command.Env = append(os.Environ(), "GOTOOLCHAIN=auto", "INSTALL_COUNT="+installCount)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("make %s failed: %v\n%s", strings.Join(arguments, " "), err, output)
		}
	}

	payload, err := os.ReadFile(installCount)
	if err != nil {
		t.Fatal(err)
	}
	count, err := strconv.Atoi(strings.TrimSpace(string(payload)))
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("lint tool install count = %d, want 2 after go.mod changes", count)
	}
}

func TestLintToolRejectsWrongCachedBinaryVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the repository Makefile requires a POSIX shell")
	}

	repository := filepath.Clean(filepath.Join("..", ".."))
	temporary := t.TempDir()
	toolsBin := filepath.Join(temporary, "bin")
	if err := os.MkdirAll(toolsBin, 0o755); err != nil {
		t.Fatal(err)
	}
	linter := filepath.Join(toolsBin, "golangci-lint")
	const linterScript = `#!/bin/sh
if [ "${1:-}" = "--version" ]; then
  printf 'golangci-lint has version 2.12.0 built with go1.27.0\n'
fi
exit 0
`
	if err := os.WriteFile(linter, []byte(linterScript), 0o755); err != nil {
		t.Fatal(err)
	}

	fakeGo := filepath.Join(temporary, "go")
	const fakeGoScript = `#!/bin/sh
set -eu

case "${1:-} ${2:-}" in
  "env GOEXE")
    exit 0
    ;;
  "env GOVERSION")
    printf 'go1.27.0\n'
    exit 0
    ;;
esac

printf 'unexpected go command: %s\n' "$*" >&2
exit 64
`
	if err := os.WriteFile(fakeGo, []byte(fakeGoScript), 0o755); err != nil {
		t.Fatal(err)
	}

	command := exec.CommandContext(t.Context(), "make", "lint-tools", "GO="+fakeGo, "TOOLS_BIN="+toolsBin)
	command.Dir = repository
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("make lint-tools accepted a wrong cached golangci-lint version:\n%s", output)
	}
	if _, err := os.Stat(filepath.Join(toolsBin, ".golangci-lint-v2.13.1-go1.27.0")); !os.IsNotExist(err) {
		t.Fatalf("lint-tools created a stamp for the wrong cached binary version: %v", err)
	}
}

func TestFmtFailsWhenGoSyntaxIsInvalid(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the repository Makefile requires a POSIX shell")
	}

	repository := filepath.Clean(filepath.Join("..", ".."))
	temporary := t.TempDir()
	makefile, err := os.ReadFile(filepath.Join(repository, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	if writeMakefileErr := os.WriteFile(filepath.Join(temporary, "Makefile"), makefile, 0o644); writeMakefileErr != nil {
		t.Fatal(writeMakefileErr)
	}
	if writeModuleErr := os.WriteFile(filepath.Join(temporary, "go.mod"), []byte("module example.com/malformed\n\ngo 1.27.0\n"), 0o644); writeModuleErr != nil {
		t.Fatal(writeModuleErr)
	}
	if writeSourceErr := os.WriteFile(filepath.Join(temporary, "malformed.go"), []byte("package invalid\n\nfunc broken( {\n"), 0o644); writeSourceErr != nil {
		t.Fatal(writeSourceErr)
	}

	toolsBin := filepath.Join(temporary, ".tools", "bin")
	if mkdirErr := os.MkdirAll(toolsBin, 0o755); mkdirErr != nil {
		t.Fatal(mkdirErr)
	}
	linter := filepath.Join(toolsBin, "golangci-lint")
	const linterScript = `#!/bin/sh
if [ "${1:-}" = "--version" ]; then
  printf 'golangci-lint has version 2.13.1 built with go1.27.0\n'
fi
exit 0
`
	if writeLinterErr := os.WriteFile(linter, []byte(linterScript), 0o755); writeLinterErr != nil {
		t.Fatal(writeLinterErr)
	}
	if writeStampErr := os.WriteFile(filepath.Join(toolsBin, ".golangci-lint-v2.13.1-go1.27.0"), nil, 0o644); writeStampErr != nil {
		t.Fatal(writeStampErr)
	}
	fakeGo := filepath.Join(temporary, "go")
	const fakeGoScript = `#!/bin/sh
set -eu

case "${1:-} ${2:-}" in
  "env GOEXE")
    exit 0
    ;;
  "env GOVERSION")
    printf 'go1.27.0\n'
    exit 0
    ;;
esac

printf 'unexpected go command: %s\n' "$*" >&2
exit 64
`
	if writeGoErr := os.WriteFile(fakeGo, []byte(fakeGoScript), 0o755); writeGoErr != nil {
		t.Fatal(writeGoErr)
	}

	command := exec.CommandContext(t.Context(), "make", "fmt", "GO="+fakeGo, "GOFMT=gofmt", "TOOLS_BIN="+toolsBin)
	command.Dir = temporary
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("make fmt unexpectedly succeeded with malformed Go source:\n%s", output)
	}
	if !strings.Contains(string(output), "malformed.go") {
		t.Fatalf("make fmt failed for the wrong reason, output did not mention malformed.go:\n%s", output)
	}
}
