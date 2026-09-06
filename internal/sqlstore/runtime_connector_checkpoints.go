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
	"encoding/json/jsontext"
	"errors"

	"github.com/ob-labs/powercontext-go/source"
)

// RuntimeConnectorCheckpointStore is the transaction-owning SQLite adapter
// for Runtime connector lifecycle orchestration. It intentionally relies on
// method-set compatibility instead of importing internal/runtime.
type RuntimeConnectorCheckpointStore struct {
	database   *Database
	repository ConnectorCheckpointRepository
}

// NewRuntimeConnectorCheckpointStore constructs short transaction ownership
// around the immutable checkpoint repository.
func NewRuntimeConnectorCheckpointStore(
	database *Database,
	repository ConnectorCheckpointRepository,
) (*RuntimeConnectorCheckpointStore, error) {
	if database == nil {
		return nil, errors.New("sqlstore: Runtime Connector checkpoint database must not be nil")
	}
	return &RuntimeConnectorCheckpointStore{database: database, repository: repository}, nil
}

// Load preserves absence separately from a JSON null row.
func (s *RuntimeConnectorCheckpointStore) Load(
	ctx context.Context,
	binding source.ConnectorBinding,
) (result source.ConnectorCheckpoint, err error) {
	if bindingErr := binding.Validate(); bindingErr != nil {
		return source.NoConnectorCheckpoint(), bindingErr
	}
	result = source.NoConnectorCheckpoint()
	err = s.database.Transaction(ctx, func(tx DBTX) error {
		value, found, loadErr := s.repository.Load(ctx, tx, binding)
		if loadErr != nil {
			return loadErr
		}
		if !found {
			return nil
		}
		checkpoint, checkpointErr := source.NewConnectorCheckpoint(jsontext.Value(value))
		if checkpointErr != nil {
			return checkpointErr
		}
		result = checkpoint
		return nil
	})
	return result, err
}

// Save delegates the final optimistic raw-row comparison to
// ConnectorCheckpointRepository, whose value-based JSON CAS preserves large
// integers. The lifecycle compares values before opening this short write.
func (s *RuntimeConnectorCheckpointStore) Save(
	ctx context.Context,
	binding source.ConnectorBinding,
	next, expected source.ConnectorCheckpoint,
) error {
	if bindingErr := binding.Validate(); bindingErr != nil {
		return bindingErr
	}
	if nextErr := next.Validate(); nextErr != nil {
		return nextErr
	}
	if !next.Found() {
		return &InvalidRepositoryArgumentError{Field: "checkpoint", Detail: "must contain a JSON value"}
	}
	if expectedErr := expected.Validate(); expectedErr != nil {
		return expectedErr
	}
	nextValue, _ := next.Value()
	expectedValue, expectedFound := expected.Value()
	return s.database.Transaction(ctx, func(tx DBTX) error {
		return s.repository.Save(ctx, tx, binding, nextValue, expectedValue, expectedFound)
	})
}
