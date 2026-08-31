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
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/ob-labs/powercontext-go/internal/transportpolicy"
)

const (
	workBuddyHomeEnv               = "WORKBUDDY_HOME"
	workBuddyPluginName            = "powercontext"
	workBuddyRelative              = "integrations/workbuddy/plugins/powercontext"
	workBuddyHookDriver            = "workbuddy_powercontext_hook.py"
	workBuddyScopeResolver         = "powercontext_project_scope.py"
	workBuddySkillName             = "project-context"
	workBuddySkillOwner            = ".powercontext.json"
	workBuddyConfigFilename        = "powercontext.json"
	workBuddyConfigSchema          = 1
	workBuddyPythonPlaceholder     = "${POWERCONTEXT_PYTHON}"
	workBuddyScopePlaceholder      = "${POWERCONTEXT_PROJECT_SCOPE_SCRIPT}"
	workBuddyServerURLTemplate     = "${POWERCONTEXT_WORKBUDDY_SERVER_URL:-http://127.0.0.1:8000}/mcp"
	workBuddyAuthorizationTemplate = "${POWERCONTEXT_WORKBUDDY_AUTHORIZATION:-}"
	workBuddyLegacyMCPURL          = "http://127.0.0.1:8000/mcp"
	workBuddyMCPDescription        = "PowerContext agent memory & handoff MCP server (local service on port 8000)"

	workBuddyDefaultAuthorizationEnvironment = "POWERCONTEXT_WORKBUDDY_AUTHORIZATION"
	workBuddyDefaultRequestTimeoutSeconds    = 1.5
	workBuddyDefaultRequestBudgetSeconds     = 3.0
	workBuddyDefaultPrepareMaxBytes          = 8_000
	workBuddyDefaultSourceMaxBytes           = 16_384
	workBuddyMaximumRequestBudgetSeconds     = 10.0
	workBuddyMaximumPrepareBytes             = 32 * 1024
	workBuddyMaximumSourceBytes              = 64 * 1024
)

var (
	workBuddyAuthorizationEnvironmentPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	workBuddyHookModules                     = [...]string{
		workBuddyHookDriver,
		"workbuddy_settings.py",
		"prepared_context.py",
	}
)

type workBuddySetupResult struct {
	Plugin        string `json:"plugin"`
	PluginPath    string `json:"plugin_path"`
	WorkBuddyHome string `json:"workbuddy_home"`
	HooksDir      string `json:"hooks_dir"`
	DataDir       string `json:"data_dir"`
	ServerURL     string `json:"server_url"`
	ScopeMode     string `json:"scope_mode"`
}

type workBuddyConfiguration struct {
	Schema                   int     `json:"schema"`
	ServerURL                string  `json:"server_url"`
	ScopeMode                string  `json:"scope_mode"`
	AuthorizationEnvironment string  `json:"authorization_environment"`
	RequestTimeoutSeconds    float64 `json:"request_timeout_seconds"`
	RequestBudgetSeconds     float64 `json:"request_budget_seconds"`
	PrepareMaxBytes          int     `json:"prepare_max_bytes"`
	SourceMaxBytes           int     `json:"source_max_bytes"`
}

func newSetupWorkBuddyCommand(state *commandState) *cobra.Command {
	var source, ref, serverURL, scopeMode, authorizationEnvironment string
	command := &cobra.Command{
		Use: "workbuddy", Short: "Install the PowerContext WorkBuddy hooks, MCP server, and Skill.", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			configuration, err := newWorkBuddyConfiguration(serverURL, scopeMode, authorizationEnvironment)
			if err != nil {
				return err
			}
			result, err := installWorkBuddyPlugin(command.Context(), state.system, source, ref, configuration)
			if err != nil {
				return err
			}
			checks := runWorkBuddyDiagnostics()
			if diagnosticsStatus(checks) != "ok" {
				if writeErr := writeDiagnostics(state, checks); writeErr != nil {
					return writeErr
				}
				return alreadyReported(setupVerificationError(checks))
			}
			if state.json {
				return writeJSON(state.stdout, result)
			}
			_, err = fmt.Fprintf(state.stdout,
				"PowerContext WorkBuddy setup complete.\nPlugin: %s (%s)\nWorkBuddy home: %s\nHooks directory: %s\nData directory: %s\nNext: run `powercontext server run`, restart WorkBuddy, then send a prompt.\n",
				result.Plugin, result.PluginPath, result.WorkBuddyHome, result.HooksDir, result.DataDir,
			)
			return err
		},
	}
	command.Flags().StringVar(&source, "source", defaultMarketplaceSource, "PowerContext Git source or local checkout path.")
	command.Flags().StringVar(&ref, "ref", defaultMarketplaceRef, "Git ref used for a remote source.")
	command.Flags().StringVar(&serverURL, "server-url", defaultServerURL, "PowerContext Server base URL configured for WorkBuddy.")
	command.Flags().StringVar(&scopeMode, "scope-mode", "project", "Memory scope mode: agent or project.")
	command.Flags().StringVar(&authorizationEnvironment, "authorization-environment", workBuddyDefaultAuthorizationEnvironment, "Runtime environment variable containing an optional Bearer authorization header.")
	return command
}

