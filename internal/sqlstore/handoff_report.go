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
	"strings"
	"time"

	"github.com/ob-labs/powercontext-go/internal/handoffreport"
)

// HandoffReportStore is a use-case shaped adapter for the optional schema.
// Each exported method owns exactly one transaction; report snapshots freeze
// catalog and Activity state together.
type HandoffReportStore struct {
	database *Database
	dialect  Dialect
}

func NewHandoffReportStore(database *Database, dialect Dialect) (*HandoffReportStore, error) {
	if database == nil {
		return nil, errors.New("sqlstore: Handoff Report database must not be nil")
	}
	if dialect != SQLiteDialect && dialect != MySQLDialect {
		return nil, errors.New("sqlstore: unsupported Handoff Report dialect")
	}
	return &HandoffReportStore{database: database, dialect: dialect}, nil
}

func (s *HandoffReportStore) EnsureSchema(ctx context.Context) error {
	return s.database.Transaction(ctx, func(tx DBTX) error {
		return EnsureHandoffReportSchemaForDialect(ctx, tx, s.dialect)
	})
}

func (s *HandoffReportStore) CreateProject(ctx context.Context, value handoffreport.ProjectDescriptor, effectiveAt time.Time) (handoffreport.ProjectDescriptor, error) {
	var result handoffreport.ProjectDescriptor
	err := s.database.Transaction(ctx, func(tx DBTX) error {
		var err error
		result, err = s.createProject(ctx, tx, value, effectiveAt)
		return err
	})
	return result, err
}

func (s *HandoffReportStore) GetProject(ctx context.Context, projectID string) (handoffreport.ProjectDescriptor, error) {
	var result handoffreport.ProjectDescriptor
	err := s.database.Transaction(ctx, func(tx DBTX) error { var err error; result, err = s.getProject(ctx, tx, projectID); return err })
	return result, err
}

func (s *HandoffReportStore) UpdateProject(ctx context.Context, value handoffreport.ProjectDescriptor, expected int, effectiveAt time.Time) (handoffreport.ProjectDescriptor, error) {
	var result handoffreport.ProjectDescriptor
	err := s.database.Transaction(ctx, func(tx DBTX) error {
		var err error
		result, err = s.updateProject(ctx, tx, value, expected, effectiveAt)
		return err
	})
	return result, err
}

func (s *HandoffReportStore) ListProjects(ctx context.Context, cursor *string, limit int, includeArchived bool) (handoffreport.Page[handoffreport.ProjectDescriptor], error) {
	var result handoffreport.Page[handoffreport.ProjectDescriptor]
	err := s.database.Transaction(ctx, func(tx DBTX) error {
		var err error
		result, err = s.listProjects(ctx, tx, cursor, limit, includeArchived)
		return err
	})
	return result, err
}

func (s *HandoffReportStore) RegisterWorkstream(ctx context.Context, value handoffreport.WorkstreamDescriptor, effectiveAt time.Time) (handoffreport.WorkstreamDescriptor, error) {
	var result handoffreport.WorkstreamDescriptor
	err := s.database.Transaction(ctx, func(tx DBTX) error {
		var err error
		result, err = s.createWorkstream(ctx, tx, value, effectiveAt)
		return err
	})
	return result, err
}

func (s *HandoffReportStore) UpdateWorkstream(ctx context.Context, value handoffreport.WorkstreamDescriptor, expected int, effectiveAt time.Time) (handoffreport.WorkstreamDescriptor, error) {
	var result handoffreport.WorkstreamDescriptor
	err := s.database.Transaction(ctx, func(tx DBTX) error {
		var err error
		result, err = s.updateWorkstream(ctx, tx, value, expected, effectiveAt)
		return err
	})
	return result, err
}

func (s *HandoffReportStore) ListWorkstreams(ctx context.Context, projectID string, cursor *string, limit int, includeArchived bool) (handoffreport.Page[handoffreport.WorkstreamDescriptor], error) {
	var result handoffreport.Page[handoffreport.WorkstreamDescriptor]
	err := s.database.Transaction(ctx, func(tx DBTX) error {
		var err error
		result, err = s.listWorkstreams(ctx, tx, projectID, cursor, limit, includeArchived)
		return err
	})
	return result, err
}

