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

	v1 "github.com/ob-labs/powercontext-go/api/v1"
	"github.com/ob-labs/powercontext-go/internal/runtime"
	"github.com/ob-labs/powercontext-go/internal/scope"
)

// ScopeOperations is the durable Scope binding boundary used by HTTP and MCP.
type ScopeOperations interface {
	Bind(context.Context, scope.BindingKey, string) (scope.Binding, error)
	ClearBinding(context.Context, scope.BindingKey) (bool, error)
	Resolve(context.Context, *string, []scope.BindingKey) (scope.Descriptor, error)
}

func (h *Handler) ResolveScopeBinding(ctx context.Context, req *v1.ResolveScopeBindingRequest) (v1.ResolveScopeBindingRes, error) {
	if h.scopes == nil {
		return nil, &RuntimeNotReadyError{}
	}
	keys, err := wireScopeBindingKeys(req.BindingKeys)
	if err != nil {
		return nil, err
	}
	value, err := h.scopes.Resolve(ctx, optionalString(req.ExplicitScopeID), keys)
	if err != nil {
		return nil, err
	}
	return &v1.ScopeDescriptorHeaders{XPowerContextRequestID: requestID(ctx), Response: wireScopeDescriptor(value)}, nil
}

func (h *Handler) SetScopeBinding(ctx context.Context, req *v1.ScopeBinding) (v1.SetScopeBindingRes, error) {
	if h.scopes == nil {
		return nil, &RuntimeNotReadyError{}
	}
	key, err := wireScopeBindingKey(req.Key)
	if err != nil {
		return nil, err
	}
	value, err := h.scopes.Bind(ctx, key, req.ScopeID)
	if err != nil {
		return nil, err
	}
	return &v1.ScopeBindingHeaders{XPowerContextRequestID: requestID(ctx), Response: wireScopeBinding(value)}, nil
}

func (h *Handler) ClearScopeBinding(ctx context.Context, req *v1.ClearScopeBindingRequest) (v1.ClearScopeBindingRes, error) {
	if h.scopes == nil {
		return nil, &RuntimeNotReadyError{}
	}
	key, err := wireScopeBindingKey(req.Key)
	if err != nil {
		return nil, err
	}
	cleared, err := h.scopes.ClearBinding(ctx, key)
	if err != nil {
		return nil, err
	}
	return &v1.ClearScopeBindingResponseHeaders{
		XPowerContextRequestID: requestID(ctx),
		Response:               v1.ClearScopeBindingResponse{Cleared: cleared},
	}, nil
}

func wireScopeBindingKeys(values []v1.ScopeBindingKey) ([]scope.BindingKey, error) {
	result := make([]scope.BindingKey, len(values))
	for index, value := range values {
		key, err := wireScopeBindingKey(value)
		if err != nil {
			return nil, err
		}
		result[index] = key
	}
	return result, nil
}

func wireScopeBindingKey(value v1.ScopeBindingKey) (scope.BindingKey, error) {
	key, err := scope.NewBindingKey(value.Integration, value.Kind, value.ExternalID)
	if err != nil {
		return scope.BindingKey{}, &scope.ValidationError{}
	}
	return key, nil
}

func wireScopeBinding(value scope.Binding) v1.ScopeBinding {
	return v1.ScopeBinding{Key: wireScopeBindingKeyValue(value.Key()), ScopeID: value.ScopeID()}
}

func wireScopeBindingKeyValue(value scope.BindingKey) v1.ScopeBindingKey {
	return v1.ScopeBindingKey{Integration: value.Integration(), Kind: value.Kind(), ExternalID: value.ExternalID()}
}

func wireScopeDescriptor(value scope.Descriptor) v1.ScopeDescriptor {
	external := make([]v1.ScopeExternalReference, len(value.ExternalReferences()))
	for index, reference := range value.ExternalReferences() {
		external[index] = v1.ScopeExternalReference{Kind: reference.Kind(), Value: reference.Value()}
	}
	parent := v1.OptNilString{}
	if value.ParentScopeID() != "" {
		parent = v1.NewOptNilString(value.ParentScopeID())
	} else {
		parent.SetToNull()
	}
	return v1.ScopeDescriptor{
		ScopeID: value.ID(), Title: value.Title(), Summary: value.Summary(), ParentScopeID: parent,
		ContextReferences: value.ContextReferences(), ExternalReferences: external, Version: int(value.Version()),
	}
}

var _ ScopeOperations = (*runtime.ScopeApplication)(nil)
