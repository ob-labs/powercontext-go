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
	json "encoding/json/v2"
	"errors"
	"io/fs"
	"slices"
	"strings"
	"testing"

	"github.com/ob-labs/powercontext-go/internal/personalsvc"
)

func TestLinuxSystemdBoundaryUsesFixedStructuredUserBusCommands(t *testing.T) {
	boundary, runner, _ := newTestLinuxSystemdBoundary(t)
	runner.responses = []linuxSystemdCommandResult{
		{stdout: []byte(`{"type":"o","data":"/org/freedesktop/systemd1/unit/powercontext_2eservice"}`)},
		{stdout: unitPropertiesJSON(t, "loaded", "/home/alice/.config/systemd/user/powercontext.service", nil)},
		{stdout: servicePropertiesJSON(t, nil, []any{[]any{
			"/opt/powercontext/powercontext",
			[]any{"/opt/powercontext/powercontext", "server", "_service-run", "--env-file", "/home/alice/.config/powercontext/server.env", "--endpoint", "http://127.0.0.1:8123", "--data-dir", "/home/alice/.local/share/powercontext"},
			false, 0, 0, 0, 0, 0, 0, 0,
		}})},
	}

	unit, err := boundary.InspectUnit(t.Context(), "powercontext.service")
	if err != nil {
		t.Fatal(err)
	}
	if unit.LoadState() != "loaded" || unit.FragmentPath() != "/home/alice/.config/systemd/user/powercontext.service" ||
		!unit.HasDropInPaths() || len(unit.DropInPaths()) != 0 || len(unit.ExecStart()) != 1 {
		t.Fatalf("InspectUnit() = %#v", unit)
	}
	want := [][]string{
		{"busctl", "--user", "--json=short", "call", "org.freedesktop.systemd1", "/org/freedesktop/systemd1", "org.freedesktop.systemd1.Manager", "LoadUnit", "s", "powercontext.service"},
		{"busctl", "--user", "--json=short", "call", "org.freedesktop.systemd1", "/org/freedesktop/systemd1/unit/powercontext_2eservice", "org.freedesktop.DBus.Properties", "GetAll", "s", "org.freedesktop.systemd1.Unit"},
		{"busctl", "--user", "--json=short", "call", "org.freedesktop.systemd1", "/org/freedesktop/systemd1/unit/powercontext_2eservice", "org.freedesktop.DBus.Properties", "GetAll", "s", "org.freedesktop.systemd1.Service"},
	}
	if !slices.EqualFunc(runner.arguments(), want, slices.Equal) {
		t.Fatalf("user-bus argv = %q, want %q", runner.arguments(), want)
	}
}

func TestLinuxSystemdBoundaryClassifiesAbsentUserBusWithoutLeakingOutput(t *testing.T) {
	boundary, runner, _ := newTestLinuxSystemdBoundary(t)
	runner.responses = []linuxSystemdCommandResult{{exitCode: 1, stdout: []byte("user bus at /run/user/1000 token=secret")}}
	launcher, err := personalsvc.NewSystemdUserLauncher("server", "_service-run")
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := personalsvc.NewSystemdUserAdapter(boundary, "/home/alice/.config", launcher)
	if err != nil {
		t.Fatal(err)
	}
	support, supportErr := adapter.Support(t.Context())
	if supportErr != nil || support != personalsvc.SupportUnsupported {
		t.Fatalf("Support() = %s, %v; want unsupported, nil", support, supportErr)
	}
	if got := runner.arguments(); !slices.EqualFunc(got, [][]string{{"systemctl", "--user", "show-environment"}}, slices.Equal) {
		t.Fatalf("user-bus command = %q", got)
	}
}

func TestLinuxSystemdBoundaryClassifiesAbsentUnitAfterSupportAsNotLoaded(t *testing.T) {
	boundary, runner, _ := newTestLinuxSystemdBoundary(t)
	runner.responses = []linuxSystemdCommandResult{{exitCode: 1, stdout: []byte("unit path=/home/alice/.config token=secret")}}
	unit, err := boundary.InspectUnit(t.Context(), "powercontext.service")
	if err != nil || unit.LoadState() != "not-found" {
		t.Fatalf("InspectUnit() = %#v, %v; want not-found, nil", unit, err)
	}
	if got := runner.arguments(); !slices.EqualFunc(got, [][]string{{"busctl", "--user", "--json=short", "call", "org.freedesktop.systemd1", "/org/freedesktop/systemd1", "org.freedesktop.systemd1.Manager", "LoadUnit", "s", "powercontext.service"}}, slices.Equal) {
		t.Fatalf("absent-unit command = %q", got)
	}
}

