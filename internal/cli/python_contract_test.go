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
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v1 "github.com/ob-labs/powercontext-go/api/v1"
	"github.com/ob-labs/powercontext-go/server"
)

const generatedNoOpJSON = `{"status":"no_op","candidate":null}`

const pendingCandidateJSON = `{
  "candidate_id":"candidate-response","version":2,"family":"experience","status":"pending",
  "proposal":{"situation":"situation","action":"action","outcome":"outcome","lesson":"lesson"},
  "source_refs":[],"artifact_refs":[],"target":null,"reason":null,
  "result_artifact":null,"decision_reason":null
}`

func TestCLIHelpAndInstalledRoleCommands(t *testing.T) {
	for _, arguments := range [][]string{
		{"-h"},
		{"--help"},
		{"experience", "--help"},
		{"skill", "--help"},
		{"external-skill", "--help"},
	} {
		stdout, _, err := executeContractCLI(t, nil, arguments...)
		if err != nil {
			t.Fatalf("powercontext %v: %v", arguments, err)
		}
		if !strings.Contains(stdout, "Usage:") {
			t.Fatalf("powercontext %v output = %q", arguments, stdout)
		}
	}
	stdout, _, err := executeContractCLI(t, nil, "--help")
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"capabilities", "candidate", "stats", "server"} {
		if !strings.Contains(stdout, command) {
			t.Fatalf("root help does not expose %q: %s", command, stdout)
		}
	}
	for _, internal := range []string{"builtin", "client"} {
		if strings.Contains(stdout, internal) {
			t.Fatalf("root help exposes internal role %q: %s", internal, stdout)
		}
	}
}

func TestSkillCLIExposesTargetBasedCodexExport(t *testing.T) {
	skillHelp, _, err := executeContractCLI(t, nil, "skill", "--help")
	if err != nil {
		t.Fatal(err)
	}
	exportHelp, _, err := executeContractCLI(t, nil, "skill", "export", "--help")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(skillHelp, "export") || !strings.Contains(exportHelp, "--target") || !strings.Contains(exportHelp, "codex") {
		t.Fatalf("skill help = %q; export help = %q", skillHelp, exportHelp)
	}
}

func TestCLIVersionReportsBuildVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	command := newCommand(VersionInfo{Version: "0.0.1-test"}, &stdout, &stderr)
	command.SetArgs([]string{"--version"})
	if err := command.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "0.0.1-test\n" {
		t.Fatalf("version output = %q", stdout.String())
	}
}

func TestCLIClientSettingsLoadEnvironmentAndExplicitURLOverride(t *testing.T) {
	t.Setenv(clientURLVar, "https://memory.example/api/")
	t.Setenv(clientTimeoutVar, "3.5")
	t.Setenv(clientTokenVar, "")
	var URLs []string
	var deadlineRemaining []time.Duration
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		URLs = append(URLs, request.URL.String())
		if deadline, ok := request.Context().Deadline(); ok {
			deadlineRemaining = append(deadlineRemaining, time.Until(deadline))
		}
		return responseForCLI(request, http.StatusOK, `{"status":"ok"}`), nil
	})}
	if _, _, err := executeContractCLI(t, httpClient, "live"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := executeContractCLI(t, httpClient, "--server-url", "https://override.example/root/", "live"); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(URLs) != "[https://memory.example/api/health/live https://override.example/root/health/live]" {
		t.Fatalf("request URLs = %v", URLs)
	}
	if len(deadlineRemaining) != 2 {
		t.Fatalf("request deadlines = %v, want two", deadlineRemaining)
	}
	for _, remaining := range deadlineRemaining {
		if remaining <= 3*time.Second || remaining > 3500*time.Millisecond {
			t.Fatalf("configured timeout produced deadline %s", remaining)
		}
	}
}

