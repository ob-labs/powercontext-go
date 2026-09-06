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
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

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

func TestOpenApplicationMCPPersistsScopeBindingAndRequiresAuthentication(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "powercontext.db")
	config := scopeApplicationTestConfig(t, databasePath)
	config.MCP.Enabled = true
	config.Auth.Enabled = true
	config.Auth.Token = "scope-mcp-secret"

	first, err := OpenApplication(t.Context(), config, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	firstHandler, err := first.HTTPHandler()
	if err != nil {
		t.Fatal(err)
	}
	unauthorized := httptest.NewRecorder()
	firstHandler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodPost, "/mcp/", nil))
	if unauthorized.Code != http.StatusUnauthorized || unauthorized.Header().Get("WWW-Authenticate") != "Bearer" {
		t.Fatalf("unauthorized MCP = %d %#v", unauthorized.Code, unauthorized.Header())
	}
	defaultScope, err := first.scopes.Resolve(t.Context(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	firstClient := connectScopeMCP(t, firstHandler, config.Auth.Token)
	set, err := firstClient.CallTool(t.Context(), &mcp.CallToolParams{Name: "scope_binding_set", Arguments: map[string]any{
		"integration": "codex", "kind": "project", "external_id": "repository", "scope_id": defaultScope.ID(),
	}})
	if err != nil || set.IsError {
		t.Fatalf("set binding = %#v, %v", set, err)
	}
	set, err = firstClient.CallTool(t.Context(), &mcp.CallToolParams{Name: "scope_binding_set", Arguments: map[string]any{
		"integration": "workbuddy", "kind": "session", "external_id": "session-1", "scope_id": defaultScope.ID(),
	}})
	if err != nil || set.IsError {
		t.Fatalf("set WorkBuddy binding = %#v, %v", set, err)
	}
	if closeErr := first.Close(t.Context()); closeErr != nil {
		t.Fatal(closeErr)
	}

	second, err := OpenApplication(t.Context(), config, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeScopeTestApplication(t, second) })
	secondHandler, err := second.HTTPHandler()
	if err != nil {
		t.Fatal(err)
	}
	secondClient := connectScopeMCP(t, secondHandler, config.Auth.Token)
	for _, key := range []map[string]any{
		{"integration": "codex", "kind": "project", "external_id": "repository"},
		{"integration": "workbuddy", "kind": "session", "external_id": "session-1"},
	} {
		resolved, resolveErr := secondClient.CallTool(t.Context(), &mcp.CallToolParams{
			Name: "scope_binding_resolve", Arguments: map[string]any{"binding_keys": []any{key}},
		})
		if resolveErr != nil || resolved.IsError {
			t.Fatalf("resolve binding after restart = %#v, %v", resolved, resolveErr)
		}
		content := resolved.StructuredContent.(map[string]any)
		if content["scope_id"] != defaultScope.ID() {
			t.Fatalf("resolved scope_id = %#v, want %q", content["scope_id"], defaultScope.ID())
		}
	}
	thirdParty, err := secondClient.CallTool(t.Context(), &mcp.CallToolParams{Name: "scope_binding_set", Arguments: map[string]any{
		"integration": "claude-code", "kind": "session", "external_id": "session-1", "scope_id": defaultScope.ID(),
	}})
	if err != nil || !thirdParty.IsError {
		t.Fatalf("non-target binding result = %#v, %v", thirdParty, err)
	}
	workBuddyKey := map[string]any{"integration": "workbuddy", "kind": "session", "external_id": "session-1"}
	for _, want := range []bool{true, false} {
		cleared, clearErr := secondClient.CallTool(t.Context(), &mcp.CallToolParams{Name: "scope_binding_clear", Arguments: workBuddyKey})
		if clearErr != nil || cleared.IsError || cleared.StructuredContent.(map[string]any)["cleared"] != want {
			t.Fatalf("clear WorkBuddy binding = %#v, %v, want %t", cleared, clearErr, want)
		}
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

func connectScopeMCP(t *testing.T, handler http.Handler, token string) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "scope-binding-test", Version: "1"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint: "http://powercontext.test/mcp/",
		HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			request.Header.Set("Authorization", "Bearer "+token)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			return response.Result(), nil
		})},
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}