func newDoctorWorkBuddyCommand(state *commandState) *cobra.Command {
	return &cobra.Command{
		Use: "workbuddy", Short: "Check the PowerContext WorkBuddy hooks, MCP server, and Skill.", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			checks := runWorkBuddyDiagnostics()
			if configuration, ok := workBuddyConfiguredServerURL(); ok {
				maps.Copy(checks, runServerDiagnostics(command.Context(), state.version.Version, configuration, state.httpClient))
			}
			if writeErr := writeDiagnostics(state, checks); writeErr != nil {
				return writeErr
			}
			if diagnosticsStatus(checks) != "ok" {
				return alreadyReported(errors.New("WorkBuddy diagnostics did not pass"))
			}
			return nil
		},
	}
}

func newWorkBuddyConfiguration(serverURL, scopeMode, authorizationEnvironment string) (workBuddyConfiguration, error) {
	normalizedURL, err := normalizeWorkBuddyServerURL(serverURL)
	if err != nil {
		return workBuddyConfiguration{}, err
	}
	configuration := workBuddyConfiguration{
		Schema:                   workBuddyConfigSchema,
		ServerURL:                normalizedURL,
		ScopeMode:                scopeMode,
		AuthorizationEnvironment: authorizationEnvironment,
		RequestTimeoutSeconds:    workBuddyDefaultRequestTimeoutSeconds,
		RequestBudgetSeconds:     workBuddyDefaultRequestBudgetSeconds,
		PrepareMaxBytes:          workBuddyDefaultPrepareMaxBytes,
		SourceMaxBytes:           workBuddyDefaultSourceMaxBytes,
	}
	if err := validateWorkBuddyConfiguration(configuration); err != nil {
		return workBuddyConfiguration{}, err
	}
	return configuration, nil
}

func normalizeWorkBuddyServerURL(value string) (string, error) {
	parsed, err := url.Parse(strings.TrimRight(strings.TrimSpace(value), "/"))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", errors.New("WorkBuddy PowerContext Server URL must use HTTP or HTTPS")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("WorkBuddy PowerContext Server URL must not contain credentials, a query, or a fragment")
	}
	if transportpolicy.IsPlaintextNonLoopback(parsed) {
		return "", errors.New("unencrypted WorkBuddy PowerContext Server URLs must be loopback addresses")
	}
	parsed.Path, parsed.RawPath = strings.TrimRight(parsed.Path, "/"), ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

func validateWorkBuddyConfiguration(configuration workBuddyConfiguration) error {
	if configuration.Schema != workBuddyConfigSchema {
		return errors.New("invalid WorkBuddy configuration schema")
	}
	if _, err := normalizeWorkBuddyServerURL(configuration.ServerURL); err != nil {
		return err
	}
	if configuration.ScopeMode != "agent" && configuration.ScopeMode != "project" {
		return errors.New("WorkBuddy scope must be agent or project")
	}
	if !workBuddyAuthorizationEnvironmentPattern.MatchString(configuration.AuthorizationEnvironment) {
		return errors.New("invalid WorkBuddy authorization environment name")
	}
	if math.IsNaN(configuration.RequestTimeoutSeconds) || math.IsInf(configuration.RequestTimeoutSeconds, 0) || configuration.RequestTimeoutSeconds <= 0 {
		return errors.New("WorkBuddy request timeout must be positive")
	}
	if math.IsNaN(configuration.RequestBudgetSeconds) || math.IsInf(configuration.RequestBudgetSeconds, 0) || configuration.RequestBudgetSeconds <= 0 ||
		configuration.RequestBudgetSeconds > workBuddyMaximumRequestBudgetSeconds {
		return errors.New("WorkBuddy request budget is invalid")
	}
	if configuration.RequestTimeoutSeconds > configuration.RequestBudgetSeconds {
		return errors.New("WorkBuddy request timeout must not exceed the budget")
	}
	if configuration.PrepareMaxBytes < 1 || configuration.PrepareMaxBytes > workBuddyMaximumPrepareBytes {
		return errors.New("WorkBuddy prepare byte limit is invalid")
	}
	if configuration.SourceMaxBytes < 1 || configuration.SourceMaxBytes > workBuddyMaximumSourceBytes {
		return errors.New("WorkBuddy source byte limit is invalid")
	}
	return nil
}

