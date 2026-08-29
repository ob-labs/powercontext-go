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
#include <time.h>

typedef struct {
    const char *transport;
    unsigned int port;
    const char *endpoint;
    const char *user;
} SeekDBConnectionOptions;

static void mark(const char *name, const char *value) {
    const char *path = getenv(name);
    if (path == NULL) return;
    FILE *file = fopen(path, "w");
    if (file == NULL) return;
    fputs(value, file);
    fclose(file);
}

static void wait_for_release(const char *name) {
    const char *path = getenv(name);
    if (path == NULL) return;
    for (;;) {
        FILE *file = fopen(path, "r");
        if (file != NULL) {
            fclose(file);
            return;
        }
        struct timespec delay = { .tv_sec = 0, .tv_nsec = 1000000 };
        nanosleep(&delay, NULL);
    }
}

int seekdb_open(const char *directory, const char **error, void **out) {
    (void)directory;
    (void)error;
    mark("POWERCONTEXT_SEEKDB_TEST_OPENED", "opened");
    wait_for_release("POWERCONTEXT_SEEKDB_TEST_RELEASED");
    *out = (void *)0x1;
    return 0;
}

int seekdb_close(void *handle) {
    (void)handle;
    mark("POWERCONTEXT_SEEKDB_TEST_CLOSED", "closed");
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
		opened := filepath.Join(root, "opened-"+strconv.Itoa(attempt))
		closed := filepath.Join(root, "closed-"+strconv.Itoa(attempt))
		released := filepath.Join(root, "released-"+strconv.Itoa(attempt))
		t.Setenv("POWERCONTEXT_SEEKDB_TEST_OPENED", opened)
		t.Setenv("POWERCONTEXT_SEEKDB_TEST_CLOSED", closed)
		t.Setenv("POWERCONTEXT_SEEKDB_TEST_RELEASED", released)
		t.Cleanup(func() {
			_ = os.WriteFile(released, []byte("released"), 0o600)
		})
		ctx, cancel := context.WithCancel(t.Context())
		result := make(chan error, 1)
		go func() {
			instance, openErr := Open(ctx, Config{Path: filepath.Join(root, "data"), LibraryPath: library})
			if instance != nil {
				_ = instance.Close(context.Background())
			}
			result <- openErr
		}()
		deadline := time.Now().Add(10 * time.Second)
		for {
			if _, statErr := os.Stat(opened); statErr == nil {
				break
			}
			select {
			case openErr := <-result:
				t.Fatalf("native open returned before entering the fixture: %v", openErr)
			default:
			}
			if time.Now().After(deadline) {
				t.Fatal("native open did not start")
			}
			time.Sleep(time.Millisecond)
		}
		cancel()
		cancel()
		cancel()
		if err := os.WriteFile(released, []byte("released"), 0o600); err != nil {
			t.Fatal(err)
		}
		if openErr := <-result; !errors.Is(openErr, context.Canceled) {
			t.Fatalf("open error = %v, want context cancellation", openErr)
		}
		payload, readErr := os.ReadFile(closed)
		if readErr != nil || string(payload) != "closed" {
			t.Fatalf("native close marker = %q, error = %v", payload, readErr)
		}
	}
}
