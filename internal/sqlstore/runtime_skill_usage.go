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

	"github.com/mattn/go-sqlite3"

	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/artifact/skill"
	"github.com/ob-labs/powercontext-go/source"
)

// RuntimeSkillUsageRecorder validates a package-backed Skill revision and
// persists its bounded usage observation in the same SQLite transaction.
type RuntimeSkillUsageRecorder struct {
	database *Database
	sources  *SourceRepository
	adapter  source.SkillUsageSourceAdapter
}

func NewRuntimeSkillUsageRecorder(database *Database, sources *SourceRepository) (*RuntimeSkillUsageRecorder, error) {
	if database == nil || database.db == nil || sources == nil {
		return nil, errors.New("sqlstore: Skill usage requires a SQLite database and Source repository")
	}
	if _, ok := database.db.Driver().(*sqlite3.SQLiteDriver); !ok {
		return nil, errors.New("sqlstore: Skill usage requires a SQLite database")
	}
	return &RuntimeSkillUsageRecorder{database: database, sources: sources}, nil
}

func (r *RuntimeSkillUsageRecorder) Record(
	ctx context.Context,
	scopeID string,
	capture source.SkillUsageCapture,
) (source.Ref, int64, error) {
	if err := ctx.Err(); err != nil {
		return source.Ref{}, 0, err
	}
	if err := requireScope(scopeID); err != nil {
		return source.Ref{}, 0, err
	}
	usage, err := r.adapter.Resolve(ctx, capture)
	if err != nil {
		return source.Ref{}, 0, err
	}
	var stored StoredSource
	err = r.database.Transaction(ctx, func(tx DBTX) error {
		if verifyErr := r.verify(ctx, tx, scopeID, capture); verifyErr != nil {
			return verifyErr
		}
		var addErr error
		stored, addErr = r.sources.Add(ctx, tx, scopeID, usage)
		return addErr
	})
	if cancelErr := ctx.Err(); cancelErr != nil {
		return source.Ref{}, 0, cancelErr
	}
	if err != nil {
		var conflict *StoredPayloadConflictError
		if errors.As(err, &conflict) {
			return source.Ref{}, 0, &source.ConflictError{Field: "observation_id", Value: capture.ObservationID()}
		}
		return source.Ref{}, 0, err
	}
	return stored.Ref, stored.JournalPosition, nil
}

func (r *RuntimeSkillUsageRecorder) verify(
	ctx context.Context,
	tx DBTX,
	scopeID string,
	capture source.SkillUsageCapture,
) error {
	skillRef, err := artifact.NewRef(capture.SkillRef().Family(), capture.SkillRef().ID(), capture.SkillRef().Revision())
	if err != nil {
		return &source.InvalidSkillUsageError{Field: "skill_ref"}
	}
	row, found, err := findArtifactRow(ctx, tx, scopeID, skillRef)
	if err != nil {
		return err
	}
	if !found {
		return &artifact.NotFoundError{}
	}
	if row.family != skill.Family {
		return &source.InvalidSkillUsageError{Field: "skill_ref"}
	}
	value, err := decodeSkillVariant(ctx, tx, scopeID, row.content)
	if err != nil {
		return err
	}
	content, ok := value.(skill.PackageContent)
	if !ok {
		return &source.InvalidSkillUsageError{Field: "skill_ref"}
	}
	if capture.PackageDigest() != "sha256:"+content.Reference().TreeDigest() {
		return &source.InvalidSkillUsageError{Field: "package_digest"}
	}
	if taskSource, found := capture.TaskSource(); found {
		if _, err := r.sources.Get(ctx, tx, scopeID, taskSource); err != nil {
			var missing *RepositoryNotFoundError
			if errors.As(err, &missing) {
				return &source.SkillUsageTaskSourceNotFoundError{}
			}
			return err
		}
	}
	return ctx.Err()
}
