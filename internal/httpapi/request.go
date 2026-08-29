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
	"crypto/rand"
	"encoding/hex"

	"go.opentelemetry.io/otel/trace"

	requesttrace "github.com/ob-labs/powercontext-go/internal/observability/tracing"
)

const (
	// RequestIDHeader is the only request correlation header owned by the server.
	RequestIDHeader = "X-PowerContext-Request-ID"
	// SelectionDigestHeader and ReportDigestHeader carry canonical report identities.
	SelectionDigestHeader = "X-PowerContext-Selection-Digest"
	ReportDigestHeader    = "X-PowerContext-Report-Digest"
)

// RequestID returns the server-owned identifier for the current ingress
// request. Caller-provided correlation headers are deliberately ignored.
func RequestID(ctx context.Context) (string, bool) {
	return requesttrace.RequestID(ctx)
}

// BindSpanRequestID is an ogen middleware. ogen starts the ingress span before
// invoking middleware, so this is the first point where its final span ID is
// available. The outer response writer reads the same requestState when it
// commits headers.
func BindSpanRequestID(req Request, next Next) (Response, error) {
	bindRequestIDFromSpan(req.Context)
	return next(req)
}

// TracerProvider wraps the provider passed to ogen so request IDs are bound as
// soon as ogen creates its ingress span. Unlike ogen middleware, this also runs
// for requests rejected during security or decoding.
func TracerProvider(provider trace.TracerProvider) trace.TracerProvider {
	return requesttrace.HTTPTracerProvider(provider)
}

func bindRequestIDFromSpan(ctx context.Context) {
	spanID := trace.SpanContextFromContext(ctx).SpanID()
	if spanID.IsValid() {
		requesttrace.SetRequestID(ctx, spanID.String())
	}
}

func randomRequestID() string {
	var raw [8]byte
	for {
		if _, err := rand.Read(raw[:]); err != nil {
			// crypto/rand failures are not recoverable for a server process. A
			// fixed non-zero ID still preserves the response contract while the
			// surrounding health/telemetry surfaces report the process failure.
			return "0000000000000001"
		}
		var nonzero byte
		for _, value := range raw {
			nonzero |= value
		}
		if nonzero != 0 {
			return hex.EncodeToString(raw[:])
		}
	}
}
