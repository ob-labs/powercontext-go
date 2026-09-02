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

package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"go.opentelemetry.io/otel/propagation"

	serverlogging "github.com/ob-labs/powercontext-go/internal/observability/logging"
	requesttrace "github.com/ob-labs/powercontext-go/internal/observability/tracing"
)

// maxRequestBodyBytes bounds how much of an application/json request body the
// transport buffers for the Unicode pre-check. Contract-valid requests stay
// far below it (string fields cap at 200,000 characters and arrays at 32
// items); the bound exists so unauthenticated callers cannot exhaust server
// memory through unbounded buffering.
const maxRequestBodyBytes = 32 << 20 // 32 MiB

// ValidateJSONUnicode rejects malformed UTF-8 and unpaired JSON surrogate
// escapes before Go's JSON decoder replaces them with U+FFFD. Pydantic rejects
// the same input at the Python transport boundary; preserving that distinction
// also prevents malformed private input from reaching application handlers.
// Bodies larger than maxRequestBodyBytes are rejected with 413 before any
// buffering can grow without bound.
func ValidateJSONUnicode(next http.Handler) http.Handler {
	return validateJSONUnicodeWithLimit(next, maxRequestBodyBytes)
}

func validateJSONUnicodeWithLimit(next http.Handler, limit int64) http.Handler {
	if next == nil {
		return nil
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/json" || r.Body == nil {
			next.ServeHTTP(w, r)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		payload, readErr := io.ReadAll(r.Body)
		_ = r.Body.Close()
		if readErr != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(readErr, &tooLarge) {
				writeRequestBodyTooLarge(w, tooLarge.Limit)
				return
			}
			writeInvalidUnicode(w, "")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(payload))
		if field, valid := validJSONUnicode(payload); !valid {
			writeInvalidUnicode(w, field)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeRequestBodyTooLarge(w http.ResponseWriter, limit int64) {
	writeError(w, http.StatusRequestEntityTooLarge, Error{
		Code:    "request_too_large",
		Message: "The request body exceeds the transport limit.",
		Details: map[string]any{"limit_bytes": limit},
	})
}

func writeInvalidUnicode(w http.ResponseWriter, field string) {
	location := []any{"body"}
	if field != "" {
		location = append(location, field)
	}
	writeError(w, http.StatusUnprocessableEntity, Error{
		Code:    "invalid_request",
		Message: "The request violates the API contract.",
		Details: map[string]any{"errors": []any{map[string]any{
			"type": "string_unicode",
			"loc":  location,
			"msg":  "Input should be a valid string, unable to parse raw data as a unicode string",
		}}},
	})
}

// validJSONUnicode performs only the Unicode portion of JSON lexical
// validation. All other syntax and schema validation remains owned by ogen.
// The returned field is the nearest JSON object key and is used only for the
// bounded validation location; raw values are never retained or rendered.
func validJSONUnicode(payload []byte) (field string, valid bool) {
	if !utf8.Valid(payload) {
		return "", false
	}
	lastKey := ""
	for index := 0; index < len(payload); {
		if payload[index] != '"' {
			index++
			continue
		}
		end, unicodeValid := scanJSONString(payload, index)
		next := end
		for next < len(payload) && (payload[next] == ' ' || payload[next] == '\t' || payload[next] == '\r' || payload[next] == '\n') {
			next++
		}
		isKey := next < len(payload) && payload[next] == ':'
		if isKey && unicodeValid && end <= len(payload) {
			if decoded, err := strconv.Unquote(string(payload[index:end])); err == nil {
				lastKey = decoded
			}
		}
		if !unicodeValid {
			if isKey {
				return "", false
			}
			return lastKey, false
		}
		if end <= index {
			index++
		} else {
			index = end
		}
	}
	return "", true
}

func scanJSONString(payload []byte, start int) (int, bool) {
	for index := start + 1; index < len(payload); index++ {
		switch payload[index] {
		case '"':
			return index + 1, true
		case '\\':
			if index+1 >= len(payload) {
				return len(payload), true
			}
			if payload[index+1] != 'u' {
				index++
				continue
			}
			code, ok := jsonHexCodeUnit(payload, index+2)
			if !ok {
				return len(payload), true
			}
			index += 5
			switch {
			case code >= 0xd800 && code <= 0xdbff:
				pair := index + 1
				if pair+6 > len(payload) || payload[pair] != '\\' || payload[pair+1] != 'u' {
					return index + 1, false
				}
				low, lowOK := jsonHexCodeUnit(payload, pair+2)
				if !lowOK {
					return len(payload), true
				}
				if low < 0xdc00 || low > 0xdfff {
					return pair + 6, false
				}
				index = pair + 5
			case code >= 0xdc00 && code <= 0xdfff:
				return index + 1, false
			}
		}
	}
	return len(payload), true
}

func jsonHexCodeUnit(payload []byte, start int) (uint16, bool) {
	if start+4 > len(payload) {
		return 0, false
	}
	var value uint16
	for _, character := range payload[start : start+4] {
		value <<= 4
		switch {
		case character >= '0' && character <= '9':
			value += uint16(character - '0')
		case character >= 'a' && character <= 'f':
			value += uint16(character-'a') + 10
		case character >= 'A' && character <= 'F':
			value += uint16(character-'A') + 10
		default:
			return 0, false
		}
	}
	return value, true
}

var publicPaths = map[string]struct{}{
	"/":                {},
	"/handoff-reports": {},
	"/health/live":     {},
	"/health/ready":    {},
}

// Options configures transport-only behavior around the generated server.
type Options struct {
	BearerToken         string
	HandoffReportRoutes bool
	Access              *AccessLogOptions
}

type AccessLogOptions struct {
	Logger           *slog.Logger
	ResolveOperation func(*http.Request) string
	Skip             func(*http.Request) bool
}

// Wrap installs server-owned request IDs, optional static authentication and
// the feature gate for Handoff Report operations. It must wrap the complete
// HTTP mux so non-OpenAPI surfaces such as metrics and the Dashboard observe
// the same policy.
func Wrap(next http.Handler, options Options) (http.Handler, error) {
	if next == nil {
		return nil, errors.New("httpapi: handler must not be nil")
	}
	if strings.ContainsAny(options.BearerToken, "\r\n") {
		return nil, errors.New("httpapi: bearer token must not contain line breaks")
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parent := requesttrace.ExtractTraceContext(r.Context(), propagation.HeaderCarrier(r.Header))
		ctx := r.WithContext(requesttrace.WithRequestID(parent, randomRequestID()))
		writer := &requestIDWriter{ResponseWriter: w, ctx: ctx.Context()}
		access := options.Access
		logAccess := access != nil && access.Logger != nil && (access.Skip == nil || !access.Skip(ctx))
		started := time.Now()
		operation := "unmatched"
		if logAccess && access.ResolveOperation != nil {
			if resolved := access.ResolveOperation(ctx); resolved != "" {
				operation = resolved
			}
		}
		if logAccess {
			defer func() {
				if recovered := recover(); recovered != nil {
					logHTTPCompletion(ctx.Context(), access.Logger, writer, operation, time.Since(started), true)
					panic(recovered)
				}
				logHTTPCompletion(ctx.Context(), access.Logger, writer, operation, time.Since(started), false)
			}()
		}

		if !options.HandoffReportRoutes && strings.HasPrefix(r.URL.Path, "/v1/handoff-reports/") {
			http.NotFound(writer, ctx)
			return
		}
		if options.BearerToken != "" && !isPublicPath(r.Method, r.URL.Path) && !validBearer(r.Header.Get("Authorization"), options.BearerToken) {
			writer.Header().Set("WWW-Authenticate", "Bearer")
			writeError(writer, http.StatusUnauthorized, Error{
				Code:    "unauthorized",
				Message: "A valid bearer token is required.",
			})
			return
		}

		next.ServeHTTP(writer, ctx)
	}), nil
}

// isPublicPath reports whether a request may skip bearer authentication. All
// public surfaces (dashboard and report shells, health probes, static assets)
// are registered read-only, so only GET and HEAD qualify. Restricting the
// method keeps the prefix match for /static/ from exempting arbitrary
// state-changing verbs that the mux would otherwise reject after the bypass.
func isPublicPath(method, path string) bool {
	if method != http.MethodGet && method != http.MethodHead {
		return false
	}
	if _, ok := publicPaths[path]; ok {
		return true
	}
	return strings.HasPrefix(path, "/static/")
}

func validBearer(header, want string) bool {
	scheme, credential, ok := strings.Cut(header, " ")
	if !ok || !strings.EqualFold(scheme, "bearer") || credential == "" {
		return false
	}
	wantHash := sha256.Sum256([]byte(want))
	gotHash := sha256.Sum256([]byte(credential))
	return subtle.ConstantTimeCompare(gotHash[:], wantHash[:]) == 1
}

type requestIDWriter struct {
	http.ResponseWriter
	ctx         context.Context
	wroteHeader bool
	statusCode  int
}

func (w *requestIDWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *requestIDWriter) WriteHeader(statusCode int) {
	if w.wroteHeader {
		return
	}
	w.Header().Set(RequestIDHeader, mustRequestID(w.ctx))
	w.Header().Del("X-Request-ID")
	w.wroteHeader = true
	w.statusCode = statusCode
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *requestIDWriter) Write(body []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

func (w *requestIDWriter) ReadFrom(reader io.Reader) (int64, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if readerFrom, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		return readerFrom.ReadFrom(reader)
	}
	return io.Copy(struct{ io.Writer }{w.ResponseWriter}, reader)
}

func (w *requestIDWriter) Flush() {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *requestIDWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("httpapi: response writer does not support hijacking")
	}
	if !w.wroteHeader {
		w.Header().Set(RequestIDHeader, mustRequestID(w.ctx))
	}
	return hijacker.Hijack()
}

