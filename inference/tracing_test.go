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
	"strings"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	servertracing "github.com/ob-labs/powercontext-go/internal/observability/tracing"
)

func TestInferenceSpanRecordsNoModelContent(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithSpanProcessor(recorder),
	)
	t.Cleanup(func() { _ = provider.Shutdown(t.Context()) })
	model := TraceTextModel(tracingTextModel{}, provider)
	message, _ := NewMessage(RoleUser, "secret model content")
	if _, err := model.Complete(t.Context(), newTextRequest(
		[]string{"secret prompt"}, []Message{message}, GenerationSettings{},
	)); err != nil {
		t.Fatal(err)
	}
	spans := recorder.Ended()
	if len(spans) != 1 || spans[0].Name() != "powercontext inference.generate" {
		t.Fatalf("spans = %#v", spans)
	}
	for _, value := range spans[0].Attributes() {
		encoded := strings.ToLower(string(value.Key) + value.Value.String())
		for _, forbidden := range []string{"secret", "prompt", "content", "vector", "credential"} {
			if strings.Contains(encoded, forbidden) {
				t.Fatalf("span attribute exposes %q: %s", forbidden, encoded)
			}
		}
	}
}

func TestInferenceTracingFollowsProviderSamplingSetting(t *testing.T) {
	configured, err := servertracing.Configure(t.Context(), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = configured.Close(t.Context()) })
	underlying := new(spanObservingTextModel)
	if _, err := TraceTextModel(underlying, configured.Provider()).Complete(t.Context(), TextRequest{}); err != nil {
		t.Fatal(err)
	}
	if !underlying.valid || underlying.recording {
		t.Fatalf("inference span valid = %t, recording = %t", underlying.valid, underlying.recording)
	}
}

func TestPromptedGeneratorRetrySpansExcludeInputOutputAndFeedback(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithSpanProcessor(recorder),
	)
	t.Cleanup(func() { _ = provider.Shutdown(t.Context()) })
	invalid, _ := NewTextResponse(
		`{"candidates":[{"text":"traveler prefers aisle seats"}]}`,
		Usage{},
	)
	valid, _ := NewTextResponse(
		`{"candidates":[{"text":"redacted","intent":"add"}]}`,
		Usage{},
	)
	underlying := &recordedTextModel{responses: []TextResponse{invalid, valid}}
	generator, err := NewPromptedGenerator[promptedQuestion, promptedProposal](
		TraceTextModel(underlying, provider),
		"Propose secret candidates.",
		proposalCodec(t),
		nil,
		GenerationSettings{},
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := generator.Generate(t.Context(), promptedQuestion{Value: "bounded secret evidence"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output.Candidates[0].Intent != "add" || len(underlying.Requests()) != 2 {
		t.Fatalf("retry result = %#v, requests = %d", result.Output, len(underlying.Requests()))
	}

	spans := recorder.Ended()
	if len(spans) != 2 {
		t.Fatalf("spans = %d, want one per physical provider request", len(spans))
	}
	for _, span := range spans {
		if span.Name() != "powercontext inference.generate" {
			t.Fatalf("span name = %q", span.Name())
		}
		for _, value := range span.Attributes() {
			encoded := strings.ToLower(string(value.Key) + value.Value.String())
			for _, forbidden := range []string{
				"secret", "traveler", "aisle", "redacted", "candidate", "feedback", "schema", "prompt", "content",
			} {
				if strings.Contains(encoded, forbidden) {
					t.Fatalf("span attribute exposes %q: %s", forbidden, encoded)
				}
			}
		}
	}
}

func TestEmbeddingSpanNestsUnderActiveOperationWithoutRecordingTextOrVectors(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithSpanProcessor(recorder),
	)
	t.Cleanup(func() { _ = provider.Shutdown(t.Context()) })
	transport := fixedEmbeddingTransport(
		t,
		[]string{"bounded secret evidence"},
		EmbeddingDocument,
		[][]float64{{1, 2, 3}},
		Usage{},
	)
	model, err := NewBatchedEmbeddingModel(
		TraceEmbeddingTransport(transport, provider),
		testEmbeddingProfile{id: "test-v1", model: "test:model", dimension: 3, normalization: "none"},
		10,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, operation := provider.Tracer("test").Start(t.Context(), "powercontext flush_memory")
	if _, err := model.Embed(ctx, []string{"bounded secret evidence"}); err != nil {
		t.Fatal(err)
	}
	operation.End()

	spans := recorder.Ended()
	if len(spans) != 2 {
		t.Fatalf("spans = %d, want operation and embedding", len(spans))
	}
	var operationSpan, embeddingSpan sdktrace.ReadOnlySpan
	for _, span := range spans {
		switch span.Name() {
		case "powercontext flush_memory":
			operationSpan = span
		case "powercontext inference.embed":
			embeddingSpan = span
		}
	}
	if operationSpan == nil || embeddingSpan == nil {
		t.Fatalf("spans = %#v", spans)
	}
	if embeddingSpan.Parent().SpanID() != operationSpan.SpanContext().SpanID() {
		t.Fatalf("embedding parent = %s, want %s", embeddingSpan.Parent().SpanID(), operationSpan.SpanContext().SpanID())
	}
	for _, value := range embeddingSpan.Attributes() {
		encoded := strings.ToLower(string(value.Key) + value.Value.String())
		for _, forbidden := range []string{"secret", "bounded", "evidence", "vector", "embedding.value", "content"} {
			if strings.Contains(encoded, forbidden) {
				t.Fatalf("embedding span attribute exposes %q: %s", forbidden, encoded)
			}
		}
	}
}

type tracingTextModel struct{}

func (tracingTextModel) Complete(context.Context, TextRequest) (TextResponse, error) {
	return NewTextResponse(`{"ok":true}`, Usage{})
}

type spanObservingTextModel struct {
	valid     bool
	recording bool
}

func (m *spanObservingTextModel) Complete(ctx context.Context, _ TextRequest) (TextResponse, error) {
	span := trace.SpanFromContext(ctx)
	m.valid = span.SpanContext().IsValid()
	m.recording = span.IsRecording()
	return TextResponse{}, nil
}
