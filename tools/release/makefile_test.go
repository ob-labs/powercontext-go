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
	"strings"
	"testing"
)

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
exit 0
EOF
chmod +x "$GOBIN/golangci-lint"
`
	if err := os.WriteFile(fakeGo, []byte(fakeGoScript), 0o755); err != nil {
		t.Fatal(err)
	}

	command := exec.Command("make", "lint-tools", "GO="+fakeGo, "TOOLS_BIN="+toolsBin)
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
