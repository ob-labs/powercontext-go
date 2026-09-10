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
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	remoteskills "github.com/ob-labs/powercontext-go/api/canonical/remoteskills"
	v1 "github.com/ob-labs/powercontext-go/api/v1"
)

type remoteSkillClientSecurity string

func (s remoteSkillClientSecurity) BearerAuth(
	context.Context,
	remoteskills.OperationName,
) (remoteskills.BearerAuth, error) {
	return remoteskills.BearerAuth{Token: string(s)}, nil
}

type remoteSkillRoundTripper struct{ handler http.Handler }

func (transport remoteSkillRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	request.RemoteAddr = "127.0.0.1:4321"
	recorder := httptest.NewRecorder()
	transport.handler.ServeHTTP(recorder, request)
	return recorder.Result(), nil
}

func remoteSkillHTTPClient(t *testing.T, application *Application, token string) *remoteskills.Client {
	t.Helper()
	handler, err := application.HTTPHandler()
	if err != nil {
		t.Fatal(err)
	}
	client, err := remoteskills.NewClient(
		"http://powercontext.test",
		remoteSkillClientSecurity(token),
		remoteskills.WithClient(&http.Client{Transport: remoteSkillRoundTripper{handler: handler}}),
	)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestCanonicalRemoteSkillLifecycleUsesGeneratedClient(t *testing.T) {
	config := applicationTestConfig(t)
	config.Auth.Enabled = true
	config.Auth.Token = "remote-skill-admin"
	application, err := OpenApplication(t.Context(), config, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeScopeReaderApplication(t, application) })
	scopeID := applicationDefaultScope(t, application).ID()
	admin := remoteSkillHTTPClient(t, application, config.Auth.Token)

	createdResult, err := admin.CreateRemoteSkillTarget(t.Context(), &remoteskills.CreateRemoteSkillTargetRequest{
		ScopeID: scopeID, DisplayName: "workstation", AgentKind: remoteskills.RemoteAgentKindCodex,
	})
	if err != nil {
		t.Fatal(err)
	}
	created, ok := createdResult.(*remoteskills.RemoteSkillTargetEnrollment)
	if !ok || created.EnrollmentCode == "" || created.EnrollmentExpiresAt.IsZero() ||
		created.Target.State != remoteskills.RemoteSkillTargetStatePending {
		t.Fatalf("create response = %#v", createdResult)
	}

	listedResult, err := admin.ListRemoteSkillTargets(t.Context(), &remoteskills.ListRemoteSkillTargetsRequest{ScopeID: scopeID})
	if err != nil {
		t.Fatal(err)
	}
	listed, ok := listedResult.(*remoteskills.ListRemoteSkillTargetsResponse)
	if !ok || len(listed.Targets) != 1 || listed.Targets[0].Target.TargetID != created.Target.TargetID {
		t.Fatalf("list response = %#v", listedResult)
	}

	enroller := remoteSkillHTTPClient(t, application, "")
	enrolledResult, err := enroller.EnrollRemoteSkillTarget(t.Context(), &remoteskills.EnrollRemoteSkillTargetRequest{
		EnrollmentCode: created.EnrollmentCode, InstallationID: "installation-a", ReceiverVersion: "1.0.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	enrolled, ok := enrolledResult.(*remoteskills.RemoteSkillTargetCredential)
	if !ok || enrolled.Credential == "" || enrolled.TargetID != created.Target.TargetID {
		t.Fatalf("enroll response = %#v", enrolledResult)
	}

	secondResult, err := enroller.EnrollRemoteSkillTarget(t.Context(), &remoteskills.EnrollRemoteSkillTargetRequest{
		EnrollmentCode: created.EnrollmentCode, InstallationID: "installation-b", ReceiverVersion: "1.0.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if refusal, ok := secondResult.(*remoteskills.ConflictHeaders); !ok || refusal.Response.Error.Code != "enrollment_refused" {
		t.Fatalf("second enroll = %#v", secondResult)
	}

	renamedResult, err := admin.RenameRemoteSkillTarget(t.Context(), &remoteskills.RenameRemoteSkillTargetRequest{
		ScopeID: scopeID, TargetID: created.Target.TargetID, DisplayName: "renamed", ExpectedGeneration: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	renamed, ok := renamedResult.(*remoteskills.RemoteSkillTarget)
	if !ok || renamed.DisplayName != "renamed" || renamed.Generation != 2 {
		t.Fatalf("rename response = %#v", renamedResult)
	}

	revokedResult, err := admin.RevokeRemoteSkillTarget(t.Context(), &remoteskills.RevokeRemoteSkillTargetRequest{
		ScopeID: scopeID, TargetID: created.Target.TargetID, ExpectedGeneration: renamed.Generation,
	})
	if err != nil {
		t.Fatal(err)
	}
	revoked, ok := revokedResult.(*remoteskills.RemoteSkillTarget)
	if !ok || revoked.State != remoteskills.RemoteSkillTargetStateRevoked || revoked.Generation != 3 {
		t.Fatalf("revoke response = %#v", revokedResult)
	}
}

// Regression for PR #279: an unregistered Scope must retain the standard
// redacted 404 contract before any remote Skill target operation persists work.
func TestCanonicalRemoteSkillUnknownScopeIsRedactedAndHasNoSideEffects(t *testing.T) {
	const unknownScope = "private-unknown-remote-skill-scope"
	config := applicationTestConfig(t)
	config.Auth.Enabled = true
	config.Auth.Token = "remote-skill-secret"
	application, err := OpenApplication(t.Context(), config, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeScopeReaderApplication(t, application) })
	handler, err := application.HTTPHandler()
	if err != nil {
		t.Fatal(err)
	}
	databasePath, err := SQLiteDSN(config.Database.SQLite.URL)
	if err != nil {
		t.Fatal(err)
	}
	database, err := sql.Open("sqlite3", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	for _, test := range []struct {
		name string
		path string
		body string
	}{
		{
			name: "create",
			path: "/v1/skill/remote/target/create",
			body: `{"scope_id":"private-unknown-remote-skill-scope","display_name":"private-enrollment-code","agent_kind":"codex"}`,
		},
		{
			name: "list",
			path: "/v1/skill/remote/targets",
			body: `{"scope_id":"private-unknown-remote-skill-scope"}`,
		},
		{
			name: "rename",
			path: "/v1/skill/remote/target/rename",
			body: `{"scope_id":"private-unknown-remote-skill-scope","target_id":"private-target","display_name":"private-enrollment-code","expected_generation":1}`,
		},
		{
			name: "revoke",
			path: "/v1/skill/remote/target/revoke",
			body: `{"scope_id":"private-unknown-remote-skill-scope","target_id":"private-target","expected_generation":1}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))
			request.Header.Set("Authorization", "Bearer "+config.Auth.Token)
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404: %s", response.Code, response.Body.String())
			}
			var envelope struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil || envelope.Error.Code != "scope_not_found" {
				t.Fatalf("error response = %s, want scope_not_found (err=%v)", response.Body.String(), err)
			}
			for _, protected := range []string{unknownScope, "private-target", "private-enrollment-code", config.Auth.Token, "enrollment_code"} {
				if strings.Contains(response.Body.String(), protected) {
					t.Fatalf("response leaks %q: %s", protected, response.Body.String())
				}
			}
		})
	}

	var count int
	if err := database.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM pc_agent_skill_targets WHERE scope_id = ?", unknownScope).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("unknown Scope wrote %d remote Skill targets", count)
	}
}

// Regression for Phase 4 Task 5: the generated remote-skill sidecar relies on
// the outer transport for its bearer boundary; only exact loopback enrollment
// may bypass it.
func TestCanonicalRemoteSkillGeneratedRoutesKeepOuterBearerBoundary(t *testing.T) {
	config := applicationTestConfig(t)
	config.Auth.Enabled = true
	config.Auth.Token = "remote-skill-admin"
	application, err := OpenApplication(t.Context(), config, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeScopeReaderApplication(t, application) })

	scopeID := applicationDefaultScope(t, application).ID()
	unauthenticated := remoteSkillHTTPClient(t, application, "")
	assertUnauthorized := func(result any, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := result.(*remoteskills.UnauthorizedHeaders); !ok {
			t.Fatalf("unauthenticated generated route response = %T", result)
		}
	}

	var result any
	result, err = unauthenticated.CreateRemoteSkillTarget(t.Context(), &remoteskills.CreateRemoteSkillTargetRequest{
		ScopeID: scopeID, DisplayName: "workstation", AgentKind: remoteskills.RemoteAgentKindCodex,
	})
	assertUnauthorized(result, err)
	result, err = unauthenticated.ListRemoteSkillTargets(t.Context(), &remoteskills.ListRemoteSkillTargetsRequest{ScopeID: scopeID})
	assertUnauthorized(result, err)
	result, err = unauthenticated.RenameRemoteSkillTarget(t.Context(), &remoteskills.RenameRemoteSkillTargetRequest{
		ScopeID: scopeID, TargetID: "target", DisplayName: "renamed", ExpectedGeneration: 1,
	})
	assertUnauthorized(result, err)
	result, err = unauthenticated.RevokeRemoteSkillTarget(t.Context(), &remoteskills.RevokeRemoteSkillTargetRequest{
		ScopeID: scopeID, TargetID: "target", ExpectedGeneration: 1,
	})
	assertUnauthorized(result, err)

	handler, err := application.HTTPHandler()
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/v1/skill/remote/target/enroll", nil),
		httptest.NewRequest(http.MethodPost, "/v1/skill/remote/target/enroll/suffix", nil),
	} {
		request.RemoteAddr = "127.0.0.1:4321"
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s status = %d, want 401", request.Method, request.URL.Path, response.Code)
		}
	}

	admin := remoteSkillHTTPClient(t, application, config.Auth.Token)
	createdResult, err := admin.CreateRemoteSkillTarget(t.Context(), &remoteskills.CreateRemoteSkillTargetRequest{
		ScopeID: scopeID, DisplayName: "enrollment", AgentKind: remoteskills.RemoteAgentKindCodex,
	})
	if err != nil {
		t.Fatal(err)
	}
	created, ok := createdResult.(*remoteskills.RemoteSkillTargetEnrollment)
	if !ok || created.EnrollmentCode == "" {
		t.Fatalf("admin create did not reach generated handler: %#v", createdResult)
	}
	enrolledResult, err := unauthenticated.EnrollRemoteSkillTarget(t.Context(), &remoteskills.EnrollRemoteSkillTargetRequest{
		EnrollmentCode: created.EnrollmentCode, InstallationID: "binding-test", ReceiverVersion: "1.0.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := enrolledResult.(*remoteskills.RemoteSkillTargetCredential); !ok {
		t.Fatalf("exact anonymous enrollment response = %T", enrolledResult)
	}
}

type remoteSkillEnrollmentBoundary struct {
	remoteskills.UnimplementedHandler
	calls int
}

type canonicalRemoteSkillHealthHandler struct {
	v1.UnimplementedHandler
}

func (h *remoteSkillEnrollmentBoundary) EnrollRemoteSkillTarget(
	ctx context.Context,
	request *remoteskills.EnrollRemoteSkillTargetRequest,
) (remoteskills.EnrollRemoteSkillTargetRes, error) {
	h.calls++
	return h.UnimplementedHandler.EnrollRemoteSkillTarget(ctx, request)
}

func TestCanonicalRemoteSkillRemotePlaintextStopsBeforeGeneratedEnrollment(t *testing.T) {
	var logs bytes.Buffer
	boundary := &remoteSkillEnrollmentBoundary{}
	handler, err := NewHTTPHandler(&canonicalRemoteSkillHealthHandler{}, HTTPOptions{
		BearerToken: "private-admin-token", AccessLog: true,
		Logger:                slog.New(slog.NewJSONHandler(&logs, nil)),
		canonicalRemoteSkills: boundary,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/skill/remote/target/enroll",
		strings.NewReader(`{"enrollment_code":"private-code","installation_id":"receiver","receiver_version":"1.0.0"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.RemoteAddr = "203.0.113.7:4321"
	request.Host = "127.0.0.1"
	request.Header.Set("Forwarded", "for=127.0.0.1;proto=https")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusForbidden || boundary.calls != 0 {
		t.Fatalf("remote plaintext response = %d generated calls = %d, want 403 and 0", response.Code, boundary.calls)
	}
	assertRequestID(t, response)
	for _, protected := range []string{"private-code", "private-admin-token"} {
		if strings.Contains(response.Body.String(), protected) || strings.Contains(logs.String(), protected) {
			t.Fatalf("remote transport refusal leaked %q", protected)
		}
	}
	found := false
	for _, record := range decodeLogRecords(t, logs.String()) {
		if record["operation"] == "enroll_remote_skill_target" && record["status_code"] == float64(http.StatusForbidden) {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing outer 403 enrollment access record: %s", logs.String())
	}
}

func TestCanonicalRemoteSkillSidecarReservesOnlyGeneratedPaths(t *testing.T) {
	t.Parallel()

	generated, err := remoteskills.NewServer(remoteskills.UnimplementedHandler{}, canonicalRemoteSkillSecurity{})
	if err != nil {
		t.Fatal(err)
	}
	legacy := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	handler := canonicalRemoteSkillSidecar{next: legacy, skills: generated}

	known := httptest.NewRecorder()
	handler.ServeHTTP(known, httptest.NewRequest(http.MethodPost, "/v1/skill/remote/target/enroll", nil))
	if known.Code == http.StatusTeapot {
		t.Fatal("generated enroll route reached legacy handler")
	}

	wrongMethod := httptest.NewRecorder()
	handler.ServeHTTP(wrongMethod, httptest.NewRequest(http.MethodGet, "/v1/skill/remote/target/enroll", nil))
	if wrongMethod.Code != http.StatusTeapot {
		t.Fatalf("wrong-method response = %d, want legacy response", wrongMethod.Code)
	}
}
