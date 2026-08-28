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
	"regexp"
	"strings"
)

var (
	cursorIdentifierPattern = regexp.MustCompile(`\bcursor\b`)
	varcharIdentityPattern  = regexp.MustCompile(`\bVARCHAR\(([0-9]+)\)`)
)

// quoteCursorIdentifier preserves the frozen column name while making SQL
// valid on OceanBase, where CURSOR is reserved. Backtick identifiers are also
// accepted by SQLite, so repositories can use one statement on both profiles.
func quoteCursorIdentifier(statement string) string {
	return cursorIdentifierPattern.ReplaceAllString(statement, "`cursor`")
}

// MySQLIdentityType is the byte-exact string type used for opaque identities
// by the Python MySQL/OceanBase schema. A database default such as
// utf8mb4_general_ci would otherwise collapse case- and accent-distinct keys.
func MySQLIdentityType(length int) string {
	return fmt.Sprintf("VARCHAR(%d) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin", length)
}

func mysqlIdentityColumns(statement string) string {
	return varcharIdentityPattern.ReplaceAllString(statement, "$0 CHARACTER SET utf8mb4 COLLATE utf8mb4_bin")
}

// builtinSchema is deliberately explicit. These names, columns, constraints,
// and key orders are the Python v0.0.1 on-disk contract; Go-only state must not
// be added to these tables.
var builtinSchema = []string{
	`CREATE TABLE IF NOT EXISTS pc_source_journal_heads (
        scope_id VARCHAR(256) NOT NULL,
        position BIGINT NOT NULL,
        PRIMARY KEY (scope_id),
        CONSTRAINT ck_pc_source_journal_heads_position_nonnegative CHECK (position >= 0)
    )`,
	`CREATE TABLE IF NOT EXISTS pc_sources (
        scope_id VARCHAR(256) NOT NULL,
        source_type VARCHAR(128) NOT NULL,
        source_id VARCHAR(256) NOT NULL,
        payload BLOB NOT NULL,
        journal_position BIGINT NOT NULL,
        PRIMARY KEY (scope_id, source_type, source_id),
        CONSTRAINT uq_pc_sources_scope_journal_position UNIQUE (scope_id, journal_position)
    )`,
	`CREATE TABLE IF NOT EXISTS pc_artifacts (
        scope_id VARCHAR(256) NOT NULL,
        family VARCHAR(128) NOT NULL,
        artifact_id VARCHAR(128) NOT NULL,
        revision INTEGER NOT NULL,
        content BLOB NOT NULL,
        PRIMARY KEY (scope_id, family, artifact_id, revision)
    )`,
	`CREATE TABLE IF NOT EXISTS pc_artifact_heads (
        scope_id VARCHAR(256) NOT NULL,
        family VARCHAR(128) NOT NULL,
        artifact_id VARCHAR(128) NOT NULL,
        revision INTEGER NOT NULL,
        searchable_text TEXT,
        PRIMARY KEY (scope_id, family, artifact_id),
        CONSTRAINT fk_pc_artifact_heads_revision FOREIGN KEY (scope_id, family, artifact_id, revision)
            REFERENCES pc_artifacts (scope_id, family, artifact_id, revision) ON DELETE RESTRICT,
        CONSTRAINT ck_pc_artifact_heads_revision_positive CHECK (revision > 0)
    )`,
	`CREATE TABLE IF NOT EXISTS pc_artifact_lineage_sources (
        scope_id VARCHAR(256) NOT NULL,
        family VARCHAR(128) NOT NULL,
        artifact_id VARCHAR(128) NOT NULL,
        revision INTEGER NOT NULL,
        ordinal INTEGER NOT NULL,
        source_type VARCHAR(128) NOT NULL,
        source_id VARCHAR(256) NOT NULL,
        PRIMARY KEY (scope_id, family, artifact_id, revision, ordinal),
        CONSTRAINT fk_pc_artifact_lineage_sources_artifact FOREIGN KEY (scope_id, family, artifact_id, revision)
            REFERENCES pc_artifacts (scope_id, family, artifact_id, revision) ON DELETE CASCADE,
        CONSTRAINT fk_pc_artifact_lineage_sources_source FOREIGN KEY (scope_id, source_type, source_id)
            REFERENCES pc_sources (scope_id, source_type, source_id) ON DELETE RESTRICT
    )`,
	`CREATE TABLE IF NOT EXISTS pc_artifact_lineage_artifacts (
        scope_id VARCHAR(256) NOT NULL,
        family VARCHAR(128) NOT NULL,
        artifact_id VARCHAR(128) NOT NULL,
        revision INTEGER NOT NULL,
        ordinal INTEGER NOT NULL,
        upstream_family VARCHAR(128) NOT NULL,
        upstream_artifact_id VARCHAR(128) NOT NULL,
        upstream_revision INTEGER NOT NULL,
        PRIMARY KEY (scope_id, family, artifact_id, revision, ordinal),
        CONSTRAINT fk_pc_artifact_lineage_artifacts_artifact FOREIGN KEY (scope_id, family, artifact_id, revision)
            REFERENCES pc_artifacts (scope_id, family, artifact_id, revision) ON DELETE CASCADE,
        CONSTRAINT fk_pc_artifact_lineage_artifacts_upstream FOREIGN KEY (
            scope_id, upstream_family, upstream_artifact_id, upstream_revision
        ) REFERENCES pc_artifacts (scope_id, family, artifact_id, revision) ON DELETE RESTRICT
    )`,
	`CREATE TABLE IF NOT EXISTS pc_artifact_candidate_versions (
        scope_id VARCHAR(256) NOT NULL,
        candidate_id VARCHAR(128) NOT NULL,
        version INTEGER NOT NULL,
        family VARCHAR(128) NOT NULL,
        proposal BLOB NOT NULL,
        source_refs BLOB NOT NULL,
        artifact_refs BLOB NOT NULL,
        target_family VARCHAR(128),
        target_artifact_id VARCHAR(128),
        target_revision INTEGER,
        reason TEXT,
        PRIMARY KEY (scope_id, candidate_id, version),
        CONSTRAINT fk_pc_artifact_candidate_versions_target FOREIGN KEY (
            scope_id, target_family, target_artifact_id, target_revision
        ) REFERENCES pc_artifacts (scope_id, family, artifact_id, revision) ON DELETE RESTRICT,
        CONSTRAINT ck_pc_artifact_candidate_versions_version_positive CHECK (version > 0),
        CONSTRAINT ck_pc_artifact_candidate_versions_target_complete CHECK (
            (target_family IS NULL AND target_artifact_id IS NULL AND target_revision IS NULL) OR
            (target_family IS NOT NULL AND target_artifact_id IS NOT NULL AND target_revision > 0)
        )
    )`,
	`CREATE TABLE IF NOT EXISTS pc_artifact_candidate_heads (
        scope_id VARCHAR(256) NOT NULL,
        candidate_id VARCHAR(128) NOT NULL,
        family VARCHAR(128) NOT NULL,
        version INTEGER NOT NULL,
        status VARCHAR(16) NOT NULL,
        result_family VARCHAR(128),
        result_artifact_id VARCHAR(128),
        result_revision INTEGER,
        decision_reason TEXT,
        PRIMARY KEY (scope_id, candidate_id),
        CONSTRAINT fk_pc_artifact_candidate_heads_version FOREIGN KEY (scope_id, candidate_id, version)
            REFERENCES pc_artifact_candidate_versions (scope_id, candidate_id, version) ON DELETE RESTRICT,
        CONSTRAINT fk_pc_artifact_candidate_heads_result FOREIGN KEY (
            scope_id, result_family, result_artifact_id, result_revision
        ) REFERENCES pc_artifacts (scope_id, family, artifact_id, revision) ON DELETE RESTRICT,
        CONSTRAINT ck_pc_artifact_candidate_heads_status CHECK (status IN ('pending', 'approved', 'rejected')),
        CONSTRAINT ck_pc_artifact_candidate_heads_terminal_result CHECK (
            (status = 'approved' AND result_family IS NOT NULL AND result_artifact_id IS NOT NULL
                AND result_revision > 0 AND decision_reason IS NULL) OR
            (status = 'rejected' AND result_family IS NULL AND result_artifact_id IS NULL
                AND result_revision IS NULL AND decision_reason IS NOT NULL) OR
            (status = 'pending' AND result_family IS NULL AND result_artifact_id IS NULL
                AND result_revision IS NULL AND decision_reason IS NULL)
        )
    )`,
	`CREATE TABLE IF NOT EXISTS pc_source_cursors (
        scope_id VARCHAR(256) NOT NULL,
        binding_name VARCHAR(128) NOT NULL,
        cursor BLOB NOT NULL,
        generation BIGINT NOT NULL,
        PRIMARY KEY (scope_id, binding_name),
        CONSTRAINT ck_pc_source_cursors_generation_nonnegative CHECK (generation >= 0)
    )`,
	`CREATE TABLE IF NOT EXISTS pc_external_skill_registrations (
        scope_id VARCHAR(256) NOT NULL,
        external_skill_id VARCHAR(128) NOT NULL,
        provider VARCHAR(128) NOT NULL,
        agent_kind VARCHAR(128) NOT NULL,
        host_id VARCHAR(128) NOT NULL,
        installation_scope VARCHAR(16) NOT NULL,
        locator VARCHAR(2000) NOT NULL,
        locator_hash VARCHAR(64) NOT NULL,
        fingerprint VARCHAR(64) NOT NULL,
        name VARCHAR(128) NOT NULL,
        description VARCHAR(2000) NOT NULL,
        PRIMARY KEY (scope_id, external_skill_id),
        CONSTRAINT uq_pc_external_skill_registrations_binding UNIQUE (
            scope_id, provider, host_id, installation_scope, locator_hash
        ),
        CONSTRAINT ck_pc_external_skill_registrations_scope CHECK (
            installation_scope IN ('user', 'project', 'plugin')
        )
    )`,
	`CREATE TABLE IF NOT EXISTS pc_memory_entry_versions (
        scope_id VARCHAR(256) NOT NULL,
        family VARCHAR(128) NOT NULL,
        memory_artifact_id VARCHAR(128) NOT NULL,
        entry_id VARCHAR(128) NOT NULL,
        entry_version_id VARCHAR(128) NOT NULL,
        version INTEGER NOT NULL,
        previous_version_id VARCHAR(128),
        kind VARCHAR(128) NOT NULL,
        text TEXT NOT NULL,
        source_refs BLOB NOT NULL,
        artifact_refs BLOB NOT NULL,
        entry_content_hash VARCHAR(64) NOT NULL,
        created_in_revision INTEGER NOT NULL,
        PRIMARY KEY (scope_id, memory_artifact_id, entry_version_id),
        CONSTRAINT uq_pc_memory_entry_versions_logical_version UNIQUE (
            scope_id, memory_artifact_id, entry_id, version
        ),
        CONSTRAINT uq_pc_memory_entry_versions_identity UNIQUE (
            scope_id, memory_artifact_id, entry_id, entry_version_id
        ),
        CONSTRAINT fk_pc_memory_entry_versions_artifact FOREIGN KEY (
            scope_id, family, memory_artifact_id, created_in_revision
        ) REFERENCES pc_artifacts (scope_id, family, artifact_id, revision) ON DELETE RESTRICT,
        CONSTRAINT ck_pc_memory_entry_versions_version_positive CHECK (version > 0),
        CONSTRAINT ck_pc_memory_entry_versions_revision_positive CHECK (created_in_revision > 0)
    )`,
	`CREATE TABLE IF NOT EXISTS pc_memory_entry_heads (
        scope_id VARCHAR(256) NOT NULL,
        family VARCHAR(128) NOT NULL,
        memory_artifact_id VARCHAR(128) NOT NULL,
        head_revision INTEGER NOT NULL,
        entry_id VARCHAR(128) NOT NULL,
        entry_version_id VARCHAR(128) NOT NULL,
        entry_content_hash VARCHAR(64) NOT NULL,
        searchable_text TEXT NOT NULL,
        PRIMARY KEY (scope_id, memory_artifact_id, entry_id),
        CONSTRAINT fk_pc_memory_entry_heads_artifact FOREIGN KEY (
            scope_id, family, memory_artifact_id, head_revision
        ) REFERENCES pc_artifacts (scope_id, family, artifact_id, revision) ON DELETE RESTRICT,
        CONSTRAINT fk_pc_memory_entry_heads_entry FOREIGN KEY (
            scope_id, memory_artifact_id, entry_id, entry_version_id
        ) REFERENCES pc_memory_entry_versions (
            scope_id, memory_artifact_id, entry_id, entry_version_id
        ) ON DELETE RESTRICT,
        CONSTRAINT ck_pc_memory_entry_heads_revision_positive CHECK (head_revision > 0)
    )`,
	`CREATE TABLE IF NOT EXISTS pc_model_usage_daily (
        scope_id VARCHAR(256) NOT NULL,
        usage_date DATE NOT NULL,
        purpose VARCHAR(64) NOT NULL,
        operation VARCHAR(16) NOT NULL,
        requests BIGINT NOT NULL,
        input_tokens BIGINT NOT NULL,
        output_tokens BIGINT NOT NULL,
        input_complete BOOLEAN NOT NULL,
        output_complete BOOLEAN NOT NULL,
        PRIMARY KEY (scope_id, usage_date, purpose, operation),
        CONSTRAINT ck_pc_model_usage_daily_operation CHECK (operation IN ('generation', 'embedding')),
        CONSTRAINT ck_pc_model_usage_daily_requests_nonnegative CHECK (requests >= 0),
        CONSTRAINT ck_pc_model_usage_daily_input_nonnegative CHECK (input_tokens >= 0),
        CONSTRAINT ck_pc_model_usage_daily_output_nonnegative CHECK (output_tokens >= 0)
    )`,
	`CREATE TABLE IF NOT EXISTS pc_recall_token_daily (
        scope_id VARCHAR(256) NOT NULL,
        usage_date DATE NOT NULL,
        estimator_id VARCHAR(128) NOT NULL,
        estimator_version VARCHAR(64) NOT NULL,
        preparations BIGINT NOT NULL,
        ready_preparations BIGINT NOT NULL,
        comparable_preparations BIGINT NOT NULL,
        baseline_tokens BIGINT NOT NULL,
        recalled_tokens BIGINT NOT NULL,
        PRIMARY KEY (scope_id, usage_date, estimator_id, estimator_version),
        CONSTRAINT ck_pc_recall_token_daily_preparations_nonnegative CHECK (preparations >= 0),
        CONSTRAINT ck_pc_recall_token_daily_ready_nonnegative CHECK (ready_preparations >= 0),
        CONSTRAINT ck_pc_recall_token_daily_comparable_nonnegative CHECK (comparable_preparations >= 0),
        CONSTRAINT ck_pc_recall_token_daily_comparable_ready CHECK (comparable_preparations <= ready_preparations),
        CONSTRAINT ck_pc_recall_token_daily_ready_total CHECK (ready_preparations <= preparations),
        CONSTRAINT ck_pc_recall_token_daily_baseline_nonnegative CHECK (baseline_tokens >= 0),
        CONSTRAINT ck_pc_recall_token_daily_recalled_nonnegative CHECK (recalled_tokens >= 0)
    )`,
}

// EnsureBuiltinSchema creates only absent Python-compatible core tables.
func EnsureBuiltinSchema(ctx context.Context, db DBTX) error {
	return EnsureBuiltinSchemaForDialect(ctx, db, SQLiteDialect)
}

// EnsureBuiltinSchemaForDialect creates the same logical SQLAlchemy schema
// using the payload/text variants emitted by the frozen Python MySQL dialect.
func EnsureBuiltinSchemaForDialect(ctx context.Context, db DBTX, dialect Dialect) error {
	if dialect != SQLiteDialect && dialect != MySQLDialect {
		return fmt.Errorf("sqlstore: unsupported schema dialect %q", dialect)
	}
	for _, original := range builtinSchema {
		statement := original
		if dialect == MySQLDialect {
			statement = strings.ReplaceAll(statement, " BLOB", " MEDIUMBLOB")
			statement = strings.ReplaceAll(statement, " TEXT", " MEDIUMTEXT")
			statement = mysqlIdentityColumns(statement)
			statement = quoteCursorIdentifier(statement)
		}
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