func installWorkBuddyPlugin(
	ctx context.Context,
	commands systemCommandExecutor,
	source, ref string,
	configuration workBuddyConfiguration,
) (workBuddySetupResult, error) {
	dataDir, err := prepareDataDirectory()
	if err != nil {
		return workBuddySetupResult{}, err
	}
	plugin, err := resolveWorkBuddyPlugin(ctx, commands, source, ref, dataDir)
	if err != nil {
		return workBuddySetupResult{}, err
	}
	home, err := workBuddyHome()
	if err != nil {
		return workBuddySetupResult{}, err
	}
	hooksDir := filepath.Join(home, "hooks")
	skillDir := filepath.Join(home, "skills", workBuddySkillName)
	settingsFile := filepath.Join(home, "settings.json")
	mcpFile := filepath.Join(home, "mcp.json")
	configFile := filepath.Join(home, workBuddyConfigFilename)
	if mkdirErr := os.MkdirAll(home, 0o755); mkdirErr != nil {
		return workBuddySetupResult{}, errors.New("cannot create WorkBuddy home")
	}
	if replaceableErr := requireReplaceableWorkBuddySkill(skillDir); replaceableErr != nil {
		return workBuddySetupResult{}, replaceableErr
	}
	if _, _, configErr := readWorkBuddyConfiguration(configFile); configErr != nil {
		return workBuddySetupResult{}, errors.New("existing WorkBuddy configuration is not owned by PowerContext")
	}
	settingsSnapshot, err := snapshotWorkBuddyFile(settingsFile)
	if err != nil {
		return workBuddySetupResult{}, errors.New("Cannot update WorkBuddy settings")
	}
	mcpSnapshot, err := snapshotWorkBuddyFile(mcpFile)
	if err != nil {
		return workBuddySetupResult{}, errors.New("Cannot update WorkBuddy MCP configuration")
	}
	configSnapshot, err := snapshotWorkBuddyFile(configFile)
	if err != nil {
		return workBuddySetupResult{}, errors.New("cannot snapshot WorkBuddy configuration")
	}
	hooksBackup, err := snapshotWorkBuddyDirectory(hooksDir)
	if err != nil {
		return workBuddySetupResult{}, errors.New("cannot snapshot WorkBuddy hooks")
	}
	skillBackup, err := snapshotWorkBuddyDirectory(skillDir)
	if err != nil {
		removeWorkBuddyPath(hooksBackup)
		return workBuddySetupResult{}, errors.New("cannot snapshot WorkBuddy Skill")
	}
	succeeded := false
	defer func() {
		removeWorkBuddyPath(hooksBackup)
		removeWorkBuddyPath(skillBackup)
		if succeeded {
			return
		}
		_ = restoreWorkBuddyFile(settingsFile, settingsSnapshot)
		_ = restoreWorkBuddyFile(mcpFile, mcpSnapshot)
		_ = restoreWorkBuddyFile(configFile, configSnapshot)
		_ = restoreWorkBuddyDirectory(hooksDir, hooksBackup)
		_ = restoreWorkBuddyDirectory(skillDir, skillBackup)
	}()
	if hooksErr := installWorkBuddyHooks(plugin, hooksDir); hooksErr != nil {
		return workBuddySetupResult{}, hooksErr
	}
	if settingsErr := mergeWorkBuddySettings(settingsFile, hooksDir); settingsErr != nil {
		return workBuddySetupResult{}, settingsErr
	}
	if mcpErr := mergeWorkBuddyMCP(mcpFile, configuration); mcpErr != nil {
		return workBuddySetupResult{}, mcpErr
	}
	if configErr := writeWorkBuddyJSON(configFile, map[string]any{
		"schema":                    configuration.Schema,
		"server_url":                configuration.ServerURL,
		"scope_mode":                configuration.ScopeMode,
		"authorization_environment": configuration.AuthorizationEnvironment,
		"request_timeout_seconds":   configuration.RequestTimeoutSeconds,
		"request_budget_seconds":    configuration.RequestBudgetSeconds,
		"prepare_max_bytes":         configuration.PrepareMaxBytes,
		"source_max_bytes":          configuration.SourceMaxBytes,
	}); configErr != nil {
		return workBuddySetupResult{}, errors.New("cannot write WorkBuddy configuration")
	}
	if skillErr := installWorkBuddySkill(plugin, skillDir, hooksDir); skillErr != nil {
		return workBuddySetupResult{}, skillErr
	}
	succeeded = true
	return workBuddySetupResult{
		Plugin: workBuddyPluginName, PluginPath: plugin, WorkBuddyHome: home, HooksDir: hooksDir, DataDir: dataDir,
		ServerURL: configuration.ServerURL, ScopeMode: configuration.ScopeMode,
	}, nil
}

