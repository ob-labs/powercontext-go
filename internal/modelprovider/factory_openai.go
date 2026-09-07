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
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/ob-labs/powercontext-go/inference"
)

var snowflakeAccountPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

type openAIProviderSpec struct {
	baseURL string
	keys    []string
	headers http.Header
}

var openAIProviderSpecs = map[string]openAIProviderSpec{
	"alibaba": {
		baseURL: "https://dashscope-intl.aliyuncs.com/compatible-mode/v1",
		keys:    []string{"ALIBABA_API_KEY", "DASHSCOPE_API_KEY"},
	},
	"cerebras": {
		baseURL: "https://api.cerebras.ai/v1",
		keys:    []string{"CEREBRAS_API_KEY"},
		headers: http.Header{"X-Cerebras-3rd-Party-Integration": []string{"pydantic-ai"}},
	},
	"crusoe":     {baseURL: "https://api.inference.crusoecloud.com/v1", keys: []string{"CRUSOE_API_KEY"}},
	"deepseek":   {baseURL: "https://api.deepseek.com", keys: []string{"DEEPSEEK_API_KEY"}},
	"fireworks":  {baseURL: "https://api.fireworks.ai/inference/v1", keys: []string{"FIREWORKS_API_KEY"}},
	"github":     {baseURL: "https://models.github.ai/inference", keys: []string{"GITHUB_API_KEY"}},
	"moonshotai": {baseURL: "https://api.moonshot.ai/v1", keys: []string{"MOONSHOTAI_API_KEY"}},
	"nebius":     {baseURL: "https://api.studio.nebius.com/v1", keys: []string{"NEBIUS_API_KEY"}},
	"openrouter": {baseURL: "https://openrouter.ai/api/v1", keys: []string{"OPENROUTER_API_KEY"}},
	"ovhcloud":   {baseURL: "https://oai.endpoints.kepler.ai.cloud.ovh.net/v1", keys: []string{"OVHCLOUD_API_KEY"}},
	"together":   {baseURL: "https://api.together.xyz/v1", keys: []string{"TOGETHER_API_KEY"}},
	"zai":        {baseURL: "https://api.z.ai/api/paas/v4", keys: []string{"ZAI_API_KEY"}},
}

func (f *Factory) openAIConfig(route Route) (OpenAIConfig, error) {
	if route.gateway {
		key, baseURL, err := f.gatewayCredentials(route)
		if err != nil {
			return OpenAIConfig{}, err
		}
		return OpenAIConfig{APIKey: key, BaseURL: baseURL, HTTPClient: f.httpClient}, nil
	}

	provider := route.canonical
	switch provider {
	case "openai", "openai-chat", "openai-responses":
		return f.openAIProviderConfig()
	case "azure", "azure-responses":
		return f.azureConfig(route)
	case "heroku":
		return f.herokuConfig()
	case "litellm":
		return OpenAIConfig{
			APIKey: "litellm-placeholder", BaseURL: "https://api.openai.com/v1", HTTPClient: f.httpClient,
		}, nil
	case "ollama":
		return f.ollamaConfig()
	case "sambanova":
		return f.sambaNovaConfig()
	case "snowflake":
		return f.snowflakeConfig()
	case "vercel":
		return f.vercelConfig()
	default:
		spec, ok := openAIProviderSpecs[provider]
		if !ok {
			return OpenAIConfig{}, inference.NewConfigurationError("model", "unknown OpenAI-compatible provider")
		}
		key, ok := f.firstNonEmpty(spec.keys...)
		if !ok {
			return OpenAIConfig{}, missingProviderCredential(provider, spec.keys...)
		}
		headers := spec.headers.Clone()
		if headers == nil {
			headers = make(http.Header)
		}
		config := OpenAIConfig{
			APIKey: key, BaseURL: spec.baseURL, HTTPClient: f.httpClient, Headers: headers,
		}
		if provider == "openrouter" {
			if value, found := f.nonEmpty("OPENROUTER_APP_URL"); found {
				config.Headers.Set("HTTP-Referer", value)
			}
			if value, found := f.nonEmpty("OPENROUTER_APP_TITLE"); found {
				config.Headers.Set("X-Title", value)
			}
		}
		return config, nil
	}
}

func (c OpenAIConfig) withWorkload(workload WorkloadConfig) OpenAIConfig {
	if workload.BaseURL != "" {
		c.BaseURL = workload.BaseURL
	}
	if len(workload.Headers) > 0 {
		if c.Headers == nil {
			c.Headers = make(http.Header)
		} else {
			c.Headers = c.Headers.Clone()
		}
		for name, values := range workload.Headers {
			for existing := range c.Headers {
				if strings.EqualFold(existing, name) {
					delete(c.Headers, existing)
				}
			}
			c.Headers[name] = append([]string(nil), values...)
		}
	}
	c.ModelSettings = workload.ModelSettings
	return c
}

func (f *Factory) openAIProviderConfig() (OpenAIConfig, error) {
	baseURL, hasBaseURL := f.nonEmpty("OPENAI_BASE_URL")
	if !hasBaseURL {
		baseURL = "https://api.openai.com/v1"
	}
	key, hasKey := f.nonEmpty("OPENAI_API_KEY")
	if !hasKey {
		if !hasBaseURL {
			return OpenAIConfig{}, missingProviderCredential("openai", "OPENAI_API_KEY")
		}
		key = "api-key-not-set"
	}
	return OpenAIConfig{APIKey: key, BaseURL: baseURL, HTTPClient: f.httpClient}, nil
}

