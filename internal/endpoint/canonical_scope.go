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

	canonicalscopec "github.com/ob-labs/powercontext-go/api/canonical/scopes"
	"github.com/ob-labs/powercontext-go/internal/scope"
)

// ScopeOperations is the consumer-owned metadata surface required by the
// canonical Scope sidecar. Runtime owns the operation lifecycle; this adapter
// only validates transport input and projects immutable domain values.
type ScopeOperations interface {
	Create(context.Context, scope.Draft) (scope.Descriptor, error)
	Update(context.Context, string, scope.Mutation) (scope.Descriptor, error)
	List(context.Context) ([]scope.Descriptor, error)
	Get(context.Context, string) (scope.Descriptor, error)
	Default(context.Context) (scope.Descriptor, error)
	SetDefault(context.Context, string) (scope.Descriptor, error)
	Resolve(context.Context, *string, []scope.BindingKey) (scope.Descriptor, error)
	ResolveSelection(context.Context, scope.Selection) ([]scope.Descriptor, error)
}

// CanonicalScopeHandler projects durable Scope metadata onto the generated
// canonical Scope contract. Runtime retains mutation lifecycle ownership.
type CanonicalScopeHandler struct {
	operations ScopeOperations
}

var _ canonicalscopec.Handler = (*CanonicalScopeHandler)(nil)

func NewCanonicalScopeHandler(operations ScopeOperations) *CanonicalScopeHandler {
	return &CanonicalScopeHandler{operations: operations}
}

func (h *CanonicalScopeHandler) CreateScope(
	ctx context.Context,
	request *canonicalscopec.CreateScopeRequest,
) (canonicalscopec.CreateScopeRes, error) {
	if err := h.available(); err != nil {
		return nil, err
	}
	if request == nil {
		return nil, &scope.ValidationError{}
	}
	parent, err := canonicalParentScopeID(request.ParentScopeID)
	if err != nil {
		return nil, err
	}
	external, err := canonicalScopeExternalReferences(request.ExternalReferences)
	if err != nil {
		return nil, err
	}
	draft, err := scope.NewDraft(
		request.Title,
		request.Summary,
		parent,
		request.ContextReferences,
		external,
		request.IdempotencyKey,
	)
	if err != nil {
		return nil, &scope.ValidationError{}
	}
	created, err := h.operations.Create(ctx, draft)
	if err != nil {
		return nil, err
	}
	return scopeDescriptor(created)
}

func (h *CanonicalScopeHandler) ListScopes(ctx context.Context) (canonicalscopec.ListScopesRes, error) {
	if err := h.available(); err != nil {
		return nil, err
	}
	values, err := h.operations.List(ctx)
	if err != nil {
		return nil, err
	}
	return scopePage(values)
}

func (h *CanonicalScopeHandler) GetScope(
	ctx context.Context,
	params canonicalscopec.GetScopeParams,
) (canonicalscopec.GetScopeRes, error) {
	if err := h.available(); err != nil {
		return nil, err
	}
	if _, err := scope.NewExactSelection([]string{params.ScopeID}); err != nil {
		return nil, &scope.ValidationError{}
	}
	value, err := h.operations.Get(ctx, params.ScopeID)
	if err != nil {
		return nil, err
	}
	return scopeDescriptor(value)
}

func (h *CanonicalScopeHandler) GetDefaultScope(ctx context.Context) (canonicalscopec.GetDefaultScopeRes, error) {
	if err := h.available(); err != nil {
		return nil, err
	}
	value, err := h.operations.Default(ctx)
	if err != nil {
		return nil, err
	}
	return scopeDescriptor(value)
}

func (h *CanonicalScopeHandler) SetDefaultScope(
	ctx context.Context,
	request *canonicalscopec.SetDefaultScopeRequest,
) (canonicalscopec.SetDefaultScopeRes, error) {
	if err := h.available(); err != nil {
		return nil, err
	}
	if request == nil {
		return nil, &scope.ValidationError{}
	}
	if _, err := scope.NewExactSelection([]string{request.ScopeID}); err != nil {
		return nil, &scope.ValidationError{}
	}
	value, err := h.operations.SetDefault(ctx, request.ScopeID)
	if err != nil {
		return nil, err
	}
	return scopeDescriptor(value)
}

func (h *CanonicalScopeHandler) UpdateScope(
	ctx context.Context,
	request *canonicalscopec.UpdateScopeRequest,
	params canonicalscopec.UpdateScopeParams,
) (canonicalscopec.UpdateScopeRes, error) {
	if err := h.available(); err != nil {
		return nil, err
	}
	if request == nil {
		return nil, &scope.ValidationError{}
	}
	if _, err := scope.NewExactSelection([]string{params.ScopeID}); err != nil {
		return nil, &scope.ValidationError{}
	}
	parent, err := canonicalParentScopeID(request.ParentScopeID)
	if err != nil {
		return nil, err
	}
	external, err := canonicalScopeExternalReferences(request.ExternalReferences)
	if err != nil {
		return nil, err
	}
	mutation, err := scope.NewMutation(
		int64(request.ExpectedVersion),
		request.Title,
		request.Summary,
		parent,
		request.ContextReferences,
		external,
	)
	if err != nil {
		return nil, &scope.ValidationError{}
	}
	updated, err := h.operations.Update(ctx, params.ScopeID, mutation)
	if err != nil {
		return nil, err
	}
	return scopeDescriptor(updated)
}

