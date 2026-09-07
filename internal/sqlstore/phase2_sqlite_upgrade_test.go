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

package sqlstore_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/ob-labs/powercontext-go/internal/sqlstore"
)

const (
	legacySourcePayload   = `{"kind":"legacy-source"}`
	legacyArtifactContent = `{"kind":"legacy-artifact"}`
)

// These definitions are the Source and Artifact tables from the schema before
// Phase 2 added Scope, Definition, Observation acceptance, and checkpoint state.
var prePhase2SQLiteSchema = []string{
	`CREATE TABLE pc_source_journal_heads (
        scope_id VARCHAR(256) NOT NULL,
        position BIGINT NOT NULL,
        PRIMARY KEY (scope_id),
        CONSTRAINT ck_pc_source_journal_heads_position_nonnegative CHECK (position >= 0)
    )`,
	`CREATE TABLE pc_sources (
        scope_id VARCHAR(256) NOT NULL,
        source_type VARCHAR(128) NOT NULL,
        source_id VARCHAR(256) NOT NULL,
        payload BLOB NOT NULL,
        journal_position BIGINT NOT NULL,
        PRIMARY KEY (scope_id, source_type, source_id),
        CONSTRAINT uq_pc_sources_scope_journal_position UNIQUE (scope_id, journal_position)
    )`,
	`CREATE TABLE pc_artifacts (
        scope_id VARCHAR(256) NOT NULL,
        family VARCHAR(128) NOT NULL,
        artifact_id VARCHAR(128) NOT NULL,
        revision INTEGER NOT NULL,
        content BLOB NOT NULL,
        PRIMARY KEY (scope_id, family, artifact_id, revision)
    )`,
	`CREATE TABLE pc_artifact_heads (
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
}

func TestOpenSQLiteUpgradesPrePhase2DatabaseIdempotently(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pre-phase-2.db")
	seedPrePhase2SQLiteDatabase(t, path)

	database, err := sqlstore.OpenSQLite(t.Context(), sqlstore.DefaultSQLiteConfig(path))
	if err != nil {
		t.Fatal(err)
	}
	assertPhase2SQLiteTables(t, database.SQLDB())
	assertPrePhase2Data(t, database.SQLDB())
	if closeErr := database.Close(context.Background()); closeErr != nil {
		t.Fatal(closeErr)
	}

	reopened, err := sqlstore.OpenSQLite(t.Context(), sqlstore.DefaultSQLiteConfig(path))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close(context.Background()) })
	assertPhase2SQLiteTables(t, reopened.SQLDB())
	assertPrePhase2Data(t, reopened.SQLDB())
}

// Regression: SQLite accepts CREATE TABLE IF NOT EXISTS for a same-named view.
func TestOpenSQLitePhase2UpgradeFailureRollsBackSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pre-phase-2-failure.db")
	seedPrePhase2SQLiteDatabase(t, path)

	legacy, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, execErr := legacy.Exec(`CREATE VIEW pc_connector_checkpoints AS SELECT 1 AS checkpoint`); execErr != nil {
		t.Fatal(execErr)
	}
	if closeErr := legacy.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}

	if database, openErr := sqlstore.OpenSQLite(t.Context(), sqlstore.DefaultSQLiteConfig(path)); openErr == nil {
		_ = database.Close(context.Background())
		t.Fatal("OpenSQLite succeeded with a conflicting checkpoint view")
	}

	inspected, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = inspected.Close() })
	assertPrePhase2Data(t, inspected)
	for _, name := range []string{
		"pc_scopes",
		"pc_scope_context_references",
		"pc_scope_external_references",
		"pc_scope_creation_requests",
		"pc_scope_settings",
		"pc_scope_bindings",
		"pc_source_definition_manifests",
		"pc_source_observation_acceptances",
	} {
		assertSQLiteObjectMissing(t, inspected, name)
	}

	var objectType string
	if queryErr := inspected.QueryRowContext(t.Context(), `SELECT type FROM sqlite_master WHERE name = 'pc_connector_checkpoints'`).Scan(&objectType); queryErr != nil {
		t.Fatal(queryErr)
	}
	if objectType != "view" {
		t.Fatalf("checkpoint object type = %q, want view", objectType)
	}
}

func seedPrePhase2SQLiteDatabase(t *testing.T, path string) {
	t.Helper()
	legacy, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range prePhase2SQLiteSchema {
		if _, execErr := legacy.ExecContext(t.Context(), statement); execErr != nil {
			_ = legacy.Close()
			t.Fatal(execErr)
		}
	}
	for _, entry := range []struct {
		statement string
		arguments []any
	}{
		{
			statement: "INSERT INTO pc_source_journal_heads (scope_id, position) VALUES (?, ?)",
			arguments: []any{"legacy-scope", 1},
		},
		{
			statement: "INSERT INTO pc_sources (scope_id, source_type, source_id, payload, journal_position) VALUES (?, ?, ?, ?, ?)",
			arguments: []any{"legacy-scope", "legacy.source", "source-1", []byte(legacySourcePayload), 1},
		},
		{
			statement: "INSERT INTO pc_artifacts (scope_id, family, artifact_id, revision, content) VALUES (?, ?, ?, ?, ?)",
			arguments: []any{"legacy-scope", "memory", "artifact-1", 1, []byte(legacyArtifactContent)},
		},
		{
			statement: "INSERT INTO pc_artifact_heads (scope_id, family, artifact_id, revision, searchable_text) VALUES (?, ?, ?, ?, ?)",
			arguments: []any{"legacy-scope", "memory", "artifact-1", 1, "legacy artifact"},
		},
	} {
		if _, execErr := legacy.ExecContext(t.Context(), entry.statement, entry.arguments...); execErr != nil {
			_ = legacy.Close()
			t.Fatal(execErr)
		}
	}
	if closeErr := legacy.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
}

func assertPhase2SQLiteTables(t *testing.T, database *sql.DB) {
	t.Helper()
	for _, name := range []string{
		"pc_scopes",
		"pc_scope_context_references",
		"pc_scope_external_references",
		"pc_scope_creation_requests",
		"pc_scope_settings",
		"pc_scope_bindings",
		"pc_connector_checkpoints",
		"pc_source_definition_manifests",
		"pc_source_observation_acceptances",
	} {
		var objectType string
		if err := database.QueryRowContext(t.Context(), "SELECT type FROM sqlite_master WHERE name = ?", name).Scan(&objectType); err != nil {
			t.Fatalf("lookup %s: %v", name, err)
		}
		if objectType != "table" {
			t.Fatalf("%s object type = %q, want table", name, objectType)
		}
	}
}

func assertPrePhase2Data(t *testing.T, database *sql.DB) {
	t.Helper()
	var sourcePayload, artifactContent []byte
	if err := database.QueryRowContext(t.Context(), `SELECT payload FROM pc_sources
        WHERE scope_id = ? AND source_type = ? AND source_id = ?`, "legacy-scope", "legacy.source", "source-1").Scan(&sourcePayload); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRowContext(t.Context(), `SELECT content FROM pc_artifacts
        WHERE scope_id = ? AND family = ? AND artifact_id = ? AND revision = ?`, "legacy-scope", "memory", "artifact-1", 1).Scan(&artifactContent); err != nil {
		t.Fatal(err)
	}
	if string(sourcePayload) != legacySourcePayload {
		t.Fatalf("legacy source payload = %q", sourcePayload)
	}
	if string(artifactContent) != legacyArtifactContent {
		t.Fatalf("legacy artifact content = %q", artifactContent)
	}
}

func assertSQLiteObjectMissing(t *testing.T, database *sql.DB, name string) {
	t.Helper()
	var count int
	if err := database.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM sqlite_master WHERE name = ?", name).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("unexpected SQLite object %q after failed upgrade", name)
	}
}
