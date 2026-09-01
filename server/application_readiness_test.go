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

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/ob-labs/powercontext-go/inference"
	"github.com/ob-labs/powercontext-go/internal/endpoint"
	servermetrics "github.com/ob-labs/powercontext-go/internal/observability/metrics"
	"github.com/ob-labs/powercontext-go/internal/runtime"
)

func TestConfiguredReadinessIncludesRuntimeAndConfiguredInference(t *testing.T) {
	var embeddingCalls atomic.Int64
	checks, err := configuredReadiness(
		func(context.Context) error { return nil },
		assembledDependencies{
			embeddingReadiness: func(context.Context) error {
				embeddingCalls.Add(1)
				return inference.NewConfigurationError("embedding-model", "secret")
			},
		},
		time.Now,
	)
	if err != nil {
		t.Fatal(err)
	}
	first, err := checks.Run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	second, err := checks.Run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if first.Status() != runtime.Degraded || second.Status() != runtime.Degraded {
		t.Fatalf("statuses = %q, %q", first.Status(), second.Status())
	}
	want := map[string]runtime.CheckStatus{
		"runtime": runtime.CheckReady, "database": runtime.CheckReady,
		"inference.embedding": runtime.CheckMisconfigured,
	}
	got := first.Checks()
	if len(got) != len(want) {
		t.Fatalf("checks = %#v", got)
	}
	for name, status := range want {
		if got[name] != status {
			t.Fatalf("check %q = %q, want %q", name, got[name], status)
		}
	}
	if embeddingCalls.Load() != 1 {
		t.Fatalf("embedding probes = %d, want cached single call", embeddingCalls.Load())
	}
}

func TestGenerationReadinessUsesRawMinimalProviderRequest(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("OPENAI_BASE_URL", "https://provider.test/v1")
	config, err := DefaultConfig()
	if err != nil {
		t.Fatal(err)
	}
	config.Inference.GenerationModel = "openai-chat:test-model"

	var requestBody map[string]any
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, readErr := io.ReadAll(request.Body)
		if readErr != nil {
			return nil, readErr
		}
		if unmarshalErr := json.Unmarshal(body, &requestBody); unmarshalErr != nil {
			return nil, unmarshalErr
		}
		response := map[string]any{
			"id": "probe", "object": "chat.completion", "created": 0, "model": "test-model",
			"choices": []any{map[string]any{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": "ok"},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 2, "completion_tokens": 1, "total_tokens": 3},
		}
		encoded, marshalErr := json.Marshal(response)
		if marshalErr != nil {
			return nil, marshalErr
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewReader(encoded)),
			Request:    request,
		}, nil
	})}
	assembled, err := assembleDependencies(
		config, Dependencies{HTTPClient: httpClient}, noop.NewTracerProvider(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if assembled.generationReadiness == nil {
		t.Fatal("configured generation model did not produce a readiness operation")
	}
	if err := assembled.generationReadiness(t.Context()); err != nil {
		t.Fatal(err)
	}
	if requestBody["max_completion_tokens"] != float64(1) && requestBody["max_tokens"] != float64(1) {
		t.Fatalf("probe max tokens = %#v", requestBody)
	}
	messages, ok := requestBody["messages"].([]any)
	if !ok || len(messages) != 1 {
		t.Fatalf("probe messages = %#v", requestBody["messages"])
	}
	message, ok := messages[0].(map[string]any)
	if !ok || message["role"] != "user" || message["content"] != "Reply with one token." {
		t.Fatalf("probe message = %#v", messages[0])
	}
	if _, tracedSchema := requestBody["response_format"]; tracedSchema {
		t.Fatalf("readiness request unexpectedly used structured output: %#v", requestBody)
	}
}

func TestConfiguredEmbeddingReadinessUsesConfiguredDimension(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("OPENAI_BASE_URL", "https://provider.test/v1")
	config, err := DefaultConfig()
	if err != nil {
		t.Fatal(err)
	}
	config.Inference.EmbeddingModel = "openai:text-embedding-test"
	config.Inference.EmbeddingProfileID = "test-v1"
	config.Inference.EmbeddingDimension = 3
	config.Inference.EmbeddingNormalization = "none"

	var dimensions []float64
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, err
		}
		dimension, ok := payload["dimensions"].(float64)
		if !ok {
			return nil, errors.New("embedding request did not include a dimension")
		}
		dimensions = append(dimensions, dimension)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(
				`{"object":"list","data":[{"object":"embedding","index":0,"embedding":[1,0,0]}],"model":"text-embedding-test","usage":{"prompt_tokens":1,"total_tokens":1}}`,
			)),
			Request: request,
		}, nil
	})}
	assembled, err := assembleDependencies(config, Dependencies{HTTPClient: client}, noop.NewTracerProvider())
	if err != nil {
		t.Fatal(err)
	}
	if err := assembled.embeddingReadiness(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(dimensions) != 1 || dimensions[0] != 3 {
		t.Fatalf("readiness dimensions = %v, want [3]", dimensions)
	}
}

