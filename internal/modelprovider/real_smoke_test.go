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
	"io"
	"math"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/ob-labs/powercontext-go/inference"
)

// TestRealProviderSmoke is deliberately opt-in. CI's deterministic provider
// matrix uses fake HTTP servers; this test is the narrow credentialed check
// that protocol adapters can complete one real request without logging model
// input, model output, or credentials.
func TestRealProviderSmoke(t *testing.T) {
	config, err := realSmokeConfigurationFromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if config.generationModel == "" && config.embeddingModel == "" {
		t.Skip("set a POWERCONTEXT_REAL_SMOKE_*_MODEL variable to call a real provider")
	}

	client := &http.Client{
		Timeout: 75 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	factory, err := NewFactory(MilestoneB, ProcessEnvironment, client)
	if err != nil {
		t.Fatal(err)
	}
	if config.generationModel != "" {
		t.Run("generation", func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
			defer cancel()
			model, buildErr := factory.TextModel(config.generationModel)
			if buildErr != nil {
				t.Fatal(buildErr)
			}
			codec, buildErr := inference.NewJSONCodec[map[string]string, realSmokeOutput](
				[]byte(`{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"],"additionalProperties":false}`),
				nil,
				decodeRealSmokeOutput,
			)
			if buildErr != nil {
				t.Fatal(buildErr)
			}
			limits, _ := inference.NewLimits(75*time.Second, 1)
			settings, _ := inference.NewGenerationSettings(nil, nil)
			generator, buildErr := inference.NewPromptedGenerator(
				model,
				"Return an object whose ok field is true.",
				codec,
				&limits,
				settings,
			)
			if buildErr != nil {
				t.Fatal(buildErr)
			}
			result, callErr := generator.Generate(ctx, map[string]string{"request": "provider-smoke"})
			if callErr != nil {
				t.Fatal(callErr)
			}
			if !result.Output.OK || result.Usage.Requests != 1 {
				t.Fatal("real generation provider returned an invalid smoke result")
			}
		})
	}

	if config.embeddingModel != "" {
		t.Run("embedding", func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
			defer cancel()
			transport, buildErr := factory.EmbeddingTransport(config.embeddingModel)
			if buildErr != nil {
				t.Fatal(buildErr)
			}
			if closer, ok := transport.(interface{ Close(context.Context) error }); ok {
				t.Cleanup(func() {
					closeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()
					if closeErr := closer.Close(closeCtx); closeErr != nil {
						t.Errorf("close real embedding provider: %v", closeErr)
					}
				})
			}
			result, callErr := transport.Embed(
				ctx,
				embeddingRequestForProviderTest(
					t,
					[]string{"PowerContext provider smoke test."},
					inference.EmbeddingDocument,
					config.embeddingDimension,
				),
			)
			if callErr != nil {
				t.Fatal(callErr)
			}
			vectors := result.Embeddings()
			if len(vectors) != 1 || len(vectors[0]) != config.embeddingDimension {
				t.Fatal("real embedding provider returned an invalid vector count")
			}
			for _, value := range vectors[0] {
				if math.IsNaN(value) || math.IsInf(value, 0) {
					t.Fatal("real embedding provider returned a non-finite vector")
				}
			}
		})
	}
}

type realSmokeConfiguration struct {
	generationModel    string
	embeddingModel     string
	embeddingDimension int
}

func realSmokeConfigurationFromEnvironment() (realSmokeConfiguration, error) {
	config := realSmokeConfiguration{
		generationModel: os.Getenv("POWERCONTEXT_REAL_SMOKE_GENERATION_MODEL"),
		embeddingModel:  os.Getenv("POWERCONTEXT_REAL_SMOKE_EMBEDDING_MODEL"),
	}
	if config.embeddingModel == "" {
		return config, nil
	}
	dimension, err := strconv.Atoi(os.Getenv("POWERCONTEXT_REAL_SMOKE_EMBEDDING_DIMENSION"))
	if err != nil || dimension < 1 {
		return realSmokeConfiguration{}, errors.New("POWERCONTEXT_REAL_SMOKE_EMBEDDING_DIMENSION must be a positive integer")
	}
	config.embeddingDimension = dimension
	return config, nil
}

func TestRealSmokeConfigurationRequiresModelSupportedEmbeddingDimension(t *testing.T) {
	tests := []struct {
		name               string
		generationModel    string
		embeddingModel     string
		embeddingDimension string
		wantDimension      int
		wantError          bool
	}{
		{name: "generation only", generationModel: "openai:test"},
		{name: "explicit model dimension", embeddingModel: "openai:text-embedding-3-small", embeddingDimension: "768", wantDimension: 768},
		{name: "missing dimension", embeddingModel: "openai:text-embedding-3-small", wantError: true},
		{name: "zero dimension", embeddingModel: "openai:text-embedding-3-small", embeddingDimension: "0", wantError: true},
		{name: "negative dimension", embeddingModel: "openai:text-embedding-3-small", embeddingDimension: "-1", wantError: true},
		{name: "non-numeric dimension", embeddingModel: "openai:text-embedding-3-small", embeddingDimension: "three", wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("POWERCONTEXT_REAL_SMOKE_GENERATION_MODEL", test.generationModel)
			t.Setenv("POWERCONTEXT_REAL_SMOKE_EMBEDDING_MODEL", test.embeddingModel)
			t.Setenv("POWERCONTEXT_REAL_SMOKE_EMBEDDING_DIMENSION", test.embeddingDimension)
			config, err := realSmokeConfigurationFromEnvironment()
			if (err != nil) != test.wantError {
				t.Fatalf("error = %v, want error %t", err, test.wantError)
			}
			if err == nil && config.embeddingDimension != test.wantDimension {
				t.Fatalf("embedding dimension = %d, want %d", config.embeddingDimension, test.wantDimension)
			}
		})
	}
}

type realSmokeOutput struct {
	OK bool `json:"ok"`
}

func decodeRealSmokeOutput(value []byte) (realSmokeOutput, error) {
	var output realSmokeOutput
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&output); err != nil {
		return realSmokeOutput{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return realSmokeOutput{}, errors.New("trailing JSON data")
	}
	return output, nil
}
