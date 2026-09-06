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
	"cmp"
	"context"
	"errors"
	"slices"

	"github.com/ob-labs/powercontext-go/internal/scope"
)

// ScopeStore owns transactions. Create and Update run validation against the
// hierarchy after acquiring its database write lock and before committing.
type ScopeStore interface {
	Create(context.Context, string, scope.Draft, func([]scope.Descriptor) error) (scope.Descriptor, error)
	Update(context.Context, string, scope.Mutation, func([]scope.Descriptor) error) (scope.Descriptor, error)
	BootstrapDefault(context.Context, string, scope.Draft) (scope.Descriptor, error)
	Get(context.Context, string) (scope.Descriptor, bool, error)
	List(context.Context) ([]scope.Descriptor, error)
	SetDefault(context.Context, string) (scope.Descriptor, error)
	Default(context.Context) (scope.Descriptor, bool, error)
	SetBinding(context.Context, scope.BindingKey, string) (scope.Binding, error)
	Binding(context.Context, scope.BindingKey) (scope.Binding, bool, error)
}

// ScopeApplication owns lifecycle admission and resolution order for durable Scopes.
type ScopeApplication struct {
	runtime *Runtime
	store   ScopeStore
	newID   func() string
}

func NewScopeApplication(runtime *Runtime, store ScopeStore, newID func() string) (*ScopeApplication, error) {
	if runtime == nil || store == nil || newID == nil {
		return nil, errors.New("runtime: Scope application dependencies must not be nil")
	}
	return &ScopeApplication{runtime: runtime, store: store, newID: newID}, nil
}

func (a *ScopeApplication) Create(ctx context.Context, draft scope.Draft) (result scope.Descriptor, err error) {
	err = a.runtime.Operation(ctx, func(ctx context.Context) error {
		if _, validationErr := scope.NewDraft(draft.Title(), draft.Summary(), draft.ParentScopeID(), draft.ContextReferences(), draft.ExternalReferences(), draft.IdempotencyKey()); validationErr != nil {
			return &scope.ValidationError{}
		}
		id := a.newID()
		created, createErr := a.store.Create(ctx, id, draft, func(scopes []scope.Descriptor) error {
			return validateScopeRelationships(scopes, id, draft.ParentScopeID(), draft.ContextReferences())
		})
		result = created
		return createErr
	})
	return result, err
}

func (a *ScopeApplication) Update(ctx context.Context, id string, mutation scope.Mutation) (result scope.Descriptor, err error) {
	err = a.runtime.Operation(ctx, func(ctx context.Context) error {
		if _, validationErr := scope.NewMutation(mutation.ExpectedVersion(), mutation.Title(), mutation.Summary(), mutation.ParentScopeID(), mutation.ContextReferences(), mutation.ExternalReferences()); validationErr != nil {
			return &scope.ValidationError{}
		}
		updated, updateErr := a.store.Update(ctx, id, mutation, func(scopes []scope.Descriptor) error {
			return validateScopeRelationships(scopes, id, mutation.ParentScopeID(), mutation.ContextReferences())
		})
		result = updated
		return updateErr
	})
	return result, err
}

// BootstrapDefault preserves an existing default and otherwise creates an ordinary
// Scope with a stable creation key in the same transaction as the default setting.
func (a *ScopeApplication) BootstrapDefault(ctx context.Context) (result scope.Descriptor, err error) {
	err = a.runtime.Operation(ctx, func(ctx context.Context) error {
		draft, draftErr := scope.NewDraft("Default", "Default context", "", nil, nil, "powercontext.default-scope.v1")
		if draftErr != nil {
			return draftErr
		}
		created, bootstrapErr := a.store.BootstrapDefault(ctx, a.newID(), draft)
		result = created
		return bootstrapErr
	})
	return result, err
}

func (a *ScopeApplication) Get(ctx context.Context, id string) (result scope.Descriptor, err error) {
	err = a.runtime.Operation(ctx, func(ctx context.Context) error {
		value, found, getErr := a.store.Get(ctx, id)
		if getErr != nil {
			return getErr
		}
		if !found {
			return &scope.NotFoundError{}
		}
		result = value
		return nil
	})
	return result, err
}