func TestConfiguredEmbeddingReadinessIsUntracedButRuntimeEmbeddingIsTraced(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("OPENAI_BASE_URL", "https://provider.test/v1")
	config, err := DefaultConfig()
	if err != nil {
		t.Fatal(err)
	}
	config.Inference.EmbeddingModel = "openai:text-embedding-test"
	config.Inference.EmbeddingProfileID = "test-v1"
	config.Inference.EmbeddingDimension = 3
	config.Inference.EmbeddingNormalization = "none"

	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(
				`{"object":"list","data":[{"object":"embedding","index":0,"embedding":[1,0,0]}],"model":"text-embedding-test","usage":{"prompt_tokens":1,"total_tokens":1}}`,
			)),
			Request: request,
		}, nil
	})}
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithSpanProcessor(recorder),
	)
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	assembled, err := assembleDependencies(config, Dependencies{HTTPClient: client}, provider)
	if err != nil {
		t.Fatal(err)
	}
	if err := assembled.embeddingReadiness(t.Context()); err != nil {
		t.Fatal(err)
	}
	if spans := recorder.Ended(); len(spans) != 0 {
		t.Fatalf("readiness exported orphan inference spans: %#v", spans)
	}

	ctx, operation := provider.Tracer("test").Start(t.Context(), "powercontext search_memory")
	if _, err := assembled.embeddingModel.Embed(ctx, []string{"private runtime query"}); err != nil {
		t.Fatal(err)
	}
	operation.End()
	spans := recorder.Ended()
	if len(spans) != 2 {
		t.Fatalf("runtime spans = %d, want operation and embedding", len(spans))
	}
	var operationSpan, embeddingSpan sdktrace.ReadOnlySpan
	for _, span := range spans {
		switch span.Name() {
		case "powercontext search_memory":
			operationSpan = span
		case "powercontext inference.embed":
			embeddingSpan = span
		}
	}
	if operationSpan == nil || embeddingSpan == nil ||
		embeddingSpan.Parent().SpanID() != operationSpan.SpanContext().SpanID() {
		t.Fatalf("runtime embedding span was not nested under the application operation: %#v", spans)
	}
}

func TestAssembleDependenciesWithoutEmbeddingConfiguration(t *testing.T) {
	config, err := DefaultConfig()
	if err != nil {
		t.Fatal(err)
	}
	assembled, err := assembleDependencies(config, Dependencies{}, noop.NewTracerProvider())
	if err != nil {
		t.Fatal(err)
	}
	if assembled.embeddingModel != nil || assembled.embeddingReadiness != nil {
		t.Fatalf("unconfigured embedding dependencies = %#v", assembled)
	}
}

func TestApplicationReadinessMakesDatabaseFailureBlockingAndRedacted(t *testing.T) {
	application, handler := readinessTestApplication(
		t,
		func(context.Context) error { return errors.New("secret database URL") },
		assembledDependencies{},
		time.Now,
	)
	response := perform(handler, http.MethodGet, "/health/ready", "")
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("readiness = %d: %s", response.Code, response.Body.String())
	}
	assertReadinessBody(t, response, "not_ready", map[string]string{
		"runtime": "ready", "database": "unavailable",
	})
	if strings.Contains(response.Body.String(), "secret database URL") {
		t.Fatalf("readiness leaked database failure: %s", response.Body.String())
	}
	metrics := perform(handler, http.MethodGet, "/metrics", "")
	if metrics.Code != http.StatusOK || !strings.Contains(metrics.Body.String(), "powercontext_server_runtime_ready 0") {
		t.Fatalf("metrics = %d: %s", metrics.Code, metrics.Body.String())
	}
	if application.metrics == nil {
		t.Fatal("readiness test application has no metrics")
	}
}

