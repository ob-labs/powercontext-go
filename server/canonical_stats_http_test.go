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
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	canonicalstats "github.com/ob-labs/powercontext-go/api/canonical/stats"
)

func TestOpenApplicationServesCanonicalStatsSelectionAndPreservesLegacyGet(t *testing.T) {
	config := applicationTestConfig(t)
	config.Auth.Enabled = true
	config.Auth.Token = "canonical-stats-secret"
	application, err := OpenApplication(t.Context(), config, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeScopeReaderApplication(t, application) })
	handler, err := application.HTTPHandler()
	if err != nil {
		t.Fatal(err)
	}
	defaultScope := applicationDefaultScope(t, application)
	child := createScopeForSidecar(t, application, "Canonical statistics", "Canonical statistics scope", "")
	client := newCanonicalStatsClient(t, handler, config.Auth.Token)

	result, err := client.GetStats(t.Context(), &canonicalstats.GetStatsRequest{
		Selection: canonicalstats.NewExactScopeSelectionScopeSelection(canonicalstats.ExactScopeSelection{
			Mode: canonicalstats.ExactScopeSelectionModeExact, ScopeIds: []string{child.ID(), defaultScope.ID()},
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	statsResult, ok := result.(*canonicalstats.ScopedStatsHeaders)
	if !ok {
		t.Fatalf("GetStats() = %T", result)
	}
	if cache, set := statsResult.CacheControl.Get(); !set || cache != canonicalstats.GetStatsOKCacheControlNoStore ||
		!statsResult.XPowerContextRequestID.IsSet() {
		t.Fatalf("canonical response headers = %#v", statsResult)
	}
	wantScopeIDs := []string{defaultScope.ID(), child.ID()}
	slices.Sort(wantScopeIDs)
	if !slices.Equal(statsResult.Response.ScopeIds, wantScopeIDs) || len(statsResult.Response.ByScope) != len(wantScopeIDs) {
		t.Fatalf("canonical Scope result = %#v, want %v", statsResult.Response, wantScopeIDs)
	}
	for index, scopeID := range wantScopeIDs {
		if statsResult.Response.ByScope[index].ScopeID != scopeID {
			t.Fatalf("by_scope[%d] = %q, want %q", index, statsResult.Response.ByScope[index].ScopeID, scopeID)
		}
	}
	if statsResult.Response.Inventory.Sources.Total != 0 || statsResult.Response.Inventory.Memory.Entries.Total != 0 {
		t.Fatalf("empty Scope aggregate inventory = %#v", statsResult.Response.Inventory)
	}
	if statsResult.Response.AsOf.IsZero() || len(statsResult.Response.Usage.Daily) != 30 || len(statsResult.Response.Recall.Daily) != 30 {
		t.Fatalf("canonical period = %#v %#v", statsResult.Response.Usage, statsResult.Response.Recall)
	}

	legacy := httptest.NewRequest(http.MethodGet, "/v1/stats?scope_id="+defaultScope.ID(), nil)
	legacy.Header.Set("Authorization", "Bearer "+config.Auth.Token)
	legacyResponse := httptest.NewRecorder()
	handler.ServeHTTP(legacyResponse, legacy)
	if legacyResponse.Code != http.StatusOK || legacyResponse.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("legacy GET /v1/stats = %d %s", legacyResponse.Code, legacyResponse.Body.String())
	}
}

func TestCanonicalStatsUnknownScopeUsesRawRedactedNotFound(t *testing.T) {
	config := applicationTestConfig(t)
	config.Auth.Enabled = true
	config.Auth.Token = "canonical-stats-secret"
	application, err := OpenApplication(t.Context(), config, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeScopeReaderApplication(t, application) })
	handler, err := application.HTTPHandler()
	if err != nil {
		t.Fatal(err)
	}
	const unknownScope = "private-unknown-stats-scope"
	payload, err := json.Marshal(map[string]any{
		"selection": map[string]any{"mode": "exact", "scope_ids": []string{unknownScope}},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/stats", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+config.Auth.Token)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound || response.Header().Get("X-PowerContext-Request-ID") == "" {
		t.Fatalf("unknown Scope stats = %d %s", response.Code, response.Body.String())
	}
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil || envelope.Error.Code != "scope_not_found" {
		t.Fatalf("unknown Scope envelope = %s, err=%v", response.Body.String(), err)
	}
	for _, protected := range []string{unknownScope, config.Auth.Token} {
		if strings.Contains(response.Body.String(), protected) {
			t.Fatalf("unknown Scope response leaked %q: %s", protected, response.Body.String())
		}
	}
	generated, clientErr := newCanonicalStatsClient(t, handler, config.Auth.Token).GetStats(
		t.Context(),
		&canonicalstats.GetStatsRequest{Selection: canonicalstats.NewExactScopeSelectionScopeSelection(canonicalstats.ExactScopeSelection{
			Mode: canonicalstats.ExactScopeSelectionModeExact, ScopeIds: []string{unknownScope},
		})},
	)
	if clientErr == nil || generated != nil {
		t.Fatalf("generated Stats client covered undeclared 404: %#v, %v", generated, clientErr)
	}
}

type canonicalStatsClientSecurity struct{ token string }

func (security canonicalStatsClientSecurity) BearerAuth(
	context.Context,
	canonicalstats.OperationName,
) (canonicalstats.BearerAuth, error) {
	return canonicalstats.BearerAuth{Token: security.token}, nil
}

func newCanonicalStatsClient(t *testing.T, handler http.Handler, token string) *canonicalstats.Client {
	t.Helper()
	client, err := canonicalstats.NewClient(
		"http://powercontext.test",
		canonicalStatsClientSecurity{token: token},
		canonicalstats.WithClient(&http.Client{Transport: scopeSidecarRoundTripper{handler: handler}}),
	)
	if err != nil {
		t.Fatal(err)
	}
	return client
}
