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
	"crypto/rand"
	"encoding/hex"
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
	descriptor, err := openAbsoluteDirectoryNoSymlink(home)
	if err != nil {
		return nil, newPersonalServicePlatformError("configuration")
	}
	defer unix.Close(descriptor)
	var stat unix.Stat_t
	if owner == 0 || unix.Fstat(descriptor, &stat) != nil || !safeLinuxUserHomeStat(stat, owner) {
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
	parent, base, err := f.openPrivateParent(name, true)
	if err != nil {
		return err
	}
	defer unix.Close(parent)
	before, exists, err := f.privateAt(parent, base)
	if err != nil {
		return err
	}
	if exists && !validLinuxPrivateFileObservation(before, f.owner) {
		return errors.New("unsafe private file")
	}
	temporary, temporaryName, err := createPrivateTemporaryFile(parent)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Unlinkat(parent, temporaryName, 0) }()
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
	if err := unix.Renameat(parent, temporaryName, parent, base); err != nil {
		return err
	}
	return nil
}

func (f *linuxPrivateFiles) Remove(ctx context.Context, name string) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	parent, base, err := f.openPrivateParent(name, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer unix.Close(parent)
	identity, exists, err := f.privateAt(parent, base)
	if err != nil || !exists {
		return err
	}
	if !validLinuxPrivateFileObservation(identity, f.owner) {
		return errors.New("unsafe private file")
	}
	return unix.Unlinkat(parent, base, 0)
}

func (f *linuxPrivateFiles) openPrivate(name string) (*os.File, error) {
	parent, base, err := f.openPrivateParent(name, false)
	if err != nil {
		return nil, err
	}
	defer unix.Close(parent)
	before, exists, err := f.privateAt(parent, base)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, os.ErrNotExist
	}
	if !validLinuxPrivateFileObservation(before, f.owner) {
		return nil, errors.New("unsafe private file")
	}
	descriptor, err := unix.Openat(parent, base, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
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
	return os.NewFile(uintptr(descriptor), base), nil
}

