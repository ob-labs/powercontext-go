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
	json "encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/ob-labs/powercontext-go/internal/personalsvc"
	"github.com/ob-labs/powercontext-go/server"
)

const (
	lifecycleBinary  = "/opt/powercontext/powercontext"
	lifecycleEnvFile = "/home/alice/.config/powercontext/server.env"
	lifecycleDataDir = "/home/alice/.local/share/powercontext-service"
	lifecycleURL     = "http://127.0.0.1:8123"
)

func TestServerPersonalServiceInstallAndLauncherUseOnlyVerifiedIdentity(t *testing.T) {
	registration := lifecycleRegistration(t)
	files := &lifecyclePrivateFiles{content: map[string][]byte{
		lifecycleEnvFile: []byte("OPENAI_API_KEY=from-file\nPOWERCONTEXT_SERVER_HTTP_PORT=8123\n"),
	}}
	runner := &lifecycleSystemdRunner{files: files, registration: registration}
	boundary, err := newLinuxSystemdBoundary(
		linuxPersonalServiceRoots{configRoot: "/home/alice/.config", stateRoot: "/home/alice/.local/state/powercontext"},
		runner,
		files,
		&testLinuxOperationLock{},
	)
	if err != nil {
		t.Fatal(err)
	}
	boundary.probeClient = &lifecycleHealthClient{}

	originalBoundaryFactory := personalServiceBoundaryFactory
	originalExecutable := personalServiceExecutable
	personalServiceBoundaryFactory = func() (*linuxSystemdBoundary, error) { return boundary, nil }
	personalServiceExecutable = func() (string, error) { return lifecycleBinary, nil }
	t.Cleanup(func() {
		personalServiceBoundaryFactory = originalBoundaryFactory
		personalServiceExecutable = originalExecutable
	})

	t.Setenv("OPENAI_API_KEY", "inherited-secret")
	t.Setenv("POWERCONTEXT_SERVER_HTTP_PORT", "9123")
	var runs int
	serverRun := func(_ context.Context, _ *commandState, config server.ProcessConfig) error {
		runs++
		if config.HTTP.Host != "127.0.0.1" || config.HTTP.Port != 8123 {
			t.Fatalf("service HTTP config = %#v", config.HTTP)
		}
		if config.Database.Kind != "sqlite" || config.Database.SQLite.URL != "sqlite+aiosqlite:////home/alice/.local/share/powercontext-service/powercontext.db" {
			t.Fatalf("service database config = %#v", config.Database)
		}
		if got := environmentValue("OPENAI_API_KEY"); got != "from-file" {
			t.Fatalf("OPENAI_API_KEY = %q, want environment file value", got)
		}
		if got := environmentValue("LEAKED_PROVIDER_SECRET"); got != "" {
			t.Fatalf("LEAKED_PROVIDER_SECRET = %q, want cleared", got)
		}
		return nil
	}

	command := newCommandWithDependencies(VersionInfo{Version: "1.2.3"}, io.Discard, io.Discard, nil, serverRun)
	command.SetArgs([]string{"server", "install", "--env-file", lifecycleEnvFile, "--data-dir", lifecycleDataDir})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatalf("install = %v", err)
	}
	unit, found := files.content[boundary.unitPath()]
	if !found || !bytes.Contains(unit, []byte("Type=exec\n")) || bytes.Contains(unit, []byte("from-file")) || bytes.Contains(unit, []byte("inherited-secret")) {
		t.Fatalf("managed unit = %q", unit)
	}
	if !slices.ContainsFunc(runner.calls, func(arguments []string) bool {
		return slices.Equal(arguments, []string{"systemctl", "--user", "start", "powercontext.service"})
	}) {
		t.Fatalf("install did not start the owned user unit: %q", runner.calls)
	}
	for _, arguments := range runner.calls {
		if slices.Contains(arguments, "--system") || slices.Contains(arguments, "--global") || slices.Contains(arguments, "enable-linger") {
			t.Fatalf("install issued a forbidden manager command: %q", arguments)
		}
	}

	t.Setenv("LEAKED_PROVIDER_SECRET", "stale-manager-value")
	command = newCommandWithDependencies(VersionInfo{Version: "1.2.3"}, io.Discard, io.Discard, nil, serverRun)
	command.SetArgs([]string{"server", "_service-run", "--env-file", lifecycleEnvFile, "--endpoint", lifecycleURL, "--data-dir", lifecycleDataDir})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatalf("service-run = %v", err)
	}
	if runs != 1 {
		t.Fatalf("foreground runner calls = %d, want 1", runs)
	}
}

