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

package personalsvc

import (
	"bytes"
	"context"
	"fmt"
	"path"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	systemdUserUnitName      = "powercontext.service"
	systemdUserUnitDirectory = "systemd/user"
	systemdUnitRelativePath  = systemdUserUnitDirectory + "/" + systemdUserUnitName
)

// SystemdUserCommand is one immutable process invocation requested by the
// systemd-user adapter. Its fields are intentionally private so the adapter,
// rather than a caller, selects every executable and argument.
type SystemdUserCommand struct {
	program   string
	arguments []string
}

// Program returns the exact executable selected by the adapter.
func (c SystemdUserCommand) Program() string { return c.program }

// Arguments returns an independent copy of the exact argument vector.
func (c SystemdUserCommand) Arguments() []string { return append([]string(nil), c.arguments...) }

// SystemdUserCommandResult carries a process status and stdout. The boundary
// never supplies stderr to this package, which prevents native error details
// from reaching lifecycle errors.
type SystemdUserCommandResult struct {
	exitCode int
	stdout   []byte
}

// NewSystemdUserCommandResult constructs an immutable result for an explicit
// systemd-user boundary invocation.
func NewSystemdUserCommandResult(exitCode int, stdout []byte) SystemdUserCommandResult {
	return SystemdUserCommandResult{exitCode: exitCode, stdout: bytes.Clone(stdout)}
}

// ExitCode returns the captured process exit code.
func (r SystemdUserCommandResult) ExitCode() int { return r.exitCode }

// Stdout returns an independent copy of the captured process stdout.
func (r SystemdUserCommandResult) Stdout() []byte { return bytes.Clone(r.stdout) }

// SystemdUserExecStart is one structured ExecStart command reported by the
// composition-owned manager boundary.
type SystemdUserExecStart struct {
	path         string
	arguments    []string
	ignoreErrors bool
}

// NewSystemdUserExecStart copies one manager-reported ExecStart command.
func NewSystemdUserExecStart(path string, arguments []string, ignoreErrors bool) SystemdUserExecStart {
	return SystemdUserExecStart{
		path:         strings.Clone(path),
		arguments:    slices.Clone(arguments),
		ignoreErrors: ignoreErrors,
	}
}

// Path returns the executable path reported by the manager.
func (c SystemdUserExecStart) Path() string { return c.path }

// Arguments returns an independent copy of the manager-reported argv vector.
func (c SystemdUserExecStart) Arguments() []string { return slices.Clone(c.arguments) }

// IgnoreErrors reports whether systemd will suppress a command failure.
func (c SystemdUserExecStart) IgnoreErrors() bool { return c.ignoreErrors }

// SystemdUserManagerUnit is the structured state required to verify one
// loaded systemd user unit without parsing lossy command-line output.
type SystemdUserManagerUnit struct {
	loadState          string
	fragmentPath       string
	dropInPathsPresent bool
	dropInPaths        []string
	environment        string
	execStart          []SystemdUserExecStart
}

// NewSystemdUserManagerUnit copies the manager state returned by the native
// composition boundary.
func NewSystemdUserManagerUnit(
	loadState string,
	fragmentPath string,
	dropInPathsPresent bool,
	dropInPaths []string,
	environment string,
	execStart []SystemdUserExecStart,
) SystemdUserManagerUnit {
	return SystemdUserManagerUnit{
		loadState:          strings.Clone(loadState),
		fragmentPath:       strings.Clone(fragmentPath),
		dropInPathsPresent: dropInPathsPresent,
		dropInPaths:        slices.Clone(dropInPaths),
		environment:        strings.Clone(environment),
		execStart:          cloneSystemdUserExecStart(execStart),
	}
}

// LoadState returns the manager-reported load state.
func (u SystemdUserManagerUnit) LoadState() string { return u.loadState }

// FragmentPath returns the manager-reported main unit file path.
func (u SystemdUserManagerUnit) FragmentPath() string { return u.fragmentPath }

// HasDropInPaths reports whether the native boundary read the DropInPaths
// manager property.
func (u SystemdUserManagerUnit) HasDropInPaths() bool { return u.dropInPathsPresent }

// DropInPaths returns independent copies of the manager-reported drop-ins.
func (u SystemdUserManagerUnit) DropInPaths() []string { return slices.Clone(u.dropInPaths) }

