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

// ScopeReadOperations is the consumer-owned read surface required by the
// canonical Scope sidecar. Runtime owns the operation lifecycle; this adapter
// only validates transport input and projects immutable domain values.
type ScopeReadOperations interface {
	List(context.Context) ([]scope.Descriptor, error)
	Get(context.Context, string) (scope.Descriptor, error)
	Default(context.Context) (scope.Descriptor, error)
	Resolve(context.Context, *string, []scope.BindingKey) (scope.Descriptor, error)
	ResolveSelection(context.Context, scope.Selection) ([]scope.Descriptor, error)
}

// CanonicalScopeHandler projects durable Scope reads onto the generated
// canonical Scope contract. It deliberately does not own Scope mutations.
type CanonicalScopeHandler struct {
	operations ScopeReadOperations
}

var _ canonicalscopec.Handler = (*CanonicalScopeHandler)(nil)

func NewCanonicalScopeHandler(operations ScopeReadOperations) *CanonicalScopeHandler {
	return &CanonicalScopeHandler{operations: operations}
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
