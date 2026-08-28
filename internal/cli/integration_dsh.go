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
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
)

func newSetupDSHCommand(state *commandState) *cobra.Command {
	var source, ref string
	command := &cobra.Command{
		Use: "dsh", Short: "Install the PowerContext DeepSeek Harness plugin and prepare local storage.", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			executable, err := dshExecutable(state.system, runtime.GOOS)
			if err != nil {
				return errors.New("DeepSeek Harness CLI is not installed or is not on PATH")
			}
			dataDirectory, err := prepareDataDirectory()
			if err != nil {
				return err
			}
			pluginPath, err := resolveDSHPlugin(command.Context(), state.system, source, ref, dataDirectory)
			if err != nil {
				return err
			}
			bundle, err := os.Stat(filepath.Join(pluginPath, "lib", "index.js"))
			if err != nil || !bundle.Mode().IsRegular() {
				return errors.New("PowerContext DSH plugin is missing lib/index.js; build the plugin before setup")
			}
			if _, err := state.system.Run(command.Context(), executable, "plugin", "--profile", "web", "add", pluginPath); err != nil {
				return err
			}
			checks := runDSHDiagnostics(command.Context(), state.system)
			if diagnosticsStatus(checks) != "ok" {
				if err := writeDiagnostics(state, checks); err != nil {
					return err
				}
				return alreadyReported(errors.New("DeepSeek Harness diagnostics did not pass"))
			}
			result := map[string]string{"plugin": dshPluginName, "plugin_path": pluginPath, "data_dir": dataDirectory}
			if state.json {
				return writeJSON(state.stdout, result)
			}
			_, err = fmt.Fprintf(state.stdout,
				"PowerContext DeepSeek Harness setup complete.\nPlugin: %s (%s)\nData directory: %s\nNext: run `powercontext server run`, then start `dsh web`.\n",
				dshPluginName, pluginPath, dataDirectory,
			)
			return err
		},
	}
	command.Flags().StringVar(&source, "source", defaultMarketplaceSource, "PowerContext Git source or local checkout path.")
	command.Flags().StringVar(&ref, "ref", defaultMarketplaceRef, "Git ref used for a remote source.")
	return command
}

func runDSHDiagnostics(ctx context.Context, commands systemCommandExecutor) map[string]diagnostic {
	executable, err := dshExecutable(commands, runtime.GOOS)
	if err != nil {
		return map[string]diagnostic{
			"dsh":    {Status: "failed", Detail: "DeepSeek Harness CLI is not installed or is not on PATH"},
			"plugin": {Status: "skipped", Detail: "not checked because DeepSeek Harness CLI is unavailable"},
		}
	}
	output, err := commands.Run(ctx, executable, "--profile", "web", "--dump-config")
	if err != nil {
		return map[string]diagnostic{
			"dsh":    {Status: "failed", Detail: err.Error()},
			"plugin": {Status: "skipped", Detail: "plugin list is unavailable"},
		}
	}
	installed := false
	for _, line := range strings.Split(string(output), "\n") {
		value := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "-"))
		if value == "id: "+dshPluginName || value == `id: "`+dshPluginName+`"` || value == "id: '"+dshPluginName+"'" {
			installed = true
		}
	}
	plugin := diagnostic{Status: "failed", Detail: "PowerContext DSH plugin is not installed"}
	if installed {
		plugin = diagnostic{OK: true, Status: "ok", Detail: dshPluginName + " is installed"}
	}
	return map[string]diagnostic{"dsh": {OK: true, Status: "ok", Detail: executable}, "plugin": plugin}
}

func dshExecutable(commands systemCommandExecutor, goos string) (string, error) {
	if goos == "windows" {
		if executable, err := commands.LookPath("dsh.cmd"); err == nil {
			return executable, nil
		}
	}
	return commands.LookPath("dsh")
}

func resolveDSHPlugin(
	ctx context.Context,
	commands systemCommandExecutor,
	source, ref, dataDirectory string,
) (string, error) {
	root, local, err := normalizeMarketplaceSource(source)
	if err != nil {
		return "", err
	}
	if !local {
		if ref == "" || ref == "." || ref == ".." || strings.ContainsRune(ref, '\x00') {
			return "", errors.New("invalid DeepSeek Harness ref")
		}
		checkoutRoot := filepath.Join(dataDirectory, "checkouts", "dsh")
		target, err := checkoutTarget(checkoutRoot, ref)
		if err != nil {
			return "", errors.New("invalid DeepSeek Harness ref")
		}
		root = target
		if plugin, ok := findDSHPlugin(root); ok {
			return plugin, nil
		}
		if _, err := os.Lstat(root); err == nil {
			if err := os.RemoveAll(root); err != nil {
				return "", errors.New("cannot replace incomplete DSH checkout")
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", errors.New("cannot inspect DSH checkout")
		}
		if err := os.MkdirAll(filepath.Dir(root), 0o755); err != nil {
			return "", errors.New("cannot create DSH checkout directory")
		}
		cloneURL, err := githubCloneURL(source)
		if err != nil {
			return "", err
		}
		if _, err := commands.Run(ctx, "git", "clone", "--depth", "1", "--branch", ref, cloneURL, root); err != nil {
			return "", err
		}
	}
	if plugin, ok := findDSHPlugin(root); ok {
		return plugin, nil
	}
	return "", errors.New("PowerContext DSH plugin was not found under the selected source")
}

func findDSHPlugin(root string) (string, bool) {
	for _, candidate := range []string{root, filepath.Join(root, "integrations", "dsh", "plugins", "powercontext")} {
		payload, err := os.ReadFile(filepath.Join(candidate, "package.json"))
		if err != nil || len(payload) > maximumCommandOutput {
			continue
		}
		var manifest struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(payload, &manifest) == nil && manifest.Name == dshPluginName {
			return candidate, true
		}
	}
	return "", false
}

func checkoutTarget(root, ref string) (string, error) {
	if ref == "" || ref == "." || ref == ".." || strings.ContainsRune(ref, '\x00') || filepath.IsAbs(ref) {
		return "", errors.New("invalid ref")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	target, err := filepath.Abs(filepath.Join(root, ref))
	if err != nil {
		return "", err
	}
	rootResolved, err := resolvePath(root)
	if err != nil {
		return "", err
	}
	targetResolved, err := resolvePath(target)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(rootResolved, targetResolved)
	if err != nil || relative == "." || relative == ".." || filepath.IsAbs(relative) ||
		strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return "", errors.New("ref escapes checkout root")
	}
	return targetResolved, nil
}

// resolvePath mirrors Path.resolve(strict=False): symlinks in the existing
// prefix are resolved while a not-yet-created suffix remains lexical.
func resolvePath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	current := absolute
	var suffix []string
	for {
		resolved, resolveErr := filepath.EvalSymlinks(current)
		if resolveErr == nil {
			for index := len(suffix) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, suffix[index])
			}
			return filepath.Clean(resolved), nil
		}
		if !errors.Is(resolveErr, os.ErrNotExist) {
			return "", resolveErr
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", resolveErr
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
}

func githubCloneURL(source string) (string, error) {
	value, err := githubRepositoryCloneURL(source)
	if err != nil {
		return "", errors.New("invalid DeepSeek Harness source")
	}
	return value, nil
}
