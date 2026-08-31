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

package mcpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/trace"

	v1 "github.com/ob-labs/powercontext-go/api/v1"
	"github.com/ob-labs/powercontext-go/internal/endpoint"
	serverlogging "github.com/ob-labs/powercontext-go/internal/observability/logging"
	requesttrace "github.com/ob-labs/powercontext-go/internal/observability/tracing"
)

const (
	ServerName = "PowerContext Server"
	// frozenPythonServerVersion is the serverInfo.version emitted by FastMCP
	// 3.4.4 in the Python v0.0.2 Oracle. It is MCP wire compatibility metadata,
	// not the PowerContext Go binary version. Callers may override it explicitly.
	frozenPythonServerVersion = "3.4.4"
)

var errUnexpectedEndpointResponse = errors.New("unexpected endpoint response")

// Options controls the externally visible MCP surface. Handoff Report tools
// are omitted entirely unless the feature is enabled.
type Options struct {
	Version              string
	HandoffReportEnabled bool
	ReceivingMiddleware  []mcp.Middleware
	ApplicationObserver  ApplicationObserver
	ApplicationLogger    *slog.Logger
	TracerProvider       trace.TracerProvider
}

// ApplicationObserver is the consumer-shaped telemetry boundary needed by
// direct MCP endpoint calls.
type ApplicationObserver interface {
	ObserveApplication(operation, outcome string, started time.Time)
}

// HTTPOptions configures the official MCP streamable HTTP transport. The
// defaults preserve sessions and SSE compatibility; JSONResponse is useful for
// simple clients that do not need server-initiated notifications.
type HTTPOptions struct {
	Stateless      bool
	JSONResponse   bool
	SessionTimeout time.Duration
	Logger         *slog.Logger
}

// NewServer registers only the frozen PowerContext MCP tool surface. The MCP
// transport calls the same ogen handler used by HTTP; it never loops back over
// the network.
func NewServer(handler v1.Handler, options Options) (*mcp.Server, error) {
	if handler == nil {
		return nil, errors.New("MCP endpoint handler is required")
	}
	version := options.Version
	if version == "" {
		version = frozenPythonServerVersion
	}
	server := mcp.NewServer(&mcp.Implementation{
		Name:    ServerName,
		Version: version,
	}, nil)
	if len(options.ReceivingMiddleware) > 0 {
		server.AddReceivingMiddleware(options.ReceivingMiddleware...)
	}

	names := make([]string, 0, len(generatedToolSchemas))
	for name, schema := range generatedToolSchemas {
		if schema.HandoffReport && !options.HandoffReportEnabled {
			continue
		}
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		schema := generatedToolSchemas[name]
		toolName := name
		server.AddTool(&mcp.Tool{
			Name:         toolName,
			Description:  schema.Description,
			InputSchema:  schema.Input,
			OutputSchema: schema.Output,
			Annotations:  annotations(toolName),
		}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return runTool(ctx, options, toolName, func(ctx context.Context) (any, error) {
				return dispatch(ctx, handler, toolName, request.Params.Arguments)
			})
		})
	}
	if options.HandoffReportEnabled {
		registerHandoffWorkstreamPicker(server, handler, options)
	}
	return server, nil
}

func runTool(
	ctx context.Context,
	options Options,
	toolName string,
	operation func(context.Context) (any, error),
) (*mcp.CallToolResult, error) {
	ctx = ensureRequestID(ctx)
	ctx, span := requesttrace.StartOperation(
		ctx,
		options.TracerProvider,
		"powercontext "+toolName,
		toolName,
		"application",
		trace.SpanKindInternal,
		"",
	)
	started := time.Now()
	payload, err := operation(ctx)
	if err != nil {
		outcome := "failure"
		if errors.Is(ctx.Err(), context.Canceled) {
			outcome = "cancelled"
		}
		mapped := endpoint.MapError(err)
		serverlogging.LogApplicationCompletion(ctx, options.ApplicationLogger, serverlogging.ApplicationObservation{
			Operation: toolName, Outcome: outcome, Duration: time.Since(started),
			StatusCode: mapped.StatusCode, ErrorCode: mapped.Code,
		})
		span.Finish(outcome, err)
		if options.ApplicationObserver != nil {
			options.ApplicationObserver.ObserveApplication(toolName, "failure", started)
		}
		return errorResult(err), nil
	}
	span.Finish("success", nil)
	if options.ApplicationObserver != nil {
		options.ApplicationObserver.ObserveApplication(toolName, "success", started)
	}
	return successResult(payload)
}