func (s *HandoffReportStore) createProject(ctx context.Context, tx DBTX, value handoffreport.ProjectDescriptor, effectiveAt time.Time) (handoffreport.ProjectDescriptor, error) {
	if value.Version() != 1 {
		return handoffreport.ProjectDescriptor{}, catalogArg("version", "a new Project must start at version 1")
	}
	if _, err := s.findProject(ctx, tx, value.ProjectID()); err == nil {
		return handoffreport.ProjectDescriptor{}, projectConflict(value.ProjectID(), nil, intPointer(value.Version()), "")
	} else if !errors.Is(err, sql.ErrNoRows) {
		return handoffreport.ProjectDescriptor{}, err
	}
	var owner string
	var ownerVersion int
	err := tx.QueryRowContext(ctx, "SELECT project_id, version FROM pc_handoff_report_projects WHERE project_key = ?", value.ProjectKey()).Scan(&owner, &ownerVersion)
	if err == nil {
		return handoffreport.ProjectDescriptor{}, projectConflict(value.ProjectID(), nil, &ownerVersion, fmt.Sprintf("Project key %q is already in use", value.ProjectKey()))
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return handoffreport.ProjectDescriptor{}, err
	}
	payload, err := marshalJSON(value)
	if err != nil {
		return handoffreport.ProjectDescriptor{}, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO pc_handoff_report_projects (project_id, project_key, version, catalog_state, payload) VALUES (?, ?, ?, ?, ?)", value.ProjectID(), value.ProjectKey(), value.Version(), value.CatalogState(), string(payload)); err != nil {
		return handoffreport.ProjectDescriptor{}, projectConflict(value.ProjectID(), nil, nil, fmt.Sprintf("Project key %q conflicts with the current catalog", value.ProjectKey()))
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO pc_handoff_report_project_revisions (project_id, version, effective_at, payload) VALUES (?, ?, ?, ?)", value.ProjectID(), value.Version(), handoffreport.UTCText(effectiveAt), string(payload)); err != nil {
		return handoffreport.ProjectDescriptor{}, err
	}
	return value, nil
}

func (s *HandoffReportStore) getProject(ctx context.Context, tx DBTX, projectID string) (handoffreport.ProjectDescriptor, error) {
	if err := reportIdentifier("project_id", projectID, handoffreport.MaxReportIDLength); err != nil {
		return handoffreport.ProjectDescriptor{}, err
	}
	row, err := s.findProject(ctx, tx, projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return handoffreport.ProjectDescriptor{}, &handoffreport.ProjectNotFoundError{ProjectID: projectID}
	}
	if err != nil {
		return handoffreport.ProjectDescriptor{}, err
	}
	return decodeProjectRow(row)
}

type projectRow struct {
	projectID, projectKey string
	version               int
	state                 string
	payload               []byte
}

func (s *HandoffReportStore) findProject(ctx context.Context, tx DBTX, id string) (projectRow, error) {
	var row projectRow
	var payload any
	err := tx.QueryRowContext(ctx, "SELECT project_id, project_key, version, catalog_state, payload FROM pc_handoff_report_projects WHERE project_id = ?", id).Scan(&row.projectID, &row.projectKey, &row.version, &row.state, &payload)
	if err != nil {
		return row, err
	}
	row.payload, err = reportPayload(payload)
	return row, err
}

func decodeProjectRow(row projectRow) (handoffreport.ProjectDescriptor, error) {
	var value handoffreport.ProjectDescriptor
	if err := unmarshalJSON(row.payload, &value); err != nil {
		return value, &handoffreport.InvalidStoredCatalogError{Kind: "Project descriptor", Detail: "does not match its schema"}
	}
	if value.ProjectID() != row.projectID || value.ProjectKey() != row.projectKey || value.Version() != row.version || string(value.CatalogState()) != row.state {
		return value, &handoffreport.InvalidStoredCatalogError{Kind: "Project descriptor", Detail: "identity does not match indexed columns"}
	}
	return value, nil
}