func TestCLIClientSettingsRejectInvalidEnvironment(t *testing.T) {
	t.Run("server URL", func(t *testing.T) {
		t.Setenv(clientURLVar, "not-a-url")
		t.Setenv(clientTimeoutVar, "10")
		_, _, err := executeContractCLI(t, &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			t.Fatalf("invalid configuration reached transport: %s", request.URL)
			return nil, nil
		})}, "live")
		if err == nil || !strings.Contains(err.Error(), "server_url") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		t.Setenv(clientURLVar, "https://powercontext.test")
		t.Setenv(clientTimeoutVar, "0")
		_, _, err := executeContractCLI(t, nil, "live")
		if err == nil || err.Error() != "invalid POWERCONTEXT_CLIENT_TIMEOUT" {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestCLITimeoutFlagAcceptsSecondsAndGoDuration(t *testing.T) {
	var deadlines []time.Duration
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		deadline, ok := request.Context().Deadline()
		if !ok {
			t.Fatal("request has no timeout deadline")
		}
		deadlines = append(deadlines, time.Until(deadline))
		return responseForCLI(request, http.StatusOK, `{"status":"ok"}`), nil
	})}
	for _, value := range []string{"3.5", "3500ms"} {
		if _, _, err := executeContractCLI(t, httpClient,
			"--server-url", "https://powercontext.test", "--timeout", value, "live"); err != nil {
			t.Fatalf("--timeout %s: %v", value, err)
		}
	}
	for _, remaining := range deadlines {
		if remaining <= 3*time.Second || remaining > 3500*time.Millisecond {
			t.Fatalf("timeout deadline = %s", remaining)
		}
	}
	for _, arguments := range [][]string{
		{"--server-url", "https://powercontext.test", "--timeout", "NaN", "live"},
		{"--server-url", "", "live"},
	} {
		_, _, err := executeContractCLI(t, nil, arguments...)
		if err == nil || ExitCode(err) != 2 {
			t.Fatalf("powercontext %v error = %v, exit = %d", arguments, err, ExitCode(err))
		}
	}
}

func TestServerCommandLayersPartialCLIOverridesOverEnvironment(t *testing.T) {
	for _, test := range []struct {
		name        string
		environment map[string]string
		arguments   []string
		host        string
		port        int
	}{
		{"host override", map[string]string{"POWERCONTEXT_SERVER_HTTP_PORT": "8123"}, []string{"--host", "127.0.0.2"}, "127.0.0.2", 8123},
		{"port override", map[string]string{"POWERCONTEXT_SERVER_HTTP_HOST": "127.0.0.3"}, []string{"--port", "8124"}, "127.0.0.3", 8124},
	} {
		t.Run(test.name, func(t *testing.T) {
			for name, value := range test.environment {
				t.Setenv(name, value)
			}
			var received server.ProcessConfig
			runner := func(_ context.Context, _ *commandState, config server.ProcessConfig) error {
				received = config
				return nil
			}
			var stdout, stderr bytes.Buffer
			command := newCommandWithDependencies(VersionInfo{Version: "test"}, &stdout, &stderr, nil, runner)
			command.SetArgs(append([]string{"server", "run"}, test.arguments...))
			if err := command.ExecuteContext(context.Background()); err != nil {
				t.Fatal(err)
			}
			if received.HTTP.Host != test.host || received.HTTP.Port != test.port {
				t.Fatalf("HTTP config = %s:%d, want %s:%d", received.HTTP.Host, received.HTTP.Port, test.host, test.port)
			}
		})
	}
}

func TestServerCommandRejectsUnauthenticatedNonLoopbackHostOverride(t *testing.T) {
	called := false
	runner := func(context.Context, *commandState, server.ProcessConfig) error {
		called = true
		return nil
	}
	var stdout, stderr bytes.Buffer
	command := newCommandWithDependencies(VersionInfo{Version: "test"}, &stdout, &stderr, nil, runner)
	command.SetArgs([]string{"server", "run", "--host", "0.0.0.0"})
	err := command.ExecuteContext(context.Background())
	if err == nil || ExitCode(err) != 2 ||
		!strings.Contains(err.Error(), "POWERCONTEXT_SERVER_ALLOW_UNAUTHENTICATED_NON_LOOPBACK=true") {
		t.Fatalf("error = %v, exit = %d", err, ExitCode(err))
	}
	if called {
		t.Fatal("unsafe Server configuration reached the runner")
	}
}

