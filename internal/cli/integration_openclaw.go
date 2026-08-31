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
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

const (
	openClawPluginName  = "memory-powercontext"
	openClawPackageName = "@oceanbase/openclaw-memory-powercontext"
	openClawRelative    = "integrations/openclaw/plugins/memory-powercontext"
)

var (
	openClawVersionPattern = regexp.MustCompile(`(?:OpenClaw\s+)?(\d+)\.(\d+)\.(\d+)(?:-beta\.(\d+))?`)
	openClawTools          = []string{
		"powercontext_memory_search",
		"powercontext_memory_get",
		"powercontext_memory_store",
		"powercontext_memory_revise",
		"powercontext_memory_retire",
	}
)

func newSetupOpenClawCommand(state *commandState) *cobra.Command {
	var source, ref, serverURL, scopeMode string
	command := &cobra.Command{
		Use: "openclaw", Short: "Build, install, and configure the PowerContext OpenClaw memory plugin.", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			normalizedURL, err := normalizeOpenClawServerURL(serverURL)
			if err != nil {
				return err
			}
			executable, err := openClawExecutable(state.system, runtime.GOOS)
			if err != nil {
				return errors.New("OpenClaw CLI is not installed or is not on PATH")
			}
			if _, versionErr := supportedOpenClawVersion(command.Context(), state.system, executable); versionErr != nil {
				return versionErr
			}
			if scopeMode != "agent" && scopeMode != "project" {
				return errors.New("OpenClaw scope must be agent or project")
			}
			dataDirectory, err := prepareDataDirectory()
			if err != nil {
				return err
			}
			pluginPath, err := resolveOpenClawPlugin(command.Context(), state.system, source, ref, dataDirectory)
			if err != nil {
				return err
			}
			if buildErr := buildOpenClawPlugin(command.Context(), state.system, pluginPath, runtime.GOOS); buildErr != nil {
				return buildErr
			}
			if _, installErr := state.system.Run(command.Context(), executable, "plugins", "install", "--link", "--force", pluginPath); installErr != nil {
				return installErr
			}
			if configureErr := configureOpenClaw(command.Context(), state.system, executable, normalizedURL, scopeMode); configureErr != nil {
				return configureErr
			}
			result := map[string]string{
				"plugin": openClawPluginName, "plugin_path": pluginPath, "server_url": normalizedURL,
				"scope_mode": scopeMode, "data_dir": dataDirectory,
			}
			if state.json {
				return writeJSON(state.stdout, result)
			}
			_, err = fmt.Fprintf(state.stdout,
				"PowerContext OpenClaw setup complete.\nPlugin: %s\nPlugin path: %s\nServer: %s\nScope: %s\nData directory: %s\nNext: start a new OpenClaw session.\n",
				openClawPluginName, pluginPath, normalizedURL, scopeMode, dataDirectory,
			)
			return err
		},
	}
	command.Flags().StringVar(&source, "source", defaultMarketplaceSource, "OpenClaw plugin Git source or local PowerContext checkout path.")
	command.Flags().StringVar(&ref, "ref", defaultMarketplaceRef, "Git ref used for a remote source.")
	command.Flags().StringVar(&serverURL, "server-url", defaultServerURL, "PowerContext Server base URL configured for the plugin.")
	command.Flags().StringVar(&scopeMode, "scope-mode", "agent", "Memory scope mode: agent or project.")
	return command
}

func newDoctorOpenClawCommand(state *commandState) *cobra.Command {
	return &cobra.Command{
		Use: "openclaw", Short: "Check the optional OpenClaw CLI and PowerContext memory plugin.", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			checks := runOpenClawDiagnostics(command.Context(), state.system)
			if err := writeDiagnostics(state, checks); err != nil {
				return err
			}
			if diagnosticsStatus(checks) != "ok" {
				return alreadyReported(errors.New("OpenClaw diagnostics did not pass"))
			}
			return nil
		},
	}
}

func openClawExecutable(commands systemCommandExecutor, goos string) (string, error) {
	if goos == "windows" {
		if executable, err := commands.LookPath("openclaw.cmd"); err == nil {
			return executable, nil
		}
	}
	return commands.LookPath("openclaw")
}

func pnpmExecutable(commands systemCommandExecutor, goos string) (string, error) {
	if goos == "windows" {
		if executable, err := commands.LookPath("pnpm.cmd"); err == nil {
			return executable, nil
		}
	}
	return commands.LookPath("pnpm")
}

