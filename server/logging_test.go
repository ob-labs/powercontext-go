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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/ob-labs/powercontext-go/internal/endpoint"
	serverlogging "github.com/ob-labs/powercontext-go/internal/observability/logging"
	requesttrace "github.com/ob-labs/powercontext-go/internal/observability/tracing"
	"github.com/ob-labs/powercontext-go/internal/runtime"
	"github.com/ob-labs/powercontext-go/internal/scheduler"
)

func TestHTTPAccessLogCorrelatesWithIngressSpanAndSkipsInfrastructure(t *testing.T) {
	var output bytes.Buffer
	logger := newJSONTestLogger(t, &output)
	spans := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithSpanProcessor(spans),
	)
	t.Cleanup(func() { _ = provider.Shutdown(t.Context()) })
	handler, err := NewHTTPHandler(endpoint.NewHandler(endpoint.HandlerOptions{}), HTTPOptions{
		Logger: logger, AccessLog: true, TracerProvider: provider,
	})
	if err != nil {
		t.Fatal(err)
	}

	response := perform(handler, http.MethodGet, "/v1/capabilities", "")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	if got := perform(handler, http.MethodGet, "/health/live", ""); got.Code != http.StatusOK {
		t.Fatalf("liveness = %d: %s", got.Code, got.Body.String())
	}
	if got := perform(handler, http.MethodGet, "/metrics", ""); got.Code != http.StatusNotFound {
		t.Fatalf("metrics = %d: %s", got.Code, got.Body.String())
	}

	records := decodeLogRecords(t, output.String())
	if len(records) != 1 {
		t.Fatalf("records = %#v, want one external HTTP record", records)
	}
	record := records[0]
	assertLogFields(t, record, map[string]any{
		"level": "INFO", "logger": "powercontext.server.access",
		"event": serverlogging.TransportCompletedEvent, "operation": "get_capabilities",
		"outcome": "success", "transport": "http", "unit": "transport", "status_code": float64(200),
	})
	requestID := response.Header().Get("X-PowerContext-Request-ID")
	if record["request_id"] != requestID || record["span_id"] != requestID {
		t.Fatalf("correlation = request %#v span %#v, want %q", record["request_id"], record["span_id"], requestID)
	}
	transport := endedSpan(t, spans.Ended(), "HTTP get_capabilities")
	if record["trace_id"] != transport.SpanContext().TraceID().String() {
		t.Fatalf("trace_id = %#v, want %s", record["trace_id"], transport.SpanContext().TraceID())
	}
}

func TestHTTPFailureLogsApplicationAndTransportWithoutErrorContent(t *testing.T) {
	const secret = "secret provider response and scope project:private"
	var output bytes.Buffer
	logger := newJSONTestLogger(t, &output)
	handler, err := NewHTTPHandler(endpoint.NewHandler(endpoint.HandlerOptions{
		Capabilities: func(context.Context) (runtime.Capabilities, error) { return runtime.Capabilities{}, errors.New(secret) },
	}), HTTPOptions{Logger: logger, AccessLog: true})
	if err != nil {
		t.Fatal(err)
	}
	response := perform(handler, http.MethodGet, "/v1/capabilities", "")
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	assertRequestID(t, response)
	if strings.Contains(output.String(), secret) || strings.Contains(output.String(), "project:private") {
		t.Fatalf("log leaked failure content: %s", output.String())
	}
	records := decodeLogRecords(t, output.String())
	if len(records) != 2 {
		t.Fatalf("records = %#v, want application and transport failures", records)
	}
	application := findLogRecord(t, records, serverlogging.ApplicationCompletedEvent, "get_capabilities")
	access := findLogRecord(t, records, serverlogging.TransportCompletedEvent, "get_capabilities")
	assertLogFields(t, application, map[string]any{
		"level": "ERROR", "logger": "powercontext.server.app", "outcome": "failure",
		"unit": "application", "error_code": "internal_error",
	})
	assertLogFields(t, access, map[string]any{
		"level": "ERROR", "logger": "powercontext.server.access", "outcome": "failure",
		"transport": "http", "unit": "transport", "status_code": float64(500),
	})
	requestID := response.Header().Get("X-PowerContext-Request-ID")
	if application["request_id"] != requestID || access["request_id"] != requestID {
		t.Fatalf("request IDs = app %#v access %#v response %q", application["request_id"], access["request_id"], requestID)
	}
}

func TestHTTPDecodeAndAuthenticationFailuresOnlyProduceTransportLogs(t *testing.T) {
	var output bytes.Buffer
	handler, err := NewHTTPHandler(endpoint.NewHandler(endpoint.HandlerOptions{}), HTTPOptions{
		BearerToken: "server-secret", HandoffReportRoutes: true,
		Logger: newJSONTestLogger(t, &output), AccessLog: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	unauthorized := perform(handler, http.MethodGet, "/v1/capabilities", "")
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized = %d: %s", unauthorized.Code, unauthorized.Body.String())
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/memory/search", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer server-secret")
	invalid := httptest.NewRecorder()
	handler.ServeHTTP(invalid, request)
	if invalid.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid = %d: %s", invalid.Code, invalid.Body.String())
	}
	records := decodeLogRecords(t, output.String())
	if len(records) != 2 {
		t.Fatalf("records = %#v, want two transport failures", records)
	}
	for _, record := range records {
		if record["event"] != serverlogging.TransportCompletedEvent || record["transport"] != "http" || record["outcome"] != "failure" {
			t.Fatalf("unexpected record: %#v", record)
		}
	}
}

