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
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	canonicalartifact "github.com/ob-labs/powercontext-go/api/canonical/artifacts"
	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/artifact/skill"
	"github.com/ob-labs/powercontext-go/internal/sqlstore"
)

type artifactClientSecurity string

func (s artifactClientSecurity) BearerAuth(context.Context, canonicalartifact.OperationName) (canonicalartifact.BearerAuth, error) {
	return canonicalartifact.BearerAuth{Token: string(s)}, nil
}

func TestCanonicalArtifactAccessLogsAttributeGeneratedOperations(t *testing.T) {
	var logs bytes.Buffer
	config := applicationTestConfig(t)
	config.Logging.Access = true
	config.Auth.Enabled = true
	config.Auth.Token = "private-access-token"
	application, err := OpenApplication(t.Context(), config, Dependencies{Logger: newJSONTestLogger(t, &logs)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeScopeReaderApplication(t, application) })
	scopeID := applicationDefaultScope(t, application).ID()
	database := artifactTestDatabase(t, config)
	content := `{"situation":"private-access-content","action":"act","outcome":"done","lesson":"learn"}`
	if _, execErr := database.ExecContext(t.Context(), `INSERT INTO pc_artifacts(scope_id,family,artifact_id,revision,content) VALUES (?, 'experience', 'private-access-artifact', 1, ?)`, scopeID, []byte(content)); execErr != nil {
		t.Fatal(execErr)
	}
	if _, execErr := database.ExecContext(t.Context(), `INSERT INTO pc_artifact_heads(scope_id,family,artifact_id,revision) VALUES (?, 'experience', 'private-access-artifact', 1)`, scopeID); execErr != nil {
		t.Fatal(execErr)
	}
	_, handler := artifactHTTPClient(t, application, config.Auth.Token)
	for _, tc := range []struct{ suffix, operation string }{
		{"", "get_artifact"}, {"/revisions/1", "get_artifact_revision"},
	} {
		t.Run(tc.operation, func(t *testing.T) {
			logs.Reset()
			request := httptest.NewRequest(http.MethodGet, "/v1/scopes/"+scopeID+"/artifacts/experience/private-access-artifact"+tc.suffix, nil)
			request.Header.Set("Authorization", "Bearer "+config.Auth.Token)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", response.Code, response.Body.String())
			}
			for _, protected := range []string{scopeID, "private-access-artifact", "private-access-content", config.Auth.Token} {
				if strings.Contains(logs.String(), protected) {
					t.Fatal("access log exposed a protected value")
				}
			}
			count := 0
			for line := range strings.SplitSeq(strings.TrimSpace(logs.String()), "\n") {
				var event struct {
					Event     string `json:"event"`
					Operation string `json:"operation"`
					RequestID string `json:"request_id"`
					Status    int    `json:"status_code"`
				}
				if decodeErr := json.Unmarshal([]byte(line), &event); decodeErr != nil {
					t.Fatal(decodeErr)
				}
				if event.Event != "transport.request.completed" {
					continue
				}
				count++
				if event.Operation != tc.operation || event.RequestID == "" || event.RequestID != response.Header().Get("X-PowerContext-Request-ID") || event.Status != http.StatusOK {
					t.Fatalf("access event = %#v, want operation %s with response request ID and status 200", event, tc.operation)
				}
			}
			if count != 1 {
				t.Fatalf("access event count = %d, want 1", count)
			}
		})
	}
}

func TestCanonicalArtifactHTTPAdmissionErrorsAndNoWrites(t *testing.T) {
	var logs bytes.Buffer
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()), sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { _ = provider.Shutdown(t.Context()) })
	config := applicationTestConfig(t)
	config.Auth.Enabled = true
	config.Auth.Token = "artifact-admission-secret"
	config.Logging.Access = true
	application, err := OpenApplication(t.Context(), config, Dependencies{Logger: newJSONTestLogger(t, &logs), TracerProvider: provider})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeScopeReaderApplication(t, application) })
	scopeID := applicationDefaultScope(t, application).ID()
	database := artifactTestDatabase(t, config)
	if _, execErr := database.ExecContext(t.Context(), `INSERT INTO pc_artifacts(scope_id,family,artifact_id,revision,content) VALUES (?, 'experience', 'private-corrupt', 1, 'private-payload')`, scopeID); execErr != nil {
		t.Fatal(execErr)
	}
	if _, execErr := database.ExecContext(t.Context(), `INSERT INTO pc_artifact_heads(scope_id,family,artifact_id,revision) VALUES (?, 'experience', 'private-corrupt', 1)`, scopeID); execErr != nil {
		t.Fatal(execErr)
	}
	_, handler := artifactHTTPClient(t, application, config.Auth.Token)
	base := "/v1/scopes/" + scopeID + "/artifacts/experience"
	for _, tc := range []struct {
		name, method, path, body, token string
		status                          int
	}{
		{"authentication", http.MethodGet, base + "/private-corrupt", "", "", 401},
		{"unknown Scope", http.MethodGet, "/v1/scopes/private-missing-scope/artifacts/experience/private-missing-artifact", "", config.Auth.Token, 404},
		{"unknown Artifact", http.MethodGet, base + "/private-missing-artifact", "", config.Auth.Token, 404},
		{"unknown revision", http.MethodGet, base + "/private-corrupt/revisions/999", "", config.Auth.Token, 404},
		{"invalid revision", http.MethodGet, base + "/private-corrupt/revisions/0", "", config.Auth.Token, 422},
		{"unsupported family", http.MethodGet, "/v1/scopes/" + scopeID + "/artifacts/private-family/private-corrupt", "", config.Auth.Token, 422},
		{"corrupt content", http.MethodGet, base + "/private-corrupt", "", config.Auth.Token, 500},
		{"historical corrupt content", http.MethodGet, base + "/private-corrupt/revisions/1", "", config.Auth.Token, 500},
		{"unimplemented list", http.MethodGet, base, "", config.Auth.Token, 404},
		{"unimplemented create", http.MethodPost, "/v1/scopes/" + scopeID + "/artifacts", `{}`, config.Auth.Token, 404},
		{"unimplemented replace", http.MethodPut, base + "/private-corrupt", `{}`, config.Auth.Token, 404},
		{"body limit", http.MethodGet, base + "/private-corrupt", `{"value":"` + strings.Repeat("x", 32<<20) + `"}`, config.Auth.Token, 413},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			request.Header.Set("Authorization", "Bearer "+tc.token)
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != tc.status {
				t.Fatalf("status=%d want=%d body=%s", response.Code, tc.status, response.Body.String())
			}
			if response.Header().Get("X-PowerContext-Request-ID") == "" {
				t.Fatal("request ID missing")
			}
			for _, protected := range []string{scopeID, config.Auth.Token, "private-corrupt", "private-payload", "private-missing-scope", "private-missing-artifact", "private-family"} {
				if strings.Contains(response.Body.String(), protected) || strings.Contains(logs.String(), protected) {
					t.Fatalf("protected value leaked in HTTP/logs")
				}
				for _, span := range recorder.Ended() {
					if strings.Contains(fmt.Sprintf("%v %v %v", span.Attributes(), span.Events(), span.Status()), protected) {
						t.Fatalf("protected value leaked in tracing")
					}
				}
			}
		})
	}
	var count int
	endedSpan(t, recorder.Ended(), "HTTP get_artifact")
	endedSpan(t, recorder.Ended(), "HTTP get_artifact_revision")
	if operationErr := database.QueryRowContext(t.Context(), "SELECT count(*) FROM pc_artifacts").Scan(&count); operationErr != nil {
		t.Fatal(operationErr)
	}
	if count != 1 {
		t.Fatalf("GET/write refusals changed artifacts: %d", count)
	}
	if closeErr := application.Close(t.Context()); closeErr != nil {
		t.Fatal(closeErr)
	}
	request := httptest.NewRequest(http.MethodGet, base+"/private-corrupt", nil)
	request.Header.Set("Authorization", "Bearer "+config.Auth.Token)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 503 || !strings.Contains(response.Body.String(), "runtime_not_ready") {
		t.Fatalf("closed Runtime = %d %s", response.Code, response.Body.String())
	}
}

func TestCanonicalArtifactPackageSkillRehydratesAndRejectsCorruption(t *testing.T) {
	config := applicationTestConfig(t)
	application, err := OpenApplication(t.Context(), config, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeScopeReaderApplication(t, application) })
	scopeID := applicationDefaultScope(t, application).ID()
	database := artifactTestDatabase(t, config)
	attached, err := sqlstore.Attach(database)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := sqlstore.NewArtifactRepository(sqlstore.SQLiteDialect, sqlstore.SkillArtifactCodec())
	if err != nil {
		t.Fatal(err)
	}
	var current artifact.Snapshot
	var snapshots []skill.PackageSnapshot
	for _, word := range []string{"first", "second"} {
		root := filepath.Join(t.TempDir(), "read-skill")
		if mkdirErr := os.Mkdir(root, 0o700); mkdirErr != nil {
			t.Fatal(mkdirErr)
		}
		manifest := "---\nname: read-skill\ndescription: Package description\nlicense: Apache-2.0\ncompatibility: local\nmetadata:\n  author: tester\nallowed-tools: Read\n---\n" + word + "\n"
		if writeErr := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte(manifest), 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}
		snapshot, captureErr := skill.CapturePackageDirectory(root)
		if captureErr != nil {
			t.Fatal(captureErr)
		}
		snapshots = append(snapshots, snapshot)
		content, contentErr := skill.NewPackageContent(snapshot)
		if contentErr != nil {
			t.Fatal(contentErr)
		}
		draft, draftErr := skill.NewPackageDraft(content, nil, nil)
		if draftErr != nil {
			t.Fatal(draftErr)
		}
		if transactionErr := attached.Transaction(t.Context(), func(tx sqlstore.DBTX) error {
			if _, addErr := (sqlstore.SkillPackageRepository{}).Add(t.Context(), tx, scopeID, snapshot); addErr != nil {
				return addErr
			}
			var persistErr error
			if current == nil {
				current, persistErr = repository.Create(t.Context(), tx, scopeID, "package-read", draft)
			} else {
				current, persistErr = repository.Revise(t.Context(), tx, scopeID, current, draft)
			}
			return persistErr
		}); transactionErr != nil {
			t.Fatal(transactionErr)
		}
	}
	if closeErr := application.Close(t.Context()); closeErr != nil {
		t.Fatal(closeErr)
	}
	application, err = OpenApplication(t.Context(), config, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	client, handler := artifactHTTPClient(t, application, "")
	for index, word := range []string{"first", "second"} {
		response, getErr := client.GetArtifactRevision(t.Context(), canonicalartifact.GetArtifactRevisionParams{ScopeID: scopeID, Family: canonicalartifact.GetArtifactRevisionFamilySkill, ArtifactID: "package-read", Revision: index + 1})
		if getErr != nil {
			t.Fatal(getErr)
		}
		headers, ok := response.(*canonicalartifact.GetArtifactRevisionOKHeaders)
		if !ok {
			t.Fatalf("package revision = %#v", response)
		}
		encoded, marshalErr := json.Marshal(headers.Response.Content)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		var content struct {
			Name          string            `json:"name"`
			Description   string            `json:"description"`
			Instructions  string            `json:"instructions"`
			Validation    []string          `json:"validation"`
			License       string            `json:"license"`
			Compatibility string            `json:"compatibility"`
			Metadata      map[string]string `json:"metadata"`
			AllowedTools  string            `json:"allowed_tools"`
			Package       struct {
				TreeDigest       string `json:"tree_digest"`
				ArchiveDigest    string `json:"archive_digest"`
				FileCount        int    `json:"file_count"`
				UncompressedSize int    `json:"uncompressed_size"`
				ArchiveSize      int    `json:"archive_size"`
			} `json:"package"`
		}
		if decodeErr := json.Unmarshal(encoded, &content); decodeErr != nil {
			t.Fatal(decodeErr)
		}
		ref := snapshots[index].Reference()
		if content.Name != "read-skill" || content.Description != "Package description" || content.Instructions != word || len(content.Validation) != 0 || content.License != "Apache-2.0" || content.Compatibility != "local" || content.Metadata["author"] != "tester" || content.AllowedTools != "Read" || content.Package.TreeDigest != ref.TreeDigest() || content.Package.ArchiveDigest != ref.ArchiveDigest() || content.Package.FileCount != ref.FileCount() || content.Package.UncompressedSize != ref.UncompressedSize() || content.Package.ArchiveSize != ref.ArchiveSize() {
			t.Fatalf("package content = %s", encoded)
		}
		if strings.Contains(string(encoded), "package_ref") || strings.Contains(string(encoded), "SKILL.md") {
			t.Fatalf("package storage exposed: %s", encoded)
		}
		canonicalContent := fmt.Sprintf(`{"allowed_tools":"Read","compatibility":"local","description":"Package description","instructions":"%s","license":"Apache-2.0","metadata":{"author":"tester"},"name":"read-skill","package":{"archive_digest":"%s","archive_size":%d,"file_count":%d,"tree_digest":"%s","uncompressed_size":%d},"validation":[]}`, word, ref.ArchiveDigest(), ref.ArchiveSize(), ref.FileCount(), ref.TreeDigest(), ref.UncompressedSize())
		digest := sha256.Sum256([]byte(canonicalContent))
		if headers.Response.ContentDigest != "sha256:"+hex.EncodeToString(digest[:]) {
			t.Fatalf("package digest = %s", headers.Response.ContentDigest)
		}
		if index == 1 {
			headResponse, headErr := client.GetArtifact(t.Context(), canonicalartifact.GetArtifactParams{ScopeID: scopeID, Family: canonicalartifact.GetArtifactFamilySkill, ArtifactID: "package-read"})
			if headErr != nil {
				t.Fatal(headErr)
			}
			head, headOK := headResponse.(*canonicalartifact.ArtifactRevisionHeaders)
			if !headOK || head.Response.Revision != 2 || head.Response.ContentDigest != headers.Response.ContentDigest {
				t.Fatalf("package head = %#v", headResponse)
			}
		}
	}
	for _, mutation := range []string{
		`UPDATE pc_skill_packages SET archive = x'707269766174652d7061636b6167652d636f7272757074696f6e'`,
		`DELETE FROM pc_skill_packages`,
	} {
		if _, execErr := database.ExecContext(t.Context(), mutation); execErr != nil {
			t.Fatal(execErr)
		}
		request := httptest.NewRequest(http.MethodGet, "/v1/scopes/"+scopeID+"/artifacts/skill/package-read", nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != 500 || strings.Contains(response.Body.String(), "private-package") || strings.Contains(response.Body.String(), "package-read") || strings.Contains(response.Body.String(), scopeID) {
			t.Fatalf("package failure = %d %s", response.Code, response.Body.String())
		}
	}
}

func artifactHTTPClient(t *testing.T, application *Application, token string) (*canonicalartifact.Client, http.Handler) {
	t.Helper()
	handler, err := application.HTTPHandler()
	if err != nil {
		t.Fatal(err)
	}
	client, err := canonicalartifact.NewClient("http://powercontext.test", artifactClientSecurity(token), canonicalartifact.WithClient(&http.Client{Transport: scopeSidecarRoundTripper{handler: handler}}))
	if err != nil {
		t.Fatal(err)
	}
	return client, handler
}

func artifactTestDatabase(t *testing.T, config ProcessConfig) *sql.DB {
	t.Helper()
	dsn, err := SQLiteDSN(config.Database.SQLite.URL)
	if err != nil {
		t.Fatal(err)
	}
	database, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(1)
	if _, execErr := database.ExecContext(t.Context(), "PRAGMA foreign_keys = ON"); execErr != nil {
		t.Fatal(execErr)
	}
	t.Cleanup(func() {
		if closeErr := database.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	})
	return database
}

func TestCanonicalArtifactFourFamiliesLineageAndRestart(t *testing.T) {
	config := applicationTestConfig(t)
	config.Auth.Enabled = true
	config.Auth.Token = "artifact-reader-secret"
	application, err := OpenApplication(t.Context(), config, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeScopeReaderApplication(t, application) })
	scopeID := applicationDefaultScope(t, application).ID()
	database := artifactTestDatabase(t, config)
	for ordinal, sourceType := range []string{"accepted-observation", "content", "external-skill-snapshot"} {
		if _, execErr := database.ExecContext(t.Context(), `INSERT INTO pc_sources(scope_id,source_type,source_id,payload,journal_position) VALUES (?, ?, ?, '{}', ?)`, scopeID, sourceType, sourceType+"-source", ordinal+1); execErr != nil {
			t.Fatal(execErr)
		}
	}
	if _, execErr := database.ExecContext(t.Context(), `INSERT INTO pc_source_observation_acceptances(scope_id,source_type,source_id) VALUES (?, 'accepted-observation', 'accepted-observation-source')`, scopeID); execErr != nil {
		t.Fatal(execErr)
	}
	fixtures := []struct{ family, content string }{
		{"experience", `{"action":"act","lesson":"%s","outcome":"done","situation":"context"}`},
		{"skill", `{"allowed_tools":null,"compatibility":null,"description":"Describe","instructions":"%s","license":null,"metadata":{},"name":"read-skill","package":null,"validation":["tested","repeated"]}`},
		{"memory", `{"changes":[{"entry_id":"entry","from_entry_version_id":null,"op":"add","reason":"%s","to_entry_version_id":"version"}],"manifest":{"entries":[{"entry_content_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","entry_id":"entry","entry_version_id":"version","state":"active"}],"format":"flat-v1"},"schema":"powercontext.memory.v1"}`},
		{"handoff", `{"disposition":"complete","next_action":null,"objective":"%s","omissions":[{"citation":null,"text":"omitted"}],"schema":"powercontext.handoff.v1","state":[{"citations":[{"kind":"source","source_ref":{"source_id":"content-source","source_type":"content"}}],"text":"recorded"}]}`},
	}
	for _, fixture := range fixtures {
		for index, word := range []string{"\u00e9", "e\u0301"} {
			if fixture.family == "memory" {
				word = []string{"first", "second"}[index]
			}
			stored := fmt.Sprintf(fixture.content, word)
			if fixture.family == "skill" {
				stored = fmt.Sprintf(`{"description":"Describe","instructions":"%s","name":"read-skill","validation":["tested","repeated"]}`, word)
			}
			if _, execErr := database.ExecContext(t.Context(), `INSERT INTO pc_artifacts(scope_id,family,artifact_id,revision,content) VALUES (?, ?, 'read', ?, ?)`, scopeID, fixture.family, index+1, []byte(stored)); execErr != nil {
				t.Fatal(execErr)
			}
			for ordinal, sourceType := range []string{"accepted-observation", "content", "external-skill-snapshot"} {
				if _, execErr := database.ExecContext(t.Context(), `INSERT INTO pc_artifact_lineage_sources(scope_id,family,artifact_id,revision,ordinal,source_type,source_id) VALUES (?, ?, 'read', ?, ?, ?, ?)`, scopeID, fixture.family, index+1, ordinal, sourceType, sourceType+"-source"); execErr != nil {
					t.Fatal(execErr)
				}
			}
		}
		if _, execErr := database.ExecContext(t.Context(), `INSERT INTO pc_artifact_heads(scope_id,family,artifact_id,revision) VALUES (?, ?, 'read', 2)`, scopeID, fixture.family); execErr != nil {
			t.Fatal(execErr)
		}
	}
	for ordinal, revision := range []int{2, 1} {
		for _, childRevision := range []int{1, 2} {
			if _, execErr := database.ExecContext(t.Context(), `INSERT INTO pc_artifact_lineage_artifacts(scope_id,family,artifact_id,revision,ordinal,upstream_family,upstream_artifact_id,upstream_revision) VALUES (?, 'skill', 'read', ?, ?, 'experience', 'read', ?)`, scopeID, childRevision, ordinal, revision); execErr != nil {
				t.Fatal(execErr)
			}
		}
	}
	verify := func(t *testing.T) {
		client, _ := artifactHTTPClient(t, application, config.Auth.Token)
		for _, fixture := range fixtures {
			t.Run(fixture.family, func(t *testing.T) {
				for _, version := range []struct {
					revision int
					word     string
				}{{1, "\u00e9"}, {2, "e\u0301"}} {
					if fixture.family == "memory" {
						version.word = []string{"first", "second"}[version.revision-1]
					}
					var record canonicalartifact.ArtifactRevision
					if version.revision == 2 {
						response, getErr := client.GetArtifact(t.Context(), canonicalartifact.GetArtifactParams{ScopeID: scopeID, Family: canonicalartifact.GetArtifactFamily(fixture.family), ArtifactID: "read"})
						if getErr != nil {
							t.Fatal(getErr)
						}
						headers, ok := response.(*canonicalartifact.ArtifactRevisionHeaders)
						if !ok {
							t.Fatalf("head response = %#v", response)
						}
						if headers.ETag.Value != `"revision:2"` || headers.XPowerContextRequestID.Value == "" {
							t.Fatalf("head headers = %#v", headers)
						}
						record = headers.Response
					} else {
						response, getErr := client.GetArtifactRevision(t.Context(), canonicalartifact.GetArtifactRevisionParams{ScopeID: scopeID, Family: canonicalartifact.GetArtifactRevisionFamily(fixture.family), ArtifactID: "read", Revision: version.revision})
						if getErr != nil {
							t.Fatal(getErr)
						}
						headers, ok := response.(*canonicalartifact.GetArtifactRevisionOKHeaders)
						if !ok {
							t.Fatalf("historical response = %#v", response)
						}
						if headers.XPowerContextRequestID.Value == "" {
							t.Fatal("historical request ID missing")
						}
						record = headers.Response
					}
					wantContent := fmt.Sprintf(fixture.content, version.word)
					encoded, marshalErr := json.Marshal(record.Content, json.Deterministic(true))
					if marshalErr != nil {
						t.Fatal(marshalErr)
					}
					var decoded any
					if decodeErr := json.Unmarshal(encoded, &decoded); decodeErr != nil {
						t.Fatal(decodeErr)
					}
					encoded, marshalErr = json.Marshal(decoded, json.Deterministic(true))
					if marshalErr != nil {
						t.Fatal(marshalErr)
					}
					if string(encoded) != wantContent || record.Revision != version.revision || record.ScopeID != scopeID || string(record.Family) != fixture.family {
						t.Fatalf("revision = %d, content = %s, want %s", record.Revision, encoded, wantContent)
					}
					digest := sha256.Sum256([]byte(wantContent))
					if record.ContentDigest != "sha256:"+hex.EncodeToString(digest[:]) {
						t.Fatalf("digest = %s", record.ContentDigest)
					}
					if len(record.Sources) != 3 {
						t.Fatalf("sources = %#v", record.Sources)
					}
					for index, want := range []string{"accepted-observation", "content", "external-skill-snapshot"} {
						if string(record.Sources[index].SourceType) != want || record.Sources[index].SourceID != want+"-source" {
							t.Fatalf("source order = %#v", record.Sources)
						}
					}
					if fixture.family == "skill" && (len(record.Artifacts) != 2 || record.Artifacts[0].Revision != 2 || record.Artifacts[1].Revision != 1) {
						t.Fatalf("artifact order = %#v", record.Artifacts)
					}
				}
			})
		}
	}
	t.Run("before restart", verify)
	if closeErr := application.Close(t.Context()); closeErr != nil {
		t.Fatal(closeErr)
	}
	application, err = OpenApplication(t.Context(), config, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	t.Run("after restart", verify)
	assertArtifactIsolationAndValidators(t, application, scopeID, config.Auth.Token)
}

func assertArtifactIsolationAndValidators(t *testing.T, application *Application, scopeID, token string) {
	t.Helper()
	otherScope := createScopeForSidecar(t, application, "Other", "isolated Artifact partition", "")
	client, handler := artifactHTTPClient(t, application, token)
	for _, family := range []canonicalartifact.GetArtifactFamily{canonicalartifact.GetArtifactFamilyMemory, canonicalartifact.GetArtifactFamilyExperience, canonicalartifact.GetArtifactFamilySkill, canonicalartifact.GetArtifactFamilyHandoff} {
		response, getErr := client.GetArtifact(t.Context(), canonicalartifact.GetArtifactParams{ScopeID: otherScope.ID(), Family: family, ArtifactID: "read"})
		if getErr != nil {
			t.Fatal(getErr)
		}
		if _, ok := response.(*canonicalartifact.NotFoundHeaders); !ok {
			t.Fatalf("cross-Scope read = %#v", response)
		}
		historical, historicalErr := client.GetArtifactRevision(t.Context(), canonicalartifact.GetArtifactRevisionParams{ScopeID: otherScope.ID(), Family: canonicalartifact.GetArtifactRevisionFamily(family), ArtifactID: "read", Revision: 1})
		if historicalErr != nil {
			t.Fatal(historicalErr)
		}
		if _, ok := historical.(*canonicalartifact.NotFoundHeaders); !ok {
			t.Fatalf("cross-Scope historical read = %#v", historical)
		}
	}
	for _, validator := range []string{`"revision:2"`, `"revision:1"`, `W/"revision:2"`, `*`, `"revision:2", "revision:1"`} {
		request := httptest.NewRequest(http.MethodGet, "/v1/scopes/"+scopeID+"/artifacts/experience/read", nil)
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("If-None-Match", validator)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		wantStatus := 200
		if validator == `"revision:2"` {
			wantStatus = 304
		}
		if response.Code != wantStatus || response.Header().Get("ETag") != `"revision:2"` || response.Header().Get("X-PowerContext-Request-ID") == "" {
			t.Fatalf("validator %s: status=%d headers=%v body=%s", validator, response.Code, response.Header(), response.Body.String())
		}
		if response.Code == 304 && response.Body.Len() != 0 {
			t.Fatalf("304 body = %s", response.Body.String())
		}
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/scopes/"+scopeID+"/artifacts/experience/read/revisions/1", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("If-None-Match", `"revision:2"`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 200 || response.Header().Get("ETag") != "" || !strings.Contains(response.Body.String(), `"revision":1`) {
		t.Fatalf("exact revision used head caching: %d %v %s", response.Code, response.Header(), response.Body.String())
	}
}
