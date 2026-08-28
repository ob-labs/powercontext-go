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
	"fmt"

	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/artifact/experience"
	"github.com/ob-labs/powercontext-go/artifact/memory"
)

type ExperienceIndex interface {
	Initialize(context.Context, DBTX) error
	Replace(context.Context, DBTX, string, experience.Experience) error
	Search(context.Context, DBTX, string, string, int) ([]experience.SearchHit, error)
}

type NoExperienceIndex struct{}

func (NoExperienceIndex) Initialize(context.Context, DBTX) error { return nil }
func (NoExperienceIndex) Replace(context.Context, DBTX, string, experience.Experience) error {
	return nil
}

func (NoExperienceIndex) Search(
	context.Context,
	DBTX,
	string,
	string,
	int,
) ([]experience.SearchHit, error) {
	return []experience.SearchHit{}, nil
}

// SQLiteExperienceFTSIndex maintains approved Experience heads in FTS5.
type SQLiteExperienceFTSIndex struct{}

func (SQLiteExperienceFTSIndex) Initialize(ctx context.Context, db DBTX) error {
	if err := EnsureArtifactHeadSearchableText(ctx, db, SQLiteDialect); err != nil {
		return err
	}
	return rebuildExperienceProjectionsAndSQLiteFTS(ctx, db)
}

func rebuildExperienceProjectionsAndSQLiteFTS(ctx context.Context, db DBTX) error {
	if err := RebuildExperienceProjections(ctx, db); err != nil {
		return err
	}
	for _, statement := range []string{
		`CREATE VIRTUAL TABLE IF NOT EXISTS pc_experience_fts USING fts5(
            scope_id UNINDEXED,
            artifact_id UNINDEXED,
            revision UNINDEXED,
            searchable_text,
            tokenize='unicode61'
        )`,
		`DELETE FROM pc_experience_fts`,
		`INSERT INTO pc_experience_fts (scope_id, artifact_id, revision, searchable_text)
         SELECT scope_id, artifact_id, revision, searchable_text FROM pc_artifact_heads
         WHERE family = 'experience' AND searchable_text IS NOT NULL`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return &memory.CapabilityNotSupportedError{Capability: "sqlite-experience-fts", Detail: err.Error()}
		}
	}
	var probe any
	if err := db.QueryRowContext(ctx,
		`SELECT rowid FROM pc_experience_fts WHERE pc_experience_fts MATCH 'powercontext' LIMIT 1`,
	).Scan(&probe); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return &memory.CapabilityNotSupportedError{Capability: "sqlite-experience-fts", Detail: err.Error()}
	}
	return nil
}

// EnsureArtifactHeadSearchableText upgrades databases created before the
// rebuildable Experience projection was added. It is safe to call repeatedly.
func EnsureArtifactHeadSearchableText(ctx context.Context, db DBTX, dialect Dialect) error {
	var count int64
	query, migration, err := artifactHeadSearchableTextStatements(dialect)
	if err != nil {
		return err
	}
	if err := db.QueryRowContext(ctx, query).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		_, err := db.ExecContext(ctx, migration)
		return err
	}
	return nil
}

func artifactHeadSearchableTextStatements(dialect Dialect) (query, migration string, err error) {
	switch dialect {
	case SQLiteDialect:
		query = `SELECT COUNT(*) FROM pragma_table_info('pc_artifact_heads') WHERE name = 'searchable_text'`
		migration = `ALTER TABLE pc_artifact_heads ADD COLUMN searchable_text TEXT NULL`
	case MySQLDialect:
		query = `SELECT COUNT(*) FROM information_schema.columns
            WHERE table_schema = DATABASE() AND table_name = 'pc_artifact_heads' AND column_name = 'searchable_text'`
		migration = `ALTER TABLE pc_artifact_heads ADD COLUMN searchable_text MEDIUMTEXT NULL`
	default:
		return "", "", &InvalidRepositoryArgumentError{Field: "dialect", Detail: "unsupported database dialect"}
	}
	return query, migration, nil
}

// RebuildExperienceProjections refreshes the relational searchable_text field
// from immutable approved Experience heads without touching backend indexes.
func RebuildExperienceProjections(ctx context.Context, db DBTX) error {
	rows, err := db.QueryContext(ctx, `SELECT h.scope_id, h.artifact_id, h.revision, a.content
        FROM pc_artifact_heads AS h
        JOIN pc_artifacts AS a
          ON a.scope_id = h.scope_id AND a.family = h.family
         AND a.artifact_id = h.artifact_id AND a.revision = h.revision
        WHERE h.family = ? ORDER BY h.scope_id, h.artifact_id`, experience.Family)
	if err != nil {
		return err
	}
	type projection struct {
		scopeID, artifactID, searchable string
		revision                        int64
	}
	projections := make([]projection, 0)
	for rows.Next() {
		var scopeID, artifactID string
		var revision, content any
		if err := rows.Scan(&scopeID, &artifactID, &revision, &content); err != nil {
			rows.Close()
			return err
		}
		decodedRevision, ok := integer(revision)
		if !ok {
			rows.Close()
			return &InvalidStoredColumnError{Column: "revision", Expected: "an integer"}
		}
		value, err := DecodeExperienceStoredValue(content)
		if err != nil {
			rows.Close()
			return err
		}
		projections = append(projections, projection{
			scopeID: scopeID, artifactID: artifactID, revision: decodedRevision,
			searchable: experience.SearchableText(value),
		})
	}
	if err := closeRows(rows); err != nil {
		return err
	}
	for _, projection := range projections {
		if err := replaceExperienceSearchableText(
			ctx, db, projection.scopeID, projection.artifactID, projection.revision, projection.searchable,
		); err != nil {
			return err
		}
	}
	return nil
}

