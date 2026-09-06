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

package runtime

import (
	"context"
	"testing"

	"github.com/ob-labs/powercontext-go/internal/scope"
)

func TestScopeApplicationResolvesExplicitBindingAndDefaultInOrder(t *testing.T) {
	store := &memoryScopeStore{scopes: map[string]scope.Descriptor{}}
	application, err := NewScopeApplication(New(), store, func() string { return "scope-created" })
	if err != nil {
		t.Fatal(err)
	}
	draft, err := scope.NewDraft("title", "summary", "", nil, nil, "create")
	if err != nil {
		t.Fatal(err)
	}
	created, err := application.Create(t.Context(), draft)
	if err != nil || created.ID() != "scope-created" {
		t.Fatalf("created Scope = %#v, %v", created, err)
	}
	if _, setDefaultErr := application.SetDefault(t.Context(), created.ID()); setDefaultErr != nil {
		t.Fatal(setDefaultErr)
	}
	key, err := scope.NewBindingKey("codex", "project", "repository")
	if err != nil {
		t.Fatal(err)
	}
	if _, bindErr := application.Bind(t.Context(), key, created.ID()); bindErr != nil {
		t.Fatal(bindErr)
	}
	for _, test := range []struct {
		name     string
		explicit string
		keys     []scope.BindingKey
	}{
		{name: "explicit", explicit: created.ID()},
		{name: "binding", keys: []scope.BindingKey{key}},
		{name: "default"},
	} {
		t.Run(test.name, func(t *testing.T) {
			resolved, resolveErr := application.Resolve(t.Context(), test.explicit, test.keys)
			if resolveErr != nil || resolved.ID() != created.ID() {
				t.Fatalf("resolved Scope = %#v, %v", resolved, resolveErr)
			}
		})
	}
}

type memoryScopeStore struct {
	scopes    map[string]scope.Descriptor
	defaultID string
	bindings  map[scope.BindingKey]scope.Binding
}

func (s *memoryScopeStore) Create(_ context.Context, id string, draft scope.Draft) (scope.Descriptor, error) {
	created, err := scope.NewDescriptor(id, draft.Title(), draft.Summary(), draft.ParentScopeID(), draft.ContextReferences(), draft.ExternalReferences(), 1)
	if err == nil {
		s.scopes[id] = created
	}
	return created, err
}

func (s *memoryScopeStore) Get(_ context.Context, id string) (scope.Descriptor, bool, error) {
	value, found := s.scopes[id]
	return value, found, nil
}

func (s *memoryScopeStore) SetDefault(_ context.Context, id string) (scope.Descriptor, error) {
	value := s.scopes[id]
	s.defaultID = id
	return value, nil
}

func (s *memoryScopeStore) Default(_ context.Context) (scope.Descriptor, bool, error) {
	value, found := s.scopes[s.defaultID]
	return value, found, nil
}

func (s *memoryScopeStore) SetBinding(_ context.Context, key scope.BindingKey, id string) (scope.Binding, error) {
	binding, err := scope.NewBinding(key, id)
	if err != nil {
		return scope.Binding{}, err
	}
	if s.bindings == nil {
		s.bindings = make(map[scope.BindingKey]scope.Binding)
	}
	s.bindings[key] = binding
	return binding, nil
}

func (s *memoryScopeStore) Binding(_ context.Context, key scope.BindingKey) (scope.Binding, bool, error) {
	binding, found := s.bindings[key]
	return binding, found, nil
}
