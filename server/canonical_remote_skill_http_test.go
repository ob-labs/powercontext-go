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
	"net/http"
	"net/http/httptest"
	"testing"

	remoteskills "github.com/ob-labs/powercontext-go/api/canonical/remoteskills"
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
