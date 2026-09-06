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
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"

	"github.com/ob-labs/powercontext-go/internal/scope"
)

// ScopeRepository persists Scope metadata and durable external bindings.
type ScopeRepository struct{}

// LockHierarchy obtains SQLite's write lock before any hierarchy snapshot is
// read, including an empty hierarchy. The caller must own the transaction.
func (ScopeRepository) LockHierarchy(ctx context.Context, db DBTX) error {
	_, err := db.ExecContext(ctx, `UPDATE pc_scopes SET version = version WHERE 1 = 0`)
	return err
}

func (ScopeRepository) Create(ctx context.Context, db DBTX, id string, draft scope.Draft) (scope.Descriptor, error) {
	if _, err := scope.NewDescriptor(id, draft.Title(), draft.Summary(), draft.ParentScopeID(), draft.ContextReferences(), draft.ExternalReferences(), 1); err != nil {
		return scope.Descriptor{}, err
	}
	if _, err := scope.NewDraft(draft.Title(), draft.Summary(), draft.ParentScopeID(), draft.ContextReferences(), draft.ExternalReferences(), draft.IdempotencyKey()); err != nil {
		return scope.Descriptor{}, &scope.ValidationError{}
	}
	digest, err := draftDigest(draft)
	if err != nil {
		return scope.Descriptor{}, err
	}
	if existing, found, findErr := findScopeCreation(ctx, db, draft.IdempotencyKey(), digest); findErr != nil || found {
		return existing, findErr
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO pc_scopes (scope_id, title, summary, parent_scope_id, version)
        VALUES (?, ?, ?, NULLIF(?, ''), 1)`, id, draft.Title(), draft.Summary(), draft.ParentScopeID()); err != nil {
		return scope.Descriptor{}, err
	}
	if err := writeScopeRelationships(ctx, db, id, draft.ContextReferences(), draft.ExternalReferences()); err != nil {
		return scope.Descriptor{}, err
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO pc_scope_creation_requests
        (idempotency_key, request_digest, scope_id) VALUES (?, ?, ?)`, draft.IdempotencyKey(), digest, id); err != nil {
		return scope.Descriptor{}, err
	}
	return ScopeRepository{}.Get(ctx, db, id)
}

func findScopeCreation(ctx context.Context, db DBTX, key, digest string) (scope.Descriptor, bool, error) {
	var existingDigest, existingID string
	err := db.QueryRowContext(ctx, `SELECT request_digest, scope_id FROM pc_scope_creation_requests
        WHERE idempotency_key = ?`, key).Scan(&existingDigest, &existingID)
	if errors.Is(err, sql.ErrNoRows) {
		return scope.Descriptor{}, false, nil
	}
	if err != nil {
		return scope.Descriptor{}, false, err
	}
	if existingDigest != digest {
		return scope.Descriptor{}, false, &scope.IdempotencyConflictError{}
	}
	value, getErr := ScopeRepository{}.Get(ctx, db, existingID)
	return value, true, getErr
}

func (ScopeRepository) Update(ctx context.Context, db DBTX, id string, mutation scope.Mutation) (scope.Descriptor, error) {
	result, err := db.ExecContext(ctx, `UPDATE pc_scopes
        SET title = ?, summary = ?, parent_scope_id = NULLIF(?, ''), version = ?
        WHERE scope_id = ? AND version = ?`,
		mutation.Title(), mutation.Summary(), mutation.ParentScopeID(), mutation.ExpectedVersion()+1, id, mutation.ExpectedVersion(),
	)
	if err != nil {
		return scope.Descriptor{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return scope.Descriptor{}, err
	}
	if affected != 1 {
		current, getErr := ScopeRepository{}.Get(ctx, db, id)
		if getErr != nil {
			return scope.Descriptor{}, getErr
		}
		return scope.Descriptor{}, &scope.VersionConflictError{Expected: mutation.ExpectedVersion(), Actual: current.Version()}
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM pc_scope_context_references WHERE scope_id = ?`, id); err != nil {
		return scope.Descriptor{}, err
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM pc_scope_external_references WHERE scope_id = ?`, id); err != nil {
		return scope.Descriptor{}, err
	}
	if err := writeScopeRelationships(ctx, db, id, mutation.ContextReferences(), mutation.ExternalReferences()); err != nil {
		return scope.Descriptor{}, err
	}
	return ScopeRepository{}.Get(ctx, db, id)
}

func writeScopeRelationships(ctx context.Context, db DBTX, id string, contextReferences []string, externalReferences []scope.ExternalReference) error {
	for _, referenceID := range contextReferences {
		if _, err := db.ExecContext(ctx, `INSERT INTO pc_scope_context_references
            (scope_id, referenced_scope_id) VALUES (?, ?)`, id, referenceID); err != nil {
			return err
		}
	}
	for ordinal, reference := range externalReferences {
		digest := sha256.Sum256([]byte(reference.Value()))
		if _, err := db.ExecContext(ctx, `INSERT INTO pc_scope_external_references
            (scope_id, ordinal, kind, value, value_digest) VALUES (?, ?, ?, ?, ?)`,
			id, ordinal, reference.Kind(), reference.Value(), hex.EncodeToString(digest[:]),
		); err != nil {
			return err
		}
	}
	return nil
}

func (ScopeRepository) Get(ctx context.Context, db DBTX, id string) (scope.Descriptor, error) {
	row := db.QueryRowContext(ctx, `SELECT scope_id, title, summary, parent_scope_id, version
        FROM pc_scopes WHERE scope_id = ?`, id)
	return loadScope(ctx, db, row)
}

func (ScopeRepository) List(ctx context.Context, db DBTX) (result []scope.Descriptor, returnErr error) {
	rows, err := db.QueryContext(ctx, `SELECT scope_id, title, summary, parent_scope_id, version
        FROM pc_scopes ORDER BY scope_id`)
	if err != nil {
		return nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, rows.Close()) }()
	for rows.Next() {
		descriptor, err := loadScope(ctx, db, rows)
		if err != nil {
			return nil, err
		}
		result = append(result, descriptor)
	}
	return result, rows.Err()
}

