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

package server

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestArtifactCursorKeyPersistsAcrossConcurrentInitialization(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "private-database.db")
	var workers sync.WaitGroup
	var keys [16][32]byte
	var failures [16]error
	for i := range keys {
		workers.Go(func() { keys[i], failures[i] = loadArtifactCursorKey(t.Context(), dsn) })
	}
	workers.Wait()
	for i, key := range keys {
		if failures[i] != nil || key == [32]byte{} || key != keys[0] {
			t.Fatalf("initialization %d: failed=%t, key disagreement=%t", i, failures[i] != nil, key != keys[0])
		}
	}
	path := filepath.Join(filepath.Dir(dsn), ".private-database.db.cursor-key")
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, keys[0][:]) {
		t.Fatal("stored cursor key differs")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("cursor key permissions = %o", info.Mode().Perm())
	}
	reopened, err := loadArtifactCursorKey(t.Context(), dsn)
	if err != nil || reopened != keys[0] {
		t.Fatal("reopening replaced the signing key")
	}
}

func TestArtifactCursorKeyRejectsInvalidMaterialWithoutReplacement(t *testing.T) {
	for _, length := range []int{0, 1, 31, 33, 4096} {
		t.Run(fmt.Sprint(length), func(t *testing.T) {
			dsn := filepath.Join(t.TempDir(), "private-database.db")
			path := filepath.Join(filepath.Dir(dsn), ".private-database.db.cursor-key")
			original := bytes.Repeat([]byte("s"), length)
			if err := os.WriteFile(path, original, 0o600); err != nil {
				t.Fatal(err)
			}
			key, err := loadArtifactCursorKey(t.Context(), dsn)
			assertCursorKeyFailure(t, key, err, dsn, path)
			stored, readErr := os.ReadFile(path)
			if readErr != nil || !bytes.Equal(stored, original) {
				t.Fatal("invalid key material was changed")
			}
		})
	}
	t.Run("directory", func(t *testing.T) {
		dsn := filepath.Join(t.TempDir(), "private-database.db")
		path := filepath.Join(filepath.Dir(dsn), ".private-database.db.cursor-key")
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		key, err := loadArtifactCursorKey(t.Context(), dsn)
		assertCursorKeyFailure(t, key, err, dsn, path)
		info, statErr := os.Stat(path)
		if statErr != nil || !info.IsDir() {
			t.Fatal("key directory was changed")
		}
	})
	t.Run("unwritable parent", func(t *testing.T) {
		parent := filepath.Join(t.TempDir(), "private-parent")
		if err := os.WriteFile(parent, []byte("sentinel"), 0o600); err != nil {
			t.Fatal(err)
		}
		dsn := filepath.Join(parent, "private-database.db")
		key, err := loadArtifactCursorKey(t.Context(), dsn)
		assertCursorKeyFailure(t, key, err, dsn, parent)
	})
}

func TestArtifactCursorKeyMemoryIsEphemeral(t *testing.T) {
	first, err := loadArtifactCursorKey(t.Context(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	second, err := loadArtifactCursorKey(t.Context(), ":memory:")
	if err != nil || first == second || first == [32]byte{} || second == [32]byte{} {
		t.Fatal("memory databases require independent random signing keys")
	}
}

func assertCursorKeyFailure(t *testing.T, key [32]byte, err error, protected ...string) {
	t.Helper()
	if err == nil || key != [32]byte{} {
		t.Fatal("invalid cursor key must fail closed")
	}
	for _, value := range protected {
		if strings.Contains(fmt.Sprintf("%v %+v %#v", err, err, err), value) {
			t.Fatal("cursor key error exposed a protected path")
		}
	}
}
