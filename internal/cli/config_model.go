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
	"encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/ob-labs/powercontext-go/inference"
	"github.com/ob-labs/powercontext-go/internal/modelprovider"
	"github.com/ob-labs/powercontext-go/server"
)

type configProviderVariable struct {
	name  string
	value string
}

type configModelSelection struct {
	model       string
	environment []configProviderVariable
}

type providerAwareConfig struct {
	scopeID            string
	displayName        string
	generation         configModelSelection
	embedding          configModelSelection
	embeddingProfileID string
	embeddingDimension int
	credentials        []string
}

type configDashboardScope struct {
	ScopeID     string `json:"scope_id"`
	DisplayName string `json:"display_name"`
}

var configProfileComponent = regexp.MustCompile(`[^a-z0-9]+`)

func defaultProviderAwareConfig() providerAwareConfig {
	return providerAwareConfig{
		scopeID: "project:quickstart", displayName: "Quick Start",
		generation: configModelSelection{
			model: "openai-chat:gpt-4.1-mini",
			environment: []configProviderVariable{
				{name: "OPENAI_BASE_URL", value: "https://api.openai.com/v1"},
				{name: "OPENAI_API_KEY", value: ""},
			},
		},
		embedding: configModelSelection{
			model: "openai:text-embedding-3-small",
			environment: []configProviderVariable{
				{name: "OPENAI_BASE_URL", value: "https://api.openai.com/v1"},
				{name: "OPENAI_API_KEY", value: ""},
			},
		},
		embeddingProfileID: "openai-text-embedding-3-small-1536-unit-v1",
		embeddingDimension: 1536,
		credentials:        []string{"OPENAI_API_KEY"},
	}
}

func validateProviderAwareConfig(config providerAwareConfig, strictCredentials bool) error {
	if strings.TrimSpace(config.scopeID) == "" || strings.TrimSpace(config.displayName) == "" {
		return errors.New("Scope ID and Dashboard display name are required")
	}
	if len(config.scopeID) > 255 {
		return errors.New("Scope ID must contain at most 255 characters")
	}
	if len(config.displayName) > 80 {
		return errors.New("Dashboard display name must contain at most 80 characters")
	}
	if config.embeddingDimension < 1 {
		return errors.New("Embedding dimension must be positive")
	}

	values := make(map[string]string)
	if err := validateConfigModelSelection(config.generation, modelprovider.Generation, values); err != nil {
		return err
	}
	if err := validateConfigModelSelection(config.embedding, modelprovider.Embedding, values); err != nil {
		return err
	}
	credentials := make(map[string]struct{}, len(config.credentials))
	for _, name := range config.credentials {
		if !configEnvironmentName.MatchString(name) {
			return errors.New("invalid credential environment variable")
		}
		if _, exists := credentials[name]; exists {
			return errors.New("duplicate credential environment variable")
		}
		credentials[name] = struct{}{}
		if strictCredentials && strings.TrimSpace(values[name]) == "" {
			return errors.New("provider credential is required")
		}
	}
	if !strictCredentials {
		return nil
	}
	factory, err := modelprovider.NewFactory(modelprovider.MilestoneB, lookupConfigEnvironment(values), nil)
	if err != nil {
		return errors.New("provider models cannot be configured")
	}
	if _, err := factory.TextModel(config.generation.model); err != nil {
		return configProviderValidationError(err)
	}
	if _, err := factory.EmbeddingTransport(config.embedding.model); err != nil {
		return configProviderValidationError(err)
	}
	return nil
}

func validateConfigModelSelection(
	selection configModelSelection,
	capability modelprovider.Capability,
	values map[string]string,
) error {
	route, err := modelprovider.Resolve(selection.model, capability)
	if err != nil || modelprovider.RequireAvailable(route, modelprovider.MilestoneB) != nil {
		return errors.New("provider models cannot be configured")
	}
	seen := make(map[string]struct{}, len(selection.environment))
	for _, variable := range selection.environment {
		if !configEnvironmentName.MatchString(variable.name) {
			return errors.New("invalid provider environment variable")
		}
		if _, exists := seen[variable.name]; exists {
			return errors.New("duplicate provider environment variable")
		}
		seen[variable.name] = struct{}{}
		if configBaseURLName(variable.name) {
			parsed, parseErr := url.Parse(variable.value)
			if parseErr != nil || parsed.User != nil {
				return errors.New("provider Base URL must not contain credentials")
			}
		}
		if existing, exists := values[variable.name]; exists && existing != variable.value {
			return fmt.Errorf("Generation and Embedding selected conflicting values for %s", variable.name)
		}
		values[variable.name] = variable.value
	}
	return nil
}

