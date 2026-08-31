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
	"io"
	"math"
	"net/http"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	v1 "github.com/ob-labs/powercontext-go/api/v1"
	pcclient "github.com/ob-labs/powercontext-go/client"
)

const (
	defaultServerURL = "http://127.0.0.1:8000"
	clientURLVar     = "POWERCONTEXT_CLIENT_SERVER_URL"
	clientTokenVar   = "POWERCONTEXT_CLIENT_API_TOKEN"
	clientTimeoutVar = "POWERCONTEXT_CLIENT_TIMEOUT"
)

type VersionInfo struct {
	Version string
	Commit  string
	Date    string
}

// UsageError marks invalid command-line input. It is intentionally separate
// from Server and transport failures so the process can preserve the CLI's
// conventional exit status 2 without inspecting error text.
type UsageError struct{ cause error }

func (e *UsageError) Error() string {
	if e == nil || e.cause == nil {
		return "invalid command-line input"
	}
	return e.cause.Error()
}

func (e *UsageError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func usageError(err error) error {
	if err == nil {
		return nil
	}
	var existing *UsageError
	if errors.As(err, &existing) {
		return err
	}
	return &UsageError{cause: err}
}

// ExitCode maps command errors to stable process exit statuses.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var usage *UsageError
	if errors.As(err, &usage) {
		return 2
	}
	return 1
}

type commandState struct {
	serverURL  string
	timeout    time.Duration
	token      string
	json       bool
	stdout     io.Writer
	stderr     io.Writer
	version    VersionInfo
	httpClient *http.Client
	serverRun  serverCommandRunner
	system     systemCommandExecutor
}

// New constructs the complete local-server and remote-client command tree.
func New(version VersionInfo) *cobra.Command {
	return newCommand(version, os.Stdout, os.Stderr)
}

func newCommand(version VersionInfo, stdout, stderr io.Writer) *cobra.Command {
	return newCommandWithHTTPClient(version, stdout, stderr, nil)
}

func newCommandWithHTTPClient(version VersionInfo, stdout, stderr io.Writer, httpClient *http.Client) *cobra.Command {
	return newCommandWithDependencies(version, stdout, stderr, httpClient, nil)
}

func newCommandWithDependencies(
	version VersionInfo,
	stdout, stderr io.Writer,
	httpClient *http.Client,
	serverRun serverCommandRunner,
) *cobra.Command {
	return newCommandWithAllDependencies(
		version, stdout, stderr, httpClient, serverRun, processCommandExecutor{},
	)
}

func newCommandWithAllDependencies(
	version VersionInfo,
	stdout, stderr io.Writer,
	httpClient *http.Client,
	serverRun serverCommandRunner,
	system systemCommandExecutor,
) *cobra.Command {
	if system == nil {
		system = processCommandExecutor{}
	}
	state := &commandState{
		stdout: stdout, stderr: stderr, version: version,
		httpClient: httpClient, serverRun: serverRun, system: system,
	}
	root := &cobra.Command{
		Use:          "powercontext",
		Short:        "Operate one PowerContext Server and its integrations.",
		SilenceUsage: true, SilenceErrors: true,
		PersistentPreRunE: func(command *cobra.Command, _ []string) error {
			if !needsRemoteClient(command) {
				return nil
			}
			return state.loadClientEnvironment(command)
		},
	}
	root.SetOut(state.stdout)
	root.SetErr(state.stderr)
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return usageError(err) })
	root.PersistentFlags().StringVar(&state.serverURL, "server-url", "", "PowerContext Server base URL used by content commands.")
	root.PersistentFlags().Var(durationSecondsValue{target: &state.timeout}, "timeout", "HTTP timeout in seconds (Go duration syntax is also accepted).")
	root.PersistentFlags().BoolVar(&state.json, "json", false, "Write content command responses as JSON.")
	root.Version = version.Version
	root.SetVersionTemplate("{{.Version}}\n")

	root.AddCommand(
		newCapabilitiesCommand(state), newStatsCommand(state), newLiveCommand(state), newReadyCommand(state),
		newCandidateCommand(state), newConfigCommand(state), newExperienceCommand(state), newSkillCommand(state), newExternalSkillCommand(state),
		newServerCommand(state), newSetupCommand(state), newDoctorCommand(state),
	)
	return root
}

// reportedError marks a failure whose user-facing diagnostics have already
// been written. The command still exits unsuccessfully, but main must not add
// an unrelated second error line that would corrupt JSON output.
type reportedError struct{ cause error }

func (e *reportedError) Error() string {
	if e == nil || e.cause == nil {
		return "command failed"
	}
	return e.cause.Error()
}

