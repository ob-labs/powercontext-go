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
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"go.opentelemetry.io/otel/trace"

	v1 "github.com/ob-labs/powercontext-go/api/v1"
)

func TestValidJSONUnicodeDistinguishesSurrogateEscapes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		payload   []byte
		wantField string
		wantValid bool
	}{
		{name: "ordinary", payload: []byte(`{"query":"replacement �"}`), wantValid: true},
		{name: "valid pair", payload: []byte(`{"query":"\ud83d\ude00"}`), wantValid: true},
		{name: "low surrogate", payload: []byte(`{"query":"\udcaa"}`), wantField: "query"},
		{name: "high without pair", payload: []byte(`{"query":"\ud800x"}`), wantField: "query"},
		{name: "high with non-low", payload: []byte(`{"query":"\ud800\u0041"}`), wantField: "query"},
		{name: "invalid raw utf8", payload: []byte{'{', '"', 'x', '"', ':', '"', 0xed, 0xb2, 0xaa, '"', '}'}},
		{name: "other invalid json stays downstream", payload: []byte(`{"query":`), wantValid: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			field, valid := validJSONUnicode(test.payload)
			if field != test.wantField || valid != test.wantValid {
				t.Fatalf("validJSONUnicode = (%q, %t), want (%q, %t)", field, valid, test.wantField, test.wantValid)
			}
		})
	}
}

func FuzzValidJSONUnicodeNeverPanics(f *testing.F) {
	for _, seed := range [][]byte{
		[]byte(`{"query":"plain"}`),
		[]byte(`{"query":"\ud83d\ude00"}`),
		[]byte(`{"query":"\udcaa"}`),
		{0xff, '"', '\\', 'u'},
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, payload []byte) {
		_, valid := validJSONUnicode(payload)
		if !utf8.Valid(payload) && valid {
			t.Fatal("invalid UTF-8 was accepted")
		}
	})
}

var requestIDPattern = regexp.MustCompile(`^[0-9a-f]{16}$`)

func TestWrapOwnsRequestID(t *testing.T) {
	t.Parallel()

	var observed string
	handler, err := Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observed, _ = RequestID(r.Context())
		w.Header().Set("X-Request-ID", "application-value")
		w.WriteHeader(http.StatusNoContent)
	}), Options{})
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "/v1/capabilities", nil)
	request.Header.Set(RequestIDHeader, "caller-request-id")
	request.Header.Set("X-Request-ID", "legacy-request-id")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	got := response.Header().Get(RequestIDHeader)
	if !requestIDPattern.MatchString(got) || got == "caller-request-id" {
		t.Fatalf("unexpected request ID %q", got)
	}
	if got != observed {
		t.Fatalf("response request ID %q differs from context %q", got, observed)
	}
	if got := response.Header().Get("X-Request-ID"); got != "" {
		t.Fatalf("legacy request ID leaked into response: %q", got)
	}
}

func TestWrapBearerPolicy(t *testing.T) {
	t.Parallel()

	called := 0
	handler, err := Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called++
		w.WriteHeader(http.StatusNoContent)
	}), Options{BearerToken: "server-secret", HandoffReportRoutes: true})
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name          string
		path          string
		authorization string
		wantStatus    int
	}{
		{name: "missing", path: "/v1/capabilities", wantStatus: http.StatusUnauthorized},
		{name: "wrong", path: "/v1/capabilities", authorization: "Bearer wrong", wantStatus: http.StatusUnauthorized},
		{name: "wrong scheme", path: "/v1/capabilities", authorization: "Basic server-secret", wantStatus: http.StatusUnauthorized},
		{name: "spaces in credential", path: "/v1/capabilities", authorization: "Bearer server-secret extra", wantStatus: http.StatusUnauthorized},
		{name: "accepted", path: "/v1/capabilities", authorization: "bEaReR server-secret", wantStatus: http.StatusNoContent},
		{name: "live is public", path: "/health/live", wantStatus: http.StatusNoContent},
		{name: "ready is public", path: "/health/ready", wantStatus: http.StatusNoContent},
		{name: "dashboard shell is public", path: "/", wantStatus: http.StatusNoContent},
		{name: "report shell is public", path: "/handoff-reports", wantStatus: http.StatusNoContent},
		{name: "static is public", path: "/static/dashboard.js", wantStatus: http.StatusNoContent},
		{name: "metrics is protected", path: "/metrics", wantStatus: http.StatusUnauthorized},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			if test.authorization != "" {
				request.Header.Set("Authorization", test.authorization)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d: %s", response.Code, test.wantStatus, response.Body.String())
			}
			if !requestIDPattern.MatchString(response.Header().Get(RequestIDHeader)) {
				t.Fatalf("missing server request ID: %q", response.Header().Get(RequestIDHeader))
			}
			if test.wantStatus == http.StatusUnauthorized {
				assertUnauthorized(t, response)
			}
		})
	}

	if called != 6 {
		t.Fatalf("application called %d times, want 6", called)
	}
}

