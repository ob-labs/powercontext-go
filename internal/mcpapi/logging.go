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

package mcpapi

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	serverlogging "github.com/ob-labs/powercontext-go/internal/observability/logging"
)

// AccessLogMiddleware observes logical MCP protocol requests. It must be
// installed inside the tracing middleware so the record shares the MCP
// transport span and request ID; Streamable HTTP frames are logged nowhere.
func AccessLogMiddleware(logger *slog.Logger) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
			started := time.Now()
			result, err := next(ctx, method, request)
			outcome := "success"
			if err != nil {
				outcome = "failure"
			} else if call, ok := result.(*mcp.CallToolResult); ok && call.IsError {
				outcome = "failure"
			}
			if errors.Is(context.Cause(ctx), context.Canceled) || errors.Is(context.Cause(ctx), context.DeadlineExceeded) {
				outcome = "cancelled"
			}
			serverlogging.LogTransportCompletion(ctx, logger, serverlogging.TransportObservation{
				Operation: "mcp." + mcpMethodName(method), Outcome: outcome, Transport: "mcp", Duration: time.Since(started),
			})
			return result, err
		}
	}
}

func mcpMethodName(method string) string {
	value := strings.ReplaceAll(strings.TrimPrefix(method, "/"), "/", ".")
	if value == "" {
		return "unknown"
	}
	return value
}