func supportedOpenClawVersion(
	ctx context.Context,
	commands systemCommandExecutor,
	executable string,
) (string, error) {
	output, err := commands.RunTimeout(ctx, 30*time.Second, executable, "--version")
	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(string(output))
	match := openClawVersionPattern.FindStringSubmatch(text)
	if match == nil {
		return "", fmt.Errorf("OpenClaw %s is unsupported; upgrade to >= 2026.8.1-beta.2", versionOrUnknown(text))
	}
	parts := make([]int, 4)
	for index := 0; index < 3; index++ {
		parts[index], _ = strconv.Atoi(match[index+1])
	}
	if match[4] == "" {
		parts[3] = int(^uint(0) >> 1)
	} else {
		parts[3], _ = strconv.Atoi(match[4])
	}
	minimum := []int{2026, 8, 1, 2}
	for index := range parts {
		if parts[index] > minimum[index] {
			return text, nil
		}
		if parts[index] < minimum[index] {
			return "", fmt.Errorf("OpenClaw %s is unsupported; upgrade to >= 2026.8.1-beta.2", versionOrUnknown(text))
		}
	}
	return text, nil
}

func versionOrUnknown(value string) string {
	if value == "" {
		return "version unknown"
	}
	return value
}

func resolveOpenClawPlugin(
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
			return "", errors.New("invalid OpenClaw ref")
		}
		cloneURL, err := githubRepositoryCloneURL(source)
		if err != nil {
			return "", errors.New("invalid OpenClaw source")
		}
		sourceHash := sha256.Sum256([]byte(strings.ToLower(cloneURL)))
		refHash := sha256.Sum256([]byte(ref))
		target := filepath.Join(
			dataDirectory, "checkouts", "openclaw", hex.EncodeToString(sourceHash[:8]), hex.EncodeToString(refHash[:8]), "current",
		)
		root, err = refreshIntegrationCheckout(ctx, commands, cloneURL, ref, target, func(path string) error {
			if _, ok := findOpenClawPlugin(path); !ok {
				return errors.New("PowerContext OpenClaw plugin was not found under the selected source")
			}
			return nil
		})
		if err != nil {
			return "", err
		}
	}
	plugin, ok := findOpenClawPlugin(root)
	if !ok {
		return "", errors.New("PowerContext OpenClaw plugin was not found under the selected source")
	}
	return plugin, nil
}

func findOpenClawPlugin(root string) (string, bool) {
	for _, candidate := range []string{root, filepath.Join(root, filepath.FromSlash(openClawRelative))} {
		payload, err := os.ReadFile(filepath.Join(candidate, "package.json"))
		if err != nil || len(payload) > maximumCommandOutput {
			continue
		}
		var manifest struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(payload, &manifest) == nil && manifest.Name == openClawPackageName {
			resolved, resolveErr := resolvePath(candidate)
			return resolved, resolveErr == nil
		}
	}
	return "", false
}

func buildOpenClawPlugin(
	ctx context.Context,
	commands systemCommandExecutor,
	pluginPath, goos string,
) error {
	executable, err := pnpmExecutable(commands, goos)
	if err != nil {
		return errors.New("pnpm is not installed or is not on PATH; it is required to build the OpenClaw plugin")
	}
	if _, installErr := commands.RunTimeout(ctx, 10*time.Minute, executable, "--dir", pluginPath, "install", "--frozen-lockfile"); installErr != nil {
		return installErr
	}
	if _, buildErr := commands.RunTimeout(ctx, 10*time.Minute, executable, "--dir", pluginPath, "run", "build"); buildErr != nil {
		return buildErr
	}
	info, err := os.Stat(filepath.Join(pluginPath, "dist", "index.js"))
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("PowerContext OpenClaw plugin is missing dist/index.js after build")
	}
	return nil
}

func normalizeOpenClawServerURL(value string) (string, error) {
	trimmed := strings.TrimRight(strings.TrimSpace(value), "/")
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", errors.New("OpenClaw PowerContext Server URL must use HTTP or HTTPS")
	}
	if parsed.User != nil {
		return "", errors.New("OpenClaw PowerContext Server URL must not contain credentials")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("OpenClaw PowerContext Server URL must not contain a query or fragment")
	}
	parsed.Path, parsed.RawPath = strings.TrimRight(parsed.Path, "/"), ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

