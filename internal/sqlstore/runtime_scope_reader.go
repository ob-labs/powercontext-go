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

// RuntimeScopeReader adapts one short ScopeRepository read transaction to a
// consumer-owned runtime admission boundary.
type RuntimeScopeReader struct {
	database   *Database
	repository ScopeRepository
}

func NewRuntimeScopeReader(database *Database, repository ScopeRepository) (*RuntimeScopeReader, error) {
	if database == nil {
		return nil, errors.New("sqlstore: Runtime Scope Reader database must not be nil")
	}
	return &RuntimeScopeReader{database: database, repository: repository}, nil
}

func (r *RuntimeScopeReader) Get(ctx context.Context, id string) (scope.Descriptor, bool, error) {
	var result scope.Descriptor
	err := r.database.Transaction(ctx, func(tx DBTX) error {
		value, getErr := r.repository.Get(ctx, tx, id)
		result = value
		return getErr
	})
	if errors.Is(err, sql.ErrNoRows) {
		return scope.Descriptor{}, false, nil
	}
	if err != nil {
		return scope.Descriptor{}, false, err
	}
	return result, true, nil
}