func TestServerCommandReportsFriendlyErrorWhenAuthTokenMissing(t *testing.T) {
	t.Setenv("POWERCONTEXT_SERVER_AUTH_ENABLED", "true")
	t.Setenv("POWERCONTEXT_SERVER_AUTH_TOKEN", "")
	called := false
	runner := func(context.Context, *commandState, server.ProcessConfig) error {
		called = true
		return nil
	}
	var stdout, stderr bytes.Buffer
	command := newCommandWithDependencies(VersionInfo{Version: "test"}, &stdout, &stderr, nil, runner)
	command.SetArgs([]string{"server", "run"})
	err := command.ExecuteContext(context.Background())
	if err == nil || ExitCode(err) != 2 ||
		!strings.Contains(err.Error(), "POWERCONTEXT_SERVER_AUTH_TOKEN") ||
		!strings.Contains(err.Error(), "POWERCONTEXT_SERVER_AUTH_ENABLED=false") {
		t.Fatalf("error = %v, exit = %d", err, ExitCode(err))
	}
	if called {
		t.Fatal("invalid authentication configuration reached the runner")
	}
}

func TestServerCommandRejectsBlankHostOverride(t *testing.T) {
	for _, host := range []string{"", " "} {
		t.Run(fmt.Sprintf("host=%q", host), func(t *testing.T) {
			called := false
			runner := func(context.Context, *commandState, server.ProcessConfig) error {
				called = true
				return nil
			}
			var stdout, stderr bytes.Buffer
			command := newCommandWithDependencies(VersionInfo{Version: "test"}, &stdout, &stderr, nil, runner)
			command.SetArgs([]string{"server", "run", "--host", host})
			err := command.ExecuteContext(context.Background())
			if err == nil || ExitCode(err) != 2 {
				t.Fatalf("server run --host error = %v, exit = %d, want usage failure", err, ExitCode(err))
			}
			if called {
				t.Fatal("server runner was called for a blank host")
			}
		})
	}
}

func TestServerCommandLoopbackOverrideRepairsUnsafeEnvironment(t *testing.T) {
	t.Setenv("POWERCONTEXT_SERVER_HTTP_HOST", "0.0.0.0")
	t.Setenv("POWERCONTEXT_SERVER_AUTH_ENABLED", "false")
	t.Setenv("POWERCONTEXT_SERVER_ALLOW_UNAUTHENTICATED_NON_LOOPBACK", "false")
	var received server.ProcessConfig
	runner := func(_ context.Context, _ *commandState, config server.ProcessConfig) error {
		received = config
		return nil
	}
	var stdout, stderr bytes.Buffer
	command := newCommandWithDependencies(VersionInfo{Version: "test"}, &stdout, &stderr, nil, runner)
	command.SetArgs([]string{"server", "run", "--host", "127.0.0.1"})
	if err := command.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if received.HTTP.Host != "127.0.0.1" {
		t.Fatalf("HTTP host = %q, want repaired loopback bind", received.HTTP.Host)
	}
}

func TestServerCommandDoesNotLoadClientSettings(t *testing.T) {
	t.Setenv(clientURLVar, "not-a-url")
	called := false
	runner := func(context.Context, *commandState, server.ProcessConfig) error {
		called = true
		return nil
	}
	var stdout, stderr bytes.Buffer
	command := newCommandWithDependencies(VersionInfo{Version: "test"}, &stdout, &stderr, nil, runner)
	command.SetArgs([]string{"server", "run"})
	if err := command.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("server runner was not called")
	}
}

func TestCLIReportsServerErrorWithRequestContext(t *testing.T) {
	httpClient := fakeHTTPClient(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/plain")
		writer.Header().Set("X-PowerContext-Request-ID", "request-123")
		writer.WriteHeader(http.StatusServiceUnavailable)
		_, _ = writer.Write([]byte("Service Unavailable"))
	})
	_, _, err := executeContractCLI(t, httpClient, "--server-url", "https://powercontext.test", "ready")
	if err == nil || err.Error() != "PowerContext Server returned HTTP 503 (request ID: request-123)" {
		t.Fatalf("error = %v", err)
	}
}

