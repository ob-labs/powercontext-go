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

	"github.com/ob-labs/powercontext-go/artifact"
)

type RuntimeArtifactReader struct {
	database   *Database
	repository *ArtifactRepository
}

func NewRuntimeArtifactReader(database *Database, repository *ArtifactRepository) (*RuntimeArtifactReader, error) {
	if database == nil || repository == nil || repository.dialect != SQLiteDialect {
		return nil, errors.New("sqlstore: Artifact resource reads require SQLite dependencies")
	}
	return &RuntimeArtifactReader{database: database, repository: repository}, nil
}

func (r *RuntimeArtifactReader) ReadArtifact(ctx context.Context, scopeID, family, artifactID string, revision int64) (artifact.Snapshot, error) {
	var value artifact.Snapshot
	err := r.database.Transaction(ctx, func(tx DBTX) error {
		var readErr error
		if revision == 0 {
			value, readErr = r.repository.Latest(ctx, tx, scopeID, family, artifactID)
		} else {
			ref, refErr := artifact.NewRef(family, artifactID, revision)
			if refErr != nil {
				return refErr
			}
			value, readErr = r.repository.Get(ctx, tx, scopeID, ref)
		}
		return readErr
	})
	if err != nil {
		if missing, ok := errors.AsType[*RepositoryNotFoundError](err); ok && missing.Kind == "artifact" {
			return nil, &artifact.NotFoundError{}
		}
		return nil, err
	}
	return value, nil
}

func (r *RuntimeArtifactReader) ReadArtifactPage(ctx context.Context, scopeID, family, after string, limit int) ([]artifact.Snapshot, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if err := requireScope(scopeID); err != nil {
		return nil, false, err
	}
	if limit < 1 || limit > 100 {
		return nil, false, &artifact.InvalidReferenceError{Field: "limit", Detail: "must be between 1 and 100"}
	}
	if after != "" {
		if _, refErr := artifact.NewRef(family, after, 1); refErr != nil {
			return nil, false, refErr
		}
	}
	values := make([]artifact.Snapshot, 0, limit)
	var hasMore bool
	err := r.database.Transaction(ctx, func(tx DBTX) error {
		refs, readErr := readArtifactPageHeads(ctx, tx, scopeID, family, after, limit+1)
		if readErr != nil {
			return readErr
		}
		hasMore = len(refs) > limit
		for _, ref := range refs[:min(len(refs), limit)] {
			value, getErr := r.repository.Get(ctx, tx, scopeID, ref)
			if getErr != nil {
				return getErr
			}
			values = append(values, value)
		}
		return ctx.Err()
	})
	if err != nil {
		return nil, false, err
	}
	return values, hasMore, nil
}

// Close the head iterator before decoding revisions on the same transaction.
func readArtifactPageHeads(ctx context.Context, tx DBTX, scopeID, family, after string, limit int) (refs []artifact.Ref, returnErr error) {
	rows, err := tx.QueryContext(ctx, `SELECT artifact_id, revision FROM pc_artifact_heads
        WHERE scope_id = ? AND family = ? AND artifact_id COLLATE BINARY > ?
        ORDER BY artifact_id COLLATE BINARY ASC LIMIT ?`, scopeID, family, after, limit)
	if err != nil {
		return nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, rows.Close()) }()
	for rows.Next() {
		var id string
		var revision int64
		if scanErr := rows.Scan(&id, &revision); scanErr != nil {
			return nil, scanErr
		}
		ref, refErr := artifact.NewRef(family, id, revision)
		if refErr != nil {
			return nil, refErr
		}
		refs = append(refs, ref)
	}
	return refs, rows.Err()
}
