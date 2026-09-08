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
	"context"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
)

var artifactCursorKeyMu sync.Mutex

var errArtifactCursorKey = errors.New("server: Artifact cursor signing key is unavailable")

func loadArtifactCursorKey(ctx context.Context, dsn string) ([32]byte, error) {
	// Serialize local initializers so no reader observes our exclusive creation
	// before the complete key is synced. Other processes fail closed on short keys.
	artifactCursorKeyMu.Lock()
	defer artifactCursorKeyMu.Unlock()
	if err := ctx.Err(); err != nil {
		return [32]byte{}, err
	}
	if dsn == ":memory:" {
		var key [32]byte
		_, _ = rand.Read(key[:])
		return key, nil
	}
	path := filepath.Join(filepath.Dir(dsn), "."+filepath.Base(dsn)+".cursor-key")
	key, err := readArtifactCursorKey(ctx, path)
	if err == nil {
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return [32]byte{}, errArtifactCursorKey
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		existingKey, readErr := readArtifactCursorKey(ctx, path)
		if readErr != nil {
			return [32]byte{}, errArtifactCursorKey
		}
		return existingKey, nil
	}
	if err != nil {
		return [32]byte{}, errArtifactCursorKey
	}
	_, _ = rand.Read(key[:])
	written, writeErr := file.Write(key[:])
	syncErr := file.Sync()
	closeErr := file.Close()
	if written != len(key) || writeErr != nil || syncErr != nil || closeErr != nil {
		return [32]byte{}, errArtifactCursorKey
	}
	return key, nil
}

func readArtifactCursorKey(ctx context.Context, path string) ([32]byte, error) {
	if err := ctx.Err(); err != nil {
		return [32]byte{}, err
	}
	before, err := os.Lstat(path)
	if err != nil {
		return [32]byte{}, err
	}
	if !before.Mode().IsRegular() || before.Size() != 32 {
		return [32]byte{}, errArtifactCursorKey
	}
	file, err := openArtifactCursorKey(ctx, path)
	if err != nil {
		return [32]byte{}, err
	}
	info, statErr := file.Stat()
	if statErr != nil || !info.Mode().IsRegular() || info.Size() != 32 || !os.SameFile(before, info) {
		_ = file.Close()
		return [32]byte{}, errArtifactCursorKey
	}
	var material [33]byte
	read, readErr := io.ReadFull(file, material[:])
	closeErr := file.Close()
	if read != 32 || !errors.Is(readErr, io.ErrUnexpectedEOF) || closeErr != nil {
		return [32]byte{}, errArtifactCursorKey
	}
	return [32]byte(material[:32]), nil
}
