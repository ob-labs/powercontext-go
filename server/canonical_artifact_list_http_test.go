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
	"bytes"
	"database/sql"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	canonicalartifact "github.com/ob-labs/powercontext-go/api/canonical/artifacts"
)

func TestCanonicalArtifactListPaginationAndRestart(t *testing.T) {
	config := applicationTestConfig(t)
	application, err := OpenApplication(t.Context(), config, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeScopeReaderApplication(t, application) })
	scopeID := applicationDefaultScope(t, application).ID()
	otherScope := createScopeForSidecar(t, application, "Other", "list isolation", "")
	database := artifactTestDatabase(t, config)
	ids := []string{"z", "a", "_", "Z", "A", "-"}
	for i := range 96 {
		ids = append(ids, fmt.Sprintf("item-%03d", i))
	}
	for _, id := range ids {
		seedArtifactListExperience(t, database, scopeID, id, 1)
	}
	seedArtifactListExperience(t, database, otherScope.ID(), "other-scope-only", 1)
	slices.Sort(ids)
	client, _ := artifactHTTPClient(t, application, "")
	for _, limit := range []int{0, 1, 100} {
		params := canonicalartifact.ListArtifactsParams{ScopeID: scopeID, Family: canonicalartifact.ListArtifactsFamilyExperience}
		wantCount := limit
		if limit == 0 {
			wantCount = 50
		} else {
			params.Limit = canonicalartifact.NewOptInt(limit)
		}
		page := artifactListPage(t, client, params)
		if len(page.Items) != wantCount || page.NextCursor.IsNull() {
			t.Fatalf("limit %d: items=%d cursor null=%t", limit, len(page.Items), page.NextCursor.IsNull())
		}
		for i, item := range page.Items {
			if item.ArtifactID != ids[i] || item.ScopeID != scopeID || item.Revision != 1 || item.Family != canonicalartifact.BaseArtifactFamilyExperience {
				t.Fatalf("wrong list order, scope, family or head at %d: %#v", i, item)
			}
		}
	}
	params := canonicalartifact.ListArtifactsParams{ScopeID: scopeID, Family: canonicalartifact.ListArtifactsFamilyExperience, Limit: canonicalartifact.NewOptInt(1)}
	first := artifactListPage(t, client, params)
	seedArtifactListExperience(t, database, scopeID, ids[1], 2)
	if closeErr := application.Close(t.Context()); closeErr != nil {
		t.Fatal(closeErr)
	}
	application, err = OpenApplication(t.Context(), config, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	client, handler := artifactHTTPClient(t, application, "")
	params.Cursor = canonicalartifact.NewOptString(first.NextCursor.Value)
	params.Limit = canonicalartifact.NewOptInt(100)
	second := artifactListPage(t, client, params)
	if len(second.Items) != 100 || second.Items[0].ArtifactID != ids[1] || second.Items[0].Revision != 2 || second.NextCursor.IsNull() {
		t.Fatal("restart continuation did not observe the current head with changed page size")
	}
	params.Cursor = canonicalartifact.NewOptString(second.NextCursor.Value)
	last := artifactListPage(t, client, params)
	if len(last.Items) != 1 || last.Items[0].ArtifactID != ids[len(ids)-1] || !last.NextCursor.IsNull() {
		t.Fatal("last page duplicated or omitted an artifact")
	}
	for _, partition := range []struct{ scope, family string }{{scopeID, "skill"}, {scopeID, "memory"}, {scopeID, "handoff"}, {otherScope.ID(), "skill"}} {
		request := httptest.NewRequest(http.MethodGet, "/v1/scopes/"+partition.scope+"/artifacts/"+partition.family, nil)
		request.Header.Set("If-None-Match", `"revision:1"`)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != 200 || strings.TrimSpace(response.Body.String()) != `{"items":[],"next_cursor":null}` || response.Header().Get("ETag") != "" || response.Header().Get("X-PowerContext-Request-ID") == "" {
			t.Fatalf("empty partition/caching = %d %s", response.Code, response.Body.String())
		}
	}
	isolated := artifactListPage(t, client, canonicalartifact.ListArtifactsParams{ScopeID: otherScope.ID(), Family: canonicalartifact.ListArtifactsFamilyExperience})
	if len(isolated.Items) != 1 || isolated.Items[0].ArtifactID != "other-scope-only" {
		t.Fatal("list crossed the Scope boundary")
	}
}

