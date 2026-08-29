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
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/trace"

	requesttrace "github.com/ob-labs/powercontext-go/internal/observability/tracing"
)

type Format string

const (
	Console Format = "console"
	JSON    Format = "json"
)

var operationalFields = map[string]struct{}{
	"candidate_count": {}, "duration_ms": {}, "error_code": {}, "event": {}, "logger": {},
	"operation": {}, "outcome": {}, "purpose": {}, "request_id": {}, "source_count": {},
	"span_id": {}, "status_code": {}, "trace_id": {}, "transport": {}, "unit": {},
}

type Config struct {
	Level  slog.Level
	Format Format
	Writer io.Writer
	Clock  func() time.Time
}

// New returns a standard slog logger backed by a restrictive operational
// handler. Unknown attributes are dropped rather than serialized, preventing a
// future call site from accidentally logging content, prompts, vectors,
// credentials, raw scopes, database URLs, or full local paths.
func New(config Config) (*slog.Logger, error) {
	if config.Format == "" {
		config.Format = Console
	}
	if config.Format != Console && config.Format != JSON {
		return nil, errors.New("logging: format must be console or json")
	}
	if config.Writer == nil {
		config.Writer = os.Stdout
	}
	if config.Clock == nil {
		config.Clock = time.Now
	}
	handler := &operationalHandler{shared: &handlerState{
		writer: config.Writer, format: config.Format, level: config.Level, clock: config.Clock,
	}}
	return slog.New(handler), nil
}

// Named keeps Python's stable logger field without introducing a custom logger
// interface.
func Named(logger *slog.Logger, name string) *slog.Logger {
	if logger == nil {
		return slog.Default().With("logger", name)
	}
	return logger.With("logger", name)
}

type handlerState struct {
	mu     sync.Mutex
	writer io.Writer
	format Format
	level  slog.Level
	clock  func() time.Time
}

type operationalHandler struct {
	shared *handlerState
	attrs  []slog.Attr
	group  string
}

func (h *operationalHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.shared.level
}

func (h *operationalHandler) Handle(ctx context.Context, record slog.Record) error {
	fields := make(map[string]any, len(h.attrs)+record.NumAttrs()+3)
	if h.group == "" {
		for _, attribute := range h.attrs {
			addAttribute(fields, attribute)
		}
		record.Attrs(func(attribute slog.Attr) bool {
			addAttribute(fields, attribute)
			return true
		})
	}
	if _, exists := fields["request_id"]; !exists {
		if requestID, ok := requesttrace.RequestID(ctx); ok {
			fields["request_id"] = requestID
		}
	}
	span := trace.SpanContextFromContext(ctx)
	if !span.IsValid() {
		if requestSpan, ok := requesttrace.RequestSpanContext(ctx); ok {
			span = requestSpan
		}
	}
	if span.IsValid() {
		if _, exists := fields["trace_id"]; !exists {
			fields["trace_id"] = span.TraceID().String()
		}
		if _, exists := fields["span_id"]; !exists {
			fields["span_id"] = span.SpanID().String()
		}
	}
	timestamp := record.Time
	if timestamp.IsZero() {
		timestamp = h.shared.clock()
	}

	h.shared.mu.Lock()
	defer h.shared.mu.Unlock()
	if h.shared.format == JSON {
		return writeJSON(h.shared.writer, timestamp, record.Level, record.Message, fields)
	}
	return writeConsole(h.shared.writer, timestamp, record.Level, record.Message, fields)
}

func (h *operationalHandler) WithAttrs(attributes []slog.Attr) slog.Handler {
	clone := *h
	clone.attrs = append(append([]slog.Attr(nil), h.attrs...), attributes...)
	return &clone
}

func (h *operationalHandler) WithGroup(name string) slog.Handler {
	clone := *h
	if name != "" {
		clone.group = name
	}
	return &clone
}

func addAttribute(fields map[string]any, attribute slog.Attr) {
	attribute.Value = attribute.Value.Resolve()
	if attribute.Equal(slog.Attr{}) {
		return
	}
	if _, allowed := operationalFields[attribute.Key]; !allowed {
		return
	}
	value, ok := safeValue(attribute.Value)
	if ok {
		fields[attribute.Key] = value
	}
}

func safeValue(value slog.Value) (any, bool) {
	switch value.Kind() {
	case slog.KindString:
		return value.String(), true
	case slog.KindBool:
		return value.Bool(), true
	case slog.KindInt64:
		return value.Int64(), true
	case slog.KindUint64:
		return value.Uint64(), true
	case slog.KindFloat64:
		return value.Float64(), true
	case slog.KindDuration:
		return value.Duration().Seconds(), true
	case slog.KindTime:
		return value.Time().UTC().Format(time.RFC3339Nano), true
	default:
		return nil, false
	}
}

func writeJSON(writer io.Writer, timestamp time.Time, level slog.Level, message string, fields map[string]any) error {
	payload := make(map[string]any, len(fields)+4)
	payload["timestamp"] = timestamp.UTC().Format(time.RFC3339Nano)
	payload["level"] = strings.ToUpper(level.String())
	payload["message"] = message
	if logger, ok := fields["logger"]; ok {
		payload["logger"] = logger
		delete(fields, "logger")
	}
	for key, value := range fields {
		payload[key] = value
	}
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(payload); err != nil {
		return err
	}
	_, err := writer.Write(encoded.Bytes())
	return err
}

func writeConsole(writer io.Writer, timestamp time.Time, level slog.Level, message string, fields map[string]any) error {
	var line strings.Builder
	line.WriteString(timestamp.UTC().Format(time.RFC3339Nano))
	line.WriteByte(' ')
	line.WriteString(strings.ToUpper(level.String()))
	if logger, ok := fields["logger"]; ok {
		line.WriteByte(' ')
		line.WriteString(fmt.Sprint(logger))
		delete(fields, "logger")
	}
	line.WriteByte(' ')
	line.WriteString(message)
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		encoded, err := json.Marshal(fields[key])
		if err != nil {
			continue
		}
		line.WriteByte(' ')
		line.WriteString(key)
		line.WriteByte('=')
		line.Write(encoded)
	}
	line.WriteByte('\n')
	_, err := io.WriteString(writer, line.String())
	return err
}