func (s *HandoffReportStore) updateProject(ctx context.Context, tx DBTX, value handoffreport.ProjectDescriptor, expected int, effectiveAt time.Time) (handoffreport.ProjectDescriptor, error) {
	if expected < 1 {
		return handoffreport.ProjectDescriptor{}, catalogArg("expected_version", "must be a positive integer")
	}
	if value.Version() != expected+1 {
		return handoffreport.ProjectDescriptor{}, catalogArg("version", "updated Project version must equal expected_version + 1")
	}
	current, err := s.getProject(ctx, tx, value.ProjectID())
	if err != nil {
		return handoffreport.ProjectDescriptor{}, err
	}
	if current.Version() != expected {
		return handoffreport.ProjectDescriptor{}, projectConflict(value.ProjectID(), &expected, intPointer(current.Version()), "")
	}
	var owner string
	var version int
	err = tx.QueryRowContext(ctx, "SELECT project_id, version FROM pc_handoff_report_projects WHERE project_key = ?", value.ProjectKey()).Scan(&owner, &version)
	if err == nil && owner != value.ProjectID() {
		return handoffreport.ProjectDescriptor{}, projectConflict(value.ProjectID(), &expected, intPointer(current.Version()), fmt.Sprintf("Project key %q is already in use", value.ProjectKey()))
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return handoffreport.ProjectDescriptor{}, err
	}
	payload, err := marshalJSON(value)
	if err != nil {
		return handoffreport.ProjectDescriptor{}, err
	}
	result, err := tx.ExecContext(ctx, "UPDATE pc_handoff_report_projects SET project_key = ?, version = ?, catalog_state = ?, payload = ? WHERE project_id = ? AND version = ?", value.ProjectKey(), value.Version(), value.CatalogState(), string(payload), value.ProjectID(), expected)
	if err != nil {
		return handoffreport.ProjectDescriptor{}, projectConflict(value.ProjectID(), &expected, intPointer(current.Version()), fmt.Sprintf("Project key %q conflicts with the current catalog", value.ProjectKey()))
	}
	affected, _ := result.RowsAffected()
	if affected != 1 {
		return handoffreport.ProjectDescriptor{}, projectConflict(value.ProjectID(), &expected, intPointer(current.Version()), "")
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO pc_handoff_report_project_revisions (project_id, version, effective_at, payload) VALUES (?, ?, ?, ?)", value.ProjectID(), value.Version(), handoffreport.UTCText(effectiveAt), string(payload)); err != nil {
		return handoffreport.ProjectDescriptor{}, err
	}
	return value, nil
}

func (s *HandoffReportStore) listProjects(ctx context.Context, tx DBTX, cursor *string, limit int, includeArchived bool) (page handoffreport.Page[handoffreport.ProjectDescriptor], returnErr error) {
	if err := pageArguments(cursor, limit, handoffreport.MaxReportIDLength); err != nil {
		return handoffreport.Page[handoffreport.ProjectDescriptor]{}, err
	}
	query := "SELECT project_id, project_key, version, catalog_state, payload FROM pc_handoff_report_projects WHERE 1 = 1"
	args := []any{}
	if cursor != nil {
		query += " AND project_id > ?"
		args = append(args, *cursor)
	}
	if !includeArchived {
		query += " AND catalog_state = 'included'"
	}
	query += " ORDER BY project_id LIMIT ?"
	args = append(args, limit+1)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return handoffreport.Page[handoffreport.ProjectDescriptor]{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, rows.Close()) }()
	items := []handoffreport.ProjectDescriptor{}
	for rows.Next() {
		var row projectRow
		var payload any
		if scanErr := rows.Scan(&row.projectID, &row.projectKey, &row.version, &row.state, &payload); scanErr != nil {
			return handoffreport.Page[handoffreport.ProjectDescriptor]{}, scanErr
		}
		row.payload, err = reportPayload(payload)
		if err != nil {
			return handoffreport.Page[handoffreport.ProjectDescriptor]{}, err
		}
		value, err := decodeProjectRow(row)
		if err != nil {
			return handoffreport.Page[handoffreport.ProjectDescriptor]{}, err
		}
		items = append(items, value)
	}
	if err := rows.Err(); err != nil {
		return handoffreport.Page[handoffreport.ProjectDescriptor]{}, err
	}
	var next *string
	if len(items) > limit {
		items = items[:limit]
		value := items[len(items)-1].ProjectID()
		next = &value
	}
	page = handoffreport.Page[handoffreport.ProjectDescriptor]{Items: items, NextCursor: next}
	return page, nil
}