func resolveWorkBuddyPlugin(
	ctx context.Context,
	commands systemCommandExecutor,
	source, ref, dataDir string,
) (string, error) {
	root, local, err := normalizeMarketplaceSource(source)
	if err != nil {
		return "", err
	}
	if !local {
		if refErr := validateRemoteRef(ref); refErr != nil {
			return "", errors.New("invalid WorkBuddy ref")
		}
		cloneURL, err := githubRepositoryCloneURL(source)
		if err != nil {
			return "", errors.New("invalid WorkBuddy source")
		}
		sourceHash := sha256.Sum256([]byte(strings.ToLower(cloneURL)))
		refHash := sha256.Sum256([]byte(ref))
		target := filepath.Join(
			dataDir, "checkouts", "workbuddy", hex.EncodeToString(sourceHash[:8]), hex.EncodeToString(refHash[:8]), "current",
		)
		root, err = refreshIntegrationCheckout(ctx, commands, cloneURL, ref, target, func(path string) error {
			if _, ok := findWorkBuddyPlugin(path); !ok {
				return errors.New("WorkBuddy plugin was not found under the selected source")
			}
			return nil
		})
		if err != nil {
			return "", err
		}
	}
	plugin, ok := findWorkBuddyPlugin(root)
	if !ok {
		return "", errors.New("WorkBuddy plugin was not found under the selected source")
	}
	return plugin, nil
}