// Environment returns the manager-reported environment assignments.
func (u SystemdUserManagerUnit) Environment() string { return u.environment }

// ExecStart returns independent copies of the manager-reported commands.
func (u SystemdUserManagerUnit) ExecStart() []SystemdUserExecStart {
	return cloneSystemdUserExecStart(u.execStart)
}

func cloneSystemdUserExecStart(commands []SystemdUserExecStart) []SystemdUserExecStart {
	cloned := make([]SystemdUserExecStart, 0, len(commands))
	for _, command := range commands {
		cloned = append(cloned, NewSystemdUserExecStart(command.path, command.arguments, command.ignoreErrors))
	}
	return cloned
}

// SystemdUserBoundary owns every platform interaction required by
// SystemdUserAdapter. It is deliberately supplied by composition: constructing
// an adapter neither reads host state nor starts a process.
type SystemdUserBoundary interface {
	ReadFile(context.Context, string) ([]byte, bool, error)
	WriteFile(context.Context, string, []byte) error
	RemoveFile(context.Context, string) error
	InspectUnit(context.Context, string) (SystemdUserManagerUnit, error)
	Run(context.Context, SystemdUserCommand) (SystemdUserCommandResult, error)
	Probe(context.Context, string) (ProbeState, error)
}

// SystemdUserLauncher is the fixed command prefix for the manager-owned
// personal-service launcher. The executable and all mutable values belong to
// the registration, so the unit can be compared directly with its definition.
type SystemdUserLauncher struct {
	arguments []string
}

// NewSystemdUserLauncher accepts only the release binary's hidden service
// command. It does not inspect the executable or filesystem.
func NewSystemdUserLauncher(arguments ...string) (SystemdUserLauncher, error) {
	if !slices.Equal(arguments, []string{"server", "_service-run"}) {
		return SystemdUserLauncher{}, newSystemdAdapterError("configuration")
	}
	return SystemdUserLauncher{arguments: slices.Clone(arguments)}, nil
}

func (l SystemdUserLauncher) command(registration Registration) ([]string, error) {
	if !slices.Equal(l.arguments, []string{"server", "_service-run"}) {
		return nil, newSystemdAdapterError("configuration")
	}
	if err := registration.Definition().Validate(); err != nil {
		return nil, newSystemdAdapterError("configuration")
	}
	arguments := make([]string, 0, 1+len(l.arguments)+6)
	arguments = append(arguments, registration.Definition().Binary())
	arguments = append(arguments, l.arguments...)
	arguments = append(arguments,
		"--env-file", registration.Definition().EnvFile(),
		"--endpoint", registration.Definition().Endpoint(),
		"--data-dir", registration.Definition().DataDir(),
	)
	return arguments, nil
}

// SystemdUserAdapter is the explicit Linux systemd --user contract for one
// PowerContext registration. It must be constructed with
// NewSystemdUserAdapter. It does not discover a manager, environment, unit
// directory, launcher, or host filesystem by itself.
type SystemdUserAdapter struct {
	boundary SystemdUserBoundary
	unitPath string
	launcher SystemdUserLauncher
}

var _ Adapter = (*SystemdUserAdapter)(nil)

// NewSystemdUserAdapter constructs a per-user systemd adapter from explicit
// platform boundaries. The composition-owned user configuration root derives
// the fixed unit path; known system-wide and root paths are rejected before
// the boundary is called.
func NewSystemdUserAdapter(
	boundary SystemdUserBoundary,
	userConfigRoot string,
	launcher SystemdUserLauncher,
) (*SystemdUserAdapter, error) {
	if boundary == nil || !validSystemdUserConfigRoot(userConfigRoot) {
		return nil, newSystemdAdapterError("configuration")
	}
	if !slices.Equal(launcher.arguments, []string{"server", "_service-run"}) {
		return nil, newSystemdAdapterError("configuration")
	}
	return &SystemdUserAdapter{
		boundary: boundary,
		unitPath: path.Join(userConfigRoot, systemdUnitRelativePath),
		launcher: launcher,
	}, nil
}