func TestCanonicalArtifactListPolicyAndRedaction(t *testing.T) {
	var logs bytes.Buffer
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()), sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { _ = provider.Shutdown(t.Context()) })
	config := applicationTestConfig(t)
	config.Auth.Enabled = true
	config.Auth.Token = "private-list-bearer"
	config.Logging.Access = true
	now := time.Date(2026, time.September, 8, 0, 0, 0, 0, time.UTC)
	application, err := OpenApplication(t.Context(), config, Dependencies{Clock: func() time.Time { return now }, Logger: newJSONTestLogger(t, &logs), TracerProvider: provider})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeScopeReaderApplication(t, application) })
	scopeID := applicationDefaultScope(t, application).ID()
	otherScope := createScopeForSidecar(t, application, "Other", "list replay", "")
	database := artifactTestDatabase(t, config)
	for _, id := range []string{"private-list-a", "private-list-b"} {
		seedArtifactListExperience(t, database, scopeID, id, 1)
	}
	client, handler := artifactHTTPClient(t, application, config.Auth.Token)
	params := canonicalartifact.ListArtifactsParams{ScopeID: scopeID, Family: canonicalartifact.ListArtifactsFamilyExperience, Limit: canonicalartifact.NewOptInt(1)}
	page := artifactListPage(t, client, params)
	cursor := page.NextCursor.Value
	if cursor == "" {
		t.Fatal("fixture did not produce a cursor")
	}
	base := "/v1/scopes/" + scopeID + "/artifacts/experience"
	for _, tc := range []struct {
		name, path, token, code string
		status                  int
	}{
		{"auth", base, "", "unauthorized", 401},
		{"unknown Scope", "/v1/scopes/private-unknown-scope/artifacts/experience", config.Auth.Token, "scope_not_found", 404},
		{"empty", base + "?cursor=", config.Auth.Token, "invalid_request", 422},
		{"malformed", base + "?cursor=private-cursor", config.Auth.Token, "invalid_cursor", 400},
		{"overlong", base + "?cursor=" + strings.Repeat("x", 8200), config.Auth.Token, "invalid_request", 422},
		{"tampered", base + "?cursor=" + url.QueryEscape("x"+cursor[1:]), config.Auth.Token, "invalid_cursor", 400},
		{"scope replay", "/v1/scopes/" + otherScope.ID() + "/artifacts/experience?cursor=" + url.QueryEscape(cursor), config.Auth.Token, "invalid_cursor", 400},
		{"family replay", "/v1/scopes/" + scopeID + "/artifacts/skill?cursor=" + url.QueryEscape(cursor), config.Auth.Token, "invalid_cursor", 400},
		{"limit zero", base + "?limit=0", config.Auth.Token, "invalid_request", 422},
		{"limit 101", base + "?limit=101", config.Auth.Token, "invalid_request", 422},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := artifactListRequest(handler, tc.path, tc.token)
			if response.Code != tc.status || !strings.Contains(response.Body.String(), tc.code) || response.Header().Get("X-PowerContext-Request-ID") == "" {
				t.Fatalf("policy = %d %s", response.Code, response.Body.String())
			}
			for _, protected := range []string{scopeID, otherScope.ID(), config.Auth.Token, cursor, "private-cursor", "private-unknown-scope", "private-list-content"} {
				if strings.Contains(response.Body.String(), protected) {
					t.Fatal("error response exposed a protected value")
				}
			}
		})
	}
	now = now.Add(time.Hour + time.Second)
	params.Cursor = canonicalartifact.NewOptString(cursor)
	response, err := client.ListArtifacts(t.Context(), params)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := response.(*canonicalartifact.CursorExpiredHeaders); !ok {
		t.Fatalf("expired cursor = %T", response)
	}
	if closeErr := application.Close(t.Context()); closeErr != nil {
		t.Fatal(closeErr)
	}
	closed := artifactListRequest(handler, base, config.Auth.Token)
	if closed.Code != 503 || !strings.Contains(closed.Body.String(), "runtime_not_ready") {
		t.Fatalf("closed Runtime = %d %s", closed.Code, closed.Body.String())
	}
	dsn, err := SQLiteDSN(config.Database.SQLite.URL)
	if err != nil {
		t.Fatal(err)
	}
	key, err := os.ReadFile(filepath.Join(filepath.Dir(dsn), "."+filepath.Base(dsn)+".cursor-key"))
	if err != nil {
		t.Fatal(err)
	}
	endedSpan(t, recorder.Ended(), "HTTP list_artifacts")
	for _, protected := range []string{scopeID, otherScope.ID(), config.Auth.Token, cursor, dsn, string(key), "private-list-content", "private-cursor"} {
		if strings.Contains(logs.String(), protected) {
			t.Fatal("list logs exposed a protected value")
		}
		for _, span := range recorder.Ended() {
			if strings.Contains(fmt.Sprintf("%v %v %v", span.Attributes(), span.Events(), span.Status()), protected) {
				t.Fatal("list trace exposed a protected value")
			}
		}
	}
	var count int
	if err := database.QueryRowContext(t.Context(), "SELECT count(*) FROM pc_artifacts").Scan(&count); err != nil || count != 2 {
		t.Fatalf("list modified persisted artifacts: count=%d err=%v", count, err)
	}
}

func seedArtifactListExperience(t *testing.T, database *sql.DB, scopeID, id string, revision int) {
	t.Helper()
	content := []byte(`{"situation":"private-list-content","action":"act","outcome":"done","lesson":"learn"}`)
	if _, err := database.ExecContext(t.Context(), `INSERT INTO pc_artifacts(scope_id,family,artifact_id,revision,content) VALUES (?, 'experience', ?, ?, ?)`, scopeID, id, revision, content); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(t.Context(), `INSERT INTO pc_artifact_heads(scope_id,family,artifact_id,revision) VALUES (?, 'experience', ?, ?) ON CONFLICT(scope_id,family,artifact_id) DO UPDATE SET revision = excluded.revision`, scopeID, id, revision); err != nil {
		t.Fatal(err)
	}
}

