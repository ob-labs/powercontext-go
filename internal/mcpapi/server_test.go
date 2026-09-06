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
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	v1 "github.com/ob-labs/powercontext-go/api/v1"
	"github.com/ob-labs/powercontext-go/internal/endpoint"
	requesttrace "github.com/ob-labs/powercontext-go/internal/observability/tracing"
	"github.com/ob-labs/powercontext-go/internal/runtime"
	"github.com/ob-labs/powercontext-go/internal/scope"
)

var baseToolNames = []string{
	"acknowledge_handoff",
	"activate_handoff",
	"approve_artifact_candidate",
	"capture_content_source",
	"clear_scope_binding",
	"commit_handoff",
	"continue_handoff",
	"create_work_contract",
	"finalize_handoff",
	"get_artifact_candidate",
	"get_memory_entry",
	"handoff_current_work",
	"list_artifact_candidates",
	"list_memory_entries",
	"reject_artifact_candidate",
	"record_task_outcome",
	"remember_memory",
	"resolve_scope_binding",
	"retire_memory_entry",
	"revise_artifact_candidate",
	"revise_memory_entry",
	"search_memory",
	"set_scope_binding",
}

func TestDefaultServerInfoMatchesFrozenPython(t *testing.T) {
	t.Parallel()
	server, err := NewServer(endpoint.NewHandler(endpoint.HandlerOptions{}), Options{})
	if err != nil {
		t.Fatal(err)
	}
	client := connectInMemory(t, server)
	info := client.InitializeResult().ServerInfo
	if info == nil || info.Name != ServerName || info.Version != frozenPythonServerVersion {
		t.Fatalf("serverInfo = %#v, want name %q version %q", info, ServerName, frozenPythonServerVersion)
	}
}

