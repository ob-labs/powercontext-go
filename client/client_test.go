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

package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-faster/jx"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	v1 "github.com/ob-labs/powercontext-go/api/v1"
)

const capabilitiesJSON = `{"source_types":[],"artifact_families":[],"memory_extraction":false,"experience_generation":false,"managed_skill_generation":false,"external_skill_registry":false,"handoff_generation":false,"search_modes":[],"context_versions":[]}`

func repeatValue[T any](value T, count int) []T {
	result := make([]T, count)
	for index := range result {
		result[index] = value
	}
	return result
}

func TestClientRejectsUndeclaredSuccessStatus(t *testing.T) {
	t.Parallel()
	client, err := New("https://powercontext.test", Options{HTTPClient: &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return response(http.StatusOK, `{"status":"accepted","source":{"name":"content","source_id":"turn-1"},"position":1}`), nil
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.CaptureContentSource(context.Background(), &v1.CaptureContentSourceRequest{
		ScopeID: "project", SourceID: "turn-1", Content: "content",
	})
	if result != nil {
		t.Fatalf("result = %T, want nil", result)
	}
	var serverError *ServerError
	if !errors.As(err, &serverError) || serverError.StatusCode != http.StatusOK {
		t.Fatalf("error = %#v, want ServerError with status 200", err)
	}
}

func TestClientValidatesRequestsBeforeTransport(t *testing.T) {
	t.Parallel()
	transportCalled := false
	client, err := New("https://powercontext.test", Options{HTTPClient: &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			transportCalled = true
			return response(http.StatusInternalServerError, ""), nil
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		path string
		call func() error
	}{
		{"/v1/sources/content", func() error {
			_, err := client.CaptureContentSource(context.Background(), &v1.CaptureContentSourceRequest{
				SourceID: "turn-1", Content: "content",
			})
			return err
		}},
		{"/v1/stats", func() error {
			_, err := client.GetStats(context.Background(), v1.GetStatsParams{})
			return err
		}},
	} {
		err := test.call()
		var requestError *RequestValidationError
		if !errors.As(err, &requestError) || requestError.Path != test.path {
			t.Fatalf("error = %#v, want RequestValidationError for %s", err, test.path)
		}
	}
	if transportCalled {
		t.Fatal("invalid request reached HTTP transport")
	}
}

func TestClientRejectsCombinedCandidateEvidenceBeforeTransport(t *testing.T) {
	t.Parallel()
	transportCalled := false
	client, err := New("https://powercontext.test", Options{HTTPClient: &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			transportCalled = true
			return response(http.StatusCreated, `{}`), nil
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	source := v1.SourceReference{Name: "content", SourceID: "task-1"}
	artifact := v1.ArtifactReference{Family: "experience", ArtifactID: "exp-1", Revision: 1}
	request := &v1.ProposeExperienceRequest{
		ScopeID: "project",
		Proposal: v1.ExperienceProposal{
			Situation: "OpenAPI changed.", Action: "Regenerate the Client.",
			Outcome: "Transport stays aligned.", Lesson: "Keep contract tests green.",
		},
		SourceRefs:   repeatValue(source, 20),
		ArtifactRefs: repeatValue(artifact, 13),
	}
	result, err := client.ProposeExperience(context.Background(), request)
	if result != nil {
		t.Fatalf("result = %T, want nil", result)
	}
	var requestError *RequestValidationError
	var limit *v1.CombinedEvidenceLimitError
	if !errors.As(err, &requestError) || !errors.As(err, &limit) {
		t.Fatalf("error = %#v, want RequestValidationError wrapping CombinedEvidenceLimitError", err)
	}
	if transportCalled {
		t.Fatal("transport was called for a semantically invalid request")
	}
}

func TestClientRejectsCombinedCandidateEvidenceInSuccessResponse(t *testing.T) {
	t.Parallel()
	source := map[string]any{"name": "content", "source_id": "task-1"}
	artifact := map[string]any{"family": "experience", "artifact_id": "exp-1", "revision": 1}
	payload, err := json.Marshal(map[string]any{
		"candidate_id": "candidate-1", "version": 1, "family": "experience", "status": "pending",
		"proposal": map[string]any{
			"situation": "OpenAPI changed.", "action": "Regenerate the Client.",
			"outcome": "Transport stays aligned.", "lesson": "Keep contract tests green.",
		},
		"source_refs": repeatValue(source, 20), "artifact_refs": repeatValue(artifact, 13),
		"target": nil, "reason": nil, "result_artifact": nil, "decision_reason": nil,
	})
	if err != nil {
		t.Fatal(err)
	}
	client, err := New("https://powercontext.test", Options{HTTPClient: &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			result := response(http.StatusCreated, string(payload))
			result.Header.Set(requestIDHeader, "0123456789abcdef")
			return result, nil
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.ProposeExperience(context.Background(), &v1.ProposeExperienceRequest{
		ScopeID: "project",
		Proposal: v1.ExperienceProposal{
			Situation: "A", Action: "B", Outcome: "C", Lesson: "D",
		},
		SourceRefs:   []v1.SourceReference{{Name: "content", SourceID: "task-1"}},
		ArtifactRefs: []v1.ArtifactReference{},
	})
	if result != nil {
		t.Fatalf("result = %T, want nil", result)
	}
	var invalid *InvalidResponseError
	var limit *v1.CombinedEvidenceLimitError
	if !errors.As(err, &invalid) || !errors.As(err, &limit) {
		t.Fatalf("error = %#v, want InvalidResponseError wrapping CombinedEvidenceLimitError", err)
	}
	if invalid.RequestID != "0123456789abcdef" {
		t.Fatalf("request ID = %q", invalid.RequestID)
	}
}

func TestClientPreservesDeclaredServerErrorContext(t *testing.T) {
	t.Parallel()
	client, err := New("https://powercontext.test", Options{HTTPClient: &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			result := response(http.StatusServiceUnavailable, `{"error":{"code":"runtime_not_ready","message":"The Runtime is not ready.","details":{"component":"memory"}}}`)
			result.Header.Set(requestIDHeader, "request-123")
			return result, nil
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.GetReadiness(context.Background())
	if result != nil {
		t.Fatalf("result = %T, want nil", result)
	}
	var serverError *ServerError
	if !errors.As(err, &serverError) {
		t.Fatalf("error = %T, want *ServerError", err)
	}
	if serverError.StatusCode != http.StatusServiceUnavailable || serverError.RequestID != "request-123" ||
		serverError.Code != "runtime_not_ready" || serverError.Message != "The Runtime is not ready." ||
		serverError.Details["component"] != "memory" {
		t.Fatalf("server error = %#v", serverError)
	}
}

func TestClientRejectsPlaintextNonLoopbackByDefault(t *testing.T) {
	t.Parallel()
	callerTransport := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response(http.StatusOK, capabilitiesJSON), nil
	})}
	for _, test := range []struct {
		name      string
		serverURL string
		options   Options
	}{
		{name: "owned transport without token", serverURL: "http://memory.example"},
		{name: "private network host", serverURL: "http://192.168.1.10:8000"},
		{
			name:      "owned transport with token",
			serverURL: "http://memory.example",
			options:   Options{BearerToken: "probe-token"},
		},
		{
			name:      "caller transport without token",
			serverURL: "http://memory.example",
			options:   Options{HTTPClient: callerTransport},
		},
		{
			name:      "caller transport with token",
			serverURL: "http://memory.example",
			options: Options{
				BearerToken: "probe-token",
				HTTPClient:  callerTransport,
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := New(test.serverURL, test.options)
			if err == nil || !IsConfigurationError(err) {
				t.Fatalf("New() error = %v, want configuration error", err)
			}
		})
	}
}

func TestClientAllowsLoopbackPlaintext(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name        string
		serverURL   string
		bearerToken string
	}{
		{name: "IPv4 loopback with bearer", serverURL: "http://127.0.0.1:8000", bearerToken: "probe-token"},
		{name: "localhost loopback", serverURL: "http://localhost:8000"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var authorization string
			client, err := New(test.serverURL, Options{
				BearerToken: test.bearerToken,
				HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
					authorization = request.Header.Get("Authorization")
					return response(http.StatusOK, capabilitiesJSON), nil
				})},
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.GetCapabilities(t.Context()); err != nil {
				t.Fatal(err)
			}
			wantAuthorization := ""
			if test.bearerToken != "" {
				wantAuthorization = "Bearer " + test.bearerToken
			}
			if authorization != wantAuthorization {
				t.Fatalf("Authorization = %q, want %q", authorization, wantAuthorization)
			}
		})
	}
}

func TestClientTrustOverrideRequiresCallerTransport(t *testing.T) {
	t.Parallel()
	_, err := New("http://memory.example", Options{TrustTransportSecurity: true})
	if err == nil || !IsConfigurationError(err) {
		t.Fatalf("New() error = %v, want configuration error", err)
	}
}

func TestClientPlaintextNonLoopbackErrorNamesPolicyWithoutLeakingURL(t *testing.T) {
	t.Parallel()
	const rejectedHost = "private-memory.example"
	_, err := New("http://"+rejectedHost, Options{BearerToken: "probe-token"})
	if err == nil {
		t.Fatal("New() accepted plaintext HTTP to a non-loopback host")
	}
	if _, ok := errors.AsType[*PlaintextNonLoopbackError](err); !ok {
		t.Fatalf("New() error = %T %v, want PlaintextNonLoopbackError", err, err)
	}
	if !IsConfigurationError(err) {
		t.Fatalf("IsConfigurationError(%T) = false, want compatibility with configuration errors", err)
	}
	rendered := fmt.Sprintf("%v\n%#v", err, err)
	if !strings.Contains(rendered, "non-loopback") {
		t.Fatalf("refusal = %q, want the upstream policy token", rendered)
	}
	if strings.Contains(rendered, rejectedHost) {
		t.Fatalf("refusal leaked the rejected host: %q", rendered)
	}
}

func TestClientAllowsExplicitlyTrustedCallerTransport(t *testing.T) {
	t.Parallel()
	var authorization string
	client, err := New("http://memory.example", Options{
		BearerToken: "probe-token",
		HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			authorization = request.Header.Get("Authorization")
			return response(http.StatusOK, capabilitiesJSON), nil
		})},
		TrustTransportSecurity: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetCapabilities(t.Context()); err != nil {
		t.Fatal(err)
	}
	if authorization != "Bearer probe-token" {
		t.Fatalf("Authorization = %q, want %q", authorization, "Bearer probe-token")
	}
}

func TestClientRejectsPlaintextNonLoopbackPerRequestOverride(t *testing.T) {
	t.Parallel()
	requests := 0
	client, err := New("https://powercontext.test", Options{
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			requests++
			return response(http.StatusOK, capabilitiesJSON), nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	override, err := url.Parse("http://memory.example")
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.GetCapabilities(v1.WithServerURL(t.Context(), override))
	if err == nil || !IsConfigurationError(err) {
		t.Fatalf("GetCapabilities() error = %v, want configuration error", err)
	}
	if _, ok := errors.AsType[*PlaintextNonLoopbackError](err); !ok {
		t.Fatalf("GetCapabilities() error = %T %v, want PlaintextNonLoopbackError", err, err)
	}
	if requests != 0 {
		t.Fatalf("requests = %d, want 0", requests)
	}
}

func TestClientSendsBearerAndPreservesCallerHTTPClient(t *testing.T) {
	t.Parallel()
	var authorization string
	original := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		authorization = request.Header.Get("Authorization")
		return response(http.StatusOK, capabilitiesJSON), nil
	})}
	client, err := New("https://powercontext.test/root/", Options{
		BearerToken: "server-secret", HTTPClient: original,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.GetCapabilities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := result.(*v1.CapabilitiesHeaders); !ok {
		t.Fatalf("response type = %T", result)
	}
	if authorization != "Bearer server-secret" {
		t.Fatalf("Authorization = %q", authorization)
	}
	if original.CheckRedirect != nil {
		t.Fatal("caller-owned HTTP client was mutated")
	}
}

func TestClientWithoutTokenOmitsAuthorization(t *testing.T) {
	t.Parallel()
	var authorizationPresent bool
	client, err := New("https://powercontext.test", Options{HTTPClient: &http.Client{
		Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			_, authorizationPresent = request.Header["Authorization"]
			return response(http.StatusOK, capabilitiesJSON), nil
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetCapabilities(context.Background()); err != nil {
		t.Fatal(err)
	}
	if authorizationPresent {
		t.Fatal("Authorization header was sent without an explicit token")
	}
}

func TestClientKeepsGenericServerErrorForInvalidErrorBody(t *testing.T) {
	t.Parallel()
	client, err := New("https://powercontext.test", Options{HTTPClient: &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			result := response(http.StatusInternalServerError, "Internal Server Error")
			result.Header.Set("Content-Type", "text/plain")
			return result, nil
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.GetLiveness(context.Background())
	if result != nil {
		t.Fatalf("result = %T, want nil", result)
	}
	var serverError *ServerError
	if !errors.As(err, &serverError) || serverError.StatusCode != http.StatusInternalServerError || serverError.Code != "" {
		t.Fatalf("error = %#v, want generic ServerError with status 500", err)
	}
}

func TestClientRejectsInvalidSuccessResponse(t *testing.T) {
	t.Parallel()
	client, err := New("https://powercontext.test", Options{HTTPClient: &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			result := response(http.StatusOK, `{"status":"ok","unexpected":true}`)
			result.Header.Set(requestIDHeader, "request-123")
			return result, nil
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.GetLiveness(context.Background())
	if result != nil {
		t.Fatalf("result = %T, want nil", result)
	}
	var invalid *InvalidResponseError
	if !errors.As(err, &invalid) || invalid.Path != "/health/live" || invalid.RequestID != "request-123" {
		t.Fatalf("error = %#v, want InvalidResponseError with request context", err)
	}
}

func TestClientWrapsHTTPTransportFailure(t *testing.T) {
	t.Parallel()
	connectionError := errors.New("connection refused")
	client, err := New("https://powercontext.test", Options{HTTPClient: &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, connectionError
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.GetLiveness(context.Background())
	if result != nil {
		t.Fatalf("result = %T, want nil", result)
	}
	var transportError *TransportError
	if !errors.As(err, &transportError) || transportError.Path != "/health/live" || !errors.Is(err, connectionError) {
		t.Fatalf("error = %#v, want wrapping TransportError", err)
	}
}

func TestClientRendersAndDownloadsHandoffReportWithoutMutatingRequest(t *testing.T) {
	t.Parallel()
	var downloadFlags []bool
	client, err := New("https://powercontext.test", Options{HTTPClient: &http.Client{
		Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			body, readErr := io.ReadAll(request.Body)
			if readErr != nil {
				return nil, readErr
			}
			var payload struct {
				Download bool `json:"download"`
			}
			if decodeErr := json.Unmarshal(body, &payload); decodeErr != nil {
				return nil, decodeErr
			}
			downloadFlags = append(downloadFlags, payload.Download)
			result := response(http.StatusOK, "# Handoff Report\n")
			result.Header.Set("Content-Type", "text/markdown")
			return result, nil
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	request := v1.GetHandoffReportRequest{ScopeID: "scope-1"}
	rendered, err := client.RenderHandoffReport(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	downloaded, err := client.DownloadHandoffReport(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if rendered != "# Handoff Report\n" || string(downloaded) != rendered {
		t.Fatalf("rendered = %q, downloaded = %q", rendered, downloaded)
	}
	if fmt.Sprint(downloadFlags) != "[false true]" {
		t.Fatalf("download flags = %v, want [false true]", downloadFlags)
	}
	if request.Download.IsSet() || request.Format.IsSet() {
		t.Fatal("handoff report helpers mutated the caller-owned request")
	}
}

func TestClientDownloadPreservesCanonicalJSONBytes(t *testing.T) {
	t.Parallel()
	digest := "sha256:" + strings.Repeat("a", 64)
	canonical := []byte(`{"format":"json","report":{},"markdown":null,"selection_digest":"` + digest + `","report_digest":"` + digest + `"}`)
	client, err := New("https://powercontext.test", Options{HTTPClient: &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return response(http.StatusOK, string(canonical)), nil
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	content, err := client.DownloadHandoffReport(context.Background(), v1.GetHandoffReportRequest{
		ScopeID: "scope-1", Format: v1.NewOptReportFormat(v1.ReportFormatJSON),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(content, canonical) {
		t.Fatalf("download = %q, want exact canonical bytes %q", content, canonical)
	}
}

func TestClientSpanInjectsW3CTraceContext(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithSpanProcessor(recorder),
	)
	t.Cleanup(func() { _ = provider.Shutdown(t.Context()) })
	var traceparent string
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		traceparent = request.Header.Get("traceparent")
		return response(http.StatusOK, capabilitiesJSON), nil
	})
	client, err := New("https://powercontext.test", Options{
		HTTPClient: &http.Client{Transport: transport}, TracerProvider: provider,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetCapabilities(t.Context()); err != nil {
		t.Fatal(err)
	}
	if traceparent == "" {
		t.Fatal("client did not inject W3C traceparent")
	}
	spans := recorder.Ended()
	if len(spans) != 1 || spans[0].Name() != "PowerContextClient get_capabilities" {
		t.Fatalf("spans = %#v", spans)
	}
	for _, value := range spans[0].Attributes() {
		if string(value.Key) == "powercontext.operation.outcome" && value.Value.AsString() == "success" {
			return
		}
	}
	t.Fatalf("span attributes = %#v, want successful operation outcome", spans[0].Attributes())
}

func TestClientRejectsRedirects(t *testing.T) {
	t.Parallel()
	requests := 0
	callerRedirects := 0
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		result := response(http.StatusTemporaryRedirect, "")
		result.Header.Set("Location", "/redirected")
		result.Request = request
		return result, nil
	})
	original := &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			callerRedirects++
			return nil
		},
	}
	client, err := New("https://powercontext.test", Options{HTTPClient: original})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetCapabilities(context.Background()); err == nil {
		t.Fatal("redirect status was accepted")
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
	if callerRedirects != 0 || original.CheckRedirect == nil {
		t.Fatal("client did not isolate its no-redirect policy from the caller-owned HTTP client")
	}
}

func TestClientURLValidationDoesNotExposeRejectedValue(t *testing.T) {
	t.Parallel()
	for _, value := range []string{
		"", "powercontext.test", "ftp://powercontext.test",
		"https://user:very-secret@powercontext.test", "https://powercontext.test?q=secret",
		"https://powercontext.test/#secret",
	} {
		_, err := New(value, Options{})
		if err == nil || !IsConfigurationError(err) {
			t.Fatalf("New(%q) error = %v", value, err)
		}
		for _, secret := range []string{"very-secret", "q=secret", "#secret"} {
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("error leaks %q: %v", secret, err)
			}
		}
	}
}

func TestClientConfigurationErrorRepresentationDoesNotExposeURLCredentials(t *testing.T) {
	t.Parallel()
	const secret = "do-not-log"
	_, err := New("https://user:"+secret+"@powercontext.test", Options{})
	if err == nil {
		t.Fatal("credential-bearing Server URL was accepted")
	}
	representations := strings.Join([]string{
		err.Error(), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err),
	}, "\n")
	if strings.Contains(representations, secret) {
		t.Fatalf("configuration error leaked URL credentials: %s", representations)
	}
}

func TestClientOptionsRepresentationsHideBearerToken(t *testing.T) {
	t.Parallel()
	const secret = "secret-token-that-must-not-leak"
	options := Options{BearerToken: secret, Timeout: 3 * time.Second, HTTPClient: &http.Client{}}
	encoded, err := json.Marshal(options)
	if err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	slog.New(slog.NewJSONHandler(&log, nil)).Info("client configuration", "options", options)
	representations := strings.Join([]string{
		fmt.Sprint(options), fmt.Sprintf("%+v", options), fmt.Sprintf("%#v", options), string(encoded), log.String(),
	}, "\n")
	if strings.Contains(representations, secret) {
		t.Fatalf("client options leaked bearer token: %s", representations)
	}
	if !strings.Contains(representations, "token_configured") {
		t.Fatalf("safe configuration signal missing: %s", representations)
	}
}

func TestAsServerErrorPreservesStableContext(t *testing.T) {
	t.Parallel()
	details := v1.ErrorDetailDetails{"current_version": jx.Raw(`3`)}
	response := &v1.ConflictHeaders{
		XPowerContextRequestID: v1.NewOptString("0123456789abcdef"),
		Response: v1.ErrorResponse{Error: v1.ErrorDetail{
			Code: "candidate_conflict", Message: "The Candidate version is stale.",
			Details: v1.NewNilErrorDetailDetails(details),
		}},
	}
	serverError, ok := AsServerError(response)
	if !ok {
		t.Fatal("conflict response was not recognized")
	}
	if serverError.StatusCode != http.StatusConflict || serverError.RequestID != "0123456789abcdef" ||
		serverError.Code != "candidate_conflict" || serverError.Details["current_version"] != float64(3) {
		t.Fatalf("server error = %#v", serverError)
	}
}

func response(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}, "X-PowerContext-Request-ID": []string{"0123456789abcdef"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }
