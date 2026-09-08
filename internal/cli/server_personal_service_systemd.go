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
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/ob-labs/powercontext-go/internal/personalsvc"
	"github.com/ob-labs/powercontext-go/internal/transportpolicy"
)

const (
	personalServiceUnitName         = "powercontext.service"
	personalServiceManagerName      = "org.freedesktop.systemd1"
	personalServiceManagerPath      = "/org/freedesktop/systemd1"
	personalServiceUnitObjectPath   = "/org/freedesktop/systemd1/unit/powercontext_2eservice"
	personalServiceUnitInterface    = "org.freedesktop.systemd1.Unit"
	personalServiceServiceInterface = "org.freedesktop.systemd1.Service"
	personalServiceProbeTimeout     = 5 * time.Second
)

// linuxPersonalServiceRoots are derived from the current user's home rather
// than from caller-selected systemd roots. They are kept in CLI composition so
// personalsvc remains platform and filesystem independent.
type linuxPersonalServiceRoots struct {
	configRoot string
	stateRoot  string
}

func newLinuxPersonalServiceRoots(home string) (linuxPersonalServiceRoots, error) {
	if !validLinuxUserRoot(home) {
		return linuxPersonalServiceRoots{}, newPersonalServicePlatformError("configuration")
	}
	return linuxPersonalServiceRoots{
		configRoot: path.Join(home, ".config"),
		stateRoot:  path.Join(home, ".local", "state", "powercontext"),
	}, nil
}

func validLinuxUserRoot(value string) bool {
	if !path.IsAbs(value) || path.Clean(value) != value || value == "/" || strings.ContainsRune(value, '\\') {
		return false
	}
	for _, global := range []string{"/etc", "/root", "/usr", "/lib", "/run", "/var"} {
		if value == global || strings.HasPrefix(value, global+"/") {
			return false
		}
	}
	return true
}

type linuxSystemdProcessResult struct {
	exitCode int
	stdout   []byte
}

// linuxSystemdRunner is injected so the fixed native argv contract can
// be tested without starting a user manager.
type linuxSystemdRunner interface {
	Run(context.Context, string, ...string) (linuxSystemdProcessResult, error)
}

// linuxPrivateFileStore owns the no-follow, nonblocking filesystem operations
// used for the private unit and credential-bearing environment file.
type linuxPrivateFileStore interface {
	Read(context.Context, string) ([]byte, bool, error)
	Write(context.Context, string, []byte) error
	Remove(context.Context, string) error
	Validate(context.Context, string) error
}

// linuxOperationLocker serializes an entire native inspect/mutate sequence
// across processes. It is separate from the controller's in-process mutex.
type linuxOperationLocker interface {
	WithLock(context.Context, func(context.Context) error) error
}

type linuxSystemdHTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

// linuxPrivateFileIdentity records the two observations around a no-follow
// open. A changed identity, non-regular type, loose mode, or foreign owner is
// always unsafe.
type linuxPrivateFileIdentity struct {
	device uint64
	inode  uint64
	mode   fs.FileMode
	uid    uint32
}

func validLinuxPrivateFile(before, after linuxPrivateFileIdentity, owner uint32) bool {
	return validLinuxPrivateFileObservation(before, owner) &&
		validLinuxPrivateFileObservation(after, owner) &&
		before.device == after.device && before.inode == after.inode
}

func validLinuxPrivateFileObservation(value linuxPrivateFileIdentity, owner uint32) bool {
	return value.device != 0 && value.inode != 0 && value.uid == owner && value.mode.IsRegular() &&
		value.mode&fs.ModeSymlink == 0 && value.mode.Perm()&0o077 == 0
}

// linuxSystemdBoundary is the composition-owned Linux systemd-user boundary.
// It is inert until a caller invokes one of its methods; constructing it does
// not inspect a bus, file, process, or service lifecycle.
type linuxSystemdBoundary struct {
	roots       linuxPersonalServiceRoots
	runner      linuxSystemdRunner
	files       linuxPrivateFileStore
	locker      linuxOperationLocker
	probeClient linuxSystemdHTTPClient
}

var _ personalsvc.SystemdUserBoundary = (*linuxSystemdBoundary)(nil)