func TestValidateJSONUnicodeRejectsOversizedBody(t *testing.T) {
	t.Parallel()

	called := 0
	handler := validateJSONUnicodeWithLimit(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called++
		w.WriteHeader(http.StatusNoContent)
	}), 64)

	oversized := httptest.NewRequest(
		http.MethodPost,
		"/v1/memory/search",
		strings.NewReader(`{"query":"`+strings.Repeat("a", 128)+`"}`),
	)
	oversized.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, oversized)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d: %s", response.Code, http.StatusRequestEntityTooLarge, response.Body.String())
	}
	var envelope errorEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != "request_too_large" || envelope.Error.Details["limit_bytes"] != float64(64) {
		t.Fatalf("unexpected envelope: %#v", envelope)
	}
	if called != 0 {
		t.Fatalf("application called %d times, want 0", called)
	}

	bounded := httptest.NewRequest(http.MethodPost, "/v1/memory/search", strings.NewReader(`{"query":"ok"}`))
	bounded.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, bounded)
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d: %s", response.Code, http.StatusNoContent, response.Body.String())
	}
	if called != 1 {
		t.Fatalf("application called %d times, want 1", called)
	}
}

func TestWrapPublicPathsRequireReadOnlyMethods(t *testing.T) {
	t.Parallel()

	called := 0
	handler, err := Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called++
		w.WriteHeader(http.StatusNoContent)
	}), Options{BearerToken: "server-secret", HandoffReportRoutes: true})
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name       string
		method     string
		path       string
		wantStatus int
	}{
		{name: "static GET stays public", method: http.MethodGet, path: "/static/dashboard.js", wantStatus: http.StatusNoContent},
		{name: "static HEAD stays public", method: http.MethodHead, path: "/static/dashboard.js", wantStatus: http.StatusNoContent},
		{name: "static POST requires bearer", method: http.MethodPost, path: "/static/dashboard.js", wantStatus: http.StatusUnauthorized},
		{name: "static PUT requires bearer", method: http.MethodPut, path: "/static/dashboard.js", wantStatus: http.StatusUnauthorized},
		{name: "dashboard shell POST requires bearer", method: http.MethodPost, path: "/", wantStatus: http.StatusUnauthorized},
		{name: "health POST requires bearer", method: http.MethodPost, path: "/health/live", wantStatus: http.StatusUnauthorized},
		{name: "report shell DELETE requires bearer", method: http.MethodDelete, path: "/handoff-reports", wantStatus: http.StatusUnauthorized},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d: %s", response.Code, test.wantStatus, response.Body.String())
			}
			if test.wantStatus == http.StatusUnauthorized {
				assertUnauthorized(t, response)
			}
		})
	}

	if called != 2 {
		t.Fatalf("application called %d times, want 2", called)
	}
}

func TestWrapRejectsDisabledHandoffReportRoutesBeforeApplication(t *testing.T) {
	t.Parallel()

	called := false
	handler, err := Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}), Options{})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/handoff-reports/projects/list", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
	if called {
		t.Fatal("disabled route reached application")
	}
}