func TestLinuxSystemdBoundaryRejectsGlobalRootsAndForeignDropIns(t *testing.T) {
	for _, home := range []string{"/", "/etc", "/root", "/usr/local", "/run/user/1000", "/home/alice/../bob", "relative"} {
		if _, err := newLinuxPersonalServiceRoots(home); err == nil {
			t.Fatalf("newLinuxPersonalServiceRoots(%q) succeeded", home)
		}
	}
	roots, err := newLinuxPersonalServiceRoots("/home/alice")
	if err != nil {
		t.Fatal(err)
	}
	roots.stateRoot = "/home/bob/.local/state/powercontext"
	if boundary, boundaryErr := newLinuxSystemdBoundary(roots, &testLinuxSystemdRunner{}, &testLinuxPrivateFiles{}, &testLinuxOperationLock{}); boundary != nil || boundaryErr == nil {
		t.Fatalf("newLinuxSystemdBoundary accepted split current-user roots: %v, %v", boundary, boundaryErr)
	}

	boundary, runner, _ := newTestLinuxSystemdBoundary(t)
	runner.responses = []linuxSystemdCommandResult{
		{stdout: []byte(`{"type":"o","data":"/org/freedesktop/systemd1/unit/powercontext_2eservice"}`)},
		{stdout: unitPropertiesJSON(t, "loaded", "/home/alice/.config/systemd/user/powercontext.service", []string{"/home/alice/.config/systemd/user/powercontext.service.d/foreign.conf"})},
		{stdout: servicePropertiesJSON(t, nil, nil)},
	}
	unit, err := boundary.InspectUnit(t.Context(), "powercontext.service")
	if err != nil || len(unit.DropInPaths()) != 1 {
		t.Fatalf("InspectUnit() = %#v, %v", unit, err)
	}
	launcher, err := personalsvc.NewSystemdUserLauncher("server", "_service-run")
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := personalsvc.NewSystemdUserAdapter(boundary, "/home/alice/.config", launcher)
	if err != nil {
		t.Fatal(err)
	}
	runner.responses = []linuxSystemdCommandResult{
		{stdout: []byte(`{"type":"o","data":"/org/freedesktop/systemd1/unit/powercontext_2eservice"}`)},
		{stdout: unitPropertiesJSON(t, "loaded", "/home/alice/.config/systemd/user/powercontext.service", []string{"/home/alice/.config/systemd/user/powercontext.service.d/foreign.conf"})},
		{stdout: servicePropertiesJSON(t, nil, nil)},
	}
	manager, err := adapter.InspectManager(t.Context())
	if err != nil || manager.Ownership() != personalsvc.ManagerOwnershipForeign {
		t.Fatalf("InspectManager() = %s, %v; want foreign, nil", manager.Ownership(), err)
	}
}

func TestLinuxSystemdBoundaryRejectsForeignOrMalformedStructuredBusData(t *testing.T) {
	for _, test := range []struct {
		name      string
		responses []linuxSystemdCommandResult
	}{
		{
			name: "foreign unit object",
			responses: []linuxSystemdCommandResult{
				{stdout: []byte(`{"type":"o","data":"/org/freedesktop/systemd1/unit/foreign_2eservice"}`)},
			},
		},
		{
			name: "malformed ExecStart tuple",
			responses: []linuxSystemdCommandResult{
				{stdout: []byte(`{"type":"o","data":"/org/freedesktop/systemd1/unit/powercontext_2eservice"}`)},
				{stdout: unitPropertiesJSON(t, "loaded", "/home/alice/.config/systemd/user/powercontext.service", nil)},
				{stdout: servicePropertiesJSON(t, nil, []any{[]any{"/opt/powercontext/powercontext", []any{"/opt/powercontext/powercontext"}, false}})},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			boundary, runner, _ := newTestLinuxSystemdBoundary(t)
			runner.responses = test.responses
			if _, err := boundary.InspectUnit(t.Context(), "powercontext.service"); err == nil {
				t.Fatal("InspectUnit accepted unverified user-bus data")
			}
		})
	}
}