func findWorkBuddyPlugin(root string) (string, bool) {
	for _, candidate := range []string{root, filepath.Join(root, filepath.FromSlash(workBuddyRelative))} {
		valid := true
		for _, name := range workBuddyHookModules {
			info, err := os.Stat(filepath.Join(candidate, "hooks", name))
			if err != nil || !info.Mode().IsRegular() {
				valid = false
				break
			}
		}
		for _, name := range []string{
			filepath.Join("scripts", "project_scope.py"),
			filepath.Join("skills", workBuddySkillName, "SKILL.md"),
		} {
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

func workBuddyHome() (string, error) {
	configured := strings.TrimSpace(os.Getenv(workBuddyHomeEnv))
	if configured == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		configured = filepath.Join(home, ".workbuddy")
	}
	return resolvePath(configured)
}

func installWorkBuddyHooks(plugin, hooksDir string) error {
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return errors.New("cannot create WorkBuddy hooks directory")
	}
	for _, name := range workBuddyHookModules {
		if err := copyWorkBuddyFile(filepath.Join(plugin, "hooks", name), filepath.Join(hooksDir, name)); err != nil {
			return errors.New("cannot install WorkBuddy hooks")
		}
	}
	if err := copyWorkBuddyFile(
		filepath.Join(plugin, "scripts", "project_scope.py"), filepath.Join(hooksDir, workBuddyScopeResolver),
	); err != nil {
		return errors.New("cannot install WorkBuddy scope resolver")
	}
	return nil
}

func mergeWorkBuddySettings(settingsFile, hooksDir string) error {
	settings, err := loadWorkBuddyJSON(settingsFile)
	if err != nil {
		return errors.New("cannot update WorkBuddy settings")
	}
	hooks, ok := settings["hooks"].(map[string]any)
	if !ok {
		if _, exists := settings["hooks"]; exists {
			return errors.New("invalid WorkBuddy settings")
		}
		hooks = make(map[string]any)
		settings["hooks"] = hooks
	}
	matchers, ok := hooks["UserPromptSubmit"].([]any)
	if !ok {
		if _, exists := hooks["UserPromptSubmit"]; exists {
			return errors.New("invalid WorkBuddy settings")
		}
		matchers = make([]any, 0, 1)
	}
	entry := map[string]any{
		"type": "command", "command": workBuddyHookCommand(hooksDir), "timeout": 10, "statusMessage": "Syncing PowerContext",
	}
	updated := false
	for _, matcher := range matchers {
		matcherObject, ok := matcher.(map[string]any)
		if !ok {
			return errors.New("invalid WorkBuddy settings")
		}
		group, ok := matcherObject["hooks"].([]any)
		if !ok {
			if _, exists := matcherObject["hooks"]; exists {
				return errors.New("invalid WorkBuddy settings")
			}
			continue
		}
		for index, existing := range group {
			value, ok := existing.(map[string]any)
			if !ok || !isWorkBuddyHook(value) {
				continue
			}
			for name, item := range entry {
				value[name] = item
			}
			group[index] = value
			updated = true
			break
		}
		if updated {
			break
		}
	}
	if !updated {
		matchers = append(matchers, map[string]any{"hooks": []any{entry}})
	}
	hooks["UserPromptSubmit"] = matchers
	return writeWorkBuddyJSON(settingsFile, settings)
}

func mergeWorkBuddyMCP(mcpFile string, configuration workBuddyConfiguration) error {
	config, err := loadWorkBuddyJSON(mcpFile)
	if err != nil {
		return errors.New("cannot update WorkBuddy MCP configuration")
	}
	servers, ok := config["mcpServers"].(map[string]any)
	if !ok {
		if _, exists := config["mcpServers"]; exists {
			return errors.New("invalid WorkBuddy MCP configuration")
		}
		servers = make(map[string]any)
		config["mcpServers"] = servers
	}
	entry := map[string]any{
		"type": "http", "url": configuration.ServerURL + "/mcp",
		"headers":     map[string]any{"Authorization": "${" + configuration.AuthorizationEnvironment + ":-}"},
		"description": workBuddyMCPDescription, "disabled": false,
	}
	if existing, ok := servers[workBuddyPluginName].(map[string]any); ok &&
		!isLegacyWorkBuddyMCP(existing) &&
		!isOwnedWorkBuddyMCP(existing) {
		return errors.New("existing WorkBuddy PowerContext MCP entry is not owned by PowerContext")
	}
	servers[workBuddyPluginName] = entry
	return writeWorkBuddyJSON(mcpFile, config)
}

func installWorkBuddySkill(plugin, target, hooksDir string) error {
	parent := filepath.Dir(target)
	if mkdirErr := os.MkdirAll(parent, 0o755); mkdirErr != nil {
		return errors.New("cannot create WorkBuddy Skill directory")
	}
	staging, err := os.MkdirTemp(parent, ".project-context-")
	if err != nil {
		return errors.New("cannot create WorkBuddy Skill staging directory")
	}
	defer func() { _ = os.RemoveAll(staging) }()
	if copyErr := copyRegularTree(filepath.Join(plugin, "skills", workBuddySkillName), staging); copyErr != nil {
		return errors.New("cannot copy WorkBuddy Skill")
	}
	skillPath := filepath.Join(staging, "SKILL.md")
	content, err := os.ReadFile(skillPath)
	if err != nil {
		return errors.New("cannot read WorkBuddy Skill")
	}
	content = []byte(strings.ReplaceAll(
		strings.ReplaceAll(string(content), workBuddyPythonPlaceholder, workBuddyPythonCommand()),
		workBuddyScopePlaceholder, shellQuoteWorkBuddy(filepath.Join(hooksDir, workBuddyScopeResolver)),
	))
	if writeErr := os.WriteFile(skillPath, content, 0o644); writeErr != nil {
		return errors.New("cannot write WorkBuddy Skill")
	}
	manifest, err := json.MarshalIndent(map[string]any{"schema": 1, "owner": "powercontext", "integration": "workbuddy"}, "", "  ")
	if err != nil {
		return err
	}
	if manifestWriteErr := os.WriteFile(filepath.Join(staging, workBuddySkillOwner), append(manifest, '\n'), 0o644); manifestWriteErr != nil {
		return errors.New("cannot write WorkBuddy Skill ownership manifest")
	}
	backup := ""
	if _, targetStatErr := os.Lstat(target); targetStatErr == nil {
		backup, err = os.MkdirTemp(parent, ".project-context-backup-")
		if err != nil {
			return errors.New("cannot stage WorkBuddy Skill replacement")
		}
		if removeBackupErr := os.Remove(backup); removeBackupErr != nil {
			return removeBackupErr
		}
		if preserveErr := os.Rename(target, backup); preserveErr != nil {
			return errors.New("cannot preserve existing WorkBuddy Skill")
		}
	}
	if activateErr := os.Rename(staging, target); activateErr != nil {
		if backup != "" {
			_ = os.Rename(backup, target)
		}
		return errors.New("cannot activate WorkBuddy Skill")
	}
	if backup != "" {
		_ = os.RemoveAll(backup)
	}
	return nil
}

func runWorkBuddyDiagnostics() map[string]diagnostic {
	home, err := workBuddyHome()
	if err != nil {
		return map[string]diagnostic{
			"config":   {Status: "failed", Detail: "cannot resolve WorkBuddy home"},
			"hooks":    {Status: "failed", Detail: "cannot resolve WorkBuddy home"},
			"settings": {Status: "failed", Detail: "cannot resolve WorkBuddy home"},
			"mcp":      {Status: "failed", Detail: "cannot resolve WorkBuddy home"},
			"skill":    {Status: "failed", Detail: "cannot resolve WorkBuddy home"},
		}
	}
	hooksDir := filepath.Join(home, "hooks")
	configuration, present, configErr := readWorkBuddyConfiguration(filepath.Join(home, workBuddyConfigFilename))
	config := diagnostic{OK: true, Status: "ok", Detail: "credential-free WorkBuddy configuration is valid"}
	if configErr != nil || !present {
		config = diagnostic{Status: "failed", Detail: "PowerContext WorkBuddy configuration is missing or invalid"}
	}
	hooks := diagnostic{OK: true, Status: "ok", Detail: "hooks installed in " + hooksDir}
	for _, name := range workBuddyHookModules {
		if !isRegularWorkBuddyFile(filepath.Join(hooksDir, name)) {
			hooks = diagnostic{Status: "failed", Detail: "PowerContext WorkBuddy hooks are not installed"}
			break
		}
	}
	if !isRegularWorkBuddyFile(filepath.Join(hooksDir, workBuddyScopeResolver)) {
		hooks = diagnostic{Status: "failed", Detail: "PowerContext WorkBuddy hooks are not installed"}
	}
	settings := workBuddySettingsDiagnostic(filepath.Join(home, "settings.json"))
	mcp := diagnostic{Status: "failed", Detail: "PowerContext WorkBuddy MCP server is not registered in mcp.json"}
	if config.OK {
		mcp = workBuddyMCPDiagnostic(filepath.Join(home, "mcp.json"), configuration)
	}
	skill := workBuddySkillDiagnostic(filepath.Join(home, "skills", workBuddySkillName))
	return map[string]diagnostic{"config": config, "hooks": hooks, "settings": settings, "mcp": mcp, "skill": skill}
}

func workBuddySettingsDiagnostic(path string) diagnostic {
	settings, err := loadWorkBuddyJSON(path)
	if err != nil || !settingsHaveWorkBuddyHook(settings) {
		return diagnostic{Status: "failed", Detail: "PowerContext WorkBuddy hook is not registered in settings.json"}
	}
	return diagnostic{OK: true, Status: "ok", Detail: path}
}

func workBuddyConfiguredServerURL() (string, bool) {
	home, err := workBuddyHome()
	if err != nil {
		return "", false
	}
	configuration, present, err := readWorkBuddyConfiguration(filepath.Join(home, workBuddyConfigFilename))
	return configuration.ServerURL, present && err == nil
}

func workBuddyMCPDiagnostic(path string, configuration workBuddyConfiguration) diagnostic {
	config, err := loadWorkBuddyJSON(path)
	if err != nil {
		return diagnostic{Status: "failed", Detail: "PowerContext WorkBuddy MCP server is not registered in mcp.json"}
	}
	servers, ok := config["mcpServers"].(map[string]any)
	if !ok {
		return diagnostic{Status: "failed", Detail: "PowerContext WorkBuddy MCP server is not registered in mcp.json"}
	}
	if _, ok := servers[workBuddyPluginName].(map[string]any); !ok {
		return diagnostic{Status: "failed", Detail: "PowerContext WorkBuddy MCP server is not registered in mcp.json"}
	}
	entry := servers[workBuddyPluginName].(map[string]any)
	if entry["description"] != workBuddyMCPDescription || entry["url"] != configuration.ServerURL+"/mcp" {
		return diagnostic{Status: "failed", Detail: "PowerContext WorkBuddy MCP server does not match its configuration"}
	}
	headers, ok := entry["headers"].(map[string]any)
	if !ok || headers["Authorization"] != "${"+configuration.AuthorizationEnvironment+":-}" {
		return diagnostic{Status: "failed", Detail: "PowerContext WorkBuddy MCP authorization reference does not match its configuration"}
	}
	return diagnostic{OK: true, Status: "ok", Detail: path}
}

func workBuddySkillDiagnostic(path string) diagnostic {
	skill := filepath.Join(path, "SKILL.md")
	if !isRegularWorkBuddyFile(skill) || !ownedWorkBuddySkill(path) {
		return diagnostic{Status: "failed", Detail: "PowerContext WorkBuddy skill is not installed"}
	}
	content, err := os.ReadFile(skill)
	if err != nil || strings.Contains(string(content), workBuddyPythonPlaceholder) || strings.Contains(string(content), workBuddyScopePlaceholder) {
		return diagnostic{Status: "failed", Detail: "PowerContext WorkBuddy skill still contains an unresolved command placeholder"}
	}
	return diagnostic{OK: true, Status: "ok", Detail: path}
}

func requireReplaceableWorkBuddySkill(path string) error {
	if _, err := os.Lstat(path); err == nil && !ownedWorkBuddySkill(path) {
		return errors.New("WorkBuddy Skill is not owned by PowerContext")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.New("cannot inspect WorkBuddy Skill")
	}
	return nil
}

func ownedWorkBuddySkill(path string) bool {
	payload, err := os.ReadFile(filepath.Join(path, workBuddySkillOwner))
	if err != nil || len(payload) > 4096 {
		return false
	}
	var manifest struct {
		Schema      int    `json:"schema"`
		Owner       string `json:"owner"`
		Integration string `json:"integration"`
	}
	return json.Unmarshal(payload, &manifest) == nil && manifest.Schema == 1 &&
		manifest.Owner == "powercontext" && manifest.Integration == "workbuddy"
}

func loadWorkBuddyJSON(path string) (map[string]any, error) {
	payload, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return make(map[string]any), nil
	}
	if err != nil {
		return nil, err
	}
	var value map[string]any
	if parseErr := json.Unmarshal(payload, &value); parseErr != nil || value == nil {
		return nil, errors.New("invalid JSON object")
	}
	return value, nil
}

