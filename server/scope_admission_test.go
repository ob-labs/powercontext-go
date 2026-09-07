// Copyright (c) 2026 OceanBase.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
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
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

func TestOpenApplicationAdmitsOnlyDurableScopesBeforeHTTPWork(t *testing.T) {
	application, err := OpenApplication(t.Context(), applicationTestConfig(t), Dependencies{})
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
	handler, err := application.HTTPHandler()
	if err != nil {
		t.Fatal(err)
	}
	defaultScope := applicationDefaultScopeID(t, application)

	accepted := postApplicationJSON(t, handler, "/v1/sources/content", map[string]any{
		"scope_id": defaultScope, "source_id": "default-scope-source", "content": "Durable Scope admission succeeds.",
	})
	if accepted.Code != http.StatusAccepted {
		t.Fatalf("capture in default Scope = %d: %s", accepted.Code, accepted.Body.String())
	}

	databasePath, err := SQLiteDSN(application.config.Database.SQLite.URL)
	if err != nil {
		t.Fatal(err)
	}
	database, err := sql.Open("sqlite3", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	const unknownScope = "unregistered-scope"
	for _, request := range []struct {
		name, path   string
		body         map[string]any
		privateValue string
	}{
		{
			name: "capture", path: "/v1/sources/content",
			body:         map[string]any{"scope_id": unknownScope, "source_id": "must-not-persist", "content": "must not reach Source persistence"},
			privateValue: "must not reach Source persistence",
		},
		{
			name: "prepare", path: "/v1/context/prepare",
			body:         map[string]any{"scope_id": unknownScope, "query": "must not reach Memory persistence"},
			privateValue: "must not reach Memory persistence",
		},
	} {
		t.Run(request.name, func(t *testing.T) {
			response := postApplicationJSON(t, handler, request.path, request.body)
			if response.Code != http.StatusNotFound {
				t.Fatalf("%s status = %d: %s", request.name, response.Code, response.Body.String())
			}
			var envelope struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if decodeErr := json.Unmarshal(response.Body.Bytes(), &envelope); decodeErr != nil {
				t.Fatal(decodeErr)
			}
			if envelope.Error.Code != "scope_not_found" {
				t.Fatalf("%s error = %#v", request.name, envelope.Error)
			}
			for _, private := range []string{unknownScope, request.privateValue} {
				if private != "" && strings.Contains(response.Body.String(), private) {
					t.Fatalf("%s error exposed %q: %s", request.name, private, response.Body.String())
				}
			}
		})
	}
	assertNoScopePersistence(t, database, unknownScope)
}

func applicationDefaultScopeID(t *testing.T, application *Application) string {
	t.Helper()
	if application == nil || application.scopes == nil {
		t.Fatal("application does not expose Scope resolution")
	}
	descriptor, err := application.scopes.Resolve(t.Context(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return descriptor.ID()
}

func assertNoScopePersistence(t *testing.T, database *sql.DB, scopeID string) {
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
