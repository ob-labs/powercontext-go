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
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestBareMakeKeepsGenerateAsDefaultGoal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the repository Makefile requires a POSIX shell")
	}

	repository := filepath.Clean(filepath.Join("..", ".."))
	fakeGo := filepath.Join(t.TempDir(), "go")
	if err := os.WriteFile(fakeGo, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	command := exec.CommandContext(t.Context(), "make", "--dry-run", "GO="+fakeGo)
	command.Dir = repository
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("make dry-run failed: %v\n%s", err, output)
	}
	if first := firstGeneratedCommand(string(output)); !strings.Contains(first, "generate ./openapi") {
		t.Fatalf("bare make first command = %q, want generate default goal", first)
	}
}

func firstGeneratedCommand(output string) string {
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "make") && (strings.Contains(line, "Entering directory") ||
			strings.Contains(line, "Leaving directory")) {
			continue
		}
		if strings.TrimSpace(line) != "" {
			return line
		}
	}
	return ""
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
