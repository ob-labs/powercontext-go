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
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ob-labs/powercontext-go/internal/endpoint"
	"github.com/ob-labs/powercontext-go/internal/scope"
)

const (
	scopeBindingSetToolName     = "scope_binding_set"
	scopeBindingResolveToolName = "scope_binding_resolve"
	scopeBindingClearToolName   = "scope_binding_clear"
)

// ScopeBindingOperations is the MCP consumer contract for durable Scope
// bindings. It intentionally does not depend on generated OpenAPI types.
type ScopeBindingOperations interface {
	Bind(context.Context, scope.BindingKey, string) (scope.Binding, error)
	Resolve(context.Context, *string, []scope.BindingKey) (scope.Descriptor, error)
	ClearBinding(context.Context, scope.BindingKey) (bool, error)
}

type scopeBindingKeyInput struct {
	Integration string `json:"integration"`
	Kind        string `json:"kind"`
	ExternalID  string `json:"external_id"`
}

type scopeBindingSetInput struct {
	scopeBindingKeyInput
	ScopeID string           `json:"scope_id"`
	Key     scope.BindingKey `json:"-"`
}

type scopeBindingResolveInput struct {
	ExplicitScopeID *string                `json:"explicit_scope_id"`
	BindingKeys     []scopeBindingKeyInput `json:"binding_keys"`
}

type decodedScopeBindingResolveInput struct {
	ExplicitScopeID *string
	BindingKeys     []scope.BindingKey
}

type scopeBindingResult struct {
	Key     scopeBindingKeyResult `json:"key"`
	ScopeID string                `json:"scope_id"`
}

type scopeBindingKeyResult struct {
	Integration string `json:"integration"`
	Kind        string `json:"kind"`
	ExternalID  string `json:"external_id"`
}

type scopeBindingResolveResult struct {
	ScopeID string `json:"scope_id"`
}

type scopeBindingClearResult struct {
	Cleared bool `json:"cleared"`
}

var scopeBindingKeyInputSchema = map[string]any{
	"type": "object", "additionalProperties": false,
	"required": []string{"integration", "kind", "external_id"},
	"properties": map[string]any{
		"integration": map[string]any{"type": "string", "enum": []string{"codex", "workbuddy"}, "minLength": 1, "maxLength": scope.MaxBindingIntegrationLength, "pattern": ".*\\S.*"},
		"kind":        map[string]any{"type": "string", "minLength": 1, "maxLength": scope.MaxBindingKindLength, "pattern": ".*\\S.*"},
		"external_id": map[string]any{"type": "string", "minLength": 1, "maxLength": scope.MaxBindingExternalIDLength, "pattern": ".*\\S.*"},
	},
}

var scopeBindingSetInputSchema = map[string]any{
	"type": "object", "additionalProperties": false,
	"required": []string{"integration", "kind", "external_id", "scope_id"},
	"properties": map[string]any{
		"integration": scopeBindingKeyInputSchema["properties"].(map[string]any)["integration"],
		"kind":        scopeBindingKeyInputSchema["properties"].(map[string]any)["kind"],
		"external_id": scopeBindingKeyInputSchema["properties"].(map[string]any)["external_id"],
		"scope_id":    map[string]any{"type": "string", "minLength": 1, "maxLength": scope.MaxIDLength, "pattern": ".*\\S.*"},
	},
}

var scopeBindingResolveInputSchema = map[string]any{
	"type": "object", "additionalProperties": false,
	"properties": map[string]any{
		"explicit_scope_id": map[string]any{"type": []string{"string", "null"}, "minLength": 1, "maxLength": scope.MaxIDLength, "pattern": ".*\\S.*"},
		"binding_keys":      map[string]any{"type": "array", "items": scopeBindingKeyInputSchema},
	},
}

var scopeBindingClearInputSchema = map[string]any{
	"type": "object", "additionalProperties": false,
	"required":   []string{"integration", "kind", "external_id"},
	"properties": scopeBindingKeyInputSchema["properties"],
}

var scopeBindingSetOutputSchema = map[string]any{
	"type": "object", "additionalProperties": false, "required": []string{"key", "scope_id"},
	"properties": map[string]any{
		"key": scopeBindingKeyInputSchema, "scope_id": map[string]any{"type": "string"},
	},
}

var scopeBindingResolveOutputSchema = map[string]any{
	"type": "object", "additionalProperties": false, "required": []string{"scope_id"},
	"properties": map[string]any{"scope_id": map[string]any{"type": "string"}},
}

var scopeBindingClearOutputSchema = map[string]any{
	"type": "object", "additionalProperties": false, "required": []string{"cleared"},
	"properties": map[string]any{"cleared": map[string]any{"type": "boolean"}},
}

