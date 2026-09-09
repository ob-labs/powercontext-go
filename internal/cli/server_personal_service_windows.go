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

//go:build windows

package cli

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ob-labs/powercontext-go/internal/personalsvc"
	personalsvcwindows "github.com/ob-labs/powercontext-go/internal/personalsvchost/windows"
	"github.com/ob-labs/powercontext-go/server"
)

const (
	windowsPersonalServiceStartupTimeout  = 15 * time.Second
	windowsPersonalServiceStartupInterval = 100 * time.Millisecond
)

func runPersonalServiceInstall(ctx context.Context, state *commandState, envFile, dataDir string, startOnLogin bool) error {
	registration, plan, root, err := newWindowsPersonalServiceRegistration(ctx, state, envFile, dataDir, startOnLogin)
	if err != nil {
		return err
	}
	runtime, err := newWindowsPersonalServiceRuntime(root, plan)
	if err != nil {
		return err
	}
	status, err := runtime.controller.Install(ctx, registration)
	if err != nil && windowsPersonalServiceStartupPending(status, err) {
		ready, waitErr := waitForWindowsPersonalServiceStartup(ctx, runtime.controller, registration)
		if waitErr != nil {
			return waitErr
		}
		if ready {
			status, err = runtime.controller.Status(ctx, registration)
		}
	}
	if err != nil {
		return err
	}
	return writeWindowsPersonalServiceStatus(state, status)
}

func windowsPersonalServiceStartupPending(status personalsvc.Status, err error) bool {
	var operation *personalsvc.OperationError
	return errors.As(err, &operation) &&
		operation.Kind() == personalsvc.ErrorPostCommit &&
		operation.Stage() == personalsvc.StageProbe &&
		status.Registration() == personalsvc.RegistrationInstalled &&
		status.Definition() == personalsvc.DefinitionCurrent &&
		status.ManagerOwnership() == personalsvc.ManagerOwnershipOwned &&
		status.Manager() == personalsvc.ManagerActive &&
		status.Liveness() == personalsvc.LivenessUnreachable
}

func waitForWindowsPersonalServiceStartup(
	ctx context.Context,
	controller *personalsvc.Controller,
	registration personalsvc.Registration,
) (bool, error) {
	startupCtx, cancel := context.WithTimeout(ctx, windowsPersonalServiceStartupTimeout)
	defer cancel()
	ticker := time.NewTicker(windowsPersonalServiceStartupInterval)
	defer ticker.Stop()
	for {
		status, err := controller.Status(startupCtx, registration)
		if err == nil && status.Healthy() {
			return true, nil
		}
		select {
		case <-ctx.Done():
			return false, context.Cause(ctx)
		case <-startupCtx.Done():
			return false, nil
		case <-ticker.C:
		}
	}
}

func runPersonalServiceStatus(ctx context.Context, state *commandState, dataDir string) error {
	runtime, err := newWindowsPersonalServiceInspectionRuntime(dataDir)
	if err != nil {
		return err
	}
	registration, found, err := installedWindowsPersonalServiceRegistration(ctx, runtime.composition.Adapter())
	if err != nil {
		return err
	}
	if !found {
		status, statusErr := runtime.controller.Status(ctx, personalsvc.Registration{})
		if statusErr != nil {
			return statusErr
		}
		return writeWindowsPersonalServiceStatus(state, status)
	}
	status, err := runtime.controller.Status(ctx, registration)
	if err != nil {
		return err
	}
	return writeWindowsPersonalServiceStatus(state, status)
}

func runPersonalServiceUninstall(ctx context.Context, state *commandState, dataDir string) error {
	runtime, err := newWindowsPersonalServiceInspectionRuntime(dataDir)
	if err != nil {
		return err
	}
	registration, found, err := installedWindowsPersonalServiceRegistration(ctx, runtime.composition.Adapter())
	if err != nil {
		return err
	}
	if !found {
		status, statusErr := runtime.controller.Status(ctx, personalsvc.Registration{})
		if statusErr != nil {
			return statusErr
		}
		return writeWindowsPersonalServiceStatus(state, status)
	}
	status, err := runtime.controller.Uninstall(ctx, registration)
	if err != nil {
		return err
	}
	return writeWindowsPersonalServiceStatus(state, status)
}