func artifactListPage(t *testing.T, client *canonicalartifact.Client, params canonicalartifact.ListArtifactsParams) canonicalartifact.ArtifactPage {
	t.Helper()
	response, err := client.ListArtifacts(t.Context(), params)
	if err != nil {
		t.Fatal(err)
	}
	headers, ok := response.(*canonicalartifact.ArtifactPageHeaders)
	if !ok || headers.XPowerContextRequestID.Value == "" {
		t.Fatalf("list response = %T or missing request ID", response)
	}
	encoded, err := json.Marshal(headers.Response)
	if err != nil {
		t.Fatal(err)
	}
	for _, prohibited := range []string{`"content":`, `"package_ref"`, `"archive"`, `"instructions"`, "private-list-content"} {
		if strings.Contains(string(encoded), prohibited) {
			t.Fatal("list contains private content or package storage")
		}
	}
	return headers.Response
}

func artifactListRequest(handler http.Handler, path, token string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func assertArtifactCollectionMatchesRevision(t *testing.T, client *canonicalartifact.Client, record canonicalartifact.ArtifactRevision) {
	t.Helper()
	page := artifactListPage(t, client, canonicalartifact.ListArtifactsParams{ScopeID: record.ScopeID, Family: canonicalartifact.ListArtifactsFamily(record.Family)})
	if len(page.Items) != 1 || !page.NextCursor.IsNull() {
		t.Fatal("single-head collection has an incorrect length or cursor")
	}
	item := page.Items[0]
	if item.ArtifactID != record.ArtifactID || item.ScopeID != record.ScopeID || item.Family != record.Family || item.Revision != record.Revision || item.ContentDigest != record.ContentDigest || !slices.Equal(item.Sources, record.Sources) || !slices.Equal(item.Artifacts, record.Artifacts) {
		t.Fatal("collection lost current head identity, public content digest or ordered lineage")
	}
}

func TestCanonicalArtifactCursorKeyStartup(t *testing.T) {
	t.Run("new parent directory", func(t *testing.T) {
		config := applicationTestConfig(t)
		config.Database.SQLite.URL = sqliteURL(filepath.Join(t.TempDir(), "new-parent", "private-database.db"))
		application, err := OpenApplication(t.Context(), config, Dependencies{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { closeScopeReaderApplication(t, application) })
		client, _ := artifactHTTPClient(t, application, "")
		page := artifactListPage(t, client, canonicalartifact.ListArtifactsParams{ScopeID: applicationDefaultScope(t, application).ID(), Family: canonicalartifact.ListArtifactsFamilySkill})
		if len(page.Items) != 0 || !page.NextCursor.IsNull() {
			t.Fatal("fresh SQLite database did not return an empty list")
		}
	})
	t.Run("corrupt existing key", func(t *testing.T) {
		config := applicationTestConfig(t)
		dsn, err := SQLiteDSN(config.Database.SQLite.URL)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(filepath.Dir(dsn), "."+filepath.Base(dsn)+".cursor-key")
		original := []byte("private-corrupt-key")
		if writeErr := os.WriteFile(path, original, 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}
		application, openErr := OpenApplication(t.Context(), config, Dependencies{})
		if application != nil || openErr == nil {
			t.Fatal("invalid key did not prevent Server startup")
		}
		for _, protected := range []string{dsn, path, config.Database.SQLite.URL, string(original)} {
			if strings.Contains(fmt.Sprintf("%v %+v %#v", openErr, openErr, openErr), protected) {
				t.Fatal("startup error exposed key material or path")
			}
		}
		stored, readErr := os.ReadFile(path)
		if readErr != nil || !bytes.Equal(stored, original) {
			t.Fatal("startup replaced corrupt cursor key")
		}
	})
}

func TestCanonicalArtifactListRemainsHTTPOnly(t *testing.T) {
	config := applicationTestConfig(t)
	config.MCP.Enabled = true
	application, err := OpenApplication(t.Context(), config, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeScopeReaderApplication(t, application) })
	_, handler := artifactHTTPClient(t, application, "")
	client := mcp.NewClient(&mcp.Implementation{Name: "artifact-list-test", Version: "1"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint:             "http://powercontext.test" + strings.TrimRight(config.MCP.Path, "/") + "/",
		HTTPClient:           &http.Client{Transport: scopeSidecarRoundTripper{handler: handler}},
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	tools, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) == 0 {
		t.Fatal("MCP inventory is unexpectedly empty")
	}
	for _, tool := range tools.Tools {
		if slices.Contains([]string{"list_artifacts", "get_artifact", "get_artifact_revision", "create_artifact", "replace_artifact"}, tool.Name) {
			t.Fatalf("canonical-only operation became an MCP tool: %s", tool.Name)
		}
	}
}
