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
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	managedskills "github.com/ob-labs/powercontext-go/api/canonical/managedskills"
	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/artifact/skill"
	serverlogging "github.com/ob-labs/powercontext-go/internal/observability/logging"
	"github.com/ob-labs/powercontext-go/internal/sqlstore"
)

type managedSkillClientSecurity string

func (security managedSkillClientSecurity) BearerAuth(context.Context, managedskills.OperationName) (managedskills.BearerAuth, error) {
	return managedskills.BearerAuth{Token: string(security)}, nil
}

type managedSkillAccessSpanExpectation uint8

const (
	managedSkillAccessSpanIgnored managedSkillAccessSpanExpectation = iota
	managedSkillAccessSpanAbsent
	managedSkillAccessSpanMatchesRequest
)

func managedSkillHTTPClient(t *testing.T, application *Application, token string) *managedskills.Client {
	t.Helper()
	handler, err := application.HTTPHandler()
	if err != nil {
		t.Fatal(err)
	}
	client, err := managedskills.NewClient("http://powercontext.test", managedSkillClientSecurity(token), managedskills.WithClient(&http.Client{
		Transport: scopeSidecarRoundTripper{handler: handler},
	}))
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestCanonicalManagedSkillPackageManifestDownloadAndRestart(t *testing.T) {
	config := applicationTestConfig(t)
	config.Auth.Enabled = true
	config.Auth.Token = "managed-skill-reader-secret"
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
	snapshots := make([]skill.PackageSnapshot, 0, 2)
	for _, instructions := range []string{"first package instruction", "second package instruction"} {
		snapshot := captureManagedSkillPackage(t, instructions)
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
	client := managedSkillHTTPClient(t, application, config.Auth.Token)
	for index, snapshot := range snapshots {
		request := &managedskills.GetSkillPackageRequest{ScopeID: scopeID, Artifact: managedskills.ArtifactReference{
			Family: "skill", ArtifactID: "package-read", Revision: index + 1,
		}}
		manifestResult, manifestErr := client.GetSkillPackageManifest(t.Context(), request)
		if manifestErr != nil {
			t.Fatal(manifestErr)
		}
		manifest, ok := manifestResult.(*managedskills.SkillPackageManifest)
		if !ok {
			t.Fatalf("manifest revision %d = %T", index+1, manifestResult)
		}
		ref := snapshot.Reference()
		if manifest.Name != "read-skill" || manifest.Description != "Package description" || manifest.Package.TreeDigest != ref.TreeDigest() || manifest.Package.ArchiveDigest != ref.ArchiveDigest() || manifest.Package.FileCount != ref.FileCount() || manifest.Package.UncompressedSize != ref.UncompressedSize() || manifest.Package.ArchiveSize != ref.ArchiveSize() {
			t.Fatalf("manifest revision %d = %#v", index+1, manifest)
		}
		if len(manifest.Files) != len(snapshot.Entries()) || !manifest.License.Null || !manifest.Compatibility.Null || !manifest.AllowedTools.Null || len(manifest.Metadata) != 0 {
			t.Fatalf("manifest optional/file projection = %#v", manifest)
		}
		downloadResult, downloadErr := client.DownloadSkillPackage(t.Context(), request)
		if downloadErr != nil {
			t.Fatal(downloadErr)
		}
		download, ok := downloadResult.(*managedskills.SkillPackageDownload)
		if !ok {
			t.Fatalf("download revision %d = %T", index+1, downloadResult)
		}
		archive, decodeErr := base64.StdEncoding.DecodeString(download.ArchiveBase64)
		if decodeErr != nil || string(archive) != string(snapshot.Archive()) || download.Package != manifest.Package {
			t.Fatalf("download revision %d did not preserve the verified snapshot", index+1)
		}
	}
	unauthenticated := managedSkillHTTPClient(t, application, "")
	denied, deniedErr := unauthenticated.GetSkillPackageManifest(t.Context(), &managedskills.GetSkillPackageRequest{ScopeID: scopeID, Artifact: managedskills.ArtifactReference{Family: "skill", ArtifactID: "package-read", Revision: 1}})
	if deniedErr != nil {
		t.Fatal(deniedErr)
	}
	if _, ok := denied.(*managedskills.UnauthorizedHeaders); !ok {
		t.Fatalf("unauthenticated response = %T", denied)
	}
	invalid, invalidErr := client.GetSkillPackageManifest(t.Context(), &managedskills.GetSkillPackageRequest{ScopeID: scopeID, Artifact: managedskills.ArtifactReference{Family: "skill", ArtifactID: "package-read", Revision: 0}})
	if invalidErr != nil {
		t.Fatal(invalidErr)
	}
	if _, ok := invalid.(*managedskills.InvalidRequestHeaders); !ok {
		t.Fatalf("zero revision response = %T", invalid)
	}
}

func TestCanonicalManagedSkillUsageHTTPRecordsExactPackageEvidence(t *testing.T) {
	config := applicationTestConfig(t)
	config.Auth.Enabled = true
	config.Auth.Token = "managed-skill-usage-token"
	application, err := OpenApplication(t.Context(), config, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeScopeReaderApplication(t, application) })
	scopeID := applicationDefaultScope(t, application).ID()
	snapshot := persistManagedSkillPackage(t, artifactTestDatabase(t, config), scopeID, "usage-package", "usage-instruction")
	task, err := application.sources.CaptureContent(t.Context(), scopeID, "usage-task", "completed task", nil)
	if err != nil {
		t.Fatal(err)
	}
	client := managedSkillHTTPClient(t, application, config.Auth.Token)
	request := &managedskills.RecordSkillUsageRequest{
		ScopeID: scopeID, ObservationID: "usage-observation",
		SkillRef:      managedskills.ArtifactReference{Family: skill.Family, ArtifactID: "usage-package", Revision: 1},
		PackageDigest: "sha256:" + snapshot.Reference().TreeDigest(),
		TargetID:      "codex-target", Selected: true,
		Invoked:    managedskills.RecordSkillUsageRequestInvokedTrue,
		Validation: managedskills.RecordSkillUsageRequestValidationPassed,
		Outcome:    managedskills.RecordSkillUsageRequestOutcomeSuccess,
		TaskSource: managedskills.NewOptSourceReference(managedskills.SourceReference{Name: task.Ref.Type(), SourceID: task.Ref.ID()}),
	}
	result, requestErr := client.RecordSkillUsage(t.Context(), request)
	if requestErr != nil {
		t.Fatal(requestErr)
	}
	response, ok := result.(*managedskills.CaptureContentSourceResponse)
	if !ok || response.Status != managedskills.CaptureStatusAccepted || response.Position != 2 ||
		response.Source.Name != "skill-usage" || response.Source.SourceID != "usage-observation" {
		t.Fatalf("usage response = %#v", result)
	}
	replayed, requestErr := client.RecordSkillUsage(t.Context(), request)
	if requestErr != nil {
		t.Fatal(requestErr)
	}
	replayedResponse, ok := replayed.(*managedskills.CaptureContentSourceResponse)
	if !ok || *replayedResponse != *response {
		t.Fatalf("usage idempotency response = %#v", replayed)
	}
	invalid := *request
	invalid.ObservationID = "rejected-usage"
	invalid.PackageDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	rejected, requestErr := client.RecordSkillUsage(t.Context(), &invalid)
	if requestErr != nil {
		t.Fatal(requestErr)
	}
	if _, ok := rejected.(*managedskills.InvalidRequestHeaders); !ok {
		t.Fatalf("mismatched digest response = %T", rejected)
	}
}

func TestCanonicalManagedSkillPackageHTTPFailuresAreRedactedAndReadOnly(t *testing.T) {
	var logs bytes.Buffer
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()), sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { _ = provider.Shutdown(t.Context()) })
	config := applicationTestConfig(t)
	config.Auth.Enabled = true
	config.Auth.Token = "private-managed-skill-token"
	config.Logging.Access = true
	application, err := OpenApplication(t.Context(), config, Dependencies{Logger: newJSONTestLogger(t, &logs), TracerProvider: provider})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeScopeReaderApplication(t, application) })
	scopeID := applicationDefaultScope(t, application).ID()
	database := artifactTestDatabase(t, config)
	snapshot := persistManagedSkillPackage(t, database, scopeID, "private-package", "private-instruction")
	if _, err := database.ExecContext(t.Context(), `INSERT INTO pc_artifacts(scope_id, family, artifact_id, revision, content) VALUES (?, 'skill', 'private-legacy', 1, ?), (?, 'experience', 'private-other-family', 1, ?)`,
		scopeID, []byte(`{"name":"legacy","description":"legacy","instructions":"private-legacy-content"}`), scopeID, []byte(`{"situation":"private-other-content","action":"a","outcome":"o","lesson":"l"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(t.Context(), `INSERT INTO pc_artifact_heads(scope_id, family, artifact_id, revision) VALUES (?, 'skill', 'private-legacy', 1), (?, 'experience', 'private-other-family', 1)`, scopeID, scopeID); err != nil {
		t.Fatal(err)
	}
	handler, err := application.HTTPHandler()
	if err != nil {
		t.Fatal(err)
	}
	beforeArtifacts := managedSkillTableCount(t, database, "pc_artifacts")
	beforePackages := managedSkillTableCount(t, database, "pc_skill_packages")
	beforeSources := managedSkillTableCount(t, database, "pc_sources")
	requestBody := `{"scope_id":"` + scopeID + `","artifact":{"family":"skill","artifact_id":"private-package","revision":1}}`
	usageDigest := "sha256:" + snapshot.Reference().TreeDigest()
	usageBody := `{"scope_id":"` + scopeID + `","observation_id":"private-usage-observation","skill_ref":{"family":"skill","artifact_id":"private-package","revision":1},"package_digest":"` + usageDigest + `","target_id":"private-usage-target","selected":true,"invoked":"true","validation":"passed","outcome":"success","task_source":{"name":"content","source_id":"private-missing-task"}}`
	for _, testCase := range []struct {
		name, path, body, token, operation string
		status                             int
		spanExpectation                    managedSkillAccessSpanExpectation
	}{
		{"manifest", "/v1/skill/package/manifest", requestBody, config.Auth.Token, "get_skill_package_manifest", http.StatusOK, managedSkillAccessSpanMatchesRequest},
		{"download", "/v1/skill/package/download", requestBody, config.Auth.Token, "download_skill_package", http.StatusOK, managedSkillAccessSpanMatchesRequest},
		{"auth", "/v1/skill/package/manifest", requestBody, "", "get_skill_package_manifest", http.StatusUnauthorized, managedSkillAccessSpanAbsent},
		{"unknown scope", "/v1/skill/package/manifest", `{"scope_id":"private-missing-scope","artifact":{"family":"skill","artifact_id":"private-package","revision":1}}`, config.Auth.Token, "get_skill_package_manifest", http.StatusNotFound, managedSkillAccessSpanIgnored},
		{"missing revision", "/v1/skill/package/download", `{"scope_id":"` + scopeID + `","artifact":{"family":"skill","artifact_id":"private-package","revision":99}}`, config.Auth.Token, "download_skill_package", http.StatusNotFound, managedSkillAccessSpanIgnored},
		{"missing arbitrary family", "/v1/skill/package/manifest", `{"scope_id":"` + scopeID + `","artifact":{"family":"future.family","artifact_id":"private-arbitrary-family","revision":1}}`, config.Auth.Token, "get_skill_package_manifest", http.StatusNotFound, managedSkillAccessSpanIgnored},
		{"invalid revision", "/v1/skill/package/manifest", `{"scope_id":"` + scopeID + `","artifact":{"family":"skill","artifact_id":"private-package","revision":0}}`, config.Auth.Token, "get_skill_package_manifest", http.StatusUnprocessableEntity, managedSkillAccessSpanIgnored},
		{"legacy Skill", "/v1/skill/package/manifest", `{"scope_id":"` + scopeID + `","artifact":{"family":"skill","artifact_id":"private-legacy","revision":1}}`, config.Auth.Token, "get_skill_package_manifest", http.StatusInternalServerError, managedSkillAccessSpanIgnored},
		{"persisted experience", "/v1/skill/package/manifest", `{"scope_id":"` + scopeID + `","artifact":{"family":"experience","artifact_id":"private-other-family","revision":1}}`, config.Auth.Token, "get_skill_package_manifest", http.StatusInternalServerError, managedSkillAccessSpanIgnored},
		{"missing usage task Source", "/v1/skill/usage", usageBody, config.Auth.Token, "record_skill_usage", http.StatusNotFound, managedSkillAccessSpanMatchesRequest},
		{"MCP remains isolated", "/mcp", requestBody, config.Auth.Token, "", http.StatusNotFound, managedSkillAccessSpanIgnored},
		{"legacy route remains isolated", "/v1/skill/package/materialize", requestBody, config.Auth.Token, "", http.StatusNotFound, managedSkillAccessSpanIgnored},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, testCase.path, bytes.NewBufferString(testCase.body))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Authorization", "Bearer "+testCase.token)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			requestID := response.Header().Get("X-PowerContext-Request-ID")
			if response.Code != testCase.status || requestID == "" {
				t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
			}
			if testCase.operation != "" {
				assertManagedSkillPackageAccessLog(t, logs.String(), requestID, testCase.operation, testCase.status, testCase.spanExpectation)
			}
			for _, protected := range []string{scopeID, config.Auth.Token, "private-package", "private-instruction", "private-missing-scope", "private-arbitrary-family", "private-other-family", "private-usage-observation", "private-usage-target", "private-missing-task", usageDigest} {
				if strings.Contains(response.Body.String(), protected) || strings.Contains(logs.String(), protected) {
					t.Fatalf("HTTP or access logs leaked %q", protected)
				}
				for _, span := range recorder.Ended() {
					if strings.Contains(fmt.Sprintf("%v %v %v", span.Attributes(), span.Events(), span.Status()), protected) {
						t.Fatalf("trace leaked %q", protected)
					}
				}
			}
		})
	}
	if got := managedSkillTableCount(t, database, "pc_artifacts"); got != beforeArtifacts {
		t.Fatalf("reads changed artifacts: got %d want %d", got, beforeArtifacts)
	}
	if got := managedSkillTableCount(t, database, "pc_skill_packages"); got != beforePackages {
		t.Fatalf("reads changed packages: got %d want %d", got, beforePackages)
	}
	if got := managedSkillTableCount(t, database, "pc_sources"); got != beforeSources {
		t.Fatalf("failed usage write changed Sources: got %d want %d", got, beforeSources)
	}
	if _, err := database.ExecContext(t.Context(), `UPDATE pc_skill_packages SET archive = x'707269766174652d636f7272757074' WHERE scope_id = ?`, scopeID); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/skill/package/manifest", bytes.NewBufferString(requestBody))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+config.Auth.Token)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("corrupt package status = %d body=%s", response.Code, response.Body.String())
	}
	for _, protected := range []string{scopeID, config.Auth.Token, "private-package", "private-instruction"} {
		if strings.Contains(response.Body.String(), protected) || strings.Contains(logs.String(), protected) {
			t.Fatalf("corrupt package response or logs leaked %q", protected)
		}
	}
	if err := application.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest(http.MethodPost, "/v1/skill/package/manifest", bytes.NewBufferString(requestBody))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+config.Auth.Token)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "runtime_not_ready") {
		t.Fatalf("closed runtime response = %d %s", response.Code, response.Body.String())
	}
}

func assertManagedSkillPackageAccessLog(
	t *testing.T,
	output, requestID, operation string,
	status int,
	spanExpectation managedSkillAccessSpanExpectation,
) {
	t.Helper()
	for _, record := range decodeLogRecords(t, output) {
		if record["event"] != serverlogging.TransportCompletedEvent || record["operation"] != operation || record["request_id"] != requestID {
			continue
		}
		level := "INFO"
		if status >= http.StatusInternalServerError {
			level = "ERROR"
		}
		outcome := "success"
		if status >= http.StatusBadRequest {
			outcome = "failure"
		}
		assertLogFields(t, record, map[string]any{
			"level": level, "logger": "powercontext.server.access", "outcome": outcome,
			"transport": "http", "unit": "transport", "status_code": float64(status),
		})
		switch spanExpectation {
		case managedSkillAccessSpanAbsent:
			if _, found := record["span_id"]; found {
				t.Fatalf("access log operation %q request ID %q unexpectedly has span ID %#v", operation, requestID, record["span_id"])
			}
		case managedSkillAccessSpanMatchesRequest:
			if record["span_id"] != requestID {
				t.Fatalf("access log operation %q span ID %#v, want request ID %q", operation, record["span_id"], requestID)
			}
		}
		return
	}
	t.Fatalf("access log operation %q request ID %q not found in %s", operation, requestID, output)
}

func persistManagedSkillPackage(t *testing.T, database *sql.DB, scopeID, artifactID, instructions string) skill.PackageSnapshot {
	t.Helper()
	attached, err := sqlstore.Attach(database)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := sqlstore.NewArtifactRepository(sqlstore.SQLiteDialect, sqlstore.SkillArtifactCodec())
	if err != nil {
		t.Fatal(err)
	}
	snapshot := captureManagedSkillPackage(t, instructions)
	content, err := skill.NewPackageContent(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := skill.NewPackageDraft(content, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := attached.Transaction(t.Context(), func(tx sqlstore.DBTX) error {
		if _, err := (sqlstore.SkillPackageRepository{}).Add(t.Context(), tx, scopeID, snapshot); err != nil {
			return err
		}
		_, err := repository.Create(t.Context(), tx, scopeID, artifactID, draft)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func managedSkillTableCount(t *testing.T, database *sql.DB, table string) int {
	t.Helper()
	query := ""
	switch table {
	case "pc_artifacts":
		query = "SELECT COUNT(*) FROM pc_artifacts"
	case "pc_skill_packages":
		query = "SELECT COUNT(*) FROM pc_skill_packages"
	case "pc_sources":
		query = "SELECT COUNT(*) FROM pc_sources"
	default:
		t.Fatalf("unexpected table %q", table)
	}
	var count int
	if err := database.QueryRowContext(t.Context(), query).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func captureManagedSkillPackage(t *testing.T, instructions string) skill.PackageSnapshot {
	t.Helper()
	root := filepath.Join(t.TempDir(), "read-skill")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := "---\nname: read-skill\ndescription: Package description\nmetadata: {}\n---\n" + instructions + "\n"
	if err := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := skill.CapturePackageDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}
