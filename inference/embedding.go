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

package inference

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
)

type EmbeddingInputType string

const (
	EmbeddingDocument EmbeddingInputType = "document"
	EmbeddingQuery    EmbeddingInputType = "query"
)

type EmbeddingRequest struct {
	inputs    []string
	inputType EmbeddingInputType
	dimension int
}

func NewEmbeddingRequest(inputs []string, inputType EmbeddingInputType, dimension int) (EmbeddingRequest, error) {
	if inputType != EmbeddingDocument && inputType != EmbeddingQuery {
		return EmbeddingRequest{}, NewConfigurationError("request-rejected", "invalid embedding input type")
	}
	if dimension < 1 {
		return EmbeddingRequest{}, NewConfigurationError("dimension-positive", "")
	}
	return EmbeddingRequest{inputs: slices.Clone(inputs), inputType: inputType, dimension: dimension}, nil
}

func (r EmbeddingRequest) Inputs() []string              { return slices.Clone(r.inputs) }
func (r EmbeddingRequest) InputType() EmbeddingInputType { return r.inputType }
func (r EmbeddingRequest) DimensionCount() int           { return r.dimension }

type ProviderEmbeddingResult struct {
	inputs     []string
	inputType  EmbeddingInputType
	embeddings [][]float64
	usage      Usage
}

func NewProviderEmbeddingResult(
	inputs []string,
	inputType EmbeddingInputType,
	embeddings [][]float64,
	usage Usage,
) (ProviderEmbeddingResult, error) {
	if err := validateUsage(usage); err != nil {
		return ProviderEmbeddingResult{}, err
	}
	return ProviderEmbeddingResult{
		inputs: slices.Clone(inputs), inputType: inputType,
		embeddings: cloneVectors(embeddings), usage: cloneUsage(usage),
	}, nil
}

func (r ProviderEmbeddingResult) Inputs() []string              { return slices.Clone(r.inputs) }
func (r ProviderEmbeddingResult) InputType() EmbeddingInputType { return r.inputType }
func (r ProviderEmbeddingResult) Embeddings() [][]float64       { return cloneVectors(r.embeddings) }
func (r ProviderEmbeddingResult) Usage() Usage                  { return cloneUsage(r.usage) }

type EmbeddingTransport interface {
	Embed(context.Context, EmbeddingRequest) (ProviderEmbeddingResult, error)
}

type fixedEmbeddingProfile struct {
	id            string
	model         string
	dimension     int
	normalization string
}

func (p fixedEmbeddingProfile) ID() string                { return p.id }
func (p fixedEmbeddingProfile) ModelName() string         { return p.model }
func (p fixedEmbeddingProfile) DimensionCount() int       { return p.dimension }
func (p fixedEmbeddingProfile) NormalizationMode() string { return p.normalization }

type BatchedEmbeddingModel struct {
	transport EmbeddingTransport
	profile   fixedEmbeddingProfile
	batchSize int
	limits    Limits
}

func NewBatchedEmbeddingModel(
	transport EmbeddingTransport,
	profile EmbeddingProfile,
	batchSize int,
	limits *Limits,
) (*BatchedEmbeddingModel, error) {
	if transport == nil || profile == nil {
		return nil, NewConfigurationError("embedding-model", "")
	}
	if batchSize < 1 {
		return nil, NewConfigurationError("embedding-batch", "")
	}
	if profile.DimensionCount() < 1 {
		return nil, NewConfigurationError("dimension-positive", "")
	}
	if strings.TrimSpace(profile.ID()) == "" || strings.TrimSpace(profile.ModelName()) == "" || strings.TrimSpace(profile.NormalizationMode()) == "" {
		return nil, NewConfigurationError("profile-identifiers", "")
	}
	if profile.NormalizationMode() != "none" && profile.NormalizationMode() != "unit" {
		return nil, NewConfigurationError("profile-identifiers", "unsupported normalization")
	}
	resolvedLimits := DefaultLimits()
	if limits != nil {
		if limits.timeout <= 0 || limits.maxRequests < 1 {
			return nil, NewConfigurationError("request-rejected", "invalid limits")
		}
		resolvedLimits = *limits
	}
	return &BatchedEmbeddingModel{
		transport: transport,
		profile: fixedEmbeddingProfile{
			id: profile.ID(), model: profile.ModelName(), dimension: profile.DimensionCount(),
			normalization: profile.NormalizationMode(),
		},
		batchSize: batchSize,
		limits:    resolvedLimits,
	}, nil
}

