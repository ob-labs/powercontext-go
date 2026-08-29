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

package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ogen-go/ogen/ogenerrors"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	v1 "github.com/ob-labs/powercontext-go/api/v1"
	requesttrace "github.com/ob-labs/powercontext-go/internal/observability/tracing"
	"github.com/ob-labs/powercontext-go/internal/transportpolicy"
)

const DefaultTimeout = 10 * time.Second

// Options configures the public HTTP client without exposing credentials in
// generated-client errors. A caller-supplied HTTP client is shallow-cloned and
// is never mutated.
type Options struct {
	BearerToken string        `json:"-"`
	Timeout     time.Duration `json:"timeout"`
	HTTPClient  *http.Client  `json:"-"`
	// TrustTransportSecurity permits a caller-supplied HTTPClient to use an
	// http:// label only when its transport is secured outside ordinary TCP,
	// such as an in-process handler, Unix socket, or TLS-terminating proxy.
	TrustTransportSecurity bool                 `json:"trust_transport_security"`
	TracerProvider         trace.TracerProvider `json:"-"`
	MeterProvider          metric.MeterProvider `json:"-"`
}

func (o Options) String() string   { return o.redactedString() }
func (o Options) GoString() string { return o.redactedString() }

func (o Options) trustsTransportSecurity() bool {
	return o.HTTPClient != nil && o.TrustTransportSecurity
}

func (o Options) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Bool("token_configured", o.BearerToken != ""),
		slog.Duration("timeout", o.Timeout),
		slog.Bool("http_client_configured", o.HTTPClient != nil),
		slog.Bool("trust_transport_security", o.TrustTransportSecurity),
		slog.Bool("tracer_provider_configured", o.TracerProvider != nil),
		slog.Bool("meter_provider_configured", o.MeterProvider != nil),
	)
}

func (o Options) redactedString() string {
	return fmt.Sprintf(
		"client.Options{token_configured:%t, timeout:%s, http_client_configured:%t, trust_transport_security:%t, tracer_provider_configured:%t, meter_provider_configured:%t}",
		o.BearerToken != "", o.Timeout, o.HTTPClient != nil, o.TrustTransportSecurity,
		o.TracerProvider != nil, o.MeterProvider != nil,
	)
}

// Client exposes all 53 strongly typed operations from the authoritative ogen
// contract. Embedding the generated invoker keeps new OpenAPI operations a
// compile-time concern instead of a handwritten transport drift risk.
type Client struct {
	v1.Invoker
	raw *v1.Client
}

var _ v1.Invoker = (*Client)(nil)

// New validates transport configuration and constructs a no-redirect client.
// Server URLs may contain a path prefix but never credentials, query values, or
// fragments.
func New(serverURL string, options Options) (*Client, error) {
	endpoint, err := normalizeServerURL(serverURL)
	if err != nil {
		return nil, err
	}
	if !options.trustsTransportSecurity() && transportpolicy.IsPlaintextNonLoopback(endpoint) {
		return nil, &ConfigurationError{Field: "server_url"}
	}
	transport, err := clientTransport(options)
	if err != nil {
		return nil, err
	}
	generatedOptions := []v1.ClientOption{v1.WithClient(transport)}
	generatedOptions = append(generatedOptions, v1.WithTracerProvider(requesttrace.ClientTracerProvider(options.TracerProvider)))
	if options.MeterProvider != nil {
		generatedOptions = append(generatedOptions, v1.WithMeterProvider(options.MeterProvider))
	}
	raw, err := v1.NewClient(endpoint.String(), bearerSource{token: options.BearerToken}, generatedOptions...)
	if err != nil {
		return nil, &ConfigurationError{Field: "server_url"}
	}
	return &Client{Invoker: normalizedInvoker{raw: raw}, raw: raw}, nil
}

// Raw returns the generated client for advanced transport access such as a
// per-request server URL override. Raw calls intentionally bypass Client's
// stable error normalization; most callers should use the promoted methods.
func (c *Client) Raw() *v1.Client {
	if c == nil {
		return nil
	}
	return c.raw
}

// ConfigurationError deliberately excludes the rejected value so URLs and
// tokens cannot leak through logs or command-line diagnostics.
type ConfigurationError struct{ Field string }

