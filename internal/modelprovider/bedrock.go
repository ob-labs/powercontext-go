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
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsmiddleware "github.com/aws/aws-sdk-go-v2/aws/middleware"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	bedrocktypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/aws/smithy-go/auth/bearer"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"github.com/ob-labs/powercontext-go/inference"
)

type BedrockConfig struct {
	Region      string
	BaseURL     string
	BearerToken string
	HTTPClient  *http.Client
}

type bedrockClient interface {
	Converse(context.Context, *bedrockruntime.ConverseInput, ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseOutput, error)
	InvokeModel(context.Context, *bedrockruntime.InvokeModelInput, ...func(*bedrockruntime.Options)) (*bedrockruntime.InvokeModelOutput, error)
}

type BedrockTextModel struct {
	route  Route
	client bedrockClient
}

type BedrockEmbeddingTransport struct {
	route  Route
	client bedrockClient
}

func NewBedrockTextModel(ctx context.Context, route Route, config BedrockConfig) (*BedrockTextModel, error) {
	if route.protocol != ProtocolBedrock {
		return nil, inference.NewConfigurationError("model", "route is not the Bedrock generation protocol")
	}
	client, err := newBedrockClient(ctx, config)
	if err != nil {
		return nil, err
	}
	return &BedrockTextModel{route: route, client: client}, nil
}

func NewBedrockEmbeddingTransport(ctx context.Context, route Route, config BedrockConfig) (*BedrockEmbeddingTransport, error) {
	if route.protocol != ProtocolBedrock {
		return nil, inference.NewConfigurationError("embedding-model", "route is not the Bedrock embedding protocol")
	}
	if _, err := bedrockEmbeddingKind(route.model); err != nil {
		return nil, err
	}
	client, err := newBedrockClient(ctx, config)
	if err != nil {
		return nil, err
	}
	return &BedrockEmbeddingTransport{route: route, client: client}, nil
}

func newBedrockClient(ctx context.Context, config BedrockConfig) (bedrockClient, error) {
	if config.BaseURL != "" {
		if _, err := newProviderHTTPClient(config.HTTPClient, config.BaseURL, nil); err != nil {
			return nil, err
		}
	}
	options := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRetryer(func() aws.Retryer { return aws.NopRetryer{} }),
	}
	if config.Region != "" {
		options = append(options, awsconfig.WithRegion(config.Region))
	}
	if config.BaseURL != "" {
		options = append(options, awsconfig.WithBaseEndpoint(config.BaseURL))
	}
	if config.HTTPClient != nil {
		options = append(options, awsconfig.WithHTTPClient(config.HTTPClient))
	}
	resolved, err := awsconfig.LoadDefaultConfig(ctx, options...)
	if err != nil {
		return nil, inference.NewConfigurationError("model", "AWS configuration could not be loaded")
	}
	return bedrockruntime.NewFromConfig(resolved, func(options *bedrockruntime.Options) {
		options.Retryer = aws.NopRetryer{}
		if config.BaseURL != "" {
			options.BaseEndpoint = aws.String(config.BaseURL)
		}
		if config.HTTPClient != nil {
			options.HTTPClient = config.HTTPClient
		}
		if config.BearerToken != "" {
			token := config.BearerToken
			options.BearerAuthTokenProvider = bearer.TokenProviderFunc(func(context.Context) (bearer.Token, error) {
				return bearer.Token{Value: token}, nil
			})
			options.AuthSchemePreference = []string{"httpBearerAuth"}
		}
	}), nil
}

func (m *BedrockTextModel) Complete(ctx context.Context, request inference.TextRequest) (inference.TextResponse, error) {
	messages := bedrockMessages(request.Messages())
	system := make([]bedrocktypes.SystemContentBlock, len(request.Instructions()))
	for index, instruction := range request.Instructions() {
		system[index] = &bedrocktypes.SystemContentBlockMemberText{Value: instruction}
	}
	config := &bedrocktypes.InferenceConfiguration{}
	settings := request.Settings()
	if temperature := settings.Temperature(); temperature != nil {
		value := float32(*temperature)
		config.Temperature = &value
	}
	if maxTokens := settings.MaxTokens(); maxTokens != nil {
		if *maxTokens > int64(^uint32(0)>>1) {
			return inference.TextResponse{}, inference.NewConfigurationError("request-rejected", "Bedrock max tokens exceeds int32")
		}
		value := int32(*maxTokens)
		config.MaxTokens = &value
	}
	response, err := m.client.Converse(ctx, &bedrockruntime.ConverseInput{
		ModelId: aws.String(removeBedrockGeoPrefix(m.route.model)), Messages: messages,
		System: system, InferenceConfig: config,
	})
	if err != nil {
		return inference.TextResponse{}, mapBedrockError(err, "generate")
	}
	if response == nil {
		return inference.TextResponse{}, inference.NewInvalidOutputError("generate", "provider returned an empty response")
	}
	message, ok := response.Output.(*bedrocktypes.ConverseOutputMemberMessage)
	if !ok {
		return inference.TextResponse{}, inference.NewInvalidOutputError("generate", "provider returned no message")
	}
	var content strings.Builder
	for _, block := range message.Value.Content {
		if text, textOK := block.(*bedrocktypes.ContentBlockMemberText); textOK {
			content.WriteString(text.Value)
		}
	}
	usage := inference.Usage{}
	if response.Usage != nil {
		usage.InputTokens = int32PointerToInt64(response.Usage.InputTokens)
		usage.OutputTokens = int32PointerToInt64(response.Usage.OutputTokens)
	}
	return inference.NewTextResponse(content.String(), usage)
}

