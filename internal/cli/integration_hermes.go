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
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

const (
	hermesPluginName        = "powercontext"
	hermesCommandPluginName = "powercontext-command"
	hermesRelative          = "integrations/hermes/plugins/powercontext"
	hermesCommandRelative   = "integrations/hermes/plugins/powercontext-command"
)

var (
	hermesVersionPattern = regexp.MustCompile(`Hermes Agent v?(\d+)\.(\d+)\.(\d+)`)
	hermesRename         = os.Rename
)

func newSetupHermesCommand(state *commandState) *cobra.Command {
	var source, ref string
	command := &cobra.Command{
		Use: "hermes", Short: "Install the PowerContext Hermes memory provider.", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			executable, err := state.system.LookPath("hermes")
			if err != nil {
				return errors.New("Hermes CLI is not installed or is not on PATH")
			}
			if _, versionErr := hermesVersion(command.Context(), state.system, executable); versionErr != nil {
				return versionErr
			}
			dataDirectory, err := prepareDataDirectory()
			if err != nil {
				return err
			}
			sourcePlugins, err := resolveHermesPlugins(command.Context(), state.system, source, ref, dataDirectory)
			if err != nil {
				return err
			}
			home, err := hermesHome()
			if err != nil {
				return err
			}
			target := filepath.Join(home, "plugins", hermesPluginName)
			commandTarget := filepath.Join(home, "plugins", hermesCommandPluginName)
			if installErr := installHermesPlugins(command.Context(), state.system, executable, sourcePlugins, map[string]string{hermesPluginName: target, hermesCommandPluginName: commandTarget}); installErr != nil {
				return installErr
			}
			checks := runHermesDiagnostics(command.Context(), state.system)
			if diagnosticsStatus(checks) != "ok" {
				if writeErr := writeDiagnostics(state, checks); writeErr != nil {
					return writeErr
				}
				return alreadyReported(setupVerificationError(checks))
			}
			result := map[string]string{
				"plugin": hermesPluginName, "plugin_path": target, "command_plugin_path": commandTarget, "hermes_home": home, "data_dir": dataDirectory,
			}
			if state.json {
				return writeJSON(state.stdout, result)
			}
			_, err = fmt.Fprintf(state.stdout,
				"PowerContext Hermes setup complete.\nPlugin: %s (%s)\nHermes home: %s\nData directory: %s\nNext: run `hermes memory setup`, select PowerContext, then start Hermes.\n",
				hermesPluginName, target, home, dataDirectory,
			)
			return err
		},
	}
	command.Flags().StringVar(&source, "source", defaultMarketplaceSource, "PowerContext Git source or local checkout path.")
	command.Flags().StringVar(&ref, "ref", defaultMarketplaceRef, "Git ref used for a remote source.")
	return command
}

