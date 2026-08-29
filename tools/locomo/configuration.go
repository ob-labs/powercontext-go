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

package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/ob-labs/powercontext-go/artifact/memory"
	"github.com/ob-labs/powercontext-go/inference"
	"github.com/ob-labs/powercontext-go/internal/benchmark/locomo"
	"github.com/ob-labs/powercontext-go/internal/modelprovider"
	"github.com/ob-labs/powercontext-go/server"
)

var environmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func loadConfiguration(path string) (server.ProcessConfig, error) {
	if err := loadEnvironmentFile(path); err != nil {
		return server.ProcessConfig{}, err
	}
	return server.LoadConfig()
}

func publicConfiguration(config server.ProcessConfig) (locomo.PublicConfiguration, error) {
	version, err := memory.ExtractionInstructionsVersion(config.Runtime.MemoryExtractionProfile)
	if err != nil {
		return locomo.PublicConfiguration{}, err
	}
	return locomo.PublicConfiguration{
		DatabaseKind: config.Database.Kind, GenerationModel: config.Inference.GenerationModel,
		EmbeddingModel: config.Inference.EmbeddingModel, EmbeddingProfileID: config.Inference.EmbeddingProfileID,
		EmbeddingDimension:           config.Inference.EmbeddingDimension,
		EmbeddingNormalization:       config.Inference.EmbeddingNormalization,
		EmbeddingBatchSize:           config.Inference.EmbeddingBatchSize,
		MemoryExtractionProfile:      string(config.Runtime.MemoryExtractionProfile),
		MemoryExtractionInstructions: version,
	}, nil
}

type benchmarkApplication struct {
	application *server.Application
	operations  endpointOperations
	model       inference.TextModel
}

func openBenchmarkApplication(
	ctx context.Context,
	config server.ProcessConfig,
	rerank locomo.RerankMode,
	candidateLimit int,
	needAnswerModel bool,
) (*benchmarkApplication, error) {
	config.Runtime.SourceWindowLimit = 1
	config.Runtime.SourceWindowInterval = nil
	config.Runtime.ExperienceIncubationInterval = nil
	config.Runtime.MemoryRerankEnabled = rerank == locomo.RerankLLM
	config.Runtime.MemoryRerankCandidateLimit = candidateLimit
	var model inference.TextModel
	var recorder *recordingReranker
	dependencies := server.Dependencies{}
	if needAnswerModel || rerank == locomo.RerankLLM {
		var err error
		model, err = textModel(config.Inference.GenerationModel, nil)
		if err != nil {
			return nil, err
		}
	}
	if rerank == locomo.RerankLLM {
		limits, err := inference.NewLimits(config.Inference.GenerationTimeout, config.Inference.GenerationMaxRequests)
		if err != nil {
			return nil, err
		}
		generator, err := memory.NewRerankPromptedGenerator(model, &limits)
		if err != nil {
			return nil, err
		}
		reranker, err := memory.NewLLMReranker(generator)
		if err != nil {
			return nil, err
		}
		recorder = &recordingReranker{next: reranker}
		dependencies.MemoryReranker = recorder
	}
	application, err := server.OpenApplication(ctx, config, dependencies)
	if err != nil {
		return nil, err
	}
	return &benchmarkApplication{
		application: application,
		operations:  endpointOperations{handler: application.Endpoint(), recorder: recorder},
		model:       model,
	}, nil
}

func (a *benchmarkApplication) Close(ctx context.Context) error {
	if a == nil || a.application == nil {
		return nil
	}
	return a.application.Close(ctx)
}

func textModel(modelID string, client *http.Client) (inference.TextModel, error) {
	if strings.TrimSpace(modelID) == "" {
		return nil, fmt.Errorf("LoCoMo answer evaluation requires a configured generation model")
	}
	factory, err := modelprovider.NewFactory(modelprovider.MilestoneB, modelprovider.ProcessEnvironment, client)
	if err != nil {
		return nil, err
	}
	return factory.TextModel(modelID)
}

func loadEnvironmentFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open benchmark environment file: %w", err)
	}
	defer func() { _ = file.Close() }()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4<<10), 1<<20)
	for line := 1; scanner.Scan(); line++ {
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		text = strings.TrimSpace(strings.TrimPrefix(text, "export "))
		name, raw, found := strings.Cut(text, "=")
		name = strings.TrimSpace(name)
		if !found || !environmentName.MatchString(name) {
			return fmt.Errorf("invalid benchmark environment assignment on line %d", line)
		}
		value, err := parseEnvironmentValue(strings.TrimSpace(raw))
		if err != nil {
			return fmt.Errorf("invalid benchmark environment value on line %d: %w", line, err)
		}
		if err := os.Setenv(name, value); err != nil {
			return fmt.Errorf("set benchmark environment on line %d: %w", line, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read benchmark environment file: %w", err)
	}
	return nil
}

func parseEnvironmentValue(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if value[0] == '\'' {
		if len(value) < 2 || value[len(value)-1] != '\'' {
			return "", errors.New("unterminated single-quoted value")
		}
		return value[1 : len(value)-1], nil
	}
	if value[0] == '"' {
		decoded, err := strconv.Unquote(value)
		if err != nil {
			return "", err
		}
		return decoded, nil
	}
	if index := strings.Index(value, " #"); index >= 0 {
		value = value[:index]
	}
	return strings.TrimSpace(value), nil
}
