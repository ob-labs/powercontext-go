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
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestDisabledTracingKeepsValidNonRecordingContext(t *testing.T) {
	server, err := Configure(t.Context(), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close(t.Context()) })
	_, span := server.Provider().Tracer("test").Start(t.Context(), "test")
	defer span.End()
	if !span.SpanContext().IsValid() || span.IsRecording() {
		t.Fatalf("span context = %#v, recording = %t", span.SpanContext(), span.IsRecording())
	}
}

func TestEnabledTracingHasCompiledOTLPHTTPExporter(t *testing.T) {
	// Python needs an optional runtime extra before enabled tracing can be
	// configured. The standard Go artifact instead compiles the exporter in;
	// constructing it must succeed without loading any optional component.
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:4318")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	server, err := Configure(t.Context(), true)
	if err != nil {
		t.Fatal(err)
	}
	if server.provider == nil {
		t.Fatal("enabled tracing did not construct a provider")
	}
	if err := server.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestOperationNameConversionIsDeterministic(t *testing.T) {
	for input, want := range map[string]string{
		"GetCapabilities":             "get_capabilities",
		"GetHandoffReportWorkspace":   "get_handoff_report_workspace",
		"ListHandoffReportActivities": "list_handoff_report_activities",
	} {
		if got := camelToSnake(input); got != want {
			t.Fatalf("camelToSnake(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestGeneratedSpanRecordsRedactedErrorsAndPreservesEventOptions(t *testing.T) {
	for _, wrapper := range []struct {
		name string
		wrap func(trace.TracerProvider) trace.TracerProvider
	}{
		{"HTTP", HTTPTracerProvider},
		{"Client", ClientTracerProvider},
	} {
		t.Run(wrapper.name, func(t *testing.T) {
			recorder := tracetest.NewSpanRecorder()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()), sdktrace.WithSpanProcessor(recorder))
			t.Cleanup(func() { _ = provider.Shutdown(t.Context()) })
			_, span := wrapper.wrap(provider).Tracer("test").Start(t.Context(), "CreateSource")
			timestamp := time.Unix(1750000000, 123)
			span.RecordError(nil)
			for _, failure := range []error{
				errors.New("private-source private-scope private-payload"),
				fmt.Errorf("private-sql-diagnostic: %w", errors.New("private-credential")),
			} {
				span.RecordError(failure, trace.WithTimestamp(timestamp), trace.WithAttributes(attribute.String("test.option", "preserved")))
			}
			span.End()
			spans := recorder.Ended()
			if len(spans) != 1 {
				t.Fatalf("ended spans = %d", len(spans))
			}
			if spans[0].Status().Code != codes.Unset {
				t.Fatalf("RecordError changed span status: %#v", spans[0].Status())
			}
			events := spans[0].Events()
			if len(events) != 2 {
				t.Fatalf("error events = %d, want two non-nil failures", len(events))
			}
			var errorClass string
			for _, event := range events {
				if event.Name != "exception" || !event.Time.Equal(timestamp) {
					t.Fatalf("error event lost identity or timestamp: %#v", event)
				}
				attributes := attribute.NewSet(event.Attributes...)
				if option, ok := attributes.Value("test.option"); !ok || option.AsString() != "preserved" {
					t.Fatalf("RecordError dropped EventOption: %#v", event.Attributes)
				}
				if message, ok := attributes.Value("exception.message"); !ok || message.AsString() != "PowerContext operation failed." {
					t.Fatalf("unredacted error message: %#v", event.Attributes)
				}
				class, ok := attributes.Value("exception.type")
				if !ok || class.AsString() == "" {
					t.Fatal("failure lost its error class")
				}
				if errorClass != "" && class.AsString() != errorClass {
					t.Fatalf("raw error type escaped: %q != %q", class.AsString(), errorClass)
				}
				errorClass = class.AsString()
			}
			if strings.Contains(fmt.Sprintf("%v %v %v", spans[0].Attributes(), events, spans[0].Status()), "private-") {
				t.Fatal("protected error input escaped into span data")
			}
		})
	}
}