// Support classifies a missing or unavailable user manager as unsupported. A
// platform diagnostic is intentionally not returned because it can disclose
// manager state, paths, or launch environment.
func (a *SystemdUserAdapter) Support(ctx context.Context) (Support, error) {
	result, err := a.run(ctx, "systemctl", "--user", "show-environment")
	if err != nil || result.ExitCode() != 0 {
		return SupportUnsupported, nil
	}
	return SupportSupported, nil
}

// InspectArtifact verifies that the stored unit is byte-for-byte the unit
// rendered from its encoded PowerContext registration.
func (a *SystemdUserAdapter) InspectArtifact(ctx context.Context) (Artifact, error) {
	if !a.available() {
		return UnknownArtifact(), newSystemdAdapterError("inspect artifact")
	}
	content, exists, err := a.boundary.ReadFile(ctx, a.unitPath)
	if err != nil {
		return UnknownArtifact(), newSystemdAdapterError("inspect artifact")
	}
	if !exists {
		return NoArtifact(), nil
	}
	registration, found := a.registrationForUnit(content)
	if !found {
		return InvalidArtifact(), nil
	}
	return InstalledArtifact(registration), nil
}

// InspectManager accepts a loaded systemd unit only when its exact fragment,
// managed metadata, and launcher argument vector all match one registration.
func (a *SystemdUserAdapter) InspectManager(ctx context.Context) (ManagerRegistration, error) {
	if !a.available() {
		return UnknownManager(), newSystemdAdapterError("inspect manager")
	}
	unit, err := a.boundary.InspectUnit(ctx, systemdUserUnitName)
	if err != nil {
		return UnknownManager(), newSystemdAdapterError("inspect manager")
	}
	if unit.LoadState() == "not-found" {
		return NotLoadedManager(), nil
	}
	if unit.LoadState() != "loaded" || !unit.HasDropInPaths() || len(unit.DropInPaths()) != 0 {
		return ForeignManager(), nil
	}
	metadata, owned, found := systemdOwnership(unit.Environment())
	if !found || !owned {
		return ForeignManager(), nil
	}
	registration, err := DecodeRegistration(metadata)
	if err != nil {
		return ForeignManager(), nil
	}
	arguments, err := a.launcher.command(registration)
	if err != nil || unit.FragmentPath() != a.unitPath || !matchesSystemdExecStart(unit.ExecStart(), arguments) {
		return ForeignManager(), nil
	}
	return OwnedManager(registration), nil
}

// Probe delegates endpoint liveness through the composition-owned boundary.
func (a *SystemdUserAdapter) Probe(ctx context.Context, endpoint string) (ProbeState, error) {
	if !a.available() {
		return ProbeUnreachable, newSystemdAdapterError("probe")
	}
	state, err := a.boundary.Probe(ctx, endpoint)
	if err != nil {
		return ProbeUnreachable, newSystemdAdapterError("probe")
	}
	return state, nil
}

// Write records a newly rendered unit only when any existing unit has already
// been verified as PowerContext-owned.
func (a *SystemdUserAdapter) Write(ctx context.Context, registration Registration) error {
	artifact, err := a.InspectArtifact(ctx)
	if err != nil {
		return err
	}
	if artifact.State() == RegistrationInvalid || artifact.State() == RegistrationUnknown {
		return newSystemdAdapterError("write")
	}
	content, err := a.render(registration)
	if err != nil {
		return err
	}
	if err := a.boundary.WriteFile(ctx, a.unitPath, content); err != nil {
		return newSystemdAdapterError("write")
	}
	return nil
}

// Reload requests only the systemd --user daemon reload operation.
func (a *SystemdUserAdapter) Reload(ctx context.Context) error {
	return a.runRequired(ctx, "reload", "systemctl", "--user", "daemon-reload")
}

// Enable enables the fixed user unit only after manager ownership is verified.
func (a *SystemdUserAdapter) Enable(ctx context.Context) error {
	if err := a.requireMutableManager(ctx); err != nil {
		return err
	}
	return a.runRequired(ctx, "enable", "systemctl", "--user", "enable", systemdUserUnitName)
}