func bedrockMessages(messages []inference.Message) []bedrocktypes.Message {
	result := make([]bedrocktypes.Message, 0, len(messages))
	for _, message := range messages {
		role := bedrocktypes.ConversationRoleUser
		if message.Role() == inference.RoleAssistant {
			role = bedrocktypes.ConversationRoleAssistant
		}
		block := &bedrocktypes.ContentBlockMemberText{Value: message.Content()}
		if len(result) > 0 && result[len(result)-1].Role == role {
			result[len(result)-1].Content = append(result[len(result)-1].Content, block)
			continue
		}
		result = append(result, bedrocktypes.Message{
			Role: role, Content: []bedrocktypes.ContentBlock{block},
		})
	}
	return result
}

type bedrockEmbeddingProvider uint8

const (
	bedrockTitan bedrockEmbeddingProvider = iota + 1
	bedrockCohere
	bedrockNova
)

func bedrockEmbeddingKind(model string) (bedrockEmbeddingProvider, error) {
	normalized := removeBedrockGeoPrefix(model)
	switch {
	case strings.HasPrefix(normalized, "amazon.titan-embed"):
		return bedrockTitan, nil
	case strings.HasPrefix(normalized, "cohere.embed"):
		return bedrockCohere, nil
	case strings.HasPrefix(normalized, "amazon.nova"):
		return bedrockNova, nil
	default:
		return 0, inference.NewConfigurationError("embedding-model", "unsupported Bedrock embedding model")
	}
}

func (t *BedrockEmbeddingTransport) Embed(
	ctx context.Context,
	request inference.EmbeddingRequest,
) (inference.ProviderEmbeddingResult, error) {
	inputs := request.Inputs()
	inputType := request.InputType()
	kind, err := bedrockEmbeddingKind(t.route.model)
	if err != nil {
		return inference.ProviderEmbeddingResult{}, err
	}
	if kind == bedrockCohere {
		vectors, tokens, invokeErr := t.invokeCohere(ctx, inputs, inputType)
		if invokeErr != nil {
			return inference.ProviderEmbeddingResult{}, invokeErr
		}
		return inference.NewProviderEmbeddingResult(inputs, inputType, vectors, inference.Usage{InputTokens: &tokens})
	}
	return t.embedIndividually(ctx, inputs, inputType, kind)
}

type bedrockEmbeddingItem struct {
	vector []float64
	tokens int64
	err    error
}

func (t *BedrockEmbeddingTransport) embedIndividually(
	ctx context.Context,
	inputs []string,
	inputType inference.EmbeddingInputType,
	kind bedrockEmbeddingProvider,
) (inference.ProviderEmbeddingResult, error) {
	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make([]bedrockEmbeddingItem, len(inputs))
	semaphore := make(chan struct{}, 5)
	var wait sync.WaitGroup
	for index, input := range inputs {
		wait.Add(1)
		go func() {
			defer wait.Done()
			select {
			case semaphore <- struct{}{}:
			case <-callCtx.Done():
				results[index].err = callCtx.Err()
				return
			}
			defer func() { <-semaphore }()
			var vector []float64
			var tokens int64
			var err error
			if kind == bedrockTitan {
				vector, tokens, err = t.invokeTitan(callCtx, input)
			} else {
				vector, tokens, err = t.invokeNova(callCtx, input, inputType)
			}
			results[index] = bedrockEmbeddingItem{vector: vector, tokens: tokens, err: err}
			if err != nil {
				cancel()
			}
		}()
	}
	wait.Wait()
	vectors := make([][]float64, len(results))
	tokens := int64(0)
	for index, result := range results {
		if result.err != nil {
			if ctx.Err() != nil {
				return inference.ProviderEmbeddingResult{}, ctx.Err()
			}
			return inference.ProviderEmbeddingResult{}, result.err
		}
		vectors[index] = result.vector
		tokens += result.tokens
	}
	return inference.NewProviderEmbeddingResult(inputs, inputType, vectors, inference.Usage{InputTokens: &tokens})
}

func (t *BedrockEmbeddingTransport) invokeTitan(ctx context.Context, input string) ([]float64, int64, error) {
	payload := map[string]any{"inputText": input}
	if !strings.Contains(removeBedrockGeoPrefix(t.route.model), "-v1") {
		payload["normalize"] = true
	}
	var response struct {
		Embedding []float64 `json:"embedding"`
	}
	tokens, err := t.invokeEmbedding(ctx, payload, &response)
	return response.Embedding, tokens, err
}

