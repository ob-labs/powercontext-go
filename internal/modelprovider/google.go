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
	"errors"
	"net/http"
	"slices"

	"google.golang.org/genai"

	"github.com/ob-labs/powercontext-go/inference"
)

type GoogleBackend uint8

const (
	GoogleGeminiAPI GoogleBackend = iota + 1
	GoogleVertexAI
)

type GoogleConfig struct {
	Backend    GoogleBackend
	APIKey     string
	Project    string
	Location   string
	BaseURL    string
	APIVersion string
	HTTPClient *http.Client
	Headers    http.Header
}

type googleModels interface {
	GenerateContent(context.Context, string, []*genai.Content, *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error)
	EmbedContent(context.Context, string, []*genai.Content, *genai.EmbedContentConfig) (*genai.EmbedContentResponse, error)
}

type GoogleTextModel struct {
	route  Route
	models googleModels
}

type GoogleEmbeddingTransport struct {
	route  Route
	models googleModels
}

func NewGoogleTextModel(ctx context.Context, route Route, config GoogleConfig) (*GoogleTextModel, error) {
	if route.protocol != ProtocolGoogle {
		return nil, inference.NewConfigurationError("model", "route is not the Google generation protocol")
	}
	models, err := newGoogleModels(ctx, config)
	if err != nil {
		return nil, err
	}
	return &GoogleTextModel{route: route, models: models}, nil
}

func NewGoogleEmbeddingTransport(ctx context.Context, route Route, config GoogleConfig) (*GoogleEmbeddingTransport, error) {
	if route.protocol != ProtocolGoogle {
		return nil, inference.NewConfigurationError("embedding-model", "route is not the Google embedding protocol")
	}
	models, err := newGoogleModels(ctx, config)
	if err != nil {
		return nil, err
	}
	return &GoogleEmbeddingTransport{route: route, models: models}, nil
}

func newGoogleModels(ctx context.Context, config GoogleConfig) (googleModels, error) {
	if config.Backend != GoogleGeminiAPI && config.Backend != GoogleVertexAI {
		return nil, inference.NewConfigurationError("model", "invalid Google backend")
	}
	backend := genai.BackendGeminiAPI
	if config.Backend == GoogleVertexAI {
		backend = genai.BackendVertexAI
	}
	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey: config.APIKey, Backend: backend, Project: config.Project, Location: config.Location,
		HTTPClient: config.HTTPClient,
		HTTPOptions: genai.HTTPOptions{
			BaseURL: config.BaseURL, APIVersion: config.APIVersion, Headers: config.Headers.Clone(),
		},
	})
	if err != nil {
		return nil, inference.NewConfigurationError("model", "Google client could not be configured")
	}
	return client.Models, nil
}

func (m *GoogleTextModel) Complete(ctx context.Context, request inference.TextRequest) (inference.TextResponse, error) {
	contents := make([]*genai.Content, 0, len(request.Messages()))
	for _, message := range request.Messages() {
		role := genai.Role(genai.RoleUser)
		if message.Role() == inference.RoleAssistant {
			role = genai.RoleModel
		}
		contents = append(contents, genai.NewContentFromText(message.Content(), role))
	}
	config := &genai.GenerateContentConfig{}
	if request.StructuredOutput() {
		config.ResponseMIMEType = "application/json"
	}
	if instructions := request.Instructions(); len(instructions) > 0 {
		parts := make([]*genai.Part, len(instructions))
		for index, instruction := range instructions {
			parts[index] = genai.NewPartFromText(instruction)
		}
		config.SystemInstruction = genai.NewContentFromParts(parts, genai.RoleUser)
	}
	settings := request.Settings()
	if temperature := settings.Temperature(); temperature != nil {
		value := float32(*temperature)
		config.Temperature = &value
	}
	if maxTokens := settings.MaxTokens(); maxTokens != nil {
		if *maxTokens > int64(^uint32(0)>>1) {
			return inference.TextResponse{}, inference.NewConfigurationError("request-rejected", "Google max tokens exceeds int32")
		}
		config.MaxOutputTokens = int32(*maxTokens)
	}
	response, err := m.models.GenerateContent(ctx, m.route.model, contents, config)
	if err != nil {
		return inference.TextResponse{}, mapGoogleError(err, "generate")
	}
	if response == nil || len(response.Candidates) == 0 {
		return inference.TextResponse{}, inference.NewInvalidOutputError("generate", "provider returned no candidates")
	}
	usage := inference.Usage{}
	if response.UsageMetadata != nil {
		usage.InputTokens = int64Value(int64(response.UsageMetadata.PromptTokenCount))
		usage.OutputTokens = int64Value(int64(response.UsageMetadata.CandidatesTokenCount))
	}
	return inference.NewTextResponse(response.Text(), usage)
}

func (t *GoogleEmbeddingTransport) Embed(
	ctx context.Context,
	inputs []string,
	inputType inference.EmbeddingInputType,
) (inference.ProviderEmbeddingResult, error) {
	texts := slices.Clone(inputs)
	config := &genai.EmbedContentConfig{}
	if t.route.model == "gemini-embedding-2" {
		for index, value := range texts {
			if inputType == inference.EmbeddingDocument {
				texts[index] = "title: none | text: " + value
			} else {
				texts[index] = "task: search result | query: " + value
			}
		}
	} else if inputType == inference.EmbeddingDocument {
		config.TaskType = "RETRIEVAL_DOCUMENT"
	} else {
		config.TaskType = "RETRIEVAL_QUERY"
	}
	contents := make([]*genai.Content, len(texts))
	for index, value := range texts {
		contents[index] = genai.NewContentFromText(value, genai.RoleUser)
	}
	response, err := t.models.EmbedContent(ctx, t.route.model, contents, config)
	if err != nil {
		return inference.ProviderEmbeddingResult{}, mapGoogleError(err, "embed")
	}
	if response == nil {
		return inference.ProviderEmbeddingResult{}, inference.NewInvalidOutputError("embed", "provider returned an empty response")
	}
	vectors := make([][]float64, len(response.Embeddings))
	inputTokens := int64(0)
	for index, embedding := range response.Embeddings {
		if embedding == nil {
			return inference.ProviderEmbeddingResult{}, inference.NewInvalidOutputError("embed", "provider returned an empty embedding")
		}
		vectors[index] = make([]float64, len(embedding.Values))
		for valueIndex, value := range embedding.Values {
			vectors[index][valueIndex] = float64(value)
		}
		if embedding.Statistics != nil {
			inputTokens += int64(embedding.Statistics.TokenCount)
		}
	}
	return inference.NewProviderEmbeddingResult(inputs, inputType, vectors, inference.Usage{
		InputTokens: &inputTokens,
	})
}

func mapGoogleError(err error, operation string) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var apiError genai.APIError
	if errors.As(err, &apiError) {
		return mapProviderHTTPStatus(apiError.Code, operation, err)
	}
	return inference.WrapUnavailableError(operation, err)
}

var (
	_ inference.TextModel          = (*GoogleTextModel)(nil)
	_ inference.EmbeddingTransport = (*GoogleEmbeddingTransport)(nil)
)