func (e *reportedError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func alreadyReported(err error) error {
	if err == nil {
		return nil
	}
	return &reportedError{cause: err}
}

// ErrorAlreadyReported reports whether a command has already rendered the
// complete failure for the user. It is used by the process entrypoint only.
func ErrorAlreadyReported(err error) bool {
	var reported *reportedError
	return errors.As(err, &reported)
}

func needsRemoteClient(command *cobra.Command) bool {
	for current := command; current != nil; current = current.Parent() {
		switch current.Name() {
		case "server", "setup", "doctor":
			return false
		}
	}
	return command.Name() != "powercontext"
}

func (s *commandState) loadClientEnvironment(command *cobra.Command) error {
	if !command.Flags().Changed("server-url") && s.serverURL == "" {
		configured, present := os.LookupEnv(clientURLVar)
		if present {
			s.serverURL = strings.TrimSpace(configured)
			if s.serverURL == "" {
				return errors.New("invalid POWERCONTEXT_CLIENT_SERVER_URL")
			}
		}
	}
	if s.serverURL == "" {
		if command.Flags().Changed("server-url") {
			return usageError(errors.New("--server-url must not be empty"))
		}
		s.serverURL = defaultServerURL
	}
	if !command.Flags().Changed("timeout") && s.timeout == 0 {
		configured := strings.TrimSpace(os.Getenv(clientTimeoutVar))
		if configured == "" {
			s.timeout = pcclient.DefaultTimeout
		} else {
			value, err := durationFromSeconds(configured)
			if err != nil {
				return errors.New("invalid POWERCONTEXT_CLIENT_TIMEOUT")
			}
			s.timeout = value
		}
	}
	if s.timeout <= 0 {
		return errors.New("timeout must be positive")
	}
	s.token = os.Getenv(clientTokenVar)
	return nil
}

type durationSecondsValue struct{ target *time.Duration }

func (v durationSecondsValue) Set(value string) error {
	duration, err := durationFromSeconds(value)
	if err != nil {
		duration, err = time.ParseDuration(strings.TrimSpace(value))
		if err != nil || duration <= 0 {
			return errors.New("timeout must be a positive number of seconds or Go duration")
		}
	}
	*v.target = duration
	return nil
}

func (v durationSecondsValue) String() string {
	if v.target == nil {
		return "0"
	}
	return strconv.FormatFloat(v.target.Seconds(), 'f', -1, 64)
}

func (durationSecondsValue) Type() string { return "seconds" }

func durationFromSeconds(value string) (time.Duration, error) {
	seconds, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	maximum := float64(math.MaxInt64) / float64(time.Second)
	if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 || seconds > maximum {
		return 0, errors.New("timeout seconds are invalid")
	}
	duration := time.Duration(seconds * float64(time.Second))
	if duration <= 0 {
		return 0, errors.New("timeout seconds are invalid")
	}
	return duration, nil
}

type clientOperation func(context.Context, *pcclient.Client) (any, error)

func (s *commandState) execute(command *cobra.Command, operation clientOperation) error {
	body, err := s.call(command.Context(), operation)
	if err != nil {
		return err
	}
	if s.json {
		return writeJSON(s.stdout, body)
	}
	return writeHuman(s.stdout, body)
}

func (s *commandState) call(ctx context.Context, operation clientOperation) (any, error) {
	client, err := pcclient.New(s.serverURL, pcclient.Options{
		BearerToken: s.token,
		Timeout:     s.timeout,
		HTTPClient:  s.httpClient,
	})
	if err != nil {
		return nil, err
	}
	response, err := operation(ctx, client)
	if err != nil {
		var serverError *pcclient.ServerError
		if errors.As(err, &serverError) {
			return nil, formatServerError(serverError)
		}
		var requestError *pcclient.RequestValidationError
		if errors.As(err, &requestError) {
			return nil, usageError(requestError)
		}
		return nil, err
	}
	if serverError, ok := pcclient.AsServerError(response); ok {
		return nil, formatServerError(serverError)
	}
	body, err := responseBody(response)
	if err != nil {
		return nil, err
	}
	return body, nil
}

func formatServerError(value *pcclient.ServerError) error {
	if value == nil {
		return errors.New("PowerContext Server returned an error")
	}
	message := value.Error()
	if value.RequestID != "" {
		return fmt.Errorf("%s (request ID: %s)", message, value.RequestID)
	}
	return errors.New(message)
}

func responseBody(response any) (any, error) {
	if response == nil {
		return nil, errors.New("PowerContext Server returned an empty response")
	}
	value := reflect.ValueOf(response)
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return nil, errors.New("PowerContext Server returned an empty response")
		}
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return nil, fmt.Errorf("unsupported PowerContext response %T", response)
	}
	field := value.FieldByName("Response")
	if !field.IsValid() || !field.CanInterface() {
		return nil, fmt.Errorf("unsupported PowerContext response %T", response)
	}
	return field.Interface(), nil
}

func writeJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func writeHuman(writer io.Writer, value any) error {
	switch value := value.(type) {
	case v1.Capabilities:
		_, err := fmt.Fprintf(writer,
			"Source types: %s\nArtifact families: %s\nMemory extraction: %s\nExperience generation: %s\nManaged Skill generation: %s\nExternal Skill Registry: %s\nHandoff generation: %s\nSearch modes: %s\nContext versions: %s\n",
			items(value.SourceTypes), items(value.ArtifactFamilies), enabled(value.MemoryExtraction),
			enabled(value.ExperienceGeneration.Or(false)), enabled(value.ManagedSkillGeneration.Or(false)),
			enabled(value.ExternalSkillRegistry.Or(false)), enabled(value.HandoffGeneration),
			items(value.SearchModes), items(value.ContextVersions),
		)
		return err
	case v1.HealthResponse:
		_, err := fmt.Fprintf(writer, "Status: %s\n", value.Status)
		return err
	case v1.ReadinessResponse:
		if _, err := fmt.Fprintf(writer, "Status: %s\n", value.Status); err != nil {
			return err
		}
		keys := make([]string, 0, len(value.Checks))
		for key := range value.Checks {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		for _, key := range keys {
			if _, err := fmt.Fprintf(writer, "%s: %s\n", key, value.Checks[key]); err != nil {
				return err
			}
		}
		return nil
	case v1.ScopedStats:
		return writeStats(writer, value)
	default:
		return writeJSON(writer, value)
	}
}

func writeStats(writer io.Writer, value v1.ScopedStats) error {
	var output strings.Builder
	inventory := value.Inventory
	fmt.Fprintf(&output, "Scope: %s\n", value.ScopeID)
	fmt.Fprintf(&output, "As of: %s\n", isoDateTime(value.AsOf))
	fmt.Fprintf(&output, "Sources: %d total, %d memory processed, %d memory pending\n",
		inventory.Sources.Total, inventory.Sources.MemoryProcessed, inventory.Sources.MemoryPending)
	fmt.Fprintf(&output, "Artifacts: %d (%s)\n",
		inventory.Artifacts.Total, familyCounts(inventory.Artifacts.ByFamily))
	fmt.Fprintf(&output, "Candidates: %d total, %d pending, %d approved, %d rejected\n",
		inventory.Candidates.Total, inventory.Candidates.Pending,
		inventory.Candidates.Approved, inventory.Candidates.Rejected)
	entries := inventory.Memory.Entries
	fmt.Fprintf(&output, "Memory entries: %d total, %d active, %d inactive\n",
		entries.Total, entries.Active, entries.Inactive)
	period := value.Usage.Period
	fmt.Fprintf(&output, "Model usage: %s to %s (%s)\n",
		period.StartDate.Format("2006-01-02"), period.EndDate.Format("2006-01-02"), period.Timezone)
	writeModelUsage(&output, "Generation", value.Usage.Totals.Generation)
	writeModelUsage(&output, "Embedding", value.Usage.Totals.Embedding)
	for _, purpose := range value.Usage.ByPurpose {
		if purpose.Generation.Requests != 0 {
			writeModelUsage(&output, "  "+purpose.Purpose+" generation", purpose.Generation)
		}
		if purpose.Embedding.Requests != 0 {
			writeModelUsage(&output, "  "+purpose.Purpose+" embedding", purpose.Embedding)
		}
	}
	if estimator, ok := value.Recall.Estimator.Get(); ok {
		totals := value.Recall.Totals
		fmt.Fprintf(&output, "Recall token estimator: %s@%s\n", estimator.EstimatorID, estimator.Version)
		fmt.Fprintf(&output,
			"Recall tokens: %d preparations (%d ready, %d comparable), %d baseline, %d recalled, %d reduction\n",
			totals.Preparations, totals.ReadyPreparations, totals.ComparablePreparations,
			totals.BaselineTokens, totals.RecalledTokens, totals.TokenReduction)
	} else {
		output.WriteString("Recall token estimation: disabled\n")
	}
	_, err := io.WriteString(writer, output.String())
	return err
}

func writeModelUsage(output *strings.Builder, name string, value v1.ModelUsageValue) {
	fmt.Fprintf(output, "%s: %d requests, %s input tokens, %s output tokens\n",
		name, value.Requests, tokenCount(value.InputTokens), tokenCount(value.OutputTokens))
}

func tokenCount(value v1.NilInt) string {
	count, ok := value.Get()
	if !ok {
		return "unknown"
	}
	return strconv.Itoa(count)
}

func familyCounts(values []v1.FamilyCount) string {
	if len(values) == 0 {
		return "none"
	}
	formatted := make([]string, len(values))
	for index, value := range values {
		formatted[index] = fmt.Sprintf("%s=%d", value.Family, value.Total)
	}
	return strings.Join(formatted, ", ")
}

func isoDateTime(value time.Time) string {
	layout := "2006-01-02T15:04:05-07:00"
	if value.Nanosecond() != 0 {
		layout = "2006-01-02T15:04:05.000000-07:00"
	}
	return value.Format(layout)
}

func enabled(value bool) string {
	if value {
		return "enabled"
	}
	return "disabled"
}

func items[T ~string](values []T) string {
	if len(values) == 0 {
		return "none"
	}
	converted := make([]string, len(values))
	for index, value := range values {
		converted[index] = string(value)
	}
	return strings.Join(converted, ", ")
}

func optionalString(value string) v1.OptNilString {
	if value == "" {
		return v1.OptNilString{}
	}
	return v1.NewOptNilString(value)
}