func configBaseURLName(name string) bool {
	upper := strings.ToUpper(name)
	return strings.HasSuffix(upper, "_BASE_URL") || strings.HasSuffix(upper, "_ENDPOINT") ||
		strings.HasSuffix(upper, "_INFERENCE_URL")
}

func lookupConfigEnvironment(values map[string]string) modelprovider.EnvLookup {
	return func(name string) (string, bool) {
		value, found := values[name]
		return value, found
	}
}

func configProviderValidationError(err error) error {
	configuration, ok := errors.AsType[*inference.ConfigurationError](err)
	if ok && strings.Contains(configuration.Detail(), "requires") {
		return errors.New("provider credential is required")
	}
	return errors.New("provider models cannot be configured")
}

func renderProviderAwareEnvironment(config providerAwareConfig) (map[string]string, error) {
	if err := validateProviderAwareConfig(config, false); err != nil {
		return nil, err
	}
	defaults, err := server.DefaultConfig()
	if err != nil {
		return nil, err
	}
	scopes, err := json.Marshal([]configDashboardScope{{ScopeID: config.scopeID, DisplayName: config.displayName}})
	if err != nil {
		return nil, err
	}
	values := map[string]string{
		"POWERCONTEXT_SERVER_HTTP_HOST":                         defaults.HTTP.Host,
		"POWERCONTEXT_SERVER_HTTP_PORT":                         strconv.Itoa(defaults.HTTP.Port),
		"POWERCONTEXT_SERVER_MCP_ENABLED":                       strconv.FormatBool(defaults.MCP.Enabled),
		"POWERCONTEXT_SERVER_MCP_PATH":                          defaults.MCP.Path,
		"POWERCONTEXT_SERVER_AUTH_ENABLED":                      strconv.FormatBool(defaults.Auth.Enabled),
		"POWERCONTEXT_SERVER_DASHBOARD_ENABLED":                 "true",
		"POWERCONTEXT_SERVER_DASHBOARD_SCOPES":                  string(scopes),
		"POWERCONTEXT_SERVER_DATABASE_KIND":                     "sqlite",
		"POWERCONTEXT_SERVER_DATABASE_URL":                      defaults.Database.SQLite.URL,
		"POWERCONTEXT_SERVER_RUNTIME_SOURCE_WINDOW_LIMIT":       strconv.FormatInt(defaults.Runtime.SourceWindowLimit, 10),
		"POWERCONTEXT_SERVER_INFERENCE_GENERATION_MODEL":        config.generation.model,
		"POWERCONTEXT_SERVER_INFERENCE_EMBEDDING_MODEL":         config.embedding.model,
		"POWERCONTEXT_SERVER_INFERENCE_EMBEDDING_PROFILE_ID":    config.embeddingProfileID,
		"POWERCONTEXT_SERVER_INFERENCE_EMBEDDING_DIMENSION":     strconv.Itoa(config.embeddingDimension),
		"POWERCONTEXT_SERVER_INFERENCE_EMBEDDING_NORMALIZATION": "unit",
	}
	for _, selection := range []configModelSelection{config.generation, config.embedding} {
		for _, variable := range selection.environment {
			if existing, exists := values[variable.name]; exists && existing != variable.value {
				return nil, fmt.Errorf("Generation and Embedding selected conflicting values for %s", variable.name)
			}
			values[variable.name] = variable.value
		}
	}
	return values, nil
}

func renderProviderAwareConfigBlock(config providerAwareConfig) (string, error) {
	values, err := renderProviderAwareEnvironment(config)
	if err != nil {
		return "", err
	}
	lines := []string{
		configManagedBegin,
		"# config-version=1",
		"# generation-environment=" + configEnvironmentNames(config.generation),
		"# embedding-environment=" + configEnvironmentNames(config.embedding),
	}
	if len(config.credentials) > 0 {
		lines = append(lines, "# credentials="+strings.Join(config.credentials, ","))
	}
	for _, name := range slices.Sorted(maps.Keys(values)) {
		lines = append(lines, name+"="+strconv.Quote(values[name]))
	}
	lines = append(lines, configManagedEnd)
	return strings.Join(lines, "\n"), nil
}

func configEnvironmentNames(selection configModelSelection) string {
	names := make([]string, 0, len(selection.environment))
	for _, variable := range selection.environment {
		names = append(names, variable.name)
	}
	return strings.Join(names, ",")
}