func registerScopeBindingTools(server *mcp.Server, operations ScopeBindingOperations, options Options) {
	server.AddTool(&mcp.Tool{
		Name: scopeBindingSetToolName, Description: "Create or replace a durable external Scope binding.",
		InputSchema: scopeBindingSetInputSchema, OutputSchema: scopeBindingSetOutputSchema,
		Annotations: annotations(scopeBindingSetToolName),
	}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return runTool(ctx, options, scopeBindingSetToolName, func(ctx context.Context) (any, error) {
			input, err := decodeScopeBindingSetInput(request.Params.Arguments)
			if err != nil {
				return nil, err
			}
			binding, err := operations.Bind(ctx, input.Key, input.ScopeID)
			if err != nil {
				return nil, err
			}
			return scopeBindingResult{Key: scopeBindingKeyValue(binding.Key()), ScopeID: binding.ScopeID()}, nil
		})
	})
	server.AddTool(&mcp.Tool{
		Name: scopeBindingResolveToolName, Description: "Resolve an explicit Scope, ordered durable binding keys, or the durable default Scope.",
		InputSchema: scopeBindingResolveInputSchema, OutputSchema: scopeBindingResolveOutputSchema,
		Annotations: annotations(scopeBindingResolveToolName),
	}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return runTool(ctx, options, scopeBindingResolveToolName, func(ctx context.Context) (any, error) {
			input, err := decodeScopeBindingResolveInput(request.Params.Arguments)
			if err != nil {
				return nil, err
			}
			descriptor, err := operations.Resolve(ctx, input.ExplicitScopeID, input.BindingKeys)
			if err != nil {
				return nil, err
			}
			return scopeBindingResolveResult{ScopeID: descriptor.ID()}, nil
		})
	})
	server.AddTool(&mcp.Tool{
		Name: scopeBindingClearToolName, Description: "Remove a durable external Scope binding. Repeating the operation is a no-op.",
		InputSchema: scopeBindingClearInputSchema, OutputSchema: scopeBindingClearOutputSchema,
		Annotations: annotations(scopeBindingClearToolName),
	}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return runTool(ctx, options, scopeBindingClearToolName, func(ctx context.Context) (any, error) {
			key, err := decodeScopeBindingKey(request.Params.Arguments)
			if err != nil {
				return nil, err
			}
			cleared, err := operations.ClearBinding(ctx, key)
			if err != nil {
				return nil, err
			}
			return scopeBindingClearResult{Cleared: cleared}, nil
		})
	})
}

func decodeScopeBindingSetInput(raw json.RawMessage) (scopeBindingSetInput, error) {
	var input scopeBindingSetInput
	if err := decodeScopeBindingJSON(raw, &input); err != nil {
		return scopeBindingSetInput{}, err
	}
	key, err := scopeBindingKey(input.scopeBindingKeyInput)
	if err != nil {
		return scopeBindingSetInput{}, err
	}
	if _, err := scope.NewBinding(key, input.ScopeID); err != nil {
		return scopeBindingSetInput{}, &endpoint.InvalidRequestError{Field: "arguments.scope_id"}
	}
	input.scopeBindingKeyInput = scopeBindingKeyInput{Integration: key.Integration(), Kind: key.Kind(), ExternalID: key.ExternalID()}
	input.Key = key
	return input, nil
}

func decodeScopeBindingResolveInput(raw json.RawMessage) (decodedScopeBindingResolveInput, error) {
	object, err := scopeBindingJSONObject(raw)
	if err != nil {
		return decodedScopeBindingResolveInput{}, err
	}
	if value, present := object["binding_keys"]; present && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		return decodedScopeBindingResolveInput{}, &endpoint.InvalidRequestError{Field: "arguments.binding_keys"}
	}
	var input scopeBindingResolveInput
	if err := decodeScopeBindingJSON(raw, &input); err != nil {
		return decodedScopeBindingResolveInput{}, err
	}
	if input.ExplicitScopeID != nil && !validScopeBindingText(*input.ExplicitScopeID, scope.MaxIDLength) {
		return decodedScopeBindingResolveInput{}, &endpoint.InvalidRequestError{Field: "arguments.explicit_scope_id"}
	}
	keys := make([]scope.BindingKey, len(input.BindingKeys))
	for index, value := range input.BindingKeys {
		key, err := scopeBindingKey(value)
		if err != nil {
			return decodedScopeBindingResolveInput{}, err
		}
		keys[index] = key
	}
	return decodedScopeBindingResolveInput{ExplicitScopeID: input.ExplicitScopeID, BindingKeys: keys}, nil
}

func decodeScopeBindingKey(raw json.RawMessage) (scope.BindingKey, error) {
	var input scopeBindingKeyInput
	if err := decodeScopeBindingJSON(raw, &input); err != nil {
		return scope.BindingKey{}, err
	}
	return scopeBindingKey(input)
}

func scopeBindingKey(input scopeBindingKeyInput) (scope.BindingKey, error) {
	key, err := scope.NewBindingKey(input.Integration, input.Kind, input.ExternalID)
	if err != nil {
		return scope.BindingKey{}, &endpoint.InvalidRequestError{Field: "arguments"}
	}
	if key.Integration() != "codex" && key.Integration() != "workbuddy" {
		return scope.BindingKey{}, &endpoint.InvalidRequestError{Field: "arguments.integration"}
	}
	return key, nil
}

func scopeBindingJSONObject(raw json.RawMessage) (map[string]json.RawMessage, error) {
	if len(raw) == 0 || !utf8.Valid(raw) {
		return nil, &endpoint.InvalidRequestError{Field: "arguments"}
	}
	var object map[string]json.RawMessage
	if err := jsonv2.Unmarshal(raw, &object); err != nil || object == nil {
		return nil, &endpoint.InvalidRequestError{Field: "arguments"}
	}
	return object, nil
}

func decodeScopeBindingJSON(raw json.RawMessage, target any) error {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	if !utf8.Valid(raw) {
		return &endpoint.InvalidRequestError{Field: "arguments"}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return &endpoint.InvalidRequestError{Field: "arguments"}
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return &endpoint.InvalidRequestError{Field: "arguments"}
	}
	return nil
}

func validScopeBindingText(value string, maximum int) bool {
	return utf8.ValidString(value) && value == strings.TrimSpace(value) && value != "" && utf8.RuneCountInString(value) <= maximum
}

func scopeBindingKeyValue(key scope.BindingKey) scopeBindingKeyResult {
	return scopeBindingKeyResult{Integration: key.Integration(), Kind: key.Kind(), ExternalID: key.ExternalID()}
}
