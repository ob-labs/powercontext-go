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
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/ob-labs/powercontext-go/inference"
)

const defaultAnthropicMaxTokens int64 = 4096

type AnthropicAuthMode uint8

const (
	AnthropicAPIKey AnthropicAuthMode = iota + 1
	AnthropicBearerToken
)

// AnthropicConfig is fully resolved by server composition. The adapter never
// consults process environment variables, which keeps one configured runtime
// isolated from ambient credentials.
type AnthropicConfig struct {
	APIKey     string
	BaseURL    string
	AuthMode   AnthropicAuthMode
	HTTPClient *http.Client
	Headers    http.Header
	Query      url.Values
}

type AnthropicTextModel struct {
	client anthropic.Client
	route  Route
}

func NewAnthropicTextModel(route Route, config AnthropicConfig) (*AnthropicTextModel, error) {
	if route.protocol != ProtocolAnthropic {
		return nil, inference.NewConfigurationError("model", "route is not the Anthropic generation protocol")
	}
	if strings.TrimSpace(config.APIKey) == "" || strings.TrimSpace(config.BaseURL) == "" {
		return nil, inference.NewConfigurationError("model", "Anthropic API key and base URL are required")
	}
	parsed, err := url.Parse(config.BaseURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return nil, inference.NewConfigurationError("model", "invalid Anthropic base URL")
	}
	if config.AuthMode == 0 {
		config.AuthMode = AnthropicAPIKey
	}
	if config.AuthMode != AnthropicAPIKey && config.AuthMode != AnthropicBearerToken {
		return nil, inference.NewConfigurationError("model", "invalid Anthropic authentication mode")
	}

	config.Headers = config.Headers.Clone()
	config.Query = cloneURLValues(config.Query)
	opts := []option.RequestOption{
		option.WithoutEnvironmentDefaults(),
		option.WithBaseURL(config.BaseURL),
		option.WithMaxRetries(0),
	}
	if config.AuthMode == AnthropicBearerToken {
		opts = append(opts, option.WithAuthToken(config.APIKey))
	} else {
		opts = append(opts, option.WithAPIKey(config.APIKey))
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
	return &AnthropicTextModel{client: anthropic.NewClient(opts...), route: route}, nil
}

func (m *AnthropicTextModel) Complete(
	ctx context.Context,
	request inference.TextRequest,
) (inference.TextResponse, error) {
	params := anthropic.MessageNewParams{
		MaxTokens: defaultAnthropicMaxTokens,
		Messages:  anthropicMessages(request.Messages()),
		Model:     anthropic.Model(m.route.model),
		System:    anthropicSystem(request.Instructions()),
	}
	settings := request.Settings()
	if value := settings.MaxTokens(); value != nil {
		params.MaxTokens = *value
	}
	if value := settings.Temperature(); value != nil {
		params.Temperature = anthropic.Float(*value)
	}

	response, err := m.client.Messages.New(ctx, params, option.WithJSONSet("stream", false))
	if err != nil {
		return inference.TextResponse{}, mapAnthropicError(err, "generate")
	}
	return inference.NewTextResponse(anthropicResponseText(response.Content), inference.Usage{
		InputTokens:  int64Value(response.Usage.InputTokens),
		OutputTokens: int64Value(response.Usage.OutputTokens),
	})
}

func anthropicSystem(instructions []string) []anthropic.TextBlockParam {
	result := make([]anthropic.TextBlockParam, len(instructions))
	for index, instruction := range instructions {
		result[index] = anthropic.TextBlockParam{Text: instruction}
	}
	return result
}

func anthropicMessages(messages []inference.Message) []anthropic.MessageParam {
	result := make([]anthropic.MessageParam, 0, len(messages))
	for _, message := range messages {
		block := anthropic.NewTextBlock(message.Content())
		if message.Role() == inference.RoleAssistant {
			result = append(result, anthropic.NewAssistantMessage(block))
		} else {
			result = append(result, anthropic.NewUserMessage(block))
		}
	}
	return result
}

func anthropicResponseText(blocks []anthropic.ContentBlockUnion) string {
	parts := make([]string, 0, len(blocks))
	adjacent := false
	for _, block := range blocks {
		if block.Type != "text" {
			adjacent = false
			continue
		}
		if adjacent {
			parts[len(parts)-1] += block.Text
		} else {
			parts = append(parts, block.Text)
		}
		adjacent = true
	}
	return strings.Join(parts, "\n\n")
}

func mapAnthropicError(err error, operation string) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var apiError *anthropic.Error
	if errors.As(err, &apiError) {
		status := apiError.StatusCode
		if status == http.StatusRequestTimeout || status == http.StatusGatewayTimeout {
			return context.DeadlineExceeded
		}
		if status == http.StatusConflict || status == http.StatusTooEarly || status == http.StatusTooManyRequests || status >= http.StatusInternalServerError {
			return inference.WrapUnavailableError(operation, err)
		}
		return inference.WrapConfigurationError("provider-rejected", "", err)
	}
	return inference.WrapUnavailableError(operation, err)
}

var _ inference.TextModel = (*AnthropicTextModel)(nil)
