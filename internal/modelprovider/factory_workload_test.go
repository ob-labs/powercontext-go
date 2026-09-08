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
	json "encoding/json/v2"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ob-labs/powercontext-go/inference"
)

func TestFactoryAnthropicWorkloadBaseURLUsesEnvironmentAPIKey(t *testing.T) {
	type requestDetails struct {
		method        string
		path          string
		apiKey        string
		authorization string
	}
	requests := make(chan requestDetails, 4)
	provider := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests <- requestDetails{
			method:        request.Method,
			path:          request.URL.Path,
			apiKey:        request.Header.Get("X-Api-Key"),
			authorization: request.Header.Get("Authorization"),
		}
		response.Header().Set("Content-Type", "application/json")
		encoded, err := json.Marshal(anthropicResponse("msg_1", "ok", 1, 1))
		if err != nil {
			t.Errorf("encode Anthropic response: %v", err)
			return
		}
		if _, err := response.Write(encoded); err != nil {
			t.Errorf("write Anthropic response: %v", err)
		}
	}))
	t.Cleanup(provider.Close)

	factory, err := NewFactory(MilestoneB, testEnvironment{
		"ANTHROPIC_API_KEY":  "environment-key",
		"ANTHROPIC_BASE_URL": "http://127.0.0.1:1",
	}.lookup, provider.Client())
	if err != nil {
		t.Fatal(err)
	}
	workload := WorkloadConfig{BaseURL: provider.URL + "/custom-anthropic/"}
	model, err := factory.TextModelWithWorkload("anthropic:claude-test", workload)
	if err != nil {
		t.Fatal(err)
	}
	workload.BaseURL = "http://127.0.0.1:2/mutated/"
	if _, err := model.Complete(t.Context(), textRequestForProviderTest(t)); err != nil {
		t.Fatal(err)
	}

	request := <-requests
	if request.method != http.MethodPost || request.path != "/custom-anthropic/v1/messages" ||
		request.apiKey != "environment-key" || request.authorization != "" {
		t.Fatalf("Anthropic request = %#v", request)
	}
	if len(requests) != 0 {
		t.Fatalf("unexpected additional Anthropic requests = %d", len(requests))
	}
}