// ReplaceExperienceProjection updates the rebuildable relational projection.
func ReplaceExperienceProjection(ctx context.Context, db DBTX, scopeID string, value experience.Experience) error {
	return replaceExperienceSearchableText(ctx, db, scopeID, value.ID(), value.Revision(), experience.SearchableText(value.Content()))
}

// DecodeExperienceStoredValue applies the authoritative Artifact codec to a
// driver-returned payload value for backend-specific indexes.
func DecodeExperienceStoredValue(content any) (experience.Content, error) {
	payload, err := storedBytes(content, "content")
	if err != nil {
		return experience.Content{}, err
	}
	decoded, err := ExperienceArtifactCodec().decodeContent(payload)
	if err != nil {
		return experience.Content{}, err
	}
	value, ok := decoded.(experience.Content)
	if !ok {
		return experience.Content{}, fmt.Errorf("sqlstore: Experience codec returned %T", decoded)
	}
	return value, nil
}

func (SQLiteExperienceFTSIndex) Replace(
	ctx context.Context,
	db DBTX,
	scopeID string,
	value experience.Experience,
) error {
	searchable := experience.SearchableText(value.Content())
	if err := ReplaceExperienceProjection(ctx, db, scopeID, value); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx,
		"DELETE FROM pc_experience_fts WHERE scope_id = ? AND artifact_id = ?",
		scopeID, value.ID()); err != nil {
		return err
	}
	_, err := db.ExecContext(ctx, `INSERT INTO pc_experience_fts
        (scope_id, artifact_id, revision, searchable_text) VALUES (?, ?, ?, ?)`,
		scopeID, value.ID(), value.Revision(), searchable)
	return err
}

func (SQLiteExperienceFTSIndex) Search(
	ctx context.Context,
	db DBTX,
	scopeID, query string,
	limit int,
) ([]experience.SearchHit, error) {
	match, ok := memory.FTSMatchQuery(query)
	if !ok {
		return []experience.SearchHit{}, nil
	}
	rows, err := db.QueryContext(ctx, `SELECT f.artifact_id, f.revision, a.content
        FROM pc_experience_fts AS f
        JOIN pc_artifacts AS a
          ON a.scope_id = f.scope_id AND a.family = 'experience'
         AND a.artifact_id = f.artifact_id AND a.revision = f.revision
        WHERE pc_experience_fts MATCH ? AND f.scope_id = ?
        ORDER BY bm25(pc_experience_fts), f.artifact_id, f.revision LIMIT ?`,
		match, scopeID, limit*4)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	codec := ExperienceArtifactCodec()
	hits := make([]experience.SearchHit, 0, limit)
	for rows.Next() {
		var artifactID string
		var revision, content any
		if err := rows.Scan(&artifactID, &revision, &content); err != nil {
			return nil, err
		}
		decodedRevision, ok := integer(revision)
		if !ok {
			return nil, &InvalidStoredColumnError{Column: "revision", Expected: "an integer"}
		}
		payload, err := storedBytes(content, "content")
		if err != nil {
			return nil, err
		}
		decoded, err := codec.decodeContent(payload)
		if err != nil {
			return nil, err
		}
		value := decoded.(experience.Content)
		if !memory.AdmitsFTSText(query, experience.SearchText(value)) {
			continue
		}
		ref, err := artifact.NewRef(experience.Family, artifactID, decodedRevision)
		if err != nil {
			return nil, err
		}
		hits = append(hits, experience.SearchHit{ArtifactRef: ref, Content: value})
		if len(hits) == limit {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return hits, nil
}

func replaceExperienceSearchableText(
	ctx context.Context,
	db DBTX,
	scopeID, artifactID string,
	revision int64,
	searchable string,
) error {
	result, err := db.ExecContext(ctx, `UPDATE pc_artifact_heads SET searchable_text = ?
        WHERE scope_id = ? AND family = ? AND artifact_id = ? AND revision = ?`,
		searchable, scopeID, experience.Family, artifactID, revision)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		ref, _ := artifact.NewRef(experience.Family, artifactID, revision)
		return &RepositoryNotFoundError{Kind: "artifact head", Identity: ref}
	}
	return nil
}
