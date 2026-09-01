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

	"go.opentelemetry.io/otel/trace"

	requesttrace "github.com/ob-labs/powercontext-go/internal/observability/tracing"
	pcruntime "github.com/ob-labs/powercontext-go/internal/runtime"
)

type runtimeStageTracing struct{ provider trace.TracerProvider }

func newRuntimeStageTracing(provider trace.TracerProvider) pcruntime.StageTracing {
	return runtimeStageTracing{provider: provider}
}

func (t runtimeStageTracing) StartStage(
	ctx context.Context,
	name string,
	attributes map[string]pcruntime.TraceAttribute,
) (context.Context, pcruntime.StageSpan) {
	spanContext, operation := requesttrace.StartOperation(
		ctx, t.provider, name, name, "stage", trace.SpanKindInternal, "",
	)
	operation.SetAttributes(attributes)
	return spanContext, operation
}

func (t runtimeStageTracing) StartBackground(
	ctx context.Context,
	name string,
	attributes map[string]pcruntime.TraceAttribute,
) (context.Context, pcruntime.StageSpan) {
	spanContext, operation := requesttrace.StartBackgroundOperation(ctx, t.provider, name, name)
	operation.SetAttributes(attributes)
	return spanContext, operation
}

var (
	_ pcruntime.StageTracing      = runtimeStageTracing{}
	_ pcruntime.BackgroundTracing = runtimeStageTracing{}
)
