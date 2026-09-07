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
	"errors"
	"testing"

	canonicalscopec "github.com/ob-labs/powercontext-go/api/canonical/scopes"
	"github.com/ob-labs/powercontext-go/internal/scope"
)

func TestCanonicalScopeHandlerRejectsUnsupportedBindingBeforeResolution(t *testing.T) {
	operations := &canonicalScopeOperationsStub{}
	handler := NewCanonicalScopeHandler(operations)

	response, err := handler.ResolveScopeBinding(t.Context(), &canonicalscopec.ResolveScopeBindingRequest{
		BindingKeys: []canonicalscopec.ScopeBindingKey{{
			Integration: canonicalscopec.ScopeBindingKeyIntegration("third-agent"),
			Kind:        "project",
			ExternalID:  "repository",
		}},
	})
	if response != nil {
		t.Fatalf("ResolveScopeBinding() response = %T, want nil", response)
	}
	if _, ok := errors.AsType[*scope.ValidationError](err); !ok {
		t.Fatalf("ResolveScopeBinding() error = %T %v, want ValidationError", err, err)
	}
	if operations.resolveCalls != 0 {
		t.Fatalf("Resolve() calls = %d, want no Scope operation", operations.resolveCalls)
	}
}

func TestCanonicalScopeHandlerProjectsSetBinding(t *testing.T) {
	key, err := scope.NewBindingKey("codex", "project", "repository")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := scope.NewBinding(key, "scope-a")
	if err != nil {
		t.Fatal(err)
	}
	operations := &canonicalScopeOperationsStub{binding: binding}
	handler := NewCanonicalScopeHandler(operations)
	wireKey := canonicalscopec.ScopeBindingKey{
		Integration: canonicalscopec.ScopeBindingKeyIntegrationCodex,
		Kind:        "project",
		ExternalID:  "repository",
	}

	set, err := handler.SetScopeBinding(t.Context(), &canonicalscopec.ScopeBinding{
		Key: wireKey, ScopeID: "scope-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	setBinding, ok := set.(*canonicalscopec.ScopeBinding)
	if !ok || setBinding.Key != wireKey || setBinding.ScopeID != "scope-a" {
		t.Fatalf("SetScopeBinding() = %#v, want projected binding", set)
	}
	if operations.bindCalls != 1 || operations.boundKey != key || operations.boundScopeID != "scope-a" {
		t.Fatalf("Bind() = (%d, %#v, %q), want one call with supported key and scope-a", operations.bindCalls, operations.boundKey, operations.boundScopeID)
	}
}

func TestCanonicalScopeHandlerProjectsClearBindingResult(t *testing.T) {
	key, err := scope.NewBindingKey("codex", "project", "repository")
	if err != nil {
		t.Fatal(err)
	}
	operations := &canonicalScopeOperationsStub{clearResults: []bool{true, false}}
	handler := NewCanonicalScopeHandler(operations)
	wireKey := canonicalscopec.ScopeBindingKey{
		Integration: canonicalscopec.ScopeBindingKeyIntegrationCodex,
		Kind:        "project",
		ExternalID:  "repository",
	}

	for index, want := range []bool{true, false} {
		cleared, clearErr := handler.ClearScopeBinding(t.Context(), &canonicalscopec.ClearScopeBindingRequest{Key: wireKey})
		if clearErr != nil {
			t.Fatal(clearErr)
		}
		response, ok := cleared.(*canonicalscopec.ClearScopeBindingResponse)
		if !ok || response.Cleared != want {
			t.Fatalf("ClearScopeBinding() call %d = %#v, want cleared=%t", index+1, cleared, want)
		}
	}
	if operations.clearCalls != 2 || operations.clearedKey != key {
		t.Fatalf("ClearBinding() = (%d, %#v), want two calls with supported key", operations.clearCalls, operations.clearedKey)
	}
}

func TestCanonicalScopeHandlerRejectsUnsupportedBindingWritesBeforeRuntime(t *testing.T) {
	operations := &canonicalScopeOperationsStub{}
	handler := NewCanonicalScopeHandler(operations)
	key := canonicalscopec.ScopeBindingKey{
		Integration: canonicalscopec.ScopeBindingKeyIntegration("third-agent"),
		Kind:        "project",
		ExternalID:  "repository",
	}

	for _, test := range []struct {
		name string
		call func() (any, error)
	}{
		{
			name: "set",
			call: func() (any, error) {
				return handler.SetScopeBinding(t.Context(), &canonicalscopec.ScopeBinding{Key: key, ScopeID: "scope-a"})
			},
		},
		{
			name: "clear",
			call: func() (any, error) {
				return handler.ClearScopeBinding(t.Context(), &canonicalscopec.ClearScopeBindingRequest{Key: key})
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			response, err := test.call()
			if response != nil {
				t.Fatalf("response = %T, want nil", response)
			}
			if _, ok := errors.AsType[*scope.ValidationError](err); !ok {
				t.Fatalf("error = %T %v, want ValidationError", err, err)
			}
			if operations.bindCalls != 0 || operations.clearCalls != 0 {
				t.Fatalf("binding write calls = (%d, %d), want no Scope operation", operations.bindCalls, operations.clearCalls)
			}
		})
	}
}

func TestCanonicalScopeHandlerRejectsInvalidMetadataWritesBeforeRuntime(t *testing.T) {
	operations := &canonicalScopeOperationsStub{}
	handler := NewCanonicalScopeHandler(operations)

	for _, test := range []struct {
		name  string
		call  func() (any, error)
		calls func() int
	}{
		{
			name: "create external reference",
			call: func() (any, error) {
				return handler.CreateScope(t.Context(), &canonicalscopec.CreateScopeRequest{
					Title: "title", Summary: "summary", IdempotencyKey: "request",
					ExternalReferences: []canonicalscopec.ScopeExternalReference{{Kind: " ", Value: "private-reference"}},
				})
			},
			calls: func() int { return operations.createCalls },
		},
		{
			name: "update Scope ID",
			call: func() (any, error) {
				return handler.UpdateScope(t.Context(), &canonicalscopec.UpdateScopeRequest{
					ExpectedVersion: 1, Title: "title", Summary: "summary",
				}, canonicalscopec.UpdateScopeParams{ScopeID: " "})
			},
			calls: func() int { return operations.updateCalls },
		},
		{
			name: "default Scope ID",
			call: func() (any, error) {
				return handler.SetDefaultScope(t.Context(), &canonicalscopec.SetDefaultScopeRequest{ScopeID: " "})
			},
			calls: func() int { return operations.setDefaultCalls },
		},
		{
			name: "binding Scope ID",
			call: func() (any, error) {
				return handler.SetScopeBinding(t.Context(), &canonicalscopec.ScopeBinding{
					Key: canonicalscopec.ScopeBindingKey{
						Integration: canonicalscopec.ScopeBindingKeyIntegrationCodex,
						Kind:        "project",
						ExternalID:  "repository",
					},
					ScopeID: " ",
				})
			},
			calls: func() int { return operations.bindCalls },
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			response, err := test.call()
			if response != nil {
				t.Fatalf("response = %T, want nil", response)
			}
			if _, ok := errors.AsType[*scope.ValidationError](err); !ok {
				t.Fatalf("error = %T %v, want ValidationError", err, err)
			}
			if calls := test.calls(); calls != 0 {
				t.Fatalf("runtime calls = %d, want 0", calls)
			}
		})
	}
}

type canonicalScopeOperationsStub struct {
	createCalls, updateCalls, setDefaultCalls, resolveCalls int
	bindCalls, clearCalls                                   int
	binding                                                 scope.Binding
	boundKey, clearedKey                                    scope.BindingKey
	boundScopeID                                            string
	clearResults                                            []bool
}

func (s *canonicalScopeOperationsStub) Create(context.Context, scope.Draft) (scope.Descriptor, error) {
	s.createCalls++
	return scope.Descriptor{}, nil
}

func (s *canonicalScopeOperationsStub) Update(context.Context, string, scope.Mutation) (scope.Descriptor, error) {
	s.updateCalls++
	return scope.Descriptor{}, nil
}

func (s *canonicalScopeOperationsStub) SetDefault(context.Context, string) (scope.Descriptor, error) {
	s.setDefaultCalls++
	return scope.Descriptor{}, nil
}

func (s *canonicalScopeOperationsStub) Bind(_ context.Context, key scope.BindingKey, scopeID string) (scope.Binding, error) {
	s.bindCalls++
	s.boundKey = key
	s.boundScopeID = scopeID
	return s.binding, nil
}

func (s *canonicalScopeOperationsStub) ClearBinding(_ context.Context, key scope.BindingKey) (bool, error) {
	s.clearCalls++
	s.clearedKey = key
	if len(s.clearResults) == 0 {
		return false, nil
	}
	result := s.clearResults[0]
	s.clearResults = s.clearResults[1:]
	return result, nil
}

func (s *canonicalScopeOperationsStub) List(context.Context) ([]scope.Descriptor, error) {
	return nil, nil
}

func (s *canonicalScopeOperationsStub) Get(context.Context, string) (scope.Descriptor, error) {
	return scope.Descriptor{}, nil
}

func (s *canonicalScopeOperationsStub) Default(context.Context) (scope.Descriptor, error) {
	return scope.Descriptor{}, nil
}

func (s *canonicalScopeOperationsStub) Resolve(
	context.Context,
	*string,
	[]scope.BindingKey,
) (scope.Descriptor, error) {
	s.resolveCalls++
	return scope.Descriptor{}, nil
}

func (s *canonicalScopeOperationsStub) ResolveSelection(
	context.Context,
	scope.Selection,
) ([]scope.Descriptor, error) {
	return nil, nil
}