func TestOgenIngressSpanOwnsRequestIDIncludingDecodeFailures(t *testing.T) {
	t.Parallel()

	spanID := trace.SpanID{0x10, 0x32, 0x54, 0x76, 0x98, 0xba, 0xdc, 0xfe}
	handler := &healthHandler{}
	security, err := NewSecurity("")
	if err != nil {
		t.Fatal(err)
	}
	generated, err := v1.NewServer(
		handler,
		security,
		v1.WithTracerProvider(TracerProvider(fixedTracerProvider{spanID: spanID})),
		v1.WithMiddleware(BindSpanRequestID),
		v1.WithErrorHandler(ErrorHandler(nil)),
	)
	if err != nil {
		t.Fatal(err)
	}
	server, err := Wrap(generated, Options{HandoffReportRoutes: true})
	if err != nil {
		t.Fatal(err)
	}

	live := httptest.NewRecorder()
	server.ServeHTTP(live, httptest.NewRequest(http.MethodGet, "/health/live", nil))
	if live.Code != http.StatusOK || live.Header().Get(RequestIDHeader) != spanID.String() {
		t.Fatalf("live response = (%d, %q): %s", live.Code, live.Header().Get(RequestIDHeader), live.Body.String())
	}
	if handler.requestID != spanID.String() {
		t.Fatalf("handler request ID = %q, want %q", handler.requestID, spanID.String())
	}

	invalid := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/memory/search", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(invalid, request)
	if invalid.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid status = %d: %s", invalid.Code, invalid.Body.String())
	}
	if invalid.Header().Get(RequestIDHeader) != spanID.String() {
		t.Fatalf("invalid request ID = %q, want %q", invalid.Header().Get(RequestIDHeader), spanID.String())
	}
	var envelope errorEnvelope
	if err := json.Unmarshal(invalid.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != "invalid_request" {
		t.Fatalf("error = %#v", envelope.Error)
	}
	wantDetails := map[string]any{
		"errors": []any{
			map[string]any{
				"type": "missing",
				"loc":  []any{"body", "scope_id"},
				"msg":  "Field required",
			},
			map[string]any{
				"type": "missing",
				"loc":  []any{"body", "query"},
				"msg":  "Field required",
			},
		},
	}
	if !reflect.DeepEqual(envelope.Error.Details, wantDetails) {
		t.Fatalf("validation details = %#v, want %#v", envelope.Error.Details, wantDetails)
	}
}

