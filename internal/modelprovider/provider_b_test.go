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
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	bedrocktypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"google.golang.org/genai"

	"github.com/ob-labs/powercontext-go/inference"
)

func TestHTTPChatProviderWireProfiles(t *testing.T) {
	tests := []struct {
		model      string
		path       string
		jsonObject bool
		candidateN bool
	}{
		{model: "groq:test", path: "/openai/v1/chat/completions", candidateN: true},
		{model: "xai:test", path: "/chat/completions", jsonObject: true},
		{model: "mistral:test", path: "/v1/chat/completions", candidateN: true},
		{model: "huggingface:owner/model", path: "/chat/completions"},
	}
	for _, test := range tests {
		t.Run(test.model, func(t *testing.T) {
			fake := &openAIFake{responses: []any{chatResponse("id", `{"value":"stable"}`, 4, 2)}}
			route, err := Resolve(test.model, Generation)
			if err != nil {
				t.Fatal(err)
			}
			model, err := NewChatHTTPTextModel(route, ChatHTTPConfig{
				APIKey: "secret", BaseURL: "https://provider.test", EndpointPath: test.path,
				HTTPClient: fake.Client(), SupportsJSONObject: test.jsonObject,
				SendCandidateCount: test.candidateN,
			})
			if err != nil {
				t.Fatal(err)
			}
			response, err := model.Complete(t.Context(), textRequestForProviderTest(t))
			if err != nil {
				t.Fatal(err)
			}
			if response.Content() != `{"value":"stable"}` || *response.Usage().InputTokens != 4 || *response.Usage().OutputTokens != 2 {
				t.Fatalf("response = %#v", response)
			}
			request := fake.Requests()[0]
			if request.path != test.path || request.authorization != "Bearer secret" {
				t.Fatalf("request = %#v", request)
			}
			_, hasFormat := request.body["response_format"]
			if hasFormat != test.jsonObject {
				t.Fatalf("response_format present = %t, body = %#v", hasFormat, request.body)
			}
			_, hasN := request.body["n"]
			if hasN != test.candidateN {
				t.Fatalf("n present = %t, body = %#v", hasN, request.body)
			}
			if roles(objectSlice(t, request.body["messages"])) != "system,system,user" {
				t.Fatalf("messages = %#v", request.body["messages"])
			}
		})
	}
}