func (h *CanonicalScopeHandler) ResolveScopeSelection(
	ctx context.Context,
	request *canonicalscopec.ResolveScopeSelectionRequest,
) (canonicalscopec.ResolveScopeSelectionRes, error) {
	if err := h.available(); err != nil {
		return nil, err
	}
	if request == nil {
		return nil, &scope.ValidationError{}
	}
	selection, err := canonicalScopeSelection(request.Selection)
	if err != nil {
		return nil, err
	}
	values, err := h.operations.ResolveSelection(ctx, selection)
	if err != nil {
		return nil, err
	}
	return scopePage(values)
}

func (h *CanonicalScopeHandler) ResolveScopeBinding(
	ctx context.Context,
	request *canonicalscopec.ResolveScopeBindingRequest,
) (canonicalscopec.ResolveScopeBindingRes, error) {
	if err := h.available(); err != nil {
		return nil, err
	}
	if request == nil {
		return nil, &scope.ValidationError{}
	}
	keys, err := canonicalScopeBindingKeys(request.BindingKeys)
	if err != nil {
		return nil, err
	}
	var explicit *string
	if value, found := request.ExplicitScopeID.Get(); found {
		if _, validationErr := scope.NewExactSelection([]string{value}); validationErr != nil {
			return nil, &scope.ValidationError{}
		}
		explicit = new(value)
	}
	resolved, err := h.operations.Resolve(ctx, explicit, keys)
	if err != nil {
		return nil, err
	}
	return scopeDescriptor(resolved)
}

func (h *CanonicalScopeHandler) available() error {
	if h == nil || h.operations == nil {
		return &RuntimeNotReadyError{}
	}
	return nil
}

func canonicalScopeSelection(value canonicalscopec.ScopeSelection) (scope.Selection, error) {
	switch value.Type {
	case canonicalscopec.AllScopeSelectionScopeSelection:
		return scope.AllSelection(), nil
	case canonicalscopec.ExactScopeSelectionScopeSelection:
		selection, err := scope.NewExactSelection(value.ExactScopeSelection.ScopeIds)
		if err != nil {
			return scope.Selection{}, &scope.ValidationError{}
		}
		return selection, nil
	case canonicalscopec.SubtreeScopeSelectionScopeSelection:
		selection, err := scope.NewSubtreeSelection(value.SubtreeScopeSelection.RootScopeID)
		if err != nil {
			return scope.Selection{}, &scope.ValidationError{}
		}
		return selection, nil
	default:
		return scope.Selection{}, &scope.ValidationError{}
	}
}

func canonicalScopeBindingKeys(values []canonicalscopec.ScopeBindingKey) ([]scope.BindingKey, error) {
	keys := make([]scope.BindingKey, 0, len(values))
	for _, value := range values {
		switch value.Integration {
		case canonicalscopec.ScopeBindingKeyIntegrationCodex, canonicalscopec.ScopeBindingKeyIntegrationWorkbuddy:
		default:
			return nil, &scope.ValidationError{}
		}
		key, err := scope.NewBindingKey(string(value.Integration), value.Kind, value.ExternalID)
		if err != nil {
			return nil, &scope.ValidationError{}
		}
		keys = append(keys, key)
	}
	return keys, nil
}

func canonicalParentScopeID(value canonicalscopec.OptNilString) (string, error) {
	if value.IsEmpty() || value.IsNull() {
		return "", nil
	}
	parent, found := value.Get()
	if !found {
		return "", &scope.ValidationError{}
	}
	if _, err := scope.NewExactSelection([]string{parent}); err != nil {
		return "", &scope.ValidationError{}
	}
	return parent, nil
}

func canonicalScopeExternalReferences(values []canonicalscopec.ScopeExternalReference) ([]scope.ExternalReference, error) {
	references := make([]scope.ExternalReference, 0, len(values))
	for _, value := range values {
		reference, err := scope.NewExternalReference(value.Kind, value.Value)
		if err != nil {
			return nil, &scope.ValidationError{}
		}
		references = append(references, reference)
	}
	return references, nil
}

func scopePage(values []scope.Descriptor) (*canonicalscopec.ScopePage, error) {
	items := make([]canonicalscopec.ScopeDescriptor, 0, len(values))
	for _, value := range values {
		item, err := scopeDescriptorValue(value)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return &canonicalscopec.ScopePage{Items: items}, nil
}

func scopeDescriptor(value scope.Descriptor) (*canonicalscopec.ScopeDescriptor, error) {
	result, err := scopeDescriptorValue(value)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func scopeDescriptorValue(value scope.Descriptor) (canonicalscopec.ScopeDescriptor, error) {
	if value.Version() < 1 || int64(int(value.Version())) != value.Version() {
		return canonicalscopec.ScopeDescriptor{}, &RuntimeNotReadyError{}
	}
	external := value.ExternalReferences()
	externalReferences := make([]canonicalscopec.ScopeExternalReference, 0, len(external))
	for _, reference := range external {
		externalReferences = append(externalReferences, canonicalscopec.ScopeExternalReference{
			Kind: reference.Kind(), Value: reference.Value(),
		})
	}
	result := canonicalscopec.ScopeDescriptor{
		ContextReferences:  value.ContextReferences(),
		ExternalReferences: externalReferences,
		ScopeID:            value.ID(),
		Summary:            value.Summary(),
		Title:              value.Title(),
		Version:            int(value.Version()),
	}
	if parent := value.ParentScopeID(); parent != "" {
		result.ParentScopeID = canonicalscopec.NewOptNilString(parent)
	}
	return result, nil
}