func configureOpenClaw(
	ctx context.Context,
	commands systemCommandExecutor,
	executable, serverURL, scopeMode string,
) error {
	settings := []map[string]any{
		{"path": "plugins.entries.memory-powercontext.enabled", "value": true},
		{"path": "plugins.entries.memory-powercontext.config.endpoint", "value": serverURL},
		{"path": "plugins.entries.memory-powercontext.config.autoRecall", "value": true},
		{"path": "plugins.entries.memory-powercontext.config.autoCapture", "value": true},
		{"path": "plugins.entries.memory-powercontext.config.scopeMode", "value": scopeMode},
		{"path": "plugins.entries.memory-powercontext.hooks.allowConversationAccess", "value": true},
		{"path": "plugins.slots.memory", "value": openClawPluginName},
	}
	if _, present, err := readOpenClawConfig(ctx, commands, executable, "gateway.mode"); err != nil {
		return err
	} else if !present {
		settings = append([]map[string]any{{"path": "gateway.mode", "value": "local"}}, settings...)
	}
	encodedSettings, _ := json.Marshal(settings)
	if _, err := commands.Run(ctx, executable, "config", "set", "--batch-json", string(encodedSettings)); err != nil {
		return err
	}
	allowlist := make([]any, 0)
	if value, present, err := readOpenClawConfig(ctx, commands, executable, "tools.alsoAllow"); err != nil {
		return err
	} else if present {
		var ok bool
		allowlist, ok = value.([]any)
		if !ok {
			return errors.New("OpenClaw tools.alsoAllow is not an array")
		}
	}
	for _, tool := range openClawTools {
		if !containsOpenClawTool(allowlist, tool) {
			allowlist = append(allowlist, tool)
		}
	}
	encodedTools, _ := json.Marshal(allowlist)
	if _, err := commands.Run(ctx, executable, "config", "set", "tools.alsoAllow", string(encodedTools), "--strict-json"); err != nil {
		return err
	}
	_, err := commands.Run(ctx, executable, "gateway", "restart")
	return err
}

func readOpenClawConfig(
	ctx context.Context,
	commands systemCommandExecutor,
	executable, path string,
) (any, bool, error) {
	output, err := commands.RunTimeout(ctx, time.Minute, executable, "config", "get", path, "--json")
	if err != nil {
		return nil, false, nil
	}
	var value any
	if json.Unmarshal(output, &value) != nil {
		return nil, false, fmt.Errorf("OpenClaw config %s returned invalid JSON", path)
	}
	return value, true, nil
}

func containsOpenClawTool(values []any, tool string) bool {
	for _, value := range values {
		if value == tool {
			return true
		}
	}
	return false
}

func runOpenClawDiagnostics(ctx context.Context, commands systemCommandExecutor) map[string]diagnostic {
	executable, err := openClawExecutable(commands, runtime.GOOS)
	if err != nil {
		return map[string]diagnostic{
			"openclaw": {Status: "failed", Detail: "OpenClaw CLI is not installed or is not on PATH"},
			"plugin":   {Status: "skipped", Detail: "not checked because OpenClaw CLI is unavailable"},
		}
	}
	output, err := commands.Run(ctx, executable, "plugins", "list", "--enabled", "--json")
	if err != nil {
		return map[string]diagnostic{
			"openclaw": {Status: "failed", Detail: err.Error()},
			"plugin":   {Status: "skipped", Detail: "plugin list is unavailable"},
		}
	}
	installed, err := openClawPluginInstalled(output)
	if err != nil {
		return map[string]diagnostic{
			"openclaw": {Status: "failed", Detail: err.Error()},
			"plugin":   {Status: "skipped", Detail: "plugin list is unavailable"},
		}
	}
	pluginCheck := diagnostic{Status: "failed", Detail: "PowerContext OpenClaw plugin is not enabled, loaded, and selected as the memory plugin"}
	if installed {
		pluginCheck = diagnostic{OK: true, Status: "ok", Detail: openClawPluginName + " is installed and active"}
	}
	return map[string]diagnostic{
		"openclaw": {OK: true, Status: "ok", Detail: executable},
		"plugin":   pluginCheck,
	}
}

func openClawPluginInstalled(output []byte) (bool, error) {
	var payload struct {
		Plugins []json.RawMessage `json:"plugins"`
	}
	if json.Unmarshal(output, &payload) != nil || payload.Plugins == nil {
		return false, errors.New("OpenClaw returned an invalid plugin list")
	}
	for _, raw := range payload.Plugins {
		var plugin struct {
			ID                 *string `json:"id"`
			Enabled            *bool   `json:"enabled"`
			Status             *string `json:"status"`
			MemorySlotSelected *bool   `json:"memorySlotSelected"`
		}
		if json.Unmarshal(raw, &plugin) != nil || plugin.ID == nil {
			return false, errors.New("OpenClaw returned an invalid plugin entry")
		}
		if *plugin.ID != openClawPluginName {
			continue
		}
		return plugin.Enabled != nil && *plugin.Enabled && plugin.Status != nil && *plugin.Status == "loaded" &&
			plugin.MemorySlotSelected != nil && *plugin.MemorySlotSelected, nil
	}
	return false, nil
}