func readWorkBuddyConfiguration(path string) (workBuddyConfiguration, bool, error) {
	payload, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return workBuddyConfiguration{}, false, nil
	}
	if err != nil {
		return workBuddyConfiguration{}, false, err
	}
	configuration, err := decodeWorkBuddyConfiguration(payload)
	return configuration, true, err
}

func decodeWorkBuddyConfiguration(payload []byte) (workBuddyConfiguration, error) {
	if !utf8.Valid(payload) {
		return workBuddyConfiguration{}, errors.New("invalid WorkBuddy configuration")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return workBuddyConfiguration{}, errors.New("invalid WorkBuddy configuration")
	}
	fields := make(map[string]json.RawMessage, 8)
	for decoder.More() {
		name, err := decoder.Token()
		if err != nil {
			return workBuddyConfiguration{}, errors.New("invalid WorkBuddy configuration")
		}
		key, ok := name.(string)
		if !ok || fields[key] != nil {
			return workBuddyConfiguration{}, errors.New("invalid WorkBuddy configuration")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return workBuddyConfiguration{}, errors.New("invalid WorkBuddy configuration")
		}
		fields[key] = value
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return workBuddyConfiguration{}, errors.New("invalid WorkBuddy configuration")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return workBuddyConfiguration{}, errors.New("invalid WorkBuddy configuration")
	}
	expected := map[string]bool{
		"schema": true, "server_url": true, "scope_mode": true, "authorization_environment": true,
		"request_timeout_seconds": true, "request_budget_seconds": true, "prepare_max_bytes": true, "source_max_bytes": true,
	}
	if len(fields) != len(expected) {
		return workBuddyConfiguration{}, errors.New("invalid WorkBuddy configuration")
	}
	for name := range expected {
		if fields[name] == nil {
			return workBuddyConfiguration{}, errors.New("invalid WorkBuddy configuration")
		}
	}
	var configuration workBuddyConfiguration
	if err := json.Unmarshal(payload, &configuration); err != nil || validateWorkBuddyConfiguration(configuration) != nil {
		return workBuddyConfiguration{}, errors.New("invalid WorkBuddy configuration")
	}
	return configuration, nil
}

