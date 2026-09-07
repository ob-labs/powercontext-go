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
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"

	"github.com/ob-labs/powercontext-go/inference"
)

type EnvLookup func(string) (string, bool)

func ProcessEnvironment(name string) (string, bool) { return os.LookupEnv(name) }

// WorkloadConfig carries one server-owned model workload override. It is
// copied before a provider client is assembled so callers cannot mutate a
// live provider configuration through their ProcessConfig value.
type WorkloadConfig struct {
	BaseURL       string
	Headers       http.Header
	ModelSettings map[string]any
}

func (c WorkloadConfig) hasOverrides() bool {
	return c.BaseURL != "" || len(c.Headers) != 0 || len(c.ModelSettings) != 0
}

func cloneWorkloadConfig(value WorkloadConfig) (WorkloadConfig, error) {
	result := WorkloadConfig{BaseURL: value.BaseURL, Headers: value.Headers.Clone()}
	if len(value.ModelSettings) == 0 {
		return result, nil
	}
	encoded, err := json.Marshal(value.ModelSettings)
	if err != nil {
		return WorkloadConfig{}, inference.WrapConfigurationError("workload-settings", "", err)
	}
	if unmarshalErr := json.Unmarshal(encoded, &result.ModelSettings); unmarshalErr != nil {
		return WorkloadConfig{}, inference.WrapConfigurationError("workload-settings", "", unmarshalErr)
	}
	settings, err := normalizeWorkloadModelSettings(result.ModelSettings)
	if err != nil {
		return WorkloadConfig{}, err
	}
	result.ModelSettings = settings
	return result, nil
}

func normalizeWorkloadModelSettings(settings map[string]any) (map[string]any, error) {
	if len(settings) == 0 {
		return nil, nil
	}
	result := make(map[string]any, len(settings))
	extraBody, hasExtraBody := settings["extra_body"]
	for name, value := range settings {
		if name == "extra_body" {
			continue
		}
		if err := validateWorkloadSettingName(name); err != nil {
			return nil, err
		}
		result[name] = value
	}
	if !hasExtraBody {
		return result, nil
	}
	values, ok := extraBody.(map[string]any)
	if !ok {
		return nil, inference.NewConfigurationError("workload-settings", "")
	}
	for name, value := range values {
		if name == "extra_body" {
			return nil, inference.NewConfigurationError("workload-settings", "")
		}
		if err := validateWorkloadSettingName(name); err != nil {
			return nil, err
		}
		if containsNestedExtraBody(value) {
			return nil, inference.NewConfigurationError("workload-settings", "")
		}
		if _, found := result[name]; found {
			return nil, inference.NewConfigurationError("workload-settings", "")
		}
		result[name] = value
	}
	return result, nil
}

func containsNestedExtraBody(value any) bool {
	switch value := value.(type) {
	case map[string]any:
		for name, nested := range value {
			if name == "extra_body" || containsNestedExtraBody(nested) {
				return true
			}
		}
	case []any:
		for _, nested := range value {
			if containsNestedExtraBody(nested) {
				return true
			}
		}
	}
	return false
}

func validateWorkloadSettingName(name string) error {
	if !workloadSettingNamePattern.MatchString(name) {
		return inference.NewConfigurationError("workload-settings", "")
	}
	if _, owned := workloadOwnedRequestFields[name]; owned {
		return inference.NewConfigurationError("workload-settings", "")
	}
	return nil
}

type Factory struct {
	milestone  Milestone
	lookup     EnvLookup
	httpClient *http.Client
}

func NewFactory(milestone Milestone, lookup EnvLookup, httpClient *http.Client) (*Factory, error) {
	if milestone != MilestoneA && milestone != MilestoneB {
		return nil, inference.NewConfigurationError("model", "invalid provider milestone")
	}
	if lookup == nil {
		return nil, inference.NewConfigurationError("model", "environment lookup is required")
	}
	return &Factory{milestone: milestone, lookup: lookup, httpClient: httpClient}, nil
}

func (f *Factory) TextModel(modelID string) (inference.TextModel, error) {
	return f.textModel(modelID, WorkloadConfig{})
}

// TextModelWithWorkload builds an OpenAI-compatible text client with a
// workload-local endpoint, headers, and request settings.
func (f *Factory) TextModelWithWorkload(modelID string, workload WorkloadConfig) (inference.TextModel, error) {
	return f.textModel(modelID, workload)
}

func (f *Factory) textModel(modelID string, workload WorkloadConfig) (inference.TextModel, error) {
	clonedWorkload, err := cloneWorkloadConfig(workload)
	if err != nil {
		return nil, err
	}
	route, err := Resolve(modelID, Generation)
	if err != nil {
		return nil, err
	}
	if err := RequireAvailable(route, f.milestone); err != nil {
		return nil, err
	}
	if err := validateProviderModel(route); err != nil {
		return nil, err
	}
	if clonedWorkload.hasOverrides() && route.protocol != ProtocolOpenAIChat && route.protocol != ProtocolOpenAIResponses {
		return nil, inference.NewConfigurationError("workload-provider", "workload overrides require OpenAI-compatible model routes")
	}
	switch route.protocol {
	case ProtocolOpenAIChat, ProtocolOpenAIResponses:
		config, configErr := f.openAIConfig(route)
		if configErr != nil {
			return nil, configErr
		}
		config = config.withWorkload(clonedWorkload)
		return NewOpenAITextModel(route, config)
	case ProtocolAnthropic:
		config, configErr := f.anthropicConfig(route)
		if configErr != nil {
			return nil, configErr
		}
		return NewAnthropicTextModel(route, config)
	case ProtocolGroq, ProtocolXAI, ProtocolMistral, ProtocolHuggingFace:
		config, configErr := f.chatHTTPConfig(route)
		if configErr != nil {
			return nil, configErr
		}
		return NewChatHTTPTextModel(route, config)
	case ProtocolGoogle:
		config, configErr := f.googleConfig(route)
		if configErr != nil {
			return nil, configErr
		}
		return NewGoogleTextModel(context.Background(), route, config)
	case ProtocolCohere:
		config, configErr := f.cohereConfig()
		if configErr != nil {
			return nil, configErr
		}
		return NewCohereTextModel(route, config)
	case ProtocolBedrock:
		config, configErr := f.bedrockConfig(route)
		if configErr != nil {
			return nil, configErr
		}
		return NewBedrockTextModel(context.Background(), route, config)
	case ProtocolBedrockMantle:
		config, configErr := f.bedrockMantleConfig()
		if configErr != nil {
			return nil, configErr
		}
		return NewBedrockMantleTextModel(route, config)
	default:
		return nil, inference.NewConfigurationError("model", "provider adapter is not implemented")
	}
}

func (f *Factory) EmbeddingTransport(modelID string) (inference.EmbeddingTransport, error) {
	return f.embeddingTransport(modelID, WorkloadConfig{})
}

// EmbeddingTransportWithWorkload builds an OpenAI-compatible embedding client
// with workload-local endpoint, headers, and request settings.
func (f *Factory) EmbeddingTransportWithWorkload(
	modelID string,
	workload WorkloadConfig,
) (inference.EmbeddingTransport, error) {
	return f.embeddingTransport(modelID, workload)
}

func (f *Factory) embeddingTransport(modelID string, workload WorkloadConfig) (inference.EmbeddingTransport, error) {
	clonedWorkload, err := cloneWorkloadConfig(workload)
	if err != nil {
		return nil, err
	}
	route, err := Resolve(modelID, Embedding)
	if err != nil {
		return nil, err
	}
	if err := RequireAvailable(route, f.milestone); err != nil {
		return nil, err
	}
	if err := validateProviderModel(route); err != nil {
		return nil, err
	}
	if clonedWorkload.hasOverrides() && route.protocol != ProtocolOpenAIEmbedding {
		return nil, inference.NewConfigurationError("workload-provider", "workload overrides require OpenAI-compatible model routes")
	}
	switch route.protocol {
	case ProtocolOpenAIEmbedding:
		config, configErr := f.openAIConfig(route)
		if configErr != nil {
			return nil, configErr
		}
		config = config.withWorkload(clonedWorkload)
		return NewOpenAIEmbeddingTransport(route, config)
	case ProtocolGoogle:
		config, configErr := f.googleConfig(route)
		if configErr != nil {
			return nil, configErr
		}
		return NewGoogleEmbeddingTransport(context.Background(), route, config)
	case ProtocolCohere:
		config, configErr := f.cohereConfig()
		if configErr != nil {
			return nil, configErr
		}
		return NewCohereEmbeddingTransport(route, config)
	case ProtocolBedrock:
		config, configErr := f.bedrockConfig(route)
		if configErr != nil {
			return nil, configErr
		}
		return NewBedrockEmbeddingTransport(context.Background(), route, config)
	case ProtocolVoyageAI:
		config, configErr := f.voyageAIConfig()
		if configErr != nil {
			return nil, configErr
		}
		return NewVoyageAIEmbeddingTransport(route, config)
	case ProtocolSentenceTransformers:
		return newSentenceTransformersTransport(route, f.lookup)
	default:
		return nil, inference.NewConfigurationError("embedding-model", "provider adapter is not implemented")
	}
}

var gatewayKeyPattern = regexp.MustCompile(`^pylf_v[0-9]+_([a-z]+)_[A-Za-z0-9_-]+$`)

var workloadSettingNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

var workloadOwnedRequestFields = map[string]struct{}{
	"dimensions": {}, "include": {}, "input": {}, "instructions": {}, "messages": {}, "model": {},
	"response_format": {}, "stream": {}, "text": {},
}

func (f *Factory) gatewayCredentials(route Route) (string, string, error) {
	key, ok := f.firstNonEmpty("PYDANTIC_AI_GATEWAY_API_KEY", "PAIG_API_KEY")
	if !ok {
		return "", "", missingProviderCredential("gateway", "PYDANTIC_AI_GATEWAY_API_KEY")
	}
	baseURL, ok := f.firstNonEmpty("PYDANTIC_AI_GATEWAY_BASE_URL", "PAIG_BASE_URL")
	if !ok {
		matches := gatewayKeyPattern.FindStringSubmatch(key)
		if len(matches) != 2 {
			return "", "", inference.NewConfigurationError("model", "gateway base URL cannot be inferred from API key")
		}
		region := matches[1]
		if strings.HasPrefix(region, "staging") {
			baseURL = "https://gateway.pydantic.info/proxy"
		} else {
			baseURL = "https://gateway-" + region + ".pydantic.dev/proxy"
		}
	}
	return key, strings.TrimRight(baseURL, "/") + "/" + strings.TrimLeft(route.gatewayRoute, "/"), nil
}

func (f *Factory) nonEmpty(name string) (string, bool) {
	value, ok := f.lookup(name)
	return value, ok && value != ""
}

func (f *Factory) firstNonEmpty(names ...string) (string, bool) {
	for _, name := range names {
		if value, ok := f.nonEmpty(name); ok {
			return value, true
		}
	}
	return "", false
}

func validateProviderModel(route Route) error {
	if !route.gateway && route.canonical == "openrouter" {
		model := strings.TrimPrefix(route.model, "~")
		if !strings.Contains(model, "/") {
			return inference.NewConfigurationError("model", "OpenRouter model must include its upstream provider")
		}
	}
	return nil
}

func missingProviderCredential(provider string, names ...string) error {
	return inference.NewConfigurationError(
		"model",
		fmt.Sprintf("%s provider requires %s", provider, strings.Join(names, " or ")),
	)
}
