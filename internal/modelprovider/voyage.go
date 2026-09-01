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
	"slices"
	"strings"

	"github.com/ob-labs/powercontext-go/inference"
)

type VoyageAIConfig struct {
	APIKey     string
	BaseURL    string
	HTTPClient *http.Client
}

type VoyageAIEmbeddingTransport struct {
	route     Route
	transport providerHTTPClient
}

func NewVoyageAIEmbeddingTransport(route Route, config VoyageAIConfig) (*VoyageAIEmbeddingTransport, error) {
	if route.protocol != ProtocolVoyageAI {
		return nil, inference.NewConfigurationError("embedding-model", "route is not the VoyageAI embedding protocol")
	}
	if strings.TrimSpace(config.APIKey) == "" {
		return nil, inference.NewConfigurationError("embedding-model", "VoyageAI API key is required")
	}
	transport, err := newProviderHTTPClient(config.HTTPClient, config.BaseURL, http.Header{
		"Authorization": []string{"Bearer " + config.APIKey},
	})
	if err != nil {
		return nil, err
	}
	return &VoyageAIEmbeddingTransport{route: route, transport: transport}, nil
}

type voyageEmbeddingRequest struct {
	Input           []string `json:"input"`
	Model           string   `json:"model"`
	InputType       string   `json:"input_type"`
	OutputDimension int      `json:"output_dimension"`
}

type voyageEmbeddingResponse struct {
	Data []struct {
		Embedding []float64 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
	Usage struct {
		TotalTokens *int64 `json:"total_tokens"`
	} `json:"usage"`
}

func (t *VoyageAIEmbeddingTransport) Embed(
	ctx context.Context,
	request inference.EmbeddingRequest,
) (inference.ProviderEmbeddingResult, error) {
	inputs := request.Inputs()
	inputType := request.InputType()
	wireType := "document"
	if inputType == inference.EmbeddingQuery {
		wireType = "query"
	}
	var response voyageEmbeddingResponse
	if err := t.transport.postJSON(ctx, "/embeddings", voyageEmbeddingRequest{
		Input: slices.Clone(inputs), Model: t.route.model, InputType: wireType,
		OutputDimension: request.DimensionCount(),
	}, &response, "embed"); err != nil {
		return inference.ProviderEmbeddingResult{}, err
	}
	vectors := make([][]float64, len(response.Data))
	seen := make([]bool, len(response.Data))
	for _, item := range response.Data {
		if item.Index < 0 || item.Index >= len(vectors) || seen[item.Index] {
			return inference.ProviderEmbeddingResult{}, inference.NewInvalidOutputError("embed", "provider returned invalid embedding indexes")
		}
		seen[item.Index] = true
		vectors[item.Index] = slices.Clone(item.Embedding)
	}
	return inference.NewProviderEmbeddingResult(inputs, inputType, vectors, inference.Usage{
		InputTokens: cloneInt64Pointer(response.Usage.TotalTokens),
	})
}

var _ inference.EmbeddingTransport = (*VoyageAIEmbeddingTransport)(nil)