type workstreamRow struct {
	scopeID, projectID string
	key                sql.NullString
	version            int
	state              string
	payload            []byte
}

func (s *HandoffReportStore) findWorkstream(ctx context.Context, tx DBTX, scope string) (workstreamRow, error) {
	var row workstreamRow
	var payload any
	err := tx.QueryRowContext(ctx, "SELECT scope_id, project_id, workstream_key, version, catalog_state, payload FROM pc_handoff_report_workstreams WHERE scope_id = ?", scope).Scan(&row.scopeID, &row.projectID, &row.key, &row.version, &row.state, &payload)
	if err != nil {
		return row, err
	}
	row.payload, err = reportPayload(payload)
	return row, err
}

func decodeWorkstreamRow(row workstreamRow) (handoffreport.WorkstreamDescriptor, error) {
	var value handoffreport.WorkstreamDescriptor
	if err := unmarshalJSON(row.payload, &value); err != nil {
		return value, &handoffreport.InvalidStoredCatalogError{Kind: "Workstream descriptor", Detail: "does not match its schema"}
	}
	key := value.Key()
	if value.ScopeID() != row.scopeID || value.ProjectID() != row.projectID || value.Version() != row.version || string(value.CatalogState()) != row.state || (key == nil) != (!row.key.Valid) || (key != nil && *key != row.key.String) {
		return value, &handoffreport.InvalidStoredCatalogError{Kind: "Workstream descriptor", Detail: "identity does not match indexed columns"}
	}
	return value, nil
}

func (s *HandoffReportStore) createWorkstream(ctx context.Context, tx DBTX, value handoffreport.WorkstreamDescriptor, effectiveAt time.Time) (handoffreport.WorkstreamDescriptor, error) {
	if value.Version() != 1 {
		return handoffreport.WorkstreamDescriptor{}, catalogArg("version", "a new Workstream must start at version 1")
	}
	if _, err := s.getProject(ctx, tx, value.ProjectID()); err != nil {
		return handoffreport.WorkstreamDescriptor{}, err
	}
	existing, err := s.findWorkstream(ctx, tx, value.ScopeID())
	if err == nil {
		if existing.projectID != value.ProjectID() {
			return handoffreport.WorkstreamDescriptor{}, &handoffreport.ScopeAlreadyGroupedError{ScopeID: value.ScopeID(), ProjectID: existing.projectID}
		}
		return handoffreport.WorkstreamDescriptor{}, workstreamConflict(value.ScopeID(), nil, intPointer(existing.version), fmt.Sprintf("scope %q is already registered", value.ScopeID()))
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return handoffreport.WorkstreamDescriptor{}, err
	}
	if key := value.Key(); key != nil {
		var owner string
		var version int
		err = tx.QueryRowContext(ctx, "SELECT scope_id, version FROM pc_handoff_report_workstreams WHERE project_id = ? AND workstream_key = ?", value.ProjectID(), *key).Scan(&owner, &version)
		if err == nil {
			return handoffreport.WorkstreamDescriptor{}, workstreamConflict(value.ScopeID(), nil, &version, fmt.Sprintf("Workstream key %q is already in use", *key))
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return handoffreport.WorkstreamDescriptor{}, err
		}
	}
	payload, err := marshalJSON(value)
	if err != nil {
		return handoffreport.WorkstreamDescriptor{}, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO pc_handoff_report_workstreams (scope_id, project_id, workstream_key, version, catalog_state, payload) VALUES (?, ?, ?, ?, ?, ?)", value.ScopeID(), value.ProjectID(), reportNullableString(value.Key()), value.Version(), value.CatalogState(), string(payload)); err != nil {
		return handoffreport.WorkstreamDescriptor{}, workstreamConflict(value.ScopeID(), nil, nil, "Workstream key conflicts with the current catalog")
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO pc_handoff_report_workstream_revisions (scope_id, version, project_id, effective_at, payload) VALUES (?, ?, ?, ?, ?)", value.ScopeID(), value.Version(), value.ProjectID(), handoffreport.UTCText(effectiveAt), string(payload)); err != nil {
		return handoffreport.WorkstreamDescriptor{}, err
	}
	return value, nil
}

