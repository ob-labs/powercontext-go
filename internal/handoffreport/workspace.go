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

package handoffreport

import (
	"encoding/json"
	"time"
)

type WorkspaceBinding struct {
	workspaceInstanceID, projectID string
	repositoryRef                  RepositoryRef
	state                          WorkspaceBindingState
	confirmedAt                    time.Time
	version                        int
}

func NewWorkspaceBinding(workspaceID, projectID string, repository RepositoryRef, state WorkspaceBindingState, confirmedAt time.Time, version int) (WorkspaceBinding, error) {
	if err := requireText("workspace_instance_id", workspaceID, MaxWorkspaceInstanceIDLength); err != nil {
		return WorkspaceBinding{}, err
	}
	if err := requireText("project_id", projectID, MaxReportIDLength); err != nil {
		return WorkspaceBinding{}, err
	}
	if state != WorkspaceConfirmed && state != WorkspaceDetached {
		return WorkspaceBinding{}, fieldError("state", "has an unsupported value")
	}
	if confirmedAt.Location() != time.UTC {
		return WorkspaceBinding{}, fieldError("confirmed_at", "must be UTC")
	}
	if version < 1 {
		return WorkspaceBinding{}, fieldError("version", "must be positive")
	}
	return WorkspaceBinding{workspaceID, projectID, repository, state, confirmedAt, version}, nil
}
func (v WorkspaceBinding) WorkspaceInstanceID() string  { return v.workspaceInstanceID }
func (v WorkspaceBinding) ProjectID() string            { return v.projectID }
func (v WorkspaceBinding) RepositoryRef() RepositoryRef { return v.repositoryRef }
func (v WorkspaceBinding) State() WorkspaceBindingState { return v.state }
func (v WorkspaceBinding) ConfirmedAt() time.Time       { return v.confirmedAt }
func (v WorkspaceBinding) Version() int                 { return v.version }
func (v WorkspaceBinding) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"schema": "powercontext.workspace-binding.v1", "workspace_instance_id": v.workspaceInstanceID, "project_id": v.projectID, "repository_ref": v.repositoryRef, "state": v.state, "confirmed_at": JSONTimestampText(v.confirmedAt), "version": v.version})
}

func (v *WorkspaceBinding) UnmarshalJSON(data []byte) error {
	var dto struct {
		Schema      string                `json:"schema"`
		WorkspaceID string                `json:"workspace_instance_id"`
		ProjectID   string                `json:"project_id"`
		Repository  RepositoryRef         `json:"repository_ref"`
		State       WorkspaceBindingState `json:"state"`
		ConfirmedAt string                `json:"confirmed_at"`
		Version     int                   `json:"version"`
	}
	if err := decodeStrict(data, &dto); err != nil {
		return err
	}
	if dto.Schema != "powercontext.workspace-binding.v1" {
		return fieldError("schema", "has an unsupported value")
	}
	at, err := time.Parse(time.RFC3339Nano, dto.ConfirmedAt)
	if err != nil {
		return err
	}
	value, err := NewWorkspaceBinding(dto.WorkspaceID, dto.ProjectID, dto.Repository, dto.State, at.UTC(), dto.Version)
	if err == nil {
		*v = value
	}
	return err
}
