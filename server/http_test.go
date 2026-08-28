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

package server

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	v1 "github.com/ob-labs/powercontext-go/api/v1"
	"github.com/ob-labs/powercontext-go/artifact/memory"
	"github.com/ob-labs/powercontext-go/internal/endpoint"
	"github.com/ob-labs/powercontext-go/internal/httpapi"
	servermetrics "github.com/ob-labs/powercontext-go/internal/observability/metrics"
	"github.com/ob-labs/powercontext-go/internal/review"
	"github.com/ob-labs/powercontext-go/internal/runtime"
	"github.com/ob-labs/powercontext-go/internal/webui"
)

func repeatHTTPValue[T any](value T, count int) []T {
	result := make([]T, count)
	for index := range result {
		result[index] = value
	}
	return result
}

func TestSystemHTTPVerticalSlice(t *testing.T) {
	t.Parallel()
	capabilities, err := runtime.NewCapabilities(runtime.CapabilityOptions{
		SourceTypes: []string{"content"}, ArtifactFamilies: []string{"memory", "experience", "skill", "handoff"},
		MemoryExtraction: true, SearchModes: []memory.SearchMode{memory.SearchAuto, memory.SearchFTS},
		ContextVersions: []string{runtime.PreparedContextV1},
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := endpoint.NewHandler(endpoint.HandlerOptions{
		Capabilities: func(context.Context) (runtime.Capabilities, error) { return capabilities, nil },
		Readiness: func(context.Context) (runtime.Readiness, error) {
			checks, buildErr := runtime.NewReadinessChecks([]runtime.ProbeDefinition{
				{Name: "database", Blocking: true, Probe: func(context.Context) (runtime.CheckStatus, error) { return runtime.CheckReady, nil }},
				{Name: "inference.embedding", Probe: func(context.Context) (runtime.CheckStatus, error) { return runtime.CheckMisconfigured, nil }},
			})
			if buildErr != nil {
				return runtime.Readiness{}, buildErr
			}
			return checks.Run(context.Background())
		},
	})
	server, err := NewHTTPHandler(handler, HTTPOptions{HandoffReportRoutes: false})
	if err != nil {
		t.Fatal(err)
	}

	live := perform(server, http.MethodGet, "/health/live", "")
	if live.Code != http.StatusOK || live.Body.String() != `{"status":"ok"}` {
		t.Fatalf("live = %d %s", live.Code, live.Body.String())
	}
	assertRequestID(t, live)

	ready := perform(server, http.MethodGet, "/health/ready", "")
	if ready.Code != http.StatusOK {
		t.Fatalf("ready = %d %s", ready.Code, ready.Body.String())
	}
	var readiness struct {
		Status string            `json:"status"`
		Checks map[string]string `json:"checks"`
	}
	if err := json.Unmarshal(ready.Body.Bytes(), &readiness); err != nil {
		t.Fatal(err)
	}
	if readiness.Status != "degraded" || readiness.Checks["database"] != "ready" || readiness.Checks["inference.embedding"] != "misconfigured" {
		t.Fatalf("readiness = %#v", readiness)
	}

	caps := perform(server, http.MethodGet, "/v1/capabilities", "")
	if caps.Code != http.StatusOK {
		t.Fatalf("capabilities = %d %s", caps.Code, caps.Body.String())
	}
	var body v1.Capabilities
	if err := json.Unmarshal(caps.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.SourceTypes) != 1 || body.SourceTypes[0] != "content" || !body.MemoryExtraction || len(body.SearchModes) != 2 {
		t.Fatalf("capabilities = %#v", body)
	}

	disabled := perform(server, http.MethodPost, "/v1/handoff-reports/projects/list", `{}`)
	if disabled.Code != http.StatusNotFound {
		t.Fatalf("disabled report = %d %s", disabled.Code, disabled.Body.String())
	}
}

func TestHTTPCombinedCandidateEvidenceFailureMatchesFrozenPythonEnvelope(t *testing.T) {
	t.Parallel()
	handler, err := NewHTTPHandler(endpoint.NewHandler(endpoint.HandlerOptions{}), HTTPOptions{})
	if err != nil {
		t.Fatal(err)
	}
	source := map[string]any{"name": "content", "source_id": "task-1"}
	artifact := map[string]any{"family": "experience", "artifact_id": "exp-1", "revision": 1}
	payload, err := json.Marshal(map[string]any{
		"scope_id": "project",
		"proposal": map[string]any{
			"situation": "OpenAPI changed.", "action": "Regenerate the Client.",
			"outcome": "Transport stays aligned.", "lesson": "Keep contract tests green.",
		},
		"source_refs":   repeatHTTPValue(source, 20),
		"artifact_refs": repeatHTTPValue(artifact, 13),
	})
	if err != nil {
		t.Fatal(err)
	}
	response := perform(handler, http.MethodPost, "/v1/experience/propose", string(payload))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", response.Code, response.Body.String())
	}
	var envelope struct {
		Error struct {
			Code    string         `json:"code"`
			Message string         `json:"message"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != "internal_error" || envelope.Error.Message != "The Server failed." || envelope.Error.Details != nil {
		t.Fatalf("error = %#v", envelope.Error)
	}
	assertRequestID(t, response)
}

func TestHTTPRejectsCombinedCandidateEvidenceInEndpointResponse(t *testing.T) {
	t.Parallel()
	handler, err := NewHTTPHandler(invalidCandidateResponseHandler{}, HTTPOptions{})
	if err != nil {
		t.Fatal(err)
	}
	response := perform(handler, http.MethodPost, "/v1/experience/propose", `{
		"scope_id":"project",
		"proposal":{"situation":"A","action":"B","outcome":"C","lesson":"D"},
		"source_refs":[{"name":"content","source_id":"task-1"}],
		"artifact_refs":[]
	}`)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", response.Code, response.Body.String())
	}
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != "internal_error" {
		t.Fatalf("error code = %q, want internal_error", envelope.Error.Code)
	}
	assertRequestID(t, response)
}

func TestHTTPGenerationUnavailableUsesStable503Envelope(t *testing.T) {
	t.Parallel()
	handler, err := NewHTTPHandler(endpoint.NewHandler(endpoint.HandlerOptions{}), HTTPOptions{})
	if err != nil {
		t.Fatal(err)
	}
	response := perform(handler, http.MethodPost, "/v1/experience/generate", `{
		"scope_id":"project",
		"source_refs":[{"name":"content","source_id":"task-1"}],
		"artifact_refs":[]
	}`)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", response.Code, response.Body.String())
	}
	var envelope struct {
		Error struct {
			Code    string         `json:"code"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != "generation_unavailable" || envelope.Error.Details["family"] != "experience" {
		t.Fatalf("generation error envelope = %#v", envelope)
	}
	assertRequestID(t, response)
}

func TestMetricsEndpointIsAbsentWhenDisabled(t *testing.T) {
	t.Parallel()
	handler, err := NewHTTPHandler(endpoint.NewHandler(endpoint.HandlerOptions{}), HTTPOptions{})
	if err != nil {
		t.Fatal(err)
	}
	response := perform(handler, http.MethodGet, "/metrics", "")
	if response.Code != http.StatusNotFound {
		t.Fatalf("metrics = %d %s", response.Code, response.Body.String())
	}
}

func TestHTTPReadinessMapsNotReadyAndDegradedStatuses(t *testing.T) {
	t.Parallel()
	probe := func(status runtime.CheckStatus) runtime.Probe {
		return func(context.Context) (runtime.CheckStatus, error) { return status, nil }
	}
	for _, test := range []struct {
		name       string
		definition runtime.ProbeDefinition
		wantCode   int
		wantStatus string
		wantChecks map[string]string
	}{
		{
			name: "blocking dependency is not ready",
			definition: runtime.ProbeDefinition{
				Name: "database", Blocking: true, Probe: probe(runtime.CheckUnavailable),
			},
			wantCode: http.StatusServiceUnavailable, wantStatus: "not_ready",
			wantChecks: map[string]string{"database": "unavailable"},
		},
		{
			name: "optional dependency is degraded",
			definition: runtime.ProbeDefinition{
				Name: "inference.embedding", Probe: probe(runtime.CheckUnavailable),
			},
			wantCode: http.StatusOK, wantStatus: "degraded",
			wantChecks: map[string]string{"inference.embedding": "unavailable"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			checks, err := runtime.NewReadinessChecks([]runtime.ProbeDefinition{test.definition})
			if err != nil {
				t.Fatal(err)
			}
			handler := endpoint.NewHandler(endpoint.HandlerOptions{
				Readiness: func(ctx context.Context) (runtime.Readiness, error) {
					return checks.Run(ctx)
				},
			})
			server, err := NewHTTPHandler(handler, HTTPOptions{})
			if err != nil {
				t.Fatal(err)
			}
			response := perform(server, http.MethodGet, "/health/ready", "")
			if response.Code != test.wantCode {
				t.Fatalf("status code = %d: %s", response.Code, response.Body.String())
			}
			var body struct {
				Status string            `json:"status"`
				Checks map[string]string `json:"checks"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Status != test.wantStatus || !maps.Equal(body.Checks, test.wantChecks) {
				t.Fatalf("readiness = %#v", body)
			}
			assertRequestID(t, response)
		})
	}
}

func TestHTTPAuthenticationAndUnboundReadiness(t *testing.T) {
	t.Parallel()
	observability, err := servermetrics.New()
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewHTTPHandler(endpoint.NewHandler(endpoint.HandlerOptions{}), HTTPOptions{
		BearerToken: "server-secret", metrics: observability,
	})
	if err != nil {
		t.Fatal(err)
	}

	missing := perform(server, http.MethodGet, "/v1/capabilities", "")
	if missing.Code != http.StatusUnauthorized || missing.Header().Get("WWW-Authenticate") != "Bearer" {
		t.Fatalf("missing auth = %d %s", missing.Code, missing.Body.String())
	}
	live := perform(server, http.MethodGet, "/health/live", "")
	if live.Code != http.StatusOK {
		t.Fatalf("live = %d", live.Code)
	}
	protectedMetrics := perform(server, http.MethodGet, "/metrics", "")
	if protectedMetrics.Code != http.StatusUnauthorized {
		t.Fatalf("protected metrics = %d %s", protectedMetrics.Code, protectedMetrics.Body.String())
	}
	ready := perform(server, http.MethodGet, "/health/ready", "")
	if ready.Code != http.StatusServiceUnavailable || ready.Body.String() != `{"status":"not_ready","checks":{"runtime":"not_ready"}}` {
		t.Fatalf("ready = %d %s", ready.Code, ready.Body.String())
	}

	request := httptest.NewRequest(http.MethodGet, "/v1/capabilities", nil)
	request.Header.Set("Authorization", "Bearer server-secret")
	accepted := httptest.NewRecorder()
	server.ServeHTTP(accepted, request)
	if accepted.Code != http.StatusOK {
		t.Fatalf("accepted = %d %s", accepted.Code, accepted.Body.String())
	}
	metricsRequest := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	metricsRequest.Header.Set("Authorization", "Bearer server-secret")
	acceptedMetrics := httptest.NewRecorder()
	server.ServeHTTP(acceptedMetrics, metricsRequest)
	if acceptedMetrics.Code != http.StatusOK {
		t.Fatalf("accepted metrics = %d %s", acceptedMetrics.Code, acceptedMetrics.Body.String())
	}
}

func TestHTTPDecodeFailureUsesStableEnvelope(t *testing.T) {
	t.Parallel()
	server, err := NewHTTPHandler(endpoint.NewHandler(endpoint.HandlerOptions{}), HTTPOptions{HandoffReportRoutes: true})
	if err != nil {
		t.Fatal(err)
	}
	response := perform(server, http.MethodPost, "/v1/memory/search", `{}`)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil || envelope.Error.Code != "invalid_request" {
		t.Fatalf("envelope = %#v, err=%v", envelope, err)
	}
	assertRequestID(t, response)
}

func TestHTTPPrepareContextRejectsMemorySpecificTuningFields(t *testing.T) {
	t.Parallel()
	server, err := NewHTTPHandler(endpoint.NewHandler(endpoint.HandlerOptions{}), HTTPOptions{})
	if err != nil {
		t.Fatal(err)
	}
	response := perform(server, http.MethodPost, "/v1/context/prepare", `{
		"scope_id":"project:test",
		"query":"query",
		"candidate_limit":2
	}`)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil || envelope.Error.Code != "invalid_request" {
		t.Fatalf("envelope = %#v, err=%v", envelope, err)
	}
	assertRequestID(t, response)
}

func TestHTTPPrepareContextRejectsInvalidUTF8WithoutEchoingInput(t *testing.T) {
	t.Parallel()
	handler, err := NewHTTPHandler(endpoint.NewHandler(endpoint.HandlerOptions{}), HTTPOptions{})
	if err != nil {
		t.Fatal(err)
	}
	response := perform(
		handler, http.MethodPost, "/v1/context/prepare",
		`{"scope_id":"project:test","query":"\udcaa"}`,
	)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	var envelope struct {
		Error struct {
			Code    string           `json:"code"`
			Details map[string][]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != "invalid_request" {
		t.Fatalf("error = %#v", envelope.Error)
	}
	if !strings.Contains(response.Body.String(), `"loc":["body","query"]`) ||
		!strings.Contains(response.Body.String(), `"type":"string_unicode"`) {
		t.Fatalf("Unicode validation detail = %s", response.Body.String())
	}
	if strings.Contains(response.Body.String(), `"input"`) || strings.Contains(response.Body.String(), "project:test") {
		t.Fatalf("invalid request echoed input: %s", response.Body.String())
	}
}

func TestOptionalMCPRouteUsesConfiguredPath(t *testing.T) {
	t.Parallel()
	handler, err := NewHTTPHandler(endpoint.NewHandler(endpoint.HandlerOptions{}), HTTPOptions{
		MCP: MCPOptions{Enabled: true, Path: "/agent", JSONResponse: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "server-test", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: "http://powercontext.test/agent/",
		HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			return response.Result(), nil
		})},
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 20 {
		t.Fatalf("tools = %d, want 20", len(tools.Tools))
	}

	disabled, err := NewHTTPHandler(endpoint.NewHandler(endpoint.HandlerOptions{}), HTTPOptions{})
	if err != nil {
		t.Fatal(err)
	}
	response := perform(disabled, http.MethodGet, "/mcp", "")
	if response.Code != http.StatusNotFound {
		t.Fatalf("disabled MCP = %d %s", response.Code, response.Body.String())
	}
}

func TestMCPRouteUsesServerAuthentication(t *testing.T) {
	t.Parallel()
	handler, err := NewHTTPHandler(endpoint.NewHandler(endpoint.HandlerOptions{}), HTTPOptions{
		BearerToken: "server-secret",
		MCP:         MCPOptions{Enabled: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	response := perform(handler, http.MethodPost, "/mcp/", `{}`)
	if response.Code != http.StatusUnauthorized || response.Header().Get("WWW-Authenticate") != "Bearer" {
		t.Fatalf("MCP authentication = %d %s", response.Code, response.Body.String())
	}
}

func TestMCPPathValidation(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"agent", "/", "/agent?unsafe=true"} {
		_, err := NewHTTPHandler(endpoint.NewHandler(endpoint.HandlerOptions{}), HTTPOptions{
			MCP: MCPOptions{Enabled: true, Path: value},
		})
		if err == nil {
			t.Fatalf("MCP path %q was accepted", value)
		}
	}
}

func TestDashboardPageIsPublicButScopeInventoryIsAuthenticated(t *testing.T) {
	t.Parallel()
	handler, err := NewHTTPHandler(endpoint.NewHandler(endpoint.HandlerOptions{}), HTTPOptions{
		BearerToken: "dashboard-secret",
		webUI: &webui.Options{DashboardEnabled: true, AuthenticationRequired: true, Scopes: []webui.Scope{
			{ScopeID: "project:powercontext", DisplayName: "PowerContext"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	home := perform(handler, http.MethodGet, "/", "")
	if home.Code != http.StatusOK || home.Header().Get("Content-Security-Policy") == "" {
		t.Fatalf("home = %d %#v", home.Code, home.Header())
	}
	unauthorized := perform(handler, http.MethodGet, "/dashboard/scopes", "")
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized scopes = %d %s", unauthorized.Code, unauthorized.Body.String())
	}
	request := httptest.NewRequest(http.MethodGet, "/dashboard/scopes", nil)
	request.Header.Set("Authorization", "Bearer dashboard-secret")
	accepted := httptest.NewRecorder()
	handler.ServeHTTP(accepted, request)
	if accepted.Code != http.StatusOK || accepted.Body.String() != `[{"scope_id":"project:powercontext","display_name":"PowerContext"}]` {
		t.Fatalf("accepted scopes = %d %s", accepted.Code, accepted.Body.String())
	}
}

func TestWebUIMountFailureDoesNotPreventServerStartup(t *testing.T) {
	t.Parallel()
	handler, err := NewHTTPHandler(endpoint.NewHandler(endpoint.HandlerOptions{}), HTTPOptions{
		webUI: &webui.Options{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response := perform(handler, http.MethodGet, "/health/live", ""); response.Code != http.StatusOK {
		t.Fatalf("liveness after Web UI failure = %d %s", response.Code, response.Body.String())
	}
	if response := perform(handler, http.MethodGet, "/", ""); response.Code != http.StatusNotFound {
		t.Fatalf("failed Web UI root = %d %s", response.Code, response.Body.String())
	}
}

func TestHTTPAndMCPMetricsUseBoundedOperationLabels(t *testing.T) {
	t.Parallel()
	observability, err := servermetrics.New()
	if err != nil {
		t.Fatal(err)
	}
	observability.SetReady(true)
	handler, err := NewHTTPHandler(endpoint.NewHandler(endpoint.HandlerOptions{
		Memory: &serverMemoryOperations{},
		Review: &serverReviewOperations{},
	}), HTTPOptions{
		metrics: observability,
		MCP:     MCPOptions{Enabled: true, JSONResponse: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response := perform(handler, http.MethodGet, "/v1/capabilities", ""); response.Code != http.StatusOK {
		t.Fatalf("capabilities = %d %s", response.Code, response.Body.String())
	}
	if response := perform(handler, http.MethodPost, "/v1/memory/flush", `{"scope_id":"secret-scope"}`); response.Code != http.StatusOK {
		t.Fatalf("flush = %d %s", response.Code, response.Body.String())
	}
	if response := perform(handler, http.MethodPost, "/v1/artifact-candidates/list", `{"scope_id":"secret-scope"}`); response.Code != http.StatusOK {
		t.Fatalf("candidates = %d %s", response.Code, response.Body.String())
	}
	if response := perform(handler, http.MethodGet, "/health/live", ""); response.Code != http.StatusOK {
		t.Fatalf("liveness = %d %s", response.Code, response.Body.String())
	}

	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "metrics-test", Version: "1"}, nil)
	session, err := mcpClient.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: "http://powercontext.test/mcp/",
		HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			return response.Result(), nil
		})},
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "list_memory_entries", Arguments: map[string]any{"scope_id": "secret-scope"},
	})
	if err != nil || result.IsError {
		t.Fatalf("MCP call = %#v, %v", result, err)
	}

	exposition := perform(handler, http.MethodGet, "/metrics", "")
	if exposition.Code != http.StatusOK {
		t.Fatalf("metrics = %d %s", exposition.Code, exposition.Body.String())
	}
	text := exposition.Body.String()
	for _, expected := range []string{
		`powercontext_server_transport_requests_total{operation="get_capabilities",outcome="success",transport="http"} 1`,
		`powercontext_server_application_operations_total{operation="get_capabilities",outcome="success"} 1`,
		`powercontext_server_transport_requests_total{operation="flush_memory",outcome="success",transport="http"} 1`,
		`powercontext_server_application_operations_total{operation="flush_memory",outcome="noop"} 1`,
		`powercontext_server_transport_requests_total{operation="list_artifact_candidates",outcome="success",transport="http"} 1`,
		`powercontext_server_application_operations_total{operation="list_artifact_candidates",outcome="success"} 1`,
		`powercontext_server_transport_requests_total{operation="mcp.tools.call",outcome="success",transport="mcp"} 1`,
		`powercontext_server_application_operations_total{operation="list_memory_entries",outcome="success"} 1`,
		`powercontext_server_runtime_ready 1`,
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("metrics do not contain %q:\n%s", expected, text)
		}
	}
	for _, forbidden := range []string{"get_liveness", "secret-scope", "scope_id"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("metrics expose %q:\n%s", forbidden, text)
		}
	}
}

func perform(handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func assertRequestID(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	if !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(response.Header().Get(httpapi.RequestIDHeader)) {
		t.Fatalf("request ID = %q", response.Header().Get(httpapi.RequestIDHeader))
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type invalidCandidateResponseHandler struct{ v1.UnimplementedHandler }

func (invalidCandidateResponseHandler) ProposeExperience(
	context.Context,
	*v1.ProposeExperienceRequest,
) (v1.ProposeExperienceRes, error) {
	candidate := v1.ArtifactCandidate{
		CandidateID: "candidate-1", Version: 1, Family: v1.CandidateFamilyExperience,
		Status: v1.CandidateStatusPending,
		Proposal: v1.NewExperienceProposalArtifactCandidateProposal(v1.ExperienceProposal{
			Situation: "A", Action: "B", Outcome: "C", Lesson: "D",
		}),
		SourceRefs: repeatHTTPValue(v1.SourceReference{Name: "content", SourceID: "task-1"}, 20),
		ArtifactRefs: repeatHTTPValue(v1.ArtifactReference{
			Family: "experience", ArtifactID: "exp-1", Revision: 1,
		}, 13),
	}
	candidate.Target.SetToNull()
	candidate.Reason.SetToNull()
	candidate.ResultArtifact.SetToNull()
	candidate.DecisionReason.SetToNull()
	return &v1.ArtifactCandidateHeaders{Response: candidate}, nil
}

type serverMemoryOperations struct{ endpoint.MemoryOperations }

func (*serverMemoryOperations) Flush(context.Context, string) (runtime.MemoryFlushResult, error) {
	return runtime.MemoryFlushResult{}, nil
}

func (*serverMemoryOperations) List(context.Context, string, bool) (runtime.MemoryEntriesPage, error) {
	return runtime.MemoryEntriesPage{Entries: []runtime.MemoryEntryRecord{}}, nil
}

type serverReviewOperations struct{ endpoint.ReviewOperations }

func (*serverReviewOperations) ListCandidates(
	context.Context,
	string,
	review.Status,
	*string,
	*string,
	int,
) (review.Page, error) {
	return review.Page{Candidates: []review.Snapshot{}}, nil
}
