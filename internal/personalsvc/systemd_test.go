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

package personalsvc_test

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ob-labs/powercontext-go/internal/personalsvc"
)

const (
	systemdUserConfigRoot = "/home/alice/.config"
	systemdUnitPath       = systemdUserConfigRoot + "/systemd/user/powercontext.service"
)

func TestSystemdUserAdapterWritesDeterministicOwnedUnit(t *testing.T) {
	registration := systemdRegistration(t)
	boundary := newSystemdBoundary()
	adapter := newSystemdAdapter(t, boundary)

	if err := adapter.Write(t.Context(), registration); err != nil {
		t.Fatal(err)
	}

	metadata, err := registration.Encode()
	if err != nil {
		t.Fatal(err)
	}
	const want = "# Managed by PowerContext\n" +
		"# X-PowerContext-Metadata: " + "PLACEHOLDER" + "\n" +
		"[Unit]\n" +
		"Description=PowerContext personal Server\n" +
		"After=network.target\n" +
		"StartLimitIntervalSec=60\n" +
		"StartLimitBurst=3\n" +
		"\n" +
		"[Service]\n" +
		"Type=simple\n" +
		"Environment=POWERCONTEXT_SERVICE_OWNED=true\n" +
		"Environment=POWERCONTEXT_SERVICE_METADATA=" + "PLACEHOLDER" + "\n" +
		"ExecStart=\"/opt/powercontext/bin/powercontext\" \"server\" \"_service-run\" \"--env-file\" \"/home/alice/.config/powercontext/server.env\" \"--endpoint\" \"http://127.0.0.1:8123\" \"--data-dir\" \"/home/alice/.local/share/powercontext\"\n" +
		"Restart=on-failure\n" +
		"RestartSec=5s\n" +
		"TimeoutStopSec=30s\n" +
		"\n" +
		"[Install]\n" +
		"WantedBy=default.target\n"
	expected := strings.ReplaceAll(want, "PLACEHOLDER", metadata)
	if string(boundary.content) != expected {
		t.Fatalf("written unit = %q, want %q", boundary.content, expected)
	}
	if boundary.writeCount != 1 || boundary.removeCount != 0 {
		t.Fatalf("mutations = write %d remove %d, want write 1 remove 0", boundary.writeCount, boundary.removeCount)
	}

	artifact, err := adapter.InspectArtifact(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	stored, found := artifact.Registration()
	if artifact.State() != personalsvc.RegistrationInstalled || !found || stored != registration {
		t.Fatalf("artifact = %#v, registration found = %t", artifact, found)
	}
}

func TestSystemdUserAdapterRefusesForeignOrMalformedArtifactsWithoutMutation(t *testing.T) {
	registration := systemdRegistration(t)
	for _, test := range []struct {
		name    string
		content string
	}{
		{name: "foreign", content: "[Service]\nExecStart=/foreign/service\n"},
		{name: "malformed metadata", content: "# Managed by PowerContext\n# X-PowerContext-Metadata: malformed-metadata\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			boundary := newSystemdBoundary()
			boundary.exists = true
			boundary.content = []byte(test.content)
			adapter := newSystemdAdapter(t, boundary)

			artifact, err := adapter.InspectArtifact(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if artifact.State() != personalsvc.RegistrationInvalid {
				t.Fatalf("artifact state = %s, want invalid", artifact.State())
			}
			if err := adapter.Write(t.Context(), registration); err == nil {
				t.Fatal("Write succeeded for an unverified artifact")
			}
			if err := adapter.Remove(t.Context()); err == nil {
				t.Fatal("Remove succeeded for an unverified artifact")
			}
			if err := adapter.Enable(t.Context()); err == nil {
				t.Fatal("Enable succeeded for an unverified artifact")
			}
			if boundary.writeCount != 0 || boundary.removeCount != 0 {
				t.Fatalf("unverified artifact mutated: write %d remove %d", boundary.writeCount, boundary.removeCount)
			}
			if len(boundary.commands) != 0 {
				t.Fatalf("unverified artifact reached the manager: %q", boundary.commandArguments())
			}
		})
	}
}

func TestSystemdUserAdapterRejectsEveryManagerOwnershipMismatchWithoutEnablement(t *testing.T) {
	registration := systemdRegistration(t)
	const executable = "/opt/powercontext/bin/powercontext"
	canonical := systemdOwnedManagerFixture(t, registration, []personalsvc.SystemdUserExecStart{
		personalsvc.NewSystemdUserExecStart(executable, []string{
			executable, "server", "_service-run", "--env-file", "/home/alice/.config/powercontext/server.env",
			"--endpoint", "http://127.0.0.1:8123", "--data-dir", "/home/alice/.local/share/powercontext",
		}, false),
	})
	ownedBoundary := newSystemdBoundary()
	ownedAdapter := newSystemdAdapter(t, ownedBoundary)
	if writeErr := ownedAdapter.Write(t.Context(), registration); writeErr != nil {
		t.Fatal(writeErr)
	}
	ownedBoundary.setManagerUnit(canonical.unit())
	owned, ownedErr := ownedAdapter.InspectManager(t.Context())
	if ownedErr != nil || owned.Ownership() != personalsvc.ManagerOwnershipOwned {
		t.Fatalf("intact manager registration = %s, %v; want owned, nil", owned.Ownership(), ownedErr)
	}

	for _, test := range []struct {
		name   string
		mutate func(systemdManagerFixture) systemdManagerFixture
	}{
		{
			name: "fragment",
			mutate: func(value systemdManagerFixture) systemdManagerFixture {
				value.fragmentPath = "/home/alice/.config/systemd/user/foreign.service"
				return value
			},
		},
		{
			name: "executable",
			mutate: func(value systemdManagerFixture) systemdManagerFixture {
				arguments := value.execStart[0].Arguments()
				arguments[0] = "/foreign/launcher"
				value.execStart = []personalsvc.SystemdUserExecStart{personalsvc.NewSystemdUserExecStart("/foreign/launcher", arguments, false)}
				return value
			},
		},
		{
			name: "arguments",
			mutate: func(value systemdManagerFixture) systemdManagerFixture {
				arguments := value.execStart[0].Arguments()
				arguments[6] = "http://127.0.0.1:9000"
				value.execStart = []personalsvc.SystemdUserExecStart{personalsvc.NewSystemdUserExecStart(executable, arguments, false)}
				return value
			},
		},
		{
			name: "marker",
			mutate: func(value systemdManagerFixture) systemdManagerFixture {
				value.environment = strings.Replace(value.environment, "POWERCONTEXT_SERVICE_OWNED", "FOREIGN_SERVICE_OWNED", 1)
				return value
			},
		},
		{
			name: "metadata",
			mutate: func(value systemdManagerFixture) systemdManagerFixture {
				value.environment = strings.Replace(value.environment, "POWERCONTEXT_SERVICE_METADATA=", "POWERCONTEXT_SERVICE_METADATA=malformed-", 1)
				return value
			},
		},
		{
			name: "ignore errors",
			mutate: func(value systemdManagerFixture) systemdManagerFixture {
				command := value.execStart[0]
				value.execStart = []personalsvc.SystemdUserExecStart{personalsvc.NewSystemdUserExecStart(command.Path(), command.Arguments(), true)}
				return value
			},
		},
		{
			name: "drop in",
			mutate: func(value systemdManagerFixture) systemdManagerFixture {
				value.dropInPaths = []string{"/home/alice/.config/systemd/user/powercontext.service.d/99-foreign.conf"}
				return value
			},
		},
		{
			name: "missing drop in paths",
			mutate: func(value systemdManagerFixture) systemdManagerFixture {
				value.dropInPathsPresent = false
				return value
			},
		},
		{
			name: "second command",
			mutate: func(value systemdManagerFixture) systemdManagerFixture {
				value.execStart = append(value.execStart, personalsvc.NewSystemdUserExecStart("/foreign/launcher", []string{"/foreign/launcher"}, false))
				return value
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			boundary := newSystemdBoundary()
			adapter := newSystemdAdapter(t, boundary)
			if writeErr := adapter.Write(t.Context(), registration); writeErr != nil {
				t.Fatal(writeErr)
			}
			boundary.setManagerUnit(test.mutate(canonical).unit())

			manager, inspectErr := adapter.InspectManager(t.Context())
			if inspectErr != nil {
				t.Fatal(inspectErr)
			}
			if manager.Ownership() != personalsvc.ManagerOwnershipForeign {
				t.Fatalf("manager ownership = %s, want foreign", manager.Ownership())
			}
			if enableErr := adapter.Enable(t.Context()); enableErr == nil {
				t.Fatal("Enable succeeded for a foreign manager registration")
			}
			if got := boundary.inspectedUnits(); !slices.Equal(got, []string{"powercontext.service", "powercontext.service"}) {
				t.Fatalf("inspected units = %q, want two fixed-unit inspections", got)
			}
		})
	}
}

func TestSystemdUserAdapterAcceptsManagerArgumentsContainingSpaces(t *testing.T) {
	registration := systemdRegistrationWithBinary(t, "/opt/PowerContext Personal/powercontext")
	launcher, err := personalsvc.NewSystemdUserLauncher("server", "_service-run")
	if err != nil {
		t.Fatal(err)
	}
	boundary := newSystemdBoundary()
	adapter, err := personalsvc.NewSystemdUserAdapter(boundary, systemdUserConfigRoot, launcher)
	if err != nil {
		t.Fatal(err)
	}
	boundary.setManagerUnit(systemdOwnedManagerFixture(t, registration, []personalsvc.SystemdUserExecStart{
		personalsvc.NewSystemdUserExecStart(
			"/opt/PowerContext Personal/powercontext",
			[]string{
				"/opt/PowerContext Personal/powercontext", "server", "_service-run", "--env-file", "/home/alice/.config/powercontext/server.env",
				"--endpoint", "http://127.0.0.1:8123", "--data-dir", "/home/alice/.local/share/powercontext",
			},
			false,
		),
	}).unit())

	manager, inspectErr := adapter.InspectManager(t.Context())
	if inspectErr != nil || manager.Ownership() != personalsvc.ManagerOwnershipOwned {
		t.Fatalf("manager = %s, %v; want owned, nil", manager.Ownership(), inspectErr)
	}
}

func TestSystemdUserAdapterRejectsAmbiguousFlattenedManagerArguments(t *testing.T) {
	registration := systemdRegistrationWithBinary(t, "/opt/PowerContext Personal/powercontext")
	launcher, err := personalsvc.NewSystemdUserLauncher("server", "_service-run")
	if err != nil {
		t.Fatal(err)
	}
	boundary := newSystemdBoundary()
	adapter, err := personalsvc.NewSystemdUserAdapter(boundary, systemdUserConfigRoot, launcher)
	if err != nil {
		t.Fatal(err)
	}
	boundary.setManagerUnit(systemdOwnedManagerFixture(t, registration, []personalsvc.SystemdUserExecStart{
		personalsvc.NewSystemdUserExecStart(
			"/opt/PowerContext Personal/powercontext",
			[]string{
				"/opt/PowerContext Personal/powercontext", "server", "_service-run", "--env-file", "/home/alice/.config/powercontext/server.env",
				"--endpoint", "http://127.0.0.1:8123", "--data-dir", "/home/alice/.local", "share/powercontext",
			},
			false,
		),
	}).unit())

	manager, inspectErr := adapter.InspectManager(t.Context())
	if inspectErr != nil || manager.Ownership() != personalsvc.ManagerOwnershipForeign {
		t.Fatalf("manager = %s, %v; want foreign, nil", manager.Ownership(), inspectErr)
	}
}

func TestSystemdUserAdapterRejectsManagerWithoutDropInPaths(t *testing.T) {
	registration := systemdRegistration(t)
	executable := "/opt/powercontext/bin/powercontext"
	boundary := newSystemdBoundary()
	adapter := newSystemdAdapter(t, boundary)
	managerFixture := systemdOwnedManagerFixture(t, registration, []personalsvc.SystemdUserExecStart{
		personalsvc.NewSystemdUserExecStart(executable, []string{
			executable, "server", "_service-run", "--env-file", "/home/alice/.config/powercontext/server.env",
			"--endpoint", "http://127.0.0.1:8123", "--data-dir", "/home/alice/.local/share/powercontext",
		}, false),
	})
	managerFixture.dropInPathsPresent = false
	boundary.setManagerUnit(managerFixture.unit())

	manager, inspectErr := adapter.InspectManager(t.Context())
	if inspectErr != nil || manager.Ownership() != personalsvc.ManagerOwnershipForeign {
		t.Fatalf("manager = %s, %v; want foreign, nil", manager.Ownership(), inspectErr)
	}
}

func TestSystemdUserAdapterUsesOnlyExactUserManagerCommands(t *testing.T) {
	registration := systemdRegistration(t)
	boundary := newSystemdBoundary()
	adapter := newSystemdAdapter(t, boundary)
	if err := adapter.Write(t.Context(), registration); err != nil {
		t.Fatal(err)
	}

	if support, err := adapter.Support(t.Context()); err != nil || support != personalsvc.SupportSupported {
		t.Fatalf("Support = %s, %v", support, err)
	}
	if err := adapter.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Enable(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Disable(t.Context()); err != nil {
		t.Fatal(err)
	}
	if manager, err := adapter.ManagerState(t.Context()); err != nil || manager != personalsvc.ManagerActive {
		t.Fatalf("ManagerState = %s, %v", manager, err)
	}
	if _, err := adapter.RecoveryLogs(t.Context()); err != nil {
		t.Fatal(err)
	}

	want := [][]string{
		{"systemctl", "--user", "show-environment"},
		{"systemctl", "--user", "daemon-reload"},
		{"systemctl", "--user", "enable", "powercontext.service"},
		{"systemctl", "--user", "reset-failed", "powercontext.service"},
		{"systemctl", "--user", "start", "powercontext.service"},
		{"systemctl", "--user", "stop", "powercontext.service"},
		{"systemctl", "--user", "disable", "powercontext.service"},
		{"systemctl", "--user", "show", "--property=ActiveState", "--value", "powercontext.service"},
		{"journalctl", "--user", "--unit", "powercontext.service"},
	}
	if got := boundary.commandArguments(); !slices.EqualFunc(got, want, slices.Equal) {
		t.Fatalf("commands = %q, want %q", got, want)
	}
	if got := boundary.inspectedUnits(); !slices.Equal(got, []string{
		"powercontext.service", "powercontext.service", "powercontext.service", "powercontext.service",
	}) {
		t.Fatalf("inspected units = %q, want fixed unit before each mutable operation", got)
	}
}

func TestSystemdUserAdapterClassifiesUnavailableUserManagerWithoutLeakingCommandError(t *testing.T) {
	boundary := newSystemdBoundary()
	boundary.setError(systemdCommand("systemctl", "--user", "show-environment"), errors.New("manager unavailable at /run/user/1000 with secret=abc"))
	adapter := newSystemdAdapter(t, boundary)

	support, err := adapter.Support(t.Context())
	if err != nil || support != personalsvc.SupportUnsupported {
		t.Fatalf("Support = %s, %v; want unsupported, nil", support, err)
	}
	boundary.setError(systemdCommand("systemctl", "--user", "daemon-reload"), errors.New("stderr has secret=abc"))
	err = adapter.Reload(t.Context())
	if err == nil {
		t.Fatal("Reload succeeded despite command failure")
	}
	if strings.Contains(err.Error(), "secret=abc") || strings.Contains(err.Error(), "/run/user/1000") {
		t.Fatalf("command error leaked detail: %v", err)
	}
}

func TestSystemdUserAdapterZeroValueFailsClosed(t *testing.T) {
	adapter := &personalsvc.SystemdUserAdapter{}
	registration := systemdRegistration(t)
	for _, test := range []struct {
		name   string
		invoke func() error
	}{
		{
			name: "inspect artifact",
			invoke: func() error {
				_, err := adapter.InspectArtifact(t.Context())
				return err
			},
		},
		{
			name: "inspect manager",
			invoke: func() error {
				_, err := adapter.InspectManager(t.Context())
				return err
			},
		},
		{
			name: "probe",
			invoke: func() error {
				_, err := adapter.Probe(t.Context(), "http://127.0.0.1:8123")
				return err
			},
		},
		{name: "write", invoke: func() error { return adapter.Write(t.Context(), registration) }},
		{name: "reload", invoke: func() error { return adapter.Reload(t.Context()) }},
		{name: "enable", invoke: func() error { return adapter.Enable(t.Context()) }},
		{name: "start", invoke: func() error { return adapter.Start(t.Context()) }},
		{name: "stop", invoke: func() error { return adapter.Stop(t.Context()) }},
		{name: "disable", invoke: func() error { return adapter.Disable(t.Context()) }},
		{name: "remove", invoke: func() error { return adapter.Remove(t.Context()) }},
		{
			name: "manager state",
			invoke: func() error {
				_, err := adapter.ManagerState(t.Context())
				return err
			},
		},
		{
			name: "recovery logs",
			invoke: func() error {
				_, err := adapter.RecoveryLogs(t.Context())
				return err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := test.invoke()
			if err == nil || strings.Contains(err.Error(), "nil") {
				t.Fatalf("zero-value error = %v, want redacted failure", err)
			}
		})
	}

	support, err := adapter.Support(t.Context())
	if err != nil || support != personalsvc.SupportUnsupported {
		t.Fatalf("zero-value Support = %s, %v; want unsupported, nil", support, err)
	}
}

func TestSystemdUserAdapterRejectsNonPrivateUnitPathsWithoutHostAccess(t *testing.T) {
	launcher, err := personalsvc.NewSystemdUserLauncher("server", "_service-run")
	if err != nil {
		t.Fatal(err)
	}
	for _, userConfigRoot := range []string{
		"/etc",
		"/root/.config",
		"/usr/lib",
		"/usr/local/lib",
		"/usr/share",
		"/usr/local/share",
		"/lib",
		"/run",
		"relative",
	} {
		boundary := newSystemdBoundary()
		adapter, adapterErr := personalsvc.NewSystemdUserAdapter(boundary, userConfigRoot, launcher)
		if adapter != nil || adapterErr == nil {
			t.Fatalf("NewSystemdUserAdapter(%q) = %v, %v; want configuration rejection", userConfigRoot, adapter, adapterErr)
		}
		if len(boundary.commands) != 0 || boundary.writeCount != 0 || boundary.removeCount != 0 {
			t.Fatalf("NewSystemdUserAdapter(%q) accessed its boundary", userConfigRoot)
		}
	}
}

func TestSystemdUserLauncherAcceptsOnlyTheFixedServiceRunPrefix(t *testing.T) {
	for _, arguments := range [][]string{
		nil,
		{"server"},
		{"_service-run"},
		{"--endpoint", "http://127.0.0.1:9000"},
		{"--endpoint=http://127.0.0.1:9000"},
		{"--data-dir", "/foreign/data"},
		{"--data-dir=/foreign/data"},
		{"--"},
	} {
		_, err := personalsvc.NewSystemdUserLauncher(arguments...)
		if err == nil {
			t.Fatalf("NewSystemdUserLauncher(%q) accepted a non-fixed prefix", arguments)
		}
	}
}

func newSystemdAdapter(t *testing.T, boundary *systemdBoundary) *personalsvc.SystemdUserAdapter {
	t.Helper()
	launcher, err := personalsvc.NewSystemdUserLauncher("server", "_service-run")
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := personalsvc.NewSystemdUserAdapter(boundary, systemdUserConfigRoot, launcher)
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

func systemdRegistration(t *testing.T) personalsvc.Registration {
	return systemdRegistrationWithBinary(t, "/opt/powercontext/bin/powercontext")
}

func systemdRegistrationWithBinary(t *testing.T, binary string) personalsvc.Registration {
	t.Helper()
	definition, err := personalsvc.NewDefinition(personalsvc.DefinitionInput{
		Ownership:         personalsvc.OwnershipMarker,
		DefinitionVersion: personalsvc.DefinitionVersion,
		PackageVersion:    "0.2.0",
		Binary:            binary,
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

type systemdManagerFixture struct {
	fragmentPath       string
	dropInPathsPresent bool
	dropInPaths        []string
	environment        string
	execStart          []personalsvc.SystemdUserExecStart
}

func systemdOwnedManagerFixture(
	t *testing.T,
	registration personalsvc.Registration,
	execStart []personalsvc.SystemdUserExecStart,
) systemdManagerFixture {
	t.Helper()
	metadata, err := registration.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return systemdManagerFixture{
		fragmentPath:       systemdUnitPath,
		dropInPathsPresent: true,
		environment:        "POWERCONTEXT_SERVICE_OWNED=true POWERCONTEXT_SERVICE_METADATA=" + metadata,
		execStart:          execStart,
	}
}

func (f systemdManagerFixture) unit() personalsvc.SystemdUserManagerUnit {
	return personalsvc.NewSystemdUserManagerUnit(
		"loaded",
		f.fragmentPath,
		f.dropInPathsPresent,
		f.dropInPaths,
		f.environment,
		f.execStart,
	)
}

type systemdBoundary struct {
	content     []byte
	exists      bool
	readErr     error
	writeErr    error
	removeErr   error
	writeCount  int
	removeCount int
	commands    []personalsvc.SystemdUserCommand
	results     map[string]systemdCommandResponse
	managerUnit personalsvc.SystemdUserManagerUnit
	managerErr  error
	inspections []string
}

type systemdCommandResponse struct {
	result personalsvc.SystemdUserCommandResult
	err    error
}

func newSystemdBoundary() *systemdBoundary {
	return &systemdBoundary{results: make(map[string]systemdCommandResponse)}
}

func (b *systemdBoundary) ReadFile(context.Context, string) ([]byte, bool, error) {
	if b.readErr != nil {
		return nil, false, b.readErr
	}
	return bytes.Clone(b.content), b.exists, nil
}

func (b *systemdBoundary) WriteFile(_ context.Context, _ string, content []byte) error {
	if b.writeErr != nil {
		return b.writeErr
	}
	b.content = bytes.Clone(content)
	b.exists = true
	b.writeCount++
	return nil
}

func (b *systemdBoundary) RemoveFile(context.Context, string) error {
	if b.removeErr != nil {
		return b.removeErr
	}
	b.content = nil
	b.exists = false
	b.removeCount++
	return nil
}

func (b *systemdBoundary) InspectUnit(_ context.Context, unit string) (personalsvc.SystemdUserManagerUnit, error) {
	b.inspections = append(b.inspections, unit)
	if b.managerErr != nil {
		return personalsvc.SystemdUserManagerUnit{}, b.managerErr
	}
	if b.managerUnit.LoadState() == "" {
		return personalsvc.NewSystemdUserManagerUnit("not-found", "", false, nil, "", nil), nil
	}
	return b.managerUnit, nil
}

func (b *systemdBoundary) Run(_ context.Context, command personalsvc.SystemdUserCommand) (personalsvc.SystemdUserCommandResult, error) {
	b.commands = append(b.commands, command)
	if response, found := b.results[systemdCommand(command.Program(), command.Arguments()...)]; found {
		return response.result, response.err
	}
	if command.Program() == "systemctl" && slices.Equal(command.Arguments(), []string{"--user", "show", "--property=ActiveState", "--value", "powercontext.service"}) {
		return personalsvc.NewSystemdUserCommandResult(0, []byte("active\n")), nil
	}
	return personalsvc.NewSystemdUserCommandResult(0, nil), nil
}

func (b *systemdBoundary) Probe(context.Context, string) (personalsvc.ProbeState, error) {
	return personalsvc.ProbeUnreachable, nil
}

func (b *systemdBoundary) setError(command string, err error) {
	b.results[command] = systemdCommandResponse{err: err}
}

func (b *systemdBoundary) setManagerUnit(unit personalsvc.SystemdUserManagerUnit) {
	b.managerUnit = unit
}

func (b *systemdBoundary) inspectedUnits() []string {
	return slices.Clone(b.inspections)
}

func (b *systemdBoundary) commandArguments() [][]string {
	values := make([][]string, 0, len(b.commands))
	for _, command := range b.commands {
		values = append(values, append([]string{command.Program()}, command.Arguments()...))
	}
	return values
}

func systemdCommand(program string, arguments ...string) string {
	return program + "\x00" + strings.Join(arguments, "\x00")
}
