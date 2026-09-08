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
	"context"
	"errors"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	canonicalartifact "github.com/ob-labs/powercontext-go/api/canonical/artifacts"
	managedskills "github.com/ob-labs/powercontext-go/api/canonical/managedskills"
	canonicalscopec "github.com/ob-labs/powercontext-go/api/canonical/scopes"
	canonicalsource "github.com/ob-labs/powercontext-go/api/canonical/sources"
	v1 "github.com/ob-labs/powercontext-go/api/v1"
	"github.com/ob-labs/powercontext-go/internal/endpoint"
	"github.com/ob-labs/powercontext-go/internal/httpapi"
	"github.com/ob-labs/powercontext-go/internal/mcpapi"
	serverlogging "github.com/ob-labs/powercontext-go/internal/observability/logging"
	servermetrics "github.com/ob-labs/powercontext-go/internal/observability/metrics"
	requesttrace "github.com/ob-labs/powercontext-go/internal/observability/tracing"
	"github.com/ob-labs/powercontext-go/internal/webui"
)

type HTTPOptions struct {
	BearerToken         string
	HandoffReportRoutes bool
	TracerProvider      trace.TracerProvider
	MeterProvider       metric.MeterProvider
	Logger              *slog.Logger
	AccessLog           bool
	MCP                 MCPOptions
	metrics             *servermetrics.Server
	webUI               *webui.Options
	scopeBindings       mcpapi.ScopeBindingOperations
	canonicalScopes     canonicalscopec.Handler
	canonicalSources    canonicalsource.Handler
	canonicalArtifacts  canonicalartifact.Handler
	canonicalSkills     managedskills.Handler
}

// MCPOptions controls the optional MCP Streamable HTTP route. Path defaults to
// /mcp. A disabled MCP surface is not mounted and therefore returns 404.
type MCPOptions struct {
	Enabled        bool
	Path           string
	Version        string
	Stateless      bool
	JSONResponse   bool
	SessionTimeout time.Duration
	Logger         *slog.Logger
}

