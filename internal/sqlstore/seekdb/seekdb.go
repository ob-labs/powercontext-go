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

// Package seekdb loads and owns the embedded seekDB runtime used by the SQL store.
package seekdb

/*
#cgo linux LDFLAGS: -ldl
#include "loader.h"
#include <stdlib.h>
*/
import "C"

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"unsafe"
)

type ConnectionOptions struct {
	Transport string
	Port      uint
	Endpoint  string
	User      string
}

type Config struct {
	Path        string
	LibraryPath string
}

type UnavailableError struct{ Cause error }

func (e *UnavailableError) Error() string {
	return "embedded seekDB requires libseekdb and its sibling seekdb executable on Linux or macOS"
}

func (e *UnavailableError) Unwrap() error { return e.Cause }

type Instance struct {
	mu      sync.Mutex
	library *C.PCSeekDBLibrary
	handle  unsafe.Pointer
	options ConnectionOptions
	closed  bool
}

func Open(ctx context.Context, config Config) (*Instance, error) {
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	directory, err := canonicalPath(config.Path)
	if err != nil {
		return nil, err
	}
	if mkdirErr := os.MkdirAll(filepath.Dir(directory), 0o750); mkdirErr != nil {
		return nil, mkdirErr
	}
	library, err := loadLibrary(config.LibraryPath)
	if err != nil {
		return nil, err
	}
	directoryCString := C.CString(directory)
	defer C.free(unsafe.Pointer(directoryCString))
	var handle unsafe.Pointer
	if code := int(C.pc_seekdb_instance_open(library, directoryCString, &handle)); code != 0 {
		C.pc_seekdb_library_close(library)
		return nil, fmt.Errorf("seekdb: open embedded instance: return code %d", code)
	}
	instance := &Instance{library: library, handle: handle}
	if err := context.Cause(ctx); err != nil {
		_ = instance.Close(context.Background())
		return nil, err
	}
	var options C.PCSeekDBConnectionOptions
	if code := int(C.pc_seekdb_connection_options(library, handle, &options)); code != 0 {
		_ = instance.Close(context.Background())
		return nil, fmt.Errorf("seekdb: read connection options: return code %d", code)
	}
	instance.options = ConnectionOptions{
		Transport: borrowedString(options.transport),
		Port:      uint(options.port),
		Endpoint:  borrowedString(options.endpoint),
		User:      borrowedString(options.user),
	}
	if err := validateConnectionOptions(instance.options); err != nil {
		_ = instance.Close(context.Background())
		return nil, err
	}
	// The native open and connection-option calls are synchronous and cannot be
	// interrupted safely. Re-check cancellation after the complete handshake so
	// a cancellation racing either call never publishes a live native instance.
	if err := context.Cause(ctx); err != nil {
		_ = instance.Close(context.Background())
		return nil, err
	}
	return instance, nil
}

func (i *Instance) ConnectionOptions() ConnectionOptions {
	if i == nil {
		return ConnectionOptions{}
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.options
}

func (i *Instance) Close(_ context.Context) error {
	if i == nil {
		return nil
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed {
		return nil
	}
	if code := int(C.pc_seekdb_instance_close(i.library, i.handle)); code != 0 {
		return fmt.Errorf("seekdb: close embedded instance: return code %d", code)
	}
	i.handle = nil
	C.pc_seekdb_library_close(i.library)
	i.library = nil
	i.closed = true
	return nil
}

func loadLibrary(configured string) (*C.PCSeekDBLibrary, error) {
	candidates, err := libraryCandidates(configured)
	if err != nil {
		return nil, err
	}
	var failures []error
	for _, candidate := range candidates {
		candidateCString := C.CString(candidate)
		var library *C.PCSeekDBLibrary
		var message *C.char
		code := int(C.pc_seekdb_library_open(candidateCString, &library, &message))
		C.free(unsafe.Pointer(candidateCString))
		if code == 0 && library != nil {
			if message != nil {
				C.pc_seekdb_error_free(message)
			}
			return library, nil
		}
		detail := "dynamic loader rejected the library"
		if message != nil {
			detail = C.GoString(message)
			C.pc_seekdb_error_free(message)
		}
		failures = append(failures, fmt.Errorf("%s: %s", candidate, detail))
	}
	return nil, &UnavailableError{Cause: errors.Join(failures...)}
}

func libraryCandidates(configured string) ([]string, error) {
	if strings.TrimSpace(configured) != "" {
		resolved, err := canonicalPath(configured)
		if err != nil {
			return nil, err
		}
		return []string{resolved}, nil
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return nil, err
	}
	name := "libseekdb.so"
	if runtime.GOOS == "darwin" {
		name = "libseekdb.dylib"
	}
	return []string{filepath.Join(filepath.Dir(executable), name), name}, nil
}

func canonicalPath(value string) (string, error) {
	if strings.TrimSpace(value) == "" || value != strings.TrimSpace(value) {
		return "", errors.New("seekdb: path must be a non-empty trimmed string")
	}
	if value == "~" || strings.HasPrefix(value, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		value = filepath.Join(home, strings.TrimPrefix(value, "~/"))
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	absolute = filepath.Clean(absolute)
	current := absolute
	var suffix []string
	for {
		resolved, resolveErr := filepath.EvalSymlinks(current)
		if resolveErr == nil {
			for index := len(suffix) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, suffix[index])
			}
			return filepath.Clean(resolved), nil
		}
		if !errors.Is(resolveErr, os.ErrNotExist) {
			return "", resolveErr
		}
		parent := filepath.Dir(current)
		if parent == current {
			return absolute, nil
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
}

func validateConnectionOptions(value ConnectionOptions) error {
	if value.User == "" {
		return errors.New("seekdb: embedded instance did not provide a user")
	}
	switch value.Transport {
	case "tcp":
		if value.Port < 1 || value.Port > 65_535 {
			return errors.New("seekdb: embedded instance provided an invalid TCP port")
		}
	case "unix_socket":
		if value.Endpoint == "" {
			return errors.New("seekdb: embedded instance did not provide a Unix socket")
		}
	default:
		return fmt.Errorf("seekdb: unsupported connection transport %q", value.Transport)
	}
	return nil
}

func borrowedString(value *C.char) string {
	if value == nil {
		return ""
	}
	return C.GoString(value)
}
