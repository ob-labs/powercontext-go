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

type integrationDiagnosticProbe func(context.Context, systemCommandExecutor) map[string]diagnostic

type integrationHostSpec struct {
	name            string
	label           string
	cliKey          string
	integrationKeys []string
	missingDetail   string
	probe           integrationDiagnosticProbe
}

var firstClassIntegrationHosts = [...]integrationHostSpec{
	{
		name: "codex", label: "Codex", cliKey: "codex", integrationKeys: []string{"plugin"},
		missingDetail: "Codex CLI is not installed or is not on PATH", probe: runCodexDiagnostics,
	},
	{
		name: "workbuddy", label: "WorkBuddy", cliKey: "config",
		integrationKeys: []string{"hooks", "settings", "mcp", "skill"},
		missingDetail:   "PowerContext WorkBuddy configuration is missing",
		probe:           runWorkBuddyIntegrationDiagnostics,
	},
}

func runWorkBuddyIntegrationDiagnostics(context.Context, systemCommandExecutor) map[string]diagnostic {
	home, err := workBuddyHome()
	if err == nil {
		_, statErr := os.Stat(filepath.Join(home, workBuddyConfigFilename))
		if errors.Is(statErr, os.ErrNotExist) {
			return map[string]diagnostic{
				"config":   {Status: "failed", Detail: "PowerContext WorkBuddy configuration is missing"},
				"hooks":    {Status: "skipped", Detail: "not checked because WorkBuddy is not configured"},
				"settings": {Status: "skipped", Detail: "not checked because WorkBuddy is not configured"},
				"mcp":      {Status: "skipped", Detail: "not checked because WorkBuddy is not configured"},
				"skill":    {Status: "skipped", Detail: "not checked because WorkBuddy is not configured"},
			}
		}
	}
	return runWorkBuddyDiagnostics()
}

type integrationCheck struct {
	name       string
	diagnostic diagnostic
}

type integrationHostRow struct {
	host         string
	presence     string
	cliKey       string
	cli          diagnostic
	integrations []integrationCheck
}

func (r integrationHostRow) failed() bool {
	if r.presence == "missing" {
		return false
	}
	if !r.cli.OK {
		return true
	}
	for _, check := range r.integrations {
		if !check.diagnostic.OK {
			return true
		}
	}
	return false
}

type integrationReport struct {
	ok     bool
	status string
	hosts  []integrationHostRow
}

func collectIntegrationDiagnostics(
	ctx context.Context,
	commands systemCommandExecutor,
) integrationReport {
	hosts := make([]integrationHostRow, 0, len(firstClassIntegrationHosts))
	ok := true
	for _, spec := range firstClassIntegrationHosts {
		diagnostics := spec.probe(ctx, commands)
		row := integrationHostRow{
			host: spec.name, cliKey: spec.cliKey, cli: diagnostics[spec.cliKey],
			integrations: make([]integrationCheck, 0, len(spec.integrationKeys)),
		}
		missing := row.cli.Detail == spec.missingDetail
		for _, key := range spec.integrationKeys {
			check := diagnostics[key]
			row.integrations = append(row.integrations, integrationCheck{name: key, diagnostic: check})
			missing = missing && check.Status == "skipped"
		}
		if missing {
			row.presence = "missing"
		} else {
			row.presence = "present"
		}
		if row.failed() {
			ok = false
		}
		hosts = append(hosts, row)
	}
	status := "ok"
	if !ok {
		status = "failed"
	}
	return integrationReport{ok: ok, status: status, hosts: hosts}
}

func newDoctorIntegrationsCommand(state *commandState) *cobra.Command {
	return &cobra.Command{
		Use: "integrations", Short: "Check all first-class host integrations.", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			report := collectIntegrationDiagnostics(command.Context(), state.system)
			if err := writeIntegrationReport(state, report); err != nil {
				return err
			}
			if !report.ok {
				return alreadyReported(errors.New("one or more integration diagnostics did not pass"))
			}
			return nil
		},
	}
}

func writeIntegrationReport(state *commandState, report integrationReport) error {
	if state.json {
		return writeJSON(state.stdout, report.jsonValue())
	}
	for _, row := range report.hosts {
		statuses := make([]string, 0, len(row.integrations)+1)
		primary := "cli"
		if row.cliKey == "config" {
			primary = "config"
		}
		statuses = append(statuses, primary+"="+row.cli.Status)
		for _, check := range row.integrations {
			statuses = append(statuses, check.name+"="+check.diagnostic.Status)
		}
		if _, err := fmt.Fprintf(
			state.stdout, "%s: %s - %s\n", row.host, row.presence, strings.Join(statuses, " "),
		); err != nil {
			return err
		}
	}
	return nil
}

type integrationReportJSON struct {
	OK     bool                 `json:"ok"`
	Status string               `json:"status"`
	Hosts  integrationHostsJSON `json:"hosts"`
}

type integrationHostsJSON struct {
	Codex     map[string]any `json:"codex"`
	WorkBuddy map[string]any `json:"workbuddy"`
}

func (r integrationReport) jsonValue() integrationReportJSON {
	value := integrationReportJSON{OK: r.ok, Status: r.status}
	for _, row := range r.hosts {
		host := row.jsonValue()
		switch row.host {
		case "codex":
			value.Hosts.Codex = host
		case "workbuddy":
			value.Hosts.WorkBuddy = host
		}
	}
	return value
}

func (r integrationHostRow) jsonValue() map[string]any {
	value := map[string]any{"presence": r.presence, r.cliKey: r.cli}
	for _, check := range r.integrations {
		value[check.name] = check.diagnostic
	}
	return value
}
