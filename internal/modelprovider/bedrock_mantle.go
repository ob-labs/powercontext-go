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
	"net/http"
	"regexp"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	openai "github.com/openai/openai-go/v3"
	openaiBedrock "github.com/openai/openai-go/v3/bedrock"
	"github.com/openai/openai-go/v3/option"

	"github.com/ob-labs/powercontext-go/inference"
)

type BedrockMantleConfig struct {
	APIKey                 string
	Region                 string
	Origin                 string
	AWSAccessKeyID         string
	AWSSecretAccessKey     string
	AWSSessionToken        string
	AWSProfile             string
	AWSCredentialsProvider aws.CredentialsProvider
	HTTPClient             *http.Client
}

var bedrockVersionSuffix = regexp.MustCompile(`-v[0-9]+(?::[0-9]+)?$`)

func NewBedrockMantleTextModel(route Route, config BedrockMantleConfig) (inference.TextModel, error) {
	if route.protocol != ProtocolBedrockMantle {
		return nil, inference.NewConfigurationError("model", "route is not the Bedrock Mantle protocol")
	}
	provider, baseModel := splitBedrockModelID(route.model)
	if provider != "openai" || baseModel == "" {
		return nil, inference.NewConfigurationError("model", "Bedrock Mantle only supports OpenAI models")
	}
	origin, err := bedrockMantleOrigin(config.Origin)
	if err != nil {
		return nil, err
	}
	protocol := ProtocolOpenAIResponses
	baseURL := origin + "/openai/v1"
	if strings.HasPrefix(baseModel, "gpt-oss") {
		baseURL = origin + "/v1"
		if strings.HasPrefix(baseModel, "gpt-oss-safeguard") {
			protocol = ProtocolOpenAIChat
		}
	}
	adapted := route
	adapted.protocol = protocol
	clientConfig := openaiBedrock.Config{
		APIKey: config.APIKey, AWSRegion: config.Region, BaseURL: baseURL,
		AWSAccessKeyID: config.AWSAccessKeyID, AWSSecretAccessKey: config.AWSSecretAccessKey,
		AWSSessionToken: config.AWSSessionToken, AWSProfile: config.AWSProfile,
		AWSCredentialsProvider: config.AWSCredentialsProvider,
	}
	options := []option.RequestOption{option.WithMaxRetries(0)}
	if config.HTTPClient != nil {
		options = append(options, option.WithHTTPClient(config.HTTPClient))
	}
	client, err := openaiBedrock.NewClient(context.Background(), clientConfig, options...)
	if err != nil {
		return nil, inference.NewConfigurationError("model", "Bedrock Mantle client could not be configured")
	}
	supportsJSON := true
	dropSampling := openAIReasoningEnabledByDefault(baseModel)
	openAIConfig := OpenAIConfig{
		SupportsJSONObject: &supportsJSON, DropSampling: &dropSampling,
		IncludeEncryptedReasoning: protocol == ProtocolOpenAIResponses && openAIReasoningSupported(baseModel),
	}
	return newOpenAITextModelFromClient(adapted, openAIConfig, client)
}

func newOpenAITextModelFromClient(route Route, config OpenAIConfig, client openai.Client) (*OpenAITextModel, error) {
	if config.SystemRole == "" {
		config.SystemRole = openAIBehaviorFor(route).systemRole
	}
	if config.SystemRole != OpenAISystem && config.SystemRole != OpenAIDeveloper && config.SystemRole != OpenAIUser {
		return nil, inference.NewConfigurationError("model", "invalid OpenAI system role")
	}
	return &OpenAITextModel{shared: openAIClient{client: client, route: route, config: config}}, nil
}

func bedrockMantleOrigin(value string) (string, error) {
	value = strings.TrimRight(value, "/")
	for _, suffix := range []string{"/openai/v1", "/v1"} {
		value = strings.TrimSuffix(value, suffix)
	}
	validated, err := newProviderHTTPClient(nil, value, nil)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(validated.baseURL.String(), "/"), nil
}

func splitBedrockModelID(model string) (string, string) {
	normalized := removeBedrockGeoPrefix(model)
	provider, name, found := strings.Cut(normalized, ".")
	if !found {
		return "", normalized
	}
	return provider, bedrockVersionSuffix.ReplaceAllString(name, "")
}
