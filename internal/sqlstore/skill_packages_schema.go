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

var sqliteSkillPackageSchema = []string{
	`CREATE TABLE IF NOT EXISTS pc_skill_packages (
        scope_id VARCHAR(256) NOT NULL,
        tree_digest VARCHAR(64) NOT NULL,
        archive_digest VARCHAR(64) NOT NULL,
        file_count INTEGER NOT NULL,
        uncompressed_size INTEGER NOT NULL,
        archive_size INTEGER NOT NULL,
        archive BLOB NOT NULL,
        manifest BLOB NOT NULL,
        PRIMARY KEY (scope_id, tree_digest),
        CONSTRAINT ck_pc_skill_packages_file_count_positive CHECK (file_count > 0),
        CONSTRAINT ck_pc_skill_packages_uncompressed_size_nonnegative CHECK (uncompressed_size >= 0),
        CONSTRAINT ck_pc_skill_packages_archive_size_positive CHECK (archive_size > 0)
    )`,
}

// EnsureSQLiteSkillPackageSchema creates the Go-only managed Skill package
// store. It is deliberately outside the frozen cross-backend builtin schema.
func EnsureSQLiteSkillPackageSchema(ctx context.Context, db DBTX) error {
	for _, statement := range sqliteSkillPackageSchema {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	var objectType string
	if err := db.QueryRowContext(ctx, "SELECT type FROM sqlite_master WHERE name = ?", "pc_skill_packages").Scan(&objectType); err != nil {
		return fmt.Errorf("sqlstore: find SQLite schema object %q: %w", "pc_skill_packages", err)
	}
	if objectType != "table" {
		return fmt.Errorf("sqlstore: SQLite schema object %q must be a table", "pc_skill_packages")
	}
	return nil
}
