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

package metrics

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ogen-go/ogen/middleware"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	v1 "github.com/ob-labs/powercontext-go/api/v1"
)

// Server owns an isolated Prometheus registry for one PowerContext process.
// Its labels are deliberately limited to bounded operation and outcome values;
// callers cannot attach scope IDs, prompts, content, vectors, paths, or model
// credentials.
type Server struct {
	registry              *prometheus.Registry
	transportRequests     *prometheus.CounterVec
	transportDuration     *prometheus.HistogramVec
	transportInProgress   *prometheus.GaugeVec
	applicationOperations *prometheus.CounterVec
	applicationDuration   *prometheus.HistogramVec
	runtimeReady          prometheus.Gauge
	runtimeScopes         *prometheus.GaugeVec
}

func New() (*Server, error) {
	server := &Server{
		registry: prometheus.NewRegistry(),
		transportRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "powercontext_server_transport_requests_total",
			Help: "External transport requests completed by the Server.",
		}, []string{"transport", "operation", "outcome"}),
		transportDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "powercontext_server_transport_request_duration_seconds",
			Help: "External transport request duration.",
		}, []string{"transport", "operation", "outcome"}),
		transportInProgress: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "powercontext_server_transport_requests_in_progress",
			Help: "External transport requests currently in progress.",
		}, []string{"transport", "operation"}),
		applicationOperations: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "powercontext_server_application_operations_total",
			Help: "PowerContext application operations completed by the Server.",
		}, []string{"operation", "outcome"}),
		applicationDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "powercontext_server_application_operation_duration_seconds",
			Help: "PowerContext application operation duration.",
		}, []string{"operation", "outcome"}),
		runtimeReady: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "powercontext_server_runtime_ready",
			Help: "Whether the built-in Runtime can accept operations.",
		}),
		runtimeScopes: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "powercontext_server_runtime_scopes",
			Help: "Scope compositions currently active or retained by the built-in Runtime.",
		}, []string{"state"}),
	}
	if err := server.registry.Register(server.transportRequests); err != nil {
		return nil, err
	}
	if err := server.registry.Register(server.transportDuration); err != nil {
		return nil, err
	}
	if err := server.registry.Register(server.transportInProgress); err != nil {
		return nil, err
	}
	if err := server.registry.Register(server.applicationOperations); err != nil {
		return nil, err
	}
	if err := server.registry.Register(server.applicationDuration); err != nil {
		return nil, err
	}
	if err := server.registry.Register(server.runtimeReady); err != nil {
		return nil, err
	}
	if err := server.registry.Register(server.runtimeScopes); err != nil {
		return nil, err
	}
	server.SetRuntimeScopes(0, 0)
	return server, nil
}

// Handler renders only this Server's registry and does not expose Go process
// or runtime collectors unless they are explicitly registered later.
func (s *Server) Handler() http.Handler {
	return promhttp.HandlerFor(s.registry, promhttp.HandlerOpts{ErrorHandling: promhttp.ContinueOnError})
}

func (s *Server) SetReady(ready bool) {
	if s == nil {
		return
	}
	safely(func() {
		if ready {
			s.runtimeReady.Set(1)
		} else {
			s.runtimeReady.Set(0)
		}
	})
}

func (s *Server) SetRuntimeScopes(cached, active int) {
	if s == nil {
		return
	}
	safely(func() { s.runtimeScopes.WithLabelValues("active").Set(float64(active)) })
	safely(func() { s.runtimeScopes.WithLabelValues("cached").Set(float64(cached)) })
}

func (s *Server) startTransport(transport, operation string) time.Time {
	started := time.Now()
	if s == nil {
		return started
	}
	safely(func() { s.transportInProgress.WithLabelValues(transport, operation).Inc() })
	return started
}

func (s *Server) finishTransport(transport, operation, outcome string, started time.Time) {
	if s == nil {
		return
	}
	duration := max(time.Since(started).Seconds(), 0)
	safely(func() { s.transportRequests.WithLabelValues(transport, operation, outcome).Inc() })
	safely(func() { s.transportDuration.WithLabelValues(transport, operation, outcome).Observe(duration) })
	safely(func() { s.transportInProgress.WithLabelValues(transport, operation).Dec() })
}

// ObserveApplication records one direct endpoint invocation. This method is
// also used by MCP, whose tools bypass the HTTP transport by design.
func (s *Server) ObserveApplication(operation, outcome string, started time.Time) {
	if s == nil {
		return
	}
	duration := max(time.Since(started).Seconds(), 0)
	safely(func() { s.applicationOperations.WithLabelValues(operation, outcome).Inc() })
	safely(func() { s.applicationDuration.WithLabelValues(operation, outcome).Observe(duration) })
}

// HTTPMiddleware measures one decoded OpenAPI operation at both the transport
// and application boundaries. Health operations are infrastructure and remain
// absent from product metrics.
func (s *Server) HTTPMiddleware(request middleware.Request, next middleware.Next) (middleware.Response, error) {
	if request.OperationID == "get_liveness" || request.OperationID == "get_readiness" {
		return next(request)
	}
	startedTransport := s.startTransport("http", request.OperationID)
	startedApplication := time.Now()
	response, err := next(request)
	outcome := operationOutcome(response.Type, err)
	s.ObserveApplication(request.OperationID, outcome, startedApplication)
	transportOutcome := outcome
	if transportOutcome == "noop" {
		transportOutcome = "success"
	}
	s.finishTransport("http", request.OperationID, transportOutcome, startedTransport)
	return response, err
}

// MCPMiddleware measures logical protocol requests, not their Streamable HTTP
// frames. It is safe to install before any session is accepted.
func (s *Server) MCPMiddleware() mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
			operation := "mcp." + methodName(method)
			started := s.startTransport("mcp", operation)
			result, err := next(ctx, method, request)
			outcome := "success"
			if err != nil {
				outcome = "failure"
			} else if call, ok := result.(*mcp.CallToolResult); ok && call.IsError {
				outcome = "failure"
			}
			if errors.Is(ctx.Err(), context.Canceled) {
				outcome = "cancelled"
			}
			s.finishTransport("mcp", operation, outcome, started)
			return result, err
		}
	}
}

func operationOutcome(response any, err error) string {
	if err != nil {
		return "failure"
	}
	if value, ok := response.(*v1.FlushMemoryResponseHeaders); ok && value.Response.Status == v1.FlushStatusIdle {
		return "noop"
	}
	return "success"
}

func methodName(method string) string {
	result := make([]byte, len(method))
	for index := range method {
		if method[index] == '/' {
			result[index] = '.'
		} else {
			result[index] = method[index]
		}
	}
	return string(result)
}

func safely(operation func()) {
	defer func() { _ = recover() }()
	operation()
}