func TestLinuxSystemdBoundaryReportsAVerifiedStaleRunningDefinition(t *testing.T) {
	desired := newTestPersonalServiceRegistration(t, "1.0.0")
	stale := newTestPersonalServiceRegistration(t, "0.9.0")
	metadata, err := stale.Encode()
	if err != nil {
		t.Fatal(err)
	}
	boundary, runner, _ := newTestLinuxSystemdBoundary(t)
	runner.responses = []linuxSystemdCommandResult{
		{stdout: []byte(`{"type":"o","data":"/org/freedesktop/systemd1/unit/powercontext_2eservice"}`)},
		{stdout: unitPropertiesJSON(t, "loaded", "/home/alice/.config/systemd/user/powercontext.service", nil)},
		{stdout: servicePropertiesJSON(t, []string{
			"POWERCONTEXT_SERVICE_OWNED=true", "POWERCONTEXT_SERVICE_METADATA=" + metadata,
		}, []any{[]any{
			"/opt/powercontext/powercontext",
			[]any{"/opt/powercontext/powercontext", "server", "_service-run", "--env-file", "/home/alice/.config/powercontext/server.env", "--endpoint", "http://127.0.0.1:8123", "--data-dir", "/home/alice/.local/share/powercontext"},
			false, 0, 0, 0, 0, 0, 0, 0,
		}})},
	}
	launcher, err := personalsvc.NewSystemdUserLauncher("server", "_service-run")
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := personalsvc.NewSystemdUserAdapter(boundary, "/home/alice/.config", launcher)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := adapter.InspectManager(t.Context())
	loaded, found := manager.Registration()
	if err != nil || manager.Ownership() != personalsvc.ManagerOwnershipOwned || !found || loaded == desired || loaded != stale {
		t.Fatalf("InspectManager() = ownership %s registration %v %t error %v", manager.Ownership(), loaded, found, err)
	}
}

func TestLinuxPrivateFileValidationRejectsSymlinkFIFOInsecureModeAndIdentityChanges(t *testing.T) {
	private := linuxPrivateFileIdentity{device: 1, inode: 2, mode: 0o600, uid: 1000}
	if !validLinuxPrivateFile(private, private, 1000) {
		t.Fatal("valid private file was rejected")
	}
	for _, test := range []struct {
		name   string
		before linuxPrivateFileIdentity
		after  linuxPrivateFileIdentity
	}{
		{name: "symlink", before: linuxPrivateFileIdentity{device: 1, inode: 2, mode: fs.ModeSymlink | 0o600, uid: 1000}, after: private},
		{name: "fifo", before: linuxPrivateFileIdentity{device: 1, inode: 2, mode: fs.ModeNamedPipe | 0o600, uid: 1000}, after: private},
		{name: "group readable", before: linuxPrivateFileIdentity{device: 1, inode: 2, mode: 0o640, uid: 1000}, after: private},
		{name: "foreign owner", before: linuxPrivateFileIdentity{device: 1, inode: 2, mode: 0o600, uid: 1001}, after: private},
		{name: "identity changed", before: private, after: linuxPrivateFileIdentity{device: 1, inode: 3, mode: 0o600, uid: 1000}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if validLinuxPrivateFile(test.before, test.after, 1000) {
				t.Fatal("unsafe private file was accepted")
			}
		})
	}
}

func TestLinuxSystemdBoundaryValidatesEnvironmentAndSerializesOperations(t *testing.T) {
	boundary, _, files := newTestLinuxSystemdBoundary(t)
	if err := boundary.VerifyEnvironmentFile(t.Context(), "/home/alice/.config/powercontext/server.env"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(files.validated, []string{"/home/alice/.config/powercontext/server.env"}) {
		t.Fatalf("validated files = %q", files.validated)
	}
	files.validateErr = errors.New("fifo at /home/alice/.config/powercontext/server.env token=secret")
	err := boundary.VerifyEnvironmentFile(t.Context(), "/home/alice/.config/powercontext/server.env")
	if err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "/home") {
		t.Fatalf("environment refusal = %v", err)
	}

	started := false
	err = boundary.OperationBoundary().Run(t.Context(), func(context.Context) error {
		started = true
		return boundary.OperationBoundary().Run(t.Context(), func(context.Context) error { return nil })
	})
	if !started || err == nil {
		t.Fatalf("nested lock error = %v, started = %t", err, started)
	}
}

