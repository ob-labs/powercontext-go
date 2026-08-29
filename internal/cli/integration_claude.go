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
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
)

const (
	claudeMarketplaceName = "powercontext"
	claudePluginID        = "powercontext@powercontext"
)

var githubRepositorySlug = regexp.MustCompile(`^[^/\s]+/[^/\s]+$`)

type claudeSetupResult struct {
	Marketplace   string `json:"marketplace"`
	Plugin        string `json:"plugin"`
	PluginVersion string `json:"plugin_version"`
	SettingsFile  string `json:"settings_file"`
	CacheDir      string `json:"cache_dir"`
	DataDir       string `json:"data_dir"`
}

func newSetupClaudeCodeCommand(state *commandState) *cobra.Command {
	var source, ref, serverURL string
	var capturePrompts, noCapturePrompts bool
	command := &cobra.Command{
		Use: "claude-code", Short: "Install the PowerContext Claude Code plugin.", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if noCapturePrompts {
				capturePrompts = false
			}
			plan, err := claudeSetupPlan()
			if err != nil {
				return err
			}
			writeClaudeSetupPlan(state, plan)
			result, err := installClaudeCode(
				command.Context(), state.system, source, ref, serverURL, capturePrompts, plan,
			)
			if err != nil {
				return err
			}
			if state.json {
				return writeJSON(state.stdout, result)
			}
			_, err = fmt.Fprintf(state.stdout,
				"PowerContext Claude Code setup complete.\nPlugin: %s@%s (%s)\nSettings: %s\nNext: run `powercontext server run`, start a new Claude Code session, then review `/hooks` and `/mcp`.\n",
				result.Plugin, result.Marketplace, result.PluginVersion, result.SettingsFile,
			)
			return err
		},
	}
	command.Flags().StringVar(&source, "source", defaultMarketplaceSource, "Claude Code marketplace Git source or local path.")
	command.Flags().StringVar(&ref, "ref", defaultMarketplaceRef, "Git ref used for a remote marketplace source.")
	command.Flags().StringVar(&serverURL, "server-url", defaultServerURL, "PowerContext Server base URL configured for the plugin.")
	command.Flags().BoolVar(&capturePrompts, "capture-prompts", true, "Capture Claude Code user prompts as ordinary Source evidence.")
	command.Flags().BoolVar(&noCapturePrompts, "no-capture-prompts", false, "Do not capture Claude Code user prompts.")
	command.MarkFlagsMutuallyExclusive("capture-prompts", "no-capture-prompts")
	return command
}

func newDoctorClaudeCodeCommand(state *commandState) *cobra.Command {
	return &cobra.Command{
		Use: "claude-code", Short: "Check the optional Claude Code CLI and PowerContext plugin.", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			checks := runClaudeCodeDiagnostics(command.Context(), state.system)
			if err := writeDiagnostics(state, checks); err != nil {
				return err
			}
			if diagnosticsStatus(checks) != "ok" {
				return alreadyReported(errors.New("Claude Code diagnostics did not pass"))
			}
			return nil
		},
	}
}

func installClaudeCode(
	ctx context.Context,
	commands systemCommandExecutor,
	source, ref, serverURL string,
	capturePrompts bool,
	plan claudeSetupResult,
) (claudeSetupResult, error) {
	executable, err := commands.LookPath("claude")
	if err != nil {
		return claudeSetupResult{}, errors.New("Claude Code CLI is not installed or is not on PATH")
	}
	serverURL, err = normalizeClaudeServerURL(serverURL)
	if err != nil {
		return claudeSetupResult{}, err
	}
	marketplaceSource, err := normalizeClaudeMarketplaceSource(source, ref)
	if err != nil {
		return claudeSetupResult{}, err
	}
	marketplaces, err := runAnyJSONCommand(ctx, commands, executable, "plugin", "marketplace", "list")
	if err != nil {
		return claudeSetupResult{}, err
	}
	marketplace := findClaudeMarketplace(marketplaces)
	if marketplace != nil && !claudeMarketplaceMatches(marketplace, marketplaceSource) {
		return claudeSetupResult{}, errors.New(
			"Claude Code marketplace `powercontext` uses a different source; remove it with `claude plugin marketplace remove powercontext`, then rerun setup",
		)
	}
	plugins, err := runAnyJSONCommand(ctx, commands, executable, "plugin", "list")
	if err != nil {
		return claudeSetupResult{}, err
	}
	previousPlugin := findClaudePlugin(plugins)
	snapshot, existed, err := snapshotFile(plan.SettingsFile)
	if err != nil {
		return claudeSetupResult{}, errors.New("cannot read Claude Code settings")
	}
	marketplaceAdded, pluginAdded := false, false
	rollback := func() {
		if pluginAdded {
			_, _ = commands.Run(ctx, executable, "plugin", "uninstall", claudePluginID, "--scope", "user")
		}
		_ = restoreFile(plan.SettingsFile, snapshot, existed)
		if marketplaceAdded {
			_, _ = commands.Run(ctx, executable, "plugin", "marketplace", "remove", claudeMarketplaceName)
		}
	}
	if marketplace == nil {
		if _, runErr := commands.Run(ctx, executable, "plugin", "marketplace", "add", marketplaceSource, "--scope", "user"); runErr != nil {
			return claudeSetupResult{}, runErr
		}
		marketplaceAdded = true
	}
	if _, runErr := commands.Run(ctx, executable, "plugin", "install", claudePluginID, "--scope", "user"); runErr != nil {
		rollback()
		return claudeSetupResult{}, runErr
	}
	pluginAdded = previousPlugin == nil
	installed, err := runAnyJSONCommand(ctx, commands, executable, "plugin", "list")
	if err != nil {
		rollback()
		return claudeSetupResult{}, err
	}
	plugin := findClaudePlugin(installed)
	if plugin == nil || plugin["enabled"] != true {
		rollback()
		return claudeSetupResult{}, errors.New("Claude Code did not report an enabled PowerContext plugin after installation")
	}
	version, ok := plugin["version"].(string)
	if !ok || version == "" {
		rollback()
		return claudeSetupResult{}, errors.New("Claude Code did not return the plugin version")
	}
	if err := configureClaudePlugin(plan.SettingsFile, serverURL, capturePrompts); err != nil {
		rollback()
		return claudeSetupResult{}, err
	}
	plan.Marketplace = claudeMarketplaceName
	plan.Plugin = powerContextPlugin
	plan.PluginVersion = version
	return plan, nil
}

