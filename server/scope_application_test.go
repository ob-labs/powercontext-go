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
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ob-labs/powercontext-go/internal/sqlstore"
)

func TestOpenApplicationBootstrapsStableDefaultScopeAcrossRestart(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "powercontext.db")
	config := scopeApplicationTestConfig(t, databasePath)

	first, err := OpenApplication(t.Context(), config, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeScopeTestApplication(t, first) })
	if first.scopes == nil {
		t.Fatal("OpenApplication did not retain the Scope application")
	}
	firstDefault, err := first.scopes.Resolve(t.Context(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if firstDefault.ID() == "" || firstDefault.Title() != "Default" ||
		firstDefault.Summary() != "Default context" || firstDefault.Version() != 1 {
		t.Fatalf("first default Scope = %#v", firstDefault)
	}
	if closeErr := first.Close(t.Context()); closeErr != nil {
		t.Fatal(closeErr)
	}

	second, err := OpenApplication(t.Context(), config, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeScopeTestApplication(t, second) })
	secondDefault, err := second.scopes.Resolve(t.Context(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if secondDefault.ID() != firstDefault.ID() || secondDefault.Version() != firstDefault.Version() {
		t.Fatalf("default Scope changed across restart: first=%#v second=%#v", firstDefault, secondDefault)
	}
}

func TestOpenApplicationBootstrapFailureRollsBackAndAllowsRetry(t *testing.T) {
	testRoot := t.TempDir()
	databasePath := filepath.Join(testRoot, "powercontext.db")
	schedulerPath := filepath.Join(testRoot, "scheduler.db")
	config := scopeApplicationTestConfig(t, databasePath)
	interval := time.Hour
	config.Runtime.SourceWindowInterval = &interval
	config.SchedulerPath = schedulerPath
	config.Inference.GenerationModel = "openai:test-model"
	dependencies := Dependencies{
		MemoryCandidates:     noOpMemoryCandidates{},
		ExperienceCandidates: noOpExperienceCandidates{},
		ExperienceGenerator:  noOpExperienceGenerator{},
		SkillGenerator:       noOpSkillGenerator{},
		HandoffGenerator:     noOpHandoffGenerator{},
	}
	database := openScopeTestDatabase(t, databasePath)
	if transactionErr := database.Transaction(t.Context(), func(tx sqlstore.DBTX) error {
		_, createErr := tx.ExecContext(t.Context(), `CREATE TRIGGER reject_default_scope
			BEFORE INSERT ON pc_scopes
			BEGIN
				SELECT RAISE(ABORT, 'default Scope bootstrap blocked');
			END`)
		return createErr
	}); transactionErr != nil {
		t.Fatal(transactionErr)
	}
	if closeErr := database.Close(t.Context()); closeErr != nil {
		t.Fatal(closeErr)
	}

	application, err := OpenApplication(t.Context(), config, dependencies)
	if application != nil || err == nil {
		t.Fatalf("OpenApplication() = %#v, %v; want bootstrap failure", application, err)
	}
	if !strings.Contains(err.Error(), "default Scope bootstrap blocked") {
		t.Fatalf("OpenApplication error = %v; want bootstrap trigger failure", err)
	}
	if _, statErr := os.Stat(schedulerPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("bootstrap failure reached scheduler storage: %v", statErr)
	}

	database = openScopeTestDatabase(t, databasePath)
	var count int
	if transactionErr := database.Transaction(t.Context(), func(tx sqlstore.DBTX) error {
		if queryErr := tx.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM pc_scopes`).Scan(&count); queryErr != nil {
			return queryErr
		}
		_, dropErr := tx.ExecContext(t.Context(), `DROP TRIGGER reject_default_scope`)
		return dropErr
	}); transactionErr != nil {
		t.Fatal(transactionErr)
	}
	if count != 0 {
		t.Fatalf("failed bootstrap committed %d Scope rows", count)
	}
	if closeErr := database.Close(t.Context()); closeErr != nil {
		t.Fatal(closeErr)
	}

	config.Runtime.SourceWindowInterval = nil
	application, err = OpenApplication(t.Context(), config, dependencies)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeScopeTestApplication(t, application) })
	if _, resolveErr := application.scopes.Resolve(t.Context(), nil, nil); resolveErr != nil {
		t.Fatalf("resolve default Scope after retry: %v", resolveErr)
	}
}

func scopeApplicationTestConfig(t *testing.T, databasePath string) ProcessConfig {
	t.Helper()
	config, err := DefaultConfig()
	if err != nil {
		t.Fatal(err)
	}
	config.Database.SQLite.URL = sqliteURL(databasePath)
	config.MCP.Enabled = false
	config.Metrics.Enabled = false
	return config
}

func openScopeTestDatabase(t *testing.T, databasePath string) *sqlstore.Database {
	t.Helper()
	database, err := sqlstore.OpenSQLite(t.Context(), sqlstore.DefaultSQLiteConfig(databasePath))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close(context.Background()) })
	return database
}

func closeScopeTestApplication(t *testing.T, application *Application) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if closeErr := application.Close(ctx); closeErr != nil {
		t.Error(closeErr)
	}
}