func TestServerPersonalServiceLauncherRejectsAnUnregisteredIdentityBeforeEnvironmentLoad(t *testing.T) {
	registration := lifecycleRegistration(t)
	files := &lifecyclePrivateFiles{content: map[string][]byte{
		lifecycleEnvFile: []byte("OPENAI_API_KEY=from-file\n"),
	}}
	runner := &lifecycleSystemdRunner{files: files, registration: registration}
	boundary, err := newLinuxSystemdBoundary(
		linuxPersonalServiceRoots{configRoot: "/home/alice/.config", stateRoot: "/home/alice/.local/state/powercontext"},
		runner,
		files,
		&testLinuxOperationLock{},
	)
	if err != nil {
		t.Fatal(err)
	}
	launcher, err := personalsvc.NewSystemdUserLauncher("server", "_service-run")
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := personalsvc.NewSystemdUserAdapter(boundary, "/home/alice/.config", launcher)
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Write(t.Context(), registration); err != nil {
		t.Fatal(err)
	}

	originalBoundaryFactory := personalServiceBoundaryFactory
	originalExecutable := personalServiceExecutable
	personalServiceBoundaryFactory = func() (*linuxSystemdBoundary, error) { return boundary, nil }
	personalServiceExecutable = func() (string, error) { return lifecycleBinary, nil }
	t.Cleanup(func() {
		personalServiceBoundaryFactory = originalBoundaryFactory
		personalServiceExecutable = originalExecutable
	})

	command := newCommandWithDependencies(VersionInfo{Version: "1.2.3"}, io.Discard, io.Discard, nil,
		func(context.Context, *commandState, server.ProcessConfig) error {
			t.Fatal("unregistered launcher reached foreground server")
			return nil
		},
	)
	command.SetArgs([]string{"server", "_service-run", "--env-file", lifecycleEnvFile, "--endpoint", "http://127.0.0.1:8124", "--data-dir", lifecycleDataDir})
	if err := command.ExecuteContext(t.Context()); err == nil || strings.Contains(err.Error(), lifecycleEnvFile) || strings.Contains(err.Error(), "8124") {
		t.Fatalf("service-run refusal = %v", err)
	}
	if files.reads[lifecycleEnvFile] != 0 {
		t.Fatalf("unregistered launcher read the environment file %d times", files.reads[lifecycleEnvFile])
	}
}

func TestPersonalServiceRegistrationRequiresAbsoluteEnvironmentAndDataPaths(t *testing.T) {
	originalExecutable := personalServiceExecutable
	personalServiceExecutable = func() (string, error) { return lifecycleBinary, nil }
	t.Cleanup(func() { personalServiceExecutable = originalExecutable })

	for _, test := range []struct {
		name    string
		envFile string
		dataDir string
	}{
		{name: "relative environment", envFile: "server.env", dataDir: lifecycleDataDir},
		{name: "relative data directory", envFile: lifecycleEnvFile, dataDir: "powercontext-data"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := newPersonalServiceRegistrationFromIdentity(
				&commandState{version: VersionInfo{Version: "1.2.3"}}, test.envFile, lifecycleURL, test.dataDir,
			); err == nil {
				t.Fatal("relative personal-service identity was accepted")
			}
		})
	}
}

