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

package tracing

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"unicode"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
	"go.opentelemetry.io/otel/trace"
)

const instrumentationName = "powercontext.server"

// Server owns one process-scoped SDK provider. Disabled tracing still uses a
// parent-based never-sample provider so request IDs and W3C context remain
// valid without recording or exporting spans.
type Server struct {
	provider *sdktrace.TracerProvider
}

func Configure(ctx context.Context, enabled bool) (*Server, error) {
	options := []sdktrace.TracerProviderOption{sdktrace.WithResource(serverResource())}
	if enabled {
		exporter, err := otlptracehttp.New(ctx)
		if err != nil {
			return nil, errors.New("tracing: OTLP HTTP exporter could not be configured")
		}
		options = append(options, sdktrace.WithBatcher(exporter))
	} else {
		options = append(options, sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.NeverSample())))
	}
	return &Server{provider: sdktrace.NewTracerProvider(options...)}, nil
}

func (s *Server) Provider() trace.TracerProvider {
	if s == nil || s.provider == nil {
		return otel.GetTracerProvider()
	}
	return s.provider
}

func (s *Server) Close(ctx context.Context) error {
	if s == nil || s.provider == nil {
		return nil
	}
	return s.provider.Shutdown(ctx)
}

func serverResource() *resource.Resource {
	current := resource.Default()
	service, found := current.Set().Value(semconv.ServiceNameKey)
	if found && !strings.HasPrefix(service.AsString(), "unknown_service") {
		return current
	}
	fallback := resource.NewSchemaless(semconv.ServiceName("powercontext-server"))
	merged, err := resource.Merge(current, fallback)
	if err != nil {
		return fallback
	}
	return merged
}

// ExtractTraceContext accepts only the W3C traceparent/tracestate formats.
// Baggage is intentionally excluded because arbitrary caller values must not
// become telemetry attributes.
func ExtractTraceContext(ctx context.Context, headers propagation.HeaderCarrier) context.Context {
	return propagation.TraceContext{}.Extract(ctx, headers)
}

// Operation is a failure-isolated span with PowerContext's bounded attribute
// vocabulary. It never records operation inputs, outputs, model data, paths,
// credentials, or raw scope identifiers.
type Operation struct {
	span trace.Span
	done atomic.Bool
}

func StartOperation(
	ctx context.Context,
	provider trace.TracerProvider,
	name string,
	operation string,
	unit string,
	kind trace.SpanKind,
	transport string,
) (context.Context, *Operation) {
	if provider == nil {
		provider = otel.GetTracerProvider()
	}
	attributes := []attribute.KeyValue{
		attribute.String("powercontext.operation.name", operation),
		attribute.String("powercontext.operation.unit", unit),
	}
	if transport != "" {
		attributes = append(attributes, attribute.String("powercontext.transport", transport))
	}
	if requestID, ok := RequestID(ctx); ok {
		attributes = append(attributes, attribute.String("powercontext.request.id", requestID))
	}
	spanCtx, span := provider.Tracer(instrumentationName).Start(
		ctx,
		name,
		trace.WithSpanKind(kind),
		trace.WithAttributes(attributes...),
	)
	return spanCtx, &Operation{span: span}
}

// StartBackgroundOperation creates an internal operation root without
// inheriting an ambient request trace. Background workers must not masquerade
// as transport children, and they never receive a request ID attribute.
func StartBackgroundOperation(
	ctx context.Context,
	provider trace.TracerProvider,
	name string,
	operation string,
) (context.Context, *Operation) {
	if provider == nil {
		provider = otel.GetTracerProvider()
	}
	spanCtx, span := provider.Tracer(instrumentationName).Start(
		ctx,
		name,
		trace.WithNewRoot(),
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(
			attribute.String("powercontext.operation.name", operation),
			attribute.String("powercontext.operation.unit", "background"),
		),
	)
	return spanCtx, &Operation{span: span}
}

func (o *Operation) Finish(outcome string, err error) {
	if o == nil || o.span == nil || !o.done.CompareAndSwap(false, true) {
		return
	}
	if outcome == "" {
		outcome = "failure"
	}
	o.span.SetAttributes(attribute.String("powercontext.operation.outcome", outcome))
	if err != nil {
		typeName := reflect.TypeOf(err).String()
		o.span.SetAttributes(attribute.String("error.type", typeName))
	}
	if outcome == "failure" {
		o.span.SetStatus(codes.Error, "")
	}
	o.span.End()
}

// SetAttributes records only scalar PowerContext attributes and silently
// drops everything else. Stage call sites are fixed in the Runtime; this
// guard keeps a future domain value from becoming telemetry by accident.
func (o *Operation) SetAttributes(values map[string]any) {
	if o == nil || o.span == nil || o.done.Load() {
		return
	}
	attributes := make([]attribute.KeyValue, 0, len(values))
	for key, value := range values {
		if !strings.HasPrefix(key, "powercontext.") || key == "powercontext.operation.outcome" {
			continue
		}
		switch typed := value.(type) {
		case string:
			attributes = append(attributes, attribute.String(key, typed))
		case bool:
			attributes = append(attributes, attribute.Bool(key, typed))
		case int:
			attributes = append(attributes, attribute.Int(key, typed))
		case int64:
			attributes = append(attributes, attribute.Int64(key, typed))
		case float64:
			attributes = append(attributes, attribute.Float64(key, typed))
		}
	}
	if len(attributes) > 0 {
		o.span.SetAttributes(attributes...)
	}
}

