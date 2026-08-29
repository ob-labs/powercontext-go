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
	"time"

	"github.com/ob-labs/powercontext-go/internal/handoffreport"
)

func (s *HandoffReportStore) GetWorkspaceBinding(ctx context.Context, workspaceID string) (handoffreport.WorkspaceBinding, error) {
	var result handoffreport.WorkspaceBinding
	err := s.database.Transaction(ctx, func(tx DBTX) error {
		var err error
		result, err = s.getWorkspaceBinding(ctx, tx, workspaceID, true)
		return err
	})
	return result, err
}

func (s *HandoffReportStore) AttachWorkspaceBinding(ctx context.Context, workspaceID, projectID string, repository handoffreport.RepositoryRef, expected *int, confirmedAt time.Time) (handoffreport.WorkspaceBinding, error) {
	var result handoffreport.WorkspaceBinding
	err := s.database.Transaction(ctx, func(tx DBTX) error {
		if _, err := s.getProject(ctx, tx, projectID); err != nil {
			return err
		}
		var err error
		result, err = s.attachWorkspaceBinding(ctx, tx, workspaceID, projectID, repository, expected, confirmedAt)
		return err
	})
	return result, err
}

func (s *HandoffReportStore) DetachWorkspaceBinding(ctx context.Context, workspaceID string, expected int) (handoffreport.WorkspaceBinding, error) {
	var result handoffreport.WorkspaceBinding
	err := s.database.Transaction(ctx, func(tx DBTX) error {
		var err error
		result, err = s.detachWorkspaceBinding(ctx, tx, workspaceID, expected)
		return err
	})
	return result, err
}

type workspaceRow struct {
	workspaceID, projectID        string
	provider                      string
	repositoryID, remote, subpath sql.NullString
	state                         string
	confirmedAt                   string
	version                       int
	payload                       []byte
}

func (s *HandoffReportStore) findWorkspace(ctx context.Context, tx DBTX, id string) (workspaceRow, error) {
	var row workspaceRow
	var payload any
	err := tx.QueryRowContext(ctx, "SELECT workspace_instance_id, project_id, provider, repository_id, normalized_remote, subpath, state, confirmed_at, version, payload FROM pc_handoff_report_workspace_bindings WHERE workspace_instance_id = ?", id).Scan(&row.workspaceID, &row.projectID, &row.provider, &row.repositoryID, &row.remote, &row.subpath, &row.state, &row.confirmedAt, &row.version, &payload)
	if err != nil {
		return row, err
	}
	row.payload, err = reportPayload(payload)
	return row, err
}

func decodeWorkspaceRow(row workspaceRow) (handoffreport.WorkspaceBinding, error) {
	var value handoffreport.WorkspaceBinding
	if err := unmarshalJSON(row.payload, &value); err != nil {
		return value, &handoffreport.InvalidStoredCatalogError{Kind: "WorkspaceBinding", Detail: "does not match its schema"}
	}
	if value.WorkspaceInstanceID() != row.workspaceID || value.ProjectID() != row.projectID || value.Version() != row.version || string(value.State()) != row.state {
		return value, &handoffreport.InvalidStoredCatalogError{Kind: "WorkspaceBinding", Detail: "identity does not match indexed columns"}
	}
	return value, nil
}

func (s *HandoffReportStore) getWorkspaceBinding(ctx context.Context, tx DBTX, id string, confirmedOnly bool) (handoffreport.WorkspaceBinding, error) {
	if err := reportIdentifier("workspace_instance_id", id, handoffreport.MaxWorkspaceInstanceIDLength); err != nil {
		return handoffreport.WorkspaceBinding{}, err
	}
	row, err := s.findWorkspace(ctx, tx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return handoffreport.WorkspaceBinding{}, &handoffreport.WorkspaceBindingNotFoundError{WorkspaceInstanceID: id}
	}
	if err != nil {
		return handoffreport.WorkspaceBinding{}, err
	}
	value, err := decodeWorkspaceRow(row)
	if err != nil {
		return value, err
	}
	if confirmedOnly && value.State() != handoffreport.WorkspaceConfirmed {
		return handoffreport.WorkspaceBinding{}, &handoffreport.WorkspaceBindingNotFoundError{WorkspaceInstanceID: id}
	}
	return value, nil
}

