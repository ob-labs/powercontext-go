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
	"math"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

type testEmbeddingProfile struct {
	id            string
	model         string
	dimension     int
	normalization string
}

func (p testEmbeddingProfile) ID() string                { return p.id }
func (p testEmbeddingProfile) ModelName() string         { return p.model }
func (p testEmbeddingProfile) DimensionCount() int       { return p.dimension }
func (p testEmbeddingProfile) NormalizationMode() string { return p.normalization }

type recordedEmbeddingTransport struct {
	mu       sync.Mutex
	calls    [][]string
	requests []EmbeddingRequest
	complete func(context.Context, []string, EmbeddingInputType) (ProviderEmbeddingResult, error)
}

func TestEmbeddingRequestClonesInputsAndReportsDimension(t *testing.T) {
	inputs := []string{"alpha", "beta"}
	request, err := NewEmbeddingRequest(inputs, EmbeddingQuery, 384)
	if err != nil {
		t.Fatal(err)
	}
	inputs[0] = "changed"
	got := request.Inputs()
	got[1] = "mutated"
	if !slices.Equal(request.Inputs(), []string{"alpha", "beta"}) ||
		request.InputType() != EmbeddingQuery || request.DimensionCount() != 384 {
		t.Fatalf("request = %#v", request)
	}
}

