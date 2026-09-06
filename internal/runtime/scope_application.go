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
	"errors"

	"github.com/ob-labs/powercontext-go/internal/scope"
)

// ScopeStore is the transaction-owning persistence boundary consumed by ScopeApplication.
type ScopeStore interface {
	Create(context.Context, string, scope.Draft) (scope.Descriptor, error)
	Get(context.Context, string) (scope.Descriptor, bool, error)
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
		created, createErr := a.store.Create(ctx, a.newID(), draft)
		result = created
		return createErr
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

func (a *ScopeApplication) Resolve(ctx context.Context, explicitID string, keys []scope.BindingKey) (result scope.Descriptor, err error) {
	err = a.runtime.Operation(ctx, func(ctx context.Context) error {
		if explicitID != "" {
			value, found, getErr := a.store.Get(ctx, explicitID)
			if getErr != nil {
				return getErr
			}
			if !found {
				return errors.New("runtime: requested Scope was not found")
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
				return errors.New("runtime: bound Scope was not found")
			}
			result = value
			return nil
		}
		value, found, defaultErr := a.store.Default(ctx)
		if defaultErr != nil {
			return defaultErr
		}
		if !found {
			return errors.New("runtime: no Scope binding is available")
		}
		result = value
		return nil
	})
	return result, err
}
