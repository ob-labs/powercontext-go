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

package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/trace"

	requesttrace "github.com/ob-labs/powercontext-go/internal/observability/tracing"
)

func TestJSONLoggerEmitsOnlyOperationalFields(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	logger, err := New(Config{Format: JSON, Level: slog.LevelInfo, Writer: &output})
	if err != nil {
		t.Fatal(err)
	}
	logger = Named(logger, "powercontext.server.access")
	traceID, _ := trace.TraceIDFromHex("00112233445566778899aabbccddeeff")
	spanID, _ := trace.SpanIDFromHex("0123456789abcdef")
	ctx := trace.ContextWithSpanContext(
		requesttrace.WithRequestID(context.Background(), "fedcba9876543210"),
		trace.NewSpanContext(trace.SpanContextConfig{TraceID: traceID, SpanID: spanID}),
	)
	logger.LogAttrs(ctx, slog.LevelInfo, "PowerContext transport request completed",
		slog.String("event", "transport.request.completed"),
		slog.String("operation", "search_memory"),
		slog.String("outcome", "success"),
		slog.String("transport", "http"),
		slog.Float64("duration_ms", 1.25),
		slog.String("scope_id", "secret-scope"),
		slog.String("prompt", "secret prompt"),
		slog.String("credential", "secret-token"),
		slog.String("path", "/Users/private/workspace"),
	)
	var record map[string]any
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{
		"logger": "powercontext.server.access", "event": "transport.request.completed",
		"operation": "search_memory", "outcome": "success", "transport": "http",
		"request_id": "fedcba9876543210", "trace_id": traceID.String(), "span_id": spanID.String(),
	} {
		if got := record[key]; got != want {
			t.Fatalf("%s = %#v, want %#v", key, got, want)
		}
	}
	for _, forbidden := range []string{"scope_id", "prompt", "credential", "path"} {
		if _, found := record[forbidden]; found {
			t.Fatalf("record contains forbidden field %q", forbidden)
		}
	}
	for _, secret := range []string{"secret-scope", "secret prompt", "secret-token", "/Users/private"} {
		if strings.Contains(output.String(), secret) {
			t.Fatalf("record leaks %q: %s", secret, output.String())
		}
	}
}

func TestLoggerRejectsUnknownFormat(t *testing.T) {
	t.Parallel()
	if _, err := New(Config{Format: "yaml"}); err == nil {
		t.Fatal("unknown logging format was accepted")
	}
}

func TestConsoleAndJSONLoggersUseStableComponentDisplayName(t *testing.T) {
	t.Parallel()
	for _, format := range []Format{Console, JSON} {
		t.Run(string(format), func(t *testing.T) {
			var output bytes.Buffer
			logger, err := New(Config{
				Format: format, Level: slog.LevelInfo, Writer: &output,
				Clock: func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) },
			})
			if err != nil {
				t.Fatal(err)
			}
			Named(logger, "powercontext.server.factory").Info("PowerContext Server is ready")
			if !strings.Contains(output.String(), "powercontext.server.factory") ||
				!strings.Contains(output.String(), "PowerContext Server is ready") {
				t.Fatalf("log output = %s", output.String())
			}
			if format == JSON {
				var payload map[string]any
				if err := json.Unmarshal(output.Bytes(), &payload); err != nil {
					t.Fatal(err)
				}
				if payload["logger"] != "powercontext.server.factory" || payload["level"] != "INFO" {
					t.Fatalf("JSON log = %#v", payload)
				}
			}
		})
	}
}