func (s *HandoffReportStore) updateWorkstream(ctx context.Context, tx DBTX, value handoffreport.WorkstreamDescriptor, expected int, effectiveAt time.Time) (handoffreport.WorkstreamDescriptor, error) {
	if expected < 1 {
		return handoffreport.WorkstreamDescriptor{}, catalogArg("expected_version", "must be a positive integer")
	}
	if value.Version() != expected+1 {
		return handoffreport.WorkstreamDescriptor{}, catalogArg("version", "updated Workstream version must equal expected_version + 1")
	}
	row, err := s.findWorkstream(ctx, tx, value.ScopeID())
	if errors.Is(err, sql.ErrNoRows) {
		return handoffreport.WorkstreamDescriptor{}, &handoffreport.WorkstreamNotFoundError{ScopeID: value.ScopeID()}
	}
	if err != nil {
		return handoffreport.WorkstreamDescriptor{}, err
	}
	if row.projectID != value.ProjectID() {
		return handoffreport.WorkstreamDescriptor{}, catalogArg("project_id", "Workstream membership cannot move between Projects")
	}
	if row.version != expected {
		return handoffreport.WorkstreamDescriptor{}, workstreamConflict(value.ScopeID(), &expected, &row.version, "")
	}
	if key := value.Key(); key != nil {
		var owner string
		var version int
		err = tx.QueryRowContext(ctx, "SELECT scope_id, version FROM pc_handoff_report_workstreams WHERE project_id = ? AND workstream_key = ?", value.ProjectID(), *key).Scan(&owner, &version)
		if err == nil && owner != value.ScopeID() {
			return handoffreport.WorkstreamDescriptor{}, workstreamConflict(value.ScopeID(), &expected, &row.version, fmt.Sprintf("Workstream key %q is already in use", *key))
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return handoffreport.WorkstreamDescriptor{}, err
		}
	}
	payload, err := marshalJSON(value)
	if err != nil {
		return handoffreport.WorkstreamDescriptor{}, err
	}
	result, err := tx.ExecContext(ctx, "UPDATE pc_handoff_report_workstreams SET workstream_key = ?, version = ?, catalog_state = ?, payload = ? WHERE scope_id = ? AND version = ?", reportNullableString(value.Key()), value.Version(), value.CatalogState(), string(payload), value.ScopeID(), expected)
	if err != nil {
		return handoffreport.WorkstreamDescriptor{}, workstreamConflict(value.ScopeID(), &expected, &row.version, "Workstream key conflicts with the current catalog")
	}
	affected, _ := result.RowsAffected()
	if affected != 1 {
		return handoffreport.WorkstreamDescriptor{}, workstreamConflict(value.ScopeID(), &expected, &row.version, "")
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO pc_handoff_report_workstream_revisions (scope_id, version, project_id, effective_at, payload) VALUES (?, ?, ?, ?, ?)", value.ScopeID(), value.Version(), value.ProjectID(), handoffreport.UTCText(effectiveAt), string(payload)); err != nil {
		return handoffreport.WorkstreamDescriptor{}, err
	}
	return value, nil
}