func (a *ScopeApplication) SetDefault(ctx context.Context, id string) (result scope.Descriptor, err error) {
	err = a.runtime.Operation(ctx, func(ctx context.Context) error {
		value, setErr := a.store.SetDefault(ctx, id)
		result = value
		return setErr
	})
	return result, err
}

func (a *ScopeApplication) Bind(ctx context.Context, key scope.BindingKey, id string) (result scope.Binding, err error) {
	err = a.runtime.Operation(ctx, func(ctx context.Context) error {
		value, bindErr := a.store.SetBinding(ctx, key, id)
		result = value
		return bindErr
	})
	return result, err
}

// Resolve treats nil as omitted and every explicit value, including an empty
// string, as an exact Scope request that must not fall back to a binding.
func (a *ScopeApplication) Resolve(ctx context.Context, explicitID *string, keys []scope.BindingKey) (result scope.Descriptor, err error) {
	err = a.runtime.Operation(ctx, func(ctx context.Context) error {
		if explicitID != nil {
			value, found, getErr := a.store.Get(ctx, *explicitID)
			if getErr != nil {
				return getErr
			}
			if !found {
				return &scope.NotFoundError{}
			}
			result = value
			return nil
		}
		for _, key := range keys {
			binding, found, bindingErr := a.store.Binding(ctx, key)
			if bindingErr != nil {
				return bindingErr
			}
			if !found {
				continue
			}
			value, scopeFound, getErr := a.store.Get(ctx, binding.ScopeID())
			if getErr != nil {
				return getErr
			}
			if !scopeFound {
				return &scope.NotFoundError{}
			}
			result = value
			return nil
		}
		value, found, defaultErr := a.store.Default(ctx)
		if defaultErr != nil {
			return defaultErr
		}
		if !found {
			return &scope.BindingNotFoundError{}
		}
		result = value
		return nil
	})
	return result, err
}

// ResolveSelection uses parent edges only for subtree organization. A parent
// never becomes a Context Reference or an implicit member of an exact selection.
func (a *ScopeApplication) ResolveSelection(ctx context.Context, selection scope.Selection) (result []scope.Descriptor, err error) {
	err = a.runtime.Operation(ctx, func(ctx context.Context) error {
		if selection.Mode() != scope.SelectionAll && selection.Mode() != scope.SelectionExact && selection.Mode() != scope.SelectionSubtree {
			return &scope.ValidationError{}
		}
		scopes, listErr := a.store.List(ctx)
		if listErr != nil {
			return listErr
		}
		slices.SortFunc(scopes, func(a, b scope.Descriptor) int { return cmp.Compare(a.ID(), b.ID()) })
		byID := scopeIndex(scopes)
		switch selection.Mode() {
		case scope.SelectionAll:
			result = scopes
		case scope.SelectionExact:
			for _, id := range selection.ScopeIDs() {
				value, found := byID[id]
				if !found {
					return &scope.NotFoundError{}
				}
				result = append(result, value)
			}
		case scope.SelectionSubtree:
			root, found := byID[selection.RootScopeID()]
			if !found {
				return &scope.NotFoundError{}
			}
			children := make(map[string][]scope.Descriptor)
			for _, value := range scopes {
				children[value.ParentScopeID()] = append(children[value.ParentScopeID()], value)
			}
			result = []scope.Descriptor{root}
			visited := map[string]bool{root.ID(): true}
			for index := 0; index < len(result); index++ {
				for _, child := range children[result[index].ID()] {
					if !visited[child.ID()] {
						visited[child.ID()] = true
						result = append(result, child)
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func validateScopeRelationships(scopes []scope.Descriptor, id, parentID string, references []string) error {
	byID := scopeIndex(scopes)
	visited := map[string]bool{id: true}
	for parentID != "" {
		if visited[parentID] {
			return &scope.RelationshipError{}
		}
		visited[parentID] = true
		parent, found := byID[parentID]
		if !found {
			return &scope.NotFoundError{}
		}
		parentID = parent.ParentScopeID()
	}
	for _, reference := range references {
		if reference == id {
			return &scope.RelationshipError{}
		}
		if _, found := byID[reference]; !found {
			return &scope.NotFoundError{}
		}
	}
	return nil
}

func scopeIndex(scopes []scope.Descriptor) map[string]scope.Descriptor {
	byID := make(map[string]scope.Descriptor, len(scopes))
	for _, value := range scopes {
		byID[value.ID()] = value
	}
	return byID
}
