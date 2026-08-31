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
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

type setupSelectOptions struct {
	hosts            []string
	source           string
	ref              string
	serverURL        string
	serverURLSet     bool
	scopeMode        string
	capturePrompts   bool
	noCapturePrompts bool
}

type setupHostRow struct {
	Host   string `json:"host"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

type setupSelectReport struct {
	Hosts []setupHostRow `json:"hosts"`
}

func (r setupSelectReport) failed() bool {
	for _, row := range r.Hosts {
		if row.Status == "failed" {
			return true
		}
	}
	return false
}

func newSetupSelectCommand(state *commandState) *cobra.Command {
	options := setupSelectOptions{capturePrompts: true}
	command := &cobra.Command{
		Use: "select", Short: "Install selected first-class host integrations.", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			options.serverURLSet = command.Flags().Changed("server-url")
			selected, err := resolveSetupSelection(command, state.json, options.hosts)
			if err != nil || selected == nil {
				return err
			}
			if options.noCapturePrompts {
				options.capturePrompts = false
			}
			report := runSetupSelection(command.Context(), state, selected, options)
			if err := writeSetupSelectReport(state, report); err != nil {
				return err
			}
			if report.failed() {
				return alreadyReported(errors.New("one or more selected integrations failed"))
			}
			return nil
		},
	}
	command.Flags().StringArrayVar(&options.hosts, "host", nil, "First-class host to install. Repeatable. Required with --json or a non-TTY.")
	command.Flags().StringVar(&options.source, "source", defaultMarketplaceSource, "Git source or local path passed to each selected installer.")
	command.Flags().StringVar(&options.ref, "ref", defaultMarketplaceRef, "Git ref used for a remote source.")
	command.Flags().StringVar(&options.serverURL, "server-url", "", "PowerContext Server base URL override for Claude Code and OpenClaw.")
	command.Flags().StringVar(&options.scopeMode, "scope-mode", "agent", "OpenClaw memory scope mode: agent or project.")
	command.Flags().BoolVar(&options.capturePrompts, "capture-prompts", true, "Capture Claude Code user prompts as ordinary Source evidence.")
	command.Flags().BoolVar(&options.noCapturePrompts, "no-capture-prompts", false, "Do not capture Claude Code user prompts.")
	command.MarkFlagsMutuallyExclusive("capture-prompts", "no-capture-prompts")
	return command
}

func runSetupSelection(
	ctx context.Context,
	state *commandState,
	selected []string,
	options setupSelectOptions,
) setupSelectReport {
	selectedHosts := make(map[string]bool, len(selected))
	for _, host := range selected {
		selectedHosts[host] = true
	}
	report := setupSelectReport{Hosts: make([]setupHostRow, 0, len(firstClassIntegrationHosts))}
	for _, host := range firstClassIntegrationHosts {
		row := setupHostRow{Host: host.name, Status: "skipped"}
		if selectedHosts[host.name] {
			row.Status = "installed"
			if err := runSelectedSetupHost(ctx, state, host.name, options); err != nil {
				row.Status = "failed"
				row.Error = err.Error()
			}
		}
		report.Hosts = append(report.Hosts, row)
	}
	return report
}

func runSelectedSetupHost(
	ctx context.Context,
	state *commandState,
	host string,
	options setupSelectOptions,
) error {
	isolated := *state
	isolated.stdout = io.Discard
	isolated.stderr = io.Discard
	isolated.json = true

	var command *cobra.Command
	switch host {
	case "codex":
		command = newSetupCodexCommand(&isolated)
	case "claude-code":
		command = newSetupClaudeCodeCommand(&isolated)
	case "dsh":
		command = newSetupDSHCommand(&isolated)
	case "openclaw":
		command = newSetupOpenClawCommand(&isolated)
	case "opencode":
		command = newSetupOpenCodeCommand(&isolated)
	case "pi":
		command = newSetupPiCommand(&isolated)
	case "hermes":
		command = newSetupHermesCommand(&isolated)
	default:
		return fmt.Errorf("unknown host: %s", host)
	}
	arguments := []string{"--source", options.source, "--ref", options.ref}
	if host == "claude-code" {
		if options.serverURLSet {
			arguments = append(arguments, "--server-url", options.serverURL)
		}
		if !options.capturePrompts {
			arguments = append(arguments, "--no-capture-prompts")
		}
	}
	if host == "openclaw" {
		if options.serverURLSet {
			arguments = append(arguments, "--server-url", options.serverURL)
		}
		arguments = append(arguments, "--scope-mode", options.scopeMode)
	}
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	command.SetArgs(arguments)
	if err := command.ExecuteContext(ctx); err != nil {
		return err
	}
	if host == "opencode" {
		checks := runOpenCodeDiagnostics(ctx, state.system)
		if diagnosticsStatus(checks) != "ok" {
			return setupVerificationError(checks)
		}
	}
	return nil
}

func setupVerificationError(checks map[string]diagnostic) error {
	failures := make([]string, 0, len(checks))
	for _, name := range []string{"codex", "claude_code", "dsh", "openclaw", "opencode", "pi", "hermes", "plugin", "package", "skill"} {
		check, ok := checks[name]
		if ok && !check.OK {
			failures = append(failures, name+": "+check.Detail)
		}
	}
	if len(failures) == 0 {
		return errors.New("post-install verification failed")
	}
	return errors.New("post-install verification failed: " + strings.Join(failures, "; "))
}

func writeSetupSelectReport(state *commandState, report setupSelectReport) error {
	if state.json {
		return writeJSON(state.stdout, report)
	}
	installed := false
	hermesInstalled := false
	for _, row := range report.Hosts {
		if row.Status == "failed" {
			if _, err := fmt.Fprintf(state.stdout, "%s: failed - %s\n", row.Host, row.Error); err != nil {
				return err
			}
			continue
		}
		if _, err := fmt.Fprintf(state.stdout, "%s: %s\n", row.Host, row.Status); err != nil {
			return err
		}
		installed = installed || row.Status == "installed"
		hermesInstalled = hermesInstalled || row.Host == "hermes" && row.Status == "installed"
	}
	if installed {
		if _, err := fmt.Fprintln(state.stdout, "Next: run `powercontext server run`, then start a new host session."); err != nil {
			return err
		}
	}
	if hermesInstalled {
		_, err := fmt.Fprintln(state.stdout, "Hermes: run `hermes memory setup` and select PowerContext before starting Hermes.")
		return err
	}
	return nil
}

func resolveSetupSelection(command *cobra.Command, jsonOutput bool, requested []string) ([]string, error) {
	if len(requested) != 0 {
		return normalizeSetupHosts(requested)
	}
	if jsonOutput {
		return nil, errors.New("setup select --json requires --host")
	}
	if !setupInputIsTerminal(command.InOrStdin()) {
		return nil, errors.New("setup select requires --host when stdin is not a TTY")
	}
	if err := writeSetupHostCatalog(command.OutOrStdout()); err != nil {
		return nil, err
	}
	line, err := bufio.NewReader(command.InOrStdin()).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return parseSetupHostSelection(line)
}

func normalizeSetupHosts(requested []string) ([]string, error) {
	selected := make(map[string]bool, len(requested))
	for _, token := range requested {
		name, err := resolveSetupHostToken(strings.TrimSpace(token))
		if err != nil {
			return nil, err
		}
		selected[name] = true
	}
	result := make([]string, 0, len(selected))
	for _, host := range firstClassIntegrationHosts {
		if selected[host.name] {
			result = append(result, host.name)
		}
	}
	return result, nil
}

func parseSetupHostSelection(value string) ([]string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	return normalizeSetupHosts(strings.Split(value, ","))
}

func resolveSetupHostToken(token string) (string, error) {
	for index, host := range firstClassIntegrationHosts {
		if token == host.name || token == fmt.Sprint(index+1) {
			return host.name, nil
		}
	}
	names := make([]string, 0, len(firstClassIntegrationHosts))
	for _, host := range firstClassIntegrationHosts {
		names = append(names, host.name)
	}
	return "", fmt.Errorf("unknown host: %s. Choose from: %s", token, strings.Join(names, ", "))
}

func writeSetupHostCatalog(output io.Writer) error {
	if _, err := fmt.Fprintln(output, "Official first-class integrations:"); err != nil {
		return err
	}
	for index, host := range firstClassIntegrationHosts {
		if _, err := fmt.Fprintf(output, "  %d) %s (%s)\n", index+1, host.label, host.name); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintln(output, "Select hosts by number or name (comma-separated), or press Enter to cancel:")
	return err
}

type setupTerminalInput interface {
	SetupInputIsTerminal() bool
}

func setupInputIsTerminal(input io.Reader) bool {
	if terminal, ok := input.(setupTerminalInput); ok {
		return terminal.SetupInputIsTerminal()
	}
	file, ok := input.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(file.Fd()))
}