func runClaudeCodeDiagnostics(ctx context.Context, commands systemCommandExecutor) map[string]diagnostic {
	executable, err := commands.LookPath("claude")
	if err != nil {
		return map[string]diagnostic{
			"claude_code": {Status: "failed", Detail: "Claude Code CLI is not installed or is not on PATH"},
			"plugin":      {Status: "skipped", Detail: "not checked because Claude Code CLI is unavailable"},
		}
	}
	value, err := runAnyJSONCommand(ctx, commands, executable, "plugin", "list")
	if err != nil {
		return map[string]diagnostic{
			"claude_code": {Status: "failed", Detail: err.Error()},
			"plugin":      {Status: "skipped", Detail: "plugin list is unavailable"},
		}
	}
	plugin := findClaudePlugin(value)
	check := diagnostic{Status: "failed", Detail: "PowerContext plugin is not installed"}
	if plugin != nil {
		identifier, _ := plugin["id"].(string)
		enabled, _ := plugin["enabled"].(bool)
		if enabled {
			check = diagnostic{OK: true, Status: "ok", Detail: fmt.Sprintf("%s enabled=%t", identifier, enabled)}
		} else {
			check.Detail = fmt.Sprintf("%s enabled=%t", identifier, enabled)
		}
	}
	return map[string]diagnostic{
		"claude_code": {OK: true, Status: "ok", Detail: executable},
		"plugin":      check,
	}
}

func normalizeClaudeMarketplaceSource(source, ref string) (string, error) {
	value, local, err := normalizeMarketplaceSource(source)
	if err != nil {
		return "", err
	}
	if local || ref == "" {
		return value, nil
	}
	if parsed, parseErr := url.Parse(value); parseErr == nil && parsed.IsAbs() && parsed.User != nil {
		return "", errors.New("Claude Code marketplace URL must not contain credentials")
	}
	if githubRepositorySlug.MatchString(value) {
		return value + "@" + ref, nil
	}
	return value + "#" + ref, nil
}

func normalizeClaudeServerURL(value string) (string, error) {
	trimmed := strings.TrimRight(strings.TrimSpace(value), "/")
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", errors.New("PowerContext Server URL must use HTTP or HTTPS")
	}
	if parsed.User != nil {
		return "", errors.New("PowerContext Server URL must not contain credentials")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("PowerContext Server URL must not contain a query or fragment")
	}
	host := strings.ToLower(parsed.Hostname())
	if parsed.Scheme == "http" && host != "localhost" && !net.ParseIP(host).IsLoopback() {
		return "", errors.New("unencrypted PowerContext Server URLs must be loopback addresses")
	}
	path := strings.TrimRight(parsed.Path, "/")
	path = strings.TrimSuffix(path, "/mcp")
	parsed.Path, parsed.RawPath = strings.TrimRight(path, "/"), ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

