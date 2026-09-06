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

package sqlstore

import (
	"context"
	"database/sql"
	"errors"

	"github.com/ob-labs/powercontext-go/internal/scope"
)

// RuntimeScopeStore adapts ScopeRepository to a transaction-owning runtime boundary.
type RuntimeScopeStore struct {
	database   *Database
	repository ScopeRepository
}

func NewRuntimeScopeStore(database *Database, repository ScopeRepository) (*RuntimeScopeStore, error) {
	if database == nil {
		return nil, errors.New("sqlstore: Runtime Scope dependencies must not be nil")
	}
	return &RuntimeScopeStore{database: database, repository: repository}, nil
}

func (s *RuntimeScopeStore) Create(ctx context.Context, id string, draft scope.Draft) (result scope.Descriptor, err error) {
	err = s.database.Transaction(ctx, func(tx DBTX) error {
		created, createErr := s.repository.Create(ctx, tx, id, draft)
		result = created
		return createErr
	})
	return result, err
}

func (s *RuntimeScopeStore) Get(ctx context.Context, id string) (scope.Descriptor, bool, error) {
	var result scope.Descriptor
	err := s.database.Transaction(ctx, func(tx DBTX) error {
		value, getErr := s.repository.Get(ctx, tx, id)
		result = value
		return getErr
	})
	if errors.Is(err, sql.ErrNoRows) {
		return scope.Descriptor{}, false, nil
	}
	return result, err == nil, err
}

func (s *RuntimeScopeStore) SetDefault(ctx context.Context, id string) (result scope.Descriptor, err error) {
	err = s.database.Transaction(ctx, func(tx DBTX) error {
		value, getErr := s.repository.Get(ctx, tx, id)
		if getErr != nil {
			return getErr
		}
		if setErr := s.repository.SetDefault(ctx, tx, id); setErr != nil {
			return setErr
		}
		result = value
		return nil
	})
	return result, err
}

func (s *RuntimeScopeStore) Default(ctx context.Context) (scope.Descriptor, bool, error) {
	var result scope.Descriptor
	var found bool
	err := s.database.Transaction(ctx, func(tx DBTX) error {
		value, defaultFound, defaultErr := s.repository.Default(ctx, tx)
		result, found = value, defaultFound
		return defaultErr
	})
	return result, found, err
}

func (s *RuntimeScopeStore) SetBinding(ctx context.Context, key scope.BindingKey, id string) (result scope.Binding, err error) {
	err = s.database.Transaction(ctx, func(tx DBTX) error {
		if _, getErr := s.repository.Get(ctx, tx, id); getErr != nil {
			return getErr
		}
		binding, bindErr := s.repository.SetBinding(ctx, tx, key, id)
		result = binding
		return bindErr
	})
	return result, err
}

func (s *RuntimeScopeStore) Binding(ctx context.Context, key scope.BindingKey) (scope.Binding, bool, error) {
	var result scope.Binding
	var found bool
	err := s.database.Transaction(ctx, func(tx DBTX) error {
		binding, bindingFound, bindingErr := s.repository.Binding(ctx, tx, key)
		result, found = binding, bindingFound
		return bindingErr
	})
	return result, found, err
}
