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
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/mattn/go-sqlite3"

	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/artifact/skill"
	"github.com/ob-labs/powercontext-go/internal/sqlstore"
)

func TestSkillPackageRepositoryAddsAndGetsCanonicalSnapshot(t *testing.T) {
	t.Parallel()
	database := openTestDatabase(t)
	repository := sqlstore.SkillPackageRepository{}
	snapshot := skillPackageSnapshot(t, "canonical-skill")

	var added skill.PackageSnapshot
	if err := database.Transaction(t.Context(), func(tx sqlstore.DBTX) error {
		var addErr error
		added, addErr = repository.Add(t.Context(), tx, "scope-package-canonical", snapshot)
		return addErr
	}); err != nil {
		t.Fatal(err)
	}
	assertSameSkillPackageSnapshot(t, added, snapshot)

	var loaded skill.PackageSnapshot
	if err := database.Transaction(t.Context(), func(tx sqlstore.DBTX) error {
		var getErr error
		loaded, getErr = repository.Get(t.Context(), tx, "scope-package-canonical", snapshot.Reference())
		return getErr
	}); err != nil {
		t.Fatal(err)
	}
	assertSameSkillPackageSnapshot(t, loaded, snapshot)
	var artifactRows int
	if err := database.SQLDB().QueryRowContext(t.Context(), "SELECT COUNT(*) FROM pc_artifacts").Scan(&artifactRows); err != nil {
		t.Fatal(err)
	}
	if artifactRows != 0 {
		t.Fatalf("package persistence wrote %d artifact rows", artifactRows)
	}
}

