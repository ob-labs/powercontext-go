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
	"context"
	"database/sql"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	canonicalsource "github.com/ob-labs/powercontext-go/api/canonical/sources"
	v1 "github.com/ob-labs/powercontext-go/api/v1"
)

type sourceClientSecurity string

func (s sourceClientSecurity) BearerAuth(context.Context, canonicalsource.OperationName) (canonicalsource.BearerAuth, error) {
	return canonicalsource.BearerAuth{Token: string(s)}, nil
}

func sourceHTTPClient(t *testing.T, application *Application, token string) (*canonicalsource.Client, http.Handler) {
	t.Helper()
	handler, err := application.HTTPHandler()
	if err != nil {
		t.Fatal(err)
	}
	client, err := canonicalsource.NewClient("http://powercontext.test", sourceClientSecurity(token), canonicalsource.WithClient(&http.Client{Transport: scopeSidecarRoundTripper{handler: handler}}))
	if err != nil {
		t.Fatal(err)
	}
	return client, handler
}

func TestCanonicalSourceCreateReadAndSQLiteRestart(t *testing.T) {
	config := applicationTestConfig(t)
	config.Auth.Enabled = true
	config.Auth.Token = "source-resource-secret"
	application, err := OpenApplication(t.Context(), config, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeScopeReaderApplication(t, application) })
	scopeID := applicationDefaultScope(t, application).ID()
	client, _ := sourceHTTPClient(t, application, config.Auth.Token)
	legacy, err := application.Endpoint().CaptureContentSource(t.Context(), &v1.CaptureContentSourceRequest{ScopeID: scopeID, SourceID: "legacy-json-text", Content: `{"n":1}`})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := legacy.(*v1.CaptureContentSourceResponseHeaders); !ok {
		t.Fatalf("legacy = %T", legacy)
	}

	var created []canonicalsource.SourceRecord
	for _, content := range []string{`null`, `"null"`, `""`, `"  "`, `true`, `[1,false,null]`, `{"z":2,"a":1}`, `"\u00e9"`, `"e\u0301"`, `9007199254740991`, `9007199254740992.0`, `null`} {
		response, createErr := client.CreateSource(t.Context(), &canonicalsource.CreateSourceRequest{Content: []byte(content)}, canonicalsource.CreateSourceParams{ScopeID: scopeID})
		if createErr != nil {
			t.Fatalf("create %s: %v", content, createErr)
		}
		headers, ok := response.(*canonicalsource.SourceRecordHeaders)
		if !ok {
			t.Fatalf("create %s = %#v", content, response)
		}
		record := headers.Response
		if !regexp.MustCompile(`^src_[0-9a-f]{12}4[0-9a-f]{3}[89ab][0-9a-f]{15}$`).MatchString(record.SourceID) || record.ScopeID != scopeID || record.SourceType != canonicalsource.SourceRecordSourceTypeContent || record.Position != len(created)+2 {
			t.Fatalf("record = %#v", record)
		}
		if location, found := headers.Location.Get(); !found || location != "/v1/scopes/"+scopeID+"/sources/content/"+record.SourceID {
			t.Fatalf("location = %#v", headers.Location)
		}
		if id, found := headers.XPowerContextRequestID.Get(); !found || id == "" {
			t.Fatal("missing request ID")
		}
		var want, got any
		if decodeErr := json.Unmarshal([]byte(content), &want); decodeErr != nil {
			t.Fatal(decodeErr)
		}
		if decodeErr := json.Unmarshal(record.Content, &got); decodeErr != nil {
			t.Fatal(decodeErr)
		}
		wantJSON, _ := json.Marshal(want, json.Deterministic(true))
		gotJSON, _ := json.Marshal(got, json.Deterministic(true))
		if string(wantJSON) != string(gotJSON) {
			t.Fatalf("content = %s, want %s", gotJSON, wantJSON)
		}
		created = append(created, record)
	}
	if created[0].SourceID == created[len(created)-1].SourceID {
		t.Fatal("equivalent create reused identity")
	}
	if created[0].ContentDigest == created[1].ContentDigest || created[1].ContentDigest == created[2].ContentDigest || created[7].ContentDigest == created[8].ContentDigest {
		t.Fatal("distinct JSON values lost digest identity")
	}
	if closeErr := application.Close(t.Context()); closeErr != nil {
		t.Fatal(closeErr)
	}
	application, err = OpenApplication(t.Context(), config, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	client, _ = sourceHTTPClient(t, application, config.Auth.Token)
	for _, want := range created {
		response, getErr := client.GetSource(t.Context(), canonicalsource.GetSourceParams{ScopeID: scopeID, SourceType: canonicalsource.GetSourceSourceTypeContent, SourceID: want.SourceID})
		if getErr != nil {
			t.Fatal(getErr)
		}
		headers, ok := response.(*canonicalsource.GetSourceOKHeaders)
		if !ok || headers.Response.ContentDigest != want.ContentDigest || headers.Response.Position != want.Position || string(headers.Response.Content) != string(want.Content) {
			t.Fatalf("restarted source = %#v", response)
		}
	}
	legacyGet, err := client.GetSource(t.Context(), canonicalsource.GetSourceParams{ScopeID: scopeID, SourceType: canonicalsource.GetSourceSourceTypeContent, SourceID: "legacy-json-text"})
	if err != nil {
		t.Fatal(err)
	}
	if record, ok := legacyGet.(*canonicalsource.GetSourceOKHeaders); !ok || string(record.Response.Content) != `"{\"n\":1}"` || record.Response.Position != 1 {
		t.Fatalf("legacy get = %#v", legacyGet)
	}
	otherScope := createScopeForSidecar(t, application, "Other", "isolated Source partition", "")
	other, otherErr := client.GetSource(t.Context(), canonicalsource.GetSourceParams{ScopeID: otherScope.ID(), SourceType: canonicalsource.GetSourceSourceTypeContent, SourceID: created[0].SourceID})
	if otherErr != nil {
		t.Fatal(otherErr)
	}
	if _, ok := other.(*canonicalsource.NotFoundHeaders); !ok {
		t.Fatalf("cross-Scope source read = %T", other)
	}
	// A returned legal float must remain legal input; canonical digest text alone
	// would erase the float token and turn this replay into a rejected integer.
	if replay, replayErr := client.CreateSource(t.Context(), &canonicalsource.CreateSourceRequest{Content: created[10].Content}, canonicalsource.CreateSourceParams{ScopeID: scopeID}); replayErr != nil {
		t.Fatal(replayErr)
	} else if _, ok := replay.(*canonicalsource.SourceRecordHeaders); !ok {
		t.Fatalf("float replay = %T", replay)
	}
}

