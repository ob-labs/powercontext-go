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
	"net/url"
	"slices"
	"strings"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"

	"github.com/ob-labs/powercontext-go/inference"
)

const openAIResponsesIdentity = "openai-responses"

type OpenAISystemRole string

const (
	OpenAISystem    OpenAISystemRole = "system"
	OpenAIDeveloper OpenAISystemRole = "developer"
	OpenAIUser      OpenAISystemRole = "user"
)

// OpenAIConfig is an already-resolved provider configuration. Environment
// lookup belongs to server composition, keeping this adapter deterministic.
type OpenAIConfig struct {
	APIKey                    string
	BaseURL                   string
	HTTPClient                *http.Client
	Headers                   http.Header
	Query                     url.Values
	SupportsJSONObject        *bool
	UseLegacyMaxTokens        *bool
	DropSampling              *bool
	SystemRole                OpenAISystemRole
	MergeInstructions         bool
	IncludeEncryptedReasoning bool
}

type openAIClient struct {
	client openai.Client
	route  Route
	config OpenAIConfig
}

type OpenAITextModel struct{ shared openAIClient }

func NewOpenAITextModel(route Route, config OpenAIConfig) (*OpenAITextModel, error) {
	if route.protocol != ProtocolOpenAIChat && route.protocol != ProtocolOpenAIResponses {
		return nil, inference.NewConfigurationError("model", "route is not an OpenAI generation protocol")
	}
	shared, err := newOpenAIClient(route, config)
	if err != nil {
		return nil, err
	}
	return &OpenAITextModel{shared: shared}, nil
}

func (m *OpenAITextModel) Complete(ctx context.Context, request inference.TextRequest) (inference.TextResponse, error) {
	switch m.shared.route.protocol {
	case ProtocolOpenAIChat:
		return m.completeChat(ctx, request)
	case ProtocolOpenAIResponses:
		return m.completeResponses(ctx, request)
	default:
		return inference.TextResponse{}, inference.NewConfigurationError("model", "unsupported OpenAI protocol")
	}
}

func (m *OpenAITextModel) completeChat(ctx context.Context, request inference.TextRequest) (inference.TextResponse, error) {
	messages := m.chatMessages(request)
	params := openai.ChatCompletionNewParams{
		Messages: messages,
		Model:    openai.ChatModel(m.shared.route.model),
	}
	behavior := m.behavior()
	if request.StructuredOutput() && behavior.supportsJSONObject {
		jsonObject := shared.NewResponseFormatJSONObjectParam()
		params.ResponseFormat = openai.ChatCompletionNewParamsResponseFormatUnion{OfJSONObject: &jsonObject}
	}
	settings := request.Settings()
	if value := settings.Temperature(); value != nil && !behavior.dropSampling {
		params.Temperature = openai.Float(*value)
	}
	if value := settings.MaxTokens(); value != nil {
		if behavior.useLegacyMaxTokens {
			params.MaxTokens = openai.Int(*value)
		} else {
			params.MaxCompletionTokens = openai.Int(*value)
		}
	}
	response, err := m.shared.client.Chat.Completions.New(ctx, params, option.WithJSONSet("stream", false))
	if err != nil {
		return inference.TextResponse{}, mapOpenAIError(err, "generate")
	}
	content := ""
	if len(response.Choices) > 0 {
		content = response.Choices[0].Message.Content
	}
	usage := inference.Usage{
		InputTokens:  int64Value(response.Usage.PromptTokens),
		OutputTokens: int64Value(response.Usage.CompletionTokens),
	}
	return inference.NewTextResponse(content, usage)
}

