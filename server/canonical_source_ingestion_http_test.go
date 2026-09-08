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
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/go-faster/jx"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	canonicalsource "github.com/ob-labs/powercontext-go/api/canonical/sources"
	v1 "github.com/ob-labs/powercontext-go/api/v1"
	"github.com/ob-labs/powercontext-go/source"
)

type sourceLegacyClientSecurity string

func (s sourceLegacyClientSecurity) BearerAuth(context.Context, v1.OperationName) (v1.BearerAuth, error) {
	return v1.BearerAuth{Token: string(s)}, nil
}

func TestCanonicalSourceIngestionAndCheckpointHTTP(t *testing.T) {
	var logs bytes.Buffer
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()), sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { _ = provider.Shutdown(t.Context()) })

	config := applicationTestConfig(t)
	config.Auth.Enabled = true
	config.Auth.Token = "source-ingestion-secret"
	config.Logging.Access = true
	config.MCP.Enabled = true
	config.MCP.Path = DefaultMCPPath
	application, err := OpenApplication(t.Context(), config, Dependencies{
		Logger: newJSONTestLogger(t, &logs), TracerProvider: provider,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeScopeReaderApplication(t, application) })

	scopeID := applicationDefaultScope(t, application).ID()
	client, handler := sourceHTTPClient(t, application, config.Auth.Token)
	legacyClient, err := v1.NewClient(
		"http://powercontext.test", sourceLegacyClientSecurity(config.Auth.Token),
		v1.WithClient(&http.Client{Transport: scopeSidecarRoundTripper{handler: handler}}),
	)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := legacyClient.CaptureContentSource(t.Context(), &v1.CaptureContentSourceRequest{
		ScopeID: scopeID, SourceID: "legacy-after-source-sidecar", Content: "legacy path remains reachable",
	})
	if err != nil {
		t.Fatal(err)
	}
	if captured, ok := legacy.(*v1.CaptureContentSourceResponseHeaders); !ok || captured.Response.Status != v1.CaptureStatusAccepted {
		t.Fatalf("legacy CaptureContentSource() = %#v", legacy)
	}
	assertCanonicalSourceMCPIsolation(t, handler, config)
	binding := canonicalsource.ConnectorBinding{
		ScopeID: scopeID, BindingID: "checkpoint-http", ConnectorName: "fixture", ConnectorVersion: "1",
	}

	unauthenticated, _ := sourceHTTPClient(t, application, "")
	denied, err := unauthenticated.GetConnectorCheckpoint(t.Context(), &canonicalsource.GetConnectorCheckpointRequest{Binding: binding})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := denied.(*canonicalsource.UnauthorizedHeaders); !ok {
		t.Fatalf("unauthenticated checkpoint read = %T", denied)
	}

	manifest := canonicalSourceIngestionManifest(t)
	registered, err := client.RegisterSourceDefinition(t.Context(), &canonicalsource.RegisterSourceDefinitionRequest{Manifest: manifest})
	if err != nil {
		t.Fatal(err)
	}
	registeredManifest, ok := registered.(*canonicalsource.SourceDefinitionManifest)
	if !ok || registeredManifest.Fingerprint != manifest.Fingerprint {
		t.Fatalf("RegisterSourceDefinition() = %#v", registered)
	}
	idempotent, err := client.RegisterSourceDefinition(t.Context(), &canonicalsource.RegisterSourceDefinitionRequest{Manifest: manifest})
	if err != nil {
		t.Fatal(err)
	}
	if repeated, ok := idempotent.(*canonicalsource.SourceDefinitionManifest); !ok || repeated.Fingerprint != manifest.Fingerprint {
		t.Fatalf("idempotent RegisterSourceDefinition() = %#v", idempotent)
	}
	conflictingManifest := canonicalSourceIngestionManifest(t, `{"type":"object","properties":{"name":{"type":"string"},"definition_version":{"type":"string"},"materialization":{"const":"captured"},"large":{"type":"string"}},"required":["name","definition_version","materialization","large"]}`)
	definitionConflict, err := client.RegisterSourceDefinition(t.Context(), &canonicalsource.RegisterSourceDefinitionRequest{Manifest: conflictingManifest})
	if err != nil {
		t.Fatal(err)
	}
	assertCanonicalSourceError(t, definitionConflict, "source_conflict", manifest.Fingerprint, conflictingManifest.Fingerprint)
	invalidManifest := manifest
	invalidManifest.Fingerprint = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	invalidDefinition, err := client.RegisterSourceDefinition(t.Context(), &canonicalsource.RegisterSourceDefinitionRequest{Manifest: invalidManifest})
	if err != nil {
		t.Fatal(err)
	}
	assertCanonicalSourceError(t, invalidDefinition, "invalid_source_ingestion", invalidManifest.Fingerprint)

	observation := canonicalSourceIngestionObservation(t, manifest, "remote-item", `{"name":"remote-item","definition_version":"1","materialization":"captured","large":9007199254740993}`)
	accepted, err := client.SubmitSourceObservation(t.Context(), &canonicalsource.SubmitSourceObservationRequest{
		ScopeID: scopeID, Observation: observation,
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt, ok := accepted.(*canonicalsource.SourceObservationReceipt)
	if !ok || receipt.Position < 1 || receipt.Source.Name != manifest.Name || receipt.Source.SourceID != observation.Name {
		t.Fatalf("SubmitSourceObservation() = %#v", accepted)
	}
	repeatedObservation, err := client.SubmitSourceObservation(t.Context(), &canonicalsource.SubmitSourceObservationRequest{
		ScopeID: scopeID, Observation: observation,
	})
	if err != nil {
		t.Fatal(err)
	}
	if repeated, ok := repeatedObservation.(*canonicalsource.SourceObservationReceipt); !ok || repeated.Position != receipt.Position {
		t.Fatalf("idempotent SubmitSourceObservation() = %#v", repeatedObservation)
	}

	conflicting := canonicalSourceIngestionObservation(t, manifest, "remote-item", `{"name":"remote-item","definition_version":"1","materialization":"captured","large":9007199254740994}`)
	conflict, err := client.SubmitSourceObservation(t.Context(), &canonicalsource.SubmitSourceObservationRequest{
		ScopeID: scopeID, Observation: conflicting,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertCanonicalSourceError(t, conflict, "source_conflict", "remote-item", manifest.Fingerprint)

	missingDefinition := canonicalSourceIngestionObservation(t, manifest, "missing-definition", `{"name":"missing-definition","definition_version":"1","materialization":"captured","large":1}`)
	missingDefinition.SourceType = "remote.missing"
	missing, err := client.SubmitSourceObservation(t.Context(), &canonicalsource.SubmitSourceObservationRequest{
		ScopeID: scopeID, Observation: missingDefinition,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertCanonicalSourceError(t, missing, "source_definition_not_found", "missing-definition")

	initial, err := client.GetConnectorCheckpoint(t.Context(), &canonicalsource.GetConnectorCheckpointRequest{Binding: binding})
	if err != nil {
		t.Fatal(err)
	}
	assertCanonicalCheckpoint(t, initial, "null")
	storedNull, err := client.CommitConnectorCheckpoint(t.Context(), &canonicalsource.CommitConnectorCheckpointRequest{
		Binding: binding, Checkpoint: canonicalSourceRawJSON("null"), Expected: canonicalSourceRawJSON("null"),
	})
	if err != nil {
		t.Fatal(err)
	}
	assertCanonicalCheckpoint(t, storedNull, "null")
	checkpoint := `{"offset":9007199254740993,"flags":[true,1.0]}`
	advanced, err := client.CommitConnectorCheckpoint(t.Context(), &canonicalsource.CommitConnectorCheckpointRequest{
		Binding: binding, Checkpoint: canonicalSourceRawJSON(checkpoint), Expected: canonicalSourceRawJSON("null"),
	})
	if err != nil {
		t.Fatal(err)
	}
	assertCanonicalCheckpoint(t, advanced, checkpoint)
	stale, err := client.CommitConnectorCheckpoint(t.Context(), &canonicalsource.CommitConnectorCheckpointRequest{
		Binding: binding, Checkpoint: canonicalSourceRawJSON(`{"offset":4}`), Expected: canonicalSourceRawJSON("null"),
	})
	if err != nil {
		t.Fatal(err)
	}
	assertCanonicalSourceError(t, stale, "connector_checkpoint_conflict", checkpoint, scopeID)
	cleared, err := client.CommitConnectorCheckpoint(t.Context(), &canonicalsource.CommitConnectorCheckpointRequest{
		Binding: binding, Checkpoint: canonicalSourceRawJSON("null"), Expected: canonicalSourceRawJSON(checkpoint),
	})
	if err != nil {
		t.Fatal(err)
	}
	assertCanonicalCheckpoint(t, cleared, "null")
	restored, err := client.CommitConnectorCheckpoint(t.Context(), &canonicalsource.CommitConnectorCheckpointRequest{
		Binding: binding, Checkpoint: canonicalSourceRawJSON(checkpoint), Expected: canonicalSourceRawJSON("null"),
	})
	if err != nil {
		t.Fatal(err)
	}
	assertCanonicalCheckpoint(t, restored, checkpoint)
	conflictingBinding := binding
	conflictingBinding.ConnectorVersion = "2"
	bindingConflict, err := client.GetConnectorCheckpoint(t.Context(), &canonicalsource.GetConnectorCheckpointRequest{Binding: conflictingBinding})
	if err != nil {
		t.Fatal(err)
	}
	assertCanonicalSourceError(t, bindingConflict, "connector_checkpoint_conflict", scopeID)

	databasePath, err := SQLiteDSN(config.Database.SQLite.URL)
	if err != nil {
		t.Fatal(err)
	}
	database, err := sql.Open("sqlite3", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	var sourceCount int
	if err := database.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM pc_sources WHERE scope_id = ? AND source_type = ?", scopeID, manifest.Name).Scan(&sourceCount); err != nil {
		t.Fatal(err)
	}
	if sourceCount != 1 {
		t.Fatalf("accepted remote Source count = %d, want 1", sourceCount)
	}
	privateCheckpointBinding := canonicalsource.ConnectorBinding{
		ScopeID: "private-unknown-checkpoint-scope", BindingID: "private-checkpoint-binding", ConnectorName: "fixture", ConnectorVersion: "1",
	}
	for _, test := range []struct {
		name, path string
		request    any
	}{
		{
			name: "get", path: "/v1/connector-checkpoints/get",
			request: &canonicalsource.GetConnectorCheckpointRequest{Binding: privateCheckpointBinding},
		},
		{
			name: "commit", path: "/v1/connector-checkpoints/commit",
			request: &canonicalsource.CommitConnectorCheckpointRequest{
				Binding: privateCheckpointBinding, Checkpoint: canonicalSourceRawJSON("null"), Expected: canonicalSourceRawJSON("null"),
			},
		},
	} {
		t.Run("unknown Scope checkpoint "+test.name, func(t *testing.T) {
			payload, marshalErr := json.Marshal(test.request)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			request := httptest.NewRequest(http.MethodPost, test.path, bytes.NewReader(payload))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Authorization", "Bearer "+config.Auth.Token)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404: %s", response.Code, response.Body.String())
			}
			assertCanonicalRawSourceError(t, response, "scope_not_found", privateCheckpointBinding.ScopeID, privateCheckpointBinding.BindingID, config.Auth.Token)
		})
	}
	assertScopeHasNoPersistentWork(t, database, privateCheckpointBinding.ScopeID)

	privateScope := "private-unknown-source-ingestion-scope"
	privatePayload := `{"scope_id":"` + privateScope + `","observation":{"name":"private-item","definition_version":"1","materialization":"captured","source_type":"` + manifest.Name + `","definition_fingerprint":"` + manifest.Fingerprint + `","payload":{"name":"private-item","definition_version":"1","materialization":"captured","large":1},"projections":[]}}`
	privateRequest := httptest.NewRequest(http.MethodPost, "/v1/source-observations", strings.NewReader(privatePayload))
	privateRequest.Header.Set("Content-Type", "application/json")
	privateRequest.Header.Set("Authorization", "Bearer "+config.Auth.Token)
	privateResponse := httptest.NewRecorder()
	beforeSpans := len(recorder.Ended())
	handler.ServeHTTP(privateResponse, privateRequest)
	if privateResponse.Code != http.StatusNotFound {
		t.Fatalf("unknown Scope status = %d: %s", privateResponse.Code, privateResponse.Body.String())
	}
	if privateResponse.Header().Get("X-PowerContext-Request-ID") == "" {
		t.Fatal("unknown Scope response has no request ID")
	}
	for _, private := range []string{privateScope, config.Auth.Token, "private-item", manifest.Fingerprint} {
		if strings.Contains(privateResponse.Body.String(), private) || strings.Contains(logs.String(), private) {
			t.Fatalf("response or logs leak %q", private)
		}
		for _, span := range recorder.Ended()[beforeSpans:] {
			if strings.Contains(fmt.Sprintf("%v %v %v", span.Attributes(), span.Events(), span.Status()), private) {
				t.Fatalf("span %q leaks %q", span.Name(), private)
			}
		}
	}
	assertScopeHasNoPersistentWork(t, database, privateScope)

	if closeErr := application.Close(t.Context()); closeErr != nil {
		t.Fatal(closeErr)
	}
	application, err = OpenApplication(t.Context(), config, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	client, _ = sourceHTTPClient(t, application, config.Auth.Token)
	restarted, err := client.GetConnectorCheckpoint(t.Context(), &canonicalsource.GetConnectorCheckpointRequest{Binding: binding})
	if err != nil {
		t.Fatal(err)
	}
	assertCanonicalCheckpoint(t, restarted, checkpoint)
}

func canonicalSourceIngestionManifest(t *testing.T, schema ...string) canonicalsource.SourceDefinitionManifest {
	t.Helper()
	identity, err := source.NewDefinitionIdentity("remote.http", "1")
	if err != nil {
		t.Fatal(err)
	}
	definitionSchema := `{"type":"object","properties":{"name":{"type":"string"},"definition_version":{"type":"string"},"materialization":{"const":"captured"},"large":{"type":"integer"}},"required":["name","definition_version","materialization","large"]}`
	if len(schema) > 0 {
		definitionSchema = schema[0]
	}
	manifest, err := source.NewDefinitionManifest(identity, jsontextValue(definitionSchema), nil)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var result canonicalsource.SourceDefinitionManifest
	if err := json.Unmarshal(payload, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func canonicalSourceIngestionObservation(
	t *testing.T,
	manifest canonicalsource.SourceDefinitionManifest,
	name, payload string,
) canonicalsource.SourceObservation {
	t.Helper()
	ref, err := source.NewRef(manifest.Name, name)
	if err != nil {
		t.Fatal(err)
	}
	observation, err := source.NewSourceObservation(
		ref, manifest.Version, manifest.Fingerprint, nil, jsontextValue(payload), nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(observation)
	if err != nil {
		t.Fatal(err)
	}
	var result canonicalsource.SourceObservation
	if err := json.Unmarshal(encoded, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func canonicalSourceRawJSON(value string) jx.Raw { return jx.Raw([]byte(value)) }

func jsontextValue(value string) jsontext.Value { return jsontext.Value(value) }

func assertCanonicalCheckpoint(t *testing.T, result any, want string) {
	t.Helper()
	state, ok := result.(*canonicalsource.ConnectorCheckpointState)
	if !ok {
		t.Fatalf("checkpoint = %#v, want %s", result, want)
	}
	actual, err := source.NewConnectorCheckpoint(jsontext.Value(state.Checkpoint))
	if err != nil {
		t.Fatal(err)
	}
	expected, err := source.NewConnectorCheckpoint(jsontext.Value(want))
	if err != nil {
		t.Fatal(err)
	}
	equal, err := source.EqualConnectorCheckpoints(actual, expected)
	if err != nil || !equal {
		t.Fatalf("checkpoint = %#v, want %s", result, want)
	}
	if strings.Contains(want, "9007199254740993") && !strings.Contains(string(state.Checkpoint), "9007199254740993") {
		t.Fatalf("checkpoint lost large integer: %s", state.Checkpoint)
	}
}

func assertCanonicalSourceError(t *testing.T, result any, want string, private ...string) {
	t.Helper()
	response, ok := result.(interface {
		GetResponse() canonicalsource.ErrorResponse
	})
	if !ok {
		t.Fatalf("error response = %T, want %q", result, want)
	}
	payload, err := json.Marshal(response.GetResponse())
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil || envelope.Error.Code != want {
		t.Fatalf("error response = %T %s, want %q (err=%v)", result, payload, want, err)
	}
	for _, value := range private {
		if strings.Contains(string(payload), value) {
			t.Fatalf("error response leaks %q: %s", value, payload)
		}
	}
}

func assertCanonicalRawSourceError(t *testing.T, response *httptest.ResponseRecorder, want string, private ...string) {
	t.Helper()
	if response.Header().Get("X-PowerContext-Request-ID") == "" {
		t.Fatal("response has no request ID")
	}
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil || envelope.Error.Code != want {
		t.Fatalf("error response = %s, want %q (err=%v)", response.Body.String(), want, err)
	}
	for _, value := range private {
		if strings.Contains(response.Body.String(), value) {
			t.Fatalf("error response leaks %q: %s", value, response.Body.String())
		}
	}
}

func assertCanonicalSourceMCPIsolation(t *testing.T, handler http.Handler, config ProcessConfig) {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "source-ingestion-test", Version: "1"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint: "http://powercontext.test" + strings.TrimRight(config.MCP.Path, "/") + "/",
		HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			request = request.Clone(request.Context())
			request.Header.Set("Authorization", "Bearer "+config.Auth.Token)
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
	tools, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(tools.Tools))
	for index, tool := range tools.Tools {
		names[index] = tool.Name
	}
	if !slices.Contains(names, "capture_content_source") {
		t.Fatalf("MCP tools do not include legacy capture: %v", names)
	}
	for _, upstreamOnly := range []string{
		"register_source_definition", "submit_source_observation", "get_connector_checkpoint", "commit_connector_checkpoint",
	} {
		if slices.Contains(names, upstreamOnly) {
			t.Fatalf("canonical Source operation became an MCP tool: %s", upstreamOnly)
		}
	}
}