func writeWorkBuddyJSON(path string, value map[string]any) error {
	payload, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomically(path, append(payload, '\n'), 0o600)
}

func settingsHaveWorkBuddyHook(settings map[string]any) bool {
	hooks, ok := settings["hooks"].(map[string]any)
	if !ok {
		return false
	}
	matchers, ok := hooks["UserPromptSubmit"].([]any)
	if !ok {
		return false
	}
	for _, matcher := range matchers {
		matcherObject, ok := matcher.(map[string]any)
		if !ok {
			continue
		}
		entries, ok := matcherObject["hooks"].([]any)
		if !ok {
			continue
		}
		for _, entry := range entries {
			value, ok := entry.(map[string]any)
			if ok && isWorkBuddyHook(value) {
				return true
			}
		}
	}
	return false
}

func isWorkBuddyHook(entry map[string]any) bool {
	command, _ := entry["command"].(string)
	return strings.Contains(command, workBuddyHookDriver)
}

func isLegacyWorkBuddyMCP(entry map[string]any) bool {
	url, _ := entry["url"].(string)
	headers, headersOK := entry["headers"].(map[string]any)
	description, _ := entry["description"].(string)
	disabled, disabledOK := entry["disabled"].(bool)
	kind, _ := entry["type"].(string)
	return kind == "http" && url == workBuddyLegacyMCPURL && headersOK && len(headers) == 0 &&
		description == workBuddyMCPDescription && disabledOK && !disabled
}

