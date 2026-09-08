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
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const maximumPersonalServiceFileSize = 1 << 20

// newCurrentLinuxSystemdBoundary performs current-user discovery only when a
// later lifecycle command asks for it. Task 2 does not wire this constructor
// into a CLI command, so it cannot mutate a host service by itself.
func newCurrentLinuxSystemdBoundary() (*linuxSystemdBoundary, error) {
	if os.Geteuid() == 0 {
		return nil, newPersonalServicePlatformError("configuration")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, newPersonalServicePlatformError("configuration")
	}
	roots, err := newLinuxPersonalServiceRoots(filepath.ToSlash(home))
	if err != nil {
		return nil, err
	}
	files, err := newLinuxPrivateFiles(roots)
	if err != nil {
		return nil, err
	}
	locker := &linuxOperationFileLock{files: files, path: filepath.FromSlash(path.Join(roots.stateRoot, "personal-service.lock"))}
	return newLinuxSystemdBoundary(roots, linuxSystemdProcessRunner{}, files, locker)
}

type linuxPrivateFiles struct {
	home  string
	owner uint32
}

func newLinuxPrivateFiles(roots linuxPersonalServiceRoots) (*linuxPrivateFiles, error) {
	home := filepath.FromSlash(filepath.Dir(roots.configRoot))
	owner := uint32(os.Geteuid())
	if owner == 0 || !safeLinuxUserHome(home, owner) {
		return nil, newPersonalServicePlatformError("configuration")
	}
	return &linuxPrivateFiles{home: home, owner: owner}, nil
}

func (f *linuxPrivateFiles) Read(ctx context.Context, name string) ([]byte, bool, error) {
	if err := contextError(ctx); err != nil {
		return nil, false, err
	}
	file, err := f.openPrivate(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maximumPersonalServiceFileSize+1))
	if err != nil || len(content) > maximumPersonalServiceFileSize {
		return nil, false, errors.New("private file cannot be read")
	}
	return bytes.Clone(content), true, nil
}

func (f *linuxPrivateFiles) Validate(ctx context.Context, name string) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	file, err := f.openPrivate(name)
	if err != nil {
		return err
	}
	return file.Close()
}