func (s *HandoffReportStore) listWorkstreams(ctx context.Context, tx DBTX, projectID string, cursor *string, limit int, includeArchived bool) (page handoffreport.Page[handoffreport.WorkstreamDescriptor], returnErr error) {
	if err := reportIdentifier("project_id", projectID, handoffreport.MaxReportIDLength); err != nil {
		return handoffreport.Page[handoffreport.WorkstreamDescriptor]{}, err
	}
	if err := pageArguments(cursor, limit, handoffreport.MaxScopeIDLength); err != nil {
		return handoffreport.Page[handoffreport.WorkstreamDescriptor]{}, err
	}
	query := "SELECT scope_id, project_id, workstream_key, version, catalog_state, payload FROM pc_handoff_report_workstreams WHERE project_id = ?"
	args := []any{projectID}
	if cursor != nil {
		query += " AND scope_id > ?"
		args = append(args, *cursor)
	}
	if !includeArchived {
		query += " AND catalog_state = 'included'"
	}
	query += " ORDER BY scope_id LIMIT ?"
	args = append(args, limit+1)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return handoffreport.Page[handoffreport.WorkstreamDescriptor]{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, rows.Close()) }()
	items := []handoffreport.WorkstreamDescriptor{}
	for rows.Next() {
		var row workstreamRow
		var payload any
		if scanErr := rows.Scan(&row.scopeID, &row.projectID, &row.key, &row.version, &row.state, &payload); scanErr != nil {
			return handoffreport.Page[handoffreport.WorkstreamDescriptor]{}, scanErr
		}
		row.payload, err = reportPayload(payload)
		if err != nil {
			return handoffreport.Page[handoffreport.WorkstreamDescriptor]{}, err
		}
		value, err := decodeWorkstreamRow(row)
		if err != nil {
			return handoffreport.Page[handoffreport.WorkstreamDescriptor]{}, err
		}
		items = append(items, value)
	}
	if err := rows.Err(); err != nil {
		return handoffreport.Page[handoffreport.WorkstreamDescriptor]{}, err
	}
	var next *string
	if len(items) > limit {
		items = items[:limit]
		value := items[len(items)-1].ScopeID()
		next = &value
	}
	page = handoffreport.Page[handoffreport.WorkstreamDescriptor]{Items: items, NextCursor: next}
	return page, nil
}

func reportIdentifier(field, value string, maximum int) error {
	if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
		return catalogArg(field, "must be a non-empty trimmed string")
	}
	if len([]rune(value)) > maximum {
		return catalogArg(field, fmt.Sprintf("must not exceed %d characters", maximum))
	}
	return nil
}

func pageArguments(cursor *string, limit, maximum int) error {
	if cursor != nil {
		if err := reportIdentifier("cursor", *cursor, maximum); err != nil {
			return err
		}
	}
	if limit < 1 || limit > handoffreport.MaxCatalogPageSize {
		return catalogArg("limit", fmt.Sprintf("must be between 1 and %d", handoffreport.MaxCatalogPageSize))
	}
	return nil
}

func catalogArg(field, detail string) error {
	return &handoffreport.CatalogArgumentError{Field: field, Detail: detail}
}
func intPointer(value int) *int { copy := value; return &copy }
func reportNullableString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func projectConflict(id string, expected, current *int, detail string) error {
	return &handoffreport.ProjectConflictError{ProjectID: id, ExpectedVersion: expected, CurrentVersion: current, Detail: detail}
}

func workstreamConflict(id string, expected, current *int, detail string) error {
	return &handoffreport.WorkstreamConflictError{ScopeID: id, ExpectedVersion: expected, CurrentVersion: current, Detail: detail}
}

func reportPayload(value any) ([]byte, error) {
	switch typed := value.(type) {
	case string:
		return []byte(typed), nil
	case []byte:
		return bytes.Clone(typed), nil
	default:
		return nil, &InvalidStoredColumnError{Column: "payload", Expected: "text"}
	}
}

func semanticActivityPayload(payload []byte) ([]byte, error) {
	var value map[string]any
	if err := unmarshalJSON(payload, &value); err != nil {
		return nil, err
	}
	delete(value, "event_id")
	delete(value, "observed_at")
	return marshalJSON(value)
}

func duplicateStrings(values []handoffreport.ActivitySource) []handoffreport.ActivitySource {
	if values == nil {
		return nil
	}
	result := make([]handoffreport.ActivitySource, 0, len(values))
	seen := map[handoffreport.ActivitySource]struct{}{}
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
