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
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"golang.org/x/term"
)

func collectProviderAwareConfig(input io.Reader, output io.Writer) (providerAwareConfig, error) {
	config := defaultProviderAwareConfig()
	var err error
	if config.scopeID, err = configPrompt(input, output, "Scope ID", config.scopeID, false); err != nil {
		return providerAwareConfig{}, err
	}
	if config.displayName, err = configPrompt(input, output, "Dashboard display name", config.displayName, false); err != nil {
		return providerAwareConfig{}, err
	}
	protocol, err := configPrompt(input, output, "Generation API protocol", "openai-chat", false)
	if err != nil {
		return providerAwareConfig{}, err
	}
	switch protocol {
	case "openai-chat", "openai-responses":
		config.generation.model, err = configPrompt(input, output, "Generation model", "gpt-4.1-mini", false)
		if err != nil {
			return providerAwareConfig{}, err
		}
		config.generation.model = protocol + ":" + config.generation.model
		baseURL, promptErr := configPrompt(input, output, "Generation API Base URL", "https://api.openai.com/v1", false)
		if promptErr != nil {
			return providerAwareConfig{}, promptErr
		}
		credential, promptErr := configPrompt(input, output, "Generation API key", "", true)
		if promptErr != nil {
			return providerAwareConfig{}, promptErr
		}
		config.generation.environment = []configProviderVariable{
			{name: "OPENAI_BASE_URL", value: baseURL},
			{name: "OPENAI_API_KEY", value: credential},
		}
		config.embedding.model, err = configPrompt(input, output, "Embedding model", "text-embedding-3-small", false)
		if err != nil {
			return providerAwareConfig{}, err
		}
		config.embedding.model = "openai:" + config.embedding.model
		config.embedding.environment = append([]configProviderVariable(nil), config.generation.environment...)
		config.credentials = []string{"OPENAI_API_KEY"}
	case "custom":
		generation, generationCredentials, collectErr := collectCustomConfigConnection(input, output, "generation")
		if collectErr != nil {
			return providerAwareConfig{}, collectErr
		}
		embedding, embeddingCredentials, collectErr := collectCustomConfigConnection(input, output, "embedding")
		if collectErr != nil {
			return providerAwareConfig{}, collectErr
		}
		config.generation, config.embedding = generation, embedding
		config.credentials = uniqueConfigCredentialNames(append(generationCredentials, embeddingCredentials...))
	default:
		return providerAwareConfig{}, errors.New("Generation API protocol is not supported")
	}
	dimension, err := configPrompt(input, output, "Embedding dimension", strconv.Itoa(config.embeddingDimension), false)
	if err != nil {
		return providerAwareConfig{}, err
	}
	config.embeddingDimension, err = strconv.Atoi(dimension)
	if err != nil {
		return providerAwareConfig{}, errors.New("Embedding dimension must be an integer")
	}
	config.embeddingProfileID = configProfileID(config.embedding.model, config.embeddingDimension)
	return config, nil
}

func uniqueConfigCredentialNames(names []string) []string {
	seen := make(map[string]struct{}, len(names))
	unique := make([]string, 0, len(names))
	for _, name := range names {
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		unique = append(unique, name)
	}
	return unique
}

func collectCustomConfigConnection(
	input io.Reader,
	output io.Writer,
	role string,
) (configModelSelection, []string, error) {
	model, err := configPrompt(input, output, role+" model identifier", "openai-chat:model-name", false)
	if err != nil {
		return configModelSelection{}, nil, err
	}
	variables := make([]configProviderVariable, 0)
	credentials := make([]string, 0)
	for {
		name, promptErr := configPrompt(input, output, "Additional "+role+" provider environment variable name", "", false)
		if promptErr != nil {
			return configModelSelection{}, nil, promptErr
		}
		if name == "" {
			break
		}
		if !configEnvironmentName.MatchString(name) {
			return configModelSelection{}, nil, errors.New("invalid provider environment variable")
		}
		credentialMarker, promptErr := configPrompt(input, output, "Treat "+name+" as a credential", "false", false)
		if promptErr != nil {
			return configModelSelection{}, nil, promptErr
		}
		isCredential, parseErr := strconv.ParseBool(credentialMarker)
		if parseErr != nil {
			return configModelSelection{}, nil, errors.New("credential marker must be true or false")
		}
		value, promptErr := configPrompt(input, output, name, "", isCredential)
		if promptErr != nil {
			return configModelSelection{}, nil, promptErr
		}
		variables = append(variables, configProviderVariable{name: name, value: value})
		if isCredential {
			credentials = append(credentials, name)
		}
	}
	return configModelSelection{model: model, environment: variables}, credentials, nil
}

func configPrompt(input io.Reader, output io.Writer, label, fallback string, secret bool) (string, error) {
	if output == nil {
		output = io.Discard
	}
	if _, err := fmt.Fprintf(output, "%s [%s]: ", label, fallback); err != nil {
		return "", err
	}
	if secret {
		if file, ok := input.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
			value, err := term.ReadPassword(int(file.Fd()))
			if _, writeErr := fmt.Fprintln(output); err == nil && writeErr != nil {
				err = writeErr
			}
			if err != nil {
				return "", errors.New("cannot read credential input")
			}
			return strings.TrimSpace(string(value)), nil
		}
	}
	line, err := readConfigPromptLine(input)
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(line)
	if value == "" {
		return fallback, nil
	}
	return value, nil
}

func readConfigPromptLine(input io.Reader) (string, error) {
	line := make([]byte, 0, 64)
	buffer := []byte{0}
	for {
		count, err := input.Read(buffer)
		if count > 0 && buffer[0] != '\n' {
			line = append(line, buffer[0])
		}
		if count > 0 && buffer[0] == '\n' {
			return string(line), nil
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return string(line), nil
			}
			return "", err
		}
	}
}
