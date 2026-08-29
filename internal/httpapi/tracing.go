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
	"errors"

	"go.opentelemetry.io/otel/trace"

	v1 "github.com/ob-labs/powercontext-go/api/v1"
	requesttrace "github.com/ob-labs/powercontext-go/internal/observability/tracing"
)

// TraceApplication records the decoded application operation as a child of
// ogen's transport span. Decode/security failures therefore remain transport
// failures and never masquerade as entered application work.
func TraceApplication(provider trace.TracerProvider) Middleware {
	return func(request Request, next Next) (Response, error) {
		if request.OperationID == "get_liveness" || request.OperationID == "get_readiness" {
			return next(request)
		}
		ctx, span := requesttrace.StartOperation(
			request.Context,
			provider,
			"powercontext "+request.OperationID,
			request.OperationID,
			"application",
			trace.SpanKindInternal,
			"",
		)
		request.SetContext(ctx)
		response, err := next(request)
		outcome := "success"
		if err != nil {
			outcome = "failure"
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			outcome = "cancelled"
		}
		if value, ok := response.Type.(*v1.FlushMemoryResponseHeaders); ok && value.Response.Status == v1.FlushStatusIdle {
			outcome = "noop"
		}
		span.Finish(outcome, err)
		return response, err
	}
}