func TestFactoryAppliesWorkloadOverridesWithoutRetainingCallerState(t *testing.T) {
	fake := &openAIFake{responses: []any{chatResponse("chat_1", `{"value":"stable"}`, 1, 1)}}
	factory, err := NewFactory(MilestoneB, testEnvironment{"OPENAI_API_KEY": "test-key"}.lookup, fake.Client())
	if err != nil {
		t.Fatal(err)
	}
	nested := map[string]any{"enabled": true}
	workload := WorkloadConfig{
		BaseURL: "https://workload.test/v1",
		Headers: http.Header{"X-Workload": []string{"workload-secret"}},
		ModelSettings: map[string]any{
			"top_p": 0.25, "metadata": nested,
		},
	}
	model, err := factory.TextModelWithWorkload("openai-chat:test-model", workload)
	if err != nil {
		t.Fatal(err)
	}
	workload.BaseURL = "https://mutated.test/v1"
	workload.Headers.Set("X-Workload", "mutated-secret")
	nested["enabled"] = false
	workload.ModelSettings["top_p"] = 0.75

	generator, err := inference.NewPromptedGenerator[openAITestInput, openAITestOutput](
		model, "Return a value.", openAITestCodec(t), nil, inference.GenerationSettings{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := generator.Generate(t.Context(), openAITestInput{Value: "bounded"}); err != nil {
		t.Fatal(err)
	}
	requests := fake.Requests()
	if len(requests) != 1 {
		t.Fatalf("requests = %#v", requests)
	}
	request := requests[0]
	if request.host != "workload.test" || request.headers.Get("X-Workload") != "workload-secret" || request.body["top_p"] != 0.25 {
		t.Fatalf("request = %#v", request)
	}
	metadata, ok := request.body["metadata"].(map[string]any)
	if !ok || metadata["enabled"] != true {
		t.Fatalf("model settings = %#v", request.body)
	}
}

func TestFactoryRejectsUnsupportedNonOpenAIWorkloadOverrides(t *testing.T) {
	factory, err := NewFactory(MilestoneB, testEnvironment{
		"ANTHROPIC_API_KEY":            "test-key",
		"PYDANTIC_AI_GATEWAY_API_KEY":  "private-bearer",
		"PYDANTIC_AI_GATEWAY_BASE_URL": "https://gateway-provider.test/proxy",
	}.lookup, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, factoryCall := range []struct {
		name    string
		call    func() error
		secrets []string
	}{
		{name: "Anthropic headers", call: func() error {
			_, callErr := factory.TextModelWithWorkload("anthropic:claude-test", WorkloadConfig{
				Headers: http.Header{"Authorization": []string{"private-secret"}},
			})
			return callErr
		}, secrets: []string{"private-secret", "test-key"}},
		{name: "Anthropic model settings", call: func() error {
			_, callErr := factory.TextModelWithWorkload("anthropic:claude-test", WorkloadConfig{
				ModelSettings: map[string]any{"top_p": "private-setting"},
			})
			return callErr
		}, secrets: []string{"private-setting", "test-key"}},
		{name: "Anthropic gateway base URL", call: func() error {
			_, callErr := factory.TextModelWithWorkload(
				"gateway/anthropic:claude-test",
				WorkloadConfig{BaseURL: "https://private-provider.test/v1"},
			)
			return callErr
		}, secrets: []string{"private-provider", "private-bearer", "gateway-provider"}},
		{name: "Google embedding base URL", call: func() error {
			_, callErr := factory.EmbeddingTransportWithWorkload(
				"google:gemini-embedding-001",
				WorkloadConfig{BaseURL: "https://private-provider.test/v1"},
			)
			return callErr
		}, secrets: []string{"private-provider", "test-key"}},
	} {
		t.Run(factoryCall.name, func(t *testing.T) {
			err := factoryCall.call()
			configuration, ok := errors.AsType[*inference.ConfigurationError](err)
			if !ok || configuration.Code() != "workload-provider" {
				t.Fatalf("error = %v", err)
			}
			for _, secret := range factoryCall.secrets {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("error leaked %q: %v", secret, err)
				}
			}
		})
	}
}

func TestFactoryRejectsWorkloadSettingsThatOwnOpenAIRequestShapeBeforeTransport(t *testing.T) {
	fake := &openAIFake{}
	factory, err := NewFactory(MilestoneB, testEnvironment{"OPENAI_API_KEY": "test-key"}.lookup, fake.Client())
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		setting string
		value   any
		call    func(WorkloadConfig) error
	}{
		{
			name: "chat model", setting: "model", value: "other-model",
			call: func(workload WorkloadConfig) error {
				_, callErr := factory.TextModelWithWorkload("openai-chat:test-model", workload)
				return callErr
			},
		},
		{
			name: "chat messages", setting: "messages", value: []any{"private prompt"},
			call: func(workload WorkloadConfig) error {
				_, callErr := factory.TextModelWithWorkload("openai-chat:test-model", workload)
				return callErr
			},
		},
		{
			name: "chat response format", setting: "response_format", value: map[string]any{"type": "text"},
			call: func(workload WorkloadConfig) error {
				_, callErr := factory.TextModelWithWorkload("openai-chat:test-model", workload)
				return callErr
			},
		},
		{
			name: "responses input", setting: "input", value: "private prompt",
			call: func(workload WorkloadConfig) error {
				_, callErr := factory.TextModelWithWorkload("openai-responses:test-model", workload)
				return callErr
			},
		},
		{
			name: "responses text format", setting: "text", value: map[string]any{"format": map[string]any{"type": "text"}},
			call: func(workload WorkloadConfig) error {
				_, callErr := factory.TextModelWithWorkload("openai-responses:test-model", workload)
				return callErr
			},
		},
		{
			name: "responses stream", setting: "stream", value: true,
			call: func(workload WorkloadConfig) error {
				_, callErr := factory.TextModelWithWorkload("openai-responses:test-model", workload)
				return callErr
			},
		},
		{
			name: "embedding input", setting: "input", value: []any{"private input"},
			call: func(workload WorkloadConfig) error {
				_, callErr := factory.EmbeddingTransportWithWorkload("openai:embedding-model", workload)
				return callErr
			},
		},
		{
			name: "embedding dimensions", setting: "dimensions", value: 99,
			call: func(workload WorkloadConfig) error {
				_, callErr := factory.EmbeddingTransportWithWorkload("openai:embedding-model", workload)
				return callErr
			},
		},
		{
			name: "nested messages path", setting: "messages.0.content", value: "private prompt",
			call: func(workload WorkloadConfig) error {
				_, callErr := factory.TextModelWithWorkload("openai-chat:test-model", workload)
				return callErr
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := test.call(WorkloadConfig{ModelSettings: map[string]any{test.setting: test.value}})
			assertWorkloadSettingsRejected(t, err)
			if requests := fake.Requests(); len(requests) != 0 {
				t.Fatalf("reserved setting %q reached transport: %#v", test.setting, requests)
			}
		})
	}
}

func TestFactoryExpandsExtraBodyIntoOpenAIRequests(t *testing.T) {
	for _, test := range []struct {
		name        string
		response    any
		settingName string
		setting     any
		invoke      func(*testing.T, *Factory, WorkloadConfig)
	}{
		{
			name: "chat", response: chatResponse("chat_1", `{"value":"stable"}`, 1, 1),
			settingName: "top_p", setting: 0.25,
			invoke: func(t *testing.T, factory *Factory, workload WorkloadConfig) {
				t.Helper()
				model, err := factory.TextModelWithWorkload("openai-chat:test-model", workload)
				if err != nil {
					t.Fatal(err)
				}
				generator, err := inference.NewPromptedGenerator[openAITestInput, openAITestOutput](
					model, "Return a value.", openAITestCodec(t), nil, inference.GenerationSettings{},
				)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := generator.Generate(t.Context(), openAITestInput{Value: "bounded"}); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "responses", response: responsesResponse("response_1", "message_1", `{"value":"stable"}`, 1, 1),
			settingName: "top_p", setting: 0.25,
			invoke: func(t *testing.T, factory *Factory, workload WorkloadConfig) {
				t.Helper()
				model, err := factory.TextModelWithWorkload("openai-responses:test-model", workload)
				if err != nil {
					t.Fatal(err)
				}
				generator, err := inference.NewPromptedGenerator[openAITestInput, openAITestOutput](
					model, "Return a value.", openAITestCodec(t), nil, inference.GenerationSettings{},
				)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := generator.Generate(t.Context(), openAITestInput{Value: "bounded"}); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "embedding", response: map[string]any{
				"object": "list", "data": []any{map[string]any{"object": "embedding", "index": 0, "embedding": []float64{1, 0, 0}}},
				"model": "embedding-model", "usage": map[string]any{"prompt_tokens": 1, "total_tokens": 1},
			},
			settingName: "encoding_format", setting: "float",
			invoke: func(t *testing.T, factory *Factory, workload WorkloadConfig) {
				t.Helper()
				transport, err := factory.EmbeddingTransportWithWorkload("openai:embedding-model", workload)
				if err != nil {
					t.Fatal(err)
				}
				request, err := inference.NewEmbeddingRequest([]string{"bounded"}, inference.EmbeddingDocument, 3)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := transport.Embed(t.Context(), request); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := &openAIFake{responses: []any{test.response}}
			factory, err := NewFactory(MilestoneB, testEnvironment{"OPENAI_API_KEY": "test-key"}.lookup, fake.Client())
			if err != nil {
				t.Fatal(err)
			}
			workload := WorkloadConfig{ModelSettings: map[string]any{
				test.settingName: test.setting,
				"extra_body": map[string]any{
					"route": test.name, "reasoning": map[string]any{"effort": "low"},
				},
			}}
			test.invoke(t, factory, workload)
			requests := fake.Requests()
			if len(requests) != 1 {
				t.Fatalf("requests = %#v", requests)
			}
			request := requests[0]
			if _, found := request.body["extra_body"]; found || request.body["route"] != test.name ||
				request.body[test.settingName] != test.setting {
				t.Fatalf("request body = %#v", request.body)
			}
			reasoning, ok := request.body["reasoning"].(map[string]any)
			if !ok || reasoning["effort"] != "low" {
				t.Fatalf("request reasoning = %#v", request.body)
			}
			if test.name == "embedding" && request.body["dimensions"] != float64(3) {
				t.Fatalf("embedding dimensions = %#v", request.body)
			}
		})
	}
}

func TestFactoryRejectsInvalidExtraBodyBeforeTransport(t *testing.T) {
	fake := &openAIFake{}
	factory, err := NewFactory(MilestoneB, testEnvironment{"OPENAI_API_KEY": "test-key"}.lookup, fake.Client())
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name     string
		settings map[string]any
	}{
		{name: "scalar", settings: map[string]any{"extra_body": "not-an-object"}},
		{name: "array", settings: map[string]any{"extra_body": []any{"not-an-object"}}},
		{name: "recursive", settings: map[string]any{"extra_body": map[string]any{"extra_body": map[string]any{"route": "nested"}}}},
		{name: "nested recursive", settings: map[string]any{"extra_body": map[string]any{"route": map[string]any{"extra_body": map[string]any{"nested": true}}}}},
		{name: "reserved", settings: map[string]any{"extra_body": map[string]any{"messages": []any{"private prompt"}}}},
		{name: "conflicting", settings: map[string]any{"top_p": 0.25, "extra_body": map[string]any{"top_p": 0.75}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := factory.TextModelWithWorkload("openai-chat:test-model", WorkloadConfig{ModelSettings: test.settings})
			assertWorkloadSettingsRejected(t, err)
			if requests := fake.Requests(); len(requests) != 0 {
				t.Fatalf("invalid extra_body reached transport: %#v", requests)
			}
		})
	}
}

func assertWorkloadSettingsRejected(t *testing.T, err error) {
	t.Helper()
	var configuration *inference.ConfigurationError
	if !errors.As(err, &configuration) || configuration.Code() != "workload-settings" {
		t.Fatalf("error = %v", err)
	}
}