type linuxSystemdCommandResult struct {
	exitCode int
	stdout   []byte
	err      error
}

type testLinuxSystemdRunner struct {
	responses []linuxSystemdCommandResult
	calls     [][]string
}

func (r *testLinuxSystemdRunner) Run(_ context.Context, program string, arguments ...string) (linuxSystemdProcessResult, error) {
	r.calls = append(r.calls, append([]string{program}, arguments...))
	if len(r.responses) == 0 {
		return linuxSystemdProcessResult{}, errors.New("unexpected process")
	}
	result := r.responses[0]
	r.responses = r.responses[1:]
	return linuxSystemdProcessResult{exitCode: result.exitCode, stdout: bytes.Clone(result.stdout)}, result.err
}

func (r *testLinuxSystemdRunner) arguments() [][]string {
	return slices.Clone(r.calls)
}

type testLinuxPrivateFiles struct {
	validated   []string
	validateErr error
}

func (f *testLinuxPrivateFiles) Read(context.Context, string) ([]byte, bool, error) {
	return nil, false, nil
}
func (f *testLinuxPrivateFiles) Write(context.Context, string, []byte) error { return nil }
func (f *testLinuxPrivateFiles) Remove(context.Context, string) error        { return nil }
func (f *testLinuxPrivateFiles) Validate(_ context.Context, name string) error {
	f.validated = append(f.validated, name)
	return f.validateErr
}

type testLinuxOperationLock struct{ held bool }

func (l *testLinuxOperationLock) WithLock(ctx context.Context, operation func(context.Context) error) error {
	if l.held {
		return errors.New("operation lock held")
	}
	l.held = true
	defer func() { l.held = false }()
	return operation(ctx)
}

func newTestLinuxSystemdBoundary(t *testing.T) (*linuxSystemdBoundary, *testLinuxSystemdRunner, *testLinuxPrivateFiles) {
	t.Helper()
	roots, err := newLinuxPersonalServiceRoots("/home/alice")
	if err != nil {
		t.Fatal(err)
	}
	runner := &testLinuxSystemdRunner{}
	files := &testLinuxPrivateFiles{}
	boundary, err := newLinuxSystemdBoundary(roots, runner, files, &testLinuxOperationLock{})
	if err != nil {
		t.Fatal(err)
	}
	return boundary, runner, files
}

func unitPropertiesJSON(t *testing.T, loadState, fragmentPath string, dropIns []string) []byte {
	t.Helper()
	return mustBusctlJSON(t, map[string]any{
		"type": "a{sv}",
		"data": map[string]any{
			"LoadState":    map[string]any{"type": "s", "data": loadState},
			"FragmentPath": map[string]any{"type": "s", "data": fragmentPath},
			"DropInPaths":  map[string]any{"type": "as", "data": dropIns},
		},
	})
}

func servicePropertiesJSON(t *testing.T, environment []string, execStart []any) []byte {
	t.Helper()
	return mustBusctlJSON(t, map[string]any{
		"type": "a{sv}",
		"data": map[string]any{
			"Environment": map[string]any{"type": "as", "data": environment},
			"ExecStart":   map[string]any{"type": "a(sasbttttuii)", "data": execStart},
		},
	})
}

func mustBusctlJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func newTestPersonalServiceRegistration(t *testing.T, packageVersion string) personalsvc.Registration {
	t.Helper()
	definition, err := personalsvc.NewDefinition(personalsvc.DefinitionInput{
		Ownership:         personalsvc.OwnershipMarker,
		DefinitionVersion: personalsvc.DefinitionVersion,
		PackageVersion:    packageVersion,
		Binary:            "/opt/powercontext/powercontext",
		Endpoint:          "http://127.0.0.1:8123",
		DataDir:           "/home/alice/.local/share/powercontext",
		EnvFile:           "/home/alice/.config/powercontext/server.env",
	})
	if err != nil {
		t.Fatal(err)
	}
	registration, err := personalsvc.NewRegistration(definition)
	if err != nil {
		t.Fatal(err)
	}
	return registration
}