func TestEmbeddingRequestRejectsInvalidConstruction(t *testing.T) {
	for _, test := range []struct {
		name      string
		inputType EmbeddingInputType
		dimension int
		reason    string
	}{
		{name: "zero dimension", inputType: EmbeddingQuery, dimension: 0, reason: "dimension-positive"},
		{name: "unknown input type", inputType: EmbeddingInputType("unknown"), dimension: 384, reason: "request-rejected"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewEmbeddingRequest([]string{"secret input"}, test.inputType, test.dimension)
			var configuration *ConfigurationError
			if !errors.As(err, &configuration) || configuration.Code() != test.reason || strings.Contains(err.Error(), "secret") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestBatchedEmbeddingModelSendsProfileDimensionOnEveryBatch(t *testing.T) {
	transport := &recordedEmbeddingTransport{}
	model, err := NewBatchedEmbeddingModel(
		transport,
		testEmbeddingProfile{id: "test-v1", model: "test:model", dimension: 3, normalization: "none"},
		2,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := model.Embed(t.Context(), []string{"a", "b", "c"}); err != nil {
		t.Fatal(err)
	}
	if len(transport.requests) != 2 ||
		transport.requests[0].DimensionCount() != 3 ||
		transport.requests[1].DimensionCount() != 3 {
		t.Fatalf("requests = %#v", transport.requests)
	}
}

func (t *recordedEmbeddingTransport) Embed(
	ctx context.Context,
	request EmbeddingRequest,
) (ProviderEmbeddingResult, error) {
	inputs := request.Inputs()
	inputType := request.InputType()
	t.mu.Lock()
	t.calls = append(t.calls, slices.Clone(inputs))
	t.requests = append(t.requests, request)
	t.mu.Unlock()
	if t.complete == nil {
		vectors := make([][]float64, len(inputs))
		for index := range vectors {
			vectors[index] = []float64{0, 0, 0}
		}
		return NewProviderEmbeddingResult(inputs, inputType, vectors, Usage{})
	}
	return t.complete(ctx, inputs, inputType)
}

func (t *recordedEmbeddingTransport) Calls() [][]string {
	t.mu.Lock()
	defer t.mu.Unlock()
	result := make([][]string, len(t.calls))
	for index, call := range t.calls {
		result[index] = slices.Clone(call)
	}
	return result
}

func TestBatchedEmbeddingModelPreservesOrderAndUsage(t *testing.T) {
	transport := &recordedEmbeddingTransport{complete: func(
		_ context.Context,
		inputs []string,
		inputType EmbeddingInputType,
	) (ProviderEmbeddingResult, error) {
		vectors := make([][]float64, len(inputs))
		for index := range inputs {
			vectors[index] = []float64{float64(index + 1), 2, 3}
		}
		return NewProviderEmbeddingResult(
			inputs, inputType, vectors,
			Usage{InputTokens: int64Pointer(int64(len(inputs)))},
		)
	}}
	model, err := NewBatchedEmbeddingModel(
		transport,
		testEmbeddingProfile{id: "test-v1", model: "test:model", dimension: 3, normalization: "none"},
		2,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	result, err := model.Embed(context.Background(), []string{"alpha", "beta", "gamma", "delta", "epsilon"})
	if err != nil {
		t.Fatal(err)
	}
	wantCalls := [][]string{{"alpha", "beta"}, {"gamma", "delta"}, {"epsilon"}}
	if !slices.EqualFunc(transport.Calls(), wantCalls, slices.Equal[[]string]) {
		t.Fatalf("calls = %#v", transport.Calls())
	}
	if result.Usage.Requests != 3 || result.Usage.InputTokens == nil || *result.Usage.InputTokens != 5 || result.Usage.OutputTokens != nil {
		t.Fatalf("usage = %+v", result.Usage)
	}
	if len(result.Vectors) != 5 || !slices.Equal(result.Vectors[4], []float64{1, 2, 3}) {
		t.Fatalf("vectors = %#v", result.Vectors)
	}
}

func TestBatchedEmbeddingModelUsesOverflowStableUnitNormalization(t *testing.T) {
	transport := fixedEmbeddingTransport(t, []string{"alpha"}, EmbeddingDocument, [][]float64{{3e300, 4e300}}, Usage{})
	model, err := NewBatchedEmbeddingModel(
		transport,
		testEmbeddingProfile{id: "unit-v1", model: "test:model", dimension: 2, normalization: "unit"},
		10,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := model.Embed(context.Background(), []string{"alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(result.Vectors[0][0]-0.6) > 1e-15 || math.Abs(result.Vectors[0][1]-0.8) > 1e-15 {
		t.Fatalf("normalized = %#v", result.Vectors[0])
	}
}

func TestBatchedEmbeddingModelValidatesProviderEnvelope(t *testing.T) {
	tests := []struct {
		name       string
		inputs     []string
		inputType  EmbeddingInputType
		vectors    [][]float64
		wantDetail string
	}{
		{name: "input type", inputs: []string{"alpha"}, inputType: EmbeddingQuery, vectors: [][]float64{{1, 2, 3}}, wantDetail: "provider returned the wrong input type"},
		{name: "order", inputs: []string{"changed"}, inputType: EmbeddingDocument, vectors: [][]float64{{1, 2, 3}}, wantDetail: "provider changed input order"},
		{name: "count", inputs: []string{"alpha"}, inputType: EmbeddingDocument, vectors: nil, wantDetail: "provider returned the wrong vector count"},
		{name: "dimension", inputs: []string{"alpha"}, inputType: EmbeddingDocument, vectors: [][]float64{{1, 2}}, wantDetail: "embedding must contain exactly 3 dimensions"},
		{name: "finite", inputs: []string{"alpha"}, inputType: EmbeddingDocument, vectors: [][]float64{{1, math.Inf(1), 3}}, wantDetail: "embedding values must all be finite"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := fixedEmbeddingTransport(t, test.inputs, test.inputType, test.vectors, Usage{})
			model, err := NewBatchedEmbeddingModel(
				transport,
				testEmbeddingProfile{id: "test-v1", model: "test:model", dimension: 3, normalization: "none"},
				10,
				nil,
			)
			if err != nil {
				t.Fatal(err)
			}
			_, err = model.Embed(context.Background(), []string{"alpha"})
			var invalid *InvalidOutputError
			if !errors.As(err, &invalid) || invalid.Detail() != test.wantDetail {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestBatchedEmbeddingModelEmptyInputDoesNotCallProvider(t *testing.T) {
	transport := &recordedEmbeddingTransport{complete: func(context.Context, []string, EmbeddingInputType) (ProviderEmbeddingResult, error) {
		t.Fatal("provider called")
		return ProviderEmbeddingResult{}, nil
	}}
	model, err := NewBatchedEmbeddingModel(
		transport,
		testEmbeddingProfile{id: "test-v1", model: "test:model", dimension: 3, normalization: "none"},
		10,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := model.Embed(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Vectors == nil || len(result.Vectors) != 0 || result.Usage.Requests != 0 {
		t.Fatalf("result = %#v", result)
	}
}

func TestBatchedEmbeddingModelUsesOneTotalTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		transport := &recordedEmbeddingTransport{complete: func(ctx context.Context, _ []string, _ EmbeddingInputType) (ProviderEmbeddingResult, error) {
			<-ctx.Done()
			return ProviderEmbeddingResult{}, ctx.Err()
		}}
		limits, _ := NewLimits(3*time.Second, 2)
		model, err := NewBatchedEmbeddingModel(
			transport,
			testEmbeddingProfile{id: "test-v1", model: "test:model", dimension: 3, normalization: "none"},
			10,
			&limits,
		)
		if err != nil {
			t.Fatal(err)
		}
		started := time.Now()
		_, err = model.Embed(context.Background(), []string{"alpha"})
		var timeout *TimeoutError
		if !errors.As(err, &timeout) || time.Since(started) != 3*time.Second {
			t.Fatalf("error = %v, elapsed = %s", err, time.Since(started))
		}
	})
}

func TestBatchedEmbeddingModelMapsTransportFailureToUnavailableWithCause(t *testing.T) {
	provider := errors.New("secret provider response")
	transport := &recordedEmbeddingTransport{complete: func(
		context.Context,
		[]string,
		EmbeddingInputType,
	) (ProviderEmbeddingResult, error) {
		return ProviderEmbeddingResult{}, provider
	}}
	model, err := NewBatchedEmbeddingModel(
		transport,
		testEmbeddingProfile{id: "test-v1", model: "test:model", dimension: 3, normalization: "none"},
		10,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = model.Embed(t.Context(), []string{"bounded text"})
	var unavailable *UnavailableError
	if !errors.As(err, &unavailable) || !errors.Is(err, provider) {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), provider.Error()) {
		t.Fatalf("error leaked provider detail: %q", err)
	}
}

func fixedEmbeddingTransport(
	t *testing.T,
	inputs []string,
	inputType EmbeddingInputType,
	vectors [][]float64,
	usage Usage,
) *recordedEmbeddingTransport {
	t.Helper()
	return &recordedEmbeddingTransport{complete: func(
		context.Context,
		[]string,
		EmbeddingInputType,
	) (ProviderEmbeddingResult, error) {
		result, err := NewProviderEmbeddingResult(inputs, inputType, vectors, usage)
		if err != nil {
			t.Fatal(err)
		}
		return result, nil
	}}
}
