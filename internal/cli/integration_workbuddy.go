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
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
)

const (
	workBuddyHomeEnv               = "WORKBUDDY_HOME"
	workBuddyPluginName            = "powercontext"
	workBuddyRelative              = "integrations/workbuddy/plugins/powercontext"
	workBuddyHookDriver            = "workbuddy_powercontext_hook.py"
	workBuddyScopeResolver         = "powercontext_project_scope.py"
	workBuddySkillName             = "project-context"
	workBuddySkillOwner            = ".powercontext.json"
	workBuddyPythonPlaceholder     = "${POWERCONTEXT_PYTHON}"
	workBuddyScopePlaceholder      = "${POWERCONTEXT_PROJECT_SCOPE_SCRIPT}"
	workBuddyServerURLTemplate     = "${POWERCONTEXT_WORKBUDDY_SERVER_URL:-http://127.0.0.1:8000}/mcp"
	workBuddyAuthorizationTemplate = "${POWERCONTEXT_WORKBUDDY_AUTHORIZATION:-}"
	workBuddyLegacyMCPURL          = "http://127.0.0.1:8000/mcp"
	workBuddyMCPDescription        = "PowerContext agent memory & handoff MCP server (local service on port 8000)"
)

var workBuddyHookModules = [...]string{
	workBuddyHookDriver,
	"workbuddy_settings.py",
	"prepared_context.py",
}

type workBuddySetupResult struct {
	Plugin        string `json:"plugin"`
	PluginPath    string `json:"plugin_path"`
	WorkBuddyHome string `json:"workbuddy_home"`
	HooksDir      string `json:"hooks_dir"`
	DataDir       string `json:"data_dir"`
}

func newSetupWorkBuddyCommand(state *commandState) *cobra.Command {
	var source, ref string
	command := &cobra.Command{
		Use: "workbuddy", Short: "Install the PowerContext WorkBuddy hooks, MCP server, and Skill.", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			result, err := installWorkBuddyPlugin(command.Context(), state.system, source, ref)
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
	return command
}

func newDoctorWorkBuddyCommand(state *commandState) *cobra.Command {
	return &cobra.Command{
		Use: "workbuddy", Short: "Check the PowerContext WorkBuddy hooks, MCP server, and Skill.", Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			checks := runWorkBuddyDiagnostics()
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

func installWorkBuddyPlugin(
	ctx context.Context,
	commands systemCommandExecutor,
	source, ref string,
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
	if mkdirErr := os.MkdirAll(home, 0o755); mkdirErr != nil {
		return workBuddySetupResult{}, errors.New("cannot create WorkBuddy home")
	}
	if replaceableErr := requireReplaceableWorkBuddySkill(skillDir); replaceableErr != nil {
		return workBuddySetupResult{}, replaceableErr
	}
	settingsSnapshot, err := snapshotWorkBuddyFile(settingsFile)
	if err != nil {
		return workBuddySetupResult{}, errors.New("Cannot update WorkBuddy settings")
	}
	mcpSnapshot, err := snapshotWorkBuddyFile(mcpFile)
	if err != nil {
		return workBuddySetupResult{}, errors.New("Cannot update WorkBuddy MCP configuration")
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
		_ = restoreWorkBuddyDirectory(hooksDir, hooksBackup)
		_ = restoreWorkBuddyDirectory(skillDir, skillBackup)
	}()
	if hooksErr := installWorkBuddyHooks(plugin, hooksDir); hooksErr != nil {
		return workBuddySetupResult{}, hooksErr
	}
	if settingsErr := mergeWorkBuddySettings(settingsFile, hooksDir); settingsErr != nil {
		return workBuddySetupResult{}, settingsErr
	}
	if mcpErr := mergeWorkBuddyMCP(mcpFile); mcpErr != nil {
		return workBuddySetupResult{}, mcpErr
	}
	if skillErr := installWorkBuddySkill(plugin, skillDir, hooksDir); skillErr != nil {
		return workBuddySetupResult{}, skillErr
	}
	succeeded = true
	return workBuddySetupResult{
		Plugin: workBuddyPluginName, PluginPath: plugin, WorkBuddyHome: home, HooksDir: hooksDir, DataDir: dataDir,
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

func mergeWorkBuddyMCP(mcpFile string) error {
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
		"type": "http", "url": workBuddyServerURLTemplate,
		"headers":     map[string]any{"Authorization": workBuddyAuthorizationTemplate},
		"description": workBuddyMCPDescription, "disabled": false,
	}
	if existing, ok := servers[workBuddyPluginName].(map[string]any); ok && !isLegacyWorkBuddyMCP(existing) {
		if url, ok := existing["url"].(string); ok && strings.TrimSpace(url) != "" {
			entry["url"] = url
		}
		if headers, ok := existing["headers"].(map[string]any); ok && len(headers) != 0 {
			entry["headers"] = headers
		}
		for name, value := range existing {
			if _, known := entry[name]; !known {
				entry[name] = value
			}
		}
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
	if _, err := os.Lstat(target); err == nil {
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
			"hooks":    {Status: "failed", Detail: "cannot resolve WorkBuddy home"},
			"settings": {Status: "failed", Detail: "cannot resolve WorkBuddy home"},
			"mcp":      {Status: "failed", Detail: "cannot resolve WorkBuddy home"},
			"skill":    {Status: "failed", Detail: "cannot resolve WorkBuddy home"},
		}
	}
	hooksDir := filepath.Join(home, "hooks")
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
	mcp := workBuddyMCPDiagnostic(filepath.Join(home, "mcp.json"))
	skill := workBuddySkillDiagnostic(filepath.Join(home, "skills", workBuddySkillName))
	return map[string]diagnostic{"hooks": hooks, "settings": settings, "mcp": mcp, "skill": skill}
}

func workBuddySettingsDiagnostic(path string) diagnostic {
	settings, err := loadWorkBuddyJSON(path)
	if err != nil || !settingsHaveWorkBuddyHook(settings) {
		return diagnostic{Status: "failed", Detail: "PowerContext WorkBuddy hook is not registered in settings.json"}
	}
	return diagnostic{OK: true, Status: "ok", Detail: path}
}

func workBuddyMCPDiagnostic(path string) diagnostic {
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