func TestCLIPrintsHumanLivenessByDefault(t *testing.T) {
	httpClient := fakeHTTPClient(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"status":"ok"}`))
	})
	stdout, _, err := executeContractCLI(t, httpClient, "--server-url", "https://powercontext.test", "live")
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "Status: ok\n" {
		t.Fatalf("output = %q", stdout)
	}
}

func TestStatsCommandBuildsRequestAndPrintsSummary(t *testing.T) {
	var query string
	httpClient := fakeHTTPClient(func(writer http.ResponseWriter, request *http.Request) {
		query = request.URL.RawQuery
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(scopedStatsJSON))
	})
	stdout, _, err := executeContractCLI(t, httpClient,
		"--server-url", "https://powercontext.test", "stats", "--scope-id", "project", "--period", "today")
	if err != nil {
		t.Fatal(err)
	}
	if query != "scope_id=project&period=today" && query != "period=today&scope_id=project" {
		t.Fatalf("stats query = %q", query)
	}
	for _, expected := range []string{
		"Sources: 0 total, 0 memory processed, 0 memory pending",
		"Generation: 0 requests, 0 input tokens, 0 output tokens",
		"Recall token estimator: character:weighted@1",
		"Recall tokens: 3 preparations (2 ready, 1 comparable), 100 baseline, 40 recalled, 60 reduction",
	} {
		if !strings.Contains(stdout, expected) {
			t.Errorf("stats output %q does not contain %q", stdout, expected)
		}
	}
	if count := strings.Count(stdout, "Embedding: 0 requests, 0 input tokens, 0 output tokens"); count != 1 {
		t.Fatalf("stats output contains %d aggregate embedding rows: %q", count, stdout)
	}
	if strings.HasPrefix(strings.TrimSpace(stdout), "{") {
		t.Fatalf("default stats output remained JSON: %s", stdout)
	}
}

func TestCLIGenerationCommandsBuildTypedRequests(t *testing.T) {
	var experience v1.GenerateExperienceRequest
	var skill v1.GenerateSkillRequest
	httpClient := fakeHTTPClient(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/v1/experience/generate":
			decodeRequest(t, request, &experience)
		case "/v1/skill/generate":
			decodeRequest(t, request, &skill)
		default:
			t.Fatalf("unexpected path %s", request.URL.Path)
		}
		_, _ = writer.Write([]byte(generatedNoOpJSON))
	})
	if _, _, err := executeContractCLI(t, httpClient,
		"--server-url", "https://powercontext.test", "--json", "experience", "generate",
		"--scope-id", "project", "--source-ref", "content/task-1", "--source-ref", "content/task-2",
		"--target", "experience/exp-1@2", "--reason", "incorporate the latest result"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := executeContractCLI(t, httpClient,
		"--server-url", "https://powercontext.test", "--json", "skill", "generate",
		"--scope-id", "project", "--origin", "experience", "--artifact-ref", "experience/exp-2@1"); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(experience.SourceRefs) != "[{content task-1} {content task-2}]" || experience.Reason.Or("") != "incorporate the latest result" {
		t.Fatalf("experience request = %#v", experience)
	}
	target, ok := experience.Target.Get()
	if !ok || target != (v1.ArtifactReference{Family: "experience", ArtifactID: "exp-1", Revision: 2}) ||
		len(experience.ArtifactRefs) != 1 || experience.ArtifactRefs[0] != target {
		t.Fatalf("experience target/evidence = %#v / %#v", experience.Target, experience.ArtifactRefs)
	}
	if skill.Origin != v1.SkillGenerationOriginExperience || len(skill.ArtifactRefs) != 1 ||
		skill.ArtifactRefs[0] != (v1.ArtifactReference{Family: "experience", ArtifactID: "exp-2", Revision: 1}) {
		t.Fatalf("skill request = %#v", skill)
	}
}

func TestCLICandidateRevisionCommandsBuildTypedProposals(t *testing.T) {
	var received []v1.ReviseArtifactCandidateRequest
	httpClient := fakeHTTPClient(func(writer http.ResponseWriter, request *http.Request) {
		var value v1.ReviseArtifactCandidateRequest
		decodeRequest(t, request, &value)
		received = append(received, value)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(pendingCandidateJSON))
	})
	instructions := filepath.Join(t.TempDir(), "instructions.md")
	if err := os.WriteFile(instructions, []byte("Run both backend acceptance scenarios."), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := executeContractCLI(t, httpClient,
		"--server-url", "https://powercontext.test", "candidate", "revise", "experience",
		"--scope-id", "project", "--expected-version", "1", "--situation", "Only one backend was tested.",
		"--action", "Run the same scenario on both backends.", "--outcome", "Both backends passed.",
		"--lesson", "Keep acceptance behavior backend-neutral.", "--source-ref", "content/task-1", "candidate-experience"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := executeContractCLI(t, httpClient,
		"--server-url", "https://powercontext.test", "candidate", "revise", "skill",
		"--scope-id", "project", "--expected-version", "2", "--name", "backend-validation",
		"--description", "Validate storage backends consistently.", "--instructions-file", instructions,
		"--validation", "SQLite passes.", "--validation", "OceanBase passes.",
		"--target", "skill/backend-validation@1", "candidate-skill"); err != nil {
		t.Fatal(err)
	}
	if len(received) != 2 {
		t.Fatalf("revision requests = %d", len(received))
	}
	experience, ok := received[0].Proposal.GetExperienceProposal()
	if !ok || experience.Lesson != "Keep acceptance behavior backend-neutral." {
		t.Fatalf("experience proposal = %#v", received[0].Proposal)
	}
	skill, ok := received[1].Proposal.GetSkillProposal()
	if !ok || skill.Instructions != "Run both backend acceptance scenarios." ||
		fmt.Sprint(skill.Validation) != "[SQLite passes. OceanBase passes.]" {
		t.Fatalf("skill proposal = %#v", received[1].Proposal)
	}
	target, ok := received[1].Target.Get()
	if !ok || len(received[1].ArtifactRefs) != 1 || received[1].ArtifactRefs[0] != target {
		t.Fatalf("skill target/evidence = %#v / %#v", received[1].Target, received[1].ArtifactRefs)
	}
}

func TestCLICandidateLifecycleCommandsBuildTypedRequests(t *testing.T) {
	var listed v1.ListArtifactCandidatesRequest
	var shown v1.GetArtifactCandidateRequest
	var approved v1.ApproveArtifactCandidateRequest
	var rejected v1.RejectArtifactCandidateRequest
	const pending = `{
  "candidate_id":"candidate-1","version":1,"family":"experience","status":"pending",
  "proposal":{"situation":"situation","action":"action","outcome":"outcome","lesson":"lesson"},
  "source_refs":[{"name":"content","source_id":"task-1"}],"artifact_refs":[],
  "target":null,"reason":null,"result_artifact":null,"decision_reason":null
}`
	const approvedResponse = `{
  "candidate_id":"candidate-1","version":2,"family":"experience","status":"approved",
  "proposal":{"situation":"situation","action":"action","outcome":"outcome","lesson":"lesson"},
  "source_refs":[{"name":"content","source_id":"task-1"}],"artifact_refs":[],
  "target":null,"reason":null,
  "result_artifact":{"family":"experience","artifact_id":"experience-1","revision":1},
  "decision_reason":null
}`
	const rejectedResponse = `{
  "candidate_id":"candidate-2","version":1,"family":"experience","status":"rejected",
  "proposal":{"situation":"situation","action":"action","outcome":"outcome","lesson":"lesson"},
  "source_refs":[{"name":"content","source_id":"task-1"}],"artifact_refs":[],
  "target":null,"reason":null,"result_artifact":null,"decision_reason":"unsupported"
}`
	httpClient := fakeHTTPClient(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/v1/artifact-candidates/list":
			decodeRequest(t, request, &listed)
			_, _ = writer.Write([]byte(`{"candidates":[` + pending + `],"next_cursor":null}`))
		case "/v1/artifact-candidates/get":
			decodeRequest(t, request, &shown)
			_, _ = writer.Write([]byte(pending))
		case "/v1/artifact-candidates/approve":
			decodeRequest(t, request, &approved)
			_, _ = writer.Write([]byte(approvedResponse))
		case "/v1/artifact-candidates/reject":
			decodeRequest(t, request, &rejected)
			_, _ = writer.Write([]byte(rejectedResponse))
		default:
			t.Fatalf("unexpected Candidate CLI path %q", request.URL.Path)
		}
	})

	commands := [][]string{
		{"--json", "candidate", "list", "--scope-id", "project", "--status", "pending", "--family", "experience", "--cursor", "candidate-0", "--limit", "7"},
		{"--json", "candidate", "show", "--scope-id", "project", "candidate-1"},
		{"--json", "candidate", "approve", "--scope-id", "project", "--expected-version", "2", "candidate-1"},
		{"--json", "candidate", "reject", "--scope-id", "project", "--expected-version", "1", "--reason", "unsupported", "candidate-2"},
	}
	for _, arguments := range commands {
		stdout, _, err := executeContractCLI(
			t, httpClient, append([]string{"--server-url", "https://powercontext.test"}, arguments...)...,
		)
		if err != nil {
			t.Fatalf("powercontext %v: %v", arguments, err)
		}
		if !json.Valid([]byte(stdout)) {
			t.Fatalf("powercontext %v emitted invalid JSON: %q", arguments, stdout)
		}
	}

	status, statusSet := listed.Status.Get()
	family, familySet := listed.Family.Get()
	cursor, cursorSet := listed.Cursor.Get()
	limit, limitSet := listed.Limit.Get()
	if listed.ScopeID != "project" || !statusSet || status != v1.CandidateStatusPending ||
		!familySet || family != v1.CandidateFamilyExperience || !cursorSet || cursor != "candidate-0" ||
		!limitSet || limit != 7 {
		t.Fatalf("Candidate list request = %#v", listed)
	}
	if shown.ScopeID != "project" || shown.CandidateID != "candidate-1" {
		t.Fatalf("Candidate show request = %#v", shown)
	}
	if approved.ScopeID != "project" || approved.CandidateID != "candidate-1" || approved.ExpectedVersion != 2 {
		t.Fatalf("Candidate approval request = %#v", approved)
	}
	if rejected.ScopeID != "project" || rejected.CandidateID != "candidate-2" ||
		rejected.ExpectedVersion != 1 || rejected.Reason != "unsupported" {
		t.Fatalf("Candidate rejection request = %#v", rejected)
	}
}

func TestCLIGenerationCommandsRejectInvalidReferencesAsUsage(t *testing.T) {
	for _, test := range []struct {
		arguments []string
		message   string
	}{
		{[]string{"experience", "generate", "--scope-id", "project", "--source-ref", "task-1"}, "expected TYPE/ID"},
		{[]string{"skill", "generate", "--scope-id", "project", "--origin", "source", "--artifact-ref", "experience/exp-1@1"}, "source origin requires only Source refs"},
	} {
		_, _, err := executeContractCLI(t, nil, test.arguments...)
		if err == nil || ExitCode(err) != 2 || !strings.Contains(err.Error(), test.message) {
			t.Fatalf("powercontext %v error = %v, exit = %d", test.arguments, err, ExitCode(err))
		}
	}
}

func TestCLIExternalSkillImportPreservesIdentityAndIntent(t *testing.T) {
	var received v1.ImportExternalSkillRequest
	httpClient := fakeHTTPClient(func(writer http.ResponseWriter, request *http.Request) {
		decodeRequest(t, request, &received)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(generatedNoOpJSON))
	})
	fingerprint := strings.Repeat("a", 64)
	if _, _, err := executeContractCLI(t, httpClient,
		"--server-url", "https://powercontext.test", "external-skill", "import", "--scope-id", "project",
		"--fingerprint", fingerprint, "--mode", "fork", "codex:project:repository/friendly-python"); err != nil {
		t.Fatal(err)
	}
	if received.ExternalSkillID != "codex:project:repository/friendly-python" ||
		received.Fingerprint != fingerprint || received.Mode != v1.ExternalSkillImportModeFork {
		t.Fatalf("import request = %#v", received)
	}
}

func TestCLIMapsOpenAPIRequestValidationToUsage(t *testing.T) {
	transportCalled := false
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		transportCalled = true
		return responseForCLI(request, http.StatusInternalServerError, ""), nil
	})}
	_, _, err := executeContractCLI(t, httpClient,
		"--server-url", "https://powercontext.test", "external-skill", "import",
		"--scope-id", "project", "--fingerprint", "too-short", "external-skill-id")
	if err == nil || ExitCode(err) != 2 {
		t.Fatalf("error = %v, exit = %d", err, ExitCode(err))
	}
	if transportCalled {
		t.Fatal("invalid CLI request reached HTTP transport")
	}
}

func TestCLISkillExportUsesConfiguredAuthentication(t *testing.T) {
	t.Setenv(clientURLVar, "https://powercontext.test")
	t.Setenv(clientTokenVar, "secret-token")
	t.Setenv(clientTimeoutVar, "10")
	var authorization string
	httpClient := fakeHTTPClient(func(writer http.ResponseWriter, request *http.Request) {
		authorization = request.Header.Get("Authorization")
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{
			"artifact":{"family":"skill","artifact_id":"skill-123","revision":1},
			"content":{"name":"safe-skill","description":"Use for a bounded task.","instructions":"Perform the bounded task.","validation":["The expected result exists."]},
			"source_refs":[],"artifact_refs":[]
		}`))
	})
	destination := filepath.Join(t.TempDir(), "safe-skill")
	stdout, _, err := executeContractCLI(t, httpClient,
		"skill", "export", "--target", "codex", "--scope-id", "project", "--revision", "1",
		"--destination", destination, "skill-123")
	if err != nil {
		t.Fatal(err)
	}
	if authorization != "Bearer secret-token" || !strings.Contains(stdout, "Exported skill-123@1 for codex") {
		t.Fatalf("authorization = %q; output = %q", authorization, stdout)
	}
	if _, err := os.Stat(filepath.Join(destination, "SKILL.md")); err != nil {
		t.Fatalf("projected SKILL.md: %v", err)
	}
}

