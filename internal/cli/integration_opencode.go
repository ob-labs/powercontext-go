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
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

const (
	openCodePluginName      = "powercontext-opencode"
	openCodeRelative        = "integrations/opencode/plugins/powercontext"
	openCodePluginBundle    = "lib/index.js"
	openCodePluginOwner     = ".powercontext-opencode.json"
	openCodeSkillOwner      = ".powercontext.json"
	openCodeProbePath       = "POWERCONTEXT_OPENCODE_ACTIVATION_PROBE_PATH"
	openCodeProbeNonce      = "POWERCONTEXT_OPENCODE_ACTIVATION_PROBE_NONCE"
	openCodePluginOwnership = "{\n  \"schema\": 1,\n  \"owner\": \"powercontext\",\n  \"integration\": \"opencode-plugin\"\n}\n"
	openCodeProbeTimeout    = 15 * time.Second
)

type (
	openCodeProbeRunner    func(context.Context, []string, map[string]string, time.Duration) error
	openCodeProbeRequester func(context.Context, string) error
)

type openCodeProbeRunnerProvider interface {
	runOpenCodeProbe(context.Context, []string, map[string]string, time.Duration) error
}

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
			pluginTarget := filepath.Join(configDirectory, "plugins", openCodePluginName+".js")
			skillPath := filepath.Join(configDirectory, "skills", "project-context")
			if pluginErr := requireReplaceableOpenCodePlugin(pluginTarget); pluginErr != nil {
				return pluginErr
			}
			if skillErr := requireReplaceableOpenCodeSkill(skillPath); skillErr != nil {
				return skillErr
			}
			if installErr := installOpenCodePlugin(filepath.Join(pluginPath, filepath.FromSlash(openCodePluginBundle)), pluginTarget); installErr != nil {
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

func requireReplaceableOpenCodePlugin(target string) error {
	if _, err := os.Stat(target); err == nil && !ownedOpenCodePlugin(target) {
		return fmt.Errorf("OpenCode plugin path %s already exists and is not owned by PowerContext", displayPath(target))
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.New("cannot inspect OpenCode plugin path")
	}
	return nil
}

func installOpenCodePlugin(source, target string) error {
	return installOpenCodePluginWithRename(source, target, os.Rename)
}

func installOpenCodePluginWithRename(source, target string, rename func(string, string) error) error {
	if err := requireReplaceableOpenCodePlugin(target); err != nil {
		return err
	}
	info, err := os.Stat(source)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maximumCommandOutput {
		return errors.New("cannot read the OpenCode plugin bundle")
	}
	bundle, err := os.ReadFile(source)
	if err != nil {
		return errors.New("cannot read the OpenCode plugin bundle")
	}
	parent := filepath.Dir(target)
	if mkdirErr := os.MkdirAll(parent, 0o755); mkdirErr != nil {
		return errors.New("cannot create the OpenCode plugin directory")
	}
	staging, err := stageOpenCodeFile(parent, filepath.Base(target), bundle, info.Mode().Perm()&0o755)
	if err != nil {
		return errors.New("cannot stage the OpenCode plugin bundle")
	}
	defer func() { _ = os.Remove(staging) }()

	manifest := filepath.Join(parent, openCodePluginOwner)
	if !ownedOpenCodePlugin(target) {
		manifestStaging, stageErr := stageOpenCodeFile(parent, openCodePluginOwner, []byte(openCodePluginOwnership), 0o644)
		if stageErr != nil {
			return errors.New("cannot stage the OpenCode plugin ownership manifest")
		}
		defer func() { _ = os.Remove(manifestStaging) }()
		if removeErr := os.Remove(manifest); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return errors.New("cannot replace the OpenCode plugin ownership manifest")
		}
		if renameErr := rename(manifestStaging, manifest); renameErr != nil {
			return errors.New("cannot publish the OpenCode plugin ownership manifest")
		}
	}

	backup := ""
	if _, err := os.Stat(target); err == nil {
		backupFile, createErr := os.CreateTemp(parent, "."+filepath.Base(target)+"-*.bak")
		if createErr != nil {
			return errors.New("cannot stage the existing OpenCode plugin bundle")
		}
		backup = backupFile.Name()
		if closeErr := backupFile.Close(); closeErr != nil {
			_ = os.Remove(backup)
			return errors.New("cannot stage the existing OpenCode plugin bundle")
		}
		if removeErr := os.Remove(backup); removeErr != nil {
			return errors.New("cannot stage the existing OpenCode plugin bundle")
		}
		if renameErr := rename(target, backup); renameErr != nil {
			return errors.New("cannot preserve the existing OpenCode plugin bundle")
		}
	}
	if renameErr := rename(staging, target); renameErr != nil {
		if backup != "" {
			_ = rename(backup, target)
		}
		return errors.New("cannot activate the OpenCode plugin bundle")
	}
	if backup != "" {
		_ = os.Remove(backup)
	}
	return nil
}

