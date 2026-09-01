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

package cli

import (
	"bytes"
	"encoding/json/v2"
	"strings"
	"testing"
)

func TestProviderAwareConfigAllowsSupportedModelsAndExplicitEnvironment(t *testing.T) {
	config := defaultProviderAwareConfig()
	config.generation = configModelSelection{
		model: "bedrock:anthropic.claude-sonnet",
		environment: []configProviderVariable{
			{name: "AWS_PROFILE", value: "development"},
			{name: "AWS_REGION", value: "us-west-2"},
		},
	}
	config.embedding = configModelSelection{
		model:       "voyageai:voyage-3",
		environment: []configProviderVariable{{name: "VOYAGE_API_KEY", value: "voyage-secret"}},
	}
	config.credentials = []string{"VOYAGE_API_KEY"}

	if err := validateProviderAwareConfig(config, true); err != nil {
		t.Fatal(err)
	}
	values, err := renderProviderAwareEnvironment(config)
	if err != nil {
		t.Fatal(err)
	}
	if values["POWERCONTEXT_SERVER_INFERENCE_GENERATION_MODEL"] != "bedrock:anthropic.claude-sonnet" ||
		values["AWS_PROFILE"] != "development" || values["AWS_REGION"] != "us-west-2" ||
		values["VOYAGE_API_KEY"] != "voyage-secret" {
		t.Fatalf("environment = %#v", values)
	}
	var scopes []struct {
		ScopeID     string `json:"scope_id"`
		DisplayName string `json:"display_name"`
	}
	if scopeErr := json.Unmarshal([]byte(values["POWERCONTEXT_SERVER_DASHBOARD_SCOPES"]), &scopes); scopeErr != nil ||
		values["POWERCONTEXT_SERVER_DASHBOARD_ENABLED"] != "true" || len(scopes) != 1 ||
		scopes[0].ScopeID != "project:quickstart" || scopes[0].DisplayName != "Quick Start" {
		t.Fatalf("dashboard configuration = %#v, scopes = %#v, err = %v", values, scopes, scopeErr)
	}
	block, err := renderProviderAwareConfigBlock(config)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(block, "# generation-environment=AWS_PROFILE,AWS_REGION") ||
		!strings.Contains(block, "# embedding-environment=VOYAGE_API_KEY") ||
		!strings.Contains(block, "# credentials=VOYAGE_API_KEY") {
		t.Fatalf("managed block = %q", block)
	}
}

func TestProviderAwareConfigRejectsCredentialBearingBaseURL(t *testing.T) {
	config := defaultProviderAwareConfig()
	config.generation.environment = []configProviderVariable{
		{name: "OPENAI_BASE_URL", value: "https://user:password@example.com/v1"},
		{name: "OPENAI_API_KEY", value: "secret"},
	}
	config.credentials = []string{"OPENAI_API_KEY"}

	err := validateProviderAwareConfig(config, true)
	if err == nil || !strings.Contains(err.Error(), "must not contain credentials") ||
		strings.Contains(err.Error(), "password") {
		t.Fatalf("validation error = %v", err)
	}
}

func TestProviderAwareConfigRejectsDuplicateEnvironmentNames(t *testing.T) {
	config := defaultProviderAwareConfig()
	config.generation.environment = []configProviderVariable{
		{name: "OPENAI_API_KEY", value: "one"},
		{name: "OPENAI_API_KEY", value: "two"},
	}

	err := validateProviderAwareConfig(config, true)
	if err == nil || !strings.Contains(err.Error(), "duplicate provider environment variable") {
		t.Fatalf("validation error = %v", err)
	}
}

func TestProviderAwareConfigRejectsUnknownProviderBeforeWriting(t *testing.T) {
	config := defaultProviderAwareConfig()
	config.generation.model = "missing-provider:model"

	err := validateProviderAwareConfig(config, false)
	if err == nil || !strings.Contains(err.Error(), "provider models cannot be configured") {
		t.Fatalf("validation error = %v", err)
	}
}