func resolveHermesPlugins(ctx context.Context, commands systemCommandExecutor, source, ref, dataDirectory string) (map[string]string, error) {
	root, local, err := normalizeMarketplaceSource(source)
	if err != nil {
		return nil, err
	}
	if !local {
		if err := validateRemoteRef(ref); err != nil {
			return nil, errors.New("invalid Hermes ref")
		}
		cloneURL, err := githubRepositoryCloneURL(source)
		if err != nil {
			return nil, errors.New("invalid Hermes source")
		}
		sourceHash, refHash := sha256.Sum256([]byte(strings.ToLower(cloneURL))), sha256.Sum256([]byte(ref))
		target := filepath.Join(dataDirectory, "checkouts", "hermes", hex.EncodeToString(sourceHash[:8]), hex.EncodeToString(refHash[:8]), "current")
		root, err = refreshIntegrationCheckout(ctx, commands, cloneURL, ref, target, func(path string) error {
			_, ok := findHermesPluginPair(path)
			if !ok {
				return errors.New("PowerContext Hermes plugins were not found under the selected source")
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	pair, ok := findHermesPluginPair(root)
	if !ok {
		return nil, errors.New("PowerContext Hermes plugins were not found under the selected source")
	}
	return pair, nil
}

func findHermesPluginPair(root string) (map[string]string, bool) {
	for _, candidate := range []string{root, filepath.Join(root, filepath.FromSlash("integrations/hermes/plugins"))} {
		result := map[string]string{}
		for name := range map[string]string{hermesPluginName: hermesPluginName, hermesCommandPluginName: hermesCommandPluginName} {
			path := filepath.Join(candidate, name)
			if _, ok := findHermesPlugin(path); !ok {
				result = nil
				break
			}
			result[name] = path
		}
		if result != nil {
			return result, true
		}
	}
	return nil, false
}

func newDoctorHermesCommand(state *commandState) *cobra.Command {
	return &cobra.Command{
		Use: "hermes", Short: "Check the optional Hermes CLI and PowerContext memory provider.", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			checks := runHermesDiagnostics(command.Context(), state.system)
			if err := writeDiagnostics(state, checks); err != nil {
				return err
			}
			if diagnosticsStatus(checks) != "ok" {
				return alreadyReported(errors.New("Hermes diagnostics did not pass"))
			}
			return nil
		},
	}
}

func hermesVersion(ctx context.Context, commands systemCommandExecutor, executable string) (string, error) {
	output, err := commands.Run(ctx, executable, "--version")
	if err != nil {
		return "", err
	}
	match := hermesVersionPattern.FindStringSubmatch(string(output))
	if match == nil {
		return "", errors.New("Hermes returned an unrecognized version")
	}
	version := make([]int, 3)
	for index := range version {
		version[index], _ = strconv.Atoi(match[index+1])
	}
	if version[0] < 0 || (version[0] == 0 && (version[1] < 20 || (version[1] == 20 && version[2] < 4))) {
		actual := strings.Join(match[1:], ".")
		return "", fmt.Errorf("Hermes Agent v%s is unsupported; PowerContext requires Hermes Agent v0.20.4 or newer", actual)
	}
	return strings.Join(match[1:], "."), nil
}

func hermesHome() (string, error) {
	configured := strings.TrimSpace(os.Getenv("HERMES_HOME"))
	if configured == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		configured = filepath.Join(home, ".hermes")
	}
	return resolvePath(configured)
}

func resolveHermesPlugin(
	ctx context.Context,
	commands systemCommandExecutor,
	source, ref, dataDirectory string,
) (string, error) {
	root, local, err := normalizeMarketplaceSource(source)
	if err != nil {
		return "", err
	}
	if !local {
		if err := validateRemoteRef(ref); err != nil {
			return "", errors.New("invalid Hermes ref")
		}
		cloneURL, err := githubRepositoryCloneURL(source)
		if err != nil {
			return "", errors.New("invalid Hermes source")
		}
		sourceHash := sha256.Sum256([]byte(strings.ToLower(cloneURL)))
		refHash := sha256.Sum256([]byte(ref))
		target := filepath.Join(
			dataDirectory, "checkouts", "hermes", hex.EncodeToString(sourceHash[:8]), hex.EncodeToString(refHash[:8]), "current",
		)
		root, err = refreshIntegrationCheckout(ctx, commands, cloneURL, ref, target, func(path string) error {
			if _, ok := findHermesPlugin(path); !ok {
				return errors.New("PowerContext Hermes plugin was not found under the selected source")
			}
			return nil
		})
		if err != nil {
			return "", err
		}
	}
	plugin, ok := findHermesPlugin(root)
	if !ok {
		return "", errors.New("PowerContext Hermes plugin was not found under the selected source")
	}
	return plugin, nil
}

func findHermesPlugin(root string) (string, bool) {
	for _, candidate := range []string{root, filepath.Join(root, filepath.FromSlash(hermesRelative))} {
		valid := true
		for _, name := range []string{"__init__.py", "plugin.yaml"} {
			info, err := os.Stat(filepath.Join(candidate, name))
			if err != nil || !info.Mode().IsRegular() {
				valid = false
				break
			}
		}
		if valid {
			resolved, err := resolvePath(candidate)
			return resolved, err == nil
		}
	}
	return "", false
}

func installHermesPlugin(
	ctx context.Context,
	commands systemCommandExecutor,
	executable, source, target string,
) error {
	parent := filepath.Dir(target)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return errors.New("cannot create Hermes plugin directory")
	}
	staging, err := os.MkdirTemp(parent, ".powercontext-")
	if err != nil {
		return errors.New("cannot create Hermes plugin staging directory")
	}
	defer func() { _ = os.RemoveAll(staging) }()
	if err := copyRegularTree(source, staging); err != nil {
		return errors.New("cannot copy the PowerContext Hermes plugin")
	}
	if _, doctorErr := commands.Run(ctx, executable, "plugins", "doctor", "--ci", staging); doctorErr != nil {
		return doctorErr
	}
	backup := ""
	if _, err := os.Lstat(target); err == nil {
		backup, err = os.MkdirTemp(parent, ".powercontext-backup-")
		if err != nil {
			return errors.New("cannot create Hermes plugin backup")
		}
		if removeErr := os.Remove(backup); removeErr != nil {
			return removeErr
		}
		if renameErr := os.Rename(target, backup); renameErr != nil {
			return errors.New("cannot preserve existing Hermes plugin")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("cannot inspect existing Hermes plugin")
	}
	if activateErr := os.Rename(staging, target); activateErr != nil {
		if backup != "" {
			_ = os.Rename(backup, target)
		}
		return errors.New("cannot activate Hermes plugin")
	}
	if backup != "" {
		_ = os.RemoveAll(backup)
	}
	return nil
}

func installHermesPlugins(ctx context.Context, commands systemCommandExecutor, executable string, sources, targets map[string]string) error {
	type stagedPlugin struct{ name, target, staging, backup string }
	plugins := make([]stagedPlugin, 0, 2)
	for _, name := range []string{hermesPluginName, hermesCommandPluginName} {
		target := targets[name]
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return errors.New("cannot create Hermes plugin directory")
		}
		staging, err := os.MkdirTemp(filepath.Dir(target), ".powercontext-")
		if err != nil {
			return errors.New("cannot create Hermes plugin staging directory")
		}
		plugins = append(plugins, stagedPlugin{name: name, target: target, staging: staging})
		if err := copyRegularTree(sources[name], staging); err != nil {
			return errors.New("cannot copy the PowerContext Hermes plugin")
		}
		if _, err := commands.Run(ctx, executable, "plugins", "doctor", "--ci", staging); err != nil {
			return err
		}
	}
	rollback := func() {
		for _, plugin := range plugins {
			_ = os.RemoveAll(plugin.staging)
			if plugin.backup != "" {
				_ = os.RemoveAll(plugin.target)
				_ = hermesRename(plugin.backup, plugin.target)
			}
		}
	}
	for index := range plugins {
		plugin := &plugins[index]
		if _, err := os.Lstat(plugin.target); err == nil {
			backup, err := os.MkdirTemp(filepath.Dir(plugin.target), ".powercontext-backup-")
			if err != nil {
				rollback()
				return errors.New("cannot create Hermes plugin backup")
			}
			if err := os.Remove(backup); err != nil {
				rollback()
				return err
			}
			if err := hermesRename(plugin.target, backup); err != nil {
				rollback()
				return errors.New("cannot preserve existing Hermes plugin")
			}
			plugin.backup = backup
		} else if !errors.Is(err, os.ErrNotExist) {
			rollback()
			return errors.New("cannot inspect existing Hermes plugin")
		}
	}
	for index := range plugins {
		plugin := &plugins[index]
		if err := hermesRename(plugin.staging, plugin.target); err != nil {
			rollback()
			return errors.New("cannot activate Hermes plugin")
		}
		plugin.staging = ""
	}
	if _, err := commands.Run(ctx, executable, "plugins", "enable", hermesCommandPluginName); err != nil {
		rollback()
		return err
	}
	for _, plugin := range plugins {
		if plugin.backup != "" {
			_ = os.RemoveAll(plugin.backup)
		}
	}
	return nil
}

func runHermesDiagnostics(ctx context.Context, commands systemCommandExecutor) map[string]diagnostic {
	executable, err := commands.LookPath("hermes")
	if err != nil {
		return map[string]diagnostic{
			"hermes":         {Status: "failed", Detail: "Hermes CLI is not installed or is not on PATH"},
			"plugin":         {Status: "skipped", Detail: "not checked because Hermes CLI is unavailable"},
			"command_plugin": {Status: "skipped", Detail: "not checked because Hermes CLI is unavailable"},
		}
	}
	version, err := hermesVersion(ctx, commands, executable)
	if err != nil {
		return map[string]diagnostic{
			"hermes":         {Status: "failed", Detail: err.Error()},
			"plugin":         {Status: "skipped", Detail: "not checked because Hermes version validation failed"},
			"command_plugin": {Status: "skipped", Detail: "not checked because Hermes version validation failed"},
		}
	}
	home, err := hermesHome()
	if err != nil {
		return map[string]diagnostic{
			"hermes": {OK: true, Status: "ok", Detail: fmt.Sprintf("%s (Hermes Agent v%s)", executable, version)},
			"plugin": {Status: "failed", Detail: "cannot resolve Hermes home"},
		}
	}
	pluginPath := filepath.Join(home, "plugins", hermesPluginName)
	commandPath := filepath.Join(home, "plugins", hermesCommandPluginName)
	if _, ok := findHermesPlugin(pluginPath); !ok {
		return map[string]diagnostic{
			"hermes":         {OK: true, Status: "ok", Detail: fmt.Sprintf("%s (Hermes Agent v%s)", executable, version)},
			"plugin":         {Status: "failed", Detail: "PowerContext Hermes plugin is not installed"},
			"command_plugin": {Status: "skipped", Detail: "not checked because the provider plugin is unavailable"},
		}
	}
	if _, ok := findHermesPlugin(commandPath); !ok {
		return map[string]diagnostic{
			"hermes":         {OK: true, Status: "ok", Detail: fmt.Sprintf("%s (Hermes Agent v%s)", executable, version)},
			"plugin":         {OK: true, Status: "ok", Detail: "powercontext passed Hermes plugin doctor"},
			"command_plugin": {Status: "failed", Detail: "PowerContext Hermes command plugin is not installed"},
		}
	}
	pluginCheck := diagnostic{OK: true, Status: "ok", Detail: "powercontext passed Hermes plugin doctor"}
	if _, err := commands.Run(ctx, executable, "plugins", "doctor", "--ci", pluginPath); err != nil {
		pluginCheck = diagnostic{Status: "failed", Detail: err.Error()}
		return map[string]diagnostic{
			"hermes":         {OK: true, Status: "ok", Detail: fmt.Sprintf("%s (Hermes Agent v%s)", executable, version)},
			"plugin":         pluginCheck,
			"command_plugin": {Status: "skipped", Detail: "not checked because the provider plugin doctor failed"},
		}
	}
	commandCheck := diagnostic{OK: true, Status: "ok", Detail: "powercontext-command passed Hermes plugin doctor"}
	if _, err := commands.Run(ctx, executable, "plugins", "doctor", "--ci", commandPath); err != nil {
		commandCheck = diagnostic{Status: "failed", Detail: err.Error()}
	}
	return map[string]diagnostic{
		"hermes":         {OK: true, Status: "ok", Detail: fmt.Sprintf("%s (Hermes Agent v%s)", executable, version)},
		"plugin":         pluginCheck,
		"command_plugin": commandCheck,
	}
}