func TestSkillPackageRepositoryMakesEqualAddIdempotent(t *testing.T) {
	database := openTestDatabase(t)
	repository := sqlstore.SkillPackageRepository{}
	snapshot := skillPackageSnapshot(t, "idempotent-skill")
	const scopeID = "scope-package-idempotent"
	if err := database.Transaction(t.Context(), func(tx sqlstore.DBTX) error {
		_, addErr := repository.Add(t.Context(), tx, scopeID, snapshot)
		return addErr
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.Transaction(t.Context(), func(tx sqlstore.DBTX) error {
		_, addErr := repository.Add(t.Context(), tx, scopeID, snapshot)
		return addErr
	}); err != nil {
		t.Fatalf("idempotent Add() = %v", err)
	}
	var count int
	if err := database.SQLDB().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM pc_skill_packages
        WHERE scope_id = ? AND tree_digest = ?`, scopeID, snapshot.Reference().TreeDigest()).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("stored package rows = %d, want 1", count)
	}
}

func TestSkillPackageRepositoryKeepsScopesIsolated(t *testing.T) {
	database := openTestDatabase(t)
	repository := sqlstore.SkillPackageRepository{}
	snapshot := skillPackageSnapshot(t, "isolated-skill")
	for _, scopeID := range []string{"scope-package-one", "scope-package-two"} {
		if err := database.Transaction(t.Context(), func(tx sqlstore.DBTX) error {
			_, addErr := repository.Add(t.Context(), tx, scopeID, snapshot)
			return addErr
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, scopeID := range []string{"scope-package-one", "scope-package-two"} {
		if err := database.Transaction(t.Context(), func(tx sqlstore.DBTX) error {
			loaded, getErr := repository.Get(t.Context(), tx, scopeID, snapshot.Reference())
			if getErr != nil {
				return getErr
			}
			assertSameSkillPackageSnapshot(t, loaded, snapshot)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	err := database.Transaction(t.Context(), func(tx sqlstore.DBTX) error {
		_, getErr := repository.Get(t.Context(), tx, "scope-package-missing-secret", snapshot.Reference())
		return getErr
	})
	if _, ok := errors.AsType[*sqlstore.SkillPackageNotFoundError](err); !ok {
		t.Fatalf("missing scoped package error = %T %v", err, err)
	}
	assertSkillPackageErrorRedacted(t, err, "scope-package-missing-secret", snapshot.Metadata().Name())
}

func TestSkillPackageRepositoryClassifiesStoredCorruption(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		column string
		value  any
	}{
		{name: "archive digest", column: "archive_digest", value: strings.Repeat("0", 64)},
		{name: "file count", column: "file_count", value: 999},
		{name: "manifest", column: "manifest", value: []byte(`[]`)},
		{name: "archive", column: "archive", value: []byte("stored-archive-secret")},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			database := openTestDatabase(t)
			repository := sqlstore.SkillPackageRepository{}
			snapshot := skillPackageSnapshot(t, "corrupt-skill")
			const scopeID = "scope-package-corruption-secret"
			if err := database.Transaction(t.Context(), func(tx sqlstore.DBTX) error {
				_, addErr := repository.Add(t.Context(), tx, scopeID, snapshot)
				return addErr
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := database.SQLDB().ExecContext(t.Context(), "UPDATE pc_skill_packages SET "+scenario.column+" = ? WHERE scope_id = ? AND tree_digest = ?",
				scenario.value, scopeID, snapshot.Reference().TreeDigest()); err != nil {
				t.Fatal(err)
			}
			err := database.Transaction(t.Context(), func(tx sqlstore.DBTX) error {
				_, getErr := repository.Get(t.Context(), tx, scopeID, snapshot.Reference())
				return getErr
			})
			if _, ok := errors.AsType[*sqlstore.InvalidStoredPayloadError](err); !ok {
				t.Fatalf("stored corruption error = %T %v", err, err)
			}
			assertSkillPackageErrorRedacted(t, err, scopeID, snapshot.Metadata().Name(), "stored-archive-secret")
		})
	}
}

func TestSkillPackageRepositoryConflictsOnInconsistentExistingSnapshot(t *testing.T) {
	database := openTestDatabase(t)
	repository := sqlstore.SkillPackageRepository{}
	snapshot := skillPackageSnapshot(t, "conflicting-skill")
	const scopeID = "scope-package-conflict-secret"
	if err := database.Transaction(t.Context(), func(tx sqlstore.DBTX) error {
		_, addErr := repository.Add(t.Context(), tx, scopeID, snapshot)
		return addErr
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.SQLDB().ExecContext(t.Context(), `UPDATE pc_skill_packages SET archive_digest = ?
        WHERE scope_id = ? AND tree_digest = ?`, strings.Repeat("0", 64), scopeID, snapshot.Reference().TreeDigest()); err != nil {
		t.Fatal(err)
	}
	err := database.Transaction(t.Context(), func(tx sqlstore.DBTX) error {
		_, addErr := repository.Add(t.Context(), tx, scopeID, snapshot)
		return addErr
	})
	if _, ok := errors.AsType[*sqlstore.SkillPackageConflictError](err); !ok {
		t.Fatalf("inconsistent package error = %T %v", err, err)
	}
	assertSkillPackageErrorRedacted(t, err, scopeID, snapshot.Metadata().Name())
}

func TestSkillPackageRepositoryConcurrentEqualAddIsIdempotent(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "skill-package-race.db")
	databases := []*sqlstore.Database{
		openSkillPackageDatabase(t, databasePath),
		openSkillPackageDatabase(t, databasePath),
	}
	repository := sqlstore.SkillPackageRepository{}
	snapshot := skillPackageSnapshot(t, "concurrent-skill")
	results := make([]error, len(databases))
	start := make(chan struct{})
	var waitGroup sync.WaitGroup
	for index, database := range databases {
		waitGroup.Go(func() {
			<-start
			results[index] = database.Transaction(t.Context(), func(tx sqlstore.DBTX) error {
				_, addErr := repository.Add(t.Context(), tx, "scope-package-concurrent", snapshot)
				return addErr
			})
		})
	}
	close(start)
	waitGroup.Wait()
	for index, err := range results {
		if err != nil {
			t.Fatalf("concurrent Add()[%d] = %T %v", index, err, err)
		}
	}
}

func TestSkillPackageRepositoryPreservesCancellation(t *testing.T) {
	database := openTestDatabase(t)
	repository := sqlstore.SkillPackageRepository{}
	snapshot := skillPackageSnapshot(t, "error-skill")
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	err := database.Transaction(t.Context(), func(tx sqlstore.DBTX) error {
		_, addErr := repository.Add(canceled, tx, "scope-package-canceled", snapshot)
		return addErr
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Add() = %T %v", err, err)
	}
	if _, conflict := errors.AsType[*sqlstore.SkillPackageConflictError](err); conflict {
		t.Fatalf("canceled Add() became a conflict: %v", err)
	}
	var count int
	if err := database.SQLDB().QueryRowContext(t.Context(), "SELECT COUNT(*) FROM pc_skill_packages").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("canceled Add() wrote %d rows", count)
	}
}

func TestSkillPackageRepositoryPreservesNonConstraintDatabaseError(t *testing.T) {
	database := openTestDatabase(t)
	repository := sqlstore.SkillPackageRepository{}
	snapshot := skillPackageSnapshot(t, "database-error-skill")
	if _, err := database.SQLDB().ExecContext(t.Context(), "DROP TABLE pc_skill_packages"); err != nil {
		t.Fatal(err)
	}
	err := database.Transaction(t.Context(), func(tx sqlstore.DBTX) error {
		_, addErr := repository.Add(t.Context(), tx, "scope-package-db-error", snapshot)
		return addErr
	})
	if _, ok := errors.AsType[sqlite3.Error](err); !ok {
		t.Fatalf("database Add() error = %T %v", err, err)
	}
	if _, conflict := errors.AsType[*sqlstore.SkillPackageConflictError](err); conflict {
		t.Fatalf("database Add() became a conflict: %v", err)
	}
}

func TestLegacyArtifactCodecRejectsPackageContentBeforeWrite(t *testing.T) {
	database := openTestDatabase(t)
	snapshot := skillPackageSnapshot(t, "v2-package-skill")
	packageContent, err := skill.NewPackageContent(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	packageDraft, err := artifact.NewDraft(skill.Family, packageContent, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := sqlstore.NewArtifactRepository(sqlstore.SQLiteDialect, sqlstore.SkillArtifactCodec())
	if err != nil {
		t.Fatal(err)
	}
	err = database.Transaction(t.Context(), func(tx sqlstore.DBTX) error {
		_, createErr := legacy.Create(t.Context(), tx, "scope-package-codec", "legacy-artifact", packageDraft)
		return createErr
	})
	if _, ok := errors.AsType[*artifact.FamilyMismatchError](err); !ok {
		t.Fatalf("legacy codec package write = %T %v", err, err)
	}
	var artifactRows int
	if err := database.SQLDB().QueryRowContext(t.Context(), "SELECT COUNT(*) FROM pc_artifacts").Scan(&artifactRows); err != nil {
		t.Fatal(err)
	}
	if artifactRows != 0 {
		t.Fatalf("package-backed value wrote %d legacy artifact rows", artifactRows)
	}
}

func TestSkillPackageSchemaIsNotInTheMySQLBuiltinContract(t *testing.T) {
	recorder := &skillPackageSchemaRecorder{}
	if err := sqlstore.EnsureBuiltinSchemaForDialect(t.Context(), recorder, sqlstore.MySQLDialect); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(recorder.statements, "\n"), "pc_skill_packages") {
		t.Fatal("managed Skill package schema entered the historical MySQL contract")
	}
}

func TestOpenSQLiteRejectsSkillPackageViewWithoutPartialSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "skill-package-view.db")
	seed, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		"CREATE TABLE prephase_skill_package_sentinel (value TEXT NOT NULL)",
		"CREATE VIEW pc_skill_packages AS SELECT value FROM prephase_skill_package_sentinel",
	} {
		if _, execErr := seed.ExecContext(t.Context(), statement); execErr != nil {
			t.Fatal(execErr)
		}
	}
	if closeErr := seed.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}

	database, openErr := sqlstore.OpenSQLite(t.Context(), sqlstore.DefaultSQLiteConfig(path))
	if database != nil {
		_ = database.Close(context.Background())
		t.Fatal("OpenSQLite returned a database for a Skill package view conflict")
	}
	if openErr == nil {
		t.Fatal("OpenSQLite accepted a Skill package view where a table is required")
	}

	verify, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = verify.Close() })
	expected := map[string]string{
		"prephase_skill_package_sentinel": "table",
		"pc_skill_packages":               "view",
	}
	for name, wantType := range expected {
		gotType, found, lookupErr := skillPackageSchemaObject(t.Context(), verify, name)
		if lookupErr != nil || !found || gotType != wantType {
			t.Fatalf("schema object %q = %q, found=%t, err=%v; want %q", name, gotType, found, lookupErr, wantType)
		}
	}
	for _, name := range []string{"pc_scopes", "pc_artifacts"} {
		if gotType, found, lookupErr := skillPackageSchemaObject(t.Context(), verify, name); lookupErr != nil || found {
			t.Fatalf("failed SQLite initialization left %q (%q, found=%t, err=%v)", name, gotType, found, lookupErr)
		}
	}
}

type skillPackageSchemaRecorder struct{ statements []string }

func (r *skillPackageSchemaRecorder) ExecContext(_ context.Context, query string, _ ...any) (sql.Result, error) {
	r.statements = append(r.statements, query)
	return driver.RowsAffected(0), nil
}

func (*skillPackageSchemaRecorder) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	panic("unexpected QueryContext")
}

func (*skillPackageSchemaRecorder) QueryRowContext(context.Context, string, ...any) *sql.Row {
	panic("unexpected QueryRowContext")
}

func skillPackageSnapshot(t *testing.T, name string) skill.PackageSnapshot {
	t.Helper()
	root := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(filepath.Join(root, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte("---\nname: "+name+"\ndescription: Persist this verified package.\n---\n\nUse the package contents.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "scripts", "verify.sh"), []byte("#!/bin/sh\nprintf verified\\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(root, "scripts", "verify.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	snapshot, err := skill.CapturePackageDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func assertSameSkillPackageSnapshot(t *testing.T, got, want skill.PackageSnapshot) {
	t.Helper()
	if got.Reference() != want.Reference() || !bytes.Equal(got.Archive(), want.Archive()) || !bytes.Equal(got.Manifest(), want.Manifest()) || got.Metadata().Name() != want.Metadata().Name() {
		t.Fatalf("package snapshot differs: got=%v want=%v", got.Reference(), want.Reference())
	}
}

func assertSkillPackageErrorRedacted(t *testing.T, err error, values ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, value := range values {
		if value != "" && strings.Contains(err.Error(), value) {
			t.Fatalf("error disclosed package data %q: %v", value, err)
		}
	}
}

func skillPackageSchemaObject(ctx context.Context, db *sql.DB, name string) (string, bool, error) {
	var objectType string
	err := db.QueryRowContext(ctx, "SELECT type FROM sqlite_master WHERE name = ?", name).Scan(&objectType)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return objectType, true, nil
}

func openSkillPackageDatabase(t *testing.T, path string) *sqlstore.Database {
	t.Helper()
	database, err := sqlstore.OpenSQLite(t.Context(), sqlstore.DefaultSQLiteConfig(path))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := database.Close(context.Background()); closeErr != nil {
			t.Error(closeErr)
		}
	})
	return database
}
