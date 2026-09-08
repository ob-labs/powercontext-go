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
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/ob-labs/powercontext-go/internal/personalsvc"
	golangwindows "golang.org/x/sys/windows"
)

const maxArtifactBytes = 1 << 20

// Config supplies the pure Task Scheduler plan and the current user's
// composition-owned data root. New never reads an environment variable.
type Config struct {
	UserDataRoot string
	Plan         personalsvc.TaskSchedulerSpec
}

// Composition binds one Adapter and its cross-operation lock to the same
// current-user root and Task Scheduler identity.
type Composition struct {
	adapter  *Adapter
	boundary *OperationBoundary
}

// New constructs the production-only fixed PowerContext Task Scheduler
// composition for the current process token's interactive user.
func New(config Config) (*Composition, error) {
	artifacts, err := newNativeArtifactStore(config.UserDataRoot)
	if err != nil {
		return nil, newError("configuration", nil)
	}
	executor, err := NewExecutor()
	if err != nil {
		return nil, newError("configuration", nil)
	}
	portable, err := personalsvc.NewTaskScheduler(executor)
	if err != nil {
		return nil, newError("configuration", nil)
	}
	identity, err := currentUserIdentity()
	if err != nil || !validUserIdentity(identity) {
		return nil, newError("configuration", nil)
	}
	adapter, err := newAdapter(
		config.Plan,
		config.UserDataRoot,
		personalsvc.TaskSchedulerTaskName,
		identity,
		portableScheduler{scheduler: portable},
		artifacts,
		loopbackHTTPClient(),
	)
	if err != nil {
		return nil, err
	}
	boundary, err := newOperationBoundary(config.UserDataRoot)
	if err != nil {
		return nil, err
	}
	return &Composition{adapter: adapter, boundary: boundary}, nil
}

// Adapter returns the Windows native adapter owned by the composition.
func (c *Composition) Adapter() *Adapter {
	if c == nil || c.adapter == nil {
		return nil
	}
	return c.adapter
}

// OperationBoundary returns the lock paired with the adapter's exact root.
func (c *Composition) OperationBoundary() personalsvc.OperationBoundary {
	if c == nil || c.boundary == nil {
		return nil
	}
	return c.boundary
}

type portableScheduler struct{ scheduler personalsvc.TaskScheduler }

func (s portableScheduler) Query(ctx context.Context) (personalsvc.TaskSchedulerState, []byte, error) {
	return s.scheduler.QueryXML(ctx)
}

func (s portableScheduler) Create(ctx context.Context, path string) error {
	return s.scheduler.Create(ctx, path)
}

func (s portableScheduler) Run(ctx context.Context) error     { return s.scheduler.Run(ctx) }
func (s portableScheduler) End(ctx context.Context) error     { return s.scheduler.End(ctx) }
func (s portableScheduler) Enable(ctx context.Context) error  { return s.scheduler.Enable(ctx) }
func (s portableScheduler) Disable(ctx context.Context) error { return s.scheduler.Disable(ctx) }
func (s portableScheduler) Delete(ctx context.Context) error  { return s.scheduler.Delete(ctx) }

func (s portableScheduler) State(ctx context.Context) (personalsvc.ManagerState, error) {
	output, err := s.scheduler.QueryStatus(ctx)
	if err != nil {
		return personalsvc.ManagerUnknown, err
	}
	return parseTaskState(output)
}

type nativeArtifactStore struct {
	root         string
	artifactPath string
}

func newNativeArtifactStore(root string) (*nativeArtifactStore, error) {
	if !validUserDataRoot(root) {
		return nil, newError("configuration", nil)
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, newError("configuration", nil)
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil || !strings.EqualFold(filepath.Clean(resolved), root) {
		return nil, newError("configuration", nil)
	}
	return &nativeArtifactStore{
		root:         root,
		artifactPath: filepath.Join(root, filepath.FromSlash("PowerContext/Services/personal-server.xml")),
	}, nil
}

func (s *nativeArtifactStore) Read(ctx context.Context, path string) ([]byte, bool, error) {
	if err := s.check(ctx, path); err != nil {
		return nil, false, err
	}
	entry, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil || !entry.Mode().IsRegular() || entry.Mode()&os.ModeSymlink != 0 {
		return nil, false, errors.New("unsafe personal service artifact")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxArtifactBytes {
		return nil, false, errors.New("unsafe personal service artifact")
	}
	content, err := io.ReadAll(io.LimitReader(file, maxArtifactBytes+1))
	if err != nil || len(content) > maxArtifactBytes {
		return nil, false, errors.New("invalid personal service artifact")
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	return bytes.Clone(content), true, nil
}

func (s *nativeArtifactStore) Write(ctx context.Context, path string, content []byte) error {
	if err := s.check(ctx, path); err != nil || len(content) == 0 || len(content) > maxArtifactBytes {
		return errors.New("invalid personal service artifact write")
	}
	if err := s.ensureDirectory(); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		return errors.New("unsafe personal service artifact")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".personal-server-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
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
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func (s *nativeArtifactStore) Remove(ctx context.Context, path string) error {
	if err := s.check(ctx, path); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("unsafe personal service artifact")
	}
	return os.Remove(path)
}

func (*nativeArtifactStore) RegularFile(ctx context.Context, path string) (bool, error) {
	if ctx == nil || !filepath.IsAbs(path) {
		return false, errors.New("invalid executable path")
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0, nil
}

func (s *nativeArtifactStore) ensureDirectory() error {
	directory := filepath.Dir(s.artifactPath)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	resolved, err := filepath.EvalSymlinks(directory)
	if err != nil || !strings.EqualFold(filepath.Clean(resolved), directory) {
		return errors.New("unsafe personal service artifact directory")
	}
	return nil
}

func (s *nativeArtifactStore) check(ctx context.Context, path string) error {
	if s == nil || ctx == nil || path != s.artifactPath {
		return errors.New("invalid personal service artifact path")
	}
	return ctx.Err()
}

func currentUserIdentity() (userIdentity, error) {
	var sessionID uint32
	if err := golangwindows.ProcessIdToSessionId(uint32(os.Getpid()), &sessionID); err != nil || !validInteractiveSession(sessionID) {
		return userIdentity{}, errors.New("current Windows session is not interactive")
	}
	user, err := golangwindows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil {
		return userIdentity{}, errors.New("current Windows user is unavailable")
	}
	name, domain, _, err := user.User.Sid.LookupAccount("")
	if err != nil || name == "" {
		return userIdentity{}, errors.New("current Windows account is unavailable")
	}
	account := name
	if domain != "" {
		account = domain + `\` + name
	}
	expectedSID := user.User.Sid.String()
	return userIdentity{
		sid: expectedSID, account: account, name: name,
		lookup: func(value string) bool {
			resolved, _, _, lookupErr := golangwindows.LookupSID("", value)
			return lookupErr == nil && resolved != nil && resolved.String() == expectedSID
		},
	}, nil
}

func validInteractiveSession(sessionID uint32) bool { return sessionID != 0 }

func loopbackHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}
