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
