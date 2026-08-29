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
	"os/exec"
	"path/filepath"
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
	for _, target := range []string{"lint", "check", "check-portable", "generated-consumers", "module-inventory", "license-dependencies", "test", "build", "package-full", "governance-check"} {
		if !strings.Contains(helpOutput, "  "+target+" ") {
			t.Errorf("Make help output is missing %q\n%s", target, helpOutput)
		}
	}
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
