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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	v1 "github.com/ob-labs/powercontext-go/api/v1"
)

type diagnostic struct {
	OK     bool              `json:"ok"`
	Status string            `json:"status"`
	Detail string            `json:"detail"`
	Checks map[string]string `json:"checks,omitempty"`
}

func newDoctorCommand(state *commandState) *cobra.Command {
	command := &cobra.Command{
		Use: "doctor", Short: "Check an installed PowerContext environment.", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			serverURL := state.serverURL
			if serverURL == "" && !command.Flags().Changed("server-url") {
				serverURL = defaultServerURL
			}
			checks := runServerDiagnostics(command.Context(), state.version.Version, serverURL, state.httpClient)
			if err := writeDiagnostics(state, checks); err != nil {
				return err
			}
			if diagnosticsStatus(checks) != "ok" {
				return alreadyReported(errors.New("one or more diagnostics did not pass"))
			}
			return nil
		},
	}
	command.AddCommand(
		&cobra.Command{
			Use: "codex", Short: "Check the optional Codex CLI and PowerContext plugin.", Args: cobra.NoArgs,
			RunE: func(command *cobra.Command, _ []string) error {
				checks := runCodexDiagnostics(command.Context(), state.system)
				if err := writeDiagnostics(state, checks); err != nil {
					return err
				}
				if diagnosticsStatus(checks) != "ok" {
					return alreadyReported(errors.New("Codex diagnostics did not pass"))
				}
				return nil
			},
		},
		newDoctorClaudeCodeCommand(state),
		&cobra.Command{
			Use: "dsh", Short: "Check the optional DeepSeek Harness CLI and PowerContext plugin.", Args: cobra.NoArgs,
			RunE: func(command *cobra.Command, _ []string) error {
				checks := runDSHDiagnostics(command.Context(), state.system)
				if err := writeDiagnostics(state, checks); err != nil {
					return err
				}
				if diagnosticsStatus(checks) != "ok" {
					return alreadyReported(errors.New("DeepSeek Harness diagnostics did not pass"))
				}
				return nil
			},
		},
		newDoctorPiCommand(state),
		newDoctorOpenCodeCommand(state),
		newDoctorHermesCommand(state),
		newDoctorOpenClawCommand(state),
	)
	return command
}

func runServerDiagnostics(ctx context.Context, version, serverURL string, baseClient *http.Client) map[string]diagnostic {
	if version == "" {
		version = "devel"
	}
	result := map[string]diagnostic{
		"package": {OK: true, Status: "ok", Detail: "powercontext " + version},
	}
	client := diagnosticHTTPClient(baseClient)
	live := requestDiagnostic(ctx, client, serverURL, "/health/live", false)
	result["server_liveness"] = live
	if !live.OK {
		result["server_readiness"] = diagnostic{Status: "skipped", Detail: "not checked because Server liveness failed"}
		return result
	}
	result["server_readiness"] = requestDiagnostic(ctx, client, serverURL, "/health/ready", true)
	return result
}

func diagnosticHTTPClient(base *http.Client) *http.Client {
	if base == nil {
		base = &http.Client{}
	}
	client := *base
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &client
}

func requestDiagnostic(ctx context.Context, client *http.Client, serverURL, path string, readiness bool) diagnostic {
	parsed, err := url.Parse(serverURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return diagnostic{Status: "failed", Detail: "Server URL must be an HTTP base URL without credentials or query data"}
	}
	requestCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, strings.TrimRight(parsed.String(), "/")+path, nil)
	if err != nil {
		return diagnostic{Status: "failed", Detail: "Server URL must be an HTTP base URL without credentials or query data"}
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "powercontext-doctor")
	response, err := client.Do(request)
	if err != nil {
		return diagnostic{Status: "failed", Detail: "cannot reach " + serverURL}
	}
	defer response.Body.Close()
	if !readiness && response.StatusCode != http.StatusOK {
		return diagnostic{Status: "failed", Detail: fmt.Sprintf("liveness returned HTTP %d", response.StatusCode)}
	}
	if readiness && response.StatusCode != http.StatusOK && response.StatusCode != http.StatusServiceUnavailable {
		return diagnostic{Status: "failed", Detail: fmt.Sprintf("readiness returned HTTP %d", response.StatusCode)}
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, maximumCommandOutput+1))
	if err != nil || len(payload) > maximumCommandOutput {
		if readiness {
			return diagnostic{Status: "failed", Detail: "readiness returned an invalid response"}
		}
		return diagnostic{Status: "failed", Detail: "liveness returned an invalid response"}
	}
	if !readiness {
		var value struct {
			Status *string `json:"status"`
		}
		if decodeDiagnosticJSON(payload, &value) != nil || value.Status == nil {
			return diagnostic{Status: "failed", Detail: "liveness returned an invalid response"}
		}
		status := "failed"
		ok := *value.Status == "ok"
		if ok {
			status = "ok"
		}
		return diagnostic{OK: ok, Status: status, Detail: serverURL + " status=" + *value.Status}
	}
	var value struct {
		Status *v1.ReadinessStatus `json:"status"`
		Checks *map[string]string  `json:"checks"`
	}
	if decodeDiagnosticJSON(payload, &value) != nil || value.Status == nil || value.Checks == nil || value.Status.Validate() != nil {
		return diagnostic{Status: "failed", Detail: "readiness returned an invalid response"}
	}
	status := "failed"
	ok := false
	if response.StatusCode == http.StatusOK && *value.Status == v1.ReadinessStatusReady {
		status, ok = "ok", true
	} else if response.StatusCode == http.StatusOK && *value.Status == v1.ReadinessStatusDegraded {
		status = "degraded"
	}
	checks := make(map[string]string, len(*value.Checks))
	for name, check := range *value.Checks {
		checks[name] = check
	}
	return diagnostic{OK: ok, Status: status, Detail: serverURL + " status=" + string(*value.Status), Checks: checks}
}

func decodeDiagnosticJSON(payload []byte, target any) error {
	if !utf8.Valid(payload) {
		return errors.New("response is not valid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("response contains a trailing JSON value")
		}
		return err
	}
	return nil
}

func writeDiagnostics(state *commandState, values map[string]diagnostic) error {
	if state.json {
		return writeJSON(state.stdout, map[string]any{
			"ok": diagnosticsStatus(values) == "ok", "status": diagnosticsStatus(values), "checks": values,
		})
	}
	order := []string{
		"package", "server_liveness", "server_readiness", "codex", "claude_code", "dsh", "pi", "opencode",
		"hermes", "openclaw", "plugin", "skill", "activation", "version",
	}
	for _, name := range order {
		value, ok := values[name]
		if !ok {
			continue
		}
		if _, err := fmt.Fprintf(state.stdout, "%s: %s - %s\n", strings.ReplaceAll(name, "_", " "), value.Status, value.Detail); err != nil {
			return err
		}
		checks := make([]string, 0, len(value.Checks))
		for check := range value.Checks {
			checks = append(checks, check)
		}
		slices.Sort(checks)
		for _, check := range checks {
			status := value.Checks[check]
			if _, err := fmt.Fprintf(state.stdout, "  %s: %s\n", check, status); err != nil {
				return err
			}
		}
	}
	return nil
}

func diagnosticsStatus(values map[string]diagnostic) string {
	statuses := make(map[string]bool)
	for _, value := range values {
		statuses[value.Status] = true
	}
	for _, status := range []string{"failed", "degraded", "skipped"} {
		if statuses[status] {
			return status
		}
	}
	return "ok"
}
