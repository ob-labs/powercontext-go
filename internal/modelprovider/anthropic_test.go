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

package modelprovider

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
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ob-labs/powercontext-go/inference"
)

func TestAnthropicConfigRepresentationsAreRedacted(t *testing.T) {
	config := AnthropicConfig{
		APIKey:  "private-api-key",
		BaseURL: "https://private-provider.test/custom/",
		Headers: http.Header{"X-Private": []string{"private-header"}},
		Query:   url.Values{"private-query": []string{"private-query-value"}},
	}
	display := fmt.Sprintf("%v", config)
	goDisplay := fmt.Sprintf("%#v", config)
	var logged bytes.Buffer
	slog.New(slog.NewTextHandler(&logged, nil)).Info("anthropic", "config", config)
	if !strings.Contains(display, "APIKeyConfigured:true") || !strings.Contains(display, "HeaderCount:1") ||
		!strings.Contains(goDisplay, "BaseURLConfigured:true") || !strings.Contains(goDisplay, "QueryCount:1") {
		t.Fatalf("Anthropic configuration display lost redacted state: %q / %q", display, goDisplay)
	}
	if output := logged.String(); !strings.Contains(output, "config.api_key_configured=true") ||
		!strings.Contains(output, "config.header_count=1") || !strings.Contains(output, "config.query_count=1") {
		t.Fatalf("Anthropic configuration log lost redacted state: %q", output)
	}
	representations := []string{display, goDisplay, logged.String()}
	for _, secret := range []string{
		"private-api-key", "private-provider", "private-header", "private-query", "private-query-value",
	} {
		for _, representation := range representations {
			if strings.Contains(representation, secret) {
				t.Fatalf("Anthropic configuration leaked %q in %q", secret, representation)
			}
		}
	}
}

type capturedAnthropicRequest struct {
	path          string
	apiKey        string
	authorization string
	version       string
	headers       http.Header
	body          map[string]any
}

type anthropicFake struct {
	mu        sync.Mutex
	requests  []capturedAnthropicRequest
	responses []any
	statuses  []int
}

func (f *anthropicFake) RoundTrip(request *http.Request) (*http.Response, error) {
	encoded, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	var body map[string]any
	if len(encoded) > 0 && json.Unmarshal(encoded, &body) != nil {
		return nil, errors.New("invalid request JSON")
	}
	f.mu.Lock()
	f.requests = append(f.requests, capturedAnthropicRequest{
		path:          request.URL.RequestURI(),
		apiKey:        request.Header.Get("X-Api-Key"),
		authorization: request.Header.Get("Authorization"),
		version:       request.Header.Get("Anthropic-Version"),
		headers:       request.Header.Clone(),
		body:          body,
	})
	status := http.StatusOK
	if len(f.statuses) > 0 {
		status = f.statuses[0]
		f.statuses = f.statuses[1:]
	}
	var response any = map[string]any{}
	if len(f.responses) > 0 {
		response = f.responses[0]
		f.responses = f.responses[1:]
	}
	f.mu.Unlock()

	responseBody, err := json.Marshal(response)
	if err != nil {
		return nil, err
	}
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(responseBody)),
		Request:    request,
	}, nil
}

func (f *anthropicFake) Client() *http.Client { return &http.Client{Transport: f} }

func (f *anthropicFake) Requests() []capturedAnthropicRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.requests)
}

func TestAnthropicConfigDoesNotRetainMutableHeadersOrQuery(t *testing.T) {
	fake := &anthropicFake{responses: []any{anthropicResponse("msg_1", "ok", 1, 1)}}
	headers := http.Header{"X-Workload": []string{"original-header"}}
	query := url.Values{"workload": []string{"original-query"}}
	route, err := Resolve("anthropic:claude-test", Generation)
	if err != nil {
		t.Fatal(err)
	}
	model, err := NewAnthropicTextModel(route, AnthropicConfig{
		APIKey: "test-key", BaseURL: "https://provider.test/custom/", HTTPClient: fake.Client(),
		Headers: headers, Query: query,
	})
	if err != nil {
		t.Fatal(err)
	}
	headers.Set("X-Workload", "mutated-header")
	query.Set("workload", "mutated-query")
	if _, err := model.Complete(t.Context(), textRequestForProviderTest(t)); err != nil {
		t.Fatal(err)
	}

	requests := fake.Requests()
	if len(requests) != 1 || requests[0].path != "/custom/v1/messages?workload=original-query" ||
		requests[0].headers.Get("X-Workload") != "original-header" {
		t.Fatalf("Anthropic request retained caller mutation: %#v", requests)
	}
}

