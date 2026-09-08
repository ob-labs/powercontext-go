//go:build linux

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
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestLinuxPrivateFilesRejectUnsafeEnvironmentObjectsWithoutBlocking(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("personal user service refuses root execution")
	}
	files, home := newTestLinuxPrivateFiles(t)
	envFile := filepath.Join(home, "server.env")
	if err := os.WriteFile(envFile, []byte("OPENAI_API_KEY=secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := files.Validate(t.Context(), envFile); err != nil {
		t.Fatalf("Validate regular private file: %v", err)
	}
	if err := os.Remove(envFile); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(envFile, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := files.Validate(t.Context(), envFile); err == nil {
		t.Fatal("Validate accepted a FIFO")
	}
	if err := os.Remove(envFile); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, "outside.env"), envFile); err != nil {
		t.Fatal(err)
	}
	if err := files.Validate(t.Context(), envFile); err == nil {
		t.Fatal("Validate accepted a symbolic link")
	}
}

func TestLinuxOperationFileLockRefusesNestedContention(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("personal user service refuses root execution")
	}
	files, home := newTestLinuxPrivateFiles(t)
	lock := &linuxOperationFileLock{files: files, path: filepath.Join(home, ".local", "state", "powercontext", "personal-service.lock")}
	err := lock.WithLock(t.Context(), func(ctx context.Context) error {
		return lock.WithLock(ctx, func(context.Context) error { return nil })
	})
	if err == nil || errors.Is(err, context.Canceled) {
		t.Fatalf("nested lock error = %v", err)
	}
}

func TestLinuxPrivateFilesReplaceOnlyThePrivateUnitAtomically(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("personal user service refuses root execution")
	}
	files, home := newTestLinuxPrivateFiles(t)
	unit := filepath.Join(home, ".config", "systemd", "user", "powercontext.service")
	if err := files.Write(t.Context(), unit, []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := files.Write(t.Context(), unit, []byte("second")); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(unit)
	if err != nil || string(content) != "second" {
		t.Fatalf("unit content = %q, %v", content, err)
	}
	info, err := os.Lstat(unit)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("unit mode = %v, %v", info.Mode(), err)
	}
	entries, err := os.ReadDir(filepath.Dir(unit))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".powercontext.service-") {
			t.Fatalf("temporary unit %q remains", entry.Name())
		}
	}
}

func newTestLinuxPrivateFiles(t *testing.T) (*linuxPrivateFiles, string) {
	t.Helper()
	home := filepath.Join(t.TempDir(), "home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	roots, err := newLinuxPersonalServiceRoots(filepath.ToSlash(home))
	if err != nil {
		t.Fatal(err)
	}
	files, err := newLinuxPrivateFiles(roots)
	if err != nil {
		t.Fatal(err)
	}
	return files, home
}
