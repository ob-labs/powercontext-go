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
	"math"
	"net/http"
	"slices"
	"strings"

	cohere "github.com/cohere-ai/cohere-go/v2"
	"github.com/cohere-ai/cohere-go/v2/core"
	coherev2 "github.com/cohere-ai/cohere-go/v2/v2"

	"github.com/ob-labs/powercontext-go/inference"
)

type CohereConfig struct {
	APIKey     string
	BaseURL    string
	HTTPClient *http.Client
}

type cohereClient interface {
	Chat(context.Context, *cohere.V2ChatRequest, ...core.RequestOption) (*cohere.V2ChatResponse, error)
	Embed(context.Context, *cohere.V2EmbedRequest, ...core.RequestOption) (*cohere.EmbedByTypeResponse, error)
}

type CohereTextModel struct {
	route  Route
	client cohereClient
}

type CohereEmbeddingTransport struct {
	route  Route
	client cohereClient
}

func NewCohereTextModel(route Route, config CohereConfig) (*CohereTextModel, error) {
	if route.protocol != ProtocolCohere {
		return nil, inference.NewConfigurationError("model", "route is not the Cohere generation protocol")
	}
	client, err := newCohereClient(config)
	if err != nil {
		return nil, err
	}
	return &CohereTextModel{route: route, client: client}, nil
}

func NewCohereEmbeddingTransport(route Route, config CohereConfig) (*CohereEmbeddingTransport, error) {
	if route.protocol != ProtocolCohere {
		return nil, inference.NewConfigurationError("embedding-model", "route is not the Cohere embedding protocol")
	}
	client, err := newCohereClient(config)
	if err != nil {
		return nil, err
	}
	return &CohereEmbeddingTransport{route: route, client: client}, nil
}

func newCohereClient(config CohereConfig) (cohereClient, error) {
	if strings.TrimSpace(config.APIKey) == "" {
		return nil, inference.NewConfigurationError("model", "Cohere API key is required")
	}
	if strings.TrimSpace(config.BaseURL) == "" {
		config.BaseURL = "https://api.cohere.com"
	}
	transport, err := newProviderHTTPClient(config.HTTPClient, config.BaseURL, nil)
	if err != nil {
		return nil, err
	}
	options := core.NewRequestOptions()
	options.Token = config.APIKey
	options.BaseURL = strings.TrimRight(config.BaseURL, "/")
	options.HTTPClient = transport.client
	options.MaxAttempts = 1
	return coherev2.NewClient(options), nil
}

func (m *CohereTextModel) Complete(ctx context.Context, request inference.TextRequest) (inference.TextResponse, error) {
	messages := make(cohere.ChatMessages, 0, len(request.Instructions())+len(request.Messages()))
	for _, instruction := range request.Instructions() {
		messages = append(messages, &cohere.ChatMessageV2{System: &cohere.SystemMessageV2{
			Content: &cohere.SystemMessageV2Content{String: instruction},
		}})
	}
	for _, message := range request.Messages() {
		if message.Role() == inference.RoleAssistant {
			if message.Content() == "" {
				continue
			}
			messages = append(messages, &cohere.ChatMessageV2{Assistant: &cohere.AssistantMessage{
				Content: &cohere.AssistantMessageV2Content{String: message.Content()},
			}})
		} else {
			messages = append(messages, &cohere.ChatMessageV2{User: &cohere.UserMessageV2{
				Content: &cohere.UserMessageV2Content{String: message.Content()},
			}})
		}
	}
	settings := request.Settings()
	payload := &cohere.V2ChatRequest{
		Model: m.route.model, Messages: messages,
		Temperature: settings.Temperature(),
	}
	if maxTokens := settings.MaxTokens(); maxTokens != nil {
		if *maxTokens > int64(^uint(0)>>1) {
			return inference.TextResponse{}, inference.NewConfigurationError("request-rejected", "Cohere max tokens exceeds int")
		}
		value := int(*maxTokens)
		payload.MaxTokens = &value
	}
	response, err := m.client.Chat(ctx, payload)
	if err != nil {
		return inference.TextResponse{}, mapCohereError(err, "generate")
	}
	if response == nil || response.Message == nil {
		return inference.TextResponse{}, inference.NewInvalidOutputError("generate", "provider returned no message")
	}
	parts := make([]string, 0, len(response.Message.Content))
	for _, content := range response.Message.Content {
		if content != nil && content.Text != nil {
			parts = append(parts, content.Text.Text)
		}
	}
	usage := inference.Usage{}
	if response.Usage != nil && response.Usage.Tokens != nil {
		var usageErr error
		usage.InputTokens, usageErr = cohereTokenPointer(response.Usage.Tokens.InputTokens)
		if usageErr != nil {
			return inference.TextResponse{}, usageErr
		}
		usage.OutputTokens, usageErr = cohereTokenPointer(response.Usage.Tokens.OutputTokens)
		if usageErr != nil {
			return inference.TextResponse{}, usageErr
		}
	}
	return inference.NewTextResponse(strings.Join(parts, "\n\n"), usage)
}

func (t *CohereEmbeddingTransport) Embed(
	ctx context.Context,
	request inference.EmbeddingRequest,
) (inference.ProviderEmbeddingResult, error) {
	inputs := request.Inputs()
	inputType := request.InputType()
	wireType := cohere.EmbedInputTypeSearchDocument
	if inputType == inference.EmbeddingQuery {
		wireType = cohere.EmbedInputTypeSearchQuery
	}
	truncate := cohere.V2EmbedRequestTruncateNone
	response, err := t.client.Embed(ctx, &cohere.V2EmbedRequest{
		Texts: slices.Clone(inputs), Model: t.route.model, InputType: wireType,
		EmbeddingTypes: []cohere.EmbeddingType{cohere.EmbeddingTypeFloat}, Truncate: &truncate,
	})
	if err != nil {
		return inference.ProviderEmbeddingResult{}, mapCohereError(err, "embed")
	}
	if response == nil || response.Embeddings == nil {
		return inference.ProviderEmbeddingResult{}, inference.NewInvalidOutputError("embed", "provider returned no float embeddings")
	}
	usage := inference.Usage{}
	if response.Meta != nil && response.Meta.BilledUnits != nil {
		usage.InputTokens, err = cohereTokenPointer(response.Meta.BilledUnits.InputTokens)
		if err != nil {
			return inference.ProviderEmbeddingResult{}, err
		}
	}
	return inference.NewProviderEmbeddingResult(
		inputs, inputType, response.Embeddings.Float, usage,
	)
}

func cohereTokenPointer(value *float64) (*int64, error) {
	if value == nil {
		return nil, nil
	}
	if math.IsNaN(*value) || math.IsInf(*value, 0) || *value < 0 || *value > float64(^uint64(0)>>1) {
		return nil, inference.NewInvalidOutputError("usage", "provider returned invalid token usage")
	}
	converted := int64(*value)
	return &converted, nil
}

func mapCohereError(err error, operation string) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var apiError *core.APIError
	if errors.As(err, &apiError) {
		return mapProviderHTTPStatus(apiError.StatusCode, operation, err)
	}
	return inference.WrapUnavailableError(operation, err)
}

var (
	_ inference.TextModel          = (*CohereTextModel)(nil)
	_ inference.EmbeddingTransport = (*CohereEmbeddingTransport)(nil)
)
