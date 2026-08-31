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
	"os"

	"github.com/spf13/cobra"

	"github.com/ob-labs/powercontext-go/server"
)

const (
	defaultMarketplaceSource = "ob-labs/powercontext-go"
	defaultMarketplaceRef    = "main"
	powerContextPlugin       = "powercontext"
	dshPluginName            = "powercontext-dsh"
)

func newSetupCommand(state *commandState) *cobra.Command {
	command := &cobra.Command{
		Use: "setup", Short: "Install and configure PowerContext integrations.", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if err := command.Help(); err != nil {
				return err
			}
			return usageError(errors.New("setup requires a subcommand"))
		},
	}
	command.AddCommand(
		newSetupSelectCommand(state),
		newSetupWorkBuddyCommand(state),
		newSetupCodexCommand(state),
		newSetupClaudeCodeCommand(state),
		newSetupDSHCommand(state),
		newSetupPiCommand(state),
		newSetupOpenCodeCommand(state),
		newSetupHermesCommand(state),
		newSetupOpenClawCommand(state),
	)
	return command
}

func prepareDataDirectory() (string, error) {
	directory, err := server.PowerContextDataDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return "", errors.New("cannot create PowerContext data directory")
	}
	return directory, nil
}
