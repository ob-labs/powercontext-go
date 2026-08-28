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

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

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
			serialized.WriteString(attribute.Value.Emit())
		}
		if strings.Contains(serialized.String(), "private-scope") {
			t.Fatalf("%s leaked raw Scope: %s", name, serialized.String())
		}
	}
}