// NewHTTPHandler assembles the generated OpenAPI server and the transport-wide
// policy middleware. Lifecycle and listener ownership remain with cmd/server.
func NewHTTPHandler(handler v1.Handler, options HTTPOptions) (http.Handler, error) {
	if handler == nil {
		return nil, errors.New("server: endpoint handler must not be nil")
	}
	security, err := httpapi.NewSecurity(options.BearerToken)
	if err != nil {
		return nil, err
	}
	var applicationLogger, accessLogger *slog.Logger
	if options.Logger != nil {
		applicationLogger = serverlogging.Named(options.Logger, "powercontext.server.app")
		accessLogger = serverlogging.Named(options.Logger, "powercontext.server.access")
	}
	middlewares := []v1.Middleware{httpapi.BindSpanRequestID, httpapi.ValidatePowerContextContract}
	middlewares = append(middlewares, httpapi.TraceApplication(options.TracerProvider))
	if applicationLogger != nil {
		middlewares = append(middlewares, httpapi.LogApplicationFailures(applicationLogger, func(err error) httpapi.ApplicationError {
			mapped := endpoint.MapError(err)
			return httpapi.ApplicationError{StatusCode: mapped.StatusCode, Code: mapped.Code}
		}))
	}
	if options.metrics != nil {
		middlewares = append(middlewares, options.metrics.HTTPMiddleware)
	}
	mapApplicationError := func(err error) (int, httpapi.Error, bool) {
		mapped := endpoint.MapError(err)
		return mapped.StatusCode, httpapi.Error{
			Code: mapped.Code, Message: mapped.Message, Details: mapped.Details,
		}, true
	}
	serverOptions := []v1.ServerOption{
		v1.WithTracerProvider(httpapi.TracerProvider(options.TracerProvider)),
		v1.WithMiddleware(middlewares...),
		v1.WithErrorHandler(httpapi.ErrorHandler(mapApplicationError)),
	}
	if options.MeterProvider != nil {
		serverOptions = append(serverOptions, v1.WithMeterProvider(options.MeterProvider))
	}
	generated, err := v1.NewServer(handler, security, serverOptions...)
	if err != nil {
		return nil, err
	}
	var canonicalGenerated *canonicalscopec.Server
	if options.canonicalScopes != nil {
		canonicalServerOptions := []canonicalscopec.ServerOption{
			canonicalscopec.WithTracerProvider(httpapi.TracerProvider(options.TracerProvider)),
			canonicalscopec.WithMiddleware(middlewares...),
			canonicalscopec.WithErrorHandler(httpapi.ErrorHandler(mapApplicationError)),
		}
		if options.MeterProvider != nil {
			canonicalServerOptions = append(canonicalServerOptions, canonicalscopec.WithMeterProvider(options.MeterProvider))
		}
		canonicalGenerated, err = canonicalscopec.NewServer(
			options.canonicalScopes,
			canonicalScopeSecurity{},
			canonicalServerOptions...,
		)
		if err != nil {
			return nil, err
		}
	}
	var sourceGenerated *canonicalsource.Server
	if options.canonicalSources != nil {
		sourceOptions := []canonicalsource.ServerOption{
			canonicalsource.WithTracerProvider(httpapi.TracerProvider(options.TracerProvider)),
			canonicalsource.WithMiddleware(middlewares...),
			canonicalsource.WithErrorHandler(httpapi.ErrorHandler(mapApplicationError)),
		}
		if options.MeterProvider != nil {
			sourceOptions = append(sourceOptions, canonicalsource.WithMeterProvider(options.MeterProvider))
		}
		sourceGenerated, err = canonicalsource.NewServer(options.canonicalSources, canonicalSourceSecurity{}, sourceOptions...)
		if err != nil {
			return nil, err
		}
	}
	mcpPath := ""
	var artifactGenerated *canonicalartifact.Server
	if options.canonicalArtifacts != nil {
		artifactOptions := []canonicalartifact.ServerOption{
			canonicalartifact.WithTracerProvider(httpapi.TracerProvider(options.TracerProvider)),
			canonicalartifact.WithMiddleware(middlewares...),
			canonicalartifact.WithErrorHandler(httpapi.ErrorHandler(mapApplicationError)),
		}
		if options.MeterProvider != nil {
			artifactOptions = append(artifactOptions, canonicalartifact.WithMeterProvider(options.MeterProvider))
		}
		artifactGenerated, err = canonicalartifact.NewServer(options.canonicalArtifacts, canonicalArtifactSecurity{}, artifactOptions...)
		if err != nil {
			return nil, err
		}
	}
	var managedSkillGenerated *managedskills.Server
	if options.canonicalSkills != nil {
		managedSkillOptions := []managedskills.ServerOption{
			managedskills.WithTracerProvider(httpapi.TracerProvider(options.TracerProvider)),
			managedskills.WithMiddleware(middlewares...),
			managedskills.WithErrorHandler(httpapi.ErrorHandler(mapApplicationError)),
		}
		if options.MeterProvider != nil {
			managedSkillOptions = append(managedSkillOptions, managedskills.WithMeterProvider(options.MeterProvider))
		}
		managedSkillGenerated, err = managedskills.NewServer(options.canonicalSkills, canonicalManagedSkillSecurity{}, managedSkillOptions...)
		if err != nil {
			return nil, err
		}
	}
	if options.MCP.Enabled {
		mcpPath, err = normalizeMCPPath(options.MCP.Path)
		if err != nil {
			return nil, err
		}
	}
	var openAPI http.Handler = generated
	if canonicalGenerated != nil {
		openAPI = canonicalScopeSidecar{legacy: generated, canonical: canonicalGenerated}
	}
	if sourceGenerated != nil {
		openAPI = canonicalSourceSidecar{next: openAPI, sources: sourceGenerated}
	}
	if artifactGenerated != nil {
		openAPI = canonicalArtifactSidecar{next: openAPI, artifacts: artifactGenerated}
	}
	if managedSkillGenerated != nil {
		openAPI = canonicalManagedSkillSidecar{next: openAPI, skills: managedSkillGenerated}
	}
	validatedOpenAPI := httpapi.ValidateJSONUnicode(openAPI)
	var application http.Handler = validatedOpenAPI
	var mux *http.ServeMux
	if options.MCP.Enabled || options.metrics != nil || options.webUI != nil {
		mux = http.NewServeMux()
		mux.Handle("/", validatedOpenAPI)
		application = mux
	}
	if options.webUI != nil {
		if err := webui.Mount(mux, *options.webUI); err != nil {
			serverlogging.LogSafely(context.Background(), applicationLogger, slog.LevelWarn,
				"PowerContext Web UI failed to start",
				slog.String("event", "web_ui.start_failed"), slog.String("unit", "web_ui"),
			)
		}
	}
	if options.metrics != nil {
		mux.Handle("/metrics", options.metrics.Handler())
	}
	if options.MCP.Enabled {
		receiving := []mcp.Middleware{requesttrace.MCPMiddleware(options.TracerProvider)}
		if options.AccessLog && accessLogger != nil {
			receiving = append(receiving, mcpapi.AccessLogMiddleware(accessLogger))
		}
		mcpServer, mcpErr := mcpapi.NewServer(handler, mcpapi.Options{
			Version:              options.MCP.Version,
			HandoffReportEnabled: options.HandoffReportRoutes,
			ScopeBindings:        options.scopeBindings,
			ApplicationObserver:  options.metrics,
			ApplicationLogger:    applicationLogger,
			TracerProvider:       options.TracerProvider,
			ReceivingMiddleware:  receiving,
		})
		if mcpErr != nil {
			return nil, mcpErr
		}
		if options.metrics != nil {
			mcpServer.AddReceivingMiddleware(options.metrics.MCPMiddleware())
		}
		mcpHandler := mcpapi.NewHTTPHandler(mcpServer, mcpapi.HTTPOptions{
			Stateless: options.MCP.Stateless, JSONResponse: options.MCP.JSONResponse,
			SessionTimeout: options.MCP.SessionTimeout, Logger: options.MCP.Logger,
		})
		mux.Handle(mcpPath+"/", http.StripPrefix(mcpPath, mcpHandler))
		mux.Handle(mcpPath, http.RedirectHandler(mcpPath+"/", http.StatusTemporaryRedirect))
	}
	access := newAccessLogOptions(
		options,
		accessLogger,
		mcpPath,
		generated,
		canonicalGenerated,
		sourceGenerated,
		artifactGenerated,
		managedSkillGenerated,
	)
	return httpapi.Wrap(application, httpapi.Options{
		BearerToken: options.BearerToken, HandoffReportRoutes: options.HandoffReportRoutes,
		Access: access,
	})
}