func stageOpenCodeFile(directory, base string, content []byte, mode os.FileMode) (string, error) {
	file, err := os.CreateTemp(directory, "."+base+"-*.tmp")
	if err != nil {
		return "", err
	}
	path := file.Name()
	if mode == 0 {
		mode = 0o644
	}
	if err := file.Chmod(mode); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", err
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
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

func ownedOpenCodePlugin(path string) bool {
	payload, err := os.ReadFile(filepath.Join(filepath.Dir(path), openCodePluginOwner))
	if err != nil || len(payload) > 4096 {
		return false
	}
	var manifest struct {
		Schema      int    `json:"schema"`
		Owner       string `json:"owner"`
		Integration string `json:"integration"`
	}
	return json.Unmarshal(payload, &manifest) == nil && manifest.Schema == 1 &&
		manifest.Owner == "powercontext" && manifest.Integration == "opencode-plugin"
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
	pluginPath := filepath.Join(configDirectory, "plugins", openCodePluginName+".js")
	configured = configured || ownedOpenCodePlugin(pluginPath)
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
	runner := openCodeProbeRunner(runOpenCodeProbeProcess)
	if provider, ok := commands.(openCodeProbeRunnerProvider); ok {
		runner = provider.runOpenCodeProbe
	}
	return probeOpenCodeActivationWithRunner(ctx, commands, executable, runner)
}

func probeOpenCodeActivationWithRunner(
	ctx context.Context,
	commands systemCommandExecutor,
	executable string,
	runner openCodeProbeRunner,
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
	output, err := commands.Run(ctx, executable, "debug", "config")
	if err != nil {
		return false, false, err
	}
	port, err := availableOpenCodeProbePort()
	if err != nil {
		return false, false, errors.New("cannot allocate an OpenCode activation probe port")
	}
	command := []string{executable, "serve", "--hostname", "127.0.0.1", "--port", strconv.Itoa(port)}
	if err := runner(
		ctx,
		command,
		map[string]string{openCodeProbePath: probePath, openCodeProbeNonce: nonce},
		openCodeProbeTimeout,
	); err != nil {
		return false, false, err
	}
	configured := configuredOpenCodePlugin(output)
	content, readErr := os.ReadFile(probePath)
	return configured, readErr == nil && string(content) == nonce, nil
}

func availableOpenCodeProbePort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	address, ok := listener.Addr().(*net.TCPAddr)
	closeErr := listener.Close()
	if !ok {
		return 0, errors.New("OpenCode activation probe did not receive a TCP address")
	}
	if closeErr != nil {
		return 0, closeErr
	}
	return address.Port, nil
}

func runOpenCodeProbeProcess(
	ctx context.Context,
	command []string,
	environment map[string]string,
	timeout time.Duration,
) error {
	return runOpenCodeProbeProcessWithRequest(ctx, command, environment, timeout, requestOpenCodeActivation)
}

func runOpenCodeProbeProcessWithRequest(
	ctx context.Context,
	command []string,
	environment map[string]string,
	timeout time.Duration,
	requester openCodeProbeRequester,
) error {
	if len(command) == 0 || timeout <= 0 {
		return errors.New("invalid OpenCode activation probe command")
	}
	deadlineCause := errors.New("OpenCode activation probe timed out")
	probeContext, cancel := context.WithTimeoutCause(ctx, timeout, deadlineCause)
	defer cancel()
	process := exec.CommandContext(probeContext, command[0], command[1:]...)
	process.Env = os.Environ()
	for name, value := range environment {
		if name == "" || strings.ContainsAny(name, "=\x00") || strings.ContainsRune(value, '\x00') {
			return errors.New("invalid OpenCode activation probe environment")
		}
		process.Env = append(process.Env, name+"="+value)
	}
	process.Stdout = io.Discard
	process.Stderr = io.Discard
	if err := process.Start(); err != nil {
		return errors.New("cannot start the OpenCode activation probe")
	}
	completed := make(chan error, 1)
	go func() { completed <- process.Wait() }()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	probePath := environment[openCodeProbePath]
	nonce := environment[openCodeProbeNonce]
	port, err := openCodeProbeCommandPort(command)
	if err != nil {
		_ = stopOpenCodeProbeProcess(process.Process, completed)
		return err
	}
	endpoint := fmt.Sprintf("http://127.0.0.1:%d/session", port)
	for {
		if content, readErr := os.ReadFile(probePath); readErr == nil && string(content) == nonce {
			return stopOpenCodeProbeProcess(process.Process, completed)
		}
		select {
		case <-completed:
			if cause := context.Cause(probeContext); cause != nil {
				return cause
			}
			return nil
		case <-probeContext.Done():
			_ = stopOpenCodeProbeProcess(process.Process, completed)
			return context.Cause(probeContext)
		case <-ticker.C:
			_ = requester(probeContext, endpoint)
		}
	}
}

func requestOpenCodeActivation(ctx context.Context, endpoint string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return errors.New("cannot create the OpenCode activation request")
	}
	client := &http.Client{Timeout: time.Second}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	_, _ = io.CopyN(io.Discard, response.Body, 1)
	return response.Body.Close()
}

func openCodeProbeCommandPort(command []string) (int, error) {
	for index, argument := range command {
		if argument == "--port" && index+1 < len(command) {
			port, err := strconv.Atoi(command[index+1])
			if err == nil && port > 0 && port <= 65535 {
				return port, nil
			}
			break
		}
	}
	return 0, errors.New("OpenCode activation probe command has an invalid port")
}

func stopOpenCodeProbeProcess(process *os.Process, completed <-chan error) error {
	if err := process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return errors.New("cannot stop the OpenCode activation probe")
	}
	select {
	case <-completed:
		return nil
	case <-time.After(5 * time.Second):
		return errors.New("OpenCode activation probe did not stop")
	}
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