func runPersonalServiceChild(ctx context.Context, state *commandState, envFile, endpoint, dataDir string) error {
	envFile, err := windowsPersonalServiceEnvFile(envFile)
	if err != nil {
		return err
	}
	dataDir, err = windowsPersonalServiceDataDir(dataDir)
	if err != nil {
		return err
	}
	override, err := windowsPersonalServiceHTTPOverride(endpoint)
	if err != nil {
		return errors.New("server: invalid personal service endpoint")
	}
	expected, plan, err := newWindowsPersonalServiceRegistrationFromIdentity(state, envFile, endpoint, dataDir, true)
	if err != nil {
		return errors.New("server: personal service identity is invalid")
	}
	runtime, err := newWindowsPersonalServiceRuntime(dataDir, plan)
	if err != nil {
		return err
	}
	registered, found, err := installedWindowsPersonalServiceRegistration(ctx, runtime.composition.Adapter())
	if err != nil || !found || registered != expected {
		return errors.New("server: personal service identity is unavailable")
	}
	values, err := loadServerEnvironmentFile(envFile)
	if err != nil {
		return err
	}
	var config server.ProcessConfig
	err = withServerEnvironment(windowsPersonalServiceEnvironment(values, dataDir), func() error {
		var configErr error
		config, configErr = server.LoadConfigWithHTTPOverride(override)
		return configErr
	})
	if err != nil || config.Database.Kind != "sqlite" {
		return errors.New("server: personal service configuration is invalid")
	}
	runner := state.serverRun
	if runner == nil {
		runner = runServer
	}
	return runner(ctx, state, config)
}

type windowsPersonalServiceRuntime struct {
	composition *personalsvcwindows.Composition
	controller  *personalsvc.Controller
}

func newWindowsPersonalServiceRuntime(root string, plan personalsvc.TaskSchedulerSpec) (windowsPersonalServiceRuntime, error) {
	composition, err := personalsvcwindows.New(personalsvcwindows.Config{UserDataRoot: root, Plan: plan})
	if err != nil || composition.Adapter() == nil || composition.OperationBoundary() == nil {
		return windowsPersonalServiceRuntime{}, errors.New("server: Windows personal service configuration is unavailable")
	}
	controller, err := personalsvc.NewController(composition.Adapter(), composition.OperationBoundary())
	if err != nil {
		return windowsPersonalServiceRuntime{}, errors.New("server: Windows personal service controller is unavailable")
	}
	return windowsPersonalServiceRuntime{composition: composition, controller: controller}, nil
}

func newWindowsPersonalServiceInspectionRuntime(dataDir string) (windowsPersonalServiceRuntime, error) {
	dataDir, err := windowsPersonalServiceDataDir(dataDir)
	if err != nil {
		return windowsPersonalServiceRuntime{}, err
	}
	plan, err := newWindowsPersonalServiceInspectionPlan(dataDir)
	if err != nil {
		return windowsPersonalServiceRuntime{}, errors.New("server: Windows personal service configuration is unavailable")
	}
	return newWindowsPersonalServiceRuntime(dataDir, plan)
}

