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
		explicit *string
		keys     []scope.BindingKey
	}{
		{name: "explicit", explicit: new(created.ID())},
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

func TestScopeApplicationRejectsInvalidRelationships(t *testing.T) {
	for _, test := range []struct {
		name       string
		parent     string
		references []string
	}{
		{name: "missing parent", parent: "missing-secret"},
		{name: "self parent", parent: "scope-created"},
		{name: "missing reference", references: []string{"missing-secret"}},
		{name: "self reference", references: []string{"scope-created"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &memoryScopeStore{scopes: map[string]scope.Descriptor{}}
			application, err := NewScopeApplication(New(), store, func() string { return "scope-created" })
			if err != nil {
				t.Fatal(err)
			}
			draft, err := scope.NewDraft("title", "summary", test.parent, test.references, nil, "create")
			if err != nil {
				t.Fatal(err)
			}
			if _, createErr := application.Create(t.Context(), draft); createErr == nil {
				t.Fatal("invalid relationship was accepted")
			}
			if len(store.scopes) != 0 {
				t.Fatal("rejected creation persisted a Scope")
			}
		})
	}
}

type memoryScopeStore struct {
	scopes    map[string]scope.Descriptor
	defaultID string
	bindings  map[scope.BindingKey]scope.Binding
}

func (s *memoryScopeStore) Create(ctx context.Context, id string, draft scope.Draft, validate func([]scope.Descriptor) error) (scope.Descriptor, error) {
	scopes, _ := s.List(ctx)
	if err := validate(scopes); err != nil {
		return scope.Descriptor{}, err
	}
	created, err := scope.NewDescriptor(id, draft.Title(), draft.Summary(), draft.ParentScopeID(), draft.ContextReferences(), draft.ExternalReferences(), 1)
	if err == nil {
		s.scopes[id] = created
	}
	return created, err
}

func (s *memoryScopeStore) Update(ctx context.Context, id string, mutation scope.Mutation, validate func([]scope.Descriptor) error) (scope.Descriptor, error) {
	current, found := s.scopes[id]
	if !found {
		return scope.Descriptor{}, &scope.NotFoundError{}
	}
	if current.Version() != mutation.ExpectedVersion() {
		return scope.Descriptor{}, &scope.VersionConflictError{Expected: mutation.ExpectedVersion(), Actual: current.Version()}
	}
	scopes, _ := s.List(ctx)
	if err := validate(scopes); err != nil {
		return scope.Descriptor{}, err
	}
	updated, err := scope.NewDescriptor(id, mutation.Title(), mutation.Summary(), mutation.ParentScopeID(), mutation.ContextReferences(), mutation.ExternalReferences(), current.Version()+1)
	if err == nil {
		s.scopes[id] = updated
	}
	return updated, err
}

func (s *memoryScopeStore) BootstrapDefault(ctx context.Context, id string, draft scope.Draft) (scope.Descriptor, error) {
	if existing, found := s.scopes[s.defaultID]; found {
		return existing, nil
	}
	created, err := s.Create(ctx, id, draft, func([]scope.Descriptor) error { return nil })
	if err == nil {
		s.defaultID = created.ID()
	}
	return created, err
}

func (s *memoryScopeStore) List(_ context.Context) ([]scope.Descriptor, error) {
	result := make([]scope.Descriptor, 0, len(s.scopes))
	for _, value := range s.scopes {
		result = append(result, value)
	}
	return result, nil
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
