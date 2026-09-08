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
)

// SkillPackageRequiredError distinguishes an existing non-package Artifact
// from an absent revision without exposing its identity or content.
type SkillPackageRequiredError struct{}

func (*SkillPackageRequiredError) Error() string {
	return "artifact does not contain a managed Skill package"
}

// RuntimeSkillPackageReader reads exact package revisions from one SQLite snapshot.
type RuntimeSkillPackageReader struct {
	database *Database
}

func NewRuntimeSkillPackageReader(database *Database) (*RuntimeSkillPackageReader, error) {
	if database == nil || database.db == nil {
		return nil, errors.New("sqlstore: Skill package reads require a SQLite database")
	}
	if _, ok := database.db.Driver().(*sqlite3.SQLiteDriver); !ok {
		return nil, errors.New("sqlstore: Skill package reads require a SQLite database")
	}
	return &RuntimeSkillPackageReader{database: database}, nil
}

func (r *RuntimeSkillPackageReader) ReadSkillPackage(ctx context.Context, scopeID string, ref artifact.Ref) (skill.PackageSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return skill.PackageSnapshot{}, err
	}
	if err := requireScope(scopeID); err != nil {
		return skill.PackageSnapshot{}, err
	}
	if err := ref.Validate(); err != nil {
		return skill.PackageSnapshot{}, err
	}
	var result skill.PackageSnapshot
	err := r.database.Transaction(ctx, func(tx DBTX) error {
		row, found, findErr := findArtifactRow(ctx, tx, scopeID, ref)
		if findErr != nil {
			return findErr
		}
		if !found {
			return &artifact.NotFoundError{}
		}
		if row.family != skill.Family {
			return &SkillPackageRequiredError{}
		}
		// The legacy Artifact decoder intentionally hides package absence as
		// corruption. This read owns a separate absence contract.
		value, decodeErr := decodeSkillVariant(ctx, tx, scopeID, row.content)
		if decodeErr != nil {
			return decodeErr
		}
		content, ok := value.(skill.PackageContent)
		if !ok {
			return &SkillPackageRequiredError{}
		}
		result = content.Snapshot()
		return ctx.Err()
	})
	if cancelErr := ctx.Err(); cancelErr != nil {
		return skill.PackageSnapshot{}, cancelErr
	}
	if err != nil {
		if _, missing := errors.AsType[*SkillPackageNotFoundError](err); missing {
			return skill.PackageSnapshot{}, &artifact.NotFoundError{}
		}
		return skill.PackageSnapshot{}, &skillPackageReadError{cause: err}
	}
	return result, nil
}

// Preserve typed causes for classification without displaying storage or JSON diagnostics.
type skillPackageReadError struct{ cause error }

func (*skillPackageReadError) Error() string   { return "managed Skill package could not be read" }
func (e *skillPackageReadError) Unwrap() error { return e.cause }
