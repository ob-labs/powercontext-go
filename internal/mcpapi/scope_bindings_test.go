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
	"reflect"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ob-labs/powercontext-go/internal/endpoint"
	"github.com/ob-labs/powercontext-go/internal/scope"
)

func TestScopeBindingToolsAreOptionalAndOperateThroughMCP(t *testing.T) {
	t.Parallel()

	without, err := NewServer(endpoint.NewHandler(endpoint.HandlerOptions{}), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if scopeBindingToolsPresent(t, connectInMemory(t, without)) {
		t.Fatal("Scope Binding tools were registered without a Scope binding service")
	}

	operations := &scopeBindingOperationsStub{}
	with, err := NewServer(endpoint.NewHandler(endpoint.HandlerOptions{}), Options{ScopeBindings: operations})
	if err != nil {
		t.Fatal(err)
	}
	client := connectInMemory(t, with)
	if !scopeBindingToolsPresent(t, client) {
		t.Fatal("Scope Binding tools were not registered with a Scope binding service")
	}

	set, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: scopeBindingSetToolName, Arguments: map[string]any{
		"integration": "codex", "kind": "project", "external_id": "repository", "scope_id": "scope-a",
	}})
	if err != nil || set.IsError {
		t.Fatalf("set result = %#v, %v", set, err)
	}
	if got, want := set.StructuredContent, map[string]any{
		"key": map[string]any{"integration": "codex", "kind": "project", "external_id": "repository"}, "scope_id": "scope-a",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("set structured content = %#v, want %#v", got, want)
	}

	resolved, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: scopeBindingResolveToolName, Arguments: map[string]any{
		"binding_keys": []any{map[string]any{"integration": "codex", "kind": "project", "external_id": "repository"}},
	}})
	if err != nil || resolved.IsError {
		t.Fatalf("resolve result = %#v, %v", resolved, err)
	}
	if got := resolved.StructuredContent.(map[string]any)["scope_id"]; got != "scope-a" {
		t.Fatalf("resolved scope_id = %#v, want scope-a", got)
	}
	explicit, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: scopeBindingResolveToolName, Arguments: map[string]any{
		"explicit_scope_id": "scope-explicit",
	}})
	if err != nil || explicit.IsError {
		t.Fatalf("explicit resolve result = %#v, %v", explicit, err)
	}
	if got := explicit.StructuredContent.(map[string]any)["scope_id"]; got != "scope-explicit" {
		t.Fatalf("explicit scope_id = %#v, want scope-explicit", got)
	}

	cleared, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: scopeBindingClearToolName, Arguments: map[string]any{
		"integration": "codex", "kind": "project", "external_id": "repository",
	}})
	if err != nil || cleared.IsError || !reflect.DeepEqual(cleared.StructuredContent, map[string]any{"cleared": true}) {
		t.Fatalf("clear result = %#v, %v", cleared, err)
	}
}

func TestScopeBindingToolsRejectInvalidInputAndExposeRedactedErrors(t *testing.T) {
	t.Parallel()
	operations := &scopeBindingOperationsStub{resolveErr: &scope.NotFoundError{}}
	server, err := NewServer(endpoint.NewHandler(endpoint.HandlerOptions{}), Options{ScopeBindings: operations})
	if err != nil {
		t.Fatal(err)
	}
	client := connectInMemory(t, server)
	invalid, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: scopeBindingSetToolName, Arguments: map[string]any{
		"integration": "codex", "kind": "project", "external_id": "repository", "scope_id": "scope-a", "unexpected": true,
	}})
	if err != nil || !invalid.IsError {
		t.Fatalf("unknown field result = %#v, %v", invalid, err)
	}
	if code := invalid.StructuredContent.(map[string]any)["error"].(map[string]any)["code"]; code != "invalid_request" {
		t.Fatalf("unknown field code = %#v, want invalid_request", code)
	}
	missing, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: scopeBindingResolveToolName, Arguments: map[string]any{}})
	if err != nil || !missing.IsError {
		t.Fatalf("missing resolve = %#v, %v", missing, err)
	}
	if code := missing.StructuredContent.(map[string]any)["error"].(map[string]any)["code"]; code != "scope_not_found" {
		t.Fatalf("scope error code = %#v, want scope_not_found", code)
	}
}