func newWindowsPersonalServiceInspectionPlan(dataDir string) (personalsvc.TaskSchedulerSpec, error) {
	binary, err := windowsPersonalServiceExecutable()
	if err != nil {
		return personalsvc.TaskSchedulerSpec{}, err
	}
	definition, err := personalsvc.NewDefinition(personalsvc.DefinitionInput{
		Ownership: personalsvc.OwnershipMarker, DefinitionVersion: personalsvc.DefinitionVersion,
		PackageVersion: "inspection", Binary: binary, Endpoint: "http://127.0.0.1:1",
		DataDir: dataDir, EnvFile: filepath.Join(dataDir, "server.env"),
	})
	if err != nil {
		return personalsvc.TaskSchedulerSpec{}, err
	}
	registration, err := personalsvc.NewRegistration(definition)
	if err != nil {
		return personalsvc.TaskSchedulerSpec{}, err
	}
	return personalsvc.NewTaskSchedulerSpec(registration, []string{"server", "_service-run"}, dataDir, true)
}

func newWindowsPersonalServiceRegistration(ctx context.Context, state *commandState, envFile, dataDir string, startOnLogin bool) (personalsvc.Registration, personalsvc.TaskSchedulerSpec, string, error) {
	envFile, err := windowsPersonalServiceEnvFile(envFile)
	if err != nil {
		return personalsvc.Registration{}, personalsvc.TaskSchedulerSpec{}, "", err
	}
	dataDir, err = windowsPersonalServiceDataDir(dataDir)
	if err != nil {
		return personalsvc.Registration{}, personalsvc.TaskSchedulerSpec{}, "", err
	}
	if mkdirErr := os.MkdirAll(dataDir, 0o700); mkdirErr != nil {
		return personalsvc.Registration{}, personalsvc.TaskSchedulerSpec{}, "", errors.New("server: personal service data directory is unavailable")
	}
	values, err := loadServerEnvironmentFile(envFile)
	if err != nil {
		return personalsvc.Registration{}, personalsvc.TaskSchedulerSpec{}, "", err
	}
	var config server.ProcessConfig
	err = withServerEnvironment(windowsPersonalServiceEnvironment(values, dataDir), func() error {
		var configErr error
		config, configErr = server.LoadConfig()
		return configErr
	})
	if err != nil || config.Database.Kind != "sqlite" {
		return personalsvc.Registration{}, personalsvc.TaskSchedulerSpec{}, "", errors.New("server: personal service configuration is invalid")
	}
	registration, plan, err := newWindowsPersonalServiceRegistrationFromIdentity(state, envFile, "http://"+config.HTTP.Address(), dataDir, startOnLogin)
	if err != nil {
		return personalsvc.Registration{}, personalsvc.TaskSchedulerSpec{}, "", err
	}
	return registration, plan, dataDir, ctx.Err()
}

func newWindowsPersonalServiceRegistrationFromIdentity(state *commandState, envFile, endpoint, dataDir string, startOnLogin bool) (personalsvc.Registration, personalsvc.TaskSchedulerSpec, error) {
	binary, err := windowsPersonalServiceExecutable()
	if err != nil {
		return personalsvc.Registration{}, personalsvc.TaskSchedulerSpec{}, err
	}
	definition, err := personalsvc.NewDefinition(personalsvc.DefinitionInput{
		Ownership: personalsvc.OwnershipMarker, DefinitionVersion: personalsvc.DefinitionVersion,
		PackageVersion: windowsPersonalServiceVersion(state), Binary: binary,
		Endpoint: endpoint, DataDir: dataDir, EnvFile: envFile,
	})
	if err != nil {
		return personalsvc.Registration{}, personalsvc.TaskSchedulerSpec{}, err
	}
	registration, err := personalsvc.NewRegistration(definition)
	if err != nil {
		return personalsvc.Registration{}, personalsvc.TaskSchedulerSpec{}, err
	}
	arguments := []string{"server", "_service-run", "--env-file", envFile, "--endpoint", definition.Endpoint(), "--data-dir", dataDir}
	plan, err := personalsvc.NewTaskSchedulerSpec(registration, arguments, dataDir, startOnLogin)
	if err != nil {
		return personalsvc.Registration{}, personalsvc.TaskSchedulerSpec{}, err
	}
	return registration, plan, nil
}

