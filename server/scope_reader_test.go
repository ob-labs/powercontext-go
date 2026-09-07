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

//go:build sqlite_fts5

package server

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v1 "github.com/ob-labs/powercontext-go/api/v1"
	"github.com/ob-labs/powercontext-go/internal/scope"
	"github.com/ob-labs/powercontext-go/internal/sqlstore"
)

func TestOpenApplicationAdmitsOnlyPersistedScopesBeforeSourceOrContextWork(t *testing.T) {
	config := applicationTestConfig(t)
	databasePath, err := SQLiteDSN(config.Database.SQLite.URL)
	if err != nil {
		t.Fatal(err)
	}
	application, err := OpenApplication(t.Context(), config, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if closeErr := application.Close(closeCtx); closeErr != nil {
			t.Error(closeErr)
		}
	})
	defaultScope := applicationDefaultScope(t, application)

	capture, err := application.Endpoint().CaptureContentSource(t.Context(), &v1.CaptureContentSourceRequest{
		ScopeID: defaultScope.ID(), SourceID: "default-scope-source", Content: "Scope admission is durable.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := capture.(*v1.CaptureContentSourceResponseHeaders); !ok {
		t.Fatalf("default Scope capture = %T", capture)
	}
	prepared, err := application.Endpoint().PrepareContext(t.Context(), &v1.PrepareContextRequest{
		ScopeID: defaultScope.ID(), Query: "scope admission",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := prepared.(*v1.PreparedContextHeaders); !ok {
		t.Fatalf("default Scope context = %T", prepared)
	}

	database, err := sql.Open("sqlite3", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	const unknownScope = "scope-not-registered"
	unknownCapture, err := application.Endpoint().CaptureContentSource(t.Context(), &v1.CaptureContentSourceRequest{
		ScopeID: unknownScope, SourceID: "must-not-persist", Content: "must not reach the Source backend",
	})
	var missing *scope.NotFoundError
	if unknownCapture != nil || !errors.As(err, &missing) {
		t.Fatalf("unknown Scope capture = %#v, %T %v; want typed absence", unknownCapture, err, err)
	}
	assertScopeHasNoPersistentWork(t, database, unknownScope)

	unknownContext, err := application.Endpoint().PrepareContext(t.Context(), &v1.PrepareContextRequest{
		ScopeID: unknownScope, Query: "must not reach the Memory backend",
	})
	if unknownContext != nil || !errors.As(err, &missing) {
		t.Fatalf("unknown Scope context = %#v, %T %v; want typed absence", unknownContext, err, err)
	}
	assertScopeHasNoPersistentWork(t, database, unknownScope)
}

func TestOpenApplicationKeepsBootstrappedDefaultScopeStableAcrossRestart(t *testing.T) {
	config := applicationTestConfig(t)
	config.Database.SQLite.URL = sqliteURL(filepath.Join(t.TempDir(), "powercontext.db"))

	first, err := OpenApplication(t.Context(), config, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	firstDefault := applicationDefaultScope(t, first)
	if firstDefault.ID() == "" || firstDefault.Version() != 1 {
		t.Fatalf("first default Scope = %#v", firstDefault)
	}
	if closeErr := first.Close(t.Context()); closeErr != nil {
		t.Fatal(closeErr)
	}

	second, err := OpenApplication(t.Context(), config, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeScopeReaderApplication(t, second) })
	secondDefault := applicationDefaultScope(t, second)
	if secondDefault.ID() != firstDefault.ID() || secondDefault.Version() != firstDefault.Version() {
		t.Fatalf("default Scope changed across restart: first=%#v second=%#v", firstDefault, secondDefault)
	}
}

func TestOpenApplicationScopeBootstrapFailureRollsBackAndAllowsRetry(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "powercontext.db")
	config := applicationTestConfig(t)
	config.Database.SQLite.URL = sqliteURL(databasePath)

	database, err := sqlstore.OpenSQLite(t.Context(), sqlstore.DefaultSQLiteConfig(databasePath))
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Transaction(t.Context(), func(tx sqlstore.DBTX) error {
		_, triggerErr := tx.ExecContext(t.Context(), `CREATE TRIGGER reject_default_scope
			BEFORE INSERT ON pc_scopes
			BEGIN
				SELECT RAISE(ABORT, 'default Scope bootstrap blocked');
			END`)
		return triggerErr
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(t.Context()); err != nil {
		t.Fatal(err)
	}

	application, err := OpenApplication(t.Context(), config, Dependencies{})
	if application != nil || err == nil {
		t.Fatalf("OpenApplication() = %#v, %v; want bootstrap failure", application, err)
	}
	if !strings.Contains(err.Error(), "default Scope bootstrap blocked") {
		t.Fatalf("bootstrap error = %v", err)
	}

	database, err = sqlstore.OpenSQLite(t.Context(), sqlstore.DefaultSQLiteConfig(databasePath))
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := database.Transaction(t.Context(), func(tx sqlstore.DBTX) error {
		if countErr := tx.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM pc_scopes").Scan(&count); countErr != nil {
			return countErr
		}
		_, dropErr := tx.ExecContext(t.Context(), "DROP TRIGGER reject_default_scope")
		return dropErr
	}); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("failed bootstrap committed %d Scope rows", count)
	}
	if err := database.Close(t.Context()); err != nil {
		t.Fatal(err)
	}

	application, err = OpenApplication(t.Context(), config, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeScopeReaderApplication(t, application) })
	if defaultScope := applicationDefaultScope(t, application); defaultScope.ID() == "" || defaultScope.Version() != 1 {
		t.Fatalf("retried default Scope = %#v", defaultScope)
	}
}

func assertScopeHasNoPersistentWork(t *testing.T, database *sql.DB, scopeID string) {
	t.Helper()
	for _, table := range []string{
		"pc_sources",
		"pc_source_journal_heads",
		"pc_artifacts",
		"pc_memory_entry_versions",
		"pc_memory_entry_heads",
	} {
		var count int
		if err := database.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+table+" WHERE scope_id = ?", scopeID).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("unknown Scope persisted %d rows in %s", count, table)
		}
	}
}

type persistedScope struct {
	id      string
	version int64
}

func (s persistedScope) ID() string     { return s.id }
func (s persistedScope) Version() int64 { return s.version }

func applicationDefaultScope(t *testing.T, application *Application) persistedScope {
	t.Helper()
	if application == nil {
		t.Fatal("OpenApplication returned a nil application")
	}
	databasePath, err := SQLiteDSN(application.config.Database.SQLite.URL)
	if err != nil {
		t.Fatal(err)
	}
	database, err := sql.Open("sqlite3", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	var value persistedScope
	queryErr := database.QueryRowContext(t.Context(), `SELECT s.scope_id, s.version
		FROM pc_scope_settings AS settings
		JOIN pc_scopes AS s ON s.scope_id = settings.scope_id
		WHERE settings.name = 'default'`).Scan(&value.id, &value.version)
	closeErr := database.Close()
	if queryErr != nil {
		t.Fatal(queryErr)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	return value
}

func closeScopeReaderApplication(t *testing.T, application *Application) {
	t.Helper()
	closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := application.Close(closeCtx); err != nil {
		t.Error(err)
	}
}
