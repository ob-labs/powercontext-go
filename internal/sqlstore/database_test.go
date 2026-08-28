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
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ob-labs/powercontext-go/internal/sqlstore"
)

func TestSQLiteProfileCreatesMissingDatabaseDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "powercontext.db")
	database, err := sqlstore.OpenSQLite(context.Background(), sqlstore.DefaultSQLiteConfig(path))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close(context.Background()) })
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("database file = %#v, %v", info, err)
	}
}

func TestOpenSQLiteInitializesSchemaAndEveryConnection(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	config := sqlstore.DefaultSQLiteConfig(filepath.Join(t.TempDir(), "powercontext.db"))
	config.MaxOpenConns = 2
	config.MaxIdleConns = 2
	database, err := sqlstore.OpenSQLite(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})

	for _, name := range []string{
		"pc_sources",
		"pc_source_journal_heads",
		"pc_artifacts",
		"pc_artifact_heads",
		"pc_artifact_lineage_sources",
		"pc_artifact_lineage_artifacts",
		"pc_artifact_candidate_versions",
		"pc_artifact_candidate_heads",
		"pc_source_cursors",
		"pc_external_skill_registrations",
		"pc_memory_entry_versions",
		"pc_memory_entry_heads",
		"pc_model_usage_daily",
		"pc_recall_token_daily",
	} {
		var found string
		err := database.SQLDB().QueryRowContext(ctx,
			"SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?", name,
		).Scan(&found)
		if err != nil {
			t.Fatalf("schema %s: %v", name, err)
		}
	}

	first, err := database.SQLDB().Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := first.Close(); err != nil {
			t.Errorf("close first SQL connection: %v", err)
		}
	}()
	second, err := database.SQLDB().Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := second.Close(); err != nil {
			t.Errorf("close second SQL connection: %v", err)
		}
	}()
	for index, connection := range []*sql.Conn{first, second} {
		var foreignKeys, busyTimeout int
		if err := connection.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
			t.Fatal(err)
		}
		if err := connection.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
			t.Fatal(err)
		}
		if foreignKeys != 1 || busyTimeout != 5000 {
			t.Fatalf("connection %d pragmas = foreign_keys:%d busy_timeout:%d", index, foreignKeys, busyTimeout)
		}
	}
}

func TestDatabaseCloseRejectsNewTransactions(t *testing.T) {
	t.Parallel()
	config := sqlstore.DefaultSQLiteConfig(":memory:")
	database, err := sqlstore.OpenSQLite(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	err = database.Transaction(context.Background(), func(sqlstore.DBTX) error { return nil })
	var closed *sqlstore.DatabaseClosedError
	if !errors.As(err, &closed) {
		t.Fatalf("expected DatabaseClosedError, got %v", err)
	}
}

func TestDatabaseCanceledCloseRestoresAdmission(t *testing.T) {
	t.Parallel()
	config := sqlstore.DefaultSQLiteConfig(":memory:")
	database, err := sqlstore.OpenSQLite(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- database.Transaction(context.Background(), func(sqlstore.DBTX) error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if err := database.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline, got %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := database.Ping(context.Background()); err != nil {
		t.Fatalf("admission was not restored: %v", err)
	}
	if err := database.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}