func (f *linuxPrivateFiles) Write(ctx context.Context, name string, content []byte) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	parent := filepath.Dir(name)
	if err := f.ensurePrivateDirectories(parent); err != nil {
		return err
	}
	if existing, err := f.openPrivate(name); err == nil {
		if closeErr := existing.Close(); closeErr != nil {
			return closeErr
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temporary, err := os.CreateTemp(parent, ".powercontext.service-")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer func() { _ = os.Remove(temporaryName) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, name); err != nil {
		return err
	}
	return os.Chmod(name, 0o600)
}

func (f *linuxPrivateFiles) Remove(ctx context.Context, name string) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	file, err := f.openPrivate(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if closeErr := file.Close(); closeErr != nil {
		return closeErr
	}
	return os.Remove(name)
}

func (f *linuxPrivateFiles) openPrivate(name string) (*os.File, error) {
	before, err := linuxPrivateIdentity(name)
	if err != nil {
		return nil, err
	}
	if !validLinuxPrivateFileObservation(before, f.owner) {
		return nil, errors.New("unsafe private file")
	}
	descriptor, err := unix.Open(name, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(descriptor, &stat); err != nil {
		_ = unix.Close(descriptor)
		return nil, err
	}
	after := linuxPrivateIdentityFromStat(stat)
	if !validLinuxPrivateFile(before, after, f.owner) {
		_ = unix.Close(descriptor)
		return nil, errors.New("private file changed during open")
	}
	return os.NewFile(uintptr(descriptor), filepath.Base(name)), nil
}

func (f *linuxPrivateFiles) ensurePrivateDirectories(target string) error {
	relative, err := filepath.Rel(f.home, target)
	if err != nil || relative == ".." || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("private path escaped current user root")
	}
	current := f.home
	for _, element := range strings.Split(relative, string(filepath.Separator)) {
		if element == "" || element == "." {
			continue
		}
		current = filepath.Join(current, element)
		if err := mkdirPrivateDirectory(current, f.owner); err != nil {
			return err
		}
	}
	return nil
}

func mkdirPrivateDirectory(name string, owner uint32) error {
	if err := os.Mkdir(name, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	if !safeLinuxDirectory(name, owner) {
		return errors.New("unsafe private directory")
	}
	return nil
}

func safeLinuxDirectory(name string, owner uint32) bool {
	var stat unix.Stat_t
	if unix.Lstat(name, &stat) != nil {
		return false
	}
	return stat.Uid == owner && stat.Mode&unix.S_IFMT == unix.S_IFDIR && stat.Mode&0o077 == 0
}

func safeLinuxUserHome(name string, owner uint32) bool {
	var stat unix.Stat_t
	if unix.Lstat(name, &stat) != nil {
		return false
	}
	return stat.Uid == owner && stat.Mode&unix.S_IFMT == unix.S_IFDIR
}

func linuxPrivateIdentity(name string) (linuxPrivateFileIdentity, error) {
	var stat unix.Stat_t
	if err := unix.Lstat(name, &stat); err != nil {
		return linuxPrivateFileIdentity{}, err
	}
	return linuxPrivateIdentityFromStat(stat), nil
}

func linuxPrivateIdentityFromStat(stat unix.Stat_t) linuxPrivateFileIdentity {
	mode := fs.FileMode(stat.Mode & 0o777)
	switch stat.Mode & unix.S_IFMT {
	case unix.S_IFREG:
	case unix.S_IFLNK:
		mode |= fs.ModeSymlink
	case unix.S_IFIFO:
		mode |= fs.ModeNamedPipe
	case unix.S_IFDIR:
		mode |= fs.ModeDir
	default:
		mode |= fs.ModeIrregular
	}
	return linuxPrivateFileIdentity{device: uint64(stat.Dev), inode: stat.Ino, mode: mode, uid: stat.Uid}
}

type linuxOperationFileLock struct {
	files *linuxPrivateFiles
	path  string
}

func (l *linuxOperationFileLock) WithLock(ctx context.Context, operation func(context.Context) error) error {
	if l == nil || l.files == nil || operation == nil {
		return errors.New("invalid operation lock")
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	if err := l.files.ensurePrivateDirectories(filepath.Dir(l.path)); err != nil {
		return err
	}
	descriptor, err := unix.Open(l.path, unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(descriptor), filepath.Base(l.path))
	defer file.Close()
	var stat unix.Stat_t
	if err := unix.Fstat(descriptor, &stat); err != nil || !validLinuxPrivateFileObservation(linuxPrivateIdentityFromStat(stat), l.files.owner) {
		return errors.New("unsafe operation lock")
	}
	if err := unix.Flock(descriptor, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return err
	}
	defer func() { _ = unix.Flock(descriptor, unix.LOCK_UN) }()
	return operation(ctx)
}

type linuxSystemdProcessRunner struct{}

func (linuxSystemdProcessRunner) Run(ctx context.Context, program string, arguments ...string) (linuxSystemdProcessResult, error) {
	if err := contextError(ctx); err != nil {
		return linuxSystemdProcessResult{}, err
	}
	command := exec.CommandContext(ctx, program, arguments...)
	output := &linuxSystemdOutput{limit: maximumPersonalServiceFileSize}
	command.Stdout = output
	command.Stderr = io.Discard
	err := command.Run()
	if contextErr := contextError(ctx); contextErr != nil {
		return linuxSystemdProcessResult{}, contextErr
	}
	if output.exceeded {
		return linuxSystemdProcessResult{}, errors.New("systemd command output exceeded limit")
	}
	if exitError, found := errors.AsType[*exec.ExitError](err); found {
		return linuxSystemdProcessResult{exitCode: exitError.ExitCode(), stdout: bytes.Clone(output.buffer.Bytes())}, nil
	}
	if err != nil {
		return linuxSystemdProcessResult{}, err
	}
	return linuxSystemdProcessResult{stdout: bytes.Clone(output.buffer.Bytes())}, nil
}

type linuxSystemdOutput struct {
	buffer   bytes.Buffer
	limit    int
	exceeded bool
}

func (b *linuxSystemdOutput) Write(value []byte) (int, error) {
	original := len(value)
	remaining := b.limit - b.buffer.Len()
	if remaining <= 0 {
		b.exceeded = true
		return original, nil
	}
	if len(value) > remaining {
		b.exceeded = true
		value = value[:remaining]
	}
	_, _ = b.buffer.Write(value)
	return original, nil
}
