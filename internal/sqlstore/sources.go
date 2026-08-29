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
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/ob-labs/powercontext-go/source"
)

// Dialect selects the small number of locking statements that differ between
// SQLite and OceanBase/MySQL.
type Dialect string

const (
	SQLiteDialect Dialect = "sqlite"
	MySQLDialect  Dialect = "mysql"
)

// StoredSource is a decoded Source and its per-scope journal position.
type StoredSource struct {
	Ref             source.Ref
	Value           source.Value
	JournalPosition int64
}

// SourceRepository persists stable Sources through exact concrete codecs.
type SourceRepository struct {
	dialect  Dialect
	byName   map[string]SourceCodec
	bySource map[reflect.Type]SourceCodec
}

// Ref maps one exact concrete Source value to its stable persisted identity.
// Routing is deliberately exact, matching source.Catalog and the frozen
// Python adapter registry; assignable/interface-based fallbacks are rejected.
func (r *SourceRepository) Ref(value source.Value) (source.Ref, error) {
	if value == nil {
		return source.Ref{}, &source.InvalidEntryError{}
	}
	codec, ok := r.bySource[reflect.TypeOf(value)]
	if !ok {
		return source.Ref{}, &source.AdapterNotFoundError{Route: "source", Type: reflect.TypeOf(value)}
	}
	return source.NewRef(codec.name, value.SourceName())
}

func NewSourceRepository(dialect Dialect, codecs ...SourceCodec) (*SourceRepository, error) {
	if dialect != SQLiteDialect && dialect != MySQLDialect {
		return nil, fmt.Errorf("sqlstore: unsupported dialect %q", dialect)
	}
	repository := &SourceRepository{
		dialect:  dialect,
		byName:   make(map[string]SourceCodec, len(codecs)),
		bySource: make(map[reflect.Type]SourceCodec, len(codecs)),
	}
	for _, codec := range codecs {
		if _, exists := repository.byName[codec.name]; exists {
			return nil, &CodecConflictError{Route: "source name", Value: codec.name}
		}
		if _, exists := repository.bySource[codec.valueType]; exists {
			return nil, &CodecConflictError{Route: "source type", Value: codec.valueType}
		}
		repository.byName[codec.name] = codec
		repository.bySource[codec.valueType] = codec
	}
	return repository, nil
}

func (r *SourceRepository) Add(
	ctx context.Context,
	db DBTX,
	scopeID string,
	value source.Value,
) (StoredSource, error) {
	if err := requireScope(scopeID); err != nil {
		return StoredSource{}, err
	}
	codec, ok := r.bySource[reflect.TypeOf(value)]
	if !ok {
		return StoredSource{}, &RepositoryNotFoundError{Kind: "source-adapter", Identity: reflect.TypeOf(value)}
	}
	ref, err := r.Ref(value)
	if err != nil {
		return StoredSource{}, err
	}
	payload, err := codec.encode(value)
	if err != nil {
		return StoredSource{}, &InvalidStoredPayloadError{Kind: "source", Name: codec.name, Issue: "value is not JSON serializable"}
	}
	if err := r.lockJournalHead(ctx, db, scopeID); err != nil {
		return StoredSource{}, err
	}
	existing, found, err := r.find(ctx, db, scopeID, ref)
	if err != nil {
		return StoredSource{}, err
	}
	if found {
		if !bytes.Equal(existing.payload, payload) {
			return StoredSource{}, &StoredPayloadConflictError{Kind: "source", Identity: sourceIdentity(scopeID, ref)}
		}
		return r.decode(existing)
	}

	position, err := nextJournalPosition(ctx, db, scopeID)
	if err != nil {
		return StoredSource{}, err
	}
	_, err = db.ExecContext(ctx, `INSERT INTO pc_sources
        (scope_id, source_type, source_id, payload, journal_position)
        VALUES (?, ?, ?, ?, ?)`, scopeID, ref.Type(), ref.ID(), payload, position)
	if err != nil {
		existing, found, findErr := r.find(ctx, db, scopeID, ref)
		if findErr != nil {
			return StoredSource{}, errors.Join(err, findErr)
		}
		if !found {
			return StoredSource{}, err
		}
		if !bytes.Equal(existing.payload, payload) {
			return StoredSource{}, &StoredPayloadConflictError{Kind: "source", Identity: sourceIdentity(scopeID, ref)}
		}
		return r.decode(existing)
	}
	return StoredSource{Ref: ref, Value: value, JournalPosition: position}, nil
}

