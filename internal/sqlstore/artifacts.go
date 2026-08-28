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
	"reflect"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/mattn/go-sqlite3"

	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/source"
)

// ArtifactRepository persists immutable revisions and direct ordered lineage.
type ArtifactRepository struct {
	dialect  Dialect
	byFamily map[string]ArtifactCodec
}

func NewArtifactRepository(dialect Dialect, codecs ...ArtifactCodec) (*ArtifactRepository, error) {
	if dialect != SQLiteDialect && dialect != MySQLDialect {
		return nil, fmt.Errorf("sqlstore: unsupported dialect %q", dialect)
	}
	repository := &ArtifactRepository{dialect: dialect, byFamily: make(map[string]ArtifactCodec, len(codecs))}
	for _, codec := range codecs {
		if _, exists := repository.byFamily[codec.family]; exists {
			return nil, &CodecConflictError{Route: "artifact family", Value: codec.family}
		}
		repository.byFamily[codec.family] = codec
	}
	return repository, nil
}

func (r *ArtifactRepository) Create(
	ctx context.Context,
	db DBTX,
	scopeID string,
	artifactID string,
	draft artifact.DraftSnapshot,
) (artifact.Snapshot, error) {
	if err := requireScope(scopeID); err != nil {
		return nil, err
	}
	if draft == nil {
		return nil, &InvalidRepositoryArgumentError{Field: "draft", Detail: "must not be nil"}
	}
	codec, err := r.codecForDraft(draft)
	if err != nil {
		return nil, err
	}
	ref, err := artifact.NewRef(draft.Family(), artifactID, 1)
	if err != nil {
		return nil, err
	}
	// The immutable revision key is the create CAS. Do not read the head first:
	// on SQLite a read-before-write transaction can lose a concurrent create
	// with SQLITE_BUSY_SNAPSHOT instead of the stable RevisionConflictError.
	// A direct insert waits for the competing writer and then either succeeds or
	// observes its committed unique key on every supported backend.
	created, err := r.insertRevision(ctx, db, scopeID, codec, ref, draft.ContentValue(), draft.Lineage())
	if err != nil {
		return nil, r.normalizeCreateIntegrity(ctx, db, scopeID, ref, err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO pc_artifact_heads
        (scope_id, family, artifact_id, revision) VALUES (?, ?, ?, ?)`,
		scopeID, ref.Family(), ref.ID(), ref.Revision()); err != nil {
		return nil, r.normalizeCreateIntegrity(ctx, db, scopeID, ref, err)
	}
	return created, nil
}

// normalizeCreateIntegrity turns a competing create into a stable Revision
// conflict. Only a now-committed head has that meaning; unrelated foreign-key,
// check, or lineage violations retain their original database error.
func (r *ArtifactRepository) normalizeCreateIntegrity(
	ctx context.Context,
	db DBTX,
	scopeID string,
	requested artifact.Ref,
	cause error,
) error {
	if !isIntegrityConstraint(cause) {
		return cause
	}
	revision, found, err := r.findHead(ctx, db, scopeID, requested.Family(), requested.ID(), false)
	if err != nil {
		return err
	}
	if !found {
		return cause
	}
	current, err := artifact.NewRef(requested.Family(), requested.ID(), revision)
	if err != nil {
		return err
	}
	return &artifact.RevisionConflictError{Requested: requested, Current: current}
}

func isIntegrityConstraint(err error) bool {
	var sqliteError sqlite3.Error
	if errors.As(err, &sqliteError) && sqliteError.Code == sqlite3.ErrConstraint {
		return true
	}
	var mysqlError *mysql.MySQLError
	if !errors.As(err, &mysqlError) {
		return false
	}
	switch mysqlError.Number {
	case 1022, 1048, 1062, 1169, 1216, 1217, 1451, 1452, 3819:
		return true
	default:
		return false
	}
}

func (r *ArtifactRepository) Revise(
	ctx context.Context,
	db DBTX,
	scopeID string,
	current artifact.Snapshot,
	draft artifact.DraftSnapshot,
) (artifact.Snapshot, error) {
	if err := requireScope(scopeID); err != nil {
		return nil, err
	}
	if current == nil || draft == nil {
		return nil, &InvalidRepositoryArgumentError{Field: "artifact", Detail: "artifact and draft must not be nil"}
	}
	if current.Ref().Family() != draft.Family() {
		return nil, &artifact.FamilyMismatchError{
			ArtifactFamily: current.Ref().Family(),
			DraftFamily:    draft.Family(),
		}
	}
	codec, err := r.codecForDraft(draft)
	if err != nil {
		return nil, err
	}
	if reflect.TypeOf(current.ContentValue()) != codec.contentType {
		return nil, &artifact.FamilyMismatchError{
			ArtifactFamily: current.Ref().Family(),
			DraftFamily:    draft.Family(),
		}
	}
	if err := current.Ref().Validate(); err != nil {
		return nil, err
	}
	// This is intentionally the first database access. SQLite acquires its
	// write reservation; MySQL/OceanBase locks a matching row.
	if _, err := db.ExecContext(ctx, `UPDATE pc_artifact_heads SET revision = revision
        WHERE scope_id = ? AND family = ? AND artifact_id = ? AND revision = ?`,
		scopeID, current.Ref().Family(), current.Ref().ID(), current.Ref().Revision()); err != nil {
		return nil, err
	}
	lockedRevision, found, err := r.findHead(ctx, db, scopeID, current.Ref().Family(), current.Ref().ID(), true)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, &RepositoryNotFoundError{Kind: "artifact", Identity: artifactIdentity(scopeID, current.Ref())}
	}
	if lockedRevision != current.Ref().Revision() {
		latestRef, refErr := artifact.NewRef(current.Ref().Family(), current.Ref().ID(), lockedRevision)
		if refErr != nil {
			return nil, refErr
		}
		return nil, &artifact.RevisionConflictError{Requested: current.Ref(), Current: latestRef}
	}
	nextRef, err := artifact.NewRef(
		current.Ref().Family(), current.Ref().ID(), current.Ref().Revision()+1,
	)
	if err != nil {
		return nil, err
	}
	revised, err := r.insertRevision(ctx, db, scopeID, codec, nextRef, draft.ContentValue(), draft.Lineage())
	if err != nil {
		return nil, err
	}
	result, err := db.ExecContext(ctx, `UPDATE pc_artifact_heads SET revision = ?
        WHERE scope_id = ? AND family = ? AND artifact_id = ? AND revision = ?`,
		nextRef.Revision(), scopeID, nextRef.Family(), nextRef.ID(), current.Ref().Revision())
	if err != nil {
		return nil, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if affected != 1 {
		return nil, &artifact.RevisionConflictError{Requested: current.Ref(), Current: nextRef}
	}
	return revised, nil
}

func (r *ArtifactRepository) Get(
	ctx context.Context,
	db DBTX,
	scopeID string,
	ref artifact.Ref,
) (artifact.Snapshot, error) {
	if err := requireScope(scopeID); err != nil {
		return nil, err
	}
	if err := ref.Validate(); err != nil {
		return nil, err
	}
	row, found, err := findArtifactRow(ctx, db, scopeID, ref)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, &RepositoryNotFoundError{Kind: "artifact", Identity: artifactIdentity(scopeID, ref)}
	}
	return r.decode(ctx, db, row)
}

func (r *ArtifactRepository) Latest(
	ctx context.Context,
	db DBTX,
	scopeID, family, artifactID string,
) (artifact.Snapshot, error) {
	if err := requireScope(scopeID); err != nil {
		return nil, err
	}
	if _, err := artifact.NewRef(family, artifactID, 1); err != nil {
		return nil, err
	}
	revision, found, err := r.findHead(ctx, db, scopeID, family, artifactID, false)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, &RepositoryNotFoundError{Kind: "artifact", Identity: scopeID + "/" + family + ":" + artifactID}
	}
	ref, err := artifact.NewRef(family, artifactID, revision)
	if err != nil {
		return nil, err
	}
	return r.Get(ctx, db, scopeID, ref)
}

func (r *ArtifactRepository) Revisions(
	ctx context.Context,
	db DBTX,
	scopeID, family, artifactID string,
) ([]artifact.Snapshot, error) {
	if err := requireScope(scopeID); err != nil {
		return nil, err
	}
	if _, err := artifact.NewRef(family, artifactID, 1); err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT scope_id, family, artifact_id, revision, content
        FROM pc_artifacts WHERE scope_id = ? AND family = ? AND artifact_id = ? ORDER BY revision`,
		scopeID, family, artifactID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]artifact.Snapshot, 0)
	for rows.Next() {
		row, err := scanArtifact(rows)
		if err != nil {
			return nil, err
		}
		decoded, err := r.decode(ctx, db, row)
		if err != nil {
			return nil, err
		}
		result = append(result, decoded)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func (r *ArtifactRepository) codecForDraft(draft artifact.DraftSnapshot) (ArtifactCodec, error) {
	codec, ok := r.byFamily[draft.Family()]
	if !ok {
		return ArtifactCodec{}, &RepositoryNotFoundError{Kind: "artifact-family", Identity: draft.Family()}
	}
	if reflect.TypeOf(draft.ContentValue()) != codec.contentType {
		return ArtifactCodec{}, &artifact.FamilyMismatchError{
			ArtifactFamily: codec.family,
			DraftFamily:    draft.Family(),
		}
	}
	return codec, nil
}

func (r *ArtifactRepository) insertRevision(
	ctx context.Context,
	db DBTX,
	scopeID string,
	codec ArtifactCodec,
	ref artifact.Ref,
	content any,
	lineage artifact.Lineage,
) (artifact.Snapshot, error) {
	payload, err := codec.encode(content)
	if err != nil {
		return nil, &InvalidStoredPayloadError{Kind: "artifact", Name: codec.family, Issue: "value is not JSON serializable"}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO pc_artifacts
        (scope_id, family, artifact_id, revision, content) VALUES (?, ?, ?, ?, ?)`,
		scopeID, ref.Family(), ref.ID(), ref.Revision(), payload); err != nil {
		return nil, err
	}
	for ordinal, sourceRef := range lineage.Sources() {
		if _, err := db.ExecContext(ctx, `INSERT INTO pc_artifact_lineage_sources
            (scope_id, family, artifact_id, revision, ordinal, source_type, source_id)
            VALUES (?, ?, ?, ?, ?, ?, ?)`, scopeID, ref.Family(), ref.ID(), ref.Revision(),
			ordinal, sourceRef.Type(), sourceRef.ID()); err != nil {
			return nil, err
		}
	}
	for ordinal, upstream := range lineage.Artifacts() {
		if _, err := db.ExecContext(ctx, `INSERT INTO pc_artifact_lineage_artifacts
            (scope_id, family, artifact_id, revision, ordinal,
             upstream_family, upstream_artifact_id, upstream_revision)
            VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, scopeID, ref.Family(), ref.ID(), ref.Revision(),
			ordinal, upstream.Family(), upstream.ID(), upstream.Revision()); err != nil {
			return nil, err
		}
	}
	created, err := codec.decode(ref, lineage, payload)
	if err != nil {
		return nil, &InvalidStoredPayloadError{Kind: "artifact", Name: codec.family, Issue: "payload does not match the model"}
	}
	if created.Ref() != ref {
		return nil, &IdentityMismatchError{Kind: "artifact", Indexed: ref, Decoded: created.Ref()}
	}
	return created, nil
}

type storedArtifactRow struct {
	scopeID    string
	family     string
	artifactID string
	revision   int64
	content    []byte
}

func findArtifactRow(
	ctx context.Context,
	db DBTX,
	scopeID string,
	ref artifact.Ref,
) (storedArtifactRow, bool, error) {
	row, err := scanArtifact(db.QueryRowContext(ctx, `SELECT scope_id, family, artifact_id, revision, content
        FROM pc_artifacts WHERE scope_id = ? AND family = ? AND artifact_id = ? AND revision = ?`,
		scopeID, ref.Family(), ref.ID(), ref.Revision()))
	if errors.Is(err, sql.ErrNoRows) {
		return storedArtifactRow{}, false, nil
	}
	return row, err == nil, err
}

func scanArtifact(value scanner) (storedArtifactRow, error) {
	var row storedArtifactRow
	var revision, content any
	if err := value.Scan(&row.scopeID, &row.family, &row.artifactID, &revision, &content); err != nil {
		return storedArtifactRow{}, err
	}
	decodedRevision, ok := integer(revision)
	if !ok {
		return storedArtifactRow{}, &InvalidStoredColumnError{Column: "revision", Expected: "an integer"}
	}
	decodedContent, err := storedBytes(content, "content")
	if err != nil {
		return storedArtifactRow{}, err
	}
	row.revision = decodedRevision
	row.content = decodedContent
	return row, nil
}

func (r *ArtifactRepository) decode(
	ctx context.Context,
	db DBTX,
	row storedArtifactRow,
) (artifact.Snapshot, error) {
	codec, ok := r.byFamily[row.family]
	if !ok {
		return nil, &RepositoryNotFoundError{Kind: "artifact-family", Identity: row.family}
	}
	ref, err := artifact.NewRef(row.family, row.artifactID, row.revision)
	if err != nil {
		return nil, err
	}
	lineage, err := loadLineage(ctx, db, row.scopeID, ref)
	if err != nil {
		return nil, err
	}
	decoded, err := codec.decode(ref, lineage, row.content)
	if err != nil {
		return nil, &InvalidStoredPayloadError{Kind: "artifact", Name: row.family, Issue: "payload does not match the model"}
	}
	if decoded.Ref() != ref {
		return nil, &IdentityMismatchError{Kind: "artifact", Indexed: ref, Decoded: decoded.Ref()}
	}
	return decoded, nil
}

func loadLineage(ctx context.Context, db DBTX, scopeID string, ref artifact.Ref) (artifact.Lineage, error) {
	sourceRows, err := db.QueryContext(ctx, `SELECT source_type, source_id
        FROM pc_artifact_lineage_sources
        WHERE scope_id = ? AND family = ? AND artifact_id = ? AND revision = ? ORDER BY ordinal`,
		scopeID, ref.Family(), ref.ID(), ref.Revision())
	if err != nil {
		return artifact.Lineage{}, err
	}
	sources := make([]source.Ref, 0)
	for sourceRows.Next() {
		var sourceType, sourceID string
		if err := sourceRows.Scan(&sourceType, &sourceID); err != nil {
			sourceRows.Close()
			return artifact.Lineage{}, err
		}
		value, err := source.NewRef(sourceType, sourceID)
		if err != nil {
			sourceRows.Close()
			return artifact.Lineage{}, err
		}
		sources = append(sources, value)
	}
	if err := sourceRows.Close(); err != nil {
		return artifact.Lineage{}, err
	}
	if err := sourceRows.Err(); err != nil {
		return artifact.Lineage{}, err
	}

	artifactRows, err := db.QueryContext(ctx, `SELECT upstream_family, upstream_artifact_id, upstream_revision
        FROM pc_artifact_lineage_artifacts
        WHERE scope_id = ? AND family = ? AND artifact_id = ? AND revision = ? ORDER BY ordinal`,
		scopeID, ref.Family(), ref.ID(), ref.Revision())
	if err != nil {
		return artifact.Lineage{}, err
	}
	artifacts := make([]artifact.Ref, 0)
	for artifactRows.Next() {
		var family, artifactID string
		var revision any
		if err := artifactRows.Scan(&family, &artifactID, &revision); err != nil {
			artifactRows.Close()
			return artifact.Lineage{}, err
		}
		decodedRevision, ok := integer(revision)
		if !ok {
			artifactRows.Close()
			return artifact.Lineage{}, &InvalidStoredColumnError{Column: "upstream_revision", Expected: "an integer"}
		}
		value, err := artifact.NewRef(family, artifactID, decodedRevision)
		if err != nil {
			artifactRows.Close()
			return artifact.Lineage{}, err
		}
		artifacts = append(artifacts, value)
	}
	if err := artifactRows.Close(); err != nil {
		return artifact.Lineage{}, err
	}
	if err := artifactRows.Err(); err != nil {
		return artifact.Lineage{}, err
	}
	return artifact.NewLineage(sources, artifacts)
}

func (r *ArtifactRepository) findHead(
	ctx context.Context,
	db DBTX,
	scopeID, family, artifactID string,
	locked bool,
) (int64, bool, error) {
	query := `SELECT revision FROM pc_artifact_heads
        WHERE scope_id = ? AND family = ? AND artifact_id = ?`
	if locked && r.dialect == MySQLDialect {
		query += " FOR UPDATE"
	}
	var value any
	err := db.QueryRowContext(ctx, query, scopeID, family, artifactID).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	revision, ok := integer(value)
	if !ok {
		return 0, false, &InvalidStoredColumnError{Column: "revision", Expected: "an integer"}
	}
	return revision, true, nil
}

func artifactIdentity(scopeID string, ref artifact.Ref) string {
	return scopeID + "/" + ref.String()
}