func configProfileID(model string, dimension int) string {
	normalized := strings.Trim(configProfileComponent.ReplaceAllString(strings.ToLower(model), "-"), "-")
	return fmt.Sprintf("%s-%d-unit-v1", normalized, dimension)
}

func providerAwareManagedEnvironment(config providerAwareConfig) map[string]string {
	values, err := renderProviderAwareEnvironment(config)
	if err != nil {
		return nil
	}
	return values
}

func replaceConfigManagedBlock(content, block string) string {
	begin := configManagedBeginLine.FindStringIndex(content)
	end := configManagedEndLine.FindStringIndex(content)
	if begin == nil || end == nil || end[0] < begin[1] {
		return content
	}
	return joinConfigDocument(content[:begin[0]], block, content[end[1]:])
}

func providerAwareConfigFromDocument(content string, values map[string]string) (*providerAwareConfig, error) {
	metadata, err := configManagedMetadata(content)
	if err != nil {
		return nil, err
	}
	if metadata == nil {
		return nil, nil
	}
	generationModel, generationConfigured := values["POWERCONTEXT_SERVER_INFERENCE_GENERATION_MODEL"]
	embeddingModel, embeddingConfigured := values["POWERCONTEXT_SERVER_INFERENCE_EMBEDDING_MODEL"]
	if !generationConfigured && !embeddingConfigured {
		return nil, nil
	}
	if !generationConfigured || !embeddingConfigured {
		return nil, errors.New("both generation and embedding models must be configured")
	}
	dimensionValue, found := values["POWERCONTEXT_SERVER_INFERENCE_EMBEDDING_DIMENSION"]
	if !found {
		return nil, errors.New("POWERCONTEXT_SERVER_INFERENCE_EMBEDDING_DIMENSION must be an integer")
	}
	dimension, parseErr := strconv.Atoi(dimensionValue)
	if parseErr != nil {
		return nil, errors.New("POWERCONTEXT_SERVER_INFERENCE_EMBEDDING_DIMENSION must be an integer")
	}
	generation, err := configSelectionFromMetadata("generation", generationModel, metadata, values)
	if err != nil {
		return nil, err
	}
	embedding, err := configSelectionFromMetadata("embedding", embeddingModel, metadata, values)
	if err != nil {
		return nil, err
	}
	credentials := strings.Split(metadata["credentials"], ",")
	if len(credentials) == 1 && credentials[0] == "" {
		credentials = nil
	}
	profileID := values["POWERCONTEXT_SERVER_INFERENCE_EMBEDDING_PROFILE_ID"]
	if profileID == "" {
		profileID = configProfileID(embeddingModel, dimension)
	}
	return &providerAwareConfig{
		scopeID: "project:quickstart", displayName: "Quick Start",
		generation: generation, embedding: embedding, embeddingProfileID: profileID,
		embeddingDimension: dimension, credentials: credentials,
	}, nil
}

func configManagedMetadata(content string) (map[string]string, error) {
	beginMarkers := configManagedBeginLine.FindAllStringIndex(content, -1)
	endMarkers := configManagedEndLine.FindAllStringIndex(content, -1)
	if len(beginMarkers) == 0 && len(endMarkers) == 0 {
		return nil, nil
	}
	if len(beginMarkers) != 1 || len(endMarkers) != 1 || endMarkers[0][0] < beginMarkers[0][1] {
		return nil, errors.New("environment contains mismatched or repeated PowerContext managed markers")
	}
	metadata := make(map[string]string)
	for line := range strings.SplitSeq(content[beginMarkers[0][1]:endMarkers[0][0]], "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "# ") {
			continue
		}
		name, value, found := strings.Cut(strings.TrimPrefix(line, "# "), "=")
		if found {
			metadata[name] = value
		}
	}
	return metadata, nil
}

func configSelectionFromMetadata(
	role, model string,
	metadata map[string]string,
	values map[string]string,
) (configModelSelection, error) {
	name := role + "-environment"
	variableNames := strings.Split(metadata[name], ",")
	if len(variableNames) == 1 && variableNames[0] == "" {
		variableNames = nil
	}
	variables := make([]configProviderVariable, 0, len(variableNames))
	for _, variableName := range variableNames {
		value, found := values[variableName]
		if !found {
			return configModelSelection{}, fmt.Errorf("missing provider environment variable: %s", variableName)
		}
		variables = append(variables, configProviderVariable{name: variableName, value: value})
	}
	return configModelSelection{model: model, environment: variables}, nil
}