func (f *linuxPrivateFiles) openOrCreatePrivate(name string, flags int) (*os.File, error) {
	parent, base, err := f.openPrivateParent(name, true)
	if err != nil {
		return nil, err
	}
	defer unix.Close(parent)
	before, existed, err := f.privateAt(parent, base)
	if err != nil {
		return nil, err
	}
	if existed && !validLinuxPrivateFileObservation(before, f.owner) {
		return nil, errors.New("unsafe private file")
	}
	descriptor, err := unix.Openat(parent, base, flags|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(descriptor, &stat); err != nil {
		_ = unix.Close(descriptor)
		return nil, err
	}
	after := linuxPrivateIdentityFromStat(stat)
	if !validLinuxPrivateFileObservation(after, f.owner) || existed && !validLinuxPrivateFile(before, after, f.owner) {
		_ = unix.Close(descriptor)
		return nil, errors.New("private file changed during open")
	}
	return os.NewFile(uintptr(descriptor), base), nil
}

func (f *linuxPrivateFiles) openPrivateParent(name string, create bool) (int, string, error) {
	if name != filepath.Clean(name) || !filepath.IsAbs(name) {
		return -1, "", errors.New("invalid private file path")
	}
	base := filepath.Base(name)
	if base == "." || base == string(filepath.Separator) || base == ".." {
		return -1, "", errors.New("invalid private file path")
	}
	parent := filepath.Dir(name)
	if create {
		descriptor, err := f.ensurePrivateDirectories(parent)
		return descriptor, base, err
	}
	descriptor, err := f.openOwnedPrivateDirectory(parent)
	return descriptor, base, err
}

func (f *linuxPrivateFiles) ensurePrivateDirectories(target string) (int, error) {
	relative, err := filepath.Rel(f.home, target)
	if err != nil || relative == ".." || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return -1, errors.New("private path escaped current user root")
	}
	descriptor, err := openAbsoluteDirectoryNoSymlink(f.home)
	if err != nil {
		return -1, err
	}
	var home unix.Stat_t
	if err := unix.Fstat(descriptor, &home); err != nil || !safeLinuxUserHomeStat(home, f.owner) {
		_ = unix.Close(descriptor)
		return -1, errors.New("unsafe current user root")
	}
	for _, element := range strings.Split(relative, string(filepath.Separator)) {
		if element == "" || element == "." {
			continue
		}
		next, openErr := unix.Openat(descriptor, element, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if errors.Is(openErr, unix.ENOENT) {
			if mkdirErr := unix.Mkdirat(descriptor, element, 0o700); mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
				_ = unix.Close(descriptor)
				return -1, mkdirErr
			}
			next, openErr = unix.Openat(descriptor, element, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		}
		if openErr != nil {
			_ = unix.Close(descriptor)
			return -1, openErr
		}
		_ = unix.Close(descriptor)
		descriptor = next
		var stat unix.Stat_t
		if err := unix.Fstat(descriptor, &stat); err != nil || !safeLinuxDirectoryStat(stat, f.owner) {
			_ = unix.Close(descriptor)
			return -1, errors.New("unsafe private directory")
		}
	}
	if relative == "." {
		var stat unix.Stat_t
		if err := unix.Fstat(descriptor, &stat); err != nil || !safeLinuxDirectoryStat(stat, f.owner) {
			_ = unix.Close(descriptor)
			return -1, errors.New("unsafe private directory")
		}
	}
	return descriptor, nil
}

func (f *linuxPrivateFiles) openOwnedPrivateDirectory(name string) (int, error) {
	descriptor, err := openAbsoluteDirectoryNoSymlink(name)
	if err != nil {
		return -1, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(descriptor, &stat); err != nil || !safeLinuxDirectoryStat(stat, f.owner) {
		_ = unix.Close(descriptor)
		return -1, errors.New("unsafe private directory")
	}
	return descriptor, nil
}

func (f *linuxPrivateFiles) privateAt(parent int, base string) (linuxPrivateFileIdentity, bool, error) {
	var stat unix.Stat_t
	if err := unix.Fstatat(parent, base, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return linuxPrivateFileIdentity{}, false, nil
		}
		return linuxPrivateFileIdentity{}, false, err
	}
	return linuxPrivateIdentityFromStat(stat), true, nil
}

func createPrivateTemporaryFile(parent int) (*os.File, string, error) {
	for range 128 {
		var nonce [12]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return nil, "", err
		}
		name := ".powercontext.service-" + hex.EncodeToString(nonce[:])
		descriptor, err := unix.Openat(parent, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return nil, "", err
		}
		return os.NewFile(uintptr(descriptor), name), name, nil
	}
	return nil, "", errors.New("cannot create private temporary file")
}

func openAbsoluteDirectoryNoSymlink(name string) (int, error) {
	if name != filepath.Clean(name) || !filepath.IsAbs(name) {
		return -1, errors.New("invalid directory path")
	}
	descriptor, err := unix.Open(string(filepath.Separator), unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	for _, element := range strings.Split(strings.TrimPrefix(name, string(filepath.Separator)), string(filepath.Separator)) {
		if element == "" {
			continue
		}
		next, openErr := unix.Openat(descriptor, element, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		_ = unix.Close(descriptor)
		if openErr != nil {
			return -1, openErr
		}
		descriptor = next
	}
	return descriptor, nil
}

func safeLinuxDirectoryStat(stat unix.Stat_t, owner uint32) bool {
	return stat.Uid == owner && stat.Mode&unix.S_IFMT == unix.S_IFDIR && stat.Mode&0o077 == 0
}

func safeLinuxUserHomeStat(stat unix.Stat_t, owner uint32) bool {
	return stat.Uid == owner && stat.Mode&unix.S_IFMT == unix.S_IFDIR
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
	file, err := l.files.openOrCreatePrivate(l.path, unix.O_RDWR)
	if err != nil {
		return err
	}
	defer file.Close()
	descriptor := int(file.Fd())
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