// MCPMiddleware records logical MCP protocol requests, not Streamable HTTP
// frames. The HTTP boundary extracts W3C context before the SDK sees a request.
func MCPMiddleware(provider trace.TracerProvider) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
			operation := "mcp." + strings.ReplaceAll(strings.TrimPrefix(method, "/"), "/", ".")
			spanCtx, span := StartOperation(
				ctx, provider, "MCP "+operation, operation, "transport", trace.SpanKindServer, "mcp",
			)
			bindRequestIDFromSpan(spanCtx)
			result, err := next(spanCtx, method, request)
			outcome := "success"
			if err != nil {
				outcome = "failure"
			} else if call, ok := result.(*mcp.CallToolResult); ok && call.IsError {
				outcome = "failure"
			}
			if errors.Is(spanCtx.Err(), context.Canceled) {
				outcome = "cancelled"
			}
			span.Finish(outcome, err)
			return result, err
		}
	}
}

// HTTPTracerProvider renames ogen server spans to the frozen public naming
// convention and adds only bounded PowerContext attributes.
func HTTPTracerProvider(provider trace.TracerProvider) trace.TracerProvider {
	if provider == nil {
		provider = otel.GetTracerProvider()
	}
	return namingTracerProvider{TracerProvider: provider, prefix: "HTTP ", unit: "transport", transport: "http", bindRequestID: true}
}

// ClientTracerProvider keeps the generated client's complete operation set
// while preserving PowerContext's stable span names and dependency boundary.
func ClientTracerProvider(provider trace.TracerProvider) trace.TracerProvider {
	if provider == nil {
		provider = otel.GetTracerProvider()
	}
	return namingTracerProvider{TracerProvider: provider, prefix: "PowerContextClient ", unit: "dependency"}
}

type namingTracerProvider struct {
	trace.TracerProvider
	prefix        string
	unit          string
	transport     string
	bindRequestID bool
}

func (p namingTracerProvider) Tracer(name string, options ...trace.TracerOption) trace.Tracer {
	return namingTracer{Tracer: p.TracerProvider.Tracer(name, options...), config: p}
}

type namingTracer struct {
	trace.Tracer
	config namingTracerProvider
}

func (t namingTracer) Start(ctx context.Context, spanName string, options ...trace.SpanStartOption) (context.Context, trace.Span) {
	operation := camelToSnake(spanName)
	attributes := []attribute.KeyValue{
		attribute.String("powercontext.operation.name", operation),
		attribute.String("powercontext.operation.unit", t.config.unit),
	}
	if t.config.transport != "" {
		attributes = append(attributes, attribute.String("powercontext.transport", t.config.transport))
	}
	options = append(options, trace.WithAttributes(attributes...))
	spanCtx, span := t.Tracer.Start(ctx, t.config.prefix+operation, options...)
	wrapped := &outcomeSpan{Span: span}
	spanCtx = trace.ContextWithSpan(spanCtx, wrapped)
	if t.config.bindRequestID {
		bindRequestIDFromSpan(spanCtx)
		if requestID, ok := RequestID(spanCtx); ok {
			wrapped.SetAttributes(attribute.String("powercontext.request.id", requestID))
		}
	}
	return spanCtx, wrapped
}

type outcomeSpan struct {
	trace.Span
	failed atomic.Bool
	done   atomic.Bool
}

func (s *outcomeSpan) SetStatus(code codes.Code, description string) {
	if code == codes.Error {
		s.failed.Store(true)
	}
	s.Span.SetStatus(code, description)
}

func (s *outcomeSpan) End(options ...trace.SpanEndOption) {
	if !s.done.CompareAndSwap(false, true) {
		return
	}
	outcome := "success"
	if s.failed.Load() {
		outcome = "failure"
	}
	s.Span.SetAttributes(attribute.String("powercontext.operation.outcome", outcome))
	s.Span.End(options...)
}

func camelToSnake(value string) string {
	if value == "" {
		return "unknown"
	}
	runes := []rune(value)
	var result strings.Builder
	result.Grow(len(value) + 8)
	for index, current := range runes {
		if unicode.IsUpper(current) && index > 0 {
			previous := runes[index-1]
			nextLower := index+1 < len(runes) && unicode.IsLower(runes[index+1])
			if unicode.IsLower(previous) || unicode.IsDigit(previous) || (unicode.IsUpper(previous) && nextLower) {
				result.WriteByte('_')
			}
		}
		result.WriteRune(unicode.ToLower(current))
	}
	return result.String()
}

func bindRequestIDFromSpan(ctx context.Context) {
	spanContext := trace.SpanContextFromContext(ctx)
	spanID := spanContext.SpanID()
	if spanID.IsValid() {
		SetRequestID(ctx, spanID.String())
		setRequestSpanContext(ctx, spanContext)
	}
}