func (m *OpenAITextModel) completeResponses(ctx context.Context, request inference.TextRequest) (inference.TextResponse, error) {
	input := m.responsesInput(request)
	params := responses.ResponseNewParams{
		Model: m.shared.route.model,
		Input: responses.ResponseNewParamsInputUnion{OfInputItemList: input},
	}
	behavior := m.behavior()
	if request.StructuredOutput() && behavior.supportsJSONObject {
		jsonObject := shared.NewResponseFormatJSONObjectParam()
		params.Text = responses.ResponseTextConfigParam{
			Format: responses.ResponseFormatTextConfigUnionParam{OfJSONObject: &jsonObject},
		}
	}
	if behavior.includeEncryptedReasoning {
		params.Include = []responses.ResponseIncludable{"reasoning.encrypted_content"}
	}
	settings := request.Settings()
	if value := settings.Temperature(); value != nil && !behavior.dropSampling {
		params.Temperature = openai.Float(*value)
	}
	if value := settings.MaxTokens(); value != nil {
		params.MaxOutputTokens = openai.Int(*value)
	}
	response, err := m.shared.client.Responses.New(ctx, params, option.WithJSONSet("stream", false))
	if err != nil {
		return inference.TextResponse{}, mapOpenAIError(err, "generate")
	}
	usage := inference.Usage{
		InputTokens:  int64Value(response.Usage.InputTokens),
		OutputTokens: int64Value(response.Usage.OutputTokens),
	}
	result, err := inference.NewTextResponse(response.OutputText(), usage)
	if err != nil {
		return inference.TextResponse{}, err
	}
	for _, item := range response.Output {
		if item.Type != "message" || item.ID == "" {
			continue
		}
		identity, identityErr := inference.NewResponseIdentity(openAIResponsesIdentity, item.ID)
		if identityErr == nil {
			result = result.WithResponseIdentity(identity)
		}
		break
	}
	return result, nil
}

func (m *OpenAITextModel) chatMessages(request inference.TextRequest) []openai.ChatCompletionMessageParamUnion {
	instructions := request.Instructions()
	if m.shared.config.MergeInstructions && len(instructions) > 0 {
		instructions = []string{strings.Join(instructions, "\n\n")}
	}
	messages := make([]openai.ChatCompletionMessageParamUnion, 0, len(instructions)+len(request.Messages()))
	for _, instruction := range instructions {
		switch m.shared.config.SystemRole {
		case OpenAIDeveloper:
			messages = append(messages, openai.DeveloperMessage(instruction))
		case OpenAIUser:
			messages = append(messages, openai.UserMessage(instruction))
		default:
			messages = append(messages, openai.SystemMessage(instruction))
		}
	}
	for _, message := range request.Messages() {
		if message.Role() == inference.RoleAssistant {
			messages = append(messages, openai.AssistantMessage(message.Content()))
		} else {
			messages = append(messages, openai.UserMessage(message.Content()))
		}
	}
	return messages
}

func (m *OpenAITextModel) behavior() openAIBehavior {
	behavior := openAIBehaviorFor(m.shared.route)
	config := m.shared.config
	if config.SupportsJSONObject != nil {
		behavior.supportsJSONObject = *config.SupportsJSONObject
	}
	if config.UseLegacyMaxTokens != nil {
		behavior.useLegacyMaxTokens = *config.UseLegacyMaxTokens
	}
	if config.DropSampling != nil {
		behavior.dropSampling = *config.DropSampling
	}
	if config.IncludeEncryptedReasoning {
		behavior.includeEncryptedReasoning = true
	}
	return behavior
}

func (m *OpenAITextModel) responsesInput(request inference.TextRequest) responses.ResponseInputParam {
	instructions := strings.TrimSpace(strings.Join(request.Instructions(), "\n\n"))
	input := make(responses.ResponseInputParam, 0, 1+len(request.Messages()))
	if instructions != "" {
		input = append(input, responses.ResponseInputItemParamOfMessage(
			instructions,
			responses.EasyInputMessageRole(m.shared.config.SystemRole),
		))
	}
	previousAssistant := false
	for _, message := range request.Messages() {
		if message.Role() == inference.RoleAssistant {
			identity := message.ResponseIdentity()
			if identity != nil && identity.Protocol() == openAIResponsesIdentity {
				content := []responses.ResponseOutputMessageContentUnionParam{{
					OfOutputText: &responses.ResponseOutputTextParam{Text: message.Content()},
				}}
				input = append(input, responses.ResponseInputItemParamOfOutputMessage(
					content,
					identity.ItemID(),
					responses.ResponseOutputMessageStatusCompleted,
				))
			} else {
				input = append(input, responses.ResponseInputItemParamOfMessage(
					message.Content(), responses.EasyInputMessageRoleAssistant,
				))
			}
			previousAssistant = true
			continue
		}
		if previousAssistant {
			content := responses.ResponseInputMessageContentListParam{
				responses.ResponseInputContentParamOfInputText(message.Content()),
			}
			input = append(input, responses.ResponseInputItemParamOfInputMessage(content, "user"))
		} else {
			input = append(input, responses.ResponseInputItemParamOfMessage(
				message.Content(), responses.EasyInputMessageRoleUser,
			))
		}
		previousAssistant = false
	}
	return input
}