// NewHTTPHandler exposes a server through MCP Streamable HTTP. Go's standard
// same-origin protection is explicit so browser behavior does not depend on an
// SDK compatibility flag; the SDK's localhost DNS-rebinding protection remains
// enabled.
func NewHTTPHandler(server *mcp.Server, options HTTPOptions) http.Handler {
	if server == nil {
		panic("nil MCP server")
	}
	transport := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{
			Stateless:      options.Stateless,
			JSONResponse:   options.JSONResponse,
			SessionTimeout: options.SessionTimeout,
			Logger:         options.Logger,
		},
	)
	protected := http.NewCrossOriginProtection().Handler(transport)
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		ctx := ensureRequestID(request.Context())
		protected.ServeHTTP(writer, request.WithContext(ctx))
	})
}

type validatableRequest interface {
	Validate() error
}

func decodeRequest(raw json.RawMessage, target validatableRequest) error {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return &endpoint.InvalidRequestError{Field: "arguments"}
	}
	if err := target.Validate(); err != nil {
		return &endpoint.InvalidRequestError{Field: "arguments"}
	}
	if err := v1.ValidatePowerContextContract(target); err != nil {
		return &endpoint.InvalidRequestError{Field: "arguments"}
	}
	return nil
}

func dispatch(ctx context.Context, handler v1.Handler, name string, raw json.RawMessage) (any, error) {
	switch name {
	case "acknowledge_handoff":
		request := new(v1.AcknowledgeHandoffRequest)
		if err := decodeRequest(raw, request); err != nil {
			return nil, err
		}
		return endpointPayload(handler.AcknowledgeHandoff(ctx, request))
	case "activate_handoff":
		request := new(v1.ActivateHandoffRequest)
		if err := decodeRequest(raw, request); err != nil {
			return nil, err
		}
		return endpointPayload(handler.ActivateHandoff(ctx, request))
	case "approve_artifact_candidate":
		request := new(v1.ApproveArtifactCandidateRequest)
		if err := decodeRequest(raw, request); err != nil {
			return nil, err
		}
		return endpointPayload(handler.ApproveArtifactCandidate(ctx, request))
	case "capture_content_source":
		request := new(v1.CaptureContentSourceRequest)
		if err := decodeRequest(raw, request); err != nil {
			return nil, err
		}
		return endpointPayload(handler.CaptureContentSource(ctx, request))
	case "commit_handoff":
		request := new(v1.CommitHandoffRequest)
		if err := decodeRequest(raw, request); err != nil {
			return nil, err
		}
		return endpointPayload(handler.CommitHandoff(ctx, request))
	case "continue_handoff":
		request := new(v1.ContinueHandoffRequest)
		if err := decodeRequest(raw, request); err != nil {
			return nil, err
		}
		return endpointPayload(handler.ContinueHandoff(ctx, request))
	case "create_work_contract":
		request := new(v1.CreateWorkContractRequest)
		if err := decodeRequest(raw, request); err != nil {
			return nil, err
		}
		return endpointPayload(handler.CreateWorkContract(ctx, request))
	case "finalize_handoff":
		request := new(v1.FinalizeHandoffRequest)
		if err := decodeRequest(raw, request); err != nil {
			return nil, err
		}
		return endpointPayload(handler.FinalizeHandoff(ctx, request))
	case "get_artifact_candidate":
		request := new(v1.GetArtifactCandidateRequest)
		if err := decodeRequest(raw, request); err != nil {
			return nil, err
		}
		return endpointPayload(handler.GetArtifactCandidate(ctx, request))
	case "get_handoff_report":
		request := new(v1.GetHandoffReportRequest)
		if err := decodeRequest(raw, request); err != nil {
			return nil, err
		}
		return endpointPayload(handler.GetHandoffReport(ctx, request))
	case "get_handoff_report_workspace":
		request := new(v1.GetHandoffReportWorkspaceRequest)
		if err := decodeRequest(raw, request); err != nil {
			return nil, err
		}
		return endpointPayload(handler.GetHandoffReportWorkspace(ctx, request))
	case "get_memory_entry":
		request := new(v1.GetMemoryEntryRequest)
		if err := decodeRequest(raw, request); err != nil {
			return nil, err
		}
		return endpointPayload(handler.GetMemoryEntry(ctx, request))
	case "handoff_current_work":
		request := new(v1.HandoffCurrentWorkRequest)
		if err := decodeRequest(raw, request); err != nil {
			return nil, err
		}
		return endpointPayload(handler.HandoffCurrentWork(ctx, request))
	case "list_artifact_candidates":
		request := new(v1.ListArtifactCandidatesRequest)
		if err := decodeRequest(raw, request); err != nil {
			return nil, err
		}
		return endpointPayload(handler.ListArtifactCandidates(ctx, request))
	case "list_memory_entries":
		request := new(v1.ListMemoryEntriesRequest)
		if err := decodeRequest(raw, request); err != nil {
			return nil, err
		}
		return endpointPayload(handler.ListMemoryEntries(ctx, request))
	case "list_handoff_report_known_scopes":
		request := new(v1.ListHandoffReportKnownScopesRequest)
		if err := decodeRequest(raw, request); err != nil {
			return nil, err
		}
		return endpointPayload(handler.ListHandoffReportKnownScopes(ctx, request))
	case "record_task_outcome":
		request := new(v1.RecordTaskOutcomeRequest)
		if err := decodeRequest(raw, request); err != nil {
			return nil, err
		}
		return endpointPayload(handler.RecordTaskOutcome(ctx, request))
	case "reject_artifact_candidate":
		request := new(v1.RejectArtifactCandidateRequest)
		if err := decodeRequest(raw, request); err != nil {
			return nil, err
		}
		return endpointPayload(handler.RejectArtifactCandidate(ctx, request))
	case "remember_memory":
		request := new(v1.RememberMemoryRequest)
		if err := decodeRequest(raw, request); err != nil {
			return nil, err
		}
		return endpointPayload(handler.RememberMemory(ctx, request))
	case "retire_memory_entry":
		request := new(v1.RetireMemoryEntryRequest)
		if err := decodeRequest(raw, request); err != nil {
			return nil, err
		}
		return endpointPayload(handler.RetireMemoryEntry(ctx, request))
	case "revise_artifact_candidate":
		request := new(v1.ReviseArtifactCandidateRequest)
		if err := decodeRequest(raw, request); err != nil {
			return nil, err
		}
		return endpointPayload(handler.ReviseArtifactCandidate(ctx, request))
	case "revise_memory_entry":
		request := new(v1.ReviseMemoryEntryRequest)
		if err := decodeRequest(raw, request); err != nil {
			return nil, err
		}
		return endpointPayload(handler.ReviseMemoryEntry(ctx, request))
	case "search_memory":
		request := new(v1.SearchMemoryRequest)
		if err := decodeRequest(raw, request); err != nil {
			return nil, err
		}
		return endpointPayload(handler.SearchMemory(ctx, request))
	default:
		return nil, errUnexpectedEndpointResponse
	}
}

