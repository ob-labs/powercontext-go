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
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"math/big"
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
	if observation, ok := value.(source.SourceObservation); ok {
		if err := observation.Validate(); err != nil {
			return source.Ref{}, err
		}
		return observation.Ref(), nil
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

// HasNativeDefinition reports whether name is owned by a concrete Source codec.
// Remote Definition manifests must not replace these local runtime contracts.
func (r *SourceRepository) HasNativeDefinition(name string) bool {
	_, exists := r.byName[name]
	return exists
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
	ref, payload, observation, err := r.encode(value)
	if err != nil {
		return StoredSource{}, err
	}
	if lockErr := r.lockJournalHead(ctx, db, scopeID); lockErr != nil {
		return StoredSource{}, lockErr
	}
	existing, found, err := r.find(ctx, db, scopeID, ref)
	if err != nil {
		return StoredSource{}, err
	}
	if found {
		equal, equalErr := sameStoredSourcePayload(existing.payload, payload, observation)
		if equalErr != nil {
			return StoredSource{}, invalidStoredSourceObservation()
		}
		if !equal {
			if observation {
				return StoredSource{}, sourceObservationConflict()
			}
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
		equal, equalErr := sameStoredSourcePayload(existing.payload, payload, observation)
		if equalErr != nil {
			return StoredSource{}, invalidStoredSourceObservation()
		}
		if !equal {
			if observation {
				return StoredSource{}, sourceObservationConflict()
			}
			return StoredSource{}, &StoredPayloadConflictError{Kind: "source", Identity: sourceIdentity(scopeID, ref)}
		}
		return r.decode(existing)
	}
	return StoredSource{Ref: ref, Value: value, JournalPosition: position}, nil
}

func (r *SourceRepository) encode(value source.Value) (source.Ref, []byte, bool, error) {
	if observation, ok := value.(source.SourceObservation); ok {
		if err := observation.Validate(); err != nil {
			return source.Ref{}, nil, true, err
		}
		payload, err := encodeSourceObservation(observation)
		if err != nil {
			return source.Ref{}, nil, true, err
		}
		return observation.Ref(), payload, true, nil
	}
	codec, ok := r.bySource[reflect.TypeOf(value)]
	if !ok {
		return source.Ref{}, nil, false, &RepositoryNotFoundError{Kind: "source-adapter", Identity: reflect.TypeOf(value)}
	}
	ref, err := r.Ref(value)
	if err != nil {
		return source.Ref{}, nil, false, err
	}
	payload, err := codec.encode(value)
	if err != nil {
		return source.Ref{}, nil, false, &InvalidStoredPayloadError{Kind: "source", Name: codec.name, Issue: "value is not JSON serializable"}
	}
	return ref, payload, false, nil
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
	envelope, recognized, envelopeErr := parseSourceEnvelope(row.payload)
	if recognized {
		if envelopeErr != nil {
			return StoredSource{}, invalidStoredSourceObservation()
		}
		switch envelope.representation {
		case sourceObservationRepresentation:
			indexed, err := source.NewRef(row.typeName, row.sourceID)
			if err != nil {
				return StoredSource{}, err
			}
			if indexed != envelope.observation.Ref() {
				return StoredSource{}, sourceObservationIdentityMismatch()
			}
			return StoredSource{Ref: indexed, Value: envelope.observation, JournalPosition: row.position}, nil
		case sourceNativeRepresentation:
			return r.decodeNative(row, envelope.value)
		}
	}
	return r.decodeNative(row, row.payload)
}

func (r *SourceRepository) decodeNative(row storedSourceRow, payload []byte) (StoredSource, error) {
	codec, ok := r.byName[row.typeName]
	if !ok {
		return StoredSource{}, &RepositoryNotFoundError{Kind: "source-adapter", Identity: row.typeName}
	}
	value, err := codec.decode(payload)
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

func sameStoredSourcePayload(stored, expected []byte, observation bool) (bool, error) {
	if !observation {
		return bytes.Equal(stored, expected), nil
	}
	envelope, recognized, err := parseSourceEnvelope(stored)
	if err != nil {
		return false, err
	}
	if !recognized || envelope.representation != sourceObservationRepresentation {
		return false, nil
	}
	return equalJSONValues(jsontext.Value(stored), jsontext.Value(expected))
}

func equalJSONValues(left, right jsontext.Value) (bool, error) {
	if !left.IsValid() || !right.IsValid() {
		return false, fmt.Errorf("invalid JSON value")
	}
	leftKind, rightKind := left.Kind(), right.Kind()
	if leftKind != rightKind {
		return false, nil
	}
	switch leftKind {
	case '{':
		return equalJSONObjects(left, right)
	case '[':
		return equalJSONArrays(left, right)
	case '"':
		return equalJSONStrings(left, right)
	case '0':
		return equalJSONNumbers(left, right)
	default:
		return bytes.Equal(bytes.TrimSpace(left), bytes.TrimSpace(right)), nil
	}
}

func equalJSONObjects(left, right jsontext.Value) (bool, error) {
	leftMembers, err := jsonObjectMembers(left)
	if err != nil {
		return false, err
	}
	rightMembers, err := jsonObjectMembers(right)
	if err != nil {
		return false, err
	}
	if len(leftMembers) != len(rightMembers) {
		return false, nil
	}
	for name, leftValue := range leftMembers {
		rightValue, ok := rightMembers[name]
		if !ok {
			return false, nil
		}
		equal, err := equalJSONValues(leftValue, rightValue)
		if err != nil || !equal {
			return equal, err
		}
	}
	return true, nil
}

func jsonObjectMembers(value jsontext.Value) (map[string]jsontext.Value, error) {
	decoder := jsontext.NewDecoder(bytes.NewReader(value))
	start, err := decoder.ReadToken()
	if err != nil || start.Kind() != '{' {
		return nil, fmt.Errorf("invalid JSON object")
	}
	members := make(map[string]jsontext.Value)
	for decoder.PeekKind() != '}' {
		name, err := decoder.ReadToken()
		if err != nil || name.Kind() != '"' {
			return nil, fmt.Errorf("invalid JSON object member")
		}
		memberName := name.Clone().String()
		member, err := decoder.ReadValue()
		if err != nil {
			return nil, err
		}
		members[memberName] = member.Clone()
	}
	if _, err := decoder.ReadToken(); err != nil {
		return nil, err
	}
	return members, nil
}

func equalJSONArrays(left, right jsontext.Value) (bool, error) {
	leftValues, err := jsonArrayValues(left)
	if err != nil {
		return false, err
	}
	rightValues, err := jsonArrayValues(right)
	if err != nil {
		return false, err
	}
	if len(leftValues) != len(rightValues) {
		return false, nil
	}
	for index := range leftValues {
		equal, err := equalJSONValues(leftValues[index], rightValues[index])
		if err != nil || !equal {
			return equal, err
		}
	}
	return true, nil
}

func jsonArrayValues(value jsontext.Value) ([]jsontext.Value, error) {
	decoder := jsontext.NewDecoder(bytes.NewReader(value))
	start, err := decoder.ReadToken()
	if err != nil || start.Kind() != '[' {
		return nil, fmt.Errorf("invalid JSON array")
	}
	values := make([]jsontext.Value, 0)
	for decoder.PeekKind() != ']' {
		entry, err := decoder.ReadValue()
		if err != nil {
			return nil, err
		}
		values = append(values, entry.Clone())
	}
	if _, err := decoder.ReadToken(); err != nil {
		return nil, err
	}
	return values, nil
}

func equalJSONStrings(left, right jsontext.Value) (bool, error) {
	var leftString, rightString string
	if err := unmarshalJSON(left, &leftString); err != nil {
		return false, err
	}
	if err := unmarshalJSON(right, &rightString); err != nil {
		return false, err
	}
	return leftString == rightString, nil
}

func equalJSONNumbers(left, right jsontext.Value) (bool, error) {
	leftNumber, err := parseJSONNumber(left)
	if err != nil {
		return false, err
	}
	rightNumber, err := parseJSONNumber(right)
	if err != nil {
		return false, err
	}
	if leftNumber.zero || rightNumber.zero {
		return leftNumber.zero == rightNumber.zero, nil
	}
	return leftNumber.negative == rightNumber.negative &&
		leftNumber.significand == rightNumber.significand &&
		leftNumber.exponent.Cmp(rightNumber.exponent) == 0, nil
}

type jsonNumber struct {
	negative    bool
	zero        bool
	significand string
	exponent    *big.Int
}

func parseJSONNumber(value jsontext.Value) (jsonNumber, error) {
	raw := bytes.TrimSpace(value)
	negative := false
	if raw[0] == '-' {
		negative = true
		raw = raw[1:]
	}
	exponentIndex := bytes.IndexAny(raw, "eE")
	mantissa, exponentText := raw, []byte(nil)
	if exponentIndex >= 0 {
		mantissa, exponentText = raw[:exponentIndex], raw[exponentIndex+1:]
	}
	fractionDigits := 0
	if decimalIndex := bytes.IndexByte(mantissa, '.'); decimalIndex >= 0 {
		fractionDigits = len(mantissa) - decimalIndex - 1
		mantissa = append(bytes.Clone(mantissa[:decimalIndex]), mantissa[decimalIndex+1:]...)
	}
	mantissa = bytes.TrimLeft(mantissa, "0")
	if len(mantissa) == 0 {
		return jsonNumber{zero: true}, nil
	}
	trailingZeros := len(mantissa) - len(bytes.TrimRight(mantissa, "0"))
	mantissa = mantissa[:len(mantissa)-trailingZeros]
	exponent := new(big.Int).Neg(big.NewInt(int64(fractionDigits)))
	if len(exponentText) > 0 {
		if exponentText[0] == '+' {
			exponentText = exponentText[1:]
		}
		parsedExponent, ok := new(big.Int).SetString(string(exponentText), 10)
		if !ok {
			return jsonNumber{}, fmt.Errorf("invalid JSON number")
		}
		exponent.Add(exponent, parsedExponent)
	}
	exponent.Add(exponent, big.NewInt(int64(trailingZeros)))
	return jsonNumber{
		negative:    negative,
		significand: string(mantissa),
		exponent:    exponent,
	}, nil
}

func invalidStoredSourceObservation() error {
	return &InvalidStoredPayloadError{
		Kind:  sourceObservationPayloadKind,
		Name:  redactedSourceObservationIdentity,
		Issue: "payload does not match the observation envelope",
	}
}

func sourceObservationIdentityMismatch() error {
	return &IdentityMismatchError{
		Kind:    sourceObservationPayloadKind,
		Indexed: redactedSourceObservationIdentity,
		Decoded: redactedSourceObservationIdentity,
	}
}

func sourceObservationConflict() error {
	return &StoredPayloadConflictError{
		Kind:     sourceObservationPayloadKind,
		Identity: redactedSourceObservationIdentity,
	}
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
