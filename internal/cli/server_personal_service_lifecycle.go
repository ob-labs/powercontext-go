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
	"context"
	"fmt"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ob-labs/powercontext-go/internal/personalsvc"
	"github.com/ob-labs/powercontext-go/server"
)

var (
	personalServiceBoundaryFactory = newCurrentLinuxSystemdBoundary
	personalServiceExecutable      = currentPersonalServiceExecutable
)

type personalServiceRuntime struct {
	boundary   *linuxSystemdBoundary
	adapter    *personalsvc.SystemdUserAdapter
	controller *personalsvc.Controller
}

type personalServiceStatusView struct {
	Support          personalsvc.Support           `json:"support"`
	Registration     personalsvc.RegistrationState `json:"registration"`
	Definition       personalsvc.DefinitionState   `json:"definition"`
	ManagerOwnership personalsvc.ManagerOwnership  `json:"manager_ownership"`
	Manager          personalsvc.ManagerState      `json:"manager"`
	Liveness         personalsvc.Liveness          `json:"liveness"`
	Recovery         personalsvc.Recovery          `json:"recovery"`
}

func runPersonalServiceInstall(ctx context.Context, state *commandState, envFile, dataDir string) error {
	runtime, err := newPersonalServiceRuntime()
	if err != nil {
		return err
	}
	registration, err := newPersonalServiceRegistration(ctx, state, runtime.boundary, envFile, dataDir)
	if err != nil {
		return err
	}
	status, err := runtime.controller.Install(ctx, registration)
	if err != nil {
		return err
	}
	return writePersonalServiceStatus(state, status)
}

func runPersonalServiceStatus(ctx context.Context, state *commandState) error {
	runtime, err := newPersonalServiceRuntime()
	if err != nil {
		return err
	}
	registration, found, err := installedPersonalServiceRegistration(ctx, runtime.adapter)
	if err != nil {
		return err
	}
	if !found {
		status, statusErr := runtime.controller.Status(ctx, personalsvc.Registration{})
		if statusErr != nil {
			return statusErr
		}
		return writePersonalServiceStatus(state, status)
	}
	status, err := runtime.controller.Status(ctx, registration)
	if err != nil {
		return err
	}
	return writePersonalServiceStatus(state, status)
}

func runPersonalServiceUninstall(ctx context.Context, state *commandState) error {
	runtime, err := newPersonalServiceRuntime()
	if err != nil {
		return err
	}
	registration, found, err := installedPersonalServiceRegistration(ctx, runtime.adapter)
	if err != nil {
		return err
	}
	if !found {
		status, statusErr := runtime.controller.Status(ctx, personalsvc.Registration{})
		if statusErr != nil {
			return statusErr
		}
		return writePersonalServiceStatus(state, status)
	}
	status, err := runtime.controller.Uninstall(ctx, registration)
	if err != nil {
		return err
	}
	return writePersonalServiceStatus(state, status)
}

func runPersonalServiceLauncher(ctx context.Context, state *commandState, envFile, endpoint, dataDir string) error {
	runtime, err := newPersonalServiceRuntime()
	if err != nil {
		return err
	}
	registration, found, err := installedPersonalServiceRegistration(ctx, runtime.adapter)
	if err != nil || !found {
		return newPersonalServicePlatformError("launcher identity")
	}
	expected, err := newPersonalServiceRegistrationFromIdentity(state, envFile, endpoint, dataDir)
	if err != nil || registration != expected {
		return newPersonalServicePlatformError("launcher identity")
	}
	values, err := runtime.boundary.LoadEnvironmentFile(ctx, registration.Definition().EnvFile())
	if err != nil {
		return err
	}
	override, err := personalServiceHTTPOverride(registration.Definition().Endpoint())
	if err != nil {
		return newPersonalServicePlatformError("launcher identity")
	}
	return withIsolatedServerEnvironment(personalServiceEnvironment(values, registration.Definition().DataDir()), func() error {
		config, configErr := server.LoadConfigWithHTTPOverride(override)
		if configErr != nil || config.Database.Kind != "sqlite" {
			return newPersonalServicePlatformError("configuration")
		}
		runner := state.serverRun
		if runner == nil {
			runner = runServer
		}
		return runner(ctx, state, config)
	})
}

func newPersonalServiceRuntime() (personalServiceRuntime, error) {
	boundary, err := personalServiceBoundaryFactory()
	if err != nil {
		return personalServiceRuntime{}, err
	}
	launcher, err := personalsvc.NewSystemdUserLauncher("server", "_service-run")
	if err != nil {
		return personalServiceRuntime{}, newPersonalServicePlatformError("configuration")
	}
	adapter, err := personalsvc.NewSystemdUserAdapter(boundary, boundary.roots.configRoot, launcher)
	if err != nil {
		return personalServiceRuntime{}, newPersonalServicePlatformError("configuration")
	}
	controller, err := personalsvc.NewController(adapter, boundary.OperationBoundary())
	if err != nil {
		return personalServiceRuntime{}, newPersonalServicePlatformError("configuration")
	}
	return personalServiceRuntime{boundary: boundary, adapter: adapter, controller: controller}, nil
}

