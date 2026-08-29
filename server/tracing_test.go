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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/ob-labs/powercontext-go/internal/endpoint"
)

func TestHTTPAndApplicationSpansPreserveW3CParentWithoutSensitiveAttributes(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithSpanProcessor(recorder),
	)
	t.Cleanup(func() { _ = provider.Shutdown(t.Context()) })
	handler, err := NewHTTPHandler(endpoint.NewHandler(endpoint.HandlerOptions{}), HTTPOptions{
		TracerProvider: provider,
	})
	if err != nil {
		t.Fatal(err)
	}

	traceID, _ := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	parentID, _ := trace.SpanIDFromHex("00f067aa0ba902b7")
	request := httptest.NewRequest(http.MethodGet, "/v1/capabilities", nil)
	request.Header.Set("traceparent", "00-"+traceID.String()+"-"+parentID.String()+"-01")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}

	spans := recorder.Ended()
	transport := endedSpan(t, spans, "HTTP get_capabilities")
	application := endedSpan(t, spans, "powercontext get_capabilities")
	if transport.SpanContext().TraceID() != traceID || transport.Parent().SpanID() != parentID || !transport.Parent().IsRemote() {
		t.Fatalf("transport context = trace %s parent %#v", transport.SpanContext().TraceID(), transport.Parent())
	}
	if application.Parent().SpanID() != transport.SpanContext().SpanID() {
		t.Fatalf("application parent = %s, transport = %s", application.Parent().SpanID(), transport.SpanContext().SpanID())
	}
	if got := response.Header().Get("X-PowerContext-Request-ID"); got != transport.SpanContext().SpanID().String() {
		t.Fatalf("request ID = %q", got)
	}
	assertBoundedSpanAttributes(t, transport.Attributes())
	assertBoundedSpanAttributes(t, application.Attributes())
}

func endedSpan(t *testing.T, spans []sdktrace.ReadOnlySpan, name string) sdktrace.ReadOnlySpan {
	t.Helper()
	for _, span := range spans {
		if span.Name() == name {
			return span
		}
	}
	t.Fatalf("span %q not found in %d spans", name, len(spans))
	return nil
}

func assertBoundedSpanAttributes(t *testing.T, attributes []attribute.KeyValue) {
	t.Helper()
	for _, value := range attributes {
		key := string(value.Key)
		for _, forbidden := range []string{"scope", "prompt", "content", "vector", "credential", "path"} {
			if strings.Contains(strings.ToLower(key), forbidden) {
				t.Fatalf("span contains forbidden attribute %q", key)
			}
		}
	}
}