func TestOgenValidationDetailsMatchFrozenPythonContract(t *testing.T) {
	t.Parallel()
	handler := &healthHandler{}
	security, err := NewSecurity("")
	if err != nil {
		t.Fatal(err)
	}
	server, err := v1.NewServer(handler, security, v1.WithErrorHandler(ErrorHandler(nil)))
	if err != nil {
		t.Fatal(err)
	}
	sourceRefs := "[" + strings.TrimSuffix(strings.Repeat(
		`{"name":"content","source_id":"source"},`,
		33,
	), ",") + "]"

	tests := []struct {
		name, method, target, body string
		contentType                bool
		issue                      map[string]any
	}{
		{
			name: "required body", method: http.MethodPost, target: "/v1/context/prepare",
			issue: map[string]any{"type": "missing", "loc": []any{"body"}, "msg": "Field required"},
		},
		{
			name: "string type", method: http.MethodPost, target: "/v1/context/prepare", contentType: true,
			body:  `{"scope_id":7,"query":"query"}`,
			issue: map[string]any{"type": "string_type", "loc": []any{"body", "scope_id"}, "msg": "Input should be a valid string"},
		},
		{
			name: "string minimum", method: http.MethodPost, target: "/v1/context/prepare", contentType: true,
			body: `{"scope_id":"","query":"query"}`,
			issue: map[string]any{
				"type": "string_too_short", "loc": []any{"body", "scope_id"},
				"msg": "String should have at least 1 character", "ctx": map[string]any{"min_length": float64(1)},
			},
		},
		{
			name: "string pattern", method: http.MethodPost, target: "/v1/context/prepare", contentType: true,
			body: `{"scope_id":"scope","query":" "}`,
			issue: map[string]any{
				"type": "string_pattern_mismatch", "loc": []any{"body", "query"},
				"msg": "String should match pattern '.*\\S.*'", "ctx": map[string]any{"pattern": `.*\S.*`},
			},
		},
		{
			name: "extra field", method: http.MethodPost, target: "/v1/context/prepare", contentType: true,
			body:  `{"scope_id":"scope","query":"query","extra":1}`,
			issue: map[string]any{"type": "extra_forbidden", "loc": []any{"body", "extra"}, "msg": "Extra inputs are not permitted"},
		},
		{
			name: "integer minimum", method: http.MethodPost, target: "/v1/context/prepare", contentType: true,
			body: `{"scope_id":"scope","query":"query","max_bytes":1}`,
			issue: map[string]any{
				"type": "greater_than_equal", "loc": []any{"body", "max_bytes"},
				"msg": "Input should be greater than or equal to 512", "ctx": map[string]any{"ge": float64(512)},
			},
		},
		{
			name: "integer type", method: http.MethodPost, target: "/v1/context/prepare", contentType: true,
			body:  `{"scope_id":"scope","query":"query","max_bytes":1.5}`,
			issue: map[string]any{"type": "int_type", "loc": []any{"body", "max_bytes"}, "msg": "Input should be a valid integer"},
		},
		{
			name: "string maximum", method: http.MethodPost, target: "/v1/context/prepare", contentType: true,
			body: `{"scope_id":"` + strings.Repeat("s", 257) + `","query":"query"}`,
			issue: map[string]any{
				"type": "string_too_long", "loc": []any{"body", "scope_id"},
				"msg": "String should have at most 256 characters", "ctx": map[string]any{"max_length": float64(256)},
			},
		},
		{
			name: "integer maximum", method: http.MethodPost, target: "/v1/context/prepare", contentType: true,
			body: `{"scope_id":"scope","query":"query","max_bytes":32769}`,
			issue: map[string]any{
				"type": "less_than_equal", "loc": []any{"body", "max_bytes"},
				"msg": "Input should be less than or equal to 32768", "ctx": map[string]any{"le": float64(32768)},
			},
		},
		{
			name: "non-nullable null", method: http.MethodPost, target: "/v1/context/prepare", contentType: true,
			body:  `{"scope_id":null,"query":"query"}`,
			issue: map[string]any{"type": "string_type", "loc": []any{"body", "scope_id"}, "msg": "Input should be a valid string"},
		},
		{
			name: "nested model type", method: http.MethodPost, target: "/v1/experience/propose", contentType: true,
			body: `{"scope_id":"scope","proposal":"invalid","source_refs":[],"artifact_refs":[]}`,
			issue: map[string]any{
				"type": "model_attributes_type", "loc": []any{"body", "proposal"},
				"msg": "Input should be a valid dictionary or object to extract fields from",
			},
		},
		{
			name: "array type", method: http.MethodPost, target: "/v1/experience/propose", contentType: true,
			body:  `{"scope_id":"scope","proposal":{"situation":"s","action":"a","outcome":"o","lesson":"l"},"source_refs":"invalid","artifact_refs":[]}`,
			issue: map[string]any{"type": "list_type", "loc": []any{"body", "source_refs"}, "msg": "Input should be a valid list"},
		},
		{
			name: "array maximum", method: http.MethodPost, target: "/v1/experience/propose", contentType: true,
			body: `{"scope_id":"scope","proposal":{"situation":"s","action":"a","outcome":"o","lesson":"l"},"source_refs":` + sourceRefs + `,"artifact_refs":[]}`,
			issue: map[string]any{
				"type": "too_long", "loc": []any{"body", "source_refs"},
				"msg": "List should have at most 32 items after validation, not 33",
				"ctx": map[string]any{"field_type": "List", "max_length": float64(32), "actual_length": float64(33)},
			},
		},
		{
			name: "body enum", method: http.MethodPost, target: "/v1/memory/search", contentType: true,
			body: `{"scope_id":"scope","query":"query","mode":"invalid"}`,
			issue: map[string]any{
				"type": "enum", "loc": []any{"body", "mode"},
				"msg": "Input should be 'auto', 'fts', 'vector' or 'hybrid'",
				"ctx": map[string]any{"expected": "'auto', 'fts', 'vector' or 'hybrid'"},
			},
		},
		{
			name: "boolean type", method: http.MethodPost, target: "/v1/handoff-reports/get", contentType: true,
			body:  `{"scope_id":"scope","include_evidence_checks":"invalid"}`,
			issue: map[string]any{"type": "bool_type", "loc": []any{"body", "include_evidence_checks"}, "msg": "Input should be a valid boolean"},
		},
		{
			name: "required query", method: http.MethodGet, target: "/v1/stats",
			issue: map[string]any{"type": "missing", "loc": []any{"query", "scope_id"}, "msg": "Field required"},
		},
		{
			name: "query enum", method: http.MethodGet, target: "/v1/stats?scope_id=scope&period=all",
			issue: map[string]any{
				"type": "enum", "loc": []any{"query", "period"},
				"msg": "Input should be 'today', '7d' or '30d'", "ctx": map[string]any{"expected": "'today', '7d' or '30d'"},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.target, strings.NewReader(test.body))
			if test.contentType {
				request.Header.Set("Content-Type", "application/json")
			}
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			if response.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d: %s", response.Code, response.Body.String())
			}
			var envelope errorEnvelope
			if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			want := map[string]any{"errors": []any{test.issue}}
			if !reflect.DeepEqual(envelope.Error.Details, want) {
				t.Fatalf("details = %#v, want %#v", envelope.Error.Details, want)
			}
		})
	}
}