func (f *Factory) herokuConfig() (OpenAIConfig, error) {
	key, ok := f.nonEmpty("HEROKU_INFERENCE_KEY")
	if !ok {
		return OpenAIConfig{}, missingProviderCredential("heroku", "HEROKU_INFERENCE_KEY")
	}
	baseURL, ok := f.nonEmpty("HEROKU_INFERENCE_URL")
	if !ok {
		baseURL = "https://us.inference.heroku.com"
	}
	baseURL = strings.TrimRight(baseURL, "/")
	if !strings.HasSuffix(baseURL, "/v1") {
		baseURL += "/v1"
	}
	return OpenAIConfig{APIKey: key, BaseURL: baseURL, HTTPClient: f.httpClient}, nil
}

func (f *Factory) ollamaConfig() (OpenAIConfig, error) {
	baseURL, ok := f.nonEmpty("OLLAMA_BASE_URL")
	if !ok {
		return OpenAIConfig{}, inference.NewConfigurationError("model", "OLLAMA_BASE_URL is required")
	}
	key, ok := f.nonEmpty("OLLAMA_API_KEY")
	if !ok {
		key = "api-key-not-set"
	}
	return OpenAIConfig{APIKey: key, BaseURL: baseURL, HTTPClient: f.httpClient}, nil
}

func (f *Factory) sambaNovaConfig() (OpenAIConfig, error) {
	key, ok := f.nonEmpty("SAMBANOVA_API_KEY")
	if !ok {
		return OpenAIConfig{}, missingProviderCredential("sambanova", "SAMBANOVA_API_KEY")
	}
	baseURL, ok := f.nonEmpty("SAMBANOVA_BASE_URL")
	if !ok {
		baseURL = "https://api.sambanova.ai/v1"
	}
	return OpenAIConfig{APIKey: key, BaseURL: baseURL, HTTPClient: f.httpClient}, nil
}

func (f *Factory) snowflakeConfig() (OpenAIConfig, error) {
	account, ok := f.nonEmpty("SNOWFLAKE_ACCOUNT")
	if !ok {
		return OpenAIConfig{}, missingProviderCredential("snowflake", "SNOWFLAKE_ACCOUNT")
	}
	account = strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(strings.TrimSuffix(account, "/"), "https://"), "http://"), ".snowflakecomputing.com")
	if !snowflakeAccountPattern.MatchString(account) {
		return OpenAIConfig{}, inference.NewConfigurationError("model", "invalid Snowflake account identifier")
	}
	token, ok := f.nonEmpty("SNOWFLAKE_TOKEN")
	if !ok {
		return OpenAIConfig{}, missingProviderCredential("snowflake", "SNOWFLAKE_TOKEN")
	}
	return OpenAIConfig{
		APIKey:     token,
		BaseURL:    fmt.Sprintf("https://%s.snowflakecomputing.com/api/v2/cortex/v1", account),
		HTTPClient: f.httpClient,
	}, nil
}

func (f *Factory) vercelConfig() (OpenAIConfig, error) {
	key, ok := f.firstNonEmpty("VERCEL_AI_GATEWAY_API_KEY", "VERCEL_OIDC_TOKEN")
	if !ok {
		return OpenAIConfig{}, missingProviderCredential("vercel", "VERCEL_AI_GATEWAY_API_KEY", "VERCEL_OIDC_TOKEN")
	}
	return OpenAIConfig{
		APIKey: key, BaseURL: "https://ai-gateway.vercel.sh/v1", HTTPClient: f.httpClient,
		Headers: http.Header{"Http-Referer": []string{"https://ai.pydantic.dev/"}, "X-Title": []string{"pydantic-ai"}},
	}, nil
}

func (f *Factory) azureConfig(route Route) (OpenAIConfig, error) {
	endpoint, ok := f.nonEmpty("AZURE_OPENAI_ENDPOINT")
	if !ok {
		return OpenAIConfig{}, inference.NewConfigurationError("model", "AZURE_OPENAI_ENDPOINT is required")
	}
	key, ok := f.nonEmpty("AZURE_OPENAI_API_KEY")
	if !ok {
		return OpenAIConfig{}, missingProviderCredential("azure", "AZURE_OPENAI_API_KEY")
	}
	endpoint = strings.TrimRight(endpoint, "/")
	parsed, parseErr := url.Parse(endpoint)
	if parseErr != nil || parsed.Host == "" {
		return OpenAIConfig{}, inference.NewConfigurationError("model", "invalid Azure OpenAI endpoint")
	}
	if strings.HasSuffix(endpoint, "/v1") || strings.HasSuffix(strings.ToLower(parsed.Hostname()), ".models.ai.azure.com") {
		if !strings.HasSuffix(endpoint, "/v1") {
			endpoint += "/v1"
		}
		return OpenAIConfig{APIKey: key, BaseURL: endpoint, HTTPClient: f.httpClient}, nil
	}
	version, ok := f.nonEmpty("OPENAI_API_VERSION")
	if !ok {
		return OpenAIConfig{}, inference.NewConfigurationError("model", "OPENAI_API_VERSION is required for Azure OpenAI")
	}
	baseURL := endpoint + "/openai"
	if route.protocol != ProtocolOpenAIResponses {
		baseURL += "/deployments/" + url.PathEscape(route.model)
	}
	return OpenAIConfig{
		APIKey: key, BaseURL: baseURL, HTTPClient: f.httpClient,
		Headers: http.Header{"Authorization": nil, "Api-Key": []string{key}},
		Query:   url.Values{"api-version": []string{version}},
	}, nil
}