func newLinuxSystemdBoundary(
	roots linuxPersonalServiceRoots,
	runner linuxSystemdRunner,
	files linuxPrivateFileStore,
	locker linuxOperationLocker,
) (*linuxSystemdBoundary, error) {
	home := path.Dir(roots.configRoot)
	if !validLinuxUserRoot(home) || roots.configRoot != path.Join(home, ".config") ||
		roots.stateRoot != path.Join(home, ".local", "state", "powercontext") || runner == nil || files == nil || locker == nil {
		return nil, newPersonalServicePlatformError("configuration")
	}
	return &linuxSystemdBoundary{
		roots: roots, runner: runner, files: files, locker: locker,
		probeClient: &http.Client{
			Timeout:   personalServiceProbeTimeout,
			Transport: &http.Transport{Proxy: nil},
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

func (b *linuxSystemdBoundary) unitPath() string {
	if b == nil {
		return ""
	}
	return path.Join(b.roots.configRoot, "systemd", "user", personalServiceUnitName)
}

func (b *linuxSystemdBoundary) lockPath() string {
	if b == nil {
		return ""
	}
	return path.Join(b.roots.stateRoot, "personal-service.lock")
}

// OperationBoundary exposes the private cross-process lock through the pure
// controller's lifecycle boundary without importing filesystem concerns into
// personalsvc.
func (b *linuxSystemdBoundary) OperationBoundary() personalsvc.OperationBoundary {
	return linuxSystemdOperationBoundary{boundary: b}
}

type linuxSystemdOperationBoundary struct{ boundary *linuxSystemdBoundary }

var _ personalsvc.OperationBoundary = linuxSystemdOperationBoundary{}

func (b linuxSystemdOperationBoundary) Run(ctx context.Context, operation func(context.Context) error) error {
	if b.boundary == nil {
		return newPersonalServicePlatformError("operation lock")
	}
	return b.boundary.WithOperationLock(ctx, operation)
}

// WithOperationLock must wrap every inspect, registration, unit mutation,
// manager mutation, and final inspection performed by the CLI lifecycle.
func (b *linuxSystemdBoundary) WithOperationLock(ctx context.Context, operation func(context.Context) error) error {
	if !b.available() || operation == nil {
		return newPersonalServicePlatformError("operation lock")
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	if err := b.locker.WithLock(ctx, operation); err != nil {
		if contextErr := contextError(ctx); contextErr != nil {
			return contextErr
		}
		return newPersonalServicePlatformError("operation lock")
	}
	return nil
}

// VerifyEnvironmentFile validates a registered file through the Linux secure
// open boundary without retaining or rendering its contents.
func (b *linuxSystemdBoundary) VerifyEnvironmentFile(ctx context.Context, envFile string) error {
	if !b.available() || !validLinuxPrivatePath(envFile) {
		return newPersonalServicePlatformError("environment file")
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	if err := b.files.Validate(ctx, envFile); err != nil {
		if contextErr := contextError(ctx); contextErr != nil {
			return contextErr
		}
		return newPersonalServicePlatformError("environment file")
	}
	return nil
}

// LoadEnvironmentFile reads and parses a private environment file through the
// same no-follow identity validation used by install and launcher execution.
func (b *linuxSystemdBoundary) LoadEnvironmentFile(ctx context.Context, envFile string) (map[string]string, error) {
	if !b.available() || !validLinuxPrivatePath(envFile) {
		return nil, newPersonalServicePlatformError("environment file")
	}
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	content, exists, err := b.files.Read(ctx, envFile)
	if err != nil || !exists {
		if contextErr := contextError(ctx); contextErr != nil {
			return nil, contextErr
		}
		return nil, newPersonalServicePlatformError("environment file")
	}
	values, err := parseConfigEnvironment(string(content))
	if err != nil {
		return nil, newPersonalServicePlatformError("environment file")
	}
	return maps.Clone(values), nil
}

func (b *linuxSystemdBoundary) ReadFile(ctx context.Context, name string) ([]byte, bool, error) {
	if !b.available() || name != b.unitPath() {
		return nil, false, newPersonalServicePlatformError("unit path")
	}
	if err := contextError(ctx); err != nil {
		return nil, false, err
	}
	content, exists, err := b.files.Read(ctx, name)
	if err != nil {
		if contextErr := contextError(ctx); contextErr != nil {
			return nil, false, contextErr
		}
		return nil, false, newPersonalServicePlatformError("unit read")
	}
	return bytes.Clone(content), exists, nil
}

func (b *linuxSystemdBoundary) WriteFile(ctx context.Context, name string, content []byte) error {
	if !b.available() || name != b.unitPath() {
		return newPersonalServicePlatformError("unit path")
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	if err := b.files.Write(ctx, name, bytes.Clone(content)); err != nil {
		if contextErr := contextError(ctx); contextErr != nil {
			return contextErr
		}
		return newPersonalServicePlatformError("unit write")
	}
	return nil
}

func (b *linuxSystemdBoundary) RemoveFile(ctx context.Context, name string) error {
	if !b.available() || name != b.unitPath() {
		return newPersonalServicePlatformError("unit path")
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	if err := b.files.Remove(ctx, name); err != nil {
		if contextErr := contextError(ctx); contextErr != nil {
			return contextErr
		}
		return newPersonalServicePlatformError("unit remove")
	}
	return nil
}

// InspectUnit reads typed D-Bus properties instead of parsing presentation
// output. The object path, destination, interfaces, and unit name are fixed.
func (b *linuxSystemdBoundary) InspectUnit(ctx context.Context, name string) (personalsvc.SystemdUserManagerUnit, error) {
	if !b.available() || name != personalServiceUnitName {
		return personalsvc.SystemdUserManagerUnit{}, newPersonalServicePlatformError("unit inspect")
	}
	objectResult, err := b.busctlResult(ctx,
		"call", personalServiceManagerName, personalServiceManagerPath, personalServiceManagerName+".Manager", "LoadUnit", "s", personalServiceUnitName,
	)
	if err != nil {
		return personalsvc.SystemdUserManagerUnit{}, err
	}
	// Controller methods establish user-manager support before inspection. Once
	// that support check succeeded, LoadUnit's nonzero result is the exact
	// unregistered-unit state needed for a first install or completed uninstall.
	if objectResult.exitCode != 0 {
		return personalsvc.NewSystemdUserManagerUnit("not-found", "", false, nil, nil, nil), nil
	}
	if parseErr := parseBusctlObjectPath(objectResult.stdout); parseErr != nil {
		if b.systemctlShowNotFound(ctx) {
			return personalsvc.NewSystemdUserManagerUnit("not-found", "", false, nil, nil, nil), nil
		}
		return personalsvc.SystemdUserManagerUnit{}, newPersonalServicePlatformError("unit inspect")
	}
	unitProperties, err := b.busctl(ctx,
		"call", personalServiceManagerName, personalServiceUnitObjectPath, "org.freedesktop.DBus.Properties", "GetAll", "s", personalServiceUnitInterface,
	)
	if err != nil {
		return personalsvc.SystemdUserManagerUnit{}, err
	}
	unitValues, err := parseBusctlProperties(unitProperties)
	if err != nil {
		if b.systemctlShowNotFound(ctx) {
			return personalsvc.NewSystemdUserManagerUnit("not-found", "", false, nil, nil, nil), nil
		}
		return personalsvc.SystemdUserManagerUnit{}, newPersonalServicePlatformError("unit inspect")
	}
	loadState, err := busctlRequiredString(unitValues, "LoadState", "s")
	if err != nil {
		if b.systemctlShowNotFound(ctx) {
			return personalsvc.NewSystemdUserManagerUnit("not-found", "", false, nil, nil, nil), nil
		}
		return personalsvc.SystemdUserManagerUnit{}, newPersonalServicePlatformError("unit inspect")
	}
	if loadState == "not-found" {
		return personalsvc.NewSystemdUserManagerUnit("not-found", "", false, nil, nil, nil), nil
	}
	serviceProperties, err := b.busctl(ctx,
		"call", personalServiceManagerName, personalServiceUnitObjectPath, "org.freedesktop.DBus.Properties", "GetAll", "s", personalServiceServiceInterface,
	)
	if err != nil {
		return personalsvc.SystemdUserManagerUnit{}, err
	}
	unit, err := parseBusctlUnit(unitValues, serviceProperties)
	if err != nil {
		if b.systemctlShowNotFound(ctx) {
			return personalsvc.NewSystemdUserManagerUnit("not-found", "", false, nil, nil, nil), nil
		}
		return personalsvc.SystemdUserManagerUnit{}, newPersonalServicePlatformError("unit inspect")
	}
	return unit, nil
}

func (b *linuxSystemdBoundary) Run(ctx context.Context, command personalsvc.SystemdUserCommand) (personalsvc.SystemdUserCommandResult, error) {
	if !b.available() || !allowedLinuxSystemdCommand(command) {
		return personalsvc.SystemdUserCommandResult{}, newPersonalServicePlatformError("manager command")
	}
	if err := contextError(ctx); err != nil {
		return personalsvc.SystemdUserCommandResult{}, err
	}
	result, err := b.runner.Run(ctx, command.Program(), command.Arguments()...)
	if err != nil {
		if contextErr := contextError(ctx); contextErr != nil {
			return personalsvc.SystemdUserCommandResult{}, contextErr
		}
		return personalsvc.SystemdUserCommandResult{}, newPersonalServicePlatformError("manager command")
	}
	return personalsvc.NewSystemdUserCommandResult(result.exitCode, result.stdout), nil
}

// Probe requests only the exact public liveness route through a proxy-free,
// bounded loopback client. A valid liveness response identifies an existing
// PowerContext process; every other response proves that the endpoint is busy
// but cannot be used as the managed service's liveness evidence.
func (b *linuxSystemdBoundary) Probe(ctx context.Context, endpoint string) (personalsvc.ProbeState, error) {
	if !b.available() || b.probeClient == nil {
		return personalsvc.ProbeUnreachable, newPersonalServicePlatformError("liveness probe")
	}
	if err := contextError(ctx); err != nil {
		return personalsvc.ProbeUnreachable, err
	}
	target, err := personalServiceHealthURL(endpoint)
	if err != nil {
		return personalsvc.ProbeUnreachable, newPersonalServicePlatformError("liveness probe")
	}
	requestCtx, cancel := context.WithTimeout(ctx, personalServiceProbeTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, target.String(), nil)
	if err != nil {
		return personalsvc.ProbeUnreachable, newPersonalServicePlatformError("liveness probe")
	}
	response, err := b.probeClient.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return personalsvc.ProbeUnreachable, ctx.Err()
		}
		return personalsvc.ProbeUnreachable, nil
	}
	defer func() { _ = response.Body.Close() }()
	content, readErr := io.ReadAll(io.LimitReader(response.Body, 4<<10))
	if response.StatusCode != http.StatusOK || readErr != nil {
		return personalsvc.ProbeConflict, nil
	}
	var health struct {
		Status string `json:"status"`
	}
	if json.Unmarshal(content, &health) != nil || health.Status != "ok" {
		return personalsvc.ProbeConflict, nil
	}
	return personalsvc.ProbeLive, nil
}

func personalServiceHealthURL(endpoint string) (*url.URL, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.Hostname() == "" ||
		!transportpolicy.IsLoopbackHost(parsed.Hostname()) || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("invalid liveness endpoint")
	}
	target := parsed.Clone()
	target.Path = "/health/live"
	target.RawPath = ""
	target.RawQuery = ""
	target.ForceQuery = false
	target.Fragment = ""
	return target, nil
}

func (b *linuxSystemdBoundary) busctl(ctx context.Context, arguments ...string) ([]byte, error) {
	result, err := b.busctlResult(ctx, arguments...)
	if err != nil || result.exitCode != 0 {
		if contextErr := contextError(ctx); contextErr != nil {
			return nil, contextErr
		}
		return nil, newPersonalServicePlatformError("user bus")
	}
	return bytes.Clone(result.stdout), nil
}

func (b *linuxSystemdBoundary) busctlResult(ctx context.Context, arguments ...string) (linuxSystemdProcessResult, error) {
	if err := contextError(ctx); err != nil {
		return linuxSystemdProcessResult{}, err
	}
	command := append([]string{"--user", "--json=short"}, arguments...)
	result, err := b.runner.Run(ctx, "busctl", command...)
	if err != nil {
		if contextErr := contextError(ctx); contextErr != nil {
			return linuxSystemdProcessResult{}, contextErr
		}
		return linuxSystemdProcessResult{}, newPersonalServicePlatformError("user bus")
	}
	return linuxSystemdProcessResult{exitCode: result.exitCode, stdout: bytes.Clone(result.stdout)}, nil
}

// systemctlShowNotFound preserves a strict user-bus inspection for loaded
// units while using the upstream adapter's stable absent-unit classification
// when a systemd version emits an unrecognized D-Bus property envelope.
func (b *linuxSystemdBoundary) systemctlShowNotFound(ctx context.Context) bool {
	if err := contextError(ctx); err != nil {
		return false
	}
	result, err := b.runner.Run(ctx,
		"systemctl",
		"--user",
		"show",
		"--property=LoadState",
		"--property=FragmentPath",
		"--property=DropInPaths",
		"--property=Environment",
		"--property=ExecStart",
		personalServiceUnitName,
	)
	if err != nil || result.exitCode != 0 || contextError(ctx) != nil {
		return false
	}
	loadState, found := systemctlShowProperty(result.stdout, "LoadState")
	return !found || loadState == "not-found"
}

func systemctlShowProperty(payload []byte, name string) (string, bool) {
	var value string
	found := false
	for line := range strings.Lines(string(payload)) {
		key, candidate, ok := strings.Cut(strings.TrimRight(line, "\r\n"), "=")
		if !ok || key != name {
			continue
		}
		if found {
			return "", false
		}
		value = candidate
		found = true
	}
	return value, found
}

func (b *linuxSystemdBoundary) available() bool {
	return b != nil && b.runner != nil && b.files != nil && b.locker != nil && b.unitPath() != "" && b.lockPath() != ""
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return errors.New("missing context")
	}
	return ctx.Err()
}

func validLinuxPrivatePath(value string) bool {
	return path.IsAbs(value) && path.Clean(value) == value && value != "/" && !strings.ContainsRune(value, '\\')
}

func allowedLinuxSystemdCommand(command personalsvc.SystemdUserCommand) bool {
	arguments := command.Arguments()
	switch command.Program() {
	case "systemctl":
		return slices.ContainsFunc([][]string{
			{"--user", "show-environment"},
			{"--user", "daemon-reload"},
			{"--user", "enable", personalServiceUnitName},
			{"--user", "reset-failed", personalServiceUnitName},
			{"--user", "start", personalServiceUnitName},
			{"--user", "stop", personalServiceUnitName},
			{"--user", "disable", personalServiceUnitName},
			{"--user", "show", "--property=ActiveState", "--value", personalServiceUnitName},
		}, func(candidate []string) bool { return slices.Equal(arguments, candidate) })
	case "journalctl":
		return slices.Equal(arguments, []string{"--user", "--unit", personalServiceUnitName})
	default:
		return false
	}
}

type busctlValue struct {
	typeName string
	data     jsontext.Value
}

func parseBusctlObjectPath(payload []byte) error {
	value, err := parseBusctlEnvelope(payload)
	if err != nil || value.typeName != "o" {
		return errors.New("invalid user-bus object")
	}
	object, err := busctlString(value.data)
	if err != nil || object != personalServiceUnitObjectPath {
		return errors.New("unexpected user-bus object")
	}
	return nil
}

func parseBusctlUnit(unitValues map[string]busctlValue, servicePayload []byte) (personalsvc.SystemdUserManagerUnit, error) {
	serviceValues, err := parseBusctlProperties(servicePayload)
	if err != nil {
		return personalsvc.SystemdUserManagerUnit{}, err
	}
	loadState, err := busctlRequiredString(unitValues, "LoadState", "s")
	if err != nil {
		return personalsvc.SystemdUserManagerUnit{}, err
	}
	fragmentPath, err := busctlRequiredString(unitValues, "FragmentPath", "s")
	if err != nil {
		return personalsvc.SystemdUserManagerUnit{}, err
	}
	dropIns, err := busctlRequiredStrings(unitValues, "DropInPaths", "as")
	if err != nil {
		return personalsvc.SystemdUserManagerUnit{}, err
	}
	environment, err := busctlRequiredStrings(serviceValues, "Environment", "as")
	if err != nil {
		return personalsvc.SystemdUserManagerUnit{}, err
	}
	execStart, err := busctlExecStart(serviceValues["ExecStart"])
	if err != nil {
		return personalsvc.SystemdUserManagerUnit{}, err
	}
	return personalsvc.NewSystemdUserManagerUnit(
		loadState, fragmentPath, true, dropIns, environment, execStart,
	), nil
}

func parseBusctlProperties(payload []byte) (map[string]busctlValue, error) {
	envelope, err := parseBusctlEnvelope(payload)
	if err != nil || envelope.typeName != "a{sv}" || envelope.data.Kind() != '{' {
		return nil, errors.New("invalid user-bus properties")
	}
	var members map[string]jsontext.Value
	if err := json.Unmarshal(envelope.data, &members); err != nil || members == nil {
		return nil, errors.New("invalid user-bus properties")
	}
	result := make(map[string]busctlValue, len(members))
	for name, member := range members {
		value, err := parseBusctlEnvelope(member)
		if err != nil {
			return nil, errors.New("invalid user-bus properties")
		}
		result[name] = value
	}
	return result, nil
}

func parseBusctlEnvelope(payload []byte) (busctlValue, error) {
	if jsontext.Value(payload).Kind() != '{' {
		return busctlValue{}, errors.New("invalid user-bus JSON")
	}
	var object map[string]jsontext.Value
	if err := json.Unmarshal(payload, &object); err != nil || len(object) != 2 {
		return busctlValue{}, errors.New("invalid user-bus JSON")
	}
	typeValue, found := object["type"]
	if !found {
		return busctlValue{}, errors.New("invalid user-bus JSON")
	}
	typeName, err := busctlString(typeValue)
	if err != nil || typeName == "" {
		return busctlValue{}, errors.New("invalid user-bus JSON")
	}
	data, found := object["data"]
	if !found {
		return busctlValue{}, errors.New("invalid user-bus JSON")
	}
	return busctlValue{typeName: typeName, data: data}, nil
}

func busctlRequiredString(values map[string]busctlValue, name, typeName string) (string, error) {
	value, found := values[name]
	if !found || value.typeName != typeName {
		return "", errors.New("missing user-bus property")
	}
	return busctlString(value.data)
}

func busctlRequiredStrings(values map[string]busctlValue, name, typeName string) ([]string, error) {
	value, found := values[name]
	if !found || value.typeName != typeName || value.data.Kind() != '[' {
		return nil, errors.New("missing user-bus property")
	}
	var result []string
	if err := json.Unmarshal(value.data, &result); err != nil {
		return nil, errors.New("invalid user-bus property")
	}
	return slices.Clone(result), nil
}

func busctlExecStart(value busctlValue) ([]personalsvc.SystemdUserExecStart, error) {
	if value.typeName != "a(sasbttttuii)" || value.data.Kind() != '[' {
		return nil, errors.New("invalid ExecStart")
	}
	var rows []jsontext.Value
	if err := json.Unmarshal(value.data, &rows); err != nil {
		return nil, errors.New("invalid ExecStart")
	}
	commands := make([]personalsvc.SystemdUserExecStart, 0, len(rows))
	for _, row := range rows {
		var fields []jsontext.Value
		if row.Kind() != '[' || json.Unmarshal(row, &fields) != nil || len(fields) != 10 {
			return nil, errors.New("invalid ExecStart")
		}
		commandPath, err := busctlString(fields[0])
		if err != nil || commandPath == "" {
			return nil, errors.New("invalid ExecStart")
		}
		var arguments []string
		if fields[1].Kind() != '[' || json.Unmarshal(fields[1], &arguments) != nil || len(arguments) == 0 {
			return nil, errors.New("invalid ExecStart")
		}
		var ignoreErrors bool
		if fields[2].Kind() != 'f' && fields[2].Kind() != 't' || json.Unmarshal(fields[2], &ignoreErrors) != nil {
			return nil, errors.New("invalid ExecStart")
		}
		commands = append(commands, personalsvc.NewSystemdUserExecStart(commandPath, arguments, ignoreErrors))
	}
	return commands, nil
}

func busctlString(value jsontext.Value) (string, error) {
	if value.Kind() != '"' {
		return "", errors.New("invalid user-bus string")
	}
	var result string
	if err := json.Unmarshal(value, &result); err != nil {
		return "", errors.New("invalid user-bus string")
	}
	return result, nil
}

type personalServicePlatformError struct{ operation string }

func (e *personalServicePlatformError) Error() string {
	if e == nil || e.operation == "" {
		return "personal Server service platform operation failed"
	}
	return "personal Server service " + e.operation + " failed"
}

func newPersonalServicePlatformError(operation string) error {
	return &personalServicePlatformError{operation: operation}
}
