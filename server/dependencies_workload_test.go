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
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/trace/noop"

	"github.com/ob-labs/powercontext-go/artifact/memory"
	"github.com/ob-labs/powercontext-go/inference"
)

func TestAssembleDependenciesRoutesOpenAIWorkloadOverrides(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("OPENAI_BASE_URL", "https://default.test/v1")
	config := workloadTestConfig(t)
	config.Inference.Generation = workloadConfig(
		"https://generation.test/v1", "X-Generation", "generation-secret", map[string]any{"top_p": 0.4},
	)
	config.Inference.Embedding = workloadConfig(
		"https://embedding.test/v1", "X-Embedding", "embedding-secret", map[string]any{"encoding_format": "float"},
	)
	rerankTimeout := 200 * time.Millisecond
	rerankRequests := 2
	config.Inference.Rerank = &RerankInferenceConfig{
		Model: "openai-chat:rerank-model",
		Workload: workloadConfig(
			"https://rerank.test/v1", "X-Rerank", "rerank-secret", map[string]any{"temperature": 0.9, "top_p": 0.2},
		),
		Timeout: &rerankTimeout, MaxRequests: &rerankRequests,
	}
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}

	transport := &workloadTransport{}
	assembled, err := assembleDependencies(
		config, Dependencies{HTTPClient: &http.Client{Transport: transport}}, noop.NewTracerProvider(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if assembled.generationReadiness == nil || assembled.embeddingReadiness == nil || assembled.rerankReadiness == nil {
		t.Fatalf("readiness operations = %#v", assembled)
	}
	config.Inference.Generation.BaseURL = "https://mutated.test/v1"
	config.Inference.Generation.Headers.values["X-Generation"] = "mutated-secret"
	config.Inference.Generation.ModelSettings["top_p"] = 0.9
	if err := assembled.generationReadiness(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := assembled.embeddingReadiness(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := assembled.rerankReadiness(t.Context()); err != nil {
		t.Fatal(err)
	}

	requests := transport.Requests()
	assertWorkloadRequest(t, requests, "generation-model", "generation.test", "/v1/chat/completions", "X-Generation", "generation-secret", map[string]any{"top_p": 0.4})
	assertWorkloadRequest(t, requests, "embedding-model", "embedding.test", "/v1/embeddings", "X-Embedding", "embedding-secret", map[string]any{"dimensions": float64(3), "encoding_format": "float"})
	assertWorkloadRequest(t, requests, "rerank-model", "rerank.test", "/v1/chat/completions", "X-Rerank", "rerank-secret", map[string]any{"temperature": 0.0, "top_p": 0.2})
	for _, request := range requests {
		wantHeader := map[string]string{
			"generation-model": "X-Generation", "embedding-model": "X-Embedding", "rerank-model": "X-Rerank",
		}[request.model]
		for _, header := range []string{"X-Generation", "X-Embedding", "X-Rerank"} {
			if header != wantHeader && request.header.Get(header) != "" {
				t.Fatalf("request %q unexpectedly crossed workload header %q: %#v", request.model, header, request.header)
			}
		}
	}
}

func TestRerankWorkloadInheritsGenerationAndOverridesCaseInsensitiveValues(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")
	config := workloadTestConfig(t)
	config.Inference.Generation = &InferenceWorkloadConfig{
		BaseURL: "https://generation.test/v1",
		Headers: InferenceHeaders{values: map[string]string{
			"X-Shared": "generation-secret",
		}},
		ModelSettings: map[string]any{"top_p": 0.4, "seed": 7},
	}
	config.Inference.Rerank = &RerankInferenceConfig{Workload: &InferenceWorkloadConfig{
		Headers:       InferenceHeaders{values: map[string]string{"x-shared": "rerank-secret"}},
		ModelSettings: map[string]any{"top_p": 0.2},
	}}
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	transport := &workloadTransport{}
	assembled, err := assembleDependencies(
		config, Dependencies{HTTPClient: &http.Client{Transport: transport}}, noop.NewTracerProvider(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if assembled.rerankReadiness == nil {
		t.Fatal("generation-backed rerank override did not create independent readiness")
	}
	if err := assembled.rerankReadiness(t.Context()); err != nil {
		t.Fatal(err)
	}
	requests := transport.Requests()
	if len(requests) != 1 {
		t.Fatalf("requests = %#v", requests)
	}
	request := requests[0]
	if request.host != "generation.test" || request.model != "generation-model" ||
		request.header.Get("X-Shared") != "rerank-secret" || request.body["top_p"] != 0.2 ||
		request.body["seed"] != float64(7) || request.body["temperature"] != 0.0 {
		t.Fatalf("rerank request = %#v", request)
	}
}

func TestIndependentRerankUsesConfiguredRetryAndTimeout(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("OPENAI_BASE_URL", "https://default.test/v1")
	t.Run("retry", func(t *testing.T) {
		config := workloadTestConfig(t)
		config.Inference.GenerationModel = ""
		config.Inference.Generation = nil
		config.Inference.Rerank = &RerankInferenceConfig{
			Model:    "openai-chat:rerank-model",
			Workload: workloadConfig("https://rerank.test/v1", "X-Rerank", "rerank-secret", map[string]any{"temperature": 0.5}),
		}
		if err := config.Validate(); err != nil {
			t.Fatal(err)
		}
		transport := &workloadTransport{rerankResponses: []string{`{"unexpected":true}`, `{"selected_ranks":[1]}`}}
		assembled, err := assembleDependencies(
			config, Dependencies{HTTPClient: &http.Client{Transport: transport}}, noop.NewTracerProvider(),
		)
		if err != nil {
			t.Fatal(err)
		}
		if assembled.generationReadiness != nil || assembled.rerankReadiness == nil || assembled.memoryReranker == nil {
			t.Fatalf("independent rerank assembly = %#v", assembled)
		}
		decision, err := assembled.memoryReranker.Rerank(t.Context(), "query", []memory.Hit{{Text: "candidate"}}, 1)
		if err != nil {
			t.Fatal(err)
		}
		if decision.Usage().Requests != 2 {
			t.Fatalf("rerank requests = %d, want 2", decision.Usage().Requests)
		}
		requests := transport.Requests()
		if len(requests) != 2 {
			t.Fatalf("provider calls = %d, want retry", len(requests))
		}
		for _, request := range requests {
			if request.host != "rerank.test" || request.header.Get("X-Rerank") != "rerank-secret" || request.body["temperature"] != 0.0 {
				t.Fatalf("rerank request = %#v", request)
			}
		}
	})
	t.Run("timeout", func(t *testing.T) {
		config := workloadTestConfig(t)
		config.Inference.GenerationModel = ""
		config.Inference.Generation = nil
		timeout := 15 * time.Millisecond
		config.Inference.Rerank = &RerankInferenceConfig{Model: "openai-chat:rerank-model", Timeout: &timeout}
		if err := config.Validate(); err != nil {
			t.Fatal(err)
		}
		transport := &workloadTransport{waitForCancellation: true}
		assembled, err := assembleDependencies(
			config, Dependencies{HTTPClient: &http.Client{Transport: transport}}, noop.NewTracerProvider(),
		)
		if err != nil {
			t.Fatal(err)
		}
		_, err = assembled.memoryReranker.Rerank(t.Context(), "query", []memory.Hit{{Text: "candidate"}}, 1)
		var timeoutError *inference.TimeoutError
		if !errors.As(err, &timeoutError) || timeoutError.Timeout() != timeout {
			t.Fatalf("rerank error = %v, want timeout %s", err, timeout)
		}
	})
}

func workloadTestConfig(t *testing.T) ProcessConfig {
	t.Helper()
	config, err := DefaultConfig()
	if err != nil {
		t.Fatal(err)
	}
	config.Runtime.MemoryRerankEnabled = true
	config.Inference.GenerationModel = "openai-chat:generation-model"
	config.Inference.EmbeddingModel = "openai:embedding-model"
	config.Inference.EmbeddingProfileID = "workload-test"
	config.Inference.EmbeddingDimension = 3
	config.Inference.EmbeddingNormalization = "none"
	return config
}

func workloadConfig(baseURL, header, value string, settings map[string]any) *InferenceWorkloadConfig {
	return &InferenceWorkloadConfig{
		BaseURL: baseURL, Headers: InferenceHeaders{values: map[string]string{header: value}}, ModelSettings: settings,
	}
}

type workloadRequest struct {
	host   string
	path   string
	model  string
	header http.Header
	body   map[string]any
}

type workloadTransport struct {
	mu                  sync.Mutex
	requests            []workloadRequest
	rerankResponses     []string
	waitForCancellation bool
}

func (t *workloadTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if t.waitForCancellation {
		<-request.Context().Done()
		return nil, request.Context().Err()
	}
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	var decoded map[string]any
	if decodeErr := json.Unmarshal(body, &decoded); decodeErr != nil {
		return nil, decodeErr
	}
	model, _ := decoded["model"].(string)
	t.mu.Lock()
	t.requests = append(t.requests, workloadRequest{
		host: request.URL.Host, path: request.URL.Path, model: model, header: request.Header.Clone(), body: decoded,
	})
	content := `{"selected_ranks":[1]}`
	if model == "rerank-model" && len(t.rerankResponses) > 0 {
		content = t.rerankResponses[0]
		t.rerankResponses = t.rerankResponses[1:]
	}
	t.mu.Unlock()

	response := map[string]any{
		"id": "completion", "object": "chat.completion", "created": 0, "model": model,
		"choices": []any{map[string]any{
			"index": 0, "message": map[string]any{"role": "assistant", "content": content}, "finish_reason": "stop",
		}},
		"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
	}
	if request.URL.Path == "/v1/embeddings" {
		response = map[string]any{
			"object": "list", "data": []any{map[string]any{"object": "embedding", "index": 0, "embedding": []float64{1, 0, 0}}},
			"model": model, "usage": map[string]any{"prompt_tokens": 1, "total_tokens": 1},
		}
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return nil, err
	}
	return &http.Response{
		StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(bytes.NewReader(encoded)), Request: request,
	}, nil
}

func (t *workloadTransport) Requests() []workloadRequest {
	t.mu.Lock()
	defer t.mu.Unlock()
	result := make([]workloadRequest, len(t.requests))
	copy(result, t.requests)
	return result
}

func assertWorkloadRequest(
	t *testing.T,
	requests []workloadRequest,
	model, host, path, header, value string,
	wantBody map[string]any,
) {
	t.Helper()
	for _, request := range requests {
		if request.model != model {
			continue
		}
		if request.host != host || request.path != path || request.header.Get(header) != value {
			t.Fatalf("request for %q = %#v", model, request)
		}
		for key, want := range wantBody {
			if request.body[key] != want {
				t.Fatalf("request for %q body[%q] = %#v, want %#v", model, key, request.body[key], want)
			}
		}
		return
	}
	t.Fatalf("no request for model %q in %#v", model, requests)
}