func (s *HandoffReportStore) attachWorkspaceBinding(ctx context.Context, tx DBTX, id, projectID string, repository handoffreport.RepositoryRef, expected *int, confirmedAt time.Time) (handoffreport.WorkspaceBinding, error) {
	if err := reportIdentifier("workspace_instance_id", id, handoffreport.MaxWorkspaceInstanceIDLength); err != nil {
		return handoffreport.WorkspaceBinding{}, err
	}
	if expected != nil && *expected < 1 {
		return handoffreport.WorkspaceBinding{}, catalogArg("expected_version", "must be a positive integer")
	}
	row, err := s.findWorkspace(ctx, tx, id)
	found := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return handoffreport.WorkspaceBinding{}, err
	}
	version := 1
	if expected == nil {
		if found {
			return handoffreport.WorkspaceBinding{}, workspaceConflict(id, nil, &row.version, "workspace already has a binding record")
		}
	} else {
		version = *expected + 1
		if !found {
			return handoffreport.WorkspaceBinding{}, workspaceConflict(id, expected, nil, "workspace binding record is missing")
		}
		current, decodeErr := decodeWorkspaceRow(row)
		if decodeErr != nil {
			return handoffreport.WorkspaceBinding{}, decodeErr
		}
		if current.Version() != *expected {
			return handoffreport.WorkspaceBinding{}, workspaceConflict(id, expected, intPointer(current.Version()), "")
		}
		if current.State() == handoffreport.WorkspaceConfirmed && current.ProjectID() != projectID {
			return handoffreport.WorkspaceBinding{}, workspaceConflict(id, expected, intPointer(current.Version()), "detach the confirmed binding before attaching another Project")
		}
	}
	value, err := handoffreport.NewWorkspaceBinding(id, projectID, repository, handoffreport.WorkspaceConfirmed, confirmedAt.UTC(), version)
	if err != nil {
		return handoffreport.WorkspaceBinding{}, err
	}
	payload, err := marshalJSON(value)
	if err != nil {
		return handoffreport.WorkspaceBinding{}, err
	}
	ref := value.RepositoryRef()
	if expected == nil {
		_, err = tx.ExecContext(ctx, "INSERT INTO pc_handoff_report_workspace_bindings (workspace_instance_id, project_id, provider, repository_id, normalized_remote, subpath, state, confirmed_at, version, payload) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)", id, projectID, ref.Provider(), reportNullableString(ref.RepositoryID()), reportNullableString(ref.NormalizedRemote()), reportNullableString(ref.Subpath()), value.State(), handoffreport.UTCText(value.ConfirmedAt()), version, string(payload))
		if err != nil {
			return handoffreport.WorkspaceBinding{}, workspaceConflict(id, nil, nil, "workspace already has a binding record")
		}
		return value, nil
	}
	result, err := tx.ExecContext(ctx, "UPDATE pc_handoff_report_workspace_bindings SET project_id = ?, provider = ?, repository_id = ?, normalized_remote = ?, subpath = ?, state = ?, confirmed_at = ?, version = ?, payload = ? WHERE workspace_instance_id = ? AND version = ?", projectID, ref.Provider(), reportNullableString(ref.RepositoryID()), reportNullableString(ref.NormalizedRemote()), reportNullableString(ref.Subpath()), value.State(), handoffreport.UTCText(value.ConfirmedAt()), version, string(payload), id, *expected)
	if err != nil {
		return handoffreport.WorkspaceBinding{}, err
	}
	affected, _ := result.RowsAffected()
	if affected != 1 {
		return handoffreport.WorkspaceBinding{}, workspaceConflict(id, expected, intPointer(row.version), "")
	}
	return value, nil
}

func (s *HandoffReportStore) detachWorkspaceBinding(ctx context.Context, tx DBTX, id string, expected int) (handoffreport.WorkspaceBinding, error) {
	if expected < 1 {
		return handoffreport.WorkspaceBinding{}, catalogArg("expected_version", "must be a positive integer")
	}
	current, err := s.getWorkspaceBinding(ctx, tx, id, false)
	if err != nil {
		var missing *handoffreport.WorkspaceBindingNotFoundError
		if errors.As(err, &missing) {
			return handoffreport.WorkspaceBinding{}, workspaceConflict(id, &expected, nil, "workspace binding record is missing")
		}
		return handoffreport.WorkspaceBinding{}, err
	}
	if current.Version() != expected {
		return handoffreport.WorkspaceBinding{}, workspaceConflict(id, &expected, intPointer(current.Version()), "")
	}
	if current.State() != handoffreport.WorkspaceConfirmed {
		return handoffreport.WorkspaceBinding{}, workspaceConflict(id, &expected, intPointer(current.Version()), "workspace binding is already detached")
	}
	detached, err := handoffreport.NewWorkspaceBinding(current.WorkspaceInstanceID(), current.ProjectID(), current.RepositoryRef(), handoffreport.WorkspaceDetached, current.ConfirmedAt(), expected+1)
	if err != nil {
		return handoffreport.WorkspaceBinding{}, err
	}
	payload, err := marshalJSON(detached)
	if err != nil {
		return handoffreport.WorkspaceBinding{}, err
	}
	result, err := tx.ExecContext(ctx, "UPDATE pc_handoff_report_workspace_bindings SET state = ?, version = ?, payload = ? WHERE workspace_instance_id = ? AND version = ?", detached.State(), detached.Version(), string(payload), id, expected)
	if err != nil {
		return handoffreport.WorkspaceBinding{}, err
	}
	affected, _ := result.RowsAffected()
	if affected != 1 {
		return handoffreport.WorkspaceBinding{}, workspaceConflict(id, &expected, intPointer(current.Version()), "")
	}
	return detached, nil
}

func workspaceConflict(id string, expected, current *int, detail string) error {
	return &handoffreport.WorkspaceBindingConflictError{WorkspaceInstanceID: id, ExpectedVersion: expected, CurrentVersion: current, Detail: detail}
}