func TestOpenAICompatibleGenerationPrefixesUseFrozenWireEndpoints(t *testing.T) {
	tests := []struct {
		prefix        string
		model         string
		host          string
		path          string
		authorization string
		header        string
		headerValue   string
	}{
		{prefix: "alibaba", host: "dashscope-intl.aliyuncs.com", path: "/compatible-mode/v1/chat/completions", authorization: "Bearer key"},
		{prefix: "azure", host: "azure.test", path: "/v1/chat/completions", authorization: "Bearer key"},
		{prefix: "azure-responses", host: "azure.test", path: "/v1/responses", authorization: "Bearer key"},
		{prefix: "cerebras", host: "api.cerebras.ai", path: "/v1/chat/completions", authorization: "Bearer key", header: "X-Cerebras-3rd-Party-Integration", headerValue: "pydantic-ai"},
		{prefix: "crusoe", host: "api.inference.crusoecloud.com", path: "/v1/chat/completions", authorization: "Bearer key"},
		{prefix: "deepseek", host: "api.deepseek.com", path: "/chat/completions", authorization: "Bearer key"},
		{prefix: "fireworks", host: "api.fireworks.ai", path: "/inference/v1/chat/completions", authorization: "Bearer key"},
		{prefix: "github", host: "models.github.ai", path: "/inference/chat/completions", authorization: "Bearer key"},
		{prefix: "heroku", host: "us.inference.heroku.com", path: "/v1/chat/completions", authorization: "Bearer key"},
		{prefix: "litellm", host: "api.openai.com", path: "/v1/chat/completions", authorization: "Bearer litellm-placeholder"},
		{prefix: "moonshotai", host: "api.moonshot.ai", path: "/v1/chat/completions", authorization: "Bearer key"},
		{prefix: "nebius", host: "api.studio.nebius.com", path: "/v1/chat/completions", authorization: "Bearer key"},
		{prefix: "ollama", host: "ollama.test", path: "/v1/chat/completions", authorization: "Bearer api-key-not-set"},
		{prefix: "openai", host: "api.openai.com", path: "/v1/responses", authorization: "Bearer key"},
		{prefix: "openai-chat", host: "api.openai.com", path: "/v1/chat/completions", authorization: "Bearer key"},
		{prefix: "openai-responses", host: "api.openai.com", path: "/v1/responses", authorization: "Bearer key"},
		{prefix: "openrouter", model: "openai/test-model", host: "openrouter.ai", path: "/api/v1/chat/completions", authorization: "Bearer key", header: "HTTP-Referer", headerValue: "https://powercontext.test"},
		{prefix: "ovhcloud", host: "oai.endpoints.kepler.ai.cloud.ovh.net", path: "/v1/chat/completions", authorization: "Bearer key"},
		{prefix: "sambanova", host: "api.sambanova.ai", path: "/v1/chat/completions", authorization: "Bearer key"},
		{prefix: "snowflake", host: "account.snowflakecomputing.com", path: "/api/v2/cortex/v1/chat/completions", authorization: "Bearer key"},
		{prefix: "together", host: "api.together.xyz", path: "/v1/chat/completions", authorization: "Bearer key"},
		{prefix: "vercel", model: "openai/test-model", host: "ai-gateway.vercel.sh", path: "/v1/chat/completions", authorization: "Bearer key", header: "X-Title", headerValue: "pydantic-ai"},
		{prefix: "zai", host: "api.z.ai", path: "/api/paas/v4/chat/completions", authorization: "Bearer key"},
	}
	for _, test := range tests {
		t.Run(test.prefix, func(t *testing.T) {
			fake := &openAIFake{}
			modelName := test.model
			if modelName == "" {
				modelName = "test-model"
			}
			route, err := Resolve(test.prefix+":"+modelName, Generation)
			if err != nil {
				t.Fatal(err)
			}
			if route.protocol == ProtocolOpenAIResponses {
				fake.responses = []any{responsesResponse("resp", "msg", `{"value":"stable"}`, 1, 1)}
			} else {
				fake.responses = []any{chatResponse("chat", `{"value":"stable"}`, 1, 1)}
			}
			environment := milestoneBTestEnvironment()
			environment["OPENROUTER_APP_URL"] = "https://powercontext.test"
			factory, err := NewFactory(MilestoneB, environment.lookup, fake.Client())
			if err != nil {
				t.Fatal(err)
			}
			model, err := factory.TextModel(test.prefix + ":" + modelName)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := model.Complete(t.Context(), textRequestForProviderTest(t)); err != nil {
				t.Fatal(err)
			}
			requests := fake.Requests()
			if len(requests) != 1 {
				t.Fatalf("requests = %d", len(requests))
			}
			request := requests[0]
			if request.host != test.host || request.path != test.path || request.authorization != test.authorization {
				t.Fatalf("wire endpoint/auth = host %q, path %q, auth %q", request.host, request.path, request.authorization)
			}
			if test.header != "" && request.headers.Get(test.header) != test.headerValue {
				t.Fatalf("header %s = %q", test.header, request.headers.Get(test.header))
			}
			if request.body["model"] != modelName {
				t.Fatalf("wire model = %#v", request.body["model"])
			}
		})
	}
}