func TestApplicationReadinessClassifiesConfiguredInferenceWithoutLeakingFailures(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		dependency string
		operation  runtime.DependencyOperation
		wantStatus string
		wantCheck  string
	}{
		{
			name: "generation ready", dependency: "inference.generation",
			operation:  func(context.Context) error { return nil },
			wantStatus: "ready", wantCheck: "ready",
		},
		{
			name: "generation unavailable", dependency: "inference.generation",
			operation:  func(context.Context) error { return errors.New("secret provider response") },
			wantStatus: "degraded", wantCheck: "unavailable",
		},
		{
			name: "embedding timeout", dependency: "inference.embedding",
			operation: func(context.Context) error {
				return inference.NewTimeoutError("embedding", time.Second)
			},
			wantStatus: "degraded", wantCheck: "timeout",
		},
		{
			name: "embedding unavailable", dependency: "inference.embedding",
			operation:  func(context.Context) error { return errors.New("https://secret-provider.example/v1") },
			wantStatus: "degraded", wantCheck: "unavailable",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			dependencies := assembledDependencies{}
			if test.dependency == "inference.generation" {
				dependencies.generationReadiness = test.operation
			} else {
				dependencies.embeddingReadiness = test.operation
			}
			_, handler := readinessTestApplication(
				t, func(context.Context) error { return nil }, dependencies, time.Now,
			)
			response := perform(handler, http.MethodGet, "/health/ready", "")
			if response.Code != http.StatusOK {
				t.Fatalf("readiness = %d: %s", response.Code, response.Body.String())
			}
			assertReadinessBody(t, response, test.wantStatus, map[string]string{
				"runtime": "ready", "database": "ready", test.dependency: test.wantCheck,
			})
			if strings.Contains(response.Body.String(), "secret") {
				t.Fatalf("readiness leaked inference failure: %s", response.Body.String())
			}
			metrics := perform(handler, http.MethodGet, "/metrics", "")
			if metrics.Code != http.StatusOK || !strings.Contains(metrics.Body.String(), "powercontext_server_runtime_ready 1") {
				t.Fatalf("metrics = %d: %s", metrics.Code, metrics.Body.String())
			}
		})
	}
}

func TestProviderRejectedReadinessReportsRedactedReason(t *testing.T) {
	providerFailure := inference.WrapConfigurationError(
		"provider-rejected", "HTTP 400", errors.New("API_KEY=secret https://credential@example.invalid private Memory"),
	)
	_, handler := readinessTestApplication(
		t,
		func(context.Context) error { return nil },
		assembledDependencies{embeddingReadiness: func(context.Context) error { return providerFailure }},
		time.Now,
	)
	response := perform(handler, http.MethodGet, "/health/ready", "")
	if response.Code != http.StatusOK {
		t.Fatalf("readiness = %d: %s", response.Code, response.Body.String())
	}
	assertReadinessBody(t, response, "degraded", map[string]string{
		"runtime": "ready", "database": "ready",
		"inference.embedding": "misconfigured: provider-rejected (HTTP 400)",
	})
	for _, sentinel := range []string{"API_KEY=secret", "credential@example.invalid", "private Memory"} {
		if strings.Contains(response.Body.String(), sentinel) {
			t.Fatalf("readiness leaked provider failure sentinel %q", sentinel)
		}
	}
}

func TestApplicationReadinessRedactsPlainConfigurationErrors(t *testing.T) {
	_, handler := readinessTestApplication(
		t,
		func(context.Context) error { return nil },
		assembledDependencies{embeddingReadiness: func(context.Context) error {
			return inference.NewConfigurationError("embedding-model", "API_KEY=secret private Memory")
		}},
		time.Now,
	)
	response := perform(handler, http.MethodGet, "/health/ready", "")
	if response.Code != http.StatusOK {
		t.Fatalf("readiness = %d: %s", response.Code, response.Body.String())
	}
	assertReadinessBody(t, response, "degraded", map[string]string{
		"runtime": "ready", "database": "ready", "inference.embedding": "misconfigured",
	})
	for _, sentinel := range []string{"API_KEY=secret", "private Memory"} {
		if strings.Contains(response.Body.String(), sentinel) {
			t.Fatalf("readiness leaked configuration sentinel %q", sentinel)
		}
	}
}

func readinessTestApplication(
	t *testing.T,
	database runtime.DependencyOperation,
	dependencies assembledDependencies,
	clock runtime.Clock,
) (*Application, http.Handler) {
	t.Helper()
	checks, err := configuredReadiness(database, dependencies, clock)
	if err != nil {
		t.Fatal(err)
	}
	metrics, err := servermetrics.New()
	if err != nil {
		t.Fatal(err)
	}
	application := &Application{
		runtime: runtime.New(), readiness: checks, metrics: metrics,
		capabilities: runtime.EmptyCapabilities(), tracing: noop.NewTracerProvider(),
	}
	application.endpoint = endpoint.NewHandler(endpoint.HandlerOptions{
		Capabilities: application.getCapabilities,
		Readiness:    application.getReadiness,
	})
	handler, err := application.HTTPHandler()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = application.Close(context.Background()) })
	return application, handler
}

func assertReadinessBody(
	t *testing.T,
	response *httptest.ResponseRecorder,
	wantStatus string,
	wantChecks map[string]string,
) {
	t.Helper()
	var body struct {
		Status string            `json:"status"`
		Checks map[string]string `json:"checks"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Status != wantStatus || !maps.Equal(body.Checks, wantChecks) {
		t.Fatalf("readiness = %#v, want status=%q checks=%#v", body, wantStatus, wantChecks)
	}
}
