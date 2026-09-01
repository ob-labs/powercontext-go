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

package runtime

import (
	"context"
	"errors"
	"fmt"
)

// TraceAttribute values are validated by the tracing adapter before export.
// Runtime call sites use only string, bool, int, int64, and float64 values.
type TraceAttribute = any

// StageSpan is the minimal tracing surface consumed by Runtime operations.
// Implementations must treat attributes as operational metadata only.
type StageSpan interface {
	SetAttributes(map[string]TraceAttribute)
	Finish(outcome string, err error)
}

type StageTracing interface {
	StartStage(context.Context, string, map[string]TraceAttribute) (context.Context, StageSpan)
}

// BackgroundTracing is an optional consumer-owned boundary for work that has
// no request parent. Runtime remains transport-agnostic while a consumer such
// as the server can export an independent operation root.
type BackgroundTracing interface {
	StartBackground(context.Context, string, map[string]TraceAttribute) (context.Context, StageSpan)
}

type StageTracingFunc func(context.Context, string, map[string]TraceAttribute) (context.Context, StageSpan)

func (f StageTracingFunc) StartStage(
	ctx context.Context,
	name string,
	attributes map[string]TraceAttribute,
) (context.Context, StageSpan) {
	return f(ctx, name, attributes)
}

func (r *Runtime) resolveScope(ctx context.Context) error {
	return r.runStage(ctx, "scope.context", nil, func(context.Context, StageSpan) error { return nil })
}

func (r *Runtime) runStage(
	ctx context.Context,
	name string,
	attributes map[string]TraceAttribute,
	operation func(context.Context, StageSpan) error,
) (err error) {
	if operation == nil {
		return errors.New("runtime: stage operation must not be nil")
	}
	stageContext, span := safelyStartStage(ctx, r.tracing, name, attributes)
	defer func() {
		if recovered := recover(); recovered != nil {
			panicErr := fmt.Errorf("runtime: stage operation panicked with %T", recovered)
			safelyFinishStage(span, "failure", panicErr)
			panic(recovered)
		}
	}()
	err = operation(stageContext, span)
	outcome := "success"
	if err != nil {
		outcome = "failure"
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || context.Cause(stageContext) != nil {
		outcome = "cancelled"
	}
	safelyFinishStage(span, outcome, err)
	return err
}

func safelyStartStage(
	ctx context.Context,
	tracing StageTracing,
	name string,
	attributes map[string]TraceAttribute,
) (stageContext context.Context, span StageSpan) {
	stageContext = ctx
	if tracing == nil {
		return stageContext, nil
	}
	defer func() {
		if recover() != nil {
			stageContext, span = ctx, nil
		}
	}()
	started, span := tracing.StartStage(ctx, name, cloneTraceAttributes(attributes))
	if started != nil {
		stageContext = started
	}
	return stageContext, span
}

func safelyStartBackground(
	ctx context.Context,
	tracing StageTracing,
	name string,
	attributes map[string]TraceAttribute,
) (backgroundContext context.Context, span StageSpan) {
	backgroundContext = ctx
	background, ok := tracing.(BackgroundTracing)
	if !ok || background == nil {
		return backgroundContext, nil
	}
	defer func() {
		if recover() != nil {
			backgroundContext, span = ctx, nil
		}
	}()
	started, span := background.StartBackground(ctx, name, cloneTraceAttributes(attributes))
	if started != nil {
		backgroundContext = started
	}
	return backgroundContext, span
}

func setStageAttributes(span StageSpan, attributes map[string]TraceAttribute) {
	if span == nil {
		return
	}
	defer func() { _ = recover() }()
	span.SetAttributes(cloneTraceAttributes(attributes))
}

func safelyFinishStage(span StageSpan, outcome string, err error) {
	if span == nil {
		return
	}
	defer func() { _ = recover() }()
	span.Finish(outcome, err)
}

func cloneTraceAttributes(attributes map[string]TraceAttribute) map[string]TraceAttribute {
	if attributes == nil {
		return nil
	}
	clone := make(map[string]TraceAttribute, len(attributes))
	for key, value := range attributes {
		clone[key] = value
	}
	return clone
}