// endpointPayload intentionally accepts a result and error as separate
// arguments, allowing callers to pass a two-value endpoint invocation directly.
func endpointPayload(response any, err error) (any, error) {
	if err != nil {
		return nil, err
	}
	if err := v1.ValidatePowerContextContract(response); err != nil {
		return nil, fmt.Errorf("MCP response violates the PowerContext contract: %w", err)
	}
	switch value := response.(type) {
	case *v1.CaptureContentSourceResponseHeaders:
		return value.Response, nil
	case *v1.HandoffActivationHeaders:
		return value.Response, nil
	case *v1.PreparedHandoffHeaders:
		return value.Response, nil
	case *v1.CommittedHandoffHeaders:
		return value.Response, nil
	case *v1.HandoffResolutionHeaders:
		return value.Response, nil
	case *v1.WorkSourceReceiptHeaders:
		return value.Response, nil
	case *v1.PreparedWorkHandoffHeaders:
		return value.Response, nil
	case *v1.HandoffAcknowledgementHeaders:
		return value.Response, nil
	case *v1.SearchMemoryResponseHeaders:
		return value.Response, nil
	case *v1.ListMemoryEntriesResponseHeaders:
		return value.Response, nil
	case *v1.MemoryEntryHeaders:
		return value.Response, nil
	case *v1.MemoryMutationResponseHeaders:
		return value.Response, nil
	case *v1.ArtifactCandidatePageHeaders:
		return value.Response, nil
	case *v1.ArtifactCandidateHeaders:
		return value.Response, nil
	case *v1.HandoffReportWorkspaceBindingHeaders:
		return value.Response, nil
	case *v1.KnownHandoffScopePageHeaders:
		return value.Response, nil
	case *v1.HandoffReportResponseHeaders:
		return value.Response, nil
	case *v1.GetHandoffReportOKTextMarkdownHeaders:
		markdown, readErr := io.ReadAll(io.LimitReader(value.Response, 10<<20+1))
		if readErr != nil || len(markdown) > 10<<20 {
			return nil, errUnexpectedEndpointResponse
		}
		selectionDigest, selectionOK := value.XPowerContextSelectionDigest.Get()
		reportDigest, reportOK := value.XPowerContextReportDigest.Get()
		if !selectionOK || !reportOK {
			return nil, errUnexpectedEndpointResponse
		}
		result := v1.HandoffReportResponse{
			Format:          v1.ReportFormatMarkdown,
			Markdown:        v1.NewNilString(string(markdown)),
			SelectionDigest: selectionDigest,
			ReportDigest:    reportDigest,
		}
		result.Report.SetToNull()
		return result, nil
	default:
		return nil, errUnexpectedEndpointResponse
	}
}