func executeContractCLI(t *testing.T, httpClient *http.Client, arguments ...string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	command := newCommandWithHTTPClient(VersionInfo{Version: "test"}, &stdout, &stderr, httpClient)
	command.SetArgs(arguments)
	err := command.ExecuteContext(context.Background())
	return stdout.String(), stderr.String(), err
}

func decodeRequest(t *testing.T, request *http.Request, target any) {
	t.Helper()
	if err := json.NewDecoder(request.Body).Decode(target); err != nil {
		t.Fatalf("decode %s request: %v", request.URL.Path, err)
	}
}

func responseForCLI(request *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    request,
	}
}

const scopedStatsJSON = `{
  "scope_id":"project",
  "as_of":"2026-08-04T12:00:00Z",
  "inventory":{
    "sources":{"total":0,"memory_processed":0,"memory_pending":0},
    "artifacts":{"total":0,"by_family":[]},
    "candidates":{"total":0,"pending":0,"approved":0,"rejected":0,"by_family":[]},
    "memory":{"entries":{"total":0,"active":0,"inactive":0,"by_kind":[]}}
  },
  "usage":{
    "period":{"preset":"today","start_date":"2026-08-04","end_date":"2026-08-04","timezone":"UTC"},
    "totals":{"generation":{"requests":0,"input_tokens":0,"output_tokens":0},"embedding":{"requests":0,"input_tokens":0,"output_tokens":0}},
    "by_purpose":[],
    "daily":[{"date":"2026-08-04","generation":{"requests":0,"input_tokens":0,"output_tokens":0},"embedding":{"requests":0,"input_tokens":0,"output_tokens":0},"by_purpose":[]}]
  },
  "recall":{
    "period":{"preset":"today","start_date":"2026-08-04","end_date":"2026-08-04","timezone":"UTC"},
    "estimator":{"estimator_id":"character:weighted","version":"1"},
    "totals":{"preparations":3,"ready_preparations":2,"comparable_preparations":1,"baseline_tokens":100,"recalled_tokens":40,"token_reduction":60},
    "daily":[{"date":"2026-08-04","preparations":3,"ready_preparations":2,"comparable_preparations":1,"baseline_tokens":100,"recalled_tokens":40,"token_reduction":60}]
  }
}`
