// Copyright (c) 2026 OceanBase.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package windows

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/ob-labs/powercontext-go/internal/personalsvc"
	golangwindows "golang.org/x/sys/windows"
)

const operationLockPollInterval = 10 * time.Millisecond

// OperationBoundary serializes one complete controller lifecycle operation
// with both an in-process gate and a current-user-rooted Windows file lock.
type OperationBoundary struct {
	lockPath string
	gate     chan struct{}
}

var _ personalsvc.OperationBoundary = (*OperationBoundary)(nil)

func newOperationBoundary(userDataRoot string) (*OperationBoundary, error) {
	if !validUserDataRoot(userDataRoot) {
		return nil, newError("operation lock", nil)
	}
	return &OperationBoundary{
		lockPath: filepath.Join(userDataRoot, "PowerContext", "Services", ".personal-server.lock"),
		gate:     make(chan struct{}, 1),
	}, nil
}

// Run holds the exact lifecycle lock until operation returns. Contention is
// cancelable and never broadens the lock to a global task or filesystem root.
func (b *OperationBoundary) Run(ctx context.Context, operation func(context.Context) error) error {
	if b == nil || ctx == nil || operation == nil || b.lockPath == "" || b.gate == nil {
		return newError("operation lock", nil)
	}
	select {
	case b.gate <- struct{}{}:
		defer func() { <-b.gate }()
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(b.lockPath), 0o700); err != nil {
		return newError("operation lock", nil)
	}
	if info, err := os.Lstat(b.lockPath); err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		return newError("operation lock", nil)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return newError("operation lock", nil)
	}
	lock, err := os.OpenFile(b.lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return newError("operation lock", nil)
	}
	defer lock.Close()
	if info, err := lock.Stat(); err != nil || !info.Mode().IsRegular() {
		return newError("operation lock", nil)
	}

	overlapped := &golangwindows.Overlapped{}
	for {
		err = golangwindows.LockFileEx(
			golangwindows.Handle(lock.Fd()),
			golangwindows.LOCKFILE_EXCLUSIVE_LOCK|golangwindows.LOCKFILE_FAIL_IMMEDIATELY,
			0,
			1,
			0,
			overlapped,
		)
		if err == nil {
			break
		}
		if !errors.Is(err, golangwindows.ERROR_LOCK_VIOLATION) {
			return newError("operation lock", nil)
		}
		timer := time.NewTimer(operationLockPollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}

	operationErr := operation(ctx)
	unlockErr := golangwindows.UnlockFileEx(golangwindows.Handle(lock.Fd()), 0, 1, 0, overlapped)
	if operationErr != nil {
		return operationErr
	}
	if unlockErr != nil {
		return newError("operation lock", nil)
	}
	return nil
}
