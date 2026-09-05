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

import "github.com/spf13/cobra"

// UnsupportedIntegrationError reports an integration outside the supported
// Codex and WorkBuddy product boundary without rendering command arguments or
// configured host values.
type UnsupportedIntegrationError struct{}

func (*UnsupportedIntegrationError) Error() string {
	return "integration is unsupported; only Codex and WorkBuddy are supported"
}

func unsupportedIntegrationCommand(command *cobra.Command) *cobra.Command {
	command.PreRun = nil
	command.PreRunE = nil
	command.Run = nil
	command.RunE = func(*cobra.Command, []string) error {
		return usageError(&UnsupportedIntegrationError{})
	}
	return command
}