func TestCanonicalSourceHTTPAdmissionAndRedaction(t *testing.T) {
	var logs bytes.Buffer
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()), sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { _ = provider.Shutdown(t.Context()) })
	config := applicationTestConfig(t)
	config.Auth.Enabled = true
	config.Auth.Token = "source-admission-secret"
	config.Logging.Access = true
	application, err := OpenApplication(t.Context(), config, Dependencies{Logger: newJSONTestLogger(t, &logs), TracerProvider: provider})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeScopeReaderApplication(t, application) })
	scopeID := applicationDefaultScope(t, application).ID()
	client, handler := sourceHTTPClient(t, application, config.Auth.Token)
	databasePath, err := SQLiteDSN(config.Database.SQLite.URL)
	if err != nil {
		t.Fatal(err)
	}
	database, err := sql.Open("sqlite3", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := database.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	})
	if _, triggerErr := database.ExecContext(t.Context(), `CREATE TRIGGER refuse_resource BEFORE INSERT ON pc_sources BEGIN SELECT RAISE(ABORT, 'private-sql-diagnostic'); END`); triggerErr != nil {
		t.Fatal(triggerErr)
	}
	for _, tc := range []struct {
		name, method, path, body, token string
		status                          int
	}{
		{"auth", http.MethodPost, "/v1/scopes/" + scopeID + "/sources", `{"content":"private-payload"}`, "", 401},
		{"metadata", http.MethodPost, "/v1/scopes/" + scopeID + "/sources", `{"content":1,"metadata":{"token":"private-payload"}}`, config.Auth.Token, 422},
		{"identity", http.MethodPost, "/v1/scopes/" + scopeID + "/sources", `{"content":1,"source_id":"private-payload"}`, config.Auth.Token, 422},
		{"missing content", http.MethodPost, "/v1/scopes/" + scopeID + "/sources", `{}`, config.Auth.Token, 422},
		{"unsafe integer", http.MethodPost, "/v1/scopes/" + scopeID + "/sources", `{"content":9007199254740993}`, config.Auth.Token, 422},
		{"unsupported type", http.MethodPost, "/v1/scopes/" + scopeID + "/sources", `{"source_type":"private-payload","content":1}`, config.Auth.Token, 422},
		{"unknown scope create", http.MethodPost, "/v1/scopes/private-missing-scope/sources", `{"content":"private-payload"}`, config.Auth.Token, 404},
		{"unknown scope get", http.MethodGet, "/v1/scopes/private-missing-scope/sources/content/private-missing-source", "", config.Auth.Token, 404},
		{"missing source", http.MethodGet, "/v1/scopes/" + scopeID + "/sources/content/private-missing-source", "", config.Auth.Token, 404},
		{"wrong verb", http.MethodDelete, "/v1/scopes/" + scopeID + "/sources/content/private-missing-source", "", config.Auth.Token, 404},
		{"source list absent", http.MethodGet, "/v1/scopes/" + scopeID + "/sources", "", config.Auth.Token, 404},
		{"database failure", http.MethodPost, "/v1/scopes/" + scopeID + "/sources", `{"content":"private-payload"}`, config.Auth.Token, 500},
		{"body limit", http.MethodPost, "/v1/scopes/" + scopeID + "/sources", `{"content":"` + strings.Repeat("x", 32<<20) + `"}`, config.Auth.Token, 413},
	} {
		t.Run(tc.name, func(t *testing.T) {
			previousSpans := len(recorder.Ended())
			request := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Authorization", "Bearer "+tc.token)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", response.Code, tc.status, response.Body.String())
			}
			for _, private := range []string{"private-payload", "private-missing-source", "private-missing-scope", "private-sql-diagnostic", scopeID, config.Auth.Token, "9007199254740993"} {
				if strings.Contains(response.Body.String(), private) {
					t.Fatalf("response leaks protected value: %s", response.Body.String())
				}
				if strings.Contains(logs.String(), private) {
					t.Fatalf("logs leak protected value: %s", logs.String())
				}
				for _, span := range recorder.Ended()[previousSpans:] {
					if strings.Contains(fmt.Sprintf("%v %v %v", span.Attributes(), span.Events(), span.Status()), private) {
						t.Fatalf("span %q leaks protected value through attributes/events/status: %v %v %v", span.Name(), span.Attributes(), span.Events(), span.Status())
					}
				}
			}
			if tc.name == "unsupported type" || tc.name == "database failure" {
				transport := endedSpan(t, recorder.Ended()[previousSpans:], "HTTP create_source")
				foundFailure := false
				for _, event := range transport.Events() {
					if event.Name != "exception" {
						continue
					}
					attributes := attribute.NewSet(event.Attributes...)
					message, messageExists := attributes.Value("exception.message")
					class, classExists := attributes.Value("exception.type")
					foundFailure = messageExists && message.AsString() != "" && classExists && class.AsString() != ""
				}
				if !foundFailure {
					t.Fatal("failed Source request lost its exception event")
				}
			}
		})
	}
	// The pinned create_source schema lacks 404; preserve real Runtime admission.
	if _, createErr := client.CreateSource(t.Context(), &canonicalsource.CreateSourceRequest{Content: []byte(`null`)}, canonicalsource.CreateSourceParams{ScopeID: "unknown-scope"}); createErr == nil {
		t.Fatal("undeclared create 404 unexpectedly decoded as success")
	}
	assertScopeHasNoPersistentWork(t, database, "private-missing-scope")
	assertScopeHasNoPersistentWork(t, database, scopeID)
	var count int
	if err := database.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM pc_sources").Scan(&count); err != nil || count != 0 {
		t.Fatalf("rejected requests wrote Sources: count=%d, err=%v", count, err)
	}
}
