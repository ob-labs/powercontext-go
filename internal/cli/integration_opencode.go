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
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

const (
	openCodePluginName = "powercontext-opencode"
	openCodeRelative   = "integrations/opencode/plugins/powercontext"
	openCodeSkillOwner = ".powercontext.json"
	openCodeProbePath  = "POWERCONTEXT_OPENCODE_ACTIVATION_PROBE_PATH"
	openCodeProbeNonce = "POWERCONTEXT_OPENCODE_ACTIVATION_PROBE_NONCE"
)

var (
	openCodeVersionPattern = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)`)
	gitCommitPattern       = regexp.MustCompile(`^[0-9a-f]{40,64}$`)
)

func newSetupOpenCodeCommand(state *commandState) *cobra.Command {
	var source, ref string
	command := &cobra.Command{
		Use: "opencode", Short: "Install the PowerContext OpenCode plugin and Skill.", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			executable, err := state.system.LookPath("opencode")
			if err != nil {
				return errors.New("OpenCode CLI is not installed or is not on PATH")
			}
			if _, versionErr := openCodeVersion(command.Context(), state.system, executable); versionErr != nil {
				return versionErr
			}
			dataDirectory, err := prepareDataDirectory()
			if err != nil {
				return err
			}
			pluginPath, err := resolveOpenCodePlugin(command.Context(), state.system, source, ref, dataDirectory)
			if err != nil {
				return err
			}
			if pluginErr := requireCompleteOpenCodePlugin(pluginPath); pluginErr != nil {
				return pluginErr
			}
			configDirectory, err := openCodeConfigDirectory(command.Context(), state.system, executable)
			if err != nil {
				return err
			}
			skillPath := filepath.Join(configDirectory, "skills", "project-context")
			if skillErr := requireReplaceableOpenCodeSkill(skillPath); skillErr != nil {
				return skillErr
			}
			if _, installErr := state.system.Run(command.Context(), executable, "plugin", pluginPath, "--global", "--force"); installErr != nil {
				return installErr
			}
			if installErr := installOpenCodeSkill(filepath.Join(pluginPath, "skills", "project-context"), skillPath); installErr != nil {
				return installErr
			}
			result := map[string]string{
				"plugin": openCodePluginName, "plugin_path": pluginPath, "skill_path": skillPath, "data_dir": dataDirectory,
			}
			if state.json {
				return writeJSON(state.stdout, result)
			}
			_, err = fmt.Fprintf(state.stdout,
				"PowerContext OpenCode setup complete.\nPlugin: %s (%s)\nSkill: %s\nData directory: %s\nNext: run `powercontext server run`, then start a new OpenCode session.\n",
				openCodePluginName, pluginPath, skillPath, dataDirectory,
			)
			return err
		},
	}
	command.Flags().StringVar(&source, "source", defaultMarketplaceSource, "PowerContext Git source or local checkout path.")
	command.Flags().StringVar(&ref, "ref", defaultMarketplaceRef, "Git ref used for a remote source.")
	return command
}

func newDoctorOpenCodeCommand(state *commandState) *cobra.Command {
	return &cobra.Command{
		Use: "opencode", Short: "Check the optional OpenCode CLI, plugin, and Skill.", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			checks := runOpenCodeDiagnostics(command.Context(), state.system)
			if err := writeDiagnostics(state, checks); err != nil {
				return err
			}
			if diagnosticsStatus(checks) != "ok" {
				return alreadyReported(errors.New("OpenCode diagnostics did not pass"))
			}
			return nil
		},
	}
}

func openCodeVersion(ctx context.Context, commands systemCommandExecutor, executable string) (string, error) {
	output, err := commands.Run(ctx, executable, "--version")
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(output))
	match := openCodeVersionPattern.FindStringSubmatch(value)
	if match == nil {
		return "", errors.New("OpenCode returned an invalid version")
	}
	version := make([]int, 3)
	for index := range version {
		version[index], _ = strconv.Atoi(match[index+1])
	}
	if version[0] != 1 || version[1] < 18 || (version[1] == 18 && version[2] < 21) {
		return "", fmt.Errorf("OpenCode v%s is unsupported; PowerContext requires OpenCode v1.18.21 or newer in the 1.x line", value)
	}
	return value, nil
}

func resolveOpenCodePlugin(
	ctx context.Context,
	commands systemCommandExecutor,
	source, ref, dataDirectory string,
) (string, error) {
	root, local, err := normalizeMarketplaceSource(source)
	if err != nil {
		return "", err
	}
	if !local {
		root, err = materializeOpenCodeCheckout(ctx, commands, source, ref, dataDirectory)
		if err != nil {
			return "", err
		}
	}
	plugin, ok := findOpenCodePlugin(root)
	if !ok {
		return "", errors.New("PowerContext OpenCode plugin was not found under the selected source")
	}
	return plugin, nil
}

func materializeOpenCodeCheckout(
	ctx context.Context,
	commands systemCommandExecutor,
	source, ref, dataDirectory string,
) (string, error) {
	if err := validateRemoteRef(ref); err != nil {
		return "", errors.New("invalid OpenCode ref")
	}
	cloneURL, err := githubRepositoryCloneURL(source)
	if err != nil {
		return "", errors.New("invalid OpenCode source; use a local path or an HTTPS/SSH GitHub repository")
	}
	identity, err := normalizedGitHubIdentity(cloneURL)
	if err != nil {
		return "", errors.New("invalid OpenCode source; use a local path or an HTTPS/SSH GitHub repository")
	}
	sourceHash := sha256.Sum256([]byte(identity))
	refHash := sha256.Sum256([]byte(ref))
	parent := filepath.Join(
		dataDirectory, "checkouts", "opencode", hex.EncodeToString(sourceHash[:8]), hex.EncodeToString(refHash[:8]),
	)
	if mkdirErr := os.MkdirAll(parent, 0o755); mkdirErr != nil {
		return "", errors.New("cannot create OpenCode checkout directory")
	}
	staging, err := os.MkdirTemp(parent, ".checkout-")
	if err != nil {
		return "", errors.New("cannot create OpenCode checkout staging directory")
	}
	defer func() { _ = os.RemoveAll(staging) }()
	if _, cloneErr := commands.Run(ctx, "git", "clone", "--depth", "1", "--branch", ref, cloneURL, staging); cloneErr != nil {
		return "", errors.New("failed to clone the GitHub source")
	}
	commitOutput, err := commands.Run(ctx, "git", "-C", staging, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return "", errors.New("failed to resolve the OpenCode checkout commit")
	}
	commit := strings.ToLower(strings.TrimSpace(string(commitOutput)))
	if !gitCommitPattern.MatchString(commit) {
		return "", errors.New("failed to resolve the OpenCode checkout commit")
	}
	if _, ok := findOpenCodePlugin(staging); !ok {
		return "", errors.New("PowerContext OpenCode plugin was not found under the selected source")
	}
	target := filepath.Join(parent, commit)
	if _, ok := findOpenCodePlugin(target); ok {
		return target, nil
	}
	if _, statErr := os.Lstat(target); statErr == nil {
		return "", errors.New("OpenCode checkout target exists but is invalid")
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", errors.New("cannot inspect OpenCode checkout target")
	}
	if err := os.Rename(staging, target); err != nil {
		return "", errors.New("cannot activate OpenCode checkout")
	}
	return target, nil
}

func normalizedGitHubIdentity(cloneURL string) (string, error) {
	value := cloneURL
	switch {
	case strings.HasPrefix(value, "git@github.com:"):
		value = strings.TrimPrefix(value, "git@github.com:")
	default:
		parsed, err := url.Parse(value)
		if err != nil || !strings.EqualFold(parsed.Hostname(), "github.com") {
			return "", errors.New("not a GitHub URL")
		}
		value = strings.TrimPrefix(parsed.Path, "/")
	}
	value = strings.TrimSuffix(value, ".git")
	if value == "" {
		return "", errors.New("missing GitHub repository")
	}
	return "github.com/" + strings.ToLower(value), nil
}

func findOpenCodePlugin(root string) (string, bool) {
	for _, candidate := range []string{root, filepath.Join(root, filepath.FromSlash(openCodeRelative))} {
		payload, err := os.ReadFile(filepath.Join(candidate, "package.json"))
		if err != nil || len(payload) > maximumCommandOutput {
			continue
		}
		var manifest struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(payload, &manifest) == nil && manifest.Name == openCodePluginName {
			resolved, resolveErr := resolvePath(candidate)
			return resolved, resolveErr == nil
		}
	}
	return "", false
}

func requireCompleteOpenCodePlugin(path string) error {
	for _, relative := range []string{"lib/index.js", "skills/project-context/SKILL.md"} {
		info, err := os.Stat(filepath.Join(path, filepath.FromSlash(relative)))
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("PowerContext OpenCode plugin %s is missing lib/index.js or project-context Skill", displayPath(path))
		}
	}
	return nil
}

func openCodeConfigDirectory(
	ctx context.Context,
	commands systemCommandExecutor,
	executable string,
) (string, error) {
	output, err := commands.Run(ctx, executable, "debug", "paths")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "config" {
			return resolvePath(strings.Join(fields[1:], " "))
		}
	}
	return "", errors.New("OpenCode did not return a config path")
}

func requireReplaceableOpenCodeSkill(target string) error {
	if _, err := os.Stat(target); err == nil && !ownedOpenCodeSkill(target) {
		return fmt.Errorf("OpenCode Skill path %s already exists and is not owned by PowerContext", displayPath(target))
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.New("cannot inspect OpenCode Skill path")
	}
	return nil
}

func installOpenCodeSkill(source, target string) error {
	if err := requireReplaceableOpenCodeSkill(target); err != nil {
		return err
	}
	parent := filepath.Dir(target)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return errors.New("cannot create OpenCode Skill directory")
	}
	staging, err := os.MkdirTemp(parent, ".project-context-")
	if err != nil {
		return errors.New("cannot create OpenCode Skill staging directory")
	}
	defer func() { _ = os.RemoveAll(staging) }()
	if err := copyRegularTree(source, staging); err != nil {
		return errors.New("cannot copy OpenCode Skill")
	}
	manifest, _ := json.MarshalIndent(map[string]any{"schema": 1, "owner": "powercontext", "integration": "opencode"}, "", "  ")
	manifest = append(manifest, '\n')
	if err := os.WriteFile(filepath.Join(staging, openCodeSkillOwner), manifest, 0o644); err != nil {
		return errors.New("cannot write OpenCode Skill ownership manifest")
	}
	backup := ""
	if _, err := os.Stat(target); err == nil {
		backup, err = os.MkdirTemp(parent, ".project-context-backup-")
		if err != nil {
			return errors.New("cannot stage OpenCode Skill replacement")
		}
		if err := os.Remove(backup); err != nil {
			return err
		}
		if err := os.Rename(target, backup); err != nil {
			return errors.New("cannot preserve existing OpenCode Skill")
		}
	}
	if err := os.Rename(staging, target); err != nil {
		if backup != "" {
			_ = os.Rename(backup, target)
		}
		return errors.New("cannot activate OpenCode Skill")
	}
	if backup != "" {
		_ = os.RemoveAll(backup)
	}
	return nil
}

func copyRegularTree(source, target string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
			return errors.New("copy source escapes its root")
		}
		destination := filepath.Join(target, relative)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("copy source contains a symbolic link")
		}
		if entry.IsDir() {
			return os.MkdirAll(destination, 0o755)
		}
		if !info.Mode().IsRegular() || info.Size() > maximumCommandOutput {
			return errors.New("copy source contains an unsupported file")
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(destination, content, info.Mode().Perm()&0o755)
	})
}

func ownedOpenCodeSkill(path string) bool {
	payload, err := os.ReadFile(filepath.Join(path, openCodeSkillOwner))
	if err != nil || len(payload) > 4096 {
		return false
	}
	var manifest struct {
		Schema      int    `json:"schema"`
		Owner       string `json:"owner"`
		Integration string `json:"integration"`
	}
	return json.Unmarshal(payload, &manifest) == nil && manifest.Schema == 1 &&
		manifest.Owner == "powercontext" && manifest.Integration == "opencode"
}

func runOpenCodeDiagnostics(ctx context.Context, commands systemCommandExecutor) map[string]diagnostic {
	executable, err := commands.LookPath("opencode")
	if err != nil {
		return unavailableOpenCodeDiagnostics("OpenCode CLI is not installed or is not on PATH")
	}
	version, err := openCodeVersion(ctx, commands, executable)
	if err != nil {
		return unavailableOpenCodeDiagnostics(err.Error())
	}
	configDirectory, err := openCodeConfigDirectory(ctx, commands, executable)
	if err != nil {
		return map[string]diagnostic{
			"opencode": {OK: true, Status: "ok", Detail: fmt.Sprintf("%s (%s)", executable, version)},
			"plugin":   {Status: "failed", Detail: err.Error()},
			"skill":    {Status: "skipped", Detail: "not checked because config is unavailable"},
		}
	}
	configured, activated, err := probeOpenCodeActivation(ctx, commands, executable)
	pluginCheck := diagnostic{Status: "failed", Detail: "PowerContext OpenCode plugin is not configured"}
	if err != nil {
		pluginCheck.Detail = err.Error()
	} else if configured && activated {
		pluginCheck = diagnostic{OK: true, Status: "ok", Detail: openCodePluginName + " is configured and active"}
	} else if configured {
		pluginCheck.Detail = "PowerContext OpenCode plugin is configured but did not activate"
	}
	skillPath := filepath.Join(configDirectory, "skills", "project-context")
	skillCheck := diagnostic{Status: "failed", Detail: "PowerContext OpenCode Skill is not installed"}
	if ownedOpenCodeSkill(skillPath) {
		if info, statErr := os.Stat(filepath.Join(skillPath, "SKILL.md")); statErr == nil && info.Mode().IsRegular() {
			skillCheck = diagnostic{OK: true, Status: "ok", Detail: skillPath}
		}
	}
	return map[string]diagnostic{
		"opencode": {OK: true, Status: "ok", Detail: fmt.Sprintf("%s (%s)", executable, version)},
		"plugin":   pluginCheck,
		"skill":    skillCheck,
	}
}

func unavailableOpenCodeDiagnostics(detail string) map[string]diagnostic {
	return map[string]diagnostic{
		"opencode": {Status: "failed", Detail: detail},
		"plugin":   {Status: "skipped", Detail: "not checked because OpenCode is unavailable"},
		"skill":    {Status: "skipped", Detail: "not checked because OpenCode is unavailable"},
	}
}

func probeOpenCodeActivation(
	ctx context.Context,
	commands systemCommandExecutor,
	executable string,
) (bool, bool, error) {
	directory, err := os.MkdirTemp("", "powercontext-opencode-probe-")
	if err != nil {
		return false, false, errors.New("cannot create OpenCode activation probe")
	}
	defer func() { _ = os.RemoveAll(directory) }()
	nonce, err := randomHex(16)
	if err != nil {
		return false, false, errors.New("cannot create OpenCode activation nonce")
	}
	probePath := filepath.Join(directory, "active")
	output, err := commands.RunEnv(ctx, map[string]string{openCodeProbePath: probePath, openCodeProbeNonce: nonce}, executable, "debug", "config")
	if err != nil {
		return false, false, err
	}
	configured := configuredOpenCodePlugin(output)
	content, readErr := os.ReadFile(probePath)
	return configured, readErr == nil && string(content) == nonce, nil
}

func configuredOpenCodePlugin(output []byte) bool {
	var value map[string]any
	if json.Unmarshal(output, &value) != nil {
		return false
	}
	plugins, ok := value["plugin"].([]any)
	if !ok {
		return false
	}
	for _, item := range plugins {
		specification := item
		if values, ok := item.([]any); ok && len(values) > 0 {
			specification = values[0]
		}
		text, ok := specification.(string)
		if !ok {
			continue
		}
		path := text
		if parsed, err := url.Parse(text); err == nil && parsed.Scheme == "file" {
			path, _ = url.PathUnescape(parsed.Path)
		}
		if _, ok := findOpenCodePlugin(path); ok {
			return true
		}
		if _, ok := findOpenCodePlugin(filepath.Dir(path)); ok {
			return true
		}
	}
	return false
}

func randomHex(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}