func TestScopeBindingInputValidationAndAnnotations(t *testing.T) {
	t.Parallel()
	for _, raw := range []json.RawMessage{
		[]byte(`{"integration":" ","kind":"project","external_id":"repository","scope_id":"scope-a"}`),
		[]byte(`{"integration":"codex","kind":"project","external_id":"repository","scope_id":" ` + string(make([]byte, scope.MaxIDLength+1)) + `"}`),
		[]byte{'{', '"', 'i', 'n', 't', 'e', 'g', 'r', 'a', 't', 'i', 'o', 'n', '"', ':', '"', 0xff, '"', '}'},
	} {
		if _, err := decodeScopeBindingSetInput(raw); err == nil {
			t.Fatalf("invalid input %q was accepted", raw)
		}
	}
	server, err := NewServer(endpoint.NewHandler(endpoint.HandlerOptions{}), Options{ScopeBindings: &scopeBindingOperationsStub{}})
	if err != nil {
		t.Fatal(err)
	}
	tools, err := connectInMemory(t, server).ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]*mcp.Tool{}
	for _, tool := range tools.Tools {
		byName[tool.Name] = tool
	}
	resolve := byName[scopeBindingResolveToolName].Annotations
	if resolve == nil || !resolve.ReadOnlyHint || !resolve.IdempotentHint || resolve.OpenWorldHint == nil || *resolve.OpenWorldHint {
		t.Fatalf("resolve annotations = %#v", resolve)
	}
	for _, name := range []string{scopeBindingSetToolName, scopeBindingClearToolName} {
		decision := byName[name].Annotations
		if decision == nil || decision.ReadOnlyHint || !decision.IdempotentHint || decision.DestructiveHint == nil || !*decision.DestructiveHint || decision.OpenWorldHint == nil || *decision.OpenWorldHint {
			t.Fatalf("%s annotations = %#v", name, decision)
		}
	}
}

func TestScopeBindingDecoderRejectsNonObjectAndNullBindingKeys(t *testing.T) {
	t.Parallel()
	for _, raw := range []json.RawMessage{
		[]byte(`null`),
		[]byte(`[]`),
		[]byte(`{"binding_keys":null}`),
	} {
		if _, err := decodeScopeBindingResolveInput(raw); err == nil {
			t.Fatalf("resolve input %q was accepted", raw)
		}
	}
}

func TestScopeBindingDecoderRejectsDuplicateMembersAndAcceptsExplicitNull(t *testing.T) {
	t.Parallel()
	if _, err := decodeScopeBindingResolveInput([]byte(`{"binding_keys":[],"binding_keys":[]}`)); err == nil {
		t.Fatal("duplicate binding_keys was accepted")
	}
	decoded, err := decodeScopeBindingResolveInput([]byte(`{"explicit_scope_id":null}`))
	if err != nil || decoded.ExplicitScopeID != nil || len(decoded.BindingKeys) != 0 {
		t.Fatalf("explicit null decode = %#v, %v", decoded, err)
	}
}

func TestScopeBindingToolsRejectNonTargetIntegrationWithoutWriting(t *testing.T) {
	t.Parallel()
	operations := &scopeBindingOperationsStub{}
	server, err := NewServer(endpoint.NewHandler(endpoint.HandlerOptions{}), Options{ScopeBindings: operations})
	if err != nil {
		t.Fatal(err)
	}
	result, err := connectInMemory(t, server).CallTool(t.Context(), &mcp.CallToolParams{Name: scopeBindingSetToolName, Arguments: map[string]any{
		"integration": "claude-code", "kind": "session", "external_id": "session-1", "scope_id": "scope-a",
	}})
	if err != nil || !result.IsError {
		t.Fatalf("non-target integration result = %#v, %v", result, err)
	}
	if code := result.StructuredContent.(map[string]any)["error"].(map[string]any)["code"]; code != "invalid_request" {
		t.Fatalf("non-target integration code = %#v, want invalid_request", code)
	}
	if len(operations.bindings) != 0 {
		t.Fatalf("non-target integration wrote bindings = %#v", operations.bindings)
	}
}

func scopeBindingToolsPresent(t *testing.T, client *mcp.ClientSession) bool {
	t.Helper()
	tools, err := client.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range tools.Tools {
		names[tool.Name] = true
	}
	return names[scopeBindingSetToolName] && names[scopeBindingResolveToolName] && names[scopeBindingClearToolName]
}

type scopeBindingOperationsStub struct {
	bindings   map[scope.BindingKey]string
	resolveErr error
}

func (s *scopeBindingOperationsStub) Bind(_ context.Context, key scope.BindingKey, scopeID string) (scope.Binding, error) {
	if s.bindings == nil {
		s.bindings = make(map[scope.BindingKey]string)
	}
	s.bindings[key] = scopeID
	return scope.NewBinding(key, scopeID)
}

func (s *scopeBindingOperationsStub) Resolve(_ context.Context, explicitID *string, keys []scope.BindingKey) (scope.Descriptor, error) {
	if s.resolveErr != nil {
		return scope.Descriptor{}, s.resolveErr
	}
	if explicitID != nil {
		return scope.NewDescriptor(*explicitID, "Explicit Scope", "Scope summary", "", nil, nil, 1)
	}
	for _, key := range keys {
		if id, ok := s.bindings[key]; ok {
			return scope.NewDescriptor(id, "Scope A", "Scope summary", "", nil, nil, 1)
		}
	}
	return scope.Descriptor{}, &scope.BindingNotFoundError{}
}

func (s *scopeBindingOperationsStub) ClearBinding(_ context.Context, key scope.BindingKey) (bool, error) {
	if s.bindings == nil {
		return false, nil
	}
	if _, ok := s.bindings[key]; !ok {
		return false, nil
	}
	delete(s.bindings, key)
	return true, nil
}

var _ ScopeBindingOperations = (*scopeBindingOperationsStub)(nil)
