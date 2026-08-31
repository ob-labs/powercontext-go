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

//go:build cgo && (darwin || linux)

package seekdb

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"
)

func TestCanonicalPathResolvesExistingSymlinkAncestors(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	realRoot := filepath.Join(root, "real")
	if err := os.Mkdir(realRoot, 0o750); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(realRoot, link); err != nil {
		t.Fatal(err)
	}
	got, err := canonicalPath(filepath.Join(link, "missing", "seekdb"))
	if err != nil {
		t.Fatal(err)
	}
	want, err := canonicalPath(filepath.Join(realRoot, "missing", "seekdb"))
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("canonical path = %q, want %q", got, want)
	}
}

func TestCanonicalPathRequiresNonEmptyTrimmedValue(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"", " ", "\t/path", "/path\n"} {
		if _, err := canonicalPath(value); err == nil {
			t.Fatalf("canonicalPath(%q) accepted an invalid value", value)
		}
	}
}

func TestLibraryCandidatesPreserveExplicitChoice(t *testing.T) {
	t.Parallel()
	configured := filepath.Join(t.TempDir(), "libseekdb.test")
	candidates, err := libraryCandidates(configured)
	if err != nil {
		t.Fatal(err)
	}
	want, err := canonicalPath(configured)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0] != want {
		t.Fatalf("library candidates = %v", candidates)
	}
}

func TestOpenReportsUnavailableExplicitLibrary(t *testing.T) {
	t.Parallel()
	_, err := Open(t.Context(), Config{
		Path: filepath.Join(t.TempDir(), "seekdb"), LibraryPath: filepath.Join(t.TempDir(), "missing-library"),
	})
	var unavailable *UnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("open error = %T %v, want UnavailableError", err, err)
	}
}

func TestOpenClosesNativeInstanceWhenCancellationRepeatsDuringHandshake(t *testing.T) {
	compiler, err := exec.LookPath("cc")
	if err != nil {
		t.Skip("C compiler is unavailable")
	}
	root := t.TempDir()
	source := filepath.Join(root, "seekdb_fixture.c")
	library := filepath.Join(root, "libseekdb_fixture.so")
	if runtime.GOOS == "darwin" {
		library = filepath.Join(root, "libseekdb_fixture.dylib")
	}
	fixture := `
#include <stdio.h>
#include <stdlib.h>
#include <unistd.h>

typedef struct {
    const char *transport;
    unsigned int port;
    const char *endpoint;
    const char *user;
} SeekDBConnectionOptions;

static void signal_fd(const char *name) {
    const char *value = getenv(name);
    if (value == NULL) return;
    int fd = atoi(value);
    if (fd < 0) return;
    char signal = '1';
    (void)write(fd, &signal, 1);
}

static void wait_for_signal_fd(const char *name) {
    const char *value = getenv(name);
    if (value == NULL) return;
    int fd = atoi(value);
    if (fd < 0) return;
    char signal;
    (void)read(fd, &signal, 1);
}

int seekdb_open(const char *directory, const char **error, void **out) {
    (void)directory;
    (void)error;
    signal_fd("POWERCONTEXT_SEEKDB_TEST_OPENED_FD");
    wait_for_signal_fd("POWERCONTEXT_SEEKDB_TEST_RELEASE_FD");
    *out = (void *)0x1;
    return 0;
}

int seekdb_close(void *handle) {
    (void)handle;
    signal_fd("POWERCONTEXT_SEEKDB_TEST_CLOSED_FD");
    return 0;
}

int seekdb_connection_options(void *handle, SeekDBConnectionOptions *out) {
    (void)handle;
    out->transport = "tcp";
    out->port = 2881;
    out->endpoint = "";
    out->user = "root";
    return 0;
}
`
	if err := os.WriteFile(source, []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}
	arguments := []string{"-shared", "-fPIC", "-o", library, source}
	if runtime.GOOS == "darwin" {
		arguments = []string{"-dynamiclib", "-o", library, source}
	}
	if output, err := exec.Command(compiler, arguments...).CombinedOutput(); err != nil {
		t.Fatalf("compile seekDB fixture: %v: %s", err, output)
	}

	for attempt := 0; attempt < 3; attempt++ {
		opened, openedSignal := seekDBFixturePipe(t)
		release, releaseSignal := seekDBFixturePipe(t)
		closed, closedSignal := seekDBFixturePipe(t)
		t.Setenv("POWERCONTEXT_SEEKDB_TEST_OPENED_FD", strconv.Itoa(int(openedSignal.Fd())))
		t.Setenv("POWERCONTEXT_SEEKDB_TEST_RELEASE_FD", strconv.Itoa(int(release.Fd())))
		t.Setenv("POWERCONTEXT_SEEKDB_TEST_CLOSED_FD", strconv.Itoa(int(closedSignal.Fd())))
		ctx, cancel := context.WithCancel(t.Context())
		result := make(chan error, 1)
		go func() {
			instance, openErr := Open(ctx, Config{Path: filepath.Join(root, "data"), LibraryPath: library})
			if instance != nil {
				_ = instance.Close(context.Background())
			}
			result <- openErr
		}()
		waitSeekDBFixtureSignal(t, opened, "native open")
		cancel()
		cancel()
		cancel()
		if _, err := releaseSignal.Write([]byte{'1'}); err != nil {
			t.Fatal(err)
		}
		if openErr := <-result; !errors.Is(openErr, context.Canceled) {
			t.Fatalf("open error = %v, want context cancellation", openErr)
		}
		waitSeekDBFixtureSignal(t, closed, "native close")
	}
}

func seekDBFixturePipe(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = reader.Close()
		_ = writer.Close()
	})
	return reader, writer
}

func waitSeekDBFixtureSignal(t *testing.T, reader *os.File, name string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		var signal [1]byte
		_, err := io.ReadFull(reader, signal[:])
		result <- err
	}()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("%s signal: %v", name, err)
		}
	case <-ctx.Done():
		t.Fatalf("%s signal: %v", name, context.Cause(ctx))
	}
}