func (t *BedrockEmbeddingTransport) invokeCohere(
	ctx context.Context,
	inputs []string,
	inputType inference.EmbeddingInputType,
) ([][]float64, int64, error) {
	wireType := "search_document"
	if inputType == inference.EmbeddingQuery {
		wireType = "search_query"
	}
	payload := map[string]any{"texts": inputs, "input_type": wireType, "truncate": "NONE"}
	var response struct {
		Embeddings json.RawMessage `json:"embeddings"`
	}
	tokens, err := t.invokeEmbedding(ctx, payload, &response)
	if err != nil {
		return nil, 0, err
	}
	var direct [][]float64
	if err := decodeSingleJSON(response.Embeddings, &direct); err == nil {
		return direct, tokens, nil
	}
	var typed struct {
		Float [][]float64 `json:"float"`
	}
	if err := decodeSingleJSON(response.Embeddings, &typed); err != nil || typed.Float == nil {
		return nil, 0, inference.NewInvalidOutputError("embed", "provider returned invalid Cohere embeddings")
	}
	return typed.Float, tokens, nil
}

func (t *BedrockEmbeddingTransport) invokeNova(
	ctx context.Context,
	input string,
	inputType inference.EmbeddingInputType,
) ([]float64, int64, error) {
	purpose := "GENERIC_INDEX"
	if inputType == inference.EmbeddingQuery {
		purpose = "GENERIC_RETRIEVAL"
	}
	payload := map[string]any{
		"taskType": "SINGLE_EMBEDDING",
		"singleEmbeddingParams": map[string]any{
			"embeddingPurpose": purpose,
			"text":             map[string]any{"value": input, "truncationMode": "NONE"},
		},
	}
	var response struct {
		Embeddings []struct {
			Embedding []float64 `json:"embedding"`
		} `json:"embeddings"`
	}
	tokens, err := t.invokeEmbedding(ctx, payload, &response)
	if err != nil {
		return nil, 0, err
	}
	if len(response.Embeddings) == 0 {
		return nil, 0, inference.NewInvalidOutputError("embed", "provider returned no Nova embedding")
	}
	return response.Embeddings[0].Embedding, tokens, nil
}

func (t *BedrockEmbeddingTransport) invokeEmbedding(ctx context.Context, payload, output any) (int64, error) {
	var body bytes.Buffer
	encoder := json.NewEncoder(&body)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(payload); err != nil {
		return 0, inference.NewConfigurationError("request-rejected", "Bedrock embedding request could not be encoded")
	}
	contentType := "application/json"
	response, err := t.client.InvokeModel(ctx, &bedrockruntime.InvokeModelInput{
		ModelId: aws.String(t.route.model), Body: body.Bytes(),
		ContentType: &contentType, Accept: &contentType,
	})
	if err != nil {
		return 0, mapBedrockError(err, "embed")
	}
	if response == nil || int64(len(response.Body)) > maxProviderResponseBytes {
		return 0, inference.NewInvalidOutputError("embed", "provider returned an invalid response size")
	}
	if err := decodeSingleJSON(response.Body, output); err != nil {
		return 0, inference.NewInvalidOutputError("embed", "provider returned invalid JSON")
	}
	tokens := int64(0)
	if raw := awsmiddleware.GetRawResponse(response.ResultMetadata); raw != nil {
		if httpResponse, ok := raw.(*smithyhttp.Response); ok {
			if value, parseErr := strconv.ParseInt(httpResponse.Header.Get("x-amzn-bedrock-input-token-count"), 10, 64); parseErr == nil && value >= 0 {
				tokens = value
			}
		}
	}
	return tokens, nil
}

func decodeSingleJSON(encoded []byte, output any) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	if err := decoder.Decode(output); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("trailing JSON")
	}
	return nil
}

var bedrockGeoPrefixes = []string{"us", "eu", "apac", "jp", "au", "ca", "global", "us-gov"}

func removeBedrockGeoPrefix(model string) string {
	for _, prefix := range bedrockGeoPrefixes {
		if strings.HasPrefix(model, prefix+".") {
			return strings.TrimPrefix(model, prefix+".")
		}
	}
	return model
}

func int32PointerToInt64(value *int32) *int64 {
	if value == nil {
		return nil
	}
	converted := int64(*value)
	return &converted
}

func mapBedrockError(err error, operation string) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var responseError *smithyhttp.ResponseError
	if errors.As(err, &responseError) {
		return mapProviderHTTPStatus(responseError.HTTPStatusCode(), operation, err)
	}
	return inference.WrapUnavailableError(operation, err)
}

var (
	_ inference.TextModel          = (*BedrockTextModel)(nil)
	_ inference.EmbeddingTransport = (*BedrockEmbeddingTransport)(nil)
)