func newAccessLogOptions(
	options HTTPOptions,
	accessLogger *slog.Logger,
	mcpPath string,
	generated *v1.Server,
	canonicalGenerated *canonicalscopec.Server,
	sourceGenerated *canonicalsource.Server,
	artifactGenerated *canonicalartifact.Server,
	managedSkillGenerated *managedskills.Server,
) *httpapi.AccessLogOptions {
	if !options.AccessLog || accessLogger == nil {
		return nil
	}
	return &httpapi.AccessLogOptions{
		Logger: accessLogger,
		ResolveOperation: func(request *http.Request) string {
			if managedSkillGenerated != nil {
				if route, found := managedSkillGenerated.FindPath(request.Method, request.URL); found {
					return route.OperationID()
				}
			}
			if artifactGenerated != nil {
				if route, found := artifactGenerated.FindPath(request.Method, request.URL); found {
					return route.OperationID()
				}
			}
			if sourceGenerated != nil {
				if route, found := sourceGenerated.FindPath(request.Method, request.URL); found {
					return route.OperationID()
				}
			}
			if !options.HandoffReportRoutes && strings.HasPrefix(request.URL.Path, "/v1/handoff-reports/") {
				return "unmatched"
			}
			if canonicalGenerated != nil {
				if route, found := canonicalGenerated.FindPath(request.Method, request.URL); found {
					return route.OperationID()
				}
			}
			route, found := generated.FindPath(request.Method, request.URL)
			if !found {
				return "unmatched"
			}
			return route.OperationID()
		},
		Skip: func(request *http.Request) bool {
			for _, prefix := range []string{"/health/live", "/health/ready", "/metrics"} {
				if strings.HasPrefix(request.URL.Path, prefix) {
					return true
				}
			}
			return mcpPath != "" && strings.HasPrefix(request.URL.Path, mcpPath)
		},
	}
}

// canonicalScopeSecurity is intentionally permissive: httpapi.Wrap owns the
// process-wide bearer boundary and runs before this sidecar's generated server.
type canonicalScopeSecurity struct{}

func (canonicalScopeSecurity) HandleBearerAuth(
	ctx context.Context,
	_ canonicalscopec.OperationName,
	_ canonicalscopec.BearerAuth,
) (context.Context, error) {
	return ctx, nil
}

// canonicalManagedSkillSecurity is intentionally permissive: httpapi.Wrap
// enforces the process-wide bearer boundary before generated sidecars run.
type canonicalManagedSkillSecurity struct{}

func (canonicalManagedSkillSecurity) HandleBearerAuth(
	ctx context.Context,
	_ managedskills.OperationName,
	_ managedskills.BearerAuth,
) (context.Context, error) {
	return ctx, nil
}

// canonicalScopeSidecar reserves only exact generated Scope operation paths.
// All other method/path pairs remain on the frozen legacy handler.
type canonicalScopeSidecar struct {
	legacy    http.Handler
	canonical *canonicalscopec.Server
}

// canonicalManagedSkillSidecar reserves only exact generated managed-Skill
// operation paths. All other method/path pairs stay on preceding handlers.
type canonicalManagedSkillSidecar struct {
	next   http.Handler
	skills *managedskills.Server
}

func (handler canonicalManagedSkillSidecar) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	if _, found := handler.skills.FindPath(request.Method, request.URL); found {
		handler.skills.ServeHTTP(w, request)
		return
	}
	handler.next.ServeHTTP(w, request)
}

func (handler canonicalScopeSidecar) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	if _, found := handler.canonical.FindPath(request.Method, request.URL); found {
		handler.canonical.ServeHTTP(w, request)
		return
	}
	handler.legacy.ServeHTTP(w, request)
}

func normalizeMCPPath(value string) (string, error) {
	if value == "" {
		return DefaultMCPPath, nil
	}
	if !strings.HasPrefix(value, "/") || strings.ContainsAny(value, "?#") {
		return "", errors.New("server: MCP path must be an absolute URL path")
	}
	cleaned := path.Clean(value)
	if cleaned == "/" || cleaned == "." {
		return "", errors.New("server: MCP path must not replace the server root")
	}
	return cleaned, nil
}