func (r *SourceRepository) Get(
	ctx context.Context,
	db DBTX,
	scopeID string,
	ref source.Ref,
) (StoredSource, error) {
	if err := requireScope(scopeID); err != nil {
		return StoredSource{}, err
	}
	if _, err := source.NewRef(ref.Type(), ref.ID()); err != nil {
		return StoredSource{}, err
	}
	row, found, err := r.find(ctx, db, scopeID, ref)
	if err != nil {
		return StoredSource{}, err
	}
	if !found {
		return StoredSource{}, &RepositoryNotFoundError{Kind: "source", Identity: sourceIdentity(scopeID, ref)}
	}
	return r.decode(row)
}

func (r *SourceRepository) List(
	ctx context.Context,
	db DBTX,
	scopeID string,
	after int64,
	limit *int,
) (result []StoredSource, returnErr error) {
	if err := requireScope(scopeID); err != nil {
		return nil, err
	}
	if after < 0 {
		return nil, &InvalidRepositoryArgumentError{Field: "after", Detail: "must be non-negative"}
	}
	if limit != nil && *limit < 1 {
		return nil, &InvalidRepositoryArgumentError{Field: "limit", Detail: "must be positive"}
	}
	query := `SELECT scope_id, source_type, source_id, payload, journal_position
        FROM pc_sources WHERE scope_id = ? AND journal_position > ? ORDER BY journal_position`
	arguments := []any{scopeID, after}
	if limit != nil {
		query += " LIMIT ?"
		arguments = append(arguments, *limit)
	}
	rows, err := db.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, rows.Close()) }()
	result = make([]StoredSource, 0)
	for rows.Next() {
		row, err := scanSource(rows)
		if err != nil {
			return nil, err
		}
		decoded, err := r.decode(row)
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

func (r *SourceRepository) JournalPosition(
	ctx context.Context,
	db DBTX,
	scopeID string,
) (int64, error) {
	if err := requireScope(scopeID); err != nil {
		return 0, err
	}
	var value any
	if err := db.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(journal_position), 0) FROM pc_sources WHERE scope_id = ?", scopeID,
	).Scan(&value); err != nil {
		return 0, err
	}
	position, ok := integer(value)
	if !ok {
		return 0, &InvalidStoredColumnError{Column: "journal_position", Expected: "an integer"}
	}
	return position, nil
}

type storedSourceRow struct {
	scopeID  string
	typeName string
	sourceID string
	payload  []byte
	position int64
}

func (r *SourceRepository) find(
	ctx context.Context,
	db DBTX,
	scopeID string,
	ref source.Ref,
) (storedSourceRow, bool, error) {
	row, err := scanSource(db.QueryRowContext(ctx, `SELECT scope_id, source_type, source_id, payload, journal_position
        FROM pc_sources WHERE scope_id = ? AND source_type = ? AND source_id = ?`,
		scopeID, ref.Type(), ref.ID()))
	if errors.Is(err, sql.ErrNoRows) {
		return storedSourceRow{}, false, nil
	}
	return row, err == nil, err
}

type scanner interface{ Scan(...any) error }

func scanSource(value scanner) (storedSourceRow, error) {
	var row storedSourceRow
	var payload any
	var position any
	if err := value.Scan(&row.scopeID, &row.typeName, &row.sourceID, &payload, &position); err != nil {
		return storedSourceRow{}, err
	}
	decodedPayload, err := storedBytes(payload, "payload")
	if err != nil {
		return storedSourceRow{}, err
	}
	decodedPosition, ok := integer(position)
	if !ok {
		return storedSourceRow{}, &InvalidStoredColumnError{Column: "journal_position", Expected: "an integer"}
	}
	row.payload = decodedPayload
	row.position = decodedPosition
	return row, nil
}

