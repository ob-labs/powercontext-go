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
	"errors"

	"github.com/ob-labs/powercontext-go/source"
)

// RuntimeSourceBackend is the use-case-shaped adapter consumed by
// runtime.SourceApplication. Source resolution remains outside the SQL
// transaction; journal allocation and idempotent insertion are atomic.
type RuntimeSourceBackend struct {
	database   *Database
	repository *SourceRepository
	adapter    source.ContentAdapter
}

func NewRuntimeSourceBackend(database *Database, repository *SourceRepository) (*RuntimeSourceBackend, error) {
	if database == nil || repository == nil {
		return nil, errors.New("sqlstore: Runtime Source dependencies must not be nil")
	}
	return &RuntimeSourceBackend{database: database, repository: repository}, nil
}

func (b *RuntimeSourceBackend) Capture(
	ctx context.Context,
	scopeID string,
	capture source.ContentCapture,
) (source.Ref, int64, error) {
	value, err := b.adapter.Resolve(ctx, capture)
	if err != nil {
		return source.Ref{}, 0, err
	}
	var stored StoredSource
	err = b.database.Transaction(ctx, func(tx DBTX) error {
		var addErr error
		stored, addErr = b.repository.Add(ctx, tx, scopeID, value)
		return addErr
	})
	if err != nil {
		var conflict *StoredPayloadConflictError
		if errors.As(err, &conflict) {
			return source.Ref{}, 0, &source.ConflictError{Field: "source_id", Value: capture.ID()}
		}
		return source.Ref{}, 0, err
	}
	return stored.Ref, stored.JournalPosition, nil
}

// Entries returns a stable decoded snapshot of one scoped Source journal.
func (b *RuntimeSourceBackend) Entries(ctx context.Context, scopeID string) ([]source.JournalEntry, error) {
	var stored []StoredSource
	err := b.database.Transaction(ctx, func(tx DBTX) error {
		var listErr error
		stored, listErr = b.repository.List(ctx, tx, scopeID, 0, nil)
		return listErr
	})
	if err != nil {
		return nil, err
	}
	result := make([]source.JournalEntry, len(stored))
	for index, value := range stored {
		entry, entryErr := source.NewJournalEntry(value.Ref, value.Value, value.JournalPosition)
		if entryErr != nil {
			return nil, entryErr
		}
		result[index] = entry
	}
	return result, nil
}

// ScopeIDs returns only partitions that own a Source journal, in deterministic
// database byte order. It intentionally does not infer Scopes from Artifacts
// or configuration.
func (b *RuntimeSourceBackend) ScopeIDs(ctx context.Context) ([]string, error) {
	var result []string
	err := b.database.Transaction(ctx, func(tx DBTX) (returnErr error) {
		rows, err := tx.QueryContext(ctx,
			"SELECT scope_id FROM pc_source_journal_heads ORDER BY scope_id",
		)
		if err != nil {
			return err
		}
		defer func() { returnErr = errors.Join(returnErr, rows.Close()) }()
		for rows.Next() {
			var scopeID string
			if err := rows.Scan(&scopeID); err != nil {
				return err
			}
			result = append(result, scopeID)
		}
		return rows.Err()
	})
	return result, err
}
