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

package server

import (
	"context"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	pcruntime "github.com/ob-labs/powercontext-go/internal/runtime"
)

func TestRuntimeStageSpansInheritApplicationContextWithoutRawScope(t *testing.T) {
	t.Parallel()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithSpanProcessor(recorder),
	)
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	runtime, err := pcruntime.NewConfigured(pcruntime.RuntimeOptions{
		Tracing: newRuntimeStageTracing(provider),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, parent := provider.Tracer("test").Start(t.Context(), "powercontext remember_memory")
	if err := runtime.ScopedWrite(ctx, "project:private-scope", func(context.Context, string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	parent.End()
	spans := recorder.Ended()
	application := endedSpan(t, spans, "powercontext remember_memory")
	for _, name := range []string{"scope.context", "scope.lock"} {
		stage := endedSpan(t, spans, name)
		if stage.Parent().SpanID() != application.SpanContext().SpanID() {
			t.Fatalf("%s parent = %s, want %s", name, stage.Parent().SpanID(), application.SpanContext().SpanID())
		}
		serialized := strings.Builder{}
		for _, attribute := range stage.Attributes() {
			serialized.WriteString(attribute.Value.String())
		}
		if strings.Contains(serialized.String(), "private-scope") {
			t.Fatalf("%s leaked raw Scope: %s", name, serialized.String())
		}
	}
}

func TestRuntimeBackgroundOperationStartsIndependentRootWithoutRawScope(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{
		pcruntime.ProcessSourceWindowOperation,
		pcruntime.IncubateExperienceOperation,
	} {
		t.Run(operation, func(t *testing.T) {
			recorder := tracetest.NewSpanRecorder()
			provider := sdktrace.NewTracerProvider(
				sdktrace.WithSampler(sdktrace.AlwaysSample()),
				sdktrace.WithSpanProcessor(recorder),
			)
			t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
			runtime, err := pcruntime.NewConfigured(pcruntime.RuntimeOptions{
				Tracing: newRuntimeStageTracing(provider),
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			ctx, parent := provider.Tracer("test").Start(t.Context(), "HTTP capture_content_source")
			err = runtime.BackgroundOperation(ctx, operation, func(ctx context.Context) (string, error) {
				return pcruntime.ScheduledProcessingSuccess,
					runtime.ScopedWrite(ctx, "project:private-scheduled-scope", func(context.Context, string) error { return nil })
			})
			if err != nil {
				t.Fatal(err)
			}
			parent.End()

			spans := recorder.Ended()
			root := endedSpan(t, spans, operation)
			if root.Parent().IsValid() {
				t.Fatalf("background root inherited parent %s", root.Parent().SpanID())
			}
			if got := root.Attributes(); !hasTraceAttributes(got, map[string]string{
				"powercontext.operation.name":    operation,
				"powercontext.operation.unit":    "background",
				"powercontext.operation.outcome": pcruntime.ScheduledProcessingSuccess,
			}) {
				t.Fatalf("background root attributes = %#v", got)
			}
			contextStage := endedSpan(t, spans, "scope.context")
			if contextStage.Parent().SpanID() != root.SpanContext().SpanID() {
				t.Fatalf("scope context parent = %s, want background root %s", contextStage.Parent().SpanID(), root.SpanContext().SpanID())
			}
			for _, span := range []sdktrace.ReadOnlySpan{root, contextStage, endedSpan(t, spans, "scope.lock")} {
				serialized := strings.Builder{}
				for _, attribute := range span.Attributes() {
					serialized.WriteString(attribute.Value.String())
				}
				if strings.Contains(serialized.String(), "private-scheduled-scope") {
					t.Fatalf("%s leaked raw Scope: %s", span.Name(), serialized.String())
				}
			}
		})
	}
}

func TestRuntimeBackgroundRootFailureStartsUnparentedChildSpans(t *testing.T) {
	t.Parallel()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithSpanProcessor(recorder),
	)
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	operation := pcruntime.ProcessSourceWindowOperation
	runtime, err := pcruntime.NewConfigured(pcruntime.RuntimeOptions{
		Tracing: newRuntimeStageTracing(failNamedTracerProvider{TracerProvider: provider, name: operation}),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, ambient := provider.Tracer("test").Start(t.Context(), "HTTP flush_memory")
	err = runtime.BackgroundOperation(ctx, operation, func(ctx context.Context) (string, error) {
		return pcruntime.ScheduledProcessingSuccess,
			runtime.ScopedWrite(ctx, "project:private-scheduled-scope", func(context.Context, string) error { return nil })
	})
	if err != nil {
		t.Fatal(err)
	}
	ambient.End()

	spans := recorder.Ended()
	for _, span := range spans {
		if span.Name() == operation {
			t.Fatalf("recorded background root despite injected start failure: %#v", span)
		}
	}
	child := endedSpan(t, spans, "scope.context")
	if child.Parent().IsValid() {
		t.Fatalf("child span inherited parent %s after background root failure", child.Parent().SpanID())
	}
	if child.SpanContext().TraceID() == ambient.SpanContext().TraceID() {
		t.Fatalf("child span retained ambient trace ID %s after background root failure", child.SpanContext().TraceID())
	}
}

type failNamedTracerProvider struct {
	trace.TracerProvider
	name string
}

func (p failNamedTracerProvider) Tracer(name string, options ...trace.TracerOption) trace.Tracer {
	return failNamedTracer{Tracer: p.TracerProvider.Tracer(name, options...), name: p.name}
}

type failNamedTracer struct {
	trace.Tracer
	name string
}

func (t failNamedTracer) Start(ctx context.Context, name string, options ...trace.SpanStartOption) (context.Context, trace.Span) {
	if name == t.name {
		panic("injected background root tracer failure")
	}
	return t.Tracer.Start(ctx, name, options...)
}

func hasTraceAttributes(attributes []attribute.KeyValue, want map[string]string) bool {
	values := make(map[string]string, len(attributes))
	for _, attribute := range attributes {
		values[string(attribute.Key)] = attribute.Value.AsString()
	}
	for key, expected := range want {
		if values[key] != expected {
			return false
		}
	}
	return true
}
