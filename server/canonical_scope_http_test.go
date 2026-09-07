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
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	canonicalscopec "github.com/ob-labs/powercontext-go/api/canonical/scopes"
	v1 "github.com/ob-labs/powercontext-go/api/v1"
	"github.com/ob-labs/powercontext-go/internal/scope"
)

func TestOpenApplicationServesCanonicalScopeSidecar(t *testing.T) {
	config := applicationTestConfig(t)
	config.Auth.Enabled = true
	config.Auth.Token = "scope-sidecar-secret"
	application, err := OpenApplication(t.Context(), config, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeScopeReaderApplication(t, application) })

	handler, err := application.HTTPHandler()
	if err != nil {
		t.Fatal(err)
	}
	root := createScopeForSidecar(t, application, "Root", "root scope", "")
	child := createScopeForSidecar(t, application, "Child", "child scope", root.ID())
	defaultScope, err := application.scopes.Default(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	unauthenticated := newCanonicalScopeClient(t, handler, "")
	denied, err := unauthenticated.ListScopes(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := denied.(*canonicalscopec.UnauthorizedHeaders); !ok {
		t.Fatalf("unauthenticated ListScopes() = %T, want UnauthorizedHeaders", denied)
	}
	client := newCanonicalScopeClient(t, handler, config.Auth.Token)

	result, err := client.ListScopes(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	page, ok := result.(*canonicalscopec.ScopePage)
	if !ok {
		t.Fatalf("ListScopes() = %T, want ScopePage", result)
	}
	if len(page.Items) != 3 || page.Items[0].ScopeID == "" {
		t.Fatalf("ListScopes() = %#v, want default and two created Scopes", page)
	}

	defaultResult, err := client.GetDefaultScope(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if value, ok := defaultResult.(*canonicalscopec.ScopeDescriptor); !ok || value.ScopeID != defaultScope.ID() {
		t.Fatalf("GetDefaultScope() = %#v, want %q", defaultResult, defaultScope.ID())
	}
	getResult, err := client.GetScope(t.Context(), canonicalscopec.GetScopeParams{ScopeID: child.ID()})
	if err != nil {
		t.Fatal(err)
	}
	if value, ok := getResult.(*canonicalscopec.ScopeDescriptor); !ok || value.ScopeID != child.ID() {
		t.Fatalf("GetScope() = %#v, want %q", getResult, child.ID())
	}

	allResult, err := client.ResolveScopeSelection(t.Context(), &canonicalscopec.ResolveScopeSelectionRequest{
		Selection: canonicalscopec.NewAllScopeSelectionScopeSelection(canonicalscopec.AllScopeSelection{
			Mode: canonicalscopec.AllScopeSelectionModeAll,
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	all, ok := allResult.(*canonicalscopec.ScopePage)
	if !ok || len(all.Items) != len(page.Items) {
		t.Fatalf("all selection = %#v, want listed Scope page", allResult)
	}
	exactResult, err := client.ResolveScopeSelection(t.Context(), &canonicalscopec.ResolveScopeSelectionRequest{
		Selection: canonicalscopec.NewExactScopeSelectionScopeSelection(canonicalscopec.ExactScopeSelection{
			Mode: canonicalscopec.ExactScopeSelectionModeExact, ScopeIds: []string{child.ID(), root.ID()},
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	exactIDs := []string{root.ID(), child.ID()}
	slices.Sort(exactIDs)
	assertCanonicalScopeIDs(t, exactResult, exactIDs...)
	subtreeResult, err := client.ResolveScopeSelection(t.Context(), &canonicalscopec.ResolveScopeSelectionRequest{
		Selection: canonicalscopec.NewSubtreeScopeSelectionScopeSelection(canonicalscopec.SubtreeScopeSelection{
			Mode: canonicalscopec.SubtreeScopeSelectionModeSubtree, RootScopeID: root.ID(),
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	assertCanonicalScopeIDs(t, subtreeResult, root.ID(), child.ID())

	for _, binding := range []struct {
		integration, target string
	}{
		{integration: "codex", target: child.ID()},
		{integration: "workbuddy", target: root.ID()},
	} {
		key := canonicalscopec.ScopeBindingKey{
			Integration: canonicalscopec.ScopeBindingKeyIntegration(binding.integration),
			Kind:        "project",
			ExternalID:  "repository",
		}
		set, setErr := client.SetScopeBinding(t.Context(), &canonicalscopec.ScopeBinding{
			Key: key, ScopeID: binding.target,
		})
		if setErr != nil {
			t.Fatal(setErr)
		}
		if value, ok := set.(*canonicalscopec.ScopeBinding); !ok || value.Key != key || value.ScopeID != binding.target {
			t.Fatalf("%s SetScopeBinding() = %#v, want %q", binding.integration, set, binding.target)
		}
		result, resolveErr := client.ResolveScopeBinding(t.Context(), &canonicalscopec.ResolveScopeBindingRequest{
			BindingKeys: []canonicalscopec.ScopeBindingKey{key},
		})
		if resolveErr != nil {
			t.Fatal(resolveErr)
		}
		if value, ok := result.(*canonicalscopec.ScopeDescriptor); !ok || value.ScopeID != binding.target {
			t.Fatalf("%s binding = %#v, want %q", binding.integration, result, binding.target)
		}
		for attempt, want := range []bool{true, false} {
			cleared, clearErr := client.ClearScopeBinding(t.Context(), &canonicalscopec.ClearScopeBindingRequest{Key: key})
			if clearErr != nil {
				t.Fatal(clearErr)
			}
			response, ok := cleared.(*canonicalscopec.ClearScopeBindingResponse)
			if !ok || response.Cleared != want {
				t.Fatalf("%s ClearScopeBinding() attempt %d = %#v, want cleared=%t", binding.integration, attempt+1, cleared, want)
			}
		}
	}
	explicit := canonicalscopec.NewOptNilString(defaultScope.ID())
	resolved, err := client.ResolveScopeBinding(t.Context(), &canonicalscopec.ResolveScopeBindingRequest{
		BindingKeys: []canonicalscopec.ScopeBindingKey{{
			Integration: canonicalscopec.ScopeBindingKeyIntegrationCodex,
			Kind:        "project",
			ExternalID:  "repository",
		}},
		ExplicitScopeID: explicit,
	})
	if err != nil {
		t.Fatal(err)
	}
	if value, ok := resolved.(*canonicalscopec.ScopeDescriptor); !ok || value.ScopeID != defaultScope.ID() {
		t.Fatalf("explicit Scope binding = %#v, want %q", resolved, defaultScope.ID())
	}

	const privateCreationKey = "canonical-private-creation-key"
	createdResult, err := client.CreateScope(t.Context(), &canonicalscopec.CreateScopeRequest{
		Title:          "Canonical",
		Summary:        "created through canonical HTTP",
		IdempotencyKey: privateCreationKey,
		ParentScopeID:  canonicalscopec.NewOptNilString(root.ID()),
		ContextReferences: []string{
			child.ID(),
		},
		ExternalReferences: []canonicalscopec.ScopeExternalReference{{
			Kind: "repository", Value: "powercontext-go",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	created, ok := createdResult.(*canonicalscopec.ScopeDescriptor)
	if !ok || created.ScopeID == "" || created.Version != 1 || created.Title != "Canonical" {
		t.Fatalf("CreateScope() = %#v, want version-one canonical Scope", createdResult)
	}

	updatedResult, err := client.UpdateScope(t.Context(), &canonicalscopec.UpdateScopeRequest{
		ExpectedVersion:   created.Version,
		Title:             "Canonical updated",
		Summary:           "updated through canonical HTTP",
		ParentScopeID:     canonicalscopec.NewOptNilString(root.ID()),
		ContextReferences: []string{child.ID()},
		ExternalReferences: []canonicalscopec.ScopeExternalReference{{
			Kind: "repository", Value: "powercontext-go",
		}},
	}, canonicalscopec.UpdateScopeParams{ScopeID: created.ScopeID})
	if err != nil {
		t.Fatal(err)
	}
	updated, ok := updatedResult.(*canonicalscopec.ScopeDescriptor)
	if !ok || updated.ScopeID != created.ScopeID || updated.Version != 2 || updated.Title != "Canonical updated" {
		t.Fatalf("UpdateScope() = %#v, want version-two canonical Scope", updatedResult)
	}

	staleResult, err := client.UpdateScope(t.Context(), &canonicalscopec.UpdateScopeRequest{
		ExpectedVersion: 1,
		Title:           "stale",
		Summary:         "stale update",
	}, canonicalscopec.UpdateScopeParams{ScopeID: created.ScopeID})
	if err != nil {
		t.Fatal(err)
	}
	assertCanonicalScopeConflictRedacted(t, staleResult, "scope_version_conflict", created.ScopeID)

	idempotencyResult, err := client.CreateScope(t.Context(), &canonicalscopec.CreateScopeRequest{
		Title: "different", Summary: "different metadata", IdempotencyKey: privateCreationKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertCanonicalScopeConflictRedacted(t, idempotencyResult, "scope_idempotency_conflict", privateCreationKey, created.ScopeID)

	setDefaultResult, err := client.SetDefaultScope(t.Context(), &canonicalscopec.SetDefaultScopeRequest{ScopeID: created.ScopeID})
	if err != nil {
		t.Fatal(err)
	}
	if value, ok := setDefaultResult.(*canonicalscopec.ScopeDescriptor); !ok || value.ScopeID != created.ScopeID {
		t.Fatalf("SetDefaultScope() = %#v, want %q", setDefaultResult, created.ScopeID)
	}
	defaultResult, err = client.GetDefaultScope(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if value, ok := defaultResult.(*canonicalscopec.ScopeDescriptor); !ok || value.ScopeID != created.ScopeID {
		t.Fatalf("GetDefaultScope() after update = %#v, want %q", defaultResult, created.ScopeID)
	}
}

func TestCanonicalScopeSidecarRejectsMalformedRequestsBeforeLegacyFallback(t *testing.T) {
	application, err := OpenApplication(t.Context(), applicationTestConfig(t), Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeScopeReaderApplication(t, application) })
	handler, err := application.HTTPHandler()
	if err != nil {
		t.Fatal(err)
	}

	for _, request := range []struct {
		name, method, path, body string
	}{
		{name: "unknown union", method: http.MethodPost, path: "/v1/scopes/selection/resolve", body: `{"selection":{"mode":"other"}}`},
		{name: "empty exact", method: http.MethodPost, path: "/v1/scopes/selection/resolve", body: `{"selection":{"mode":"exact","scope_ids":[]}}`},
		{name: "oversized scope", method: http.MethodGet, path: "/v1/scopes/" + strings.Repeat("x", 257)},
	} {
		t.Run(request.name, func(t *testing.T) {
			response := scopeSidecarRequest(t, handler, request.method, request.path, request.body, "")
			if response.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422: %s", response.Code, response.Body.String())
			}
			var envelope struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if decodeErr := json.Unmarshal(response.Body.Bytes(), &envelope); decodeErr != nil || envelope.Error.Code != "invalid_request" {
				t.Fatalf("error envelope = %#v, err=%v", envelope, decodeErr)
			}
		})
	}
}

func TestCanonicalScopeSidecarReservesOnlyExactScopeOperations(t *testing.T) {
	application, err := OpenApplication(t.Context(), applicationTestConfig(t), Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeScopeReaderApplication(t, application) })
	handler, err := application.HTTPHandler()
	if err != nil {
		t.Fatal(err)
	}

	defaultScope := scopeSidecarRequest(t, handler, http.MethodGet, "/v1/scopes/default", "", "")
	if defaultScope.Code != http.StatusOK {
		t.Fatalf("default Scope status = %d: %s", defaultScope.Code, defaultScope.Body.String())
	}
	for _, request := range []struct {
		name, method, path string
	}{
		{name: "scopes wrong method", method: http.MethodDelete, path: "/v1/scopes"},
		{name: "default wrong method", method: http.MethodPost, path: "/v1/scopes/default"},
		{name: "deferred Scope path", method: http.MethodPost, path: "/v1/scopes/create"},
		{name: "binding wrong method", method: http.MethodGet, path: "/v1/scope-bindings/resolve"},
	} {
		t.Run(request.name, func(t *testing.T) {
			response := scopeSidecarRequest(t, handler, request.method, request.path, "{}", "")
			if response.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want legacy 404: %s", response.Code, response.Body.String())
			}
		})
	}

	legacy, err := v1.NewClient("http://powercontext.test", legacySidecarSecurity{}, v1.WithClient(&http.Client{
		Transport: scopeSidecarRoundTripper{handler: handler},
	}))
	if err != nil {
		t.Fatal(err)
	}
	result, err := legacy.GetLiveness(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || result.Response.Status != "ok" {
		t.Fatalf("legacy GetLiveness() = %#v", result)
	}
}

func TestCanonicalScopeSidecarRedactsMissingScopeAndBindingValues(t *testing.T) {
	config := applicationTestConfig(t)
	config.Auth.Enabled = true
	config.Auth.Token = "scope-sidecar-secret"
	application, err := OpenApplication(t.Context(), config, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeScopeReaderApplication(t, application) })
	handler, err := application.HTTPHandler()
	if err != nil {
		t.Fatal(err)
	}

	for _, request := range []struct {
		name, method, path, body, private string
	}{
		{
			name: "Scope", method: http.MethodGet, path: "/v1/scopes/private-scope-id", private: "private-scope-id",
		},
		{
			name: "subtree root", method: http.MethodPost, path: "/v1/scopes/selection/resolve",
			body: `{"selection":{"mode":"subtree","root_scope_id":"private-root-id"}}`, private: "private-root-id",
		},
	} {
		t.Run(request.name, func(t *testing.T) {
			response := scopeSidecarRequest(t, handler, request.method, request.path, request.body, config.Auth.Token)
			if response.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404: %s", response.Code, response.Body.String())
			}
			if strings.Contains(response.Body.String(), request.private) {
				t.Fatalf("response leaked %q: %s", request.private, response.Body.String())
			}
		})
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
	if _, err := database.ExecContext(t.Context(), "DELETE FROM pc_scope_settings WHERE name = 'default'"); err != nil {
		t.Fatal(err)
	}
	client := newCanonicalScopeClient(t, handler, config.Auth.Token)
	result, err := client.ResolveScopeBinding(t.Context(), &canonicalscopec.ResolveScopeBindingRequest{})
	if err != nil {
		t.Fatal(err)
	}
	missing, ok := result.(*canonicalscopec.NotFoundHeaders)
	if !ok || missing.Response.Error.Code != "scope_binding_not_found" {
		t.Fatalf("unbound Scope resolution = %#v, want redacted binding 404", result)
	}
}

type scopeSidecarClientSecurity struct {
	token string
}

type legacySidecarSecurity struct{}

func (legacySidecarSecurity) BearerAuth(context.Context, v1.OperationName) (v1.BearerAuth, error) {
	return v1.BearerAuth{}, nil
}

func (security scopeSidecarClientSecurity) BearerAuth(
	context.Context,
	canonicalscopec.OperationName,
) (canonicalscopec.BearerAuth, error) {
	return canonicalscopec.BearerAuth{Token: security.token}, nil
}

type scopeSidecarRoundTripper struct {
	handler http.Handler
}

func (transport scopeSidecarRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	recorder := httptest.NewRecorder()
	transport.handler.ServeHTTP(recorder, request)
	return recorder.Result(), nil
}

func newCanonicalScopeClient(t *testing.T, handler http.Handler, token string) *canonicalscopec.Client {
	t.Helper()
	client, err := canonicalscopec.NewClient("http://powercontext.test", scopeSidecarClientSecurity{token: token}, canonicalscopec.WithClient(&http.Client{
		Transport: scopeSidecarRoundTripper{handler: handler},
	}))
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func createScopeForSidecar(t *testing.T, application *Application, title, summary, parent string) scope.Descriptor {
	t.Helper()
	draft, err := scope.NewDraft(title, summary, parent, nil, nil, title+"-request")
	if err != nil {
		t.Fatal(err)
	}
	value, err := application.scopes.Create(t.Context(), draft)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func assertCanonicalScopeIDs(t *testing.T, result canonicalscopec.ResolveScopeSelectionRes, want ...string) {
	t.Helper()
	page, ok := result.(*canonicalscopec.ScopePage)
	if !ok {
		t.Fatalf("selection = %T, want ScopePage", result)
	}
	if len(page.Items) != len(want) {
		t.Fatalf("selection item count = %d, want %d: %#v", len(page.Items), len(want), page)
	}
	for index, id := range want {
		if page.Items[index].ScopeID != id {
			t.Fatalf("selection item %d = %q, want %q", index, page.Items[index].ScopeID, id)
		}
	}
}

func assertCanonicalScopeConflictRedacted(t *testing.T, result any, code string, private ...string) {
	t.Helper()
	conflict, ok := result.(*canonicalscopec.ConflictHeaders)
	if !ok || conflict.Response.Error.Code != code {
		t.Fatalf("conflict = %#v, want %q", result, code)
	}
	encoded, err := json.Marshal(conflict.Response)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range private {
		if strings.Contains(string(encoded), value) {
			t.Fatalf("conflict response leaked private value %q: %s", value, encoded)
		}
	}
}

func scopeSidecarRequest(t *testing.T, handler http.Handler, method, path, body, token string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