func TestAnthropicMatchesFrozenPromptedOutputWireConversation(t *testing.T) {
	fake := &anthropicFake{responses: []any{
		anthropicResponse("msg_1", `{"missing":"value"}`, 5, 2),
		anthropicResponse("msg_2", `{"value":"stable"}`, 7, 3),
	}}
	route, err := Resolve("anthropic:claude-test", Generation)
	if err != nil {
		t.Fatal(err)
	}
	model, err := NewAnthropicTextModel(route, AnthropicConfig{
		APIKey: "test-key", BaseURL: "https://provider.test", HTTPClient: fake.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	settings, _ := inference.NewGenerationSettings(floatPointer(0), nil)
	generator, err := inference.NewPromptedGenerator[openAITestInput, openAITestOutput](
		model, "Return a value.", openAITestCodec(t), nil, settings,
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := generator.Generate(context.Background(), openAITestInput{Value: "bounded"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output.Value != "stable" || result.Usage.Requests != 2 || *result.Usage.InputTokens != 12 || *result.Usage.OutputTokens != 5 {
		t.Fatalf("result = %#v", result)
	}

	requests := fake.Requests()
	if len(requests) != 2 || requests[0].path != "/v1/messages" {
		t.Fatalf("requests = %#v", requests)
	}
	if requests[0].apiKey != "test-key" || requests[0].authorization != "" || requests[0].version != "2023-06-01" {
		t.Fatalf("headers = %#v", requests[0])
	}
	if requests[0].body["max_tokens"] != float64(defaultAnthropicMaxTokens) || requests[0].body["stream"] != false {
		t.Fatalf("request controls = %#v", requests[0].body)
	}
	if requests[0].body["temperature"] != float64(0) {
		t.Fatalf("temperature = %#v", requests[0].body["temperature"])
	}

	system := objectSlice(t, requests[0].body["system"])
	if len(system) != 2 || system[0]["text"] != "Return a value." || !strings.HasPrefix(system[1]["text"].(string), "\nAlways respond with a JSON object") {
		t.Fatalf("system = %#v", system)
	}
	firstMessages := objectSlice(t, requests[0].body["messages"])
	if roles(firstMessages) != "user" || anthropicText(t, firstMessages[0]) != `{"value":"bounded"}` {
		t.Fatalf("first messages = %#v", firstMessages)
	}
	secondMessages := objectSlice(t, requests[1].body["messages"])
	if roles(secondMessages) != "user,assistant,user" {
		t.Fatalf("second roles = %s", roles(secondMessages))
	}
	if anthropicText(t, secondMessages[1]) != `{"missing":"value"}` || !strings.Contains(anthropicText(t, secondMessages[2]), "Fix the errors and try again.") {
		t.Fatalf("retry messages = %#v", secondMessages[1:])
	}
}

func TestAnthropicBearerAuthAndTextBlockBoundaries(t *testing.T) {
	fake := &anthropicFake{responses: []any{map[string]any{
		"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-test",
		"content": []any{
			map[string]any{"type": "text", "text": `{"value":`},
			map[string]any{"type": "thinking", "thinking": "private", "signature": "sig"},
			map[string]any{"type": "text", "text": `"stable"}`},
		},
		"stop_reason": "end_turn", "stop_sequence": nil,
		"usage": map[string]any{"input_tokens": 4, "output_tokens": 2},
	}}}
	route, _ := Resolve("gateway/anthropic:claude-test", Generation)
	model, err := NewAnthropicTextModel(route, AnthropicConfig{
		APIKey: "gateway-key", BaseURL: "https://gateway.test", AuthMode: AnthropicBearerToken,
		HTTPClient: fake.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := model.Complete(context.Background(), textRequestForProviderTest(t))
	if err != nil {
		t.Fatal(err)
	}
	if response.Content() != "{\"value\":\n\n\"stable\"}" {
		t.Fatalf("content = %q", response.Content())
	}
	request := fake.Requests()[0]
	if request.apiKey != "" || request.authorization != "Bearer gateway-key" {
		t.Fatalf("auth headers = %#v", request)
	}
}

func TestAnthropicSDKRetriesAreDisabledAndErrorsAreSanitized(t *testing.T) {
	tests := []struct {
		status int
		check  func(error) bool
	}{
		{status: http.StatusBadRequest, check: func(err error) bool {
			var target *inference.ConfigurationError
			return errors.As(err, &target)
		}},
		{status: http.StatusTooManyRequests, check: func(err error) bool {
			var target *inference.UnavailableError
			return errors.As(err, &target)
		}},
	}
	for _, test := range tests {
		t.Run(http.StatusText(test.status), func(t *testing.T) {
			fake := &anthropicFake{statuses: []int{test.status}, responses: []any{
				map[string]any{"type": "error", "error": map[string]any{
					"type": "invalid_request_error", "message": "secret-provider-body",
				}},
			}}
			route, _ := Resolve("anthropic:claude-test", Generation)
			model, err := NewAnthropicTextModel(route, AnthropicConfig{
				APIKey: "test-key", BaseURL: "https://provider.test", HTTPClient: fake.Client(),
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = model.Complete(context.Background(), textRequestForProviderTest(t))
			if !test.check(err) || strings.Contains(err.Error(), "secret-provider-body") {
				t.Fatalf("error = %v", err)
			}
			if cause := errors.Unwrap(err); cause == nil {
				t.Fatalf("error %T did not retain the Anthropic SDK cause", err)
			}
			if len(fake.Requests()) != 1 {
				t.Fatalf("SDK retried request %d times", len(fake.Requests()))
			}
		})
	}
}

func anthropicResponse(id, content string, inputTokens, outputTokens int64) map[string]any {
	return map[string]any{
		"id": id, "type": "message", "role": "assistant", "model": "claude-test",
		"content":     []any{map[string]any{"type": "text", "text": content}},
		"stop_reason": "end_turn", "stop_sequence": nil,
		"usage": map[string]any{"input_tokens": inputTokens, "output_tokens": outputTokens},
	}
}

func anthropicText(t *testing.T, message map[string]any) string {
	t.Helper()
	blocks := objectSlice(t, message["content"])
	if len(blocks) != 1 || blocks[0]["type"] != "text" {
		t.Fatalf("content blocks = %#v", blocks)
	}
	value, ok := blocks[0]["text"].(string)
	if !ok {
		t.Fatalf("text = %T", blocks[0]["text"])
	}
	return value
}