func claudeSetupPlan() (claudeSetupResult, error) {
	configDirectory := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR"))
	if configDirectory == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return claudeSetupResult{}, err
		}
		configDirectory = filepath.Join(home, ".claude")
	}
	configDirectory, err := resolvePath(configDirectory)
	if err != nil {
		return claudeSetupResult{}, err
	}
	return claudeSetupResult{
		SettingsFile: filepath.Join(configDirectory, "settings.json"),
		CacheDir:     filepath.Join(configDirectory, "plugins", "cache", claudeMarketplaceName, powerContextPlugin, "<version>"),
		DataDir:      filepath.Join(configDirectory, "plugins", "data", powerContextPlugin+"-"+claudeMarketplaceName),
	}, nil
}

func writeClaudeSetupPlan(state *commandState, plan claudeSetupResult) {
	_, _ = fmt.Fprintf(state.stderr,
		"Claude Code setup plan (no changes made yet):\n  Settings entry: %s\n  Plugin cache: %s\n  Plugin data: %s\n  Permissions: read/write access to the Claude Code configuration directory\n  Rollback: claude plugin uninstall %s --scope user\n  Rollback: claude plugin marketplace remove %s\n",
		plan.SettingsFile, plan.CacheDir, plan.DataDir, claudePluginID, claudeMarketplaceName,
	)
}

func findClaudeMarketplace(value any) map[string]any {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	for _, item := range items {
		entry, ok := item.(map[string]any)
		if ok && entry["name"] == claudeMarketplaceName {
			return entry
		}
	}
	return nil
}

func findClaudePlugin(value any) map[string]any {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	for _, item := range items {
		entry, ok := item.(map[string]any)
		if ok && entry["id"] == claudePluginID {
			return entry
		}
	}
	return nil
}

func claudeMarketplaceMatches(existing map[string]any, requested string) bool {
	switch existing["source"] {
	case "directory":
		path, ok := existing["path"].(string)
		if !ok {
			return false
		}
		left, leftErr := resolvePath(path)
		right, rightErr := resolvePath(requested)
		return leftErr == nil && rightErr == nil && pathsEqual(left, right)
	case "github":
		repository, _ := existing["repo"].(string)
		requestedRepository, requestedRef, _ := strings.Cut(requested, "@")
		return strings.EqualFold(repository, requestedRepository) && claudeRefMatches(existing["ref"], requestedRef)
	case "git":
		requestedURL, requestedRef := requested, ""
		if index := strings.LastIndex(requested, "#"); index >= 0 {
			requestedURL, requestedRef = requested[:index], requested[index+1:]
		}
		existingURL, _ := existing["url"].(string)
		return existingURL == requestedURL && claudeRefMatches(existing["ref"], requestedRef)
	default:
		return false
	}
}

func claudeRefMatches(value any, requested string) bool {
	existing, ok := value.(string)
	return !ok || existing == "" || existing == requested
}

func configureClaudePlugin(settingsPath, serverURL string, capturePrompts bool) error {
	settings := make(map[string]any)
	if payload, err := os.ReadFile(settingsPath); err == nil {
		if len(payload) > maximumCommandOutput || json.Unmarshal(payload, &settings) != nil || settings == nil {
			return errors.New("Claude Code settings must contain a JSON object with object-valued plugin options")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("cannot read Claude Code settings")
	}
	pluginConfigs, err := objectField(settings, "pluginConfigs")
	if err != nil {
		return err
	}
	pluginConfig, err := objectField(pluginConfigs, claudePluginID)
	if err != nil {
		return err
	}
	options, err := objectField(pluginConfig, "options")
	if err != nil {
		return err
	}
	options["server_url"] = serverURL
	options["capture_prompts"] = capturePrompts
	payload, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return errors.New("cannot encode Claude Code settings")
	}
	payload = append(payload, '\n')
	if err := writeFileAtomically(settingsPath, payload, 0o600); err != nil {
		return errors.New("cannot update Claude Code settings")
	}
	return nil
}

func objectField(parent map[string]any, name string) (map[string]any, error) {
	value, present := parent[name]
	if !present {
		created := make(map[string]any)
		parent[name] = created
		return created, nil
	}
	result, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("Claude Code settings must contain a JSON object with object-valued plugin options")
	}
	return result, nil
}

func snapshotFile(path string) ([]byte, bool, error) {
	value, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	return value, err == nil, err
}

func restoreFile(path string, value []byte, existed bool) error {
	if !existed {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	return writeFileAtomically(path, value, 0o600)
}

func writeFileAtomically(path string, value []byte, mode os.FileMode) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(path)+"-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(value); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func runAnyJSONCommand(
	ctx context.Context,
	commands systemCommandExecutor,
	executable string,
	arguments ...string,
) (any, error) {
	output, err := commands.Run(ctx, executable, append(arguments, "--json")...)
	if err != nil {
		return nil, err
	}
	if len(output) > maximumCommandOutput {
		return nil, errors.New("external command output exceeded 1 MiB")
	}
	var result any
	if json.Unmarshal(output, &result) != nil || result == nil {
		return nil, errors.New("external command returned invalid JSON")
	}
	return result, nil
}
