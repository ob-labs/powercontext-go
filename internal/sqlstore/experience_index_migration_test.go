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
	"path/filepath"
	"strings"
	"testing"
)

func TestSQLiteExperienceIndexUpgradesLegacyArtifactHeadsIdempotently(t *testing.T) {
	database, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if _, execErr := database.Exec(`CREATE TABLE pc_artifact_heads (
        scope_id VARCHAR(256) NOT NULL,
        family VARCHAR(128) NOT NULL,
        artifact_id VARCHAR(128) NOT NULL,
        revision INTEGER NOT NULL,
        PRIMARY KEY (scope_id, family, artifact_id)
	)`); execErr != nil {
		t.Fatal(execErr)
	}
	for range 2 {
		if migrationErr := EnsureArtifactHeadSearchableText(context.Background(), database, SQLiteDialect); migrationErr != nil {
			t.Fatal(migrationErr)
		}
	}
	rows, err := database.Query(`PRAGMA table_info('pc_artifact_heads')`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Errorf("close migrated artifact-head rows: %v", err)
		}
	}()
	count := 0
	for rows.Next() {
		var position, notNull, primaryKey int
		var name, kind string
		var defaultValue any
		if err := rows.Scan(&position, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		if name == "searchable_text" {
			count++
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("searchable_text columns = %d", count)
	}
}

func TestOceanBaseExperienceIndexUsesMediumTextMigration(t *testing.T) {
	query, migration, err := artifactHeadSearchableTextStatements(MySQLDialect)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(query, "information_schema.columns") ||
		migration != "ALTER TABLE pc_artifact_heads ADD COLUMN searchable_text MEDIUMTEXT NULL" {
		t.Fatalf("query = %q, migration = %q", query, migration)
	}
}
