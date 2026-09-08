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
	"errors"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
)

// UnsupportedPersonalServiceError reports that this build cannot operate a
// native personal-server lifecycle. Its text contains no caller values.
type UnsupportedPersonalServiceError struct{}

func (*UnsupportedPersonalServiceError) Error() string {
	if runtime.GOOS == "windows" {
		return "personal Server service lifecycle is not available in this build"
	}
	return "personal Server service is supported only on Windows"
}

func newPersonalServiceInstallCommand(state *commandState) *cobra.Command {
	var envFile, dataDir string
	var startOnLogin bool
	command := &cobra.Command{
		Use: "install", Short: "Install the current user's PowerContext Server service.", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if err := personalServiceRequiredPath("env-file", envFile); err != nil {
				return usageError(err)
			}
			if command.Flags().Changed("data-dir") {
				if err := personalServiceOptionalPath("data-dir", dataDir); err != nil {
					return usageError(err)
				}
			}
			return runPersonalServiceInstall(command.Context(), state, envFile, dataDir, startOnLogin)
		},
	}
	command.Flags().StringVar(&envFile, "env-file", "", "Credential-free Server environment file identity.")
	command.Flags().StringVar(&dataDir, "data-dir", "", "Private PowerContext data directory.")
	command.Flags().BoolVar(&startOnLogin, "start-on-login", true, "Start the owned service when the current user signs in.")
	_ = command.MarkFlagRequired("env-file")
	return command
}

func newPersonalServiceStatusCommand(state *commandState) *cobra.Command {
	var dataDir string
	command := &cobra.Command{
		Use: "status", Short: "Inspect the current user's PowerContext Server service.", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if command.Flags().Changed("data-dir") {
				if err := personalServiceOptionalPath("data-dir", dataDir); err != nil {
					return usageError(err)
				}
			}
			return runPersonalServiceStatus(command.Context(), state, dataDir)
		},
	}
	command.Flags().StringVar(&dataDir, "data-dir", "", "Private PowerContext data directory.")
	return command
}

func newPersonalServiceUninstallCommand(state *commandState) *cobra.Command {
	var dataDir string
	command := &cobra.Command{
		Use: "uninstall", Short: "Remove the owned PowerContext Server service.", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if command.Flags().Changed("data-dir") {
				if err := personalServiceOptionalPath("data-dir", dataDir); err != nil {
					return usageError(err)
				}
			}
			return runPersonalServiceUninstall(command.Context(), state, dataDir)
		},
	}
	command.Flags().StringVar(&dataDir, "data-dir", "", "Private PowerContext data directory.")
	return command
}

func newPersonalServiceRunCommand(state *commandState) *cobra.Command {
	var envFile, endpoint, dataDir string
	command := &cobra.Command{
		Use: "_service-run", Hidden: true, Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			for _, value := range []struct{ name, value string }{{"env-file", envFile}, {"endpoint", endpoint}, {"data-dir", dataDir}} {
				if err := personalServiceRequiredPath(value.name, value.value); err != nil {
					return usageError(err)
				}
			}
			return runPersonalServiceChild(command.Context(), state, envFile, endpoint, dataDir)
		},
	}
	command.Flags().StringVar(&envFile, "env-file", "", "Credential-free Server environment file identity.")
	command.Flags().StringVar(&endpoint, "endpoint", "", "Owned loopback Server endpoint.")
	command.Flags().StringVar(&dataDir, "data-dir", "", "Private PowerContext data directory.")
	for _, name := range []string{"env-file", "endpoint", "data-dir"} {
		_ = command.MarkFlagRequired(name)
	}
	return command
}

func personalServiceRequiredPath(name, value string) error {
	if value == "" || strings.TrimSpace(value) != value {
		return errors.New("server: --" + name + " must be a non-empty trimmed value")
	}
	return nil
}

func personalServiceOptionalPath(name, value string) error {
	if value == "" || strings.TrimSpace(value) != value {
		return errors.New("server: --" + name + " must be a non-empty trimmed value")
	}
	return nil
}
