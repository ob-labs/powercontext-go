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

package endpoint

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	v1 "github.com/ob-labs/powercontext-go/api/v1"
	"github.com/ob-labs/powercontext-go/internal/scope"
)

func TestScopeBindingOperationsTranslateDurableScopeValues(t *testing.T) {
	parentReference, err := scope.NewExternalReference("repository", "ob-labs/powercontext-go")
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := scope.NewDescriptor(
		"scope-1", "Repository", "Repository context", "", []string{"scope-base"}, []scope.ExternalReference{parentReference}, 3,
	)
	if err != nil {
		t.Fatal(err)
	}
	stub := &scopeOperationsStub{resolved: descriptor}
	handler := NewHandler(HandlerOptions{Scopes: stub})
	key := v1.ScopeBindingKey{Integration: "codex", Kind: "project", ExternalID: "ob-labs/powercontext-go"}

	set, err := handler.SetScopeBinding(t.Context(), &v1.ScopeBinding{Key: key, ScopeID: descriptor.ID()})
	if err != nil {
		t.Fatal(err)
	}
	setHeaders, ok := set.(*v1.ScopeBindingHeaders)
	if !ok || setHeaders.Response.ScopeID != descriptor.ID() || setHeaders.Response.Key != key {
		t.Fatalf("set response = %#v", set)
	}

	resolve, err := handler.ResolveScopeBinding(t.Context(), &v1.ResolveScopeBindingRequest{BindingKeys: []v1.ScopeBindingKey{key}})
	if err != nil {
		t.Fatal(err)
	}
	resolvedHeaders, ok := resolve.(*v1.ScopeDescriptorHeaders)
	if !ok {
		t.Fatalf("resolve response = %#v", resolve)
	}
	resolved := resolvedHeaders.Response
	if resolved.ScopeID != descriptor.ID() || resolved.Title != descriptor.Title() || resolved.Summary != descriptor.Summary() ||
		resolved.Version != int(descriptor.Version()) || !resolved.ParentScopeID.IsNull() ||
		len(resolved.ContextReferences) != 1 || resolved.ContextReferences[0] != "scope-base" ||
		len(resolved.ExternalReferences) != 1 || resolved.ExternalReferences[0].Kind != parentReference.Kind() || resolved.ExternalReferences[0].Value != parentReference.Value() {
		t.Fatalf("resolved descriptor = %#v", resolved)
	}
	payload, err := json.Marshal(resolved)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]json.RawMessage
	if unmarshalErr := json.Unmarshal(payload, &wire); unmarshalErr != nil {
		t.Fatal(unmarshalErr)
	}
	if parent, present := wire["parent_scope_id"]; !present || string(parent) != "null" {
		t.Fatalf("resolved JSON parent_scope_id = %q, want null", parent)
	}
	if len(stub.keys) != 2 || stub.keys[1].Integration() != key.Integration || stub.keys[1].Kind() != key.Kind || stub.keys[1].ExternalID() != key.ExternalID {
		t.Fatalf("runtime binding key = %#v", stub.keys)
	}
	explicitID := v1.NewOptNilString(descriptor.ID())
	if _, explicitResolveErr := handler.ResolveScopeBinding(t.Context(), &v1.ResolveScopeBindingRequest{
		ExplicitScopeID: explicitID, BindingKeys: []v1.ScopeBindingKey{key},
	}); explicitResolveErr != nil {
		t.Fatal(explicitResolveErr)
	}
	if stub.explicitID == nil || *stub.explicitID != descriptor.ID() {
		t.Fatalf("runtime explicit Scope ID = %#v", stub.explicitID)
	}

	clear, err := handler.ClearScopeBinding(t.Context(), &v1.ClearScopeBindingRequest{Key: key})
	if err != nil {
		t.Fatal(err)
	}
	clearHeaders, ok := clear.(*v1.ClearScopeBindingResponseHeaders)
	if !ok || !clearHeaders.Response.Cleared {
		t.Fatalf("clear response = %#v", clear)
	}
}

func TestScopeBindingOperationsRejectUnavailableOrInvalidDomainInput(t *testing.T) {
	key := v1.ScopeBindingKey{Integration: "codex", Kind: "project", ExternalID: "repository"}
	if _, err := NewHandler(HandlerOptions{}).ClearScopeBinding(t.Context(), &v1.ClearScopeBindingRequest{Key: key}); err == nil {
		t.Fatal("missing Scope application succeeded")
	} else if _, unavailable := errors.AsType[*RuntimeNotReadyError](err); !unavailable {
		t.Fatalf("missing Scope application error = %T %v", err, err)
	}
	stub := &scopeOperationsStub{}
	if _, err := NewHandler(HandlerOptions{Scopes: stub}).SetScopeBinding(t.Context(), &v1.ScopeBinding{
		Key: v1.ScopeBindingKey{}, ScopeID: "scope-1",
	}); err == nil {
		t.Fatal("invalid binding key reached Scope application")
	} else if _, invalid := errors.AsType[*scope.ValidationError](err); !invalid {
		t.Fatalf("invalid binding key error = %T %v", err, err)
	}
	if stub.binds != 0 {
		t.Fatal("invalid binding key invoked Scope application")
	}
}

func TestScopeBindingErrorsRemainRedacted(t *testing.T) {
	for _, err := range []error{&scope.NotFoundError{}, &scope.BindingNotFoundError{}, &scope.ValidationError{}} {
		mapped := MapError(err)
		if mapped.StatusCode == 500 || mapped.Code == "internal_error" || mapped.Details != nil {
			t.Fatalf("scope error mapping = %#v", mapped)
		}
	}
}

type scopeOperationsStub struct {
	resolved   scope.Descriptor
	keys       []scope.BindingKey
	binds      int
	explicitID *string
}

func (s *scopeOperationsStub) Bind(_ context.Context, key scope.BindingKey, id string) (scope.Binding, error) {
	s.keys = append(s.keys, key)
	s.binds++
	return scope.NewBinding(key, id)
}

func (s *scopeOperationsStub) ClearBinding(_ context.Context, key scope.BindingKey) (bool, error) {
	s.keys = append(s.keys, key)
	return true, nil
}

func (s *scopeOperationsStub) Resolve(_ context.Context, explicitID *string, keys []scope.BindingKey) (scope.Descriptor, error) {
	s.explicitID = explicitID
	s.keys = append(s.keys, keys...)
	return s.resolved, nil
}