func TestHandoffToolAnnotationsMatchFrozenHostApprovalSemantics(t *testing.T) {
	t.Parallel()
	server, err := NewServer(endpoint.NewHandler(endpoint.HandlerOptions{}), Options{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := connectInMemory(t, server).ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	tools := make(map[string]*mcp.Tool, len(result.Tools))
	for _, tool := range result.Tools {
		tools[tool.Name] = tool
	}
	prepare, commit, resolve := tools["handoff_current_work"], tools["commit_handoff"], tools["continue_handoff"]
	if prepare.Annotations == nil || prepare.Annotations.ReadOnlyHint || prepare.Annotations.DestructiveHint == nil ||
		*prepare.Annotations.DestructiveHint || prepare.Annotations.IdempotentHint ||
		prepare.Annotations.OpenWorldHint == nil || *prepare.Annotations.OpenWorldHint {
		t.Fatalf("handoff_current_work annotations = %#v", prepare.Annotations)
	}
	if commit.Annotations == nil || commit.Annotations.ReadOnlyHint || commit.Annotations.DestructiveHint == nil ||
		*commit.Annotations.DestructiveHint || !commit.Annotations.IdempotentHint ||
		commit.Annotations.OpenWorldHint == nil || *commit.Annotations.OpenWorldHint {
		t.Fatalf("commit_handoff annotations = %#v", commit.Annotations)
	}
	if resolve.Annotations == nil || !resolve.Annotations.ReadOnlyHint || resolve.Annotations.DestructiveHint == nil ||
		*resolve.Annotations.DestructiveHint || resolve.Annotations.OpenWorldHint == nil || *resolve.Annotations.OpenWorldHint {
		t.Fatalf("continue_handoff annotations = %#v", resolve.Annotations)
	}
	if tools["capture_content_source"].Annotations != nil {
		t.Fatalf("unannotated tool received Go-only defaults: %#v", tools["capture_content_source"].Annotations)
	}
}

func TestReviewWriteToolAnnotationsMatchUpstreamHostApprovalSemantics(t *testing.T) {
	t.Parallel()
	server, err := NewServer(endpoint.NewHandler(endpoint.HandlerOptions{}), Options{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := connectInMemory(t, server).ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	tools := make(map[string]*mcp.Tool, len(result.Tools))
	for _, tool := range result.Tools {
		tools[tool.Name] = tool
	}
	for _, name := range []string{
		"approve_artifact_candidate",
		"reject_artifact_candidate",
		"revise_artifact_candidate",
	} {
		tool := tools[name]
		if tool == nil {
			t.Fatalf("MCP tool %q is missing", name)
		}
		decision := tool.Annotations
		if decision == nil || decision.ReadOnlyHint || decision.DestructiveHint == nil ||
			!*decision.DestructiveHint || !decision.IdempotentHint ||
			decision.OpenWorldHint == nil || *decision.OpenWorldHint {
			t.Fatalf("%s annotations = %#v", name, decision)
		}
	}
}

func TestScopeBindingToolAnnotationsReflectReadAndIdempotentWrites(t *testing.T) {
	t.Parallel()
	server, err := NewServer(endpoint.NewHandler(endpoint.HandlerOptions{}), Options{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := connectInMemory(t, server).ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	tools := make(map[string]*mcp.Tool, len(result.Tools))
	for _, tool := range result.Tools {
		tools[tool.Name] = tool
	}
	read := tools["resolve_scope_binding"]
	if read == nil || read.Annotations == nil || !read.Annotations.ReadOnlyHint || read.Annotations.DestructiveHint == nil ||
		*read.Annotations.DestructiveHint || read.Annotations.OpenWorldHint == nil || *read.Annotations.OpenWorldHint {
		t.Fatalf("resolve_scope_binding annotations = %#v", read)
	}
	for _, name := range []string{"set_scope_binding", "clear_scope_binding"} {
		tool := tools[name]
		if tool == nil || tool.Annotations == nil || tool.Annotations.ReadOnlyHint || tool.Annotations.DestructiveHint == nil ||
			*tool.Annotations.DestructiveHint || !tool.Annotations.IdempotentHint || tool.Annotations.OpenWorldHint == nil || *tool.Annotations.OpenWorldHint {
			t.Fatalf("%s annotations = %#v", name, tool)
		}
	}
}

func TestScopeBindingToolsDispatchThroughSharedEndpoint(t *testing.T) {
	t.Parallel()
	descriptor, err := scope.NewDescriptor("scope-1", "Repository", "Repository context", "", nil, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	operations := &mcpScopeOperations{resolved: descriptor}
	handler := endpoint.NewHandler(endpoint.HandlerOptions{Scopes: operations})
	server, err := NewServer(handler, Options{})
	if err != nil {
		t.Fatal(err)
	}
	client := connectInMemory(t, server)
	key := map[string]any{"integration": "codex", "kind": "project", "external_id": "repository"}
	set, err := client.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "set_scope_binding", Arguments: map[string]any{"key": key, "scope_id": "scope-1"},
	})
	if err != nil || set.IsError {
		t.Fatalf("set_scope_binding = %#v, %v", set, err)
	}
	if !reflect.DeepEqual(set.StructuredContent, map[string]any{"key": key, "scope_id": "scope-1"}) {
		t.Fatalf("set structured content = %#v", set.StructuredContent)
	}
	resolve, err := client.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "resolve_scope_binding", Arguments: map[string]any{},
	})
	if err != nil || resolve.IsError {
		t.Fatalf("resolve_scope_binding = %#v, %v", resolve, err)
	}
	resolved, ok := resolve.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("resolve structured content type = %T", resolve.StructuredContent)
	}
	if got := resolved["scope_id"]; got != "scope-1" {
		t.Fatalf("resolve scope_id = %#v", got)
	}
	if parent, present := resolved["parent_scope_id"]; !present || parent != nil {
		t.Fatalf("resolve parent_scope_id = %#v, want null", parent)
	}
	clear, err := client.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "clear_scope_binding", Arguments: map[string]any{"key": key},
	})
	if err != nil || clear.IsError || !reflect.DeepEqual(clear.StructuredContent, map[string]any{"cleared": true}) {
		t.Fatalf("clear_scope_binding = %#v, %v", clear, err)
	}
	legacy, err := client.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "clear_scope_binding", Arguments: map[string]any{"binding_key": key},
	})
	if err != nil || !legacy.IsError {
		t.Fatalf("legacy binding_key clear = %#v, %v", legacy, err)
	}
}