func lifecycleRegistration(t *testing.T) personalsvc.Registration {
	t.Helper()
	definition, err := personalsvc.NewDefinition(personalsvc.DefinitionInput{
		Ownership:         personalsvc.OwnershipMarker,
		DefinitionVersion: personalsvc.DefinitionVersion,
		PackageVersion:    "1.2.3",
		Binary:            lifecycleBinary,
		Endpoint:          lifecycleURL,
		DataDir:           lifecycleDataDir,
		EnvFile:           lifecycleEnvFile,
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

func environmentValue(name string) string {
	value, _ := os.LookupEnv(name)
	return value
}

type lifecyclePrivateFiles struct {
	content map[string][]byte
	reads   map[string]int
}

func (f *lifecyclePrivateFiles) Read(_ context.Context, name string) ([]byte, bool, error) {
	if f.reads == nil {
		f.reads = make(map[string]int)
	}
	f.reads[name]++
	value, found := f.content[name]
	return bytes.Clone(value), found, nil
}

func (f *lifecyclePrivateFiles) Write(_ context.Context, name string, value []byte) error {
	if f.content == nil {
		f.content = make(map[string][]byte)
	}
	f.content[name] = bytes.Clone(value)
	return nil
}

func (f *lifecyclePrivateFiles) Remove(_ context.Context, name string) error {
	delete(f.content, name)
	return nil
}

func (*lifecyclePrivateFiles) Validate(context.Context, string) error { return nil }

type lifecycleSystemdRunner struct {
	files        *lifecyclePrivateFiles
	registration personalsvc.Registration
	calls        [][]string
}

func (r *lifecycleSystemdRunner) Run(_ context.Context, program string, arguments ...string) (linuxSystemdProcessResult, error) {
	call := append([]string{program}, arguments...)
	r.calls = append(r.calls, call)
	switch {
	case slices.Equal(call, []string{"systemctl", "--user", "show-environment"}):
		return linuxSystemdProcessResult{}, nil
	case slices.Equal(call, []string{"busctl", "--user", "--json=short", "call", personalServiceManagerName, personalServiceManagerPath, personalServiceManagerName + ".Manager", "LoadUnit", "s", personalServiceUnitName}):
		if _, found := r.files.content["/home/alice/.config/systemd/user/powercontext.service"]; !found {
			return linuxSystemdProcessResult{exitCode: 1}, nil
		}
		return linuxSystemdProcessResult{stdout: []byte(`{"type":"o","data":"/org/freedesktop/systemd1/unit/powercontext_2eservice"}`)}, nil
	case slices.Equal(call, []string{"busctl", "--user", "--json=short", "call", personalServiceManagerName, personalServiceUnitObjectPath, "org.freedesktop.DBus.Properties", "GetAll", "s", personalServiceUnitInterface}):
		value, err := json.Marshal(map[string]any{
			"type": "a{sv}",
			"data": map[string]any{
				"LoadState":    map[string]any{"type": "s", "data": "loaded"},
				"FragmentPath": map[string]any{"type": "s", "data": "/home/alice/.config/systemd/user/powercontext.service"},
				"DropInPaths":  map[string]any{"type": "as", "data": []string{}},
			},
		})
		return linuxSystemdProcessResult{stdout: value}, err
	case slices.Equal(call, []string{"busctl", "--user", "--json=short", "call", personalServiceManagerName, personalServiceUnitObjectPath, "org.freedesktop.DBus.Properties", "GetAll", "s", personalServiceServiceInterface}):
		metadata, err := r.registration.Encode()
		if err != nil {
			return linuxSystemdProcessResult{}, err
		}
		value, marshalErr := json.Marshal(map[string]any{
			"type": "a{sv}",
			"data": map[string]any{
				"Environment": map[string]any{"type": "as", "data": []string{
					"POWERCONTEXT_SERVICE_OWNED=true", "POWERCONTEXT_SERVICE_METADATA=" + metadata,
				}},
				"ExecStart": map[string]any{"type": "a(sasbttttuii)", "data": []any{[]any{
					lifecycleBinary, []any{lifecycleBinary, "server", "_service-run", "--env-file", lifecycleEnvFile, "--endpoint", lifecycleURL, "--data-dir", lifecycleDataDir},
					false, 0, 0, 0, 0, 0, 0, 0,
				}}},
			},
		})
		return linuxSystemdProcessResult{stdout: value}, marshalErr
	case slices.Equal(call, []string{"systemctl", "--user", "show", "--property=ActiveState", "--value", personalServiceUnitName}):
		return linuxSystemdProcessResult{stdout: []byte("active\n")}, nil
	default:
		return linuxSystemdProcessResult{}, nil
	}
}

type lifecycleHealthClient struct{ calls int }

func (c *lifecycleHealthClient) Do(request *http.Request) (*http.Response, error) {
	if request.URL.String() != lifecycleURL+"/health/live" {
		return nil, errors.New("unexpected health URL")
	}
	c.calls++
	if c.calls == 1 {
		return nil, errors.New("listener is absent")
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{"status":"ok"}`)),
		Request:    request,
	}, nil
}
