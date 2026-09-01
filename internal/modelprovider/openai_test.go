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
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ob-labs/powercontext-go/inference"
)

type openAITestInput struct {
	Value string `json:"value"`
}

type openAITestOutput struct {
	Value string `json:"value"`
}

type capturedOpenAIRequest struct {
	host          string
	path          string
	authorization string
	apiKey        string
	headers       http.Header
	body          map[string]any
}

type openAIFake struct {
	t         *testing.T
	mu        sync.Mutex
	requests  []capturedOpenAIRequest
	responses []any
	statuses  []int
}

func (f *openAIFake) RoundTrip(request *http.Request) (*http.Response, error) {
	encoded, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	var body map[string]any
	if len(encoded) > 0 && json.Unmarshal(encoded, &body) != nil {
		return nil, errors.New("invalid request JSON")
	}
	f.mu.Lock()
	f.requests = append(f.requests, capturedOpenAIRequest{
		host: request.URL.Host, path: request.URL.RequestURI(),
		authorization: request.Header.Get("Authorization"),
		apiKey:        request.Header.Get("Api-Key"), headers: request.Header.Clone(), body: body,
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

func (f *openAIFake) Client() *http.Client { return &http.Client{Transport: f} }

func (f *openAIFake) Requests() []capturedOpenAIRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.requests)
}

func TestOpenAIChatMatchesPromptedOutputWireConversation(t *testing.T) {
	fake := &openAIFake{t: t, responses: []any{
		chatResponse("chat_1", `{"missing":"value"}`, 5, 2),
		chatResponse("chat_2", `{"value":"stable"}`, 7, 3),
	}}
	route, err := Resolve("openai-chat:gpt-test", Generation)
	if err != nil {
		t.Fatal(err)
	}
	model, err := NewOpenAITextModel(route, OpenAIConfig{
		APIKey: "test-key", BaseURL: "https://provider.test/v1", HTTPClient: fake.Client(),
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
	if len(requests) != 2 || requests[0].path != "/v1/chat/completions" || requests[0].authorization != "Bearer test-key" {
		t.Fatalf("requests = %#v", requests)
	}
	firstMessages := objectSlice(t, requests[0].body["messages"])
	if roles(firstMessages) != "system,system,user" {
		t.Fatalf("first roles = %s", roles(firstMessages))
	}
	secondMessages := objectSlice(t, requests[1].body["messages"])
	if roles(secondMessages) != "system,system,user,assistant,user" {
		t.Fatalf("second roles = %s", roles(secondMessages))
	}
	if secondMessages[3]["content"] != `{"missing":"value"}` || !strings.Contains(secondMessages[4]["content"].(string), "Fix the errors and try again.") {
		t.Fatalf("retry messages = %#v", secondMessages[3:])
	}
	format := requests[0].body["response_format"].(map[string]any)
	if format["type"] != "json_object" {
		t.Fatalf("response format = %#v", format)
	}
	if temperature, ok := requests[0].body["temperature"]; !ok || temperature != float64(0) {
		t.Fatalf("temperature = %#v", requests[0].body["temperature"])
	}
}

func TestOpenAIResponsesPreservesProviderMessageIdentityOnRetry(t *testing.T) {
	fake := &openAIFake{t: t, responses: []any{
		responsesResponse("resp_1", "msg_1", `{"missing":"value"}`, 5, 2),
		responsesResponse("resp_2", "msg_2", `{"value":"stable"}`, 7, 3),
	}}
	route, err := Resolve("openai:gpt-test", Generation)
	if err != nil {
		t.Fatal(err)
	}
	model, err := NewOpenAITextModel(route, OpenAIConfig{
		APIKey: "test-key", BaseURL: "https://provider.test/v1", HTTPClient: fake.Client(),
		IncludeEncryptedReasoning: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	generator, err := inference.NewPromptedGenerator[openAITestInput, openAITestOutput](
		model, "Return a value.", openAITestCodec(t), nil, inference.GenerationSettings{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := generator.Generate(context.Background(), openAITestInput{Value: "bounded"}); err != nil {
		t.Fatal(err)
	}

	requests := fake.Requests()
	if len(requests) != 2 || requests[0].path != "/v1/responses" {
		t.Fatalf("requests = %#v", requests)
	}
	firstInput := objectSlice(t, requests[0].body["input"])
	if roles(firstInput) != "system,user" || !strings.Contains(firstInput[0]["content"].(string), "compatible with this schema") {
		t.Fatalf("first input = %#v", firstInput)
	}
	secondInput := objectSlice(t, requests[1].body["input"])
	if roles(secondInput) != "system,user,assistant,user" {
		t.Fatalf("second roles = %s: %#v", roles(secondInput), secondInput)
	}
	if secondInput[2]["id"] != "msg_1" || secondInput[2]["type"] != "message" || secondInput[2]["status"] != "completed" {
		t.Fatalf("assistant replay = %#v", secondInput[2])
	}
	retryContent, ok := secondInput[3]["content"].([]any)
	if !ok || len(retryContent) != 1 || retryContent[0].(map[string]any)["type"] != "input_text" {
		t.Fatalf("retry content = %#v", secondInput[3]["content"])
	}
	include := requests[0].body["include"].([]any)
	if len(include) != 1 || include[0] != "reasoning.encrypted_content" {
		t.Fatalf("include = %#v", include)
	}
}

func TestOpenAISDKRetriesAreDisabledAndErrorsAreSanitized(t *testing.T) {
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
			fake := &openAIFake{t: t, statuses: []int{test.status}, responses: []any{
				map[string]any{"error": map[string]any{"message": "secret-provider-body", "type": "test"}},
			}}
			route, _ := Resolve("openai-chat:gpt-test", Generation)
			model, err := NewOpenAITextModel(route, OpenAIConfig{
				APIKey: "test-key", BaseURL: "https://provider.test/v1", HTTPClient: fake.Client(),
			})
			if err != nil {
				t.Fatal(err)
			}
			request := textRequestForProviderTest(t)
			_, err = model.Complete(context.Background(), request)
			if !test.check(err) || strings.Contains(err.Error(), "secret-provider-body") {
				t.Fatalf("error = %v", err)
			}
			if cause := errors.Unwrap(err); cause == nil {
				t.Fatalf("error %T did not retain the OpenAI SDK cause", err)
			}
			if len(fake.Requests()) != 1 {
				t.Fatalf("SDK retried request %d times", len(fake.Requests()))
			}
		})
	}
}

func TestOpenAIEmbeddingTransportUsesOrderedBatchAndUsage(t *testing.T) {
	fake := &openAIFake{t: t, responses: []any{map[string]any{
		"object": "list", "model": "text-embedding-test",
		"data": []any{
			map[string]any{"object": "embedding", "index": 0, "embedding": []float64{1, 2}},
			map[string]any{"object": "embedding", "index": 1, "embedding": []float64{3, 4}},
		},
		"usage": map[string]any{"prompt_tokens": 9, "total_tokens": 9},
	}}}
	route, _ := Resolve("openai:text-embedding-test", Embedding)
	transport, err := NewOpenAIEmbeddingTransport(route, OpenAIConfig{
		APIKey: "test-key", BaseURL: "https://provider.test/v1", HTTPClient: fake.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := transport.Embed(context.Background(), embeddingRequestForProviderTest(t, []string{"alpha", "beta"}, inference.EmbeddingDocument, 384))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(result.Inputs(), []string{"alpha", "beta"}) || !slices.Equal(result.Embeddings()[1], []float64{3, 4}) || *result.Usage().InputTokens != 9 {
		t.Fatalf("result = %#v", result)
	}
	request := fake.Requests()[0]
	if request.path != "/v1/embeddings" || request.body["model"] != "text-embedding-test" || request.body["dimensions"] != float64(384) {
		t.Fatalf("request = %#v", request)
	}
}

func openAITestCodec(t *testing.T) *inference.JSONCodec[openAITestInput, openAITestOutput] {
	t.Helper()
	codec, err := inference.NewJSONCodec[openAITestInput, openAITestOutput](
		[]byte(`{"properties":{"value":{"type":"string"}},"required":["value"],"title":"Output","type":"object"}`),
		nil,
		func(value []byte) (openAITestOutput, error) {
			var output openAITestOutput
			if err := json.Unmarshal(value, &output); err != nil {
				return output, err
			}
			if output.Value == "" {
				return output, errors.New("value is required")
			}
			return output, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return codec
}

func textRequestForProviderTest(t *testing.T) inference.TextRequest {
	t.Helper()
	var captured inference.TextRequest
	model := providerRequestCapture(func(request inference.TextRequest) { captured = request })
	generator, err := inference.NewPromptedGenerator[openAITestInput, openAITestOutput](
		model, "Return a value.", openAITestCodec(t), nil, inference.GenerationSettings{},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = generator.Generate(context.Background(), openAITestInput{Value: "bounded"})
	return captured
}

type providerRequestCapture func(inference.TextRequest)

func (capture providerRequestCapture) Complete(_ context.Context, request inference.TextRequest) (inference.TextResponse, error) {
	capture(request)
	return inference.NewTextResponse(`{"value":"stable"}`, inference.Usage{})
}

func chatResponse(id, content string, inputTokens, outputTokens int64) map[string]any {
	return map[string]any{
		"id": id, "object": "chat.completion", "created": 0, "model": "gpt-test",
		"choices": []any{map[string]any{
			"index": 0, "message": map[string]any{"role": "assistant", "content": content}, "finish_reason": "stop",
		}},
		"usage": map[string]any{"prompt_tokens": inputTokens, "completion_tokens": outputTokens, "total_tokens": inputTokens + outputTokens},
	}
}

func responsesResponse(id, messageID, content string, inputTokens, outputTokens int64) map[string]any {
	return map[string]any{
		"id": id, "object": "response", "created_at": 0, "status": "completed", "model": "gpt-test",
		"output": []any{map[string]any{
			"id": messageID, "type": "message", "role": "assistant", "status": "completed",
			"content": []any{map[string]any{"type": "output_text", "text": content, "annotations": []any{}}},
		}},
		"usage": map[string]any{"input_tokens": inputTokens, "output_tokens": outputTokens, "total_tokens": inputTokens + outputTokens},
	}
}

func objectSlice(t *testing.T, value any) []map[string]any {
	t.Helper()
	items, ok := value.([]any)
	if !ok {
		t.Fatalf("value is %T, want []any", value)
	}
	result := make([]map[string]any, len(items))
	for index, item := range items {
		result[index], ok = item.(map[string]any)
		if !ok {
			t.Fatalf("item %d is %T", index, item)
		}
	}
	return result
}

func roles(messages []map[string]any) string {
	values := make([]string, len(messages))
	for index, message := range messages {
		values[index], _ = message["role"].(string)
	}
	return strings.Join(values, ",")
}

func floatPointer(value float64) *float64 { return &value }