func TestServerExposesFrozenToolSet(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name          string
		reports       bool
		expectedTools []string
	}{
		{name: "base", expectedTools: baseToolNames},
		{
			name:    "handoff-report-enabled",
			reports: true,
			expectedTools: append(slices.Clone(baseToolNames),
				"get_handoff_report", "get_handoff_report_workspace", "list_handoff_report_known_scopes",
				"select_handoff_workstream"),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server, err := NewServer(endpoint.NewHandler(endpoint.HandlerOptions{}), Options{
				HandoffReportEnabled: test.reports,
			})
			if err != nil {
				t.Fatal(err)
			}
			client := connectInMemory(t, server)
			result, err := client.ListTools(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			got := make([]string, len(result.Tools))
			for index, tool := range result.Tools {
				got[index] = tool.Name
				if tool.InputSchema == nil || tool.OutputSchema == nil {
					t.Fatalf("tool %q is missing a schema", tool.Name)
				}
			}
			slices.Sort(got)
			expected := slices.Clone(test.expectedTools)
			slices.Sort(expected)
			if !slices.Equal(got, expected) {
				t.Fatalf("tools = %v, want %v", got, expected)
			}

			resources, err := client.ListResources(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(resources.Resources) != 0 {
				t.Fatalf("resources = %d, want 0", len(resources.Resources))
			}
			prompts, err := client.ListPrompts(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(prompts.Prompts) != 0 {
				t.Fatalf("prompts = %d, want 0", len(prompts.Prompts))
			}
		})
	}
}

func TestToolSchemasPreserveDefaultsAndNestedCitations(t *testing.T) {
	t.Parallel()

	server, err := NewServer(endpoint.NewHandler(endpoint.HandlerOptions{}), Options{})
	if err != nil {
		t.Fatal(err)
	}
	client := connectInMemory(t, server)
	result, err := client.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	tools := make(map[string]*mcp.Tool, len(result.Tools))
	for _, tool := range result.Tools {
		tools[tool.Name] = tool
	}

	listProperties := schemaProperties(t, tools["list_memory_entries"].InputSchema)
	includeInactive := schemaObject(t, listProperties["include_inactive"])
	if value, ok := includeInactive["default"].(bool); !ok || value {
		t.Fatalf("include_inactive default = %#v, want false", includeInactive["default"])
	}

	for _, name := range []string{"get_memory_entry", "revise_memory_entry", "retire_memory_entry"} {
		properties := schemaProperties(t, tools[name].InputSchema)
		if _, found := properties["memory_id"]; found {
			t.Fatalf("%s unexpectedly exposes memory_id", name)
		}
		citation := schemaProperties(t, properties["citation"])
		got := make([]string, 0, len(citation))
		for property := range citation {
			got = append(got, property)
		}
		slices.Sort(got)
		want := []string{"entry_id", "entry_version_id", "memory_ref"}
		if !slices.Equal(got, want) {
			t.Fatalf("%s citation properties = %v, want %v", name, got, want)
		}
	}
}

func TestToolCallUsesEndpointDefaultsAndStructuredContent(t *testing.T) {
	t.Parallel()

	memory := new(memoryOperationsStub)
	handler := endpoint.NewHandler(endpoint.HandlerOptions{Memory: memory})
	server, err := NewServer(handler, Options{})
	if err != nil {
		t.Fatal(err)
	}
	client := connectInMemory(t, server)
	result, err := client.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "list_memory_entries",
		Arguments: map[string]any{"scope_id": "project:empty"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("tool returned error: %#v", result.Content)
	}
	want := map[string]any{"memory": nil, "entries": []any{}}
	if !reflect.DeepEqual(result.StructuredContent, want) {
		t.Fatalf("structured content = %#v, want %#v", result.StructuredContent, want)
	}
	if memory.scopeID != "project:empty" || memory.includeInactive {
		t.Fatalf("endpoint arguments = (%q, %t), want (project:empty, false)", memory.scopeID, memory.includeInactive)
	}
	if memory.requestID == "" || memory.requestID == "0000000000000000" {
		t.Fatalf("logical request ID = %q", memory.requestID)
	}
	content, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content type = %T, want *mcp.TextContent", result.Content[0])
	}
	var text map[string]any
	if err := json.Unmarshal([]byte(content.Text), &text); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(text, want) {
		t.Fatalf("text content = %#v, want %#v", text, want)
	}
}