// Start clears only this unit's failed state and starts only this user unit
// after manager ownership is verified.
func (a *SystemdUserAdapter) Start(ctx context.Context) error {
	if err := a.requireMutableManager(ctx); err != nil {
		return err
	}
	if err := a.runRequired(ctx, "start", "systemctl", "--user", "reset-failed", systemdUserUnitName); err != nil {
		return err
	}
	return a.runRequired(ctx, "start", "systemctl", "--user", "start", systemdUserUnitName)
}

// Stop stops only the fixed user unit after manager ownership is verified.
func (a *SystemdUserAdapter) Stop(ctx context.Context) error {
	if err := a.requireMutableManager(ctx); err != nil {
		return err
	}
	return a.runRequired(ctx, "stop", "systemctl", "--user", "stop", systemdUserUnitName)
}

// Disable disables only the fixed user unit after manager ownership is verified.
func (a *SystemdUserAdapter) Disable(ctx context.Context) error {
	if err := a.requireMutableManager(ctx); err != nil {
		return err
	}
	return a.runRequired(ctx, "disable", "systemctl", "--user", "disable", systemdUserUnitName)
}

// Remove removes an artifact only after independently verifying that it is
// PowerContext-owned. An absent artifact is already removed.
func (a *SystemdUserAdapter) Remove(ctx context.Context) error {
	artifact, err := a.InspectArtifact(ctx)
	if err != nil {
		return err
	}
	if artifact.State() == RegistrationNotInstalled {
		return nil
	}
	if artifact.State() != RegistrationInstalled {
		return newSystemdAdapterError("remove")
	}
	if err := a.boundary.RemoveFile(ctx, a.unitPath); err != nil {
		return newSystemdAdapterError("remove")
	}
	return nil
}

// ManagerState returns a closed manager state without disclosing native
// command output.
func (a *SystemdUserAdapter) ManagerState(ctx context.Context) (ManagerState, error) {
	result, err := a.run(ctx, "systemctl", "--user", "show", "--property=ActiveState", "--value", systemdUserUnitName)
	if err != nil || result.ExitCode() != 0 {
		return ManagerUnknown, newSystemdAdapterError("manager state")
	}
	switch strings.TrimSpace(string(result.Stdout())) {
	case "active":
		return ManagerActive, nil
	case "inactive":
		return ManagerInactive, nil
	case "failed":
		return ManagerFailed, nil
	default:
		return ManagerUnknown, nil
	}
}

// RecoveryLogs reads only this unit's journal selector. Callers must decide
// whether any returned log content is safe to display.
func (a *SystemdUserAdapter) RecoveryLogs(ctx context.Context) ([]byte, error) {
	result, err := a.run(ctx, "journalctl", "--user", "--unit", systemdUserUnitName)
	if err != nil || result.ExitCode() != 0 {
		return nil, newSystemdAdapterError("recovery logs")
	}
	return result.Stdout(), nil
}

func (a *SystemdUserAdapter) registrationForUnit(content []byte) (Registration, bool) {
	const marker = "# Managed by PowerContext\n# X-PowerContext-Metadata: "
	text := string(content)
	if !strings.HasPrefix(text, marker) {
		return Registration{}, false
	}
	line, remainder, found := strings.Cut(text[len(marker):], "\n")
	if !found || !validEncodedRegistration(line) {
		return Registration{}, false
	}
	registration, err := DecodeRegistration(line)
	if err != nil {
		return Registration{}, false
	}
	expected, err := a.render(registration)
	if err != nil || !bytes.Equal(content, expected) || remainder == "" {
		return Registration{}, false
	}
	return registration, true
}

func (a *SystemdUserAdapter) render(registration Registration) ([]byte, error) {
	arguments, err := a.launcher.command(registration)
	if err != nil {
		return nil, err
	}
	metadata, err := registration.Encode()
	if err != nil {
		return nil, newSystemdAdapterError("render")
	}
	quoted := make([]string, 0, len(arguments))
	for _, argument := range arguments {
		quoted = append(quoted, quoteSystemdArgument(argument))
	}
	return []byte("# Managed by PowerContext\n" +
		"# X-PowerContext-Metadata: " + metadata + "\n" +
		"[Unit]\n" +
		"Description=PowerContext personal Server\n" +
		"After=network.target\n" +
		"StartLimitIntervalSec=60\n" +
		"StartLimitBurst=3\n" +
		"\n" +
		"[Service]\n" +
		"Type=simple\n" +
		"Environment=POWERCONTEXT_SERVICE_OWNED=true\n" +
		"Environment=POWERCONTEXT_SERVICE_METADATA=" + metadata + "\n" +
		"ExecStart=" + strings.Join(quoted, " ") + "\n" +
		"Restart=on-failure\n" +
		"RestartSec=5s\n" +
		"TimeoutStopSec=30s\n" +
		"\n" +
		"[Install]\n" +
		"WantedBy=default.target\n"), nil
}