func newPersonalServiceRegistration(
	ctx context.Context,
	state *commandState,
	boundary *linuxSystemdBoundary,
	envFile, dataDir string,
) (personalsvc.Registration, error) {
	envFile, err := personalServiceEnvironmentPath(envFile)
	if err != nil {
		return personalsvc.Registration{}, newPersonalServicePlatformError("environment file")
	}
	dataDir, err = personalServiceDataDir(dataDir)
	if err != nil {
		return personalsvc.Registration{}, newPersonalServicePlatformError("data directory")
	}
	values, err := boundary.LoadEnvironmentFile(ctx, envFile)
	if err != nil {
		return personalsvc.Registration{}, err
	}
	var config server.ProcessConfig
	if err := withIsolatedServerEnvironment(personalServiceEnvironment(values, dataDir), func() error {
		var configErr error
		config, configErr = server.LoadConfig()
		return configErr
	}); err != nil || config.Database.Kind != "sqlite" {
		return personalsvc.Registration{}, newPersonalServicePlatformError("configuration")
	}
	return newPersonalServiceRegistrationFromIdentity(state, envFile, "http://"+config.HTTP.Address(), dataDir)
}

func newPersonalServiceRegistrationFromIdentity(
	state *commandState,
	envFile, endpoint, dataDir string,
) (personalsvc.Registration, error) {
	envFile, err := personalServiceEnvironmentPath(envFile)
	if err != nil {
		return personalsvc.Registration{}, err
	}
	dataDir, err = personalServiceDataDir(dataDir)
	if err != nil {
		return personalsvc.Registration{}, err
	}
	binary, err := personalServiceExecutable()
	if err != nil {
		return personalsvc.Registration{}, err
	}
	definition, err := personalsvc.NewDefinition(personalsvc.DefinitionInput{
		Ownership:         personalsvc.OwnershipMarker,
		DefinitionVersion: personalsvc.DefinitionVersion,
		PackageVersion:    personalServiceVersion(state),
		Binary:            binary,
		Endpoint:          endpoint,
		DataDir:           dataDir,
		EnvFile:           envFile,
	})
	if err != nil {
		return personalsvc.Registration{}, err
	}
	return personalsvc.NewRegistration(definition)
}

func installedPersonalServiceRegistration(ctx context.Context, adapter *personalsvc.SystemdUserAdapter) (personalsvc.Registration, bool, error) {
	artifact, err := adapter.InspectArtifact(ctx)
	if err != nil {
		return personalsvc.Registration{}, false, newPersonalServicePlatformError("registration")
	}
	if artifact.State() != personalsvc.RegistrationInstalled {
		if artifact.State() == personalsvc.RegistrationNotInstalled {
			return personalsvc.Registration{}, false, nil
		}
		return personalsvc.Registration{}, false, newPersonalServicePlatformError("registration")
	}
	registration, found := artifact.Registration()
	if !found || registration.Definition().Validate() != nil {
		return personalsvc.Registration{}, false, newPersonalServicePlatformError("registration")
	}
	return registration, true, nil
}

func personalServiceEnvironment(values map[string]string, dataDir string) map[string]string {
	result := maps.Clone(values)
	result[server.PowerContextHomeEnv] = dataDir
	result["POWERCONTEXT_SERVER_DATABASE_KIND"] = "sqlite"
	result["POWERCONTEXT_SERVER_DATABASE_URL"] = "sqlite+aiosqlite:///" + filepath.ToSlash(filepath.Join(dataDir, "powercontext.db"))
	return result
}

func personalServiceEnvironmentPath(value string) (string, error) {
	if !filepath.IsAbs(value) {
		return "", fmt.Errorf("personal-service environment file must be absolute")
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	return filepath.Clean(absolute), nil
}

func personalServiceDataDir(value string) (string, error) {
	if value == "" {
		return server.PowerContextDataDir()
	}
	if !filepath.IsAbs(value) {
		return "", fmt.Errorf("personal-service data directory must be absolute")
	}
	return resolvePath(value)
}

func personalServiceHTTPOverride(endpoint string) (server.HTTPConfigOverride, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return server.HTTPConfigOverride{}, fmt.Errorf("invalid personal-service endpoint")
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port < 1 || port > 65535 || parsed.Hostname() == "" {
		return server.HTTPConfigOverride{}, fmt.Errorf("invalid personal-service endpoint")
	}
	host := parsed.Hostname()
	return server.HTTPConfigOverride{Host: new(host), Port: new(port)}, nil
}

func currentPersonalServiceExecutable() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("invalid personal-service executable")
	}
	return filepath.Clean(resolved), nil
}

func personalServiceVersion(state *commandState) string {
	if state == nil || strings.TrimSpace(state.version.Version) == "" {
		return "devel"
	}
	return state.version.Version
}

func writePersonalServiceStatus(state *commandState, status personalsvc.Status) error {
	view := personalServiceStatusView{
		Support:          status.Support(),
		Registration:     status.Registration(),
		Definition:       status.Definition(),
		ManagerOwnership: status.ManagerOwnership(),
		Manager:          status.Manager(),
		Liveness:         status.Liveness(),
		Recovery:         status.Recovery(),
	}
	if state.json {
		return writeJSON(state.stdout, view)
	}
	_, err := fmt.Fprintf(state.stdout,
		"Support: %s\nRegistration: %s\nDefinition: %s\nManager ownership: %s\nManager: %s\nLiveness: %s\nRecovery: %s\n",
		view.Support, view.Registration, view.Definition, view.ManagerOwnership, view.Manager, view.Liveness, view.Recovery,
	)
	return err
}
