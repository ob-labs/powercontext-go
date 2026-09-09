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
	"fmt"
)

const remoteSkillTargetTable = "pc_agent_skill_targets"

// EnsureSQLiteSkillDistributionSchema creates the SQLite-only durable target
// registry. It is intentionally outside the historical cross-dialect schema.
func EnsureSQLiteSkillDistributionSchema(ctx context.Context, db DBTX) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS pc_agent_skill_targets (
        scope_id VARCHAR(256) NOT NULL,
        target_id VARCHAR(64) NOT NULL,
        display_name VARCHAR(128) NOT NULL,
        agent_kind VARCHAR(16) NOT NULL,
        installation_scope VARCHAR(16) NOT NULL,
        delivery_mode VARCHAR(16) NOT NULL,
        state VARCHAR(16) NOT NULL,
        installation_id VARCHAR(128),
        enrollment_code_digest VARCHAR(64),
        enrollment_expires_at TEXT,
        credential_subject VARCHAR(128),
        credential_verifier VARCHAR(64),
        receiver_version VARCHAR(64),
        environment_fingerprint VARCHAR(64),
        machine_hostname VARCHAR(255),
        workspace_name VARCHAR(128),
        last_seen_at TEXT,
        generation INTEGER NOT NULL,
        created_at TEXT NOT NULL,
        updated_at TEXT NOT NULL,
        PRIMARY KEY (scope_id, target_id),
        CONSTRAINT fk_pc_agent_skill_targets_scope FOREIGN KEY (scope_id)
            REFERENCES pc_scopes (scope_id) ON DELETE RESTRICT,
        CONSTRAINT uq_pc_agent_skill_targets_enrollment_digest UNIQUE (enrollment_code_digest),
        CONSTRAINT uq_pc_agent_skill_targets_credential_subject UNIQUE (credential_subject),
        CONSTRAINT uq_pc_agent_skill_targets_credential_verifier UNIQUE (credential_verifier),
        CONSTRAINT uq_pc_agent_skill_targets_installation UNIQUE (
            scope_id, agent_kind, installation_scope, installation_id
        ),
        CONSTRAINT ck_pc_agent_skill_targets_agent_kind CHECK (agent_kind IN ('codex', 'workbuddy')),
        CONSTRAINT ck_pc_agent_skill_targets_installation_scope CHECK (installation_scope = 'project'),
        CONSTRAINT ck_pc_agent_skill_targets_delivery_mode CHECK (delivery_mode = 'agent_pull'),
        CONSTRAINT ck_pc_agent_skill_targets_generation_nonnegative CHECK (generation >= 0),
        CONSTRAINT ck_pc_agent_skill_targets_state_payload CHECK (
            (
                state = 'pending'
                AND length(enrollment_code_digest) = 64
                AND enrollment_code_digest NOT GLOB '*[^0-9a-f]*'
                AND length(enrollment_expires_at) > 0
                AND installation_id IS NULL
                AND credential_subject IS NULL
                AND credential_verifier IS NULL
                AND receiver_version IS NULL
                AND environment_fingerprint IS NULL
                AND machine_hostname IS NULL
                AND workspace_name IS NULL
                AND last_seen_at IS NULL
            ) OR (
                state = 'active'
                AND enrollment_code_digest IS NULL
                AND enrollment_expires_at IS NULL
                AND length(installation_id) > 0
                AND length(credential_subject) > 0
                AND length(credential_verifier) = 64
                AND credential_verifier NOT GLOB '*[^0-9a-f]*'
                AND length(receiver_version) > 0
                AND length(last_seen_at) > 0
            ) OR (
                state = 'revoked'
                AND enrollment_code_digest IS NULL
                AND enrollment_expires_at IS NULL
                AND credential_verifier IS NULL
                AND (
                    (installation_id IS NULL AND credential_subject IS NULL) OR
                    (length(installation_id) > 0 AND length(credential_subject) > 0)
                )
            )
        )
    )`); err != nil {
		return err
	}
	var objectType string
	if err := db.QueryRowContext(ctx, "SELECT type FROM sqlite_master WHERE name = ?", remoteSkillTargetTable).Scan(&objectType); err != nil {
		return fmt.Errorf("sqlstore: find SQLite schema object %q: %w", remoteSkillTargetTable, err)
	}
	if objectType != "table" {
		return fmt.Errorf("sqlstore: SQLite schema object %q must be a table", remoteSkillTargetTable)
	}
	return nil
}
