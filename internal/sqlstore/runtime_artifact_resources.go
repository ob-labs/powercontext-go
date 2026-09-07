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