func (e *ConfigurationError) Error() string {
	if e == nil || e.Field == "" {
		return "PowerContext Client configuration is invalid"
	}
	return "PowerContext Client configuration is invalid: " + e.Field
}

func (e *ConfigurationError) GoString() string {
	if e == nil {
		return "(*client.ConfigurationError)(nil)"
	}
	return fmt.Sprintf("&client.ConfigurationError{Field:%q}", e.Field)
}

type bearerSource struct{ token string }

func (s bearerSource) BearerAuth(context.Context, v1.OperationName) (v1.BearerAuth, error) {
	if s.token == "" {
		return v1.BearerAuth{}, ogenerrors.ErrSkipClientSecurity
	}
	return v1.BearerAuth{Token: s.token}, nil
}

func normalizeServerURL(value string) (*url.URL, error) {
	if strings.TrimSpace(value) == "" {
		return nil, &ConfigurationError{Field: "server_url"}
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, &ConfigurationError{Field: "server_url"}
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, &ConfigurationError{Field: "server_url"}
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, &ConfigurationError{Field: "server_url"}
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawPath = strings.TrimRight(parsed.RawPath, "/")
	return parsed, nil
}

func clientTransport(options Options) (*http.Client, error) {
	if options.Timeout < 0 {
		return nil, &ConfigurationError{Field: "timeout"}
	}
	var result http.Client
	if options.HTTPClient != nil {
		result = *options.HTTPClient
	} else {
		result.Timeout = DefaultTimeout
	}
	if options.Timeout > 0 {
		result.Timeout = options.Timeout
	}
	result.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	if result.Transport == nil {
		result.Transport = http.DefaultTransport
	}
	result.Transport = traceContextRoundTripper{next: result.Transport}
	result.Transport = transportPolicyRoundTripper{
		next: result.Transport, trusted: options.trustsTransportSecurity(),
	}
	return &result, nil
}

type transportPolicyRoundTripper struct {
	next    http.RoundTripper
	trusted bool
}

func (t transportPolicyRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	if !t.trusted && transportpolicy.IsPlaintextNonLoopback(request.URL) {
		return nil, &ConfigurationError{Field: "server_url"}
	}
	return t.next.RoundTrip(request)
}

type traceContextRoundTripper struct{ next http.RoundTripper }

func (t traceContextRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	cloned := request.Clone(request.Context())
	cloned.Header = request.Header.Clone()
	propagation.TraceContext{}.Inject(cloned.Context(), propagation.HeaderCarrier(cloned.Header))
	response, err := t.next.RoundTrip(cloned)
	if response != nil {
		captureResponse(cloned.Context(), response)
	}
	return response, err
}

// TransportError reports that no valid HTTP response was received. Its text is
// deliberately stable; the original error remains available through Unwrap.
type TransportError struct {
	Path  string
	cause error
}

func (e *TransportError) Error() string {
	if e == nil || e.Path == "" {
		return "PowerContext transport request failed"
	}
	return "PowerContext transport request to " + e.Path + " failed"
}

func (e *TransportError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *TransportError) GoString() string {
	if e == nil {
		return "(*client.TransportError)(nil)"
	}
	return fmt.Sprintf("&client.TransportError{Path:%q}", e.Path)
}

func (e *TransportError) LogValue() slog.Value {
	if e == nil {
		return slog.GroupValue()
	}
	return slog.GroupValue(slog.String("kind", "transport"), slog.String("path", e.Path))
}

// InvalidResponseError reports a response returned with the operation's exact
// success status whose body or headers violate the OpenAPI contract.
type InvalidResponseError struct {
	Path      string
	RequestID string
	cause     error
}

// RequestValidationError reports that a caller-supplied wire request violates
// the OpenAPI schema before any network request is sent.
type RequestValidationError struct {
	Path  string
	cause error
}

func (e *RequestValidationError) Error() string {
	if e == nil || e.Path == "" {
		return "PowerContext request violates the API contract"
	}
	return "PowerContext request for " + e.Path + " violates the API contract"
}

func (e *RequestValidationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *RequestValidationError) GoString() string {
	if e == nil {
		return "(*client.RequestValidationError)(nil)"
	}
	return fmt.Sprintf("&client.RequestValidationError{Path:%q}", e.Path)
}

func (e *RequestValidationError) LogValue() slog.Value {
	if e == nil {
		return slog.GroupValue()
	}
	return slog.GroupValue(slog.String("kind", "request_validation"), slog.String("path", e.Path))
}

func (e *InvalidResponseError) Error() string {
	if e == nil || e.Path == "" {
		return "PowerContext response violated the API contract"
	}
	return "PowerContext response from " + e.Path + " violated the API contract"
}

func (e *InvalidResponseError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *InvalidResponseError) GoString() string {
	if e == nil {
		return "(*client.InvalidResponseError)(nil)"
	}
	return fmt.Sprintf("&client.InvalidResponseError{Path:%q, RequestID:%q}", e.Path, e.RequestID)
}

func (e *InvalidResponseError) LogValue() slog.Value {
	if e == nil {
		return slog.GroupValue()
	}
	return slog.GroupValue(
		slog.String("kind", "invalid_response"), slog.String("path", e.Path), slog.String("request_id", e.RequestID),
	)
}

// ServerError is the stable error context decoded from a declared non-success
// response. Use AsServerError on any operation result before treating it as a
// successful response variant.
type ServerError struct {
	StatusCode int
	RequestID  string
	Code       string
	Message    string
	Details    map[string]any
}

func (e *ServerError) Error() string {
	if e == nil || e.StatusCode == 0 {
		return "PowerContext Server returned an error"
	}
	result := "PowerContext Server returned HTTP " + strconv.Itoa(e.StatusCode)
	if e.Code != "" {
		result += " (" + e.Code + ")"
	}
	return result
}

func (e *ServerError) GoString() string {
	if e == nil {
		return "(*client.ServerError)(nil)"
	}
	return fmt.Sprintf(
		"&client.ServerError{StatusCode:%d, RequestID:%q, Code:%q}",
		e.StatusCode, e.RequestID, e.Code,
	)
}

func (e *ServerError) LogValue() slog.Value {
	if e == nil {
		return slog.GroupValue()
	}
	return slog.GroupValue(
		slog.String("kind", "server_response"), slog.Int("status_code", e.StatusCode),
		slog.String("request_id", e.RequestID), slog.String("code", e.Code),
	)
}

// AsServerError recognizes every shared OpenAPI error response wrapper.
func AsServerError(response any) (*ServerError, bool) {
	if value, ok := response.(*ServerError); ok && value != nil {
		return value, true
	}
	var status int
	switch response.(type) {
	case *v1.UnauthorizedHeaders:
		status = http.StatusUnauthorized
	case *v1.InvalidRequestHeaders:
		status = http.StatusUnprocessableEntity
	case *v1.NotFoundHeaders:
		status = http.StatusNotFound
	case *v1.ConflictHeaders:
		status = http.StatusConflict
	case *v1.ReportTooLargeHeaders:
		status = http.StatusRequestEntityTooLarge
	case *v1.UnavailableHeaders:
		status = http.StatusServiceUnavailable
	case *v1.InternalErrorHeaders:
		status = http.StatusInternalServerError
	default:
		return nil, false
	}
	wrapped, ok := response.(interface {
		GetResponse() v1.ErrorResponse
		GetXPowerContextRequestID() v1.OptString
	})
	if !ok {
		return nil, false
	}
	wire := wrapped.GetResponse().Error
	requestID, _ := wrapped.GetXPowerContextRequestID().Get()
	details := decodeDetails(wire.Details)
	return &ServerError{
		StatusCode: status, RequestID: requestID, Code: wire.Code,
		Message: wire.Message, Details: details,
	}, true
}

func decodeDetails(value v1.NilErrorDetailDetails) map[string]any {
	details, ok := value.Get()
	if !ok {
		return nil
	}
	result := make(map[string]any, len(details))
	for key, raw := range details {
		var decoded any
		if err := json.Unmarshal(raw, &decoded); err == nil {
			result[key] = decoded
		}
	}
	return result
}

// IsConfigurationError reports configuration failures without requiring users
// to import generated transport packages.
func IsConfigurationError(err error) bool {
	_, ok := errors.AsType[*ConfigurationError](err)
	return ok
}