func TestProviderAwareConfigTemplateAllowsBlankCredentialButStrictValidationRejectsIt(t *testing.T) {
	config := defaultProviderAwareConfig()
	config.generation.environment = []configProviderVariable{{name: "OPENAI_API_KEY", value: ""}}
	config.embedding.environment = []configProviderVariable{{name: "OPENAI_API_KEY", value: ""}}
	config.credentials = []string{"OPENAI_API_KEY"}

	if err := validateProviderAwareConfig(config, false); err != nil {
		t.Fatalf("template validation = %v", err)
	}
	err := validateProviderAwareConfig(config, true)
	if err == nil || !strings.Contains(err.Error(), "provider credential is required") {
		t.Fatalf("strict validation error = %v", err)
	}
}

func TestProviderAwareConfigDoesNotInferCredentialsFromModelPrefix(t *testing.T) {
	config := defaultProviderAwareConfig()
	config.generation = configModelSelection{
		model:       "openai:custom-generation",
		environment: []configProviderVariable{{name: "CUSTOM_CREDENTIAL", value: "secret"}},
	}
	config.embedding = configModelSelection{
		model:       "voyageai:custom-embedding",
		environment: []configProviderVariable{{name: "VOYAGE_API_KEY", value: "secret"}},
	}
	config.credentials = []string{"CUSTOM_CREDENTIAL", "VOYAGE_API_KEY"}

	block, err := renderProviderAwareConfigBlock(config)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(block, "# credentials=CUSTOM_CREDENTIAL,VOYAGE_API_KEY") ||
		strings.Contains(block, "OPENAI_API_KEY") {
		t.Fatalf("managed block = %q", block)
	}
}

func TestCollectProviderAwareCustomConnectionMarksOnlyExplicitCredentials(t *testing.T) {
	input := strings.NewReader(strings.Join([]string{
		"", "", "custom",
		"openai:custom-generation",
		"CUSTOM_CREDENTIAL", "true", "custom-secret",
		"AWS_REGION", "false", "us-west-2", "",
		"voyageai:custom-embedding",
		"VOYAGE_API_KEY", "true", "voyage-secret", "",
		"1536",
	}, "\n") + "\n")
	var output bytes.Buffer
	config, err := collectProviderAwareConfig(input, &output)
	if err != nil {
		t.Fatal(err)
	}
	if config.generation.model != "openai:custom-generation" ||
		config.embedding.model != "voyageai:custom-embedding" ||
		strings.Join(config.credentials, ",") != "CUSTOM_CREDENTIAL,VOYAGE_API_KEY" {
		t.Fatalf("configuration = %#v", config)
	}
	block, err := renderProviderAwareConfigBlock(config)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(block, "# credentials=CUSTOM_CREDENTIAL,VOYAGE_API_KEY") ||
		strings.Contains(block, "OPENAI_API_KEY") || !strings.Contains(block, "AWS_REGION=\"us-west-2\"") {
		t.Fatalf("managed block = %q", block)
	}
}

func TestCollectProviderAwareConfigDeduplicatesSharedCustomCredential(t *testing.T) {
	input := strings.NewReader(strings.Join([]string{
		"", "", "custom",
		"openai-chat:gpt-4.1-mini",
		"OPENAI_API_KEY", "true", "shared-secret", "",
		"openai:text-embedding-3-small",
		"OPENAI_API_KEY", "true", "shared-secret", "",
		"1536",
	}, "\n") + "\n")

	config, err := collectProviderAwareConfig(input, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if credentials := strings.Join(config.credentials, ","); credentials != "OPENAI_API_KEY" {
		t.Fatalf("credentials = %q", credentials)
	}
	if err := validateProviderAwareConfig(config, true); err != nil {
		t.Fatal(err)
	}
}