func (r *SourceRepository) decode(row storedSourceRow) (StoredSource, error) {
	codec, ok := r.byName[row.typeName]
	if !ok {
		return StoredSource{}, &RepositoryNotFoundError{Kind: "source-adapter", Identity: row.typeName}
	}
	value, err := codec.decode(row.payload)
	if err != nil {
		return StoredSource{}, &InvalidStoredPayloadError{Kind: "source", Name: row.typeName, Issue: "payload does not match the model"}
	}
	indexed, err := source.NewRef(row.typeName, row.sourceID)
	if err != nil {
		return StoredSource{}, err
	}
	decoded, err := source.NewRef(codec.name, value.SourceName())
	if err != nil {
		return StoredSource{}, err
	}
	if indexed != decoded {
		return StoredSource{}, &IdentityMismatchError{Kind: "source", Indexed: indexed, Decoded: decoded}
	}
	return StoredSource{Ref: indexed, Value: value, JournalPosition: row.position}, nil
}

func (r *SourceRepository) lockJournalHead(ctx context.Context, db DBTX, scopeID string) error {
	if _, err := db.ExecContext(ctx,
		"UPDATE pc_source_journal_heads SET position = position WHERE scope_id = ?", scopeID,
	); err != nil {
		return err
	}
	query := "SELECT position FROM pc_source_journal_heads WHERE scope_id = ?"
	if r.dialect == MySQLDialect {
		query += " FOR UPDATE"
	}
	var value any
	err := db.QueryRowContext(ctx, query, scopeID).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		if _, insertErr := db.ExecContext(ctx,
			"INSERT INTO pc_source_journal_heads (scope_id, position) VALUES (?, 0)", scopeID,
		); insertErr != nil {
			// A competing transaction can install the same allocator. The
			// following locked read decides whether recovery is valid.
			if readErr := db.QueryRowContext(ctx, query, scopeID).Scan(&value); readErr != nil {
				return errors.Join(insertErr, readErr)
			}
		} else if readErr := db.QueryRowContext(ctx, query, scopeID).Scan(&value); readErr != nil {
			return readErr
		}
	} else if err != nil {
		return err
	}
	position, ok := integer(value)
	if !ok || position < 0 {
		return &InvalidStoredColumnError{Column: "journal_position", Expected: "a non-negative scope head"}
	}
	return nil
}

func nextJournalPosition(ctx context.Context, db DBTX, scopeID string) (int64, error) {
	result, err := db.ExecContext(ctx,
		"UPDATE pc_source_journal_heads SET position = position + 1 WHERE scope_id = ?", scopeID,
	)
	if err != nil {
		return 0, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if affected != 1 {
		return 0, &InvalidStoredColumnError{Column: "journal_position", Expected: "an initialized scope head"}
	}
	var value any
	if err := db.QueryRowContext(ctx,
		"SELECT position FROM pc_source_journal_heads WHERE scope_id = ?", scopeID,
	).Scan(&value); err != nil {
		return 0, err
	}
	position, ok := integer(value)
	if !ok || position < 1 {
		return 0, &InvalidStoredColumnError{Column: "journal_position", Expected: "a positive integer"}
	}
	return position, nil
}

func integer(value any) (int64, bool) {
	switch typed := value.(type) {
	case int64:
		return typed, true
	case int32:
		return int64(typed), true
	case int:
		return int64(typed), true
	default:
		return 0, false
	}
}

func requireScope(scopeID string) error {
	if !utf8.ValidString(scopeID) || strings.TrimSpace(scopeID) == "" || strings.TrimSpace(scopeID) != scopeID {
		return &InvalidRepositoryArgumentError{Field: "scope_id", Detail: "must be a non-empty trimmed string"}
	}
	if utf8.RuneCountInString(scopeID) > 256 {
		return &InvalidRepositoryArgumentError{Field: "scope_id", Detail: "must not exceed 256 characters"}
	}
	return nil
}

func sourceIdentity(scopeID string, ref source.Ref) string {
	return scopeID + "/" + ref.String()
}