func TestOpenAICompatibleEmbeddingPrefixesUseFrozenWireEndpoints(t *testing.T) {
	expected := map[string]struct{ host, path string }{
		"alibaba":    {"dashscope-intl.aliyuncs.com", "/compatible-mode/v1/embeddings"},
		"azure":      {"azure.test", "/v1/embeddings"},
		"cerebras":   {"api.cerebras.ai", "/v1/embeddings"},
		"crusoe":     {"api.inference.crusoecloud.com", "/v1/embeddings"},
		"deepseek":   {"api.deepseek.com", "/embeddings"},
		"fireworks":  {"api.fireworks.ai", "/inference/v1/embeddings"},
		"github":     {"models.github.ai", "/inference/embeddings"},
		"heroku":     {"us.inference.heroku.com", "/v1/embeddings"},
		"litellm":    {"api.openai.com", "/v1/embeddings"},
		"moonshotai": {"api.moonshot.ai", "/v1/embeddings"},
		"nebius":     {"api.studio.nebius.com", "/v1/embeddings"},
		"ollama":     {"ollama.test", "/v1/embeddings"},
		"openai":     {"api.openai.com", "/v1/embeddings"},
		"openrouter": {"openrouter.ai", "/api/v1/embeddings"},
		"ovhcloud":   {"oai.endpoints.kepler.ai.cloud.ovh.net", "/v1/embeddings"},
		"sambanova":  {"api.sambanova.ai", "/v1/embeddings"},
		"snowflake":  {"account.snowflakecomputing.com", "/api/v2/cortex/v1/embeddings"},
		"together":   {"api.together.xyz", "/v1/embeddings"},
		"vercel":     {"ai-gateway.vercel.sh", "/v1/embeddings"},
		"zai":        {"api.z.ai", "/api/paas/v4/embeddings"},
	}
	for prefix, want := range expected {
		t.Run(prefix, func(t *testing.T) {
			fake := &openAIFake{responses: []any{map[string]any{
				"data":  []any{map[string]any{"index": 0, "embedding": []float64{1, 2}}},
				"usage": map[string]any{"prompt_tokens": 1, "total_tokens": 1},
			}}}
			environment := milestoneBTestEnvironment()
			factory, err := NewFactory(MilestoneB, environment.lookup, fake.Client())
			if err != nil {
				t.Fatal(err)
			}
			modelName := "test-model"
			if prefix == "openrouter" || prefix == "vercel" {
				modelName = "openai/test-model"
			}
			transport, err := factory.EmbeddingTransport(prefix + ":" + modelName)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := transport.Embed(t.Context(), []string{"bounded"}, inference.EmbeddingDocument); err != nil {
				t.Fatal(err)
			}
			request := fake.Requests()[0]
			if request.host != want.host || request.path != want.path {
				t.Fatalf("wire endpoint = host %q, path %q", request.host, request.path)
			}
			if request.authorization == "" || request.body["model"] != modelName {
				t.Fatalf("wire auth/model = %q, %#v", request.authorization, request.body["model"])
			}
		})
	}
}