func isOwnedWorkBuddyMCP(entry map[string]any) bool {
	headers, headersOK := entry["headers"].(map[string]any)
	authorization, _ := headers["Authorization"].(string)
	description, _ := entry["description"].(string)
	kind, _ := entry["type"].(string)
	return kind == "http" && headersOK && authorization == workBuddyAuthorizationTemplate &&
		description == workBuddyMCPDescription
}

func workBuddyHookCommand(hooksDir string) string {
	return workBuddyPythonCommand() + " " + shellQuoteWorkBuddy(filepath.Join(hooksDir, workBuddyHookDriver))
}

func workBuddyPythonCommand() string {
	return "python3"
}

func shellQuoteWorkBuddy(value string) string {
	if runtime.GOOS == "windows" {
		return `"` + strings.ReplaceAll(value, `"`, `\"`) + `"`
	}
	if value != "" && !strings.ContainsAny(value, " \t\n'\"\\$`;&|<>()[]{}*?!") {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

type workBuddyFileSnapshot struct {
	exists bool
	value  []byte
}

func snapshotWorkBuddyFile(path string) (workBuddyFileSnapshot, error) {
	payload, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return workBuddyFileSnapshot{}, nil
	}
	if err != nil {
		return workBuddyFileSnapshot{}, err
	}
	return workBuddyFileSnapshot{exists: true, value: payload}, nil
}

func restoreWorkBuddyFile(path string, snapshot workBuddyFileSnapshot) error {
	if !snapshot.exists {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	return writeFileAtomically(path, snapshot.value, 0o600)
}

func snapshotWorkBuddyDirectory(path string) (string, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("snapshot target is not a directory")
	}
	backup, err := os.MkdirTemp(filepath.Dir(path), ".powercontext-workbuddy-backup-")
	if err != nil {
		return "", err
	}
	if removeBackupErr := os.Remove(backup); removeBackupErr != nil {
		return "", removeBackupErr
	}
	if copyErr := copyRegularTree(path, backup); copyErr != nil {
		return "", copyErr
	}
	return backup, nil
}

func restoreWorkBuddyDirectory(path, backup string) error {
	if err := os.RemoveAll(path); err != nil {
		return err
	}
	if backup == "" {
		return nil
	}
	return copyRegularTree(backup, path)
}

func removeWorkBuddyPath(path string) {
	if path != "" {
		_ = os.RemoveAll(path)
	}
}

func copyWorkBuddyFile(source, target string) error {
	info, err := os.Stat(source)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("WorkBuddy source file is invalid")
	}
	content, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	if mkdirErr := os.MkdirAll(filepath.Dir(target), 0o755); mkdirErr != nil {
		return mkdirErr
	}
	return os.WriteFile(target, content, info.Mode().Perm()&0o755)
}

func isRegularWorkBuddyFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0
}
