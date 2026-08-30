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
	"regexp"
	"strings"
)

var qwen35Pattern = regexp.MustCompile(`qwen-?3[.\-]5`)

type openAIBehavior struct {
	supportsJSONObject        bool
	useLegacyMaxTokens        bool
	systemRole                OpenAISystemRole
	includeEncryptedReasoning bool
	dropSampling              bool
}

// openAIBehaviorFor is the narrow subset of Pydantic AI 2.29.0 model profiles
// that changes PowerContext's PromptedOutput wire. Keeping it separate from
// provider credentials makes both matrices independently testable.
func openAIBehaviorFor(route Route) openAIBehavior {
	provider := route.canonical
	model := strings.ToLower(route.model)
	behavior := openAIBehavior{systemRole: OpenAISystem}

	if route.gateway && (route.protocol == ProtocolOpenAIChat || route.protocol == ProtocolOpenAIResponses) {
		provider = "openai"
	}
	behavior.supportsJSONObject = supportsJSONObject(provider, model)
	behavior.useLegacyMaxTokens = provider == "openrouter" ||
		(provider == "azure" && isAzureMistralModel(model))
	behavior.systemRole = openAISystemRole(provider, model)
	if isOpenAIProfile(provider, model) {
		profileModel := openAIProfileModelName(provider, model)
		behavior.includeEncryptedReasoning = openAIReasoningSupported(profileModel)
		behavior.dropSampling = openAIReasoningEnabledByDefault(profileModel)
	}
	return behavior
}

func supportsJSONObject(provider, model string) bool {
	switch provider {
	case "openai", "openai-chat", "openai-responses", "github", "deepseek", "crusoe", "moonshotai", "ollama", "zai":
		return true
	case "azure", "azure-responses":
		if isAzureNonOpenAIFamily(model) {
			return strings.HasPrefix(model, "grok") || qwen35Pattern.MatchString(model)
		}
		return true
	case "alibaba":
		return qwen35Pattern.MatchString(model)
	case "cerebras":
		return strings.HasPrefix(model, "gpt-oss") || qwen35Pattern.MatchString(model)
	case "snowflake":
		return strings.HasPrefix(model, "openai-")
	case "litellm", "vercel", "openrouter":
		return routedProfileSupportsJSONObject(provider, model)
	default:
		return false
	}
}

func routedProfileSupportsJSONObject(provider, model string) bool {
	if provider == "litellm" && !strings.Contains(model, "/") {
		return true // LiteLLM falls back to the OpenAI profile.
	}
	upstream, downstream, found := strings.Cut(strings.TrimPrefix(model, "~"), "/")
	if !found {
		return false
	}
	downstream, _, _ = strings.Cut(downstream, ":")
	switch upstream {
	case "openai", "x-ai", "xai":
		return true
	case "google", "vertex":
		return googleProfileSupportsJSONObject(downstream)
	case "qwen":
		return qwen35Pattern.MatchString(downstream)
	default:
		return false
	}
}

func googleProfileSupportsJSONObject(model string) bool {
	model = strings.ToLower(model)
	isImageModel := strings.Contains(model, "image")
	is3OrNewer := strings.HasPrefix(model, "gemini-3") || strings.HasPrefix(model, "gemini-4")
	return is3OrNewer || !isImageModel
}

func isAzureNonOpenAIFamily(model string) bool {
	return strings.HasPrefix(model, "llama") || strings.HasPrefix(model, "meta-") ||
		strings.HasPrefix(model, "deepseek") || strings.HasPrefix(model, "mistral") ||
		strings.HasPrefix(model, "ministral") || strings.HasPrefix(model, "magistral") ||
		strings.HasPrefix(model, "cohere-") || strings.HasPrefix(model, "grok")
}

func isAzureMistralModel(model string) bool {
	return strings.HasPrefix(model, "mistralai-") || strings.HasPrefix(model, "mistral") ||
		strings.HasPrefix(model, "ministral") || strings.HasPrefix(model, "magistral")
}

func openAISystemRole(provider, model string) OpenAISystemRole {
	if isOpenAIProfile(provider, model) && openAIProfileModelName(provider, model) != "" &&
		strings.HasPrefix(openAIProfileModelName(provider, model), "o1-mini") {
		return OpenAIUser
	}
	return OpenAISystem
}

func isOpenAIProfile(provider, model string) bool {
	switch provider {
	case "openai", "openai-chat", "openai-responses", "github":
		return true
	case "azure", "azure-responses":
		return !isAzureNonOpenAIFamily(model)
	case "snowflake":
		return strings.HasPrefix(model, "openai-")
	case "litellm", "vercel", "openrouter":
		upstream, _, found := strings.Cut(strings.TrimPrefix(model, "~"), "/")
		return (!found && provider == "litellm") || upstream == "openai"
	default:
		return false
	}
}

func openAIProfileModelName(provider, model string) string {
	if provider == "snowflake" {
		return strings.TrimPrefix(model, "openai-")
	}
	if provider == "litellm" || provider == "vercel" || provider == "openrouter" {
		_, downstream, found := strings.Cut(strings.TrimPrefix(model, "~"), "/")
		if found {
			downstream, _, _ = strings.Cut(downstream, ":")
			return downstream
		}
	}
	return model
}

func openAIReasoningSupported(model string) bool {
	return openAIReasoningEnabledByDefault(model) ||
		strings.HasPrefix(model, "gpt-5.1") || strings.HasPrefix(model, "gpt-5.2") ||
		strings.HasPrefix(model, "gpt-5.3") ||
		strings.HasPrefix(model, "gpt-5.4") || strings.HasPrefix(model, "gpt-5.5") ||
		strings.HasPrefix(model, "gpt-5.6")
}

func openAIReasoningEnabledByDefault(model string) bool {
	if strings.HasPrefix(model, "gpt-5-chat") || strings.HasPrefix(model, "gpt-5.3") ||
		strings.HasPrefix(model, "gpt-5.4") {
		return strings.HasPrefix(model, "gpt-5.3-chat") || strings.HasPrefix(model, "gpt-5.4-pro")
	}
	if strings.HasPrefix(model, "gpt-5.1") {
		return strings.HasPrefix(model, "gpt-5.1-chat") || strings.HasPrefix(model, "gpt-5.1-codex")
	}
	if strings.HasPrefix(model, "gpt-5.2") {
		return strings.HasPrefix(model, "gpt-5.2-chat") || strings.HasPrefix(model, "gpt-5.2-pro")
	}
	return strings.HasPrefix(model, "gpt-5") || strings.HasPrefix(model, "o")
}
