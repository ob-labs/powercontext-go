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

//go:build unix

package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestArtifactCursorKeyRejectsFIFOWithoutWriter(t *testing.T) {
	const childArgument = "--artifact-cursor-fifo-child"
	if len(os.Args) >= 3 && os.Args[len(os.Args)-2] == childArgument {
		dsn := os.Args[len(os.Args)-1]
		if _, writeErr := fmt.Fprintln(os.Stdout, "cursor-key-read-started"); writeErr != nil {
			t.Fatal(writeErr)
		}
		key, err := loadArtifactCursorKey(t.Context(), dsn)
		assertCursorKeyFailure(t, key, err, dsn, filepath.Dir(dsn))
		if !errors.Is(err, errArtifactCursorKey) {
			t.Fatal("FIFO refusal did not preserve the fixed signing-key error")
		}
		return
	}

	dsn := filepath.Join(t.TempDir(), "private-database.db")
	path := filepath.Join(filepath.Dir(dsn), ".private-database.db.cursor-key")
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// A child keeps the regression bounded even when a blocking FIFO open holds
	// the process-wide key mutex. The deadline does not include compilation.
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "-test.run=^TestArtifactCursorKeyRejectsFIFOWithoutWriter$", "--", childArgument, dsn)
	output, runErr := command.CombinedOutput()
	after, statErr := os.Lstat(path)
	if statErr != nil || !os.SameFile(before, after) || after.Mode()&os.ModeNamedPipe == 0 {
		t.Fatal("cursor key loader changed the FIFO target")
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) && strings.Contains(string(output), "cursor-key-read-started") {
		t.Fatal("cursor key loader blocked on a FIFO with no writer")
	}
	if runErr != nil {
		t.Fatalf("FIFO rejection child failed: %v\n%s", runErr, output)
	}
}
