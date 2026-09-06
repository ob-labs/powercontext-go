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
	"encoding/json/v2"
	"errors"

	"github.com/ob-labs/powercontext-go/source"
)

// CheckpointConflictError reports an optimistic Connector checkpoint mismatch.
type CheckpointConflictError struct{}

func (*CheckpointConflictError) Error() string {
	return "Connector checkpoint changed during the run"
}

// ConnectorCheckpointRepository persists opaque JSON checkpoint values.
type ConnectorCheckpointRepository struct{}

type connectorCheckpointRow struct {
	value   []byte
	payload []byte
}

type connectorCheckpointEnvelope struct {
	Value jsontext.Value `json:"value"`
}

func (repository ConnectorCheckpointRepository) Load(
	ctx context.Context,
	db DBTX,
	binding source.ConnectorBinding,
) ([]byte, bool, error) {
	if err := binding.Validate(); err != nil {
		return nil, false, err
	}
	row, found, err := repository.load(ctx, db, binding)
	if err != nil || !found {
		return nil, found, err
	}
	return bytes.Clone(row.value), true, nil
}

func (ConnectorCheckpointRepository) load(
	ctx context.Context,
	db DBTX,
	binding source.ConnectorBinding,
) (connectorCheckpointRow, bool, error) {
	var connectorName, connectorVersion string
	var checkpoint any
	err := db.QueryRowContext(ctx, `SELECT connector_name, connector_version, checkpoint
        FROM pc_connector_checkpoints WHERE scope_id = ? AND binding_id = ?`, binding.ScopeID(), binding.ID()).Scan(&connectorName, &connectorVersion, &checkpoint)
	if errors.Is(err, sql.ErrNoRows) {
		return connectorCheckpointRow{}, false, nil
	}
	if err != nil {
		return connectorCheckpointRow{}, false, err
	}
	if connectorName != binding.ConnectorName() || connectorVersion != binding.ConnectorVersion() {
		return connectorCheckpointRow{}, false, invalidStoredCheckpoint("binding identity does not match request")
	}
	payload, err := storedBytes(checkpoint, "checkpoint")
	if err != nil {
		return connectorCheckpointRow{}, false, err
	}
	value, err := decodeCheckpoint(payload)
	if err != nil {
		return connectorCheckpointRow{}, false, err
	}
	return connectorCheckpointRow{value: value, payload: payload}, true, nil
}

func (ConnectorCheckpointRepository) Save(
	ctx context.Context,
	db DBTX,
	binding source.ConnectorBinding,
	checkpoint, expected []byte,
	expectedFound bool,
) error {
	if err := binding.Validate(); err != nil {
		return err
	}
	payload, _, err := encodeCheckpointArgument("checkpoint", checkpoint)
	if err != nil {
		return err
	}
	if !expectedFound {
		// The primary key is the atomic create CAS. A read before this insert
		// would pin a SQLite snapshot and turn a lost race into SQLITE_BUSY.
		_, insertErr := db.ExecContext(ctx, `INSERT INTO pc_connector_checkpoints
            (scope_id, binding_id, connector_name, connector_version, checkpoint) VALUES (?, ?, ?, ?, ?)`,
			binding.ScopeID(), binding.ID(), binding.ConnectorName(), binding.ConnectorVersion(), payload,
		)
		if insertErr != nil {
			if !isIntegrityConstraint(insertErr) {
				return insertErr
			}
			_, found, loadErr := (ConnectorCheckpointRepository{}).load(ctx, db, binding)
			if loadErr != nil {
				return loadErr
			}
			if found {
				return &CheckpointConflictError{}
			}
			return insertErr
		}
		return nil
	}

	expectedPayload, expectedValue, err := encodeCheckpointArgument("expected", expected)
	if err != nil {
		return err
	}
	actual, found, err := (ConnectorCheckpointRepository{}).load(ctx, db, binding)
	if err != nil {
		return err
	}
	if !found || !bytes.Equal(actual.value, expectedValue) {
		return &CheckpointConflictError{}
	}
	// A Python-written row may use different object member ordering. Its raw
	// envelope remains the exact row version for CAS after semantic equality is
	// established; rows written here use expectedPayload directly.
	comparisonPayload := expectedPayload
	if !bytes.Equal(actual.payload, expectedPayload) {
		comparisonPayload = actual.payload
	}
	result, err := db.ExecContext(ctx, `UPDATE pc_connector_checkpoints SET checkpoint = ?
		WHERE scope_id = ? AND binding_id = ? AND checkpoint = ?`,
		payload, binding.ScopeID(), binding.ID(), comparisonPayload)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return &CheckpointConflictError{}
	}
	return nil
}

func encodeCheckpointArgument(field string, checkpoint []byte) ([]byte, []byte, error) {
	value := jsontext.Value(bytes.Clone(checkpoint))
	if len(value) == 0 {
		return nil, nil, &InvalidRepositoryArgumentError{Field: field, Detail: "must be valid JSON"}
	}
	if err := value.Canonicalize(jsontext.CanonicalizeRawInts(false)); err != nil {
		return nil, nil, &InvalidRepositoryArgumentError{Field: field, Detail: "must be valid JSON"}
	}
	payload, err := json.Marshal(connectorCheckpointEnvelope{Value: value})
	if err != nil {
		return nil, nil, &InvalidRepositoryArgumentError{Field: field, Detail: "must be valid JSON"}
	}
	return payload, value, nil
}

func decodeCheckpoint(payload []byte) ([]byte, error) {
	var envelope connectorCheckpointEnvelope
	if err := json.Unmarshal(payload, &envelope, json.RejectUnknownMembers(true)); err != nil || len(envelope.Value) == 0 {
		return nil, invalidStoredCheckpoint("payload does not match the model")
	}
	if err := envelope.Value.Canonicalize(jsontext.CanonicalizeRawInts(false)); err != nil {
		return nil, invalidStoredCheckpoint("payload does not match the model")
	}
	return bytes.Clone(envelope.Value), nil
}

func invalidStoredCheckpoint(issue string) error {
	return &InvalidStoredPayloadError{Kind: "connector-checkpoint", Name: "<redacted>", Issue: issue}
}