func (ScopeRepository) SetDefault(ctx context.Context, db DBTX, id string) error {
	result, err := db.ExecContext(ctx, `UPDATE pc_scope_settings SET scope_id = ? WHERE name = 'default'`, id)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		_, err = db.ExecContext(ctx, `INSERT INTO pc_scope_settings (name, scope_id) VALUES ('default', ?)`, id)
	}
	return err
}

func (ScopeRepository) Default(ctx context.Context, db DBTX) (scope.Descriptor, bool, error) {
	var id string
	if err := db.QueryRowContext(ctx, `SELECT scope_id FROM pc_scope_settings WHERE name = 'default'`).Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return scope.Descriptor{}, false, nil
		}
		return scope.Descriptor{}, false, err
	}
	descriptor, err := ScopeRepository{}.Get(ctx, db, id)
	return descriptor, err == nil, err
}

func (ScopeRepository) SetBinding(ctx context.Context, db DBTX, key scope.BindingKey, id string) (scope.Binding, error) {
	if _, err := scope.NewBinding(key, id); err != nil {
		return scope.Binding{}, err
	}
	result, err := db.ExecContext(ctx, `UPDATE pc_scope_bindings SET scope_id = ?
        WHERE integration = ? AND kind = ? AND external_id = ?`, id, key.Integration(), key.Kind(), key.ExternalID())
	if err != nil {
		return scope.Binding{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return scope.Binding{}, err
	}
	if affected == 0 {
		if _, err := db.ExecContext(ctx, `INSERT INTO pc_scope_bindings
            (integration, kind, external_id, scope_id) VALUES (?, ?, ?, ?)`, key.Integration(), key.Kind(), key.ExternalID(), id); err != nil {
			return scope.Binding{}, err
		}
	}
	return scope.NewBinding(key, id)
}

func (ScopeRepository) Binding(ctx context.Context, db DBTX, key scope.BindingKey) (scope.Binding, bool, error) {
	var id string
	err := db.QueryRowContext(ctx, `SELECT scope_id FROM pc_scope_bindings
        WHERE integration = ? AND kind = ? AND external_id = ?`, key.Integration(), key.Kind(), key.ExternalID()).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return scope.Binding{}, false, nil
	}
	if err != nil {
		return scope.Binding{}, false, err
	}
	binding, err := scope.NewBinding(key, id)
	return binding, err == nil, err
}

type scopeScanner interface{ Scan(...any) error }

func loadScope(ctx context.Context, db DBTX, row scopeScanner) (scope.Descriptor, error) {
	var id, title, summary string
	var parent sql.NullString
	var version int64
	if err := row.Scan(&id, &title, &summary, &parent, &version); err != nil {
		return scope.Descriptor{}, err
	}
	contextReferences, err := loadScopeContextReferences(ctx, db, id)
	if err != nil {
		return scope.Descriptor{}, err
	}
	externalReferences, err := loadScopeExternalReferences(ctx, db, id)
	if err != nil {
		return scope.Descriptor{}, err
	}
	return scope.NewDescriptor(id, title, summary, parent.String, contextReferences, externalReferences, version)
}

func loadScopeContextReferences(ctx context.Context, db DBTX, id string) (result []string, returnErr error) {
	rows, err := db.QueryContext(ctx, `SELECT referenced_scope_id FROM pc_scope_context_references
        WHERE scope_id = ? ORDER BY referenced_scope_id`, id)
	if err != nil {
		return nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, rows.Close()) }()
	for rows.Next() {
		var reference string
		if err := rows.Scan(&reference); err != nil {
			return nil, err
		}
		result = append(result, reference)
	}
	return result, rows.Err()
}

func loadScopeExternalReferences(ctx context.Context, db DBTX, id string) (result []scope.ExternalReference, returnErr error) {
	rows, err := db.QueryContext(ctx, `SELECT kind, value FROM pc_scope_external_references
        WHERE scope_id = ? ORDER BY ordinal`, id)
	if err != nil {
		return nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, rows.Close()) }()
	for rows.Next() {
		var kind, value string
		if err := rows.Scan(&kind, &value); err != nil {
			return nil, err
		}
		reference, err := scope.NewExternalReference(kind, value)
		if err != nil {
			return nil, fmt.Errorf("decode Scope external reference: %w", err)
		}
		result = append(result, reference)
	}
	return result, rows.Err()
}

func draftDigest(draft scope.Draft) (string, error) {
	type externalReference struct {
		Kind  string `json:"kind"`
		Value string `json:"value"`
	}
	value := struct {
		Title              string              `json:"title"`
		Summary            string              `json:"summary"`
		ParentScopeID      string              `json:"parent_scope_id"`
		ContextReferences  []string            `json:"context_references"`
		ExternalReferences []externalReference `json:"external_references"`
	}{
		Title: draft.Title(), Summary: draft.Summary(), ParentScopeID: draft.ParentScopeID(),
		ContextReferences: draft.ContextReferences(),
	}
	for _, reference := range draft.ExternalReferences() {
		value.ExternalReferences = append(value.ExternalReferences, externalReference{Kind: reference.Kind(), Value: reference.Value()})
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}