func TestMCPLogsLogicalCallWithoutStreamableHTTPDuplicate(t *testing.T) {
	const secret = "secret MCP backend failure"
	var output bytes.Buffer
	logger := newJSONTestLogger(t, &output)
	provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()))
	t.Cleanup(func() { _ = provider.Shutdown(t.Context()) })
	handler, err := NewHTTPHandler(endpoint.NewHandler(endpoint.HandlerOptions{
		Memory: &failingServerMemoryOperations{err: errors.New(secret)},
	}), HTTPOptions{
		Logger: logger, AccessLog: true, TracerProvider: provider,
		MCP: MCPOptions{Enabled: true, JSONResponse: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "logging-test", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
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
		Name: "list_memory_entries", Arguments: map[string]any{"scope_id": "project:private"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatal("failing MCP application returned success")
	}
	if strings.Contains(output.String(), secret) || strings.Contains(output.String(), "project:private") {
		t.Fatalf("MCP log leaked content: %s", output.String())
	}
	records := decodeLogRecords(t, output.String())
	transport := findLogRecord(t, records, serverlogging.TransportCompletedEvent, "mcp.tools.call")
	application := findLogRecord(t, records, serverlogging.ApplicationCompletedEvent, "list_memory_entries")
	assertLogFields(t, transport, map[string]any{
		"level": "INFO", "logger": "powercontext.server.access", "outcome": "failure",
		"transport": "mcp", "unit": "transport",
	})
	assertLogFields(t, application, map[string]any{
		"level": "ERROR", "logger": "powercontext.server.app", "outcome": "failure",
		"unit": "application", "error_code": "internal_error",
	})
	if transport["request_id"] == nil || transport["request_id"] != application["request_id"] {
		t.Fatalf("MCP request correlation = transport %#v application %#v", transport["request_id"], application["request_id"])
	}
	for _, record := range records {
		if record["event"] == serverlogging.TransportCompletedEvent && record["transport"] == "http" {
			t.Fatalf("MCP Streamable HTTP frame was double logged: %#v", record)
		}
	}
}

func TestReadinessLogsOnlyStateTransitions(t *testing.T) {
	var output bytes.Buffer
	application := &Application{logger: newJSONTestLogger(t, &output)}
	ctx := requesttrace.WithRequestID(context.Background(), "0123456789abcdef")
	for _, status := range []runtime.ReadinessStatus{
		runtime.Ready, runtime.Ready, runtime.Degraded, runtime.Degraded, runtime.NotReady, runtime.NotReady,
	} {
		application.observeReadiness(ctx, status)
	}
	records := decodeLogRecords(t, output.String())
	if len(records) != 3 {
		t.Fatalf("readiness records = %#v, want three transitions", records)
	}
	for index, event := range []string{"server.ready", "server.degraded", "server.not_ready"} {
		assertLogFields(t, records[index], map[string]any{
			"level": "INFO", "logger": "powercontext.server.factory", "event": event,
			"unit": "server", "request_id": "0123456789abcdef",
		})
	}
}

func TestScheduledNoopLogsBoundedOutcome(t *testing.T) {
	var output bytes.Buffer
	observer := scheduledObserver(newJSONTestLogger(t, &output))
	observer(context.Background(), runtime.ScheduledObservation{
		Operation: runtime.ProcessSourceWindowOperation,
		Outcome:   runtime.ScheduledProcessingNoop,
		Duration:  1250 * time.Microsecond,
	})

	records := decodeLogRecords(t, output.String())
	if len(records) != 1 {
		t.Fatalf("records = %#v", records)
	}
	assertLogFields(t, records[0], map[string]any{
		"event": "background.operation.completed", "operation": "process_source_window",
		"outcome": "noop", "unit": "background", "source_count": float64(0),
		"duration_ms": 1.25,
	})
	if _, found := records[0]["scope_id"]; found {
		t.Fatalf("scheduled log contains scope_id: %#v", records[0])
	}
}

func TestScheduledExperienceLogsCandidateCountWithoutScope(t *testing.T) {
	var output bytes.Buffer
	observer := scheduledObserver(newJSONTestLogger(t, &output))
	candidates := 1
	observer(context.Background(), runtime.ScheduledObservation{
		Operation:   runtime.IncubateExperienceOperation,
		Outcome:     runtime.ScheduledProcessingSuccess,
		Duration:    2 * time.Millisecond,
		SourceCount: 1, CandidateCount: &candidates,
	})

	records := decodeLogRecords(t, output.String())
	if len(records) != 1 {
		t.Fatalf("records = %#v", records)
	}
	assertLogFields(t, records[0], map[string]any{
		"event": "background.operation.completed", "operation": "incubate_experience_candidates",
		"outcome": "success", "unit": "background", "source_count": float64(1),
		"candidate_count": float64(1),
	})
	for _, forbidden := range []string{"scope_id", "scope", "content", "prompt", "path"} {
		if _, found := records[0][forbidden]; found {
			t.Fatalf("scheduled log contains %q: %#v", forbidden, records[0])
		}
	}
}

func TestScheduledDispatchCancellationDoesNotFloodLogs(t *testing.T) {
	var output bytes.Buffer
	observer := scheduledRunErrorObserver(newJSONTestLogger(t, &output))
	for range 3 {
		observer(scheduler.RunError{Kind: scheduler.SourceWindow, Err: context.Canceled})
	}
	if output.Len() != 0 {
		t.Fatalf("canceled dispatch logs = %s", output.String())
	}

	observer(scheduler.RunError{Kind: scheduler.SourceWindow, Err: errors.New("secret scheduler failure")})
	records := decodeLogRecords(t, output.String())
	if len(records) != 1 {
		t.Fatalf("records = %#v, want one unexpected dispatch failure", records)
	}
	assertLogFields(t, records[0], map[string]any{
		"level": "ERROR", "event": "background.dispatch.failed", "operation": "source-window",
		"outcome": "failure", "unit": "background", "error_code": "scheduler",
	})
	if strings.Contains(output.String(), "secret scheduler failure") {
		t.Fatalf("dispatch log leaked error text: %s", output.String())
	}
}

func TestLoggingPanicDoesNotChangeApplicationResponse(t *testing.T) {
	logger := slog.New(panicLogHandler{})
	handler, err := NewHTTPHandler(endpoint.NewHandler(endpoint.HandlerOptions{
		Capabilities: func(context.Context) (runtime.Capabilities, error) {
			return runtime.Capabilities{}, errors.New("application failure")
		},
	}), HTTPOptions{Logger: logger, AccessLog: true})
	if err != nil {
		t.Fatal(err)
	}
	response := perform(handler, http.MethodGet, "/v1/capabilities", "")
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	assertRequestID(t, response)
}

func TestInMemoryMainDatabaseEmitsOneBoundedDataLossWarning(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	logger := newJSONTestLogger(t, &output)
	config := ProcessConfig{Database: DatabaseConfig{
		Kind: "sqlite", SQLite: SQLiteDatabaseConfig{URL: "sqlite+aiosqlite:///:memory:"},
	}}
	warnIfEphemeralMainDatabase(t.Context(), config, logger)
	records := decodeLogRecords(t, output.String())
	if len(records) != 1 {
		t.Fatalf("records = %#v", records)
	}
	assertLogFields(t, records[0], map[string]any{
		"level": "WARN", "logger": "powercontext.server.factory", "event": "database.ephemeral",
		"outcome": "warning", "unit": "database",
	})
	message, _ := records[0]["message"].(string)
	if !strings.Contains(message, "all main database data will be lost when the process stops") ||
		strings.Contains(output.String(), ":memory:") {
		t.Fatalf("warning is missing or leaks the DSN: %s", output.String())
	}

	output.Reset()
	config.Database.SQLite.URL = "sqlite+aiosqlite:///powercontext.db"
	warnIfEphemeralMainDatabase(t.Context(), config, logger)
	if output.Len() != 0 {
		t.Fatalf("persistent database warning = %s", output.String())
	}
}

func newJSONTestLogger(t *testing.T, output *bytes.Buffer) *slog.Logger {
	t.Helper()
	logger, err := serverlogging.New(serverlogging.Config{Format: serverlogging.JSON, Level: slog.LevelDebug, Writer: output})
	if err != nil {
		t.Fatal(err)
	}
	return logger
}

func decodeLogRecords(t *testing.T, output string) []map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil
	}
	records := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("decode log %q: %v", line, err)
		}
		records = append(records, record)
	}
	return records
}

func findLogRecord(t *testing.T, records []map[string]any, event, operation string) map[string]any {
	t.Helper()
	for _, record := range records {
		if record["event"] == event && record["operation"] == operation {
			return record
		}
	}
	t.Fatalf("log event %q operation %q not found in %#v", event, operation, records)
	return nil
}

func assertLogFields(t *testing.T, record map[string]any, want map[string]any) {
	t.Helper()
	for key, value := range want {
		if record[key] != value {
			t.Fatalf("field %q = %#v, want %#v in %#v", key, record[key], value, record)
		}
	}
}

type failingServerMemoryOperations struct {
	endpoint.MemoryOperations
	err error
}

func (m *failingServerMemoryOperations) List(context.Context, string, bool) (runtime.MemoryEntriesPage, error) {
	return runtime.MemoryEntriesPage{}, m.err
}

type panicLogHandler struct{}

func (panicLogHandler) Enabled(context.Context, slog.Level) bool  { return true }
func (panicLogHandler) Handle(context.Context, slog.Record) error { panic("logging failed") }
func (panicLogHandler) WithAttrs([]slog.Attr) slog.Handler        { return panicLogHandler{} }
func (panicLogHandler) WithGroup(string) slog.Handler             { return panicLogHandler{} }