func mustRequestID(ctx context.Context) string {
	value, ok := requesttrace.RequestID(ctx)
	if !ok {
		return "0000000000000001"
	}
	return value
}

func (w *requestIDWriter) Push(target string, options *http.PushOptions) error {
	if pusher, ok := w.ResponseWriter.(http.Pusher); ok {
		return pusher.Push(target, options)
	}
	return http.ErrNotSupported
}

func logHTTPCompletion(
	ctx context.Context,
	logger *slog.Logger,
	writer *requestIDWriter,
	operation string,
	duration time.Duration,
	panicked bool,
) {
	statusCode := writer.statusCode
	outcome := "success"
	if panicked {
		statusCode = http.StatusInternalServerError
		outcome = "failure"
	} else if context.Cause(ctx) != nil {
		statusCode = 0
		outcome = "cancelled"
	} else {
		if statusCode == 0 {
			statusCode = http.StatusOK
		}
		if statusCode >= 400 {
			outcome = "failure"
		}
	}
	serverlogging.LogTransportCompletion(ctx, logger, serverlogging.TransportObservation{
		Operation: operation, Outcome: outcome, Transport: "http", Duration: duration, StatusCode: statusCode,
	})
}

// Error is the stable wire-level error detail. Details is encoded as JSON null
// when absent, matching the frozen Python envelope.
type Error struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details"`
}

type errorEnvelope struct {
	Error Error `json:"error"`
}

func writeError(w http.ResponseWriter, statusCode int, detail Error) {
	payload, err := json.Marshal(errorEnvelope{Error: detail})
	if err != nil {
		payload = []byte(`{"error":{"code":"internal_error","message":"The Server failed.","details":null}}`)
		statusCode = http.StatusInternalServerError
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_, _ = w.Write(payload)
}