func TestToolValidationReturnsStableErrorEnvelope(t *testing.T) {
	t.Parallel()

	server, err := NewServer(endpoint.NewHandler(endpoint.HandlerOptions{}), Options{})
	if err != nil {
		t.Fatal(err)
	}
	client := connectInMemory(t, server)
	result, err := client.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "list_memory_entries",
		Arguments: map[string]any{"scope_id": " "},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatal("invalid arguments did not produce a tool error")
	}
	want := map[string]any{
		"error": map[string]any{
			"code": "invalid_request", "message": "The request is invalid.", "details": nil,
		},
	}
	if !reflect.DeepEqual(result.StructuredContent, want) {
		t.Fatalf("structured error = %#v, want %#v", result.StructuredContent, want)
	}
}

func TestMCPDecodeRejectsCombinedCandidateEvidence(t *testing.T) {
	t.Parallel()
	source := map[string]any{"name": "content", "source_id": "task-1"}
	artifact := map[string]any{"family": "experience", "artifact_id": "exp-1", "revision": 1}
	payload, err := json.Marshal(map[string]any{
		"scope_id": "project", "candidate_id": "candidate-1", "expected_version": 1,
		"proposal": map[string]any{
			"situation": "OpenAPI changed.", "action": "Regenerate the Client.",
			"outcome": "Transport stays aligned.", "lesson": "Keep contract tests green.",
		},
		"source_refs":   repeatMCPValue(source, 20),
		"artifact_refs": repeatMCPValue(artifact, 13),
	})
	if err != nil {
		t.Fatal(err)
	}
	request := new(v1.ReviseArtifactCandidateRequest)
	err = decodeRequest(payload, request)
	var invalid *endpoint.InvalidRequestError
	if !errors.As(err, &invalid) {
		t.Fatalf("error = %#v, want InvalidRequestError", err)
	}
}

func TestMCPRejectsCombinedCandidateEvidenceInEndpointResponse(t *testing.T) {
	t.Parallel()
	response := &v1.ArtifactCandidateHeaders{Response: v1.ArtifactCandidate{
		SourceRefs:   make([]v1.SourceReference, 20),
		ArtifactRefs: make([]v1.ArtifactReference, 13),
	}}
	payload, err := endpointPayload(response, nil)
	if payload != nil {
		t.Fatalf("payload = %#v, want nil", payload)
	}
	var limit *v1.CombinedEvidenceLimitError
	if !errors.As(err, &limit) {
		t.Fatalf("error = %#v, want CombinedEvidenceLimitError", err)
	}
}

func repeatMCPValue[T any](value T, count int) []T {
	result := make([]T, count)
	for index := range result {
		result[index] = value
	}
	return result
}

func connectInMemory(t *testing.T, server *mcp.Server) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "powercontext-test", Version: "1"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		_ = serverSession.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = clientSession.Close()
		_ = serverSession.Wait()
	})
	return clientSession
}

func schemaProperties(t *testing.T, value any) map[string]any {
	t.Helper()
	object := schemaObject(t, value)
	return schemaObject(t, object["properties"])
}

func schemaObject(t *testing.T, value any) map[string]any {
	t.Helper()
	object, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("schema value type = %T, want map[string]any", value)
	}
	return object
}

type memoryOperationsStub struct {
	endpoint.MemoryOperations
	scopeID         string
	includeInactive bool
	requestID       string
}

type mcpScopeOperations struct {
	resolved scope.Descriptor
}

func (m *mcpScopeOperations) Bind(_ context.Context, key scope.BindingKey, id string) (scope.Binding, error) {
	return scope.NewBinding(key, id)
}

func (*mcpScopeOperations) ClearBinding(context.Context, scope.BindingKey) (bool, error) {
	return true, nil
}

func (m *mcpScopeOperations) Resolve(_ context.Context, _ *string, _ []scope.BindingKey) (scope.Descriptor, error) {
	return m.resolved, nil
}

func (m *memoryOperationsStub) List(ctx context.Context, scopeID string, includeInactive bool) (runtime.MemoryEntriesPage, error) {
	m.scopeID = scopeID
	m.includeInactive = includeInactive
	m.requestID, _ = requesttrace.RequestID(ctx)
	return runtime.MemoryEntriesPage{Entries: []runtime.MemoryEntryRecord{}}, nil
}