func TestSemanticRequestContractFailureMatchesFrozenPythonEnvelope(t *testing.T) {
	t.Parallel()
	status, detail, ok := mapTransportError(&requestContractError{
		cause: &v1.CombinedEvidenceLimitError{SourceReferences: 20, ArtifactReferences: 13},
	})
	if !ok || status != http.StatusInternalServerError {
		t.Fatalf("mapping = (%d, %#v, %t)", status, detail, ok)
	}
	if detail.Code != "internal_error" || detail.Message != "The Server failed." || detail.Details != nil {
		t.Fatalf("detail = %#v", detail)
	}
}

func TestWrapRejectsUnsafeConfiguration(t *testing.T) {
	t.Parallel()
	if _, err := Wrap(nil, Options{}); err == nil {
		t.Fatal("expected nil handler error")
	}
	if _, err := Wrap(http.NotFoundHandler(), Options{BearerToken: "bad\ntoken"}); err == nil {
		t.Fatal("expected unsafe token error")
	}
}

func assertUnauthorized(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	if response.Header().Get("WWW-Authenticate") != "Bearer" {
		t.Fatalf("challenge = %q", response.Header().Get("WWW-Authenticate"))
	}
	var envelope errorEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != "unauthorized" || envelope.Error.Message != "A valid bearer token is required." || envelope.Error.Details != nil {
		t.Fatalf("unexpected envelope: %#v", envelope)
	}
}

type healthHandler struct {
	v1.UnimplementedHandler
	requestID string
}

func (h *healthHandler) GetLiveness(ctx context.Context) (*v1.HealthResponseHeaders, error) {
	h.requestID, _ = RequestID(ctx)
	return &v1.HealthResponseHeaders{Response: v1.HealthResponse{Status: "ok"}}, nil
}

type fixedTracerProvider struct {
	trace.TracerProvider
	spanID trace.SpanID
}

func (p fixedTracerProvider) Tracer(string, ...trace.TracerOption) trace.Tracer {
	return fixedTracer{spanID: p.spanID}
}

type fixedTracer struct {
	trace.Tracer
	spanID trace.SpanID
}

func (t fixedTracer) Start(ctx context.Context, _ string, _ ...trace.SpanStartOption) (context.Context, trace.Span) {
	spanContext := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{1},
		SpanID:  t.spanID,
	})
	ctx = trace.ContextWithSpanContext(ctx, spanContext)
	return ctx, trace.SpanFromContext(ctx)
}
