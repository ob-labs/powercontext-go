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

	"github.com/ob-labs/powercontext-go/artifact/skill"
)

// RemoteSkillTargetStorageError reports a storage failure without exposing
// durable identifiers, local database paths, or secret material.
type RemoteSkillTargetStorageError struct{}

func (*RemoteSkillTargetStorageError) Error() string {
	return "remote Skill target storage operation failed"
}

// RemoteSkillTargetRepository persists SQLite-only remote managed-Skill
// target records. All methods use the caller-owned transaction.
type RemoteSkillTargetRepository struct{}

// Create inserts a new target through its primary-key creation CAS.
func (repository RemoteSkillTargetRepository) Create(
	ctx context.Context,
	db DBTX,
	target skill.RemoteTarget,
) (skill.RemoteTarget, error) {
	if target.Generation() != 0 {
		return skill.RemoteTarget{}, &InvalidRepositoryArgumentError{
			Field: "target.generation", Detail: "must be zero for a new remote target",
		}
	}
	_, err := db.ExecContext(ctx, `INSERT INTO pc_agent_skill_targets (
        scope_id, target_id, display_name, agent_kind, installation_scope, delivery_mode, state,
        installation_id, enrollment_code_digest, enrollment_expires_at, credential_subject,
        credential_verifier, receiver_version, environment_fingerprint, machine_hostname,
        workspace_name, last_seen_at, generation, created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, remoteSkillTargetArguments(target)...)
	if err != nil {
		if isIntegrityConstraint(err) {
			return skill.RemoteTarget{}, &skill.RemoteTargetGenerationConflictError{}
		}
		return skill.RemoteTarget{}, &RemoteSkillTargetStorageError{}
	}
	return target, nil
}

// Get returns one scope-local target, or a redacted typed not-found error.
func (repository RemoteSkillTargetRepository) Get(
	ctx context.Context,
	db DBTX,
	scopeID, targetID string,
) (skill.RemoteTarget, error) {
	target, found, err := repository.find(ctx, db, `WHERE scope_id = ? AND target_id = ?`, scopeID, targetID)
	if err != nil {
		return skill.RemoteTarget{}, err
	}
	if !found {
		return skill.RemoteTarget{}, &skill.RemoteTargetNotFoundError{}
	}
	return target, nil
}

// List returns scope-local targets in stable target-ID order.
func (repository RemoteSkillTargetRepository) List(
	ctx context.Context,
	db DBTX,
	scopeID string,
) (result []skill.RemoteTarget, returnErr error) {
	rows, err := db.QueryContext(ctx, remoteSkillTargetSelect+` WHERE scope_id = ? ORDER BY created_at, target_id`, scopeID)
	if err != nil {
		return nil, &RemoteSkillTargetStorageError{}
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && returnErr == nil {
			returnErr = &RemoteSkillTargetStorageError{}
		}
	}()
	for rows.Next() {
		target, scanErr := scanRemoteSkillTarget(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, target)
	}
	if err := rows.Err(); err != nil {
		return nil, &RemoteSkillTargetStorageError{}
	}
	return result, nil
}

// FindByEnrollmentDigest resolves a pending enrollment credential without
// turning its absence into a target-existence disclosure.
func (repository RemoteSkillTargetRepository) FindByEnrollmentDigest(
	ctx context.Context,
	db DBTX,
	digest string,
) (skill.RemoteTarget, bool, error) {
	return repository.find(ctx, db, `WHERE enrollment_code_digest = ?`, digest)
}

// ConsumePending activates target only when its durable pending enrollment is
// still current. A false result deliberately does not disclose which condition
// changed between the lookup and this short SQLite transaction.
func (repository RemoteSkillTargetRepository) ConsumePending(
	ctx context.Context,
	db DBTX,
	target skill.RemoteTarget,
	digest string,
	now time.Time,
) (skill.RemoteTarget, bool, error) {
	if target.State() != skill.RemoteTargetActive || target.Generation() < 1 {
		return skill.RemoteTarget{}, false, &RemoteSkillTargetStorageError{}
	}
	arguments := remoteSkillTargetArguments(target)
	arguments = append(arguments,
		target.ScopeID(), target.ID(), target.Generation()-1, digest, now.UTC().Format(time.RFC3339Nano),
	)
	result, err := db.ExecContext(ctx, `UPDATE pc_agent_skill_targets SET
        display_name = ?, agent_kind = ?, installation_scope = ?, delivery_mode = ?, state = ?,
        installation_id = ?, enrollment_code_digest = ?, enrollment_expires_at = ?, credential_subject = ?,
        credential_verifier = ?, receiver_version = ?, environment_fingerprint = ?, machine_hostname = ?,
        workspace_name = ?, last_seen_at = ?, generation = ?, created_at = ?, updated_at = ?
        WHERE scope_id = ? AND target_id = ? AND generation = ? AND state = 'pending'
          AND enrollment_code_digest = ? AND enrollment_expires_at > ?`, arguments[2:]...)
	if err != nil {
		return skill.RemoteTarget{}, false, &RemoteSkillTargetStorageError{}
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return skill.RemoteTarget{}, false, &RemoteSkillTargetStorageError{}
	}
	if affected != 1 {
		return skill.RemoteTarget{}, false, nil
	}
	return target, true, nil
}

// Replace atomically writes a next-generation target. A zero-row update is
// re-read so a missing target cannot be misreported as a stale mutation.
func (repository RemoteSkillTargetRepository) Replace(
	ctx context.Context,
	db DBTX,
	target skill.RemoteTarget,
	expectedGeneration int,
) (skill.RemoteTarget, error) {
	if expectedGeneration < 0 || target.Generation() != expectedGeneration+1 {
		return skill.RemoteTarget{}, &RemoteSkillTargetStorageError{}
	}
	arguments := remoteSkillTargetArguments(target)
	arguments = append(arguments, target.ScopeID(), target.ID(), expectedGeneration)
	result, err := db.ExecContext(ctx, `UPDATE pc_agent_skill_targets SET
        display_name = ?, agent_kind = ?, installation_scope = ?, delivery_mode = ?, state = ?,
        installation_id = ?, enrollment_code_digest = ?, enrollment_expires_at = ?, credential_subject = ?,
        credential_verifier = ?, receiver_version = ?, environment_fingerprint = ?, machine_hostname = ?,
        workspace_name = ?, last_seen_at = ?, generation = ?, created_at = ?, updated_at = ?
        WHERE scope_id = ? AND target_id = ? AND generation = ?`, arguments[2:]...)
	if err != nil {
		return skill.RemoteTarget{}, &RemoteSkillTargetStorageError{}
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return skill.RemoteTarget{}, &RemoteSkillTargetStorageError{}
	}
	if affected == 1 {
		return target, nil
	}
	_, getErr := repository.Get(ctx, db, target.ScopeID(), target.ID())
	if getErr != nil {
		return skill.RemoteTarget{}, getErr
	}
	return skill.RemoteTarget{}, &skill.RemoteTargetGenerationConflictError{}
}

const remoteSkillTargetSelect = `SELECT
    scope_id, target_id, display_name, agent_kind, installation_scope, delivery_mode, state,
    installation_id, enrollment_code_digest, enrollment_expires_at, credential_subject,
    credential_verifier, receiver_version, environment_fingerprint, machine_hostname,
    workspace_name, last_seen_at, generation, created_at, updated_at
    FROM pc_agent_skill_targets`

func (repository RemoteSkillTargetRepository) find(
	ctx context.Context,
	db DBTX,
	where string,
	arguments ...any,
) (skill.RemoteTarget, bool, error) {
	row := db.QueryRowContext(ctx, remoteSkillTargetSelect+" "+where, arguments...)
	target, err := scanRemoteSkillTarget(row)
	if errors.Is(err, sql.ErrNoRows) {
		return skill.RemoteTarget{}, false, nil
	}
	if err != nil {
		return skill.RemoteTarget{}, false, err
	}
	return target, true, nil
}

type remoteSkillTargetScanner interface{ Scan(...any) error }

func scanRemoteSkillTarget(row remoteSkillTargetScanner) (skill.RemoteTarget, error) {
	var input skill.RemoteTargetInput
	var installationID, enrollmentDigest, enrollmentExpiry, credentialSubject, credentialVerifier sql.NullString
	var receiverVersion, environmentFingerprint, machineHostname, workspaceName, lastSeenAt sql.NullString
	var generation int
	var createdAt, updatedAt string
	if err := row.Scan(
		&input.ScopeID, &input.TargetID, &input.DisplayName, &input.AgentKind, &input.InstallationScope,
		&input.DeliveryMode, &input.State, &installationID, &enrollmentDigest, &enrollmentExpiry,
		&credentialSubject, &credentialVerifier, &receiverVersion, &environmentFingerprint, &machineHostname,
		&workspaceName, &lastSeenAt, &generation, &createdAt, &updatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return skill.RemoteTarget{}, err
		}
		return skill.RemoteTarget{}, &RemoteSkillTargetStorageError{}
	}
	input.InstallationID = installationID.String
	input.EnrollmentCodeDigest = enrollmentDigest.String
	input.CredentialSubject = credentialSubject.String
	input.CredentialVerifier = credentialVerifier.String
	input.ReceiverVersion = receiverVersion.String
	input.EnvironmentFingerprint = environmentFingerprint.String
	input.MachineHostname = machineHostname.String
	input.WorkspaceName = workspaceName.String
	input.Generation = generation
	var err error
	if input.EnrollmentExpiresAt, err = parseRemoteSkillTargetTime(enrollmentExpiry); err != nil {
		return skill.RemoteTarget{}, err
	}
	if input.LastSeenAt, err = parseRemoteSkillTargetTime(lastSeenAt); err != nil {
		return skill.RemoteTarget{}, err
	}
	if input.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
		return skill.RemoteTarget{}, &RemoteSkillTargetStorageError{}
	}
	if input.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt); err != nil {
		return skill.RemoteTarget{}, &RemoteSkillTargetStorageError{}
	}
	target, err := skill.NewRemoteTarget(input)
	if err != nil {
		return skill.RemoteTarget{}, &RemoteSkillTargetStorageError{}
	}
	return target, nil
}

func parseRemoteSkillTargetTime(value sql.NullString) (time.Time, error) {
	if !value.Valid {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value.String)
	if err != nil {
		return time.Time{}, &RemoteSkillTargetStorageError{}
	}
	return parsed, nil
}

func remoteSkillTargetArguments(target skill.RemoteTarget) []any {
	return []any{
		target.ScopeID(), target.ID(), target.DisplayName(), target.AgentKind(), target.InstallationScope(), target.DeliveryMode(), target.State(),
		nullableRemoteSkillTargetText(target.InstallationID()), nullableRemoteSkillTargetText(target.EnrollmentCodeDigest()),
		nullableRemoteSkillTargetTime(target.EnrollmentExpiresAt()), nullableRemoteSkillTargetText(target.CredentialSubject()),
		nullableRemoteSkillTargetText(target.CredentialVerifier()), nullableRemoteSkillTargetText(target.ReceiverVersion()),
		nullableRemoteSkillTargetText(target.EnvironmentFingerprint()), nullableRemoteSkillTargetText(target.MachineHostname()),
		nullableRemoteSkillTargetText(target.WorkspaceName()), nullableRemoteSkillTargetTime(target.LastSeenAt()),
		target.Generation(), target.CreatedAt().UTC().Format(time.RFC3339Nano), target.UpdatedAt().UTC().Format(time.RFC3339Nano),
	}
}

func nullableRemoteSkillTargetText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableRemoteSkillTargetTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.UTC().Format(time.RFC3339Nano)
}