func installedWindowsPersonalServiceRegistration(ctx context.Context, adapter *personalsvcwindows.Adapter) (personalsvc.Registration, bool, error) {
	if adapter == nil {
		return personalsvc.Registration{}, false, errors.New("server: Windows personal service adapter is unavailable")
	}
	artifact, err := adapter.InspectArtifact(ctx)
	if err != nil {
		return personalsvc.Registration{}, false, err
	}
	if artifact.State() == personalsvc.RegistrationNotInstalled {
		return personalsvc.Registration{}, false, nil
	}
	registration, found := artifact.Registration()
	if artifact.State() != personalsvc.RegistrationInstalled || !found || registration.Definition().Validate() != nil {
		return personalsvc.Registration{}, false, errors.New("server: Windows personal service registration is invalid")
	}
	return registration, true, nil
}

func windowsPersonalServiceEnvironment(values map[string]string, dataDir string) map[string]string {
	result := maps.Clone(values)
	result[server.PowerContextHomeEnv] = dataDir
	result["POWERCONTEXT_SERVER_DATABASE_KIND"] = "sqlite"
	result["POWERCONTEXT_SERVER_DATABASE_URL"] = "sqlite+aiosqlite:///" + filepath.ToSlash(filepath.Join(dataDir, "powercontext.db"))
	return result
}

func windowsPersonalServiceEnvFile(value string) (string, error) {
	if !filepath.IsAbs(value) || strings.TrimSpace(value) != value {
		return "", errors.New("server: personal service environment file is invalid")
	}
	resolved, err := filepath.EvalSymlinks(value)
	if err != nil {
		return "", errors.New("server: personal service environment file is unavailable")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() || strings.TrimSpace(resolved) != resolved {
		return "", errors.New("server: personal service environment file is invalid")
	}
	return filepath.Clean(resolved), nil
}

func windowsPersonalServiceDataDir(value string) (string, error) {
	if value == "" {
		var err error
		value, err = server.PowerContextDataDir()
		if err != nil {
			return "", err
		}
	}
	if !filepath.IsAbs(value) || strings.TrimSpace(value) != value {
		return "", errors.New("server: personal service data directory is invalid")
	}
	return resolvePath(value)
}

func windowsPersonalServiceHTTPOverride(endpoint string) (server.HTTPConfigOverride, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return server.HTTPConfigOverride{}, errors.New("invalid endpoint")
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port < 1 || port > 65535 || parsed.Hostname() == "" {
		return server.HTTPConfigOverride{}, errors.New("invalid endpoint")
	}
	host := parsed.Hostname()
	return server.HTTPConfigOverride{Host: &host, Port: &port}, nil
}

func windowsPersonalServiceExecutable() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", errors.New("server: personal service executable is unavailable")
	}
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return "", errors.New("server: personal service executable is unavailable")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("server: personal service executable is invalid")
	}
	return filepath.Clean(resolved), nil
}

func windowsPersonalServiceVersion(state *commandState) string {
	if state == nil || strings.TrimSpace(state.version.Version) == "" {
		return "devel"
	}
	return state.version.Version
}

func writeWindowsPersonalServiceStatus(state *commandState, status personalsvc.Status) error {
	if state == nil {
		return errors.New("server: personal service command state is unavailable")
	}
	view := map[string]string{
		"support": string(status.Support()), "registration": string(status.Registration()), "definition": string(status.Definition()),
		"manager_ownership": string(status.ManagerOwnership()), "manager": string(status.Manager()),
		"liveness": string(status.Liveness()), "recovery": string(status.Recovery()),
	}
	if state.json {
		return writeJSON(state.stdout, view)
	}
	_, err := fmt.Fprintf(state.stdout, "Support: %s\nRegistration: %s\nDefinition: %s\nManager ownership: %s\nManager: %s\nLiveness: %s\nRecovery: %s\n", status.Support(), status.Registration(), status.Definition(), status.ManagerOwnership(), status.Manager(), status.Liveness(), status.Recovery())
	return err
}