func successResult(payload any) (*mcp.CallToolResult, error) {
	encoded, structured, err := structuredJSON(payload)
	if err != nil {
		return errorResult(errUnexpectedEndpointResponse), nil
	}
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: string(encoded)}},
		StructuredContent: structured,
	}, nil
}

func errorResult(err error) *mcp.CallToolResult {
	mapping := endpoint.MapError(err)
	payload := map[string]any{
		"error": map[string]any{
			"code":    mapping.Code,
			"message": mapping.Message,
			"details": mapping.Details,
		},
	}
	encoded, structured, marshalErr := structuredJSON(payload)
	if marshalErr != nil {
		encoded = []byte(`{"error":{"code":"internal_error","message":"The Server failed.","details":null}}`)
		structured = map[string]any{
			"error": map[string]any{
				"code": "internal_error", "message": "The Server failed.", "details": nil,
			},
		}
	}
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: string(encoded)}},
		StructuredContent: structured,
		IsError:           true,
	}
}

func structuredJSON(value any) ([]byte, map[string]any, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, nil, fmt.Errorf("encode structured MCP result: %w", err)
	}
	var structured map[string]any
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if err := decoder.Decode(&structured); err != nil {
		return nil, nil, fmt.Errorf("decode structured MCP result: %w", err)
	}
	return encoded, structured, nil
}

func ensureRequestID(ctx context.Context) context.Context {
	if _, ok := requesttrace.RequestID(ctx); ok {
		return ctx
	}
	var value [8]byte
	for {
		if _, err := rand.Read(value[:]); err != nil {
			return requesttrace.WithRequestID(ctx, "0000000000000001")
		}
		var nonzero byte
		for _, current := range value {
			nonzero |= current
		}
		if nonzero != 0 {
			return requesttrace.WithRequestID(ctx, hex.EncodeToString(value[:]))
		}
	}
}

func annotations(name string) *mcp.ToolAnnotations {
	closedWorld := false
	switch name {
	case "continue_handoff", "get_artifact_candidate", "get_handoff_report", "get_handoff_report_workspace",
		"get_memory_entry", "list_artifact_candidates", "list_handoff_report_known_scopes", "list_memory_entries",
		"search_memory", "select_handoff_workstream":
		value := &mcp.ToolAnnotations{OpenWorldHint: &closedWorld}
		value.ReadOnlyHint = true
		nondestructive := false
		value.DestructiveHint = &nondestructive
		if name == "select_handoff_workstream" {
			value.IdempotentHint = true
		}
		return value
	case "handoff_current_work":
		value := &mcp.ToolAnnotations{OpenWorldHint: &closedWorld}
		nondestructive := false
		value.DestructiveHint = &nondestructive
		value.IdempotentHint = false
		return value
	case "commit_handoff":
		value := &mcp.ToolAnnotations{OpenWorldHint: &closedWorld}
		nondestructive := false
		value.DestructiveHint = &nondestructive
		value.IdempotentHint = true
		return value
	case "approve_artifact_candidate", "reject_artifact_candidate", "revise_artifact_candidate":
		value := &mcp.ToolAnnotations{OpenWorldHint: &closedWorld}
		destructive := true
		value.DestructiveHint = &destructive
		value.IdempotentHint = true
		return value
	default:
		return nil
	}
}
