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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

func newSetupCodexCommand(state *commandState) *cobra.Command {
	var source, ref string
	command := &cobra.Command{
		Use: "codex", Short: "Install the PowerContext Codex plugin and prepare local storage.", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if _, err := state.system.LookPath("codex"); err != nil {
				return errors.New("Codex CLI is not installed or is not on PATH")
			}
			dataDirectory, err := prepareDataDirectory()
			if err != nil {
				return err
			}
			marketplaceSource, local, err := normalizeMarketplaceSource(source)
			if err != nil {
				return err
			}
			arguments := []string{"plugin", "marketplace", "add", marketplaceSource}
			if !local {
				arguments = append(arguments, "--ref", ref)
			}
			marketplace, err := runJSONCommand(command.Context(), state.system, "codex", arguments...)
			if err != nil {
				return err
			}
			marketplaceName, err := requiredJSONText(marketplace, "marketplaceName")
			if err != nil {
				return err
			}
			plugin, err := runJSONCommand(
				command.Context(), state.system, "codex", "plugin", "add", powerContextPlugin+"@"+marketplaceName,
			)
			if err != nil {
				return err
			}
			name, err := requiredJSONText(plugin, "name")
			if err != nil {
				return err
			}
			version, err := requiredJSONText(plugin, "version")
			if err != nil {
				return err
			}
			checks := runCodexDiagnostics(command.Context(), state.system)
			if diagnosticsStatus(checks) != "ok" {
				if err := writeDiagnostics(state, checks); err != nil {
					return err
				}
				return alreadyReported(errors.New("Codex diagnostics did not pass"))
			}
			result := map[string]string{
				"marketplace": marketplaceName, "plugin": name, "plugin_version": version, "data_dir": dataDirectory,
			}
			if state.json {
				return writeJSON(state.stdout, result)
			}
			_, err = fmt.Fprintf(state.stdout,
				"PowerContext Codex setup complete.\nPlugin: %s@%s (%s)\nData directory: %s\nNext: run `powercontext server run`, start a new Codex session, then review `/hooks`.\n",
				name, marketplaceName, version, dataDirectory,
			)
			return err
		},
	}
	command.Flags().StringVar(&source, "source", defaultMarketplaceSource, "Codex marketplace Git source or local path.")
	command.Flags().StringVar(&ref, "ref", defaultMarketplaceRef, "Git ref used for a remote marketplace source.")
	return command
}

func runCodexDiagnostics(ctx context.Context, commands systemCommandExecutor) map[string]diagnostic {
	executable, err := commands.LookPath("codex")
	if err != nil {
		return map[string]diagnostic{
			"codex":  {Status: "failed", Detail: "Codex CLI is not installed or is not on PATH"},
			"plugin": {Status: "skipped", Detail: "not checked because Codex CLI is unavailable"},
		}
	}
	result, err := runJSONCommand(ctx, commands, "codex", "plugin", "list")
	if err != nil {
		return map[string]diagnostic{
			"codex":  {Status: "failed", Detail: err.Error()},
			"plugin": {Status: "skipped", Detail: "plugin list is unavailable"},
		}
	}
	var installed map[string]any
	if values, ok := result["installed"].([]any); ok {
		for _, item := range values {
			entry, ok := item.(map[string]any)
			if ok && entry["name"] == powerContextPlugin && entry["installed"] == true && entry["enabled"] == true {
				installed = entry
				break
			}
		}
	}
	plugin := diagnostic{Status: "failed", Detail: "PowerContext plugin is not installed"}
	if installed != nil {
		pluginID := "None"
		if value, ok := installed["pluginId"].(string); ok {
			pluginID = value
		}
		plugin = diagnostic{OK: true, Status: "ok", Detail: pluginID + " enabled=True"}
	}
	return map[string]diagnostic{"codex": {OK: true, Status: "ok", Detail: executable}, "plugin": plugin}
}

func normalizeMarketplaceSource(source string) (string, bool, error) {
	if strings.HasPrefix(source, ".") || strings.HasPrefix(source, "/") || strings.HasPrefix(source, "~") ||
		(len(source) >= 2 && source[1] == ':') {
		path := source
		if path == "~" || strings.HasPrefix(path, "~/") {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", false, err
			}
			if path == "~" {
				path = home
			} else {
				path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
			}
		}
		absolute, err := resolvePath(path)
		return absolute, true, err
	}
	if _, err := os.Stat(source); err == nil {
		absolute, err := resolvePath(source)
		return absolute, true, err
	}
	return source, false, nil
}