type OpenAIEmbeddingTransport struct{ shared openAIClient }

func NewOpenAIEmbeddingTransport(route Route, config OpenAIConfig) (*OpenAIEmbeddingTransport, error) {
	if route.protocol != ProtocolOpenAIEmbedding {
		return nil, inference.NewConfigurationError("embedding-model", "route is not an OpenAI embedding protocol")
	}
	shared, err := newOpenAIClient(route, config)
	if err != nil {
		return nil, err
	}
	return &OpenAIEmbeddingTransport{shared: shared}, nil
}

func (t *OpenAIEmbeddingTransport) Embed(
	ctx context.Context,
	request inference.EmbeddingRequest,
) (inference.ProviderEmbeddingResult, error) {
	inputs := request.Inputs()
	inputType := request.InputType()
	response, err := t.shared.client.Embeddings.New(ctx, openai.EmbeddingNewParams{
		Model:      t.shared.route.model,
		Input:      openai.EmbeddingNewParamsInputUnion{OfArrayOfStrings: slices.Clone(inputs)},
		Dimensions: param.NewOpt(int64(request.DimensionCount())),
	})
	if err != nil {
		return inference.ProviderEmbeddingResult{}, mapOpenAIError(err, "embed")
	}
	vectors := make([][]float64, len(response.Data))
	for index, item := range response.Data {
		vectors[index] = slices.Clone(item.Embedding)
	}
	return inference.NewProviderEmbeddingResult(
		inputs,
		inputType,
		vectors,
		inference.Usage{InputTokens: int64Value(response.Usage.PromptTokens)},
	)
}

func newOpenAIClient(route Route, config OpenAIConfig) (openAIClient, error) {
	if strings.TrimSpace(config.APIKey) == "" || strings.TrimSpace(config.BaseURL) == "" {
		return openAIClient{}, inference.NewConfigurationError("model", "OpenAI API key and base URL are required")
	}
	parsed, err := url.Parse(config.BaseURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return openAIClient{}, inference.NewConfigurationError("model", "invalid OpenAI base URL")
	}
	if config.SystemRole == "" {
		config.SystemRole = openAIBehaviorFor(route).systemRole
	}
	if config.SystemRole != OpenAISystem && config.SystemRole != OpenAIDeveloper && config.SystemRole != OpenAIUser {
		return openAIClient{}, inference.NewConfigurationError("model", "invalid OpenAI system role")
	}
	config.Headers = config.Headers.Clone()
	config.Query = cloneURLValues(config.Query)
	opts := []option.RequestOption{
		option.WithBaseURL(config.BaseURL),
		option.WithAPIKey(config.APIKey),
		option.WithOrganization(""),
		option.WithProject(""),
		option.WithMaxRetries(0),
	}
	if config.HTTPClient != nil {
		opts = append(opts, option.WithHTTPClient(config.HTTPClient))
	}
	for key, values := range config.Headers {
		if len(values) == 0 {
			opts = append(opts, option.WithHeaderDel(key))
			continue
		}
		opts = append(opts, option.WithHeader(key, values[0]))
		for _, value := range values[1:] {
			opts = append(opts, option.WithHeaderAdd(key, value))
		}
	}
	for key, values := range config.Query {
		if len(values) == 0 {
			opts = append(opts, option.WithQueryDel(key))
			continue
		}
		opts = append(opts, option.WithQuery(key, values[0]))
		for _, value := range values[1:] {
			opts = append(opts, option.WithQueryAdd(key, value))
		}
	}
	return openAIClient{client: openai.NewClient(opts...), route: route, config: config}, nil
}

func mapOpenAIError(err error, operation string) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var apiError *openai.Error
	if errors.As(err, &apiError) {
		return mapProviderHTTPStatus(apiError.StatusCode, operation, err)
	}
	return inference.WrapUnavailableError(operation, err)
}

func cloneURLValues(values url.Values) url.Values {
	if values == nil {
		return nil
	}
	result := make(url.Values, len(values))
	for key, value := range values {
		result[key] = slices.Clone(value)
	}
	return result
}

func int64Value(value int64) *int64 { return &value }

var (
	_ inference.TextModel          = (*OpenAITextModel)(nil)
	_ inference.EmbeddingTransport = (*OpenAIEmbeddingTransport)(nil)
)
