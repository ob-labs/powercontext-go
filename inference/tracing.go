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
	"reflect"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const inferenceInstrumentationName = "powercontext.inference"

// TraceTextModel instruments one physical provider request. The span contract
// deliberately contains no prompt, message, output, model request parameters,
// credentials, or provider response bodies.
func TraceTextModel(model TextModel, provider trace.TracerProvider) TextModel {
	if model == nil {
		return nil
	}
	if provider == nil {
		provider = otel.GetTracerProvider()
	}
	return tracedTextModel{model: model, tracer: provider.Tracer(inferenceInstrumentationName)}
}

type tracedTextModel struct {
	model  TextModel
	tracer trace.Tracer
}

func (m tracedTextModel) Complete(ctx context.Context, request TextRequest) (TextResponse, error) {
	ctx, span := startInferenceSpan(ctx, m.tracer, "generate")
	response, err := m.model.Complete(ctx, request)
	finishInferenceSpan(ctx, span, err)
	return response, err
}

// TraceEmbeddingTransport instruments each provider batch while leaving the
// batching, validation, normalization, and total timeout under
// BatchedEmbeddingModel's ownership.
func TraceEmbeddingTransport(transport EmbeddingTransport, provider trace.TracerProvider) EmbeddingTransport {
	if transport == nil {
		return nil
	}
	if provider == nil {
		provider = otel.GetTracerProvider()
	}
	return tracedEmbeddingTransport{transport: transport, tracer: provider.Tracer(inferenceInstrumentationName)}
}

type tracedEmbeddingTransport struct {
	transport EmbeddingTransport
	tracer    trace.Tracer
}

func (t tracedEmbeddingTransport) Embed(
	ctx context.Context,
	request EmbeddingRequest,
) (ProviderEmbeddingResult, error) {
	ctx, span := startInferenceSpan(ctx, t.tracer, "embed")
	result, err := t.transport.Embed(ctx, request)
	finishInferenceSpan(ctx, span, err)
	return result, err
}

func startInferenceSpan(ctx context.Context, tracer trace.Tracer, operation string) (context.Context, trace.Span) {
	return tracer.Start(
		ctx,
		"powercontext inference."+operation,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("powercontext.operation.name", operation),
			attribute.String("powercontext.operation.unit", "inference"),
		),
	)
}

func finishInferenceSpan(ctx context.Context, span trace.Span, err error) {
	outcome := "success"
	if err != nil {
		outcome = "failure"
		span.SetAttributes(attribute.String("error.type", reflect.TypeOf(err).String()))
		span.SetStatus(codes.Error, "")
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		outcome = "cancelled"
	}
	span.SetAttributes(attribute.String("powercontext.operation.outcome", outcome))
	span.End()
}

var (
	_ TextModel          = tracedTextModel{}
	_ EmbeddingTransport = tracedEmbeddingTransport{}
)