func (a *SystemdUserAdapter) requireMutableManager(ctx context.Context) error {
	artifact, err := a.InspectArtifact(ctx)
	if err != nil {
		return err
	}
	if artifact.State() != RegistrationInstalled {
		return newSystemdAdapterError("artifact ownership")
	}
	manager, err := a.InspectManager(ctx)
	if err != nil {
		return err
	}
	if manager.Ownership() != ManagerOwnershipOwned && manager.Ownership() != ManagerOwnershipNotLoaded {
		return newSystemdAdapterError("manager ownership")
	}
	return nil
}

func (a *SystemdUserAdapter) runRequired(ctx context.Context, operation string, program string, arguments ...string) error {
	result, err := a.run(ctx, program, arguments...)
	if err != nil || result.ExitCode() != 0 {
		return newSystemdAdapterError(operation)
	}
	return nil
}

func (a *SystemdUserAdapter) run(ctx context.Context, program string, arguments ...string) (SystemdUserCommandResult, error) {
	if !a.available() {
		return SystemdUserCommandResult{}, newSystemdAdapterError("command")
	}
	command := SystemdUserCommand{program: program, arguments: append([]string(nil), arguments...)}
	result, err := a.boundary.Run(ctx, command)
	if err != nil {
		return SystemdUserCommandResult{}, err
	}
	return result, nil
}

func (a *SystemdUserAdapter) available() bool {
	return a != nil && a.boundary != nil
}

func validSystemdUserConfigRoot(value string) bool {
	if !validSystemdArgument(value, true) || strings.ContainsRune(value, '\\') || path.Clean(value) != value {
		return false
	}
	if value == "/etc" || value == "/root" || strings.HasPrefix(value, "/etc/") || strings.HasPrefix(value, "/root/") {
		return false
	}
	switch path.Join(value, systemdUserUnitDirectory) {
	case "/lib/systemd/user", "/run/systemd/user", "/usr/local/lib/systemd/user", "/usr/local/share/systemd/user", "/usr/lib/systemd/user", "/usr/share/systemd/user":
		return false
	default:
		return true
	}
}

func validSystemdArgument(value string, absolute bool) bool {
	if value == "" || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	if absolute && !strings.HasPrefix(value, "/") {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func validEncodedRegistration(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || character == '-' || character == '_') {
			return false
		}
	}
	return true
}

func quoteSystemdArgument(value string) string {
	replacer := strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "%", "%%", "$", "$$")
	return "\"" + replacer.Replace(value) + "\""
}

func systemdOwnership(value string) (metadata string, owned bool, found bool) {
	for item := range strings.FieldsSeq(value) {
		name, itemValue, hasValue := strings.Cut(item, "=")
		if !hasValue {
			continue
		}
		switch name {
		case "POWERCONTEXT_SERVICE_OWNED":
			if found {
				return "", false, false
			}
			owned = itemValue == "true"
			found = true
		case "POWERCONTEXT_SERVICE_METADATA":
			if metadata != "" || !validEncodedRegistration(itemValue) {
				return "", false, false
			}
			metadata = itemValue
		}
	}
	return metadata, owned, found && metadata != ""
}

func matchesSystemdExecStart(commands []SystemdUserExecStart, arguments []string) bool {
	if len(arguments) == 0 || len(commands) != 1 {
		return false
	}
	return commands[0].Path() == arguments[0] &&
		slices.Equal(commands[0].Arguments(), arguments) &&
		!commands[0].IgnoreErrors()
}

type systemdAdapterError struct{ operation string }

func (e *systemdAdapterError) Error() string {
	return fmt.Sprintf("systemd user service %s failed", e.operation)
}

func newSystemdAdapterError(operation string) *systemdAdapterError {
	return &systemdAdapterError{operation: operation}
}