func TestHTTPChatProviderDoesNotRetryOrLeakErrors(t *testing.T) {
	fake := &openAIFake{statuses: []int{http.StatusTooManyRequests}, responses: []any{
		map[string]any{"error": "secret-provider-body"},
	}}
	route, _ := Resolve("groq:test", Generation)
	model, err := NewChatHTTPTextModel(route, ChatHTTPConfig{
		APIKey: "secret", BaseURL: "https://provider.test", EndpointPath: "/openai/v1/chat/completions",
		HTTPClient: fake.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = model.Complete(t.Context(), textRequestForProviderTest(t))
	var unavailable *inference.UnavailableError
	if !errors.As(err, &unavailable) || strings.Contains(err.Error(), "secret-provider-body") {
		t.Fatalf("error = %v", err)
	}
	if len(fake.Requests()) != 1 {
		t.Fatalf("requests = %d", len(fake.Requests()))
	}
}

func TestVoyageAIEmbeddingRestoresProviderIndexes(t *testing.T) {
	fake := &openAIFake{responses: []any{map[string]any{
		"data": []any{
			map[string]any{"index": 1, "embedding": []float64{3, 4}},
			map[string]any{"index": 0, "embedding": []float64{1, 2}},
		},
		"usage": map[string]any{"total_tokens": 7},
	}}}
	route, _ := Resolve("voyageai:voyage-test", Embedding)
	transport, err := NewVoyageAIEmbeddingTransport(route, VoyageAIConfig{
		APIKey: "secret", BaseURL: "https://provider.test/v1", HTTPClient: fake.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := transport.Embed(t.Context(), []string{"alpha", "beta"}, inference.EmbeddingQuery)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(result.Embeddings()[0], []float64{1, 2}) || *result.Usage().InputTokens != 7 {
		t.Fatalf("result = %#v", result)
	}
	request := fake.Requests()[0]
	if request.path != "/v1/embeddings" || request.body["input_type"] != "query" {
		t.Fatalf("request = %#v", request)
	}
}

type googleModelsFake struct {
	generate func(context.Context, string, []*genai.Content, *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error)
	embed    func(context.Context, string, []*genai.Content, *genai.EmbedContentConfig) (*genai.EmbedContentResponse, error)
}

func (f googleModelsFake) GenerateContent(ctx context.Context, model string, contents []*genai.Content, config *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
	return f.generate(ctx, model, contents, config)
}

func (f googleModelsFake) EmbedContent(ctx context.Context, model string, contents []*genai.Content, config *genai.EmbedContentConfig) (*genai.EmbedContentResponse, error) {
	return f.embed(ctx, model, contents, config)
}

func TestGoogleOfficialSDKMappingAndEmbeddingTaskDefaults(t *testing.T) {
	route, _ := Resolve("google:gemini-test", Generation)
	model := &GoogleTextModel{route: route, models: googleModelsFake{
		generate: func(_ context.Context, model string, contents []*genai.Content, config *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
			if model != "gemini-test" || config.ResponseMIMEType != "application/json" || config.SystemInstruction == nil {
				t.Fatalf("generation mapping = %q, %#v", model, config)
			}
			if len(contents) != 1 || contents[0].Role != "user" {
				t.Fatalf("contents = %#v", contents)
			}
			return &genai.GenerateContentResponse{
				Candidates:    []*genai.Candidate{{Content: genai.NewContentFromText(`{"value":"stable"}`, genai.RoleModel)}},
				UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 4, CandidatesTokenCount: 2},
			}, nil
		},
	}}
	response, err := model.Complete(t.Context(), textRequestForProviderTest(t))
	if err != nil {
		t.Fatal(err)
	}
	if response.Content() != `{"value":"stable"}` || *response.Usage().InputTokens != 4 {
		t.Fatalf("response = %#v", response)
	}

	embeddingRoute, _ := Resolve("google:gemini-embedding-2", Embedding)
	transport := &GoogleEmbeddingTransport{route: embeddingRoute, models: googleModelsFake{
		embed: func(_ context.Context, model string, contents []*genai.Content, config *genai.EmbedContentConfig) (*genai.EmbedContentResponse, error) {
			if model != "gemini-embedding-2" || config.TaskType != "" || contents[0].Parts[0].Text != "title: none | text: alpha" {
				t.Fatalf("embedding mapping = %q, %#v, %#v", model, contents, config)
			}
			return &genai.EmbedContentResponse{Embeddings: []*genai.ContentEmbedding{{
				Values: []float32{1, 2}, Statistics: &genai.ContentEmbeddingStatistics{TokenCount: 3},
			}}}, nil
		},
	}}
	result, err := transport.Embed(t.Context(), []string{"alpha"}, inference.EmbeddingDocument)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(result.Embeddings()[0], []float64{1, 2}) || *result.Usage().InputTokens != 3 {
		t.Fatalf("embedding result = %#v", result)
	}
}

func TestCohereOfficialSDKWireAndNoRetry(t *testing.T) {
	fake := &openAIFake{responses: []any{
		map[string]any{
			"id": "chat-id", "finish_reason": "COMPLETE",
			"message": map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "text", "text": `{"value":"stable"}`},
			}},
			"usage": map[string]any{"tokens": map[string]any{"input_tokens": 5, "output_tokens": 2}},
		},
		map[string]any{
			"id": "embed-id", "response_type": "embeddings_by_type",
			"embeddings": map[string]any{"float": []any{[]float64{1, 2}}},
		},
	}}
	route, _ := Resolve("cohere:command-test", Generation)
	model, err := NewCohereTextModel(route, CohereConfig{
		APIKey: "secret", BaseURL: "https://provider.test", HTTPClient: fake.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := model.Complete(t.Context(), textRequestForProviderTest(t))
	if err != nil {
		t.Fatal(err)
	}
	if response.Content() != `{"value":"stable"}` || *response.Usage().InputTokens != 5 {
		t.Fatalf("response = %#v", response)
	}
	embedRoute, _ := Resolve("cohere:embed-v4.0", Embedding)
	transport, err := NewCohereEmbeddingTransport(embedRoute, CohereConfig{
		APIKey: "secret", BaseURL: "https://provider.test", HTTPClient: fake.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := transport.Embed(t.Context(), []string{"alpha"}, inference.EmbeddingDocument)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(result.Embeddings()[0], []float64{1, 2}) {
		t.Fatalf("result = %#v", result)
	}
	requests := fake.Requests()
	if len(requests) != 2 || requests[0].path != "/v2/chat" || requests[1].path != "/v2/embed" {
		t.Fatalf("requests = %#v", requests)
	}
	if requests[1].body["input_type"] != "search_document" {
		t.Fatalf("embedding body = %#v", requests[1].body)
	}
}

type bedrockClientFake struct {
	converse func(*bedrockruntime.ConverseInput) (*bedrockruntime.ConverseOutput, error)
	invoke   func(*bedrockruntime.InvokeModelInput) (*bedrockruntime.InvokeModelOutput, error)
}

func (f bedrockClientFake) Converse(_ context.Context, input *bedrockruntime.ConverseInput, _ ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseOutput, error) {
	return f.converse(input)
}

func (f bedrockClientFake) InvokeModel(_ context.Context, input *bedrockruntime.InvokeModelInput, _ ...func(*bedrockruntime.Options)) (*bedrockruntime.InvokeModelOutput, error) {
	return f.invoke(input)
}

func TestBedrockConverseAndEmbeddingFormats(t *testing.T) {
	route, _ := Resolve("bedrock:us.anthropic.claude-test-v1:0", Generation)
	model := &BedrockTextModel{route: route, client: bedrockClientFake{
		converse: func(input *bedrockruntime.ConverseInput) (*bedrockruntime.ConverseOutput, error) {
			if aws.ToString(input.ModelId) != "anthropic.claude-test-v1:0" || len(input.System) != 2 || len(input.Messages) != 1 {
				t.Fatalf("input = %#v", input)
			}
			inputTokens, outputTokens, total := int32(4), int32(2), int32(6)
			return &bedrockruntime.ConverseOutput{
				Output: &bedrocktypes.ConverseOutputMemberMessage{Value: bedrocktypes.Message{
					Role:    bedrocktypes.ConversationRoleAssistant,
					Content: []bedrocktypes.ContentBlock{&bedrocktypes.ContentBlockMemberText{Value: `{"value":"stable"}`}},
				}},
				Usage: &bedrocktypes.TokenUsage{InputTokens: &inputTokens, OutputTokens: &outputTokens, TotalTokens: &total},
			}, nil
		},
	}}
	response, err := model.Complete(t.Context(), textRequestForProviderTest(t))
	if err != nil {
		t.Fatal(err)
	}
	if response.Content() != `{"value":"stable"}` || *response.Usage().OutputTokens != 2 {
		t.Fatalf("response = %#v", response)
	}

	embedRoute, _ := Resolve("bedrock:cohere.embed-v4:0", Embedding)
	transport := &BedrockEmbeddingTransport{route: embedRoute, client: bedrockClientFake{
		invoke: func(input *bedrockruntime.InvokeModelInput) (*bedrockruntime.InvokeModelOutput, error) {
			var body map[string]any
			if unmarshalErr := json.Unmarshal(input.Body, &body); unmarshalErr != nil {
				t.Fatal(unmarshalErr)
			}
			if body["input_type"] != "search_query" || body["truncate"] != "NONE" {
				t.Fatalf("body = %#v", body)
			}
			return &bedrockruntime.InvokeModelOutput{Body: []byte(`{"embeddings":{"float":[[1,2],[3,4]]}}`)}, nil
		},
	}}
	result, err := transport.Embed(t.Context(), []string{"alpha", "beta"}, inference.EmbeddingQuery)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(result.Embeddings()[1], []float64{3, 4}) {
		t.Fatalf("result = %#v", result)
	}
}

func TestMilestoneBFactoryConstructsEveryRemoteProvider(t *testing.T) {
	environment := milestoneBTestEnvironment()
	factory, err := NewFactory(MilestoneB, environment.lookup, (&openAIFake{}).Client())
	if err != nil {
		t.Fatal(err)
	}
	for _, provider := range GenerationProviders() {
		model := provider + ":test-model"
		switch provider {
		case "openrouter", "vercel", "huggingface":
			model = provider + ":openai/test-model"
		case "bedrock":
			model = "bedrock:anthropic.claude-test-v1:0"
		case "bedrock-mantle":
			model = "bedrock-mantle:openai.gpt-5.4"
		}
		if _, err := factory.TextModel(model); err != nil {
			t.Errorf("generation %s: %v", provider, err)
		}
	}
	for _, provider := range EmbeddingProviders() {
		model := provider + ":test-model"
		switch provider {
		case "openrouter", "vercel":
			model = provider + ":openai/test-model"
		case "bedrock":
			model = "bedrock:amazon.titan-embed-text-v2:0"
		case "sentence-transformers":
			// The local provider is build-tagged and has its own lifecycle tests;
			// this matrix test deliberately constructs remote providers only.
			continue
		}
		if _, err := factory.EmbeddingTransport(model); err != nil {
			t.Errorf("embedding %s: %v", provider, err)
		}
	}
}

func milestoneBTestEnvironment() testEnvironment {
	return testEnvironment{
		"ALIBABA_API_KEY": "key", "ANTHROPIC_API_KEY": "key",
		"AZURE_OPENAI_ENDPOINT": "https://azure.test/v1", "AZURE_OPENAI_API_KEY": "key",
		"AWS_DEFAULT_REGION": "us-east-1", "AWS_BEARER_TOKEN_BEDROCK": "key",
		"CEREBRAS_API_KEY": "key", "CO_API_KEY": "key", "CRUSOE_API_KEY": "key",
		"DEEPSEEK_API_KEY": "key", "FIREWORKS_API_KEY": "key", "GITHUB_API_KEY": "key",
		"GOOGLE_API_KEY": "key", "GROQ_API_KEY": "key", "HEROKU_INFERENCE_KEY": "key",
		"HF_TOKEN": "key", "MISTRAL_API_KEY": "key", "MOONSHOTAI_API_KEY": "key",
		"NEBIUS_API_KEY": "key", "OLLAMA_BASE_URL": "https://ollama.test/v1",
		"OPENAI_API_KEY": "key", "OPENROUTER_API_KEY": "key", "OVHCLOUD_API_KEY": "key",
		"SAMBANOVA_API_KEY": "key", "SNOWFLAKE_ACCOUNT": "account", "SNOWFLAKE_TOKEN": "key",
		"TOGETHER_API_KEY": "key", "VERCEL_OIDC_TOKEN": "key", "VOYAGE_API_KEY": "key",
		"XAI_API_KEY": "key", "ZAI_API_KEY": "key",
	}
}