func (m *BatchedEmbeddingModel) Profile() EmbeddingProfile { return m.profile }

func (m *BatchedEmbeddingModel) Embed(ctx context.Context, texts []string) (EmbeddingResult, error) {
	if len(texts) == 0 {
		return EmbeddingResult{Vectors: [][]float64{}}, nil
	}
	callCtx, cancel := context.WithTimeout(ctx, m.limits.timeout)
	defer cancel()

	vectors := make([][]float64, 0, len(texts))
	usage := Usage{}
	for start := 0; start < len(texts); start += m.batchSize {
		end := min(start+m.batchSize, len(texts))
		batch := slices.Clone(texts[start:end])
		request, err := NewEmbeddingRequest(batch, EmbeddingDocument, m.profile.dimension)
		if err != nil {
			return EmbeddingResult{}, err
		}
		result, err := m.transport.Embed(callCtx, request)
		if err != nil {
			return EmbeddingResult{}, mapEmbeddingCallError(ctx, callCtx, err, m.limits.timeout)
		}
		validated, err := m.validateBatch(batch, result)
		if err != nil {
			return EmbeddingResult{}, err
		}
		vectors = append(vectors, validated...)
		usage = addRequestUsage(usage, result.Usage())
	}
	return EmbeddingResult{Vectors: vectors, Usage: usage}, nil
}

func (m *BatchedEmbeddingModel) validateBatch(
	batch []string,
	result ProviderEmbeddingResult,
) ([][]float64, error) {
	if result.InputType() != EmbeddingDocument {
		return nil, NewInvalidOutputError("embed", "provider returned the wrong input type")
	}
	if !slices.Equal(result.Inputs(), batch) {
		return nil, NewInvalidOutputError("embed", "provider changed input order")
	}
	values := result.Embeddings()
	if len(values) != len(batch) {
		return nil, NewInvalidOutputError("embed", "provider returned the wrong vector count")
	}
	vectors := make([][]float64, len(values))
	for index, value := range values {
		canonical, err := canonicalEmbedding(value, m.profile.dimension, m.profile.normalization)
		if err != nil {
			return nil, NewInvalidOutputError("embed", err.Error())
		}
		vectors[index] = canonical
	}
	return vectors, nil
}

func canonicalEmbedding(values []float64, dimension int, normalization string) ([]float64, error) {
	if dimension < 1 {
		return nil, fmt.Errorf("embedding dimension must be positive")
	}
	if len(values) != dimension {
		return nil, fmt.Errorf("embedding must contain exactly %d dimensions", dimension)
	}
	vector := slices.Clone(values)
	for _, value := range vector {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return nil, fmt.Errorf("embedding values must all be finite")
		}
	}
	if normalization != "unit" {
		return vector, nil
	}
	scale := 0.0
	for _, value := range vector {
		scale = max(scale, math.Abs(value))
	}
	if scale == 0 {
		return nil, fmt.Errorf("embedding must have a non-zero norm")
	}
	normSquared := 0.0
	for index := range vector {
		vector[index] /= scale
		normSquared += vector[index] * vector[index]
	}
	norm := math.Sqrt(normSquared)
	for index := range vector {
		vector[index] /= norm
	}
	return vector, nil
}

func mapInferenceCallError(parent, callCtx context.Context, err error, operation string, timeout time.Duration) error {
	if parent.Err() != nil {
		return parent.Err()
	}
	if errors.Is(callCtx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return NewTimeoutError(operation, timeout)
	}
	var stable interface{ inferenceFailure() }
	if errors.As(err, &stable) {
		return err
	}
	return err
}

func mapEmbeddingCallError(parent, callCtx context.Context, err error, timeout time.Duration) error {
	mapped := mapInferenceCallError(parent, callCtx, err, "embed", timeout)
	if mapped != err {
		return mapped
	}
	var stable interface{ inferenceFailure() }
	if errors.As(err, &stable) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return WrapUnavailableError("embed", err)
}

func cloneVectors(values [][]float64) [][]float64 {
	result := make([][]float64, len(values))
	for index, value := range values {
		result[index] = slices.Clone(value)
	}
	return result
}
